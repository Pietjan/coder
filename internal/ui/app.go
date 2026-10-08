package ui

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/pietjan/shuttle"

	"github.com/pietjan/coder/internal/agent"
	"github.com/pietjan/coder/internal/hub"
	"github.com/pietjan/coder/internal/session"
)

// Prefix is where the app is mounted; session pages are Prefix/<id>.
const Prefix = "/s"

// streamName names the transcript stream; it ends up in element ids.
const streamName = "transcript"

// App is the whole page: the sessions sidebar, one session's transcript,
// the composer and the status line.
type App struct {
	shuttle.Base
	hub            *hub.Hub
	defaultProject string

	sessions    []session.Meta
	stopWatch   func()
	live        *hub.Live
	unsubscribe func()

	// painted is set by the first Render; before it, transcript items are
	// collected in inline and rendered with the page, after it they go
	// down the stream.
	painted bool
	inline  []templ.Component

	pending   map[string]agent.Event // approval requests waiting for a click
	toolCalls map[string]agent.Event // call id → its tool_call event, for the card
	askKeys   map[string]string      // call id → key of its approval line
	busy      bool
	tokPerSec float64
	lastErr   string
	hasCur    bool // the streaming bubble is in the page
}

// NewApp returns the component factory the shuttle handler wants.
func NewApp(h *hub.Hub, defaultProject string) func() shuttle.Component {
	return func() shuttle.Component {
		return &App{hub: h, defaultProject: defaultProject, pending: map[string]agent.Event{}, toolCalls: map[string]agent.Event{}, askKeys: map[string]string{}}
	}
}

// Signals is the client-held state: the draft and the project path.
func (a *App) Signals() map[string]any {
	return map[string]any{"draft": "", "project": a.defaultProject}
}

// Mount loads the sessions list and watches it.
func (a *App) Mount(context.Context, shuttle.Params) error {
	a.refreshSessions()
	ch, stop := a.hub.WatchSessions()
	a.stopWatch = stop
	go func() {
		for range ch {
			_ = a.Do(func(context.Context) error { a.refreshSessions(); return nil })
		}
	}()
	return nil
}

// Unmount stops watching.
func (a *App) Unmount(context.Context) {
	if a.stopWatch != nil {
		a.stopWatch()
	}
	if a.unsubscribe != nil {
		a.unsubscribe()
	}
}

// HandleParams picks the session from the path: /s/<id>, or the newest.
func (a *App) HandleParams(ctx context.Context, _ shuttle.Params) error {
	id := strings.Trim(strings.TrimPrefix(a.Path(), Prefix), "/")
	if id == "" && len(a.sessions) > 0 {
		id = a.sessions[0].ID
	}
	if id == "" {
		return nil
	}
	return a.switchTo(ctx, id)
}

func (a *App) refreshSessions() {
	list, err := a.hub.List()
	if err != nil {
		a.lastErr = err.Error()
		return
	}
	a.sessions = list
}

// switchTo makes id the shown session: replays its transcript, then
// follows it live.
func (a *App) switchTo(ctx context.Context, id string) error {
	if a.live != nil && a.live.Meta.ID == id {
		return nil
	}
	l, err := a.hub.Open(id)
	if err != nil {
		return err
	}
	if a.unsubscribe != nil {
		a.unsubscribe()
	}
	a.live = l
	a.pending = map[string]agent.Event{}
	a.toolCalls = map[string]agent.Event{}
	a.askKeys = map[string]string{}
	a.busy = l.Busy()
	a.tokPerSec = 0
	a.lastErr = ""
	a.hasCur = false
	a.inline = nil

	replay, ch, cancel := l.Subscribe()
	a.unsubscribe = cancel
	waiting := map[string]bool{}
	for _, e := range l.Pending() {
		waiting[e.CallID] = true
	}
	if a.painted {
		if err := a.Stream(streamName).Clear(ctx); err != nil {
			return err
		}
	}
	for _, e := range replay {
		if e.Kind == agent.KindApprovalRequested && !waiting[e.CallID] {
			e.Decision = "expired"
		}
		if err := a.apply(ctx, e); err != nil {
			return err
		}
	}
	go func() {
		for e := range ch {
			e := e
			if err := a.Do(func(ctx context.Context) error { return a.apply(ctx, e) }); err != nil {
				return // unmounted
			}
		}
	}()
	return nil
}

// apply folds one event into the page: the transcript item it adds or
// changes, and the state the frame shows. Replay and live events take the
// same path.
func (a *App) apply(ctx context.Context, e agent.Event) error {
	s := a.Stream(streamName)
	switch e.Kind {
	case agent.KindUser:
		a.busy = true
		a.lastErr = ""
		if err := a.add(ctx, s, keyUser(e), userItem(e)); err != nil {
			return err
		}
		return a.showCur(ctx, s, "", "")
	case agent.KindAssistantDelta:
		return a.showCur(ctx, s, e.Text, e.Reasoning)
	case agent.KindAssistant:
		if err := a.dropCur(ctx, s); err != nil {
			return err
		}
		if e.Text == "" && e.Reasoning == "" {
			return nil
		}
		return a.add(ctx, s, keyAssistant(e), assistantItem(e))
	case agent.KindToolCall:
		if err := a.dropCur(ctx, s); err != nil {
			return err
		}
		a.toolCalls[e.CallID] = e
		return a.add(ctx, s, keyTool(e), toolItem(e, nil))
	case agent.KindToolResult:
		call, ok := a.toolCalls[e.CallID]
		if !ok {
			return nil // a result whose call we never saw
		}
		if err := a.replace(ctx, s, keyTool(call), toolItem(call, &e)); err != nil {
			return err
		}
		return a.showCur(ctx, s, "", "")
	case agent.KindApprovalRequested:
		a.askKeys[e.CallID] = keyApproval(e)
		if e.Decision == "expired" {
			return a.add(ctx, s, keyApproval(e), approvalItem(e, true))
		}
		a.pending[e.CallID] = e
		return a.add(ctx, s, keyApproval(e), approvalItem(e, false))
	case agent.KindApprovalResolved:
		delete(a.pending, e.CallID)
		key, ok := a.askKeys[e.CallID]
		if !ok {
			return nil
		}
		return a.replace(ctx, s, key, approvalItem(e, true))
	case agent.KindError:
		a.lastErr = e.Text
		if err := a.dropCur(ctx, s); err != nil {
			return err
		}
		return a.add(ctx, s, keyError(e), errorItem(e))
	case agent.KindTurnDone:
		a.busy = false
		if e.Usage != nil && e.Duration > 0 {
			a.tokPerSec = float64(e.Usage.CompletionTokens) / e.Duration.Seconds()
		}
		return a.dropCur(ctx, s)
	}
	return nil
}

// add, replace and showCur route an item to the page: inline before the
// first paint, down the stream after it.
func (a *App) add(ctx context.Context, s *shuttle.Stream, key string, c templ.Component) error {
	if !a.painted {
		a.inline = append(a.inline, withID(s.ItemID(key), c))
		return nil
	}
	return s.Append(ctx, key, withID(s.ItemID(key), c))
}

func (a *App) replace(ctx context.Context, s *shuttle.Stream, key string, c templ.Component) error {
	if !a.painted {
		// before the first paint the item is still in inline; the simplest
		// correct thing is to append the newer version and drop the older
		id := s.ItemID(key)
		for i, item := range a.inline {
			if idOf(item) == id {
				a.inline[i] = withID(id, c)
				return nil
			}
		}
		a.inline = append(a.inline, withID(id, c))
		return nil
	}
	return s.Replace(ctx, key, withID(s.ItemID(key), c))
}

func (a *App) showCur(ctx context.Context, s *shuttle.Stream, text, reasoning string) error {
	if !a.painted {
		return nil // nothing streams into a page that is not there yet
	}
	if !a.hasCur {
		a.hasCur = true
		return s.Append(ctx, keyCur, withID(s.ItemID(keyCur), curItem(text, reasoning)))
	}
	return s.Replace(ctx, keyCur, withID(s.ItemID(keyCur), curItem(text, reasoning)))
}

func (a *App) dropCur(ctx context.Context, s *shuttle.Stream) error {
	if !a.hasCur {
		return nil
	}
	a.hasCur = false
	return s.Remove(ctx, keyCur)
}

// Actions

type draftSignals struct {
	Draft   string `json:"draft"`
	Project string `json:"project"`
}

func (a *App) send(ctx context.Context) error {
	var sig draftSignals
	if err := shuttle.DecodeSignals(ctx, &sig); err != nil {
		return err
	}
	text := strings.TrimSpace(sig.Draft)
	if text == "" {
		return nil
	}
	if a.live == nil {
		if err := a.create(ctx, sig.Project); err != nil {
			return err
		}
	}
	if err := a.live.Send(text); err != nil {
		if errors.Is(err, agent.ErrBusy) {
			a.lastErr = "the agent is still working; stop it or wait"
			return nil
		}
		return err
	}
	return a.PatchSignal("draft", "")
}

func (a *App) stop(context.Context) error {
	if a.live != nil {
		a.live.Interrupt()
	}
	return nil
}

func (a *App) approve(callID string, d agent.Decision) shuttle.Action {
	return func(context.Context) error {
		if a.live == nil {
			return nil
		}
		if err := a.live.Respond(callID, d); err != nil {
			a.lastErr = err.Error()
			delete(a.pending, callID)
		}
		return nil
	}
}

func (a *App) newSession(ctx context.Context) error {
	var sig draftSignals
	if err := shuttle.DecodeSignals(ctx, &sig); err != nil {
		return err
	}
	return a.create(ctx, sig.Project)
}

// create starts a session over project and navigates to it.
func (a *App) create(ctx context.Context, project string) error {
	project = strings.TrimSpace(project)
	if project == "" {
		project = a.defaultProject
	}
	l, err := a.hub.Create(project)
	if err != nil {
		a.lastErr = err.Error()
		return nil
	}
	a.refreshSessions()
	return a.Navigate(ctx, Prefix+"/"+l.Meta.ID)
}

func (a *App) open(id string) shuttle.Action {
	return func(ctx context.Context) error { return a.Navigate(ctx, Prefix+"/"+id) }
}

// Render draws the frame; the transcript's items ride the stream.
func (a *App) Render(ctx context.Context) templ.Component {
	inline := a.inline
	if a.painted {
		inline = nil
	}
	a.painted = true
	a.inline = nil
	return page(ctx, a, inline)
}

// RenderError keeps the page up when a render fails.
func (a *App) RenderError(_ context.Context, err error) templ.Component {
	return errorPage(err.Error())
}

// view helpers

func (a *App) status() string {
	parts := []string{a.hub.Model()}
	if a.live != nil {
		parts = append(parts, a.live.Meta.Project)
	}
	if a.tokPerSec > 0 {
		parts = append(parts, fmt.Sprintf("~%.1f tok/s", a.tokPerSec))
	}
	return strings.Join(parts, " · ")
}

// pendingList is the approvals waiting, oldest first.
func (a *App) pendingList() []agent.Event {
	out := make([]agent.Event, 0, len(a.pending))
	for _, e := range a.pending {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

func (a *App) current(id string) bool { return a.live != nil && a.live.Meta.ID == id }

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return t.Format("Jan 2")
	}
}
