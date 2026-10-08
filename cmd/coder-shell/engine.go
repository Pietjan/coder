package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/pietjan/colibri-go/container"
	"github.com/pietjan/colibri-go/experts"
	"github.com/pietjan/colibri-go/gpu"
	"github.com/pietjan/colibri-go/model"
	"github.com/pietjan/colibri-go/server"
	"github.com/pietjan/colibri-go/tokenizer"
)

// Engine is the model, loaded and served in-process.
type Engine struct {
	M     *model.Model
	Tok   *tokenizer.Tokenizer
	Name  string
	usage string
	cache *experts.Cache
	log   io.Writer
	seed  uint64
}

// Report is called as loading progresses: the stage, and done of total
// when the stage can count (0 of 0 when it cannot).
type Report func(stage string, done, total int64)

// OpenEngine loads the model the way `coli serve` does for an explicit
// --budget and --vram, with `--preload auto --gpu-dense --gpu-prefill`
// and the other defaults. Copied from colibri-go/cmd/coli/run.go, less
// the automatic sizing (which needs colibri's internal/vk).
func OpenEngine(cfg Config, logw io.Writer, report Report) (*Engine, error) {
	dense, err := model.ParseDenseMode(cfg.Dense)
	if err != nil {
		return nil, err
	}
	report("reading the tokenizer", 0, 0)
	tok, err := tokenizer.Load(filepath.Join(cfg.Model, "tokenizer.json"))
	if err != nil {
		return nil, err
	}
	c, err := container.LoadConfig(cfg.Model)
	if err != nil {
		return nil, err
	}

	ex := experts.Config{Warmup: 5, Wide: 1, Conf: 0.92, Prefetchers: 4, DirectIO: true}
	if ex.Budget, err = experts.ParseBytes(cfg.Budget); err != nil {
		return nil, fmt.Errorf("budget: %w", err)
	}

	kvHalf := dense == model.DenseI8
	attnCtx := 0
	if kvHalf {
		attnCtx = model.Context(c, cfg.Ctx)
	}
	var gcfg *gpu.Config
	var vram int64
	if cfg.VRAM != "" {
		if vram, err = experts.ParseBytes(cfg.VRAM); err != nil {
			return nil, fmt.Errorf("vram: %w", err)
		}
		gcfg = &gpu.Config{VRAM: vram, Uploads: cfg.GPUUploads, DenseVRAM: model.DenseBytes(c, attnCtx)}
	}

	report("loading the weights", 0, 0)
	t0 := time.Now()
	m, err := model.Open(cfg.Model, model.Options{
		Threads: cfg.Threads, Dense: dense, Ctx: cfg.Ctx, Experts: ex, KVHalf: kvHalf, GPU: gcfg, GPUDense: gcfg != nil,
	})
	if err != nil {
		return nil, err
	}
	logf(logw, "loaded %s (dense %s, ctx %d) in %.1fs\n", cfg.Model, cfg.Dense, m.Ctx(), time.Since(t0).Seconds())

	e := &Engine{M: m, Tok: tok, Name: filepath.Base(filepath.Clean(cfg.Model)), usage: cfg.UsageFile, log: logw, seed: uint64(time.Now().UnixNano())} //nolint:gosec // a seed
	if k, ok := m.Store().(*experts.Cache); ok {
		e.cache = k
		// --preload auto: warm the RAM cache in the background when it
		// holds every expert.
		if k.Cap() >= c.Experts {
			report("warming the expert cache", 0, 0)
			k.Warm(hottest(c, k.Cap(), cfg.UsageFile))
		}
	}
	if t := m.GPU(); t != nil {
		logf(logw, "gpu: %s, %.1f GiB of expert slots\n", t.DeviceName(), float64(vram)/(1<<30))
		if cfg.GPUPreload {
			per := int(vram / c.SlotBytes / int64(c.Layers))
			total := int64(min(per, c.Experts) * c.Layers)
			stop := make(chan struct{})
			go func() {
				tick := time.NewTicker(250 * time.Millisecond)
				defer tick.Stop()
				for {
					select {
					case <-stop:
						return
					case <-tick.C:
						report("preloading experts into VRAM", int64(t.Stats().Slots), total)
					}
				}
			}()
			t1 := time.Now()
			n, err := m.PreloadGPU(hottest(c, per, cfg.UsageFile))
			close(stop)
			if err != nil {
				logf(logw, "gpu preload: %v", err)
			}
			report("preloading experts into VRAM", total, total)
			logf(logw, "gpu: %d experts preloaded in %.1fs\n", n, time.Since(t1).Seconds())
		}
		m.StartStream() // --gpu-prefill
	}
	// After the GPU preload, whose reads are not routes to count.
	if e.cache != nil && cfg.UsageFile != "" {
		if err := e.cache.LoadUsage(cfg.UsageFile); err != nil && !errors.Is(err, os.ErrNotExist) {
			logf(logw, "usage file: %v", err)
		}
	}
	return e, nil
}

// logf writes one line to the log; the log is best effort.
func logf(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, strings.TrimSuffix(format, "\n")+"\n", args...)
}

// hottest lists, per layer, the per experts to load first: the most
// routed by the usage file when there is one, else the lowest ids.
// Copied from coli.
func hottest(c *container.Config, per int, usage string) [][]int32 {
	per = min(per, c.Experts)
	plan := make([][]int32, c.Layers)
	var counts []uint32
	if usage != "" {
		if u, err := experts.ReadUsage(usage, c.Layers, c.Experts); err == nil {
			counts = u
		}
	}
	for layer := range plan {
		ids := make([]int32, c.Experts)
		for e := range ids {
			ids[e] = int32(e) //nolint:gosec // expert ids are small
		}
		if counts != nil {
			row := counts[layer*c.Experts : (layer+1)*c.Experts]
			slices.SortStableFunc(ids, func(a, b int32) int { return int(row[b]) - int(row[a]) })
		}
		plan[layer] = ids[:per]
	}
	return plan
}

// Handler serves the OpenAI chat API over the model.
func (e *Engine) Handler(maxTokens int) http.Handler {
	return server.New(server.Config{
		Model: e.M, Tokenizer: e.Tok, Name: e.Name, MaxTokens: maxTokens, Log: e.log, Conversations: 4,
		Sampling: func(thinking bool) model.Sampling {
			p := model.Recommended(thinking)
			p.Seed = e.seed
			return p
		},
	})
}

// Close writes the usage file and frees the model. It can take seconds.
func (e *Engine) Close() {
	if e.cache != nil && e.usage != "" {
		if err := e.cache.SaveUsage(e.usage); err != nil {
			logf(e.log, "usage file: %v", err)
		}
	}
	_ = e.M.Close()
}
