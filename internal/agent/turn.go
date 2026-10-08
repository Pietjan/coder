package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/pietjan/coder/internal/llm"
	"github.com/pietjan/coder/internal/tool"
)

// deltaEvery is the least time between two assistant_delta events.
const deltaEvery = 40 * time.Millisecond

// runTurn is one user turn: completions until the model stops asking for
// tools. Adapted from toto's openai driver. Caller holds turnMu.
func (c *Conversation) runTurn(text string) {
	turnCtx, cancel := context.WithCancel(c.ctx)
	defer cancel()
	c.stateMu.Lock()
	c.turnCancel = cancel
	c.stateMu.Unlock()

	start := time.Now()
	c.history = append(c.history, llm.Message{Role: llm.RoleUser, Content: text})
	c.emit(Event{Kind: KindUser, Text: text})

	var usage llm.Usage
	for iter := 0; ; iter++ {
		if iter >= c.cfg.MaxIterations {
			c.emit(Event{Kind: KindError, Text: fmt.Sprintf("stopped after %d tool rounds in one turn", iter)})
			c.emit(Event{Kind: KindTurnDone, IsError: true, Usage: &usage, Duration: time.Since(start)})
			return
		}

		comp, err := c.complete(turnCtx)
		if err != nil {
			switch {
			case errors.Is(err, context.Canceled):
				if comp.Content != "" {
					c.history = append(c.history, llm.Message{Role: llm.RoleAssistant, Content: comp.Content})
					c.emit(Event{Kind: KindAssistant, Text: comp.Content, Reasoning: comp.Reasoning})
				}
				c.emit(Event{Kind: KindTurnDone, Interrupted: true, Usage: &usage, Duration: time.Since(start)})
			case llm.IsContextLength(err):
				c.emit(Event{Kind: KindError, Text: "The model's context is full (" + err.Error() + "). Start a new session; one task per session keeps it fast."})
				c.emit(Event{Kind: KindTurnDone, IsError: true, Usage: &usage, Duration: time.Since(start)})
			default:
				c.emit(Event{Kind: KindError, Text: err.Error()})
				c.emit(Event{Kind: KindTurnDone, IsError: true, Usage: &usage, Duration: time.Since(start)})
			}
			return
		}
		if comp.Usage != nil {
			usage.PromptTokens += comp.Usage.PromptTokens
			usage.CompletionTokens += comp.Usage.CompletionTokens
			usage.TotalTokens += comp.Usage.TotalTokens
		}

		c.history = append(c.history, llm.Message{Role: llm.RoleAssistant, Content: comp.Content, ToolCalls: comp.ToolCalls})
		c.emit(Event{Kind: KindAssistant, Text: comp.Content, Reasoning: comp.Reasoning, ToolCalls: comp.ToolCalls})

		if len(comp.ToolCalls) == 0 {
			c.emit(Event{Kind: KindTurnDone, Usage: &usage, Duration: time.Since(start)})
			return
		}

		interrupted := false
		for _, tc := range comp.ToolCalls {
			var output string
			if interrupted || turnCtx.Err() != nil {
				interrupted = true
				output = "interrupted by user"
				c.emit(Event{Kind: KindToolCall, CallID: tc.ID, Tool: tc.Function.Name, Input: inputOf(tc)})
				c.emit(Event{Kind: KindToolResult, CallID: tc.ID, Tool: tc.Function.Name, Output: output, IsError: true})
			} else {
				output = c.runToolCall(turnCtx, tc)
			}
			c.history = append(c.history, llm.Message{Role: llm.RoleTool, ToolCallID: tc.ID, Content: output})
		}
		if interrupted || turnCtx.Err() != nil {
			c.emit(Event{Kind: KindTurnDone, Interrupted: true, Usage: &usage, Duration: time.Since(start)})
			return
		}
	}
}

// complete runs one streaming completion over the history, emitting
// assistant_delta events as the reply grows. On cancellation the partial
// content comes back with the error.
func (c *Conversation) complete(ctx context.Context) (llm.Completion, error) {
	req := llm.Request{Model: c.cfg.Model, Messages: c.history, Tools: c.cfg.Tools.Defs(), MaxTokens: c.cfg.MaxTokens}
	var last time.Time
	var lastContent, lastReasoning string
	comp, err := c.cfg.Client.Complete(ctx, req, func(content, reasoning string) {
		lastContent, lastReasoning = content, reasoning
		if time.Since(last) < deltaEvery {
			return
		}
		last = time.Now()
		c.emit(Event{Kind: KindAssistantDelta, Text: content, Reasoning: reasoning})
	})
	if err != nil {
		return llm.Completion{Content: lastContent, Reasoning: lastReasoning}, err
	}
	if lastContent != "" || lastReasoning != "" {
		c.emit(Event{Kind: KindAssistantDelta, Text: comp.Content, Reasoning: comp.Reasoning})
	}
	return comp, nil
}

// runToolCall gates the call on the user where the tool is not read-only,
// runs it, and returns what the model gets to see.
func (c *Conversation) runToolCall(ctx context.Context, tc llm.ToolCall) string {
	name := tc.Function.Name
	input := inputOf(tc)
	c.emit(Event{Kind: KindToolCall, CallID: tc.ID, Tool: name, Input: input})

	done := func(output string, isErr bool) string {
		c.emit(Event{Kind: KindToolResult, CallID: tc.ID, Tool: name, Output: output, IsError: isErr})
		return output
	}

	t, ok := c.cfg.Tools.Get(name)
	if !ok {
		return done(fmt.Sprintf("unknown tool %q; call tools with action list", name), true)
	}

	c.stateMu.Lock()
	allowed := t.ReadOnly || c.allowed[name]
	c.stateMu.Unlock()
	if !allowed {
		ch := make(chan Decision, 1)
		c.stateMu.Lock()
		c.decisions[tc.ID] = ch
		c.stateMu.Unlock()
		c.emit(Event{Kind: KindApprovalRequested, CallID: tc.ID, Tool: name, Input: input})

		var d Decision
		select {
		case d = <-ch:
		case <-ctx.Done():
			c.stateMu.Lock()
			delete(c.decisions, tc.ID)
			c.stateMu.Unlock()
			return done("interrupted by user before the tool ran", true)
		}
		c.emit(Event{Kind: KindApprovalResolved, CallID: tc.ID, Tool: name, Decision: d})
		switch d {
		case AllowSession:
			c.stateMu.Lock()
			c.allowed[name] = true
			c.stateMu.Unlock()
		case Deny:
			return done("denied by user", true)
		}
	}

	before := c.cfg.Tools.Active()
	output, err := t.Run(ctx, input)
	if err != nil {
		if ctx.Err() != nil {
			return done("interrupted by user", true)
		}
		return done("error: "+err.Error(), true)
	}
	done(output, false)
	if name == tool.MetaName {
		if added := newlyActive(before, c.cfg.Tools.Active()); len(added) > 0 {
			c.emit(Event{Kind: KindToolsActivated, Names: added})
		}
	}
	return output
}

func inputOf(tc llm.ToolCall) json.RawMessage {
	args := tc.Function.Arguments
	if args == "" || !json.Valid([]byte(args)) {
		b, _ := json.Marshal(map[string]string{"raw": args})
		if args == "" {
			b = []byte("{}")
		}
		return b
	}
	return json.RawMessage(args)
}

func newlyActive(before, after []string) []string {
	seen := map[string]bool{}
	for _, n := range before {
		seen[n] = true
	}
	var out []string
	for _, n := range after {
		if !seen[n] {
			out = append(out, n)
		}
	}
	return out
}
