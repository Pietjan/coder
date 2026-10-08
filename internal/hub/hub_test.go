package hub

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/pietjan/coder/internal/agent"
	"github.com/pietjan/coder/internal/llm"
	"github.com/pietjan/coder/internal/session"
	"github.com/pietjan/coder/internal/testopenai"
)

func TestHubReplayAndResume(t *testing.T) {
	srv := testopenai.NewServer()
	defer srv.Close()
	store, err := session.Open(filepath.Join(t.TempDir(), "s"))
	if err != nil {
		t.Fatal(err)
	}
	h := New(store, llm.New(srv.URL+"/v1", ""), "fake-model", nil)

	watch, stop := h.WatchSessions()
	defer stop()
	l, err := h.Create(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-watch:
		if m.ID != l.Meta.ID {
			t.Errorf("watched %+v", m)
		}
	case <-time.After(time.Second):
		t.Fatal("no sessions-list notification")
	}

	replay, ch, cancel := l.Subscribe()
	defer cancel()
	if len(replay) != 0 {
		t.Errorf("replay %+v", replay)
	}
	if err := l.Send("echo: hi"); err != nil {
		t.Fatal(err)
	}
	var sawDelta bool
	for e := range ch {
		if e.Kind == agent.KindAssistantDelta {
			sawDelta = true
		}
		if e.Kind == agent.KindTurnDone {
			break
		}
	}
	if !sawDelta {
		t.Error("no delta reached the subscriber")
	}
	replay, _, cancel2 := l.Subscribe()
	cancel2()
	kinds := []agent.Kind{}
	for _, e := range replay {
		kinds = append(kinds, e.Kind)
		if e.Seq == 0 {
			t.Errorf("unnumbered replay event %+v", e)
		}
	}
	if len(kinds) != 3 || kinds[0] != agent.KindUser || kinds[1] != agent.KindAssistant || kinds[2] != agent.KindTurnDone {
		t.Errorf("replay kinds %v", kinds)
	}

	// resume from disk in a fresh hub
	h.Close()
	h2 := New(store, llm.New(srv.URL+"/v1", ""), "fake-model", nil)
	defer h2.Close()
	l2, err := h2.Open(l.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if l2.Meta.Project != l.Meta.Project || l2.Meta.Title != "echo: hi" {
		t.Errorf("meta %+v", l2.Meta)
	}
	replay, ch, cancel = l2.Subscribe()
	defer cancel()
	if len(replay) != 4 { // header + 3
		t.Errorf("resumed replay %d", len(replay))
	}
	if err := l2.Send("echo: again"); err != nil {
		t.Fatal(err)
	}
	for e := range ch {
		if e.Kind == agent.KindTurnDone {
			break
		}
	}
	events, err := session.Read(filepath.Join(store.Dir, l.Meta.ID+".jsonl"))
	if err != nil || len(events) != 7 || events[6].Seq != 7 {
		t.Errorf("file: %d events, err %v", len(events), err)
	}
	if again, _ := h2.Open(l.Meta.ID); again != l2 {
		t.Error("Open did not return the cached live session")
	}
}
