// Command coder-shell is the Windows desktop app: a WebView2 window, the
// KAT coder model served in-process on localhost, and the coder daemon
// started inside WSL and shown in the window.
//
// Built from WSL with `make build/windows`; configured by
// %APPDATA%\coder\config.json (written with defaults on the first run)
// and the flags in config.go.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/pietjan/spectacle"

	"github.com/pietjan/coder/assets"
)

func main() {
	runtime.LockOSThread()
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "coder-shell:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := LoadConfig(os.Args[1:])
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dataDir(), 0o750); err != nil {
		return err
	}
	logFile, err := os.OpenFile(filepath.Join(dataDir(), "shell.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640) //nolint:gosec // our own log
	if err != nil {
		return err
	}
	defer func() { _ = logFile.Close() }()
	logw := &tailWriter{w: io.MultiWriter(logFile, os.Stderr)}
	logf(logw, "--- coder-shell %s, config %s", time.Now().Format(time.RFC3339), cfg.path)

	b, err := spectacle.New(spectacle.Config{
		ID:      "coder",
		Name:    "Coder",
		Icon:    assets.Icon,
		DataDir: filepath.Join(dataDir(), "webview"),
		Debug:   cfg.Debug,
	})
	if err != nil {
		return err
	}
	win, err := b.NewWindow("Coder", spectacle.Rect{X: 100, Y: 100, W: 1400, H: 900})
	if err != nil {
		return err
	}
	view, err := b.NewWebView(win, "")
	if err != nil {
		return err
	}
	win.OnResize(func(w, h, _ int) { view.SetBounds(spectacle.Rect{W: w, H: h}) })
	view.NavigateHTML(loadingHTML)
	win.Show()

	ctx, cancel := context.WithCancel(context.Background())
	boot := &boot{cfg: cfg, b: b, view: view, logw: logw}
	view.OnMessage(func(msg string) {
		if strings.Contains(msg, `"quit"`) {
			b.Quit()
		}
	})
	go boot.run(ctx)

	win.OnCloseRequest(func() bool {
		// Keep the window until the model has written its usage file and
		// the daemon is gone; Quit ends the loop afterwards.
		cancel()
		win.Hide()
		go func() {
			boot.shutdown()
			b.Dispatch(b.Quit)
		}()
		return false
	})
	return b.Run()
}

// boot takes the app from the loading page to the UI, and back down.
type boot struct {
	cfg  Config
	b    spectacle.Backend
	view spectacle.WebView
	logw *tailWriter

	mu     sync.Mutex
	eng    *Engine
	srv    *http.Server
	daemon *Daemon
}

func (bt *boot) report(stage string, done, total int64) {
	p := progress{Stage: stage, Done: done, Total: total}.JSON()
	bt.b.Dispatch(func() { bt.view.PostJSON(p) })
}

func (bt *boot) line(format string, args ...any) {
	s := fmt.Sprintf(format, args...)
	logf(bt.logw, "%s", s)
	p := progress{Line: s}.JSON()
	bt.b.Dispatch(func() { bt.view.PostJSON(p) })
}

func (bt *boot) run(ctx context.Context) {
	if err := bt.start(ctx); err != nil {
		if ctx.Err() != nil {
			return
		}
		logf(bt.logw, "error: %v", err)
		page := errorHTML(err.Error(), bt.logw.Tail())
		bt.b.Dispatch(func() { bt.view.NavigateHTML(page) })
	}
}

func (bt *boot) start(ctx context.Context) error {
	cfg := bt.cfg
	bt.line("model %s (budget %s, vram %q)", cfg.Model, cfg.Budget, cfg.VRAM)
	eng, err := OpenEngine(cfg, bt.logw, bt.report)
	if err != nil {
		return fmt.Errorf("loading the model: %w", err)
	}
	bt.mu.Lock()
	bt.eng = eng
	bt.mu.Unlock()

	bt.report("serving the model", 0, 0)
	ln, err := net.Listen("tcp", cfg.LLMAddr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", cfg.LLMAddr, err)
	}
	srv := &http.Server{Handler: eng.Handler(cfg.MaxTokens), ReadHeaderTimeout: 10 * time.Second}
	bt.mu.Lock()
	bt.srv = srv
	bt.mu.Unlock()
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logf(bt.logw, "model server: %v", err)
		}
	}()
	if err := waitUp(ctx, "http://"+cfg.LLMAddr+"/health", 30*time.Second, nil); err != nil {
		return err
	}
	bt.line("model %s on http://%s/v1", eng.Name, cfg.LLMAddr)

	bt.report("starting coder in "+cfg.Distro, 0, 0)
	d, err := StartDaemon(ctx, cfg, bt.logw)
	if err != nil {
		return err
	}
	bt.mu.Lock()
	bt.daemon = d
	bt.mu.Unlock()
	if err := d.WaitUp(ctx, 60*time.Second); err != nil {
		return err
	}
	url := "http://" + cfg.UIAddr + "/s/"
	bt.line("ui on %s", url)
	bt.b.Dispatch(func() { bt.view.Navigate(url) })
	return nil
}

// shutdown stops the daemon, the model server and the model.
func (bt *boot) shutdown() {
	bt.mu.Lock()
	eng, srv, d := bt.eng, bt.srv, bt.daemon
	bt.mu.Unlock()
	if d != nil {
		d.Stop()
	}
	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = srv.Shutdown(ctx)
		cancel()
	}
	if eng != nil {
		eng.Close()
	}
	logf(bt.logw, "--- shut down")
}

// tailWriter writes through and remembers the last few KiB, for the
// error page.
type tailWriter struct {
	mu  sync.Mutex
	w   io.Writer
	buf bytes.Buffer
}

func (t *tailWriter) Write(p []byte) (int, error) {
	t.mu.Lock()
	t.buf.Write(p)
	if t.buf.Len() > 8<<10 {
		b := t.buf.Bytes()
		t.buf = *bytes.NewBuffer(append([]byte(nil), b[len(b)-(8<<10):]...))
	}
	t.mu.Unlock()
	return t.w.Write(p)
}

func (t *tailWriter) Tail() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.buf.String()
}
