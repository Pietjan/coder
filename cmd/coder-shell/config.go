package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// Config is everything the shell needs: where the model is and how to run
// it, and how to reach the daemon in WSL. It lives in a JSON file under
// the user's config directory; every field is also a flag, and flags win.
type Config struct {
	// The model, as `coli serve` would take it.
	Model      string `json:"model"`       // container directory, e.g. C:\Users\me\Downloads\kat-coder-v2.5-colibri-i4-gs64
	Budget     string `json:"budget"`      // process memory to plan for, e.g. 24G
	VRAM       string `json:"vram"`        // VRAM for expert slots, e.g. 3G; empty is CPU only
	GPUPreload bool   `json:"gpu_preload"` // fill VRAM with the hottest experts at start
	GPUUploads int    `json:"gpu_uploads"` // expert slots promoted to VRAM per token at most
	UsageFile  string `json:"usage_file"`  // expert routing counts, read at start and written at exit
	Ctx        int    `json:"ctx"`         // context length; 0 is the model's
	Threads    int    `json:"threads"`     // 0 is every CPU
	Dense      string `json:"dense"`       // i8 or f16
	MaxTokens  int    `json:"max_tokens"`  // reply length limit where a request gives none

	// The daemon.
	Distro    string `json:"distro"`     // WSL distribution
	CoderPath string `json:"coder_path"` // Linux path of the coder binary
	Project   string `json:"project"`    // Linux path of the default project directory
	LLMAddr   string `json:"llm_addr"`   // where the shell serves the model
	UIAddr    string `json:"ui_addr"`    // where the daemon serves the UI
	Debug     bool   `json:"debug"`      // WebView2 DevTools

	// Where the file came from, for messages.
	path string
}

// DefaultConfig is what a missing file means.
func DefaultConfig() Config {
	return Config{
		Model:      `C:\Users\Pietjan\Downloads\kat-coder-v2.5-colibri-i4-gs64`,
		Budget:     "24G",
		VRAM:       "3G",
		GPUPreload: true,
		GPUUploads: 16,
		UsageFile:  filepath.Join(dataDir(), "usage.bin"),
		Dense:      "i8",
		MaxTokens:  8192,
		Distro:     "Ubuntu-24.04",
		CoderPath:  "/home/pietjan/src/github.com/pietjan/coder/bin/coder",
		Project:    "/home/pietjan/src",
		LLMAddr:    "127.0.0.1:8089",
		UIAddr:     "127.0.0.1:8090",
	}
}

// dataDir is where the shell keeps its state: the log, the usage file,
// the webview profile. %LOCALAPPDATA%\coder on Windows.
func dataDir() string {
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	return filepath.Join(base, "coder")
}

// configPath is %APPDATA%\coder\config.json on Windows.
func configPath() string {
	base, err := os.UserConfigDir()
	if err != nil {
		return filepath.Join(dataDir(), "config.json")
	}
	return filepath.Join(base, "coder", "config.json")
}

// LoadConfig reads the file named by -config (or the default path) and
// applies the flags over it. A missing file is the defaults; on the first
// run it is written out so there is something to edit.
func LoadConfig(args []string) (Config, error) {
	fs := flag.NewFlagSet("coder-shell", flag.ContinueOnError)
	path := fs.String("config", configPath(), "config file")
	// Parse once to learn the path, then read the file, then parse again
	// so the flags override what it says.
	cfg := DefaultConfig()
	fs.SetOutput(os.Stderr)
	_ = fs.Parse(args)
	cfg.path = *path
	data, err := os.ReadFile(cfg.path) //nolint:gosec // the user's own config path
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &cfg); err != nil {
			return cfg, fmt.Errorf("%s: %w", cfg.path, err)
		}
	case errors.Is(err, os.ErrNotExist):
		if err := writeConfig(cfg); err != nil {
			return cfg, err
		}
	default:
		return cfg, err
	}

	fs = flag.NewFlagSet("coder-shell", flag.ContinueOnError)
	fs.String("config", cfg.path, "config file")
	fs.StringVar(&cfg.Model, "model", cfg.Model, "model container directory")
	fs.StringVar(&cfg.Budget, "budget", cfg.Budget, "process memory to plan for, e.g. 24G")
	fs.StringVar(&cfg.VRAM, "vram", cfg.VRAM, "VRAM for expert slots, e.g. 3G; empty is CPU only")
	fs.BoolVar(&cfg.GPUPreload, "gpu-preload", cfg.GPUPreload, "fill VRAM with the hottest experts at start")
	fs.IntVar(&cfg.GPUUploads, "gpu-uploads", cfg.GPUUploads, "expert slots promoted to VRAM per token at most")
	fs.StringVar(&cfg.UsageFile, "usage-file", cfg.UsageFile, "expert routing counts file")
	fs.IntVar(&cfg.Ctx, "ctx", cfg.Ctx, "context length; 0 is the model's")
	fs.IntVar(&cfg.Threads, "threads", cfg.Threads, "worker threads; 0 is every CPU")
	fs.StringVar(&cfg.Dense, "dense", cfg.Dense, "dense weights: i8 or f16")
	fs.IntVar(&cfg.MaxTokens, "max-tokens", cfg.MaxTokens, "reply length limit where a request gives none")
	fs.StringVar(&cfg.Distro, "distro", cfg.Distro, "WSL distribution")
	fs.StringVar(&cfg.CoderPath, "coder", cfg.CoderPath, "Linux path of the coder binary")
	fs.StringVar(&cfg.Project, "project", cfg.Project, "Linux path of the default project directory")
	fs.StringVar(&cfg.LLMAddr, "llm-addr", cfg.LLMAddr, "address the shell serves the model on")
	fs.StringVar(&cfg.UIAddr, "ui-addr", cfg.UIAddr, "address the daemon serves the UI on")
	fs.BoolVar(&cfg.Debug, "debug", cfg.Debug, "open the WebView2 DevTools")
	if err := fs.Parse(args); err != nil {
		return cfg, err
	}
	return cfg, cfg.check()
}

func (c Config) check() error {
	switch {
	case c.Model == "":
		return errors.New("config: model is required")
	case c.Budget == "":
		return errors.New("config: budget is required (the shell does not size it automatically)")
	case c.Distro == "" || c.CoderPath == "":
		return errors.New("config: distro and coder_path are required")
	case runtime.GOOS == "windows" && !filepath.IsAbs(c.Model):
		return fmt.Errorf("config: model %q must be an absolute Windows path", c.Model)
	}
	return nil
}

func writeConfig(cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(cfg.path), 0o750); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(cfg.path, append(data, '\n'), 0o640) //nolint:gosec // the user's own config
}
