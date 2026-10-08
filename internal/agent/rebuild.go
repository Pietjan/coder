package agent

import "github.com/pietjan/coder/internal/llm"

// Rebuild turns a session's events back into the model's history and the
// names of the tools that were activated. A trailing assistant message
// whose tool calls never got results (a crash mid-turn) gets synthetic
// results so the history stays well-formed.
func Rebuild(system string, events []Event) (history []llm.Message, activated []string) {
	history = []llm.Message{{Role: llm.RoleSystem, Content: system}}
	pending := map[string]bool{}
	var order []string
	flush := func() {
		for _, id := range order {
			if pending[id] {
				history = append(history, llm.Message{Role: llm.RoleTool, ToolCallID: id, Content: "interrupted"})
			}
		}
		pending = map[string]bool{}
		order = nil
	}
	for _, e := range events {
		switch e.Kind {
		case KindUser:
			flush()
			history = append(history, llm.Message{Role: llm.RoleUser, Content: e.Text})
		case KindAssistant:
			flush()
			history = append(history, llm.Message{Role: llm.RoleAssistant, Content: e.Text, ToolCalls: e.ToolCalls})
			for _, tc := range e.ToolCalls {
				pending[tc.ID] = true
				order = append(order, tc.ID)
			}
		case KindToolResult:
			if !pending[e.CallID] {
				continue
			}
			pending[e.CallID] = false
			history = append(history, llm.Message{Role: llm.RoleTool, ToolCallID: e.CallID, Content: e.Output})
		case KindToolsActivated:
			activated = append(activated, e.Names...)
		}
	}
	flush()
	return history, activated
}
