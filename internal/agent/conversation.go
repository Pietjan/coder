package agent

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/pietjan/coder/internal/llm"
	"github.com/pietjan/coder/internal/tool"
)

// Config sets a Conversation up.
type Config struct {
	Client     *llm.Client
	Model      string
	Tools      *tool.Registry
	ProjectDir string
	// MaxIterations caps the model calls in one turn; 0 is 40.
	MaxIterations int
	// MaxTokens caps one reply; 0 leaves it to the server.
	MaxTokens int
	Log       *slog.Logger
}

// Errors of Send and Respond.
var (
	ErrBusy           = errors.New("a turn is already running")
	ErrClosed         = errors.New("the conversation is closed")
	ErrUnknownRequest = errors.New("no such approval request")
)

// Conversation is one chat with the model: its history, the turn in
// flight, and the approvals it is waiting for.
type Conversation struct {
	cfg    Config
	system string
	events chan Event
	ctx    context.Context
	cancel context.CancelFunc

	turnMu     sync.Mutex // held for the whole turn
	busy       bool
	turnCancel context.CancelFunc
	stateMu    sync.Mutex // busy, turnCancel, decisions, allowed

	history   []llm.Message
	decisions map[string]chan Decision
	allowed   map[string]bool // tools approved for the session

	wg sync.WaitGroup
}

// New makes an idle conversation with just the system prompt.
func New(cfg Config) *Conversation {
	if cfg.MaxIterations <= 0 {
		cfg.MaxIterations = 40
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &Conversation{
		cfg:       cfg,
		system:    System(cfg.ProjectDir),
		events:    make(chan Event, 256),
		ctx:       ctx,
		cancel:    cancel,
		decisions: map[string]chan Decision{},
		allowed:   map[string]bool{},
	}
	c.history = []llm.Message{{Role: llm.RoleSystem, Content: c.system}}
	return c
}

// Restore rebuilds the history and the activated tools from a session's
// events. Call it before the first Send.
func (c *Conversation) Restore(events []Event) {
	history, activated := Rebuild(c.system, events)
	c.history = history
	c.cfg.Tools.Activate(activated...)
}

// Events delivers everything that happens, in order. The channel closes
// after Close.
func (c *Conversation) Events() <-chan Event { return c.events }

// Busy reports whether a turn is running.
func (c *Conversation) Busy() bool {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.busy
}

// Send starts a turn with the user's text. It returns ErrBusy while a
// turn runs; the turn itself runs on its own goroutine.
func (c *Conversation) Send(text string) error {
	if c.ctx.Err() != nil {
		return ErrClosed
	}
	c.stateMu.Lock()
	if c.busy {
		c.stateMu.Unlock()
		return ErrBusy
	}
	c.busy = true
	c.stateMu.Unlock()

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.turnMu.Lock()
		defer c.turnMu.Unlock()
		defer func() {
			c.stateMu.Lock()
			c.busy = false
			c.turnCancel = nil
			c.stateMu.Unlock()
		}()
		c.runTurn(text)
	}()
	return nil
}

// Respond answers an approval request.
func (c *Conversation) Respond(callID string, d Decision) error {
	c.stateMu.Lock()
	ch, ok := c.decisions[callID]
	if ok {
		delete(c.decisions, callID)
	}
	c.stateMu.Unlock()
	if !ok {
		return ErrUnknownRequest
	}
	ch <- d
	return nil
}

// Pending lists the approval requests waiting for an answer.
func (c *Conversation) Pending() []string {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	out := make([]string, 0, len(c.decisions))
	for id := range c.decisions {
		out = append(out, id)
	}
	return out
}

// Interrupt stops the running turn, if any.
func (c *Conversation) Interrupt() {
	c.stateMu.Lock()
	cancel := c.turnCancel
	c.stateMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Close interrupts the turn, waits for it, and closes Events.
func (c *Conversation) Close() {
	c.cancel()
	c.Interrupt()
	c.wg.Wait()
	close(c.events)
}

// History is a copy of the messages the next request would send.
func (c *Conversation) History() []llm.Message {
	c.turnMu.Lock()
	defer c.turnMu.Unlock()
	return append([]llm.Message(nil), c.history...)
}

func (c *Conversation) emit(e Event) {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	select {
	case c.events <- e:
	case <-c.ctx.Done():
	}
}
