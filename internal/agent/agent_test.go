package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pietjan/coder/internal/llm"
	"github.com/pietjan/coder/internal/testopenai"
	"github.com/pietjan/coder/internal/tool"
)

type harness struct {
	t    *testing.T
	conv *Conversation
	root string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	srv := testopenai.NewServer()
	t.Cleanup(srv.Close)
	root := t.TempDir()
	conv := New(Config{
		Client:        llm.New(srv.URL+"/v1", ""),
		Model:         "fake-model",
		Tools:         tool.NewRegistry(tool.Project(root)...),
		ProjectDir:    root,
		MaxIterations: 5,
	})
	t.Cleanup(conv.Close)
	return &harness{t: t, conv: conv, root: root}
}

// next waits for the next event of one of the kinds, skipping others.
func (h *harness) next(kinds ...Kind) Event {
	h.t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case e, ok := <-h.conv.Events():
			if !ok {
				h.t.Fatalf("events closed while waiting for %v", kinds)
			}
			for _, k := range kinds {
				if e.Kind == k {
					return e
				}
			}
			if e.Kind == KindError {
				h.t.Fatalf("error event while waiting for %v: %s", kinds, e.Text)
			}
		case <-deadline:
			h.t.Fatalf("timeout waiting for %v", kinds)
		}
	}
}

// turn sends text and collects events until turn_done.
func (h *harness) turn(text string) []Event {
	h.t.Helper()
	if err := h.conv.Send(text); err != nil {
		h.t.Fatal(err)
	}
	var out []Event
	for {
		e := h.next(KindUser, KindAssistant, KindToolCall, KindToolResult, KindApprovalRequested, KindApprovalResolved, KindToolsActivated, KindTurnDone, KindError)
		out = append(out, e)
		if e.Kind == KindTurnDone {
			return out
		}
	}
}

func kinds(events []Event) []Kind {
	out := make([]Kind, 0, len(events))
	for _, e := range events {
		out = append(out, e.Kind)
	}
	return out
}

func TestEchoTurn(t *testing.T) {
	h := newHarness(t)
	ev := h.turn("echo: hello")
	want := []Kind{KindUser, KindAssistant, KindTurnDone}
	if !reflect.DeepEqual(kinds(ev), want) {
		t.Fatalf("kinds %v", kinds(ev))
	}
	if ev[1].Text != "hello" || ev[2].Usage == nil || ev[2].Usage.TotalTokens != 15 {
		t.Errorf("events %+v", ev)
	}
	if h.conv.Busy() {
		t.Error("still busy")
	}
}

func TestDescribeActivatesAndReadOnlyRunsWithoutApproval(t *testing.T) {
	h := newHarness(t)
	if err := os.WriteFile(filepath.Join(h.root, "f.txt"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ev := h.turn("describe: read_file,write_file")
	want := []Kind{KindUser, KindAssistant, KindToolCall, KindToolResult, KindToolsActivated, KindAssistant, KindTurnDone}
	if !reflect.DeepEqual(kinds(ev), want) {
		t.Fatalf("kinds %v", kinds(ev))
	}
	if !reflect.DeepEqual(ev[4].Names, []string{"read_file", "write_file"}) {
		t.Errorf("activated %v", ev[4].Names)
	}
	ev = h.turn(`tool: read_file {"path":"f.txt"}`)
	if !reflect.DeepEqual(kinds(ev), []Kind{KindUser, KindAssistant, KindToolCall, KindToolResult, KindAssistant, KindTurnDone}) {
		t.Fatalf("kinds %v", kinds(ev))
	}
	if ev[3].IsError || !strings.Contains(ev[3].Output, `"content": "x"`) {
		t.Errorf("result %+v", ev[3])
	}
}

func TestApprovalDenyAllowAllowSession(t *testing.T) {
	h := newHarness(t)
	h.turn("describe: write_file")

	// deny
	if err := h.conv.Send(`tool: write_file {"path":"a.txt","content":"1"}`); err != nil {
		t.Fatal(err)
	}
	req := h.next(KindApprovalRequested)
	if req.Tool != "write_file" {
		t.Fatalf("request %+v", req)
	}
	if err := h.conv.Respond("nope", Deny); err != ErrUnknownRequest {
		t.Errorf("unknown request: %v", err)
	}
	if err := h.conv.Respond(req.CallID, Deny); err != nil {
		t.Fatal(err)
	}
	res := h.next(KindToolResult)
	if !res.IsError || res.Output != "denied by user" {
		t.Errorf("result %+v", res)
	}
	final := h.next(KindAssistant)
	if final.Text != "tool finished: denied by user" {
		t.Errorf("model saw %q", final.Text)
	}
	h.next(KindTurnDone)
	if _, err := os.Stat(filepath.Join(h.root, "a.txt")); !os.IsNotExist(err) {
		t.Error("file written despite deny")
	}

	// allow once
	if err := h.conv.Send(`tool: write_file {"path":"a.txt","content":"1"}`); err != nil {
		t.Fatal(err)
	}
	req = h.next(KindApprovalRequested)
	if err := h.conv.Respond(req.CallID, Allow); err != nil {
		t.Fatal(err)
	}
	if res := h.next(KindToolResult); res.IsError {
		t.Errorf("result %+v", res)
	}
	h.next(KindTurnDone)

	// allow for the session: asked once, then never
	if err := h.conv.Send(`tool: write_file {"path":"b.txt","content":"2"}`); err != nil {
		t.Fatal(err)
	}
	req = h.next(KindApprovalRequested)
	if err := h.conv.Respond(req.CallID, AllowSession); err != nil {
		t.Fatal(err)
	}
	h.next(KindTurnDone)
	ev := h.turn(`tool: write_file {"path":"c.txt","content":"3"}`)
	for _, e := range ev {
		if e.Kind == KindApprovalRequested {
			t.Fatal("asked again after allow_session")
		}
	}
	if _, err := os.Stat(filepath.Join(h.root, "c.txt")); err != nil {
		t.Error("c.txt not written")
	}
}

func TestInterruptWhileWaitingForApproval(t *testing.T) {
	h := newHarness(t)
	h.turn("describe: write_file")
	if err := h.conv.Send(`tools: write_file {"path":"a.txt","content":"1"}; write_file {"path":"b.txt","content":"2"}`); err != nil {
		t.Fatal(err)
	}
	h.next(KindApprovalRequested)
	if err := h.conv.Send("x"); err != ErrBusy {
		t.Errorf("Send while busy: %v", err)
	}
	h.conv.Interrupt()
	doneEv := h.next(KindTurnDone)
	if !doneEv.Interrupted {
		t.Errorf("turn_done %+v", doneEv)
	}
	// the history is still well formed: one tool message per call
	hist := h.conv.History()
	last := hist[len(hist)-1]
	if last.Role != llm.RoleTool || hist[len(hist)-2].Role != llm.RoleTool || hist[len(hist)-3].Role != llm.RoleAssistant || len(hist[len(hist)-3].ToolCalls) != 2 {
		t.Errorf("history tail: %+v", hist[len(hist)-3:])
	}
	// and the conversation still works
	ev := h.turn("echo: again")
	if ev[1].Text != "again" {
		t.Errorf("after interrupt: %+v", ev)
	}
}

func TestInterruptWhileStreaming(t *testing.T) {
	h := newHarness(t)
	if err := h.conv.Send("hang"); err != nil {
		t.Fatal(err)
	}
	h.next(KindAssistantDelta)
	h.conv.Interrupt()
	doneEv := h.next(KindTurnDone)
	if !doneEv.Interrupted {
		t.Errorf("turn_done %+v", doneEv)
	}
}

func TestContextLengthError(t *testing.T) {
	h := newHarness(t)
	if err := h.conv.Send("toolong"); err != nil {
		t.Fatal(err)
	}
	h.next(KindUser)
	e := <-h.conv.Events()
	if e.Kind != KindError || !strings.Contains(e.Text, "context is full") {
		t.Errorf("got %+v", e)
	}
	if d := h.next(KindTurnDone); !d.IsError {
		t.Errorf("turn_done %+v", d)
	}
}

func TestMaxIterations(t *testing.T) {
	h := newHarness(t)
	// the fake answers every tool result with text, so loop by making each
	// turn a tool call: not possible with the script; instead drive the
	// guard directly with a tiny limit.
	h.conv.cfg.MaxIterations = 1
	if err := h.conv.Send("describe: read_file"); err != nil {
		t.Fatal(err)
	}
	var sawError bool
	for e := range h.conv.Events() {
		if e.Kind == KindError && strings.Contains(e.Text, "tool rounds") {
			sawError = true
		}
		if e.Kind == KindTurnDone {
			if !e.IsError || !sawError {
				t.Errorf("turn_done %+v sawError %v", e, sawError)
			}
			return
		}
	}
}

func TestRebuildRoundTrip(t *testing.T) {
	h := newHarness(t)
	var all []Event
	all = append(all, h.turn("describe: read_file,write_file")...)
	if err := h.conv.Send(`tool: write_file {"path":"a.txt","content":"1"}`); err != nil {
		t.Fatal(err)
	}
	for {
		e := h.next(KindUser, KindAssistant, KindToolCall, KindToolResult, KindApprovalRequested, KindApprovalResolved, KindToolsActivated, KindTurnDone)
		all = append(all, e)
		if e.Kind == KindApprovalRequested {
			if err := h.conv.Respond(e.CallID, Allow); err != nil {
				t.Fatal(err)
			}
		}
		if e.Kind == KindTurnDone {
			break
		}
	}
	all = append(all, h.turn("echo: bye")...)

	history, activated := Rebuild(h.conv.system, all)
	if !reflect.DeepEqual(history, h.conv.History()) {
		t.Errorf("rebuilt history differs:\n%+v\nvs\n%+v", history, h.conv.History())
	}
	if !reflect.DeepEqual(activated, []string{"read_file", "write_file"}) {
		t.Errorf("activated %v", activated)
	}

	// Restore into a fresh conversation: the tools array matches too.
	fresh := New(Config{Client: h.conv.cfg.Client, Model: "fake-model", Tools: tool.NewRegistry(tool.Project(h.root)...), ProjectDir: h.root})
	defer fresh.Close()
	fresh.Restore(all)
	if !reflect.DeepEqual(fresh.cfg.Tools.Active(), h.conv.cfg.Tools.Active()) {
		t.Errorf("active after restore %v", fresh.cfg.Tools.Active())
	}
}

func TestRebuildPatchesCrashMidTurn(t *testing.T) {
	events := []Event{
		{Kind: KindUser, Text: "hi"},
		{Kind: KindAssistant, Text: "", ToolCalls: []llm.ToolCall{{ID: "c1", Type: "function", Function: llm.FunctionCall{Name: "tools", Arguments: "{}"}}, {ID: "c2", Type: "function", Function: llm.FunctionCall{Name: "grep", Arguments: "{}"}}}},
		{Kind: KindToolResult, CallID: "c1", Output: "ok"},
	}
	history, _ := Rebuild("sys", events)
	if len(history) != 5 || history[3].ToolCallID != "c1" || history[4].ToolCallID != "c2" || history[4].Content != "interrupted" {
		t.Errorf("history %+v", history)
	}
}
