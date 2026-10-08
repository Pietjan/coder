// Command coder is the agent daemon: a web UI over a local coding agent
// that talks to any OpenAI-compatible model server.
//
//	coder serve --listen 127.0.0.1:8090 --llm http://127.0.0.1:8080/v1
//	coder ask --llm http://127.0.0.1:8080/v1 --project . "what does this repo do?"
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/pietjan/shuttle"

	"github.com/pietjan/coder/assets"
	"github.com/pietjan/coder/internal/agent"
	"github.com/pietjan/coder/internal/hub"
	"github.com/pietjan/coder/internal/llm"
	"github.com/pietjan/coder/internal/session"
	"github.com/pietjan/coder/internal/tool"
	"github.com/pietjan/coder/internal/ui"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var err error
	switch os.Args[1] {
	case "serve":
		err = runServe(ctx, os.Args[2:])
	case "ask":
		err = runAsk(ctx, os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "coder:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: coder <command> [flags]

  serve   run the web UI and the agent
  ask     run one turn from the command line

`)
}

// llmFlags are what every command needs to reach the model.
type llmFlags struct {
	base, key, model *string
}

func addLLMFlags(flags *flag.FlagSet) llmFlags {
	return llmFlags{
		base:  flags.String("llm", envOr("CODER_LLM", "http://127.0.0.1:8080/v1"), "OpenAI-compatible base URL"),
		key:   flags.String("api-key", os.Getenv("CODER_API_KEY"), "bearer token for the model server"),
		model: flags.String("model", os.Getenv("CODER_MODEL"), "model id; empty takes the first the server lists"),
	}
}

func (f llmFlags) client(ctx context.Context) (*llm.Client, string, error) {
	c := llm.New(*f.base, *f.key)
	model := *f.model
	if model == "" {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		id, err := c.FirstModel(ctx)
		if err != nil {
			return nil, "", fmt.Errorf("asking %s for its models: %w", c.BaseURL, err)
		}
		model = id
	}
	return c, model, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func runServe(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	lf := addLLMFlags(flags)
	listen := flags.String("listen", "127.0.0.1:8090", "address to serve the UI on")
	sessions := flags.String("sessions", session.DefaultDir(), "directory of session files")
	project := flags.String("project", "", "default project directory for new sessions (default: the working directory)")
	debug := flags.Bool("debug", false, "log every transport request and warn about duplicate ids")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *project == "" {
		wd, err := os.Getwd()
		if err != nil {
			return err
		}
		*project = wd
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	client, model, err := lf.client(ctx)
	if err != nil {
		return err
	}
	store, err := session.Open(*sessions)
	if err != nil {
		return err
	}
	h := hub.New(store, client, model, logger)
	defer h.Close()

	sh := shuttle.New(ui.NewApp(h, *project))
	sh.Prefix = ui.Prefix
	sh.Subtree = true
	sh.Title = "coder"
	sh.ScriptURL = "/static/datastar.js"
	sh.Shell = ui.Shell
	sh.Head = assets.StylesheetLink()
	sh.Logger = logger
	sh.Debug = *debug

	static, err := fs.Sub(assets.FS, "static")
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.Handle(ui.Prefix+"/", sh)
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, ui.Prefix+"/", http.StatusFound)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})

	srv := &http.Server{
		Addr:              *listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// no WriteTimeout: the page's stream stays open for as long as it is up
	}
	errc := make(chan error, 1)
	go func() {
		logger.Info("serving", "ui", "http://"+*listen+ui.Prefix+"/", "llm", client.BaseURL, "model", model, "project", *project)
		errc <- srv.ListenAndServe()
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	logger.Info("shutting down")
	shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = sh.Shutdown(shut)
	if err := srv.Shutdown(shut); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// runAsk runs one turn and prints what happens. Approvals are printed and
// denied: it is a smoke test of the loop, not a way around the gate.
func runAsk(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("ask", flag.ContinueOnError)
	lf := addLLMFlags(flags)
	project := flags.String("project", ".", "project directory")
	yes := flags.Bool("yes", false, "approve every tool call instead of denying it")
	if err := flags.Parse(args); err != nil {
		return err
	}
	text := strings.TrimSpace(strings.Join(flags.Args(), " "))
	if text == "" {
		return errors.New("ask: give the prompt as the arguments")
	}
	client, model, err := lf.client(ctx)
	if err != nil {
		return err
	}
	conv := agent.New(agent.Config{
		Client:     client,
		Model:      model,
		Tools:      tool.NewRegistry(tool.Project(*project)...),
		ProjectDir: *project,
	})
	defer conv.Close()
	if err := conv.Send(text); err != nil {
		return err
	}
	var shown int
	for {
		select {
		case <-ctx.Done():
			conv.Interrupt()
			return ctx.Err()
		case e := <-conv.Events():
			switch e.Kind {
			case agent.KindAssistantDelta:
				if len(e.Text) > shown {
					fmt.Print(e.Text[shown:])
					shown = len(e.Text)
				}
			case agent.KindAssistant:
				if len(e.Text) > shown {
					fmt.Print(e.Text[shown:])
				}
				if e.Text != "" {
					fmt.Println()
				}
				shown = 0
			case agent.KindToolCall:
				fmt.Fprintf(os.Stderr, "→ %s %s\n", e.Tool, e.Input)
			case agent.KindToolResult:
				out := e.Output
				if len(out) > 400 {
					out = out[:400] + "…"
				}
				fmt.Fprintf(os.Stderr, "← %s\n", out)
			case agent.KindApprovalRequested:
				d := agent.Deny
				if *yes {
					d = agent.Allow
				}
				fmt.Fprintf(os.Stderr, "? %s %s: %s\n", e.Tool, e.Input, d)
				_ = conv.Respond(e.CallID, d)
			case agent.KindError:
				fmt.Fprintln(os.Stderr, "error:", e.Text)
			case agent.KindTurnDone:
				if e.Usage != nil {
					fmt.Fprintf(os.Stderr, "[%d prompt + %d completion tokens in %s]\n", e.Usage.PromptTokens, e.Usage.CompletionTokens, e.Duration.Round(time.Millisecond))
				}
				if e.IsError {
					return errors.New("the turn failed")
				}
				return nil
			}
		}
	}
}
