package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pietjan/coder/internal/agent"
)

func TestRoundTrip(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	w, m, err := s.Create("/proj", "m")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Append(agent.Event{Kind: agent.KindUser, Text: "first line\nsecond"}); err != nil {
		t.Fatal(err)
	}
	e, err := w.Append(agent.Event{Kind: agent.KindAssistant, Text: "hi"})
	if err != nil || e.Seq != 3 || e.At.IsZero() {
		t.Fatalf("append: %+v %v", e, err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	list, err := s.List()
	if err != nil || len(list) != 1 || list[0].ID != m.ID || list[0].Title != "first line" || list[0].Project != "/proj" {
		t.Fatalf("list %+v %v", list, err)
	}

	w2, events, err := s.Open(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[0].Kind != agent.KindSession || events[2].Text != "hi" {
		t.Errorf("events %+v", events)
	}
	e, _ = w2.Append(agent.Event{Kind: agent.KindTurnDone})
	if e.Seq != 4 {
		t.Errorf("seq after reopen %d", e.Seq)
	}
	_ = w2.Close()

	// a torn last line is dropped, earlier corruption is not
	p := s.path(m.ID)
	if err := os.WriteFile(p, append(mustRead(t, p), []byte(`{"kind":"us`)...), 0o600); err != nil {
		t.Fatal(err)
	}
	events, err = Read(p)
	if err != nil || len(events) != 4 {
		t.Errorf("torn tail: %d %v", len(events), err)
	}
	if err := os.WriteFile(p, []byte("{bad}\n"+string(mustRead(t, p))), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(p); err == nil {
		t.Error("corrupt head accepted")
	}

	if _, _, err := s.Open("../x"); err == nil {
		t.Error("bad id accepted")
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
