// Package hub owns the live conversations: it opens sessions, keeps each
// one's agent running, writes its events to the session file and fans
// them out to whoever is watching (a UI page, a CLI).
package hub

import (
	"fmt"
	"log/slog"
	"sync"

	"github.com/pietjan/coder/internal/agent"
	"github.com/pietjan/coder/internal/llm"
	"github.com/pietjan/coder/internal/session"
	"github.com/pietjan/coder/internal/tool"
)

// Hub holds the store and the sessions in memory.
type Hub struct {
	store  *session.Store
	client *llm.Client
	model  string
	log    *slog.Logger

	mu   sync.Mutex
	live map[string]*Live
	subs map[chan session.Meta]struct{} // sessions-list watchers
}

// New makes a hub over store, talking to client as model.
func New(store *session.Store, client *llm.Client, model string, log *slog.Logger) *Hub {
	if log == nil {
		log = slog.Default()
	}
	return &Hub{store: store, client: client, model: model, log: log, live: map[string]*Live{}, subs: map[chan session.Meta]struct{}{}}
}

// Model is the model id in use.
func (h *Hub) Model() string { return h.model }

// List is the sessions on disk, newest first.
func (h *Hub) List() ([]session.Meta, error) { return h.store.List() }

// WatchSessions delivers the Meta of every session created from now on.
func (h *Hub) WatchSessions() (<-chan session.Meta, func()) {
	ch := make(chan session.Meta, 8)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs, ch)
		h.mu.Unlock()
	}
}

// Create starts a new session over project.
func (h *Hub) Create(project string) (*Live, error) {
	w, meta, err := h.store.Create(project, h.model)
	if err != nil {
		return nil, err
	}
	l := h.start(meta, w, nil)
	h.mu.Lock()
	for ch := range h.subs {
		select {
		case ch <- meta:
		default:
		}
	}
	h.mu.Unlock()
	return l, nil
}

// Open returns the live session, resuming it from disk the first time.
func (h *Hub) Open(id string) (*Live, error) {
	h.mu.Lock()
	if l, ok := h.live[id]; ok {
		h.mu.Unlock()
		return l, nil
	}
	h.mu.Unlock()

	w, events, err := h.store.Open(id)
	if err != nil {
		return nil, err
	}
	hdr := events[0]
	meta := session.Meta{ID: hdr.ID, Project: hdr.Project, Model: hdr.Model, Created: hdr.At}
	for _, e := range events {
		if e.Kind == agent.KindUser {
			meta.Title = e.Text
			break
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if l, ok := h.live[id]; ok { // raced with another Open
		_ = w.Close()
		return l, nil
	}
	return h.startLocked(meta, w, events), nil
}

func (h *Hub) start(meta session.Meta, w *session.Writer, events []agent.Event) *Live {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.startLocked(meta, w, events)
}

func (h *Hub) startLocked(meta session.Meta, w *session.Writer, events []agent.Event) *Live {
	conv := agent.New(agent.Config{
		Client:     h.client,
		Model:      h.model,
		Tools:      tool.NewRegistry(tool.Project(meta.Project)...),
		ProjectDir: meta.Project,
		Log:        h.log.With("session", meta.ID),
	})
	if len(events) > 0 {
		conv.Restore(events)
	}
	l := &Live{Meta: meta, conv: conv, writer: w, replay: events, subs: map[chan agent.Event]struct{}{}, log: h.log.With("session", meta.ID)}
	h.live[meta.ID] = l
	go l.pump()
	return l
}

// Close stops every live session and closes its file.
func (h *Hub) Close() {
	h.mu.Lock()
	live := h.live
	h.live = map[string]*Live{}
	h.mu.Unlock()
	for _, l := range live {
		l.close()
	}
}

// Live is one running session.
type Live struct {
	Meta session.Meta

	conv   *agent.Conversation
	writer *session.Writer
	log    *slog.Logger

	mu     sync.Mutex
	replay []agent.Event // every persisted event so far
	subs   map[chan agent.Event]struct{}
}

// pump moves the agent's events to disk and to the subscribers.
func (l *Live) pump() {
	for e := range l.conv.Events() {
		if e.Persistent() {
			written, err := l.writer.Append(e)
			if err != nil {
				l.log.Error("session write", "err", err)
			} else {
				e = written
			}
		}
		l.mu.Lock()
		if e.Persistent() {
			l.replay = append(l.replay, e)
		}
		for ch := range l.subs {
			if e.Persistent() {
				ch <- e
			} else {
				select {
				case ch <- e:
				default: // a slow watcher misses a delta, never a fact
				}
			}
		}
		l.mu.Unlock()
	}
	l.mu.Lock()
	for ch := range l.subs {
		close(ch)
	}
	l.subs = nil
	l.mu.Unlock()
}

// Subscribe returns everything so far and a channel for what follows,
// with no gap between them. The channel closes when the session does.
func (l *Live) Subscribe() (replay []agent.Event, ch <-chan agent.Event, cancel func()) {
	c := make(chan agent.Event, 256)
	l.mu.Lock()
	defer l.mu.Unlock()
	replay = append([]agent.Event(nil), l.replay...)
	if l.subs == nil {
		close(c)
		return replay, c, func() {}
	}
	l.subs[c] = struct{}{}
	return replay, c, func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		if l.subs != nil {
			delete(l.subs, c)
		}
	}
}

// Send starts a turn.
func (l *Live) Send(text string) error { return l.conv.Send(text) }

// Respond answers an approval request.
func (l *Live) Respond(callID string, d agent.Decision) error { return l.conv.Respond(callID, d) }

// Interrupt stops the running turn.
func (l *Live) Interrupt() { l.conv.Interrupt() }

// Busy reports whether a turn is running.
func (l *Live) Busy() bool { return l.conv.Busy() }

// Pending lists the approval requests still waiting, as their events.
func (l *Live) Pending() []agent.Event {
	ids := map[string]bool{}
	for _, id := range l.conv.Pending() {
		ids[id] = true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []agent.Event
	for _, e := range l.replay {
		if e.Kind == agent.KindApprovalRequested && ids[e.CallID] {
			out = append(out, e)
		}
	}
	return out
}

func (l *Live) close() {
	l.conv.Close()
	if err := l.writer.Close(); err != nil {
		l.log.Error("session close", "err", err)
	}
}

// String names the session.
func (l *Live) String() string { return fmt.Sprintf("session %s (%s)", l.Meta.ID, l.Meta.Project) }
