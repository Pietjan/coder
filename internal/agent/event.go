// Package agent runs a conversation with a model that can call tools: the
// turn loop, the approval gate, and the event stream the UI and the
// session file are built from.
package agent

import (
	"encoding/json"
	"time"

	"github.com/pietjan/coder/internal/llm"
)

// Kind is what an Event reports.
type Kind string

// Event kinds. All but KindAssistantDelta are persisted.
const (
	KindSession           Kind = "session" // the session file's header line
	KindUser              Kind = "user"
	KindAssistantDelta    Kind = "assistant_delta" // accumulated text so far; never persisted
	KindAssistant         Kind = "assistant"
	KindToolCall          Kind = "tool_call"
	KindToolResult        Kind = "tool_result"
	KindApprovalRequested Kind = "approval_requested"
	KindApprovalResolved  Kind = "approval_resolved"
	KindToolsActivated    Kind = "tools_activated"
	KindTurnDone          Kind = "turn_done"
	KindError             Kind = "error"
)

// Decision answers an approval request.
type Decision string

// Decisions.
const (
	Allow        Decision = "allow"
	AllowSession Decision = "allow_session" // and every later call of this tool
	Deny         Decision = "deny"
)

// Event is one thing that happened in a session. Which fields are set
// depends on Kind; the zero value of the rest is omitted from JSON.
type Event struct {
	Seq  int64     `json:"seq,omitempty"` // assigned by the session writer
	At   time.Time `json:"at"`
	Kind Kind      `json:"kind"`

	// session
	ID      string `json:"id,omitempty"`
	Project string `json:"project,omitempty"`
	Model   string `json:"model,omitempty"`

	// user, assistant(_delta), error
	Text      string         `json:"text,omitempty"`
	Reasoning string         `json:"reasoning,omitempty"`
	ToolCalls []llm.ToolCall `json:"tool_calls,omitempty"` // assistant: the calls it made, for the history

	// tool_call, tool_result, approval_*
	CallID   string          `json:"call_id,omitempty"`
	Tool     string          `json:"tool,omitempty"`
	Input    json.RawMessage `json:"input,omitempty"`
	Output   string          `json:"output,omitempty"`
	IsError  bool            `json:"is_error,omitempty"`
	Decision Decision        `json:"decision,omitempty"`

	// tools_activated
	Names []string `json:"names,omitempty"`

	// turn_done
	Usage       *llm.Usage    `json:"usage,omitempty"`
	Interrupted bool          `json:"interrupted,omitempty"`
	Duration    time.Duration `json:"duration,omitempty"`
}

// Persistent reports whether the event belongs in the session file.
func (e Event) Persistent() bool { return e.Kind != KindAssistantDelta }
