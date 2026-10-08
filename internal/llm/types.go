// Package llm is a small client for the OpenAI chat-completions API as
// local servers speak it: colibri's `coli serve`, LM Studio, Ollama. Only
// what the agent needs is here — streaming chat completions with tool
// calls, and the model list — so the raw error body and the
// `reasoning_content` field, which the big clients hide, stay visible.
package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Roles of a Message.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

// Message is one entry of a conversation.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// ToolCall is a function call the assistant asked for.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"` // "function"
	Function FunctionCall `json:"function"`
}

// FunctionCall names the function and carries its JSON arguments.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolDef is a tool offered to the model.
type ToolDef struct {
	Type     string      `json:"type"` // "function"
	Function FunctionDef `json:"function"`
}

// FunctionDef describes one function tool; Parameters is a JSON schema.
type FunctionDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// StreamOptions asks the server for the usage block in the final chunk.
type StreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// Request is a chat-completions request.
type Request struct {
	Model         string         `json:"model"`
	Messages      []Message      `json:"messages"`
	Tools         []ToolDef      `json:"tools,omitempty"`
	Stream        bool           `json:"stream"`
	StreamOptions *StreamOptions `json:"stream_options,omitempty"`
	MaxTokens     int            `json:"max_tokens,omitempty"`
}

// Usage is the token accounting of one completion.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ToolCallDelta is one streamed fragment of a tool call, merged by Index.
type ToolCallDelta struct {
	Index    int          `json:"index"`
	ID       string       `json:"id,omitempty"`
	Type     string       `json:"type,omitempty"`
	Function FunctionCall `json:"function"`
}

// Delta is one streamed chunk, flattened to the first choice.
type Delta struct {
	Content      string
	Reasoning    string
	ToolCalls    []ToolCallDelta
	FinishReason string
	Usage        *Usage
}

// Completion is a whole streamed reply, merged.
type Completion struct {
	Content      string
	Reasoning    string
	ToolCalls    []ToolCall
	FinishReason string
	Usage        *Usage
}

// APIError is a non-2xx reply, with whatever the server said.
type APIError struct {
	Status  int
	Type    string
	Code    string
	Message string
	Body    []byte
}

func (e *APIError) Error() string {
	status := http.StatusText(e.Status)
	if status == "" {
		status = fmt.Sprintf("status %d", e.Status)
	} else {
		status = fmt.Sprintf("%d %s", e.Status, status)
	}
	if e.Message != "" {
		return fmt.Sprintf("%s: %s", status, e.Message)
	}
	body := strings.TrimSpace(string(e.Body))
	if body == "" {
		return status + " with an empty body (the request did not reach a chat API: wrong base URL?)"
	}
	const limit = 300
	if len(body) > limit {
		body = body[:limit] + "…"
	}
	return status + ": " + body
}

// IsContextLength reports whether err is the server saying the
// conversation no longer fits its context.
func IsContextLength(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Code == "context_length_exceeded"
}

// wire shapes of the streamed response

type chunk struct {
	Choices []struct {
		Delta struct {
			Content   *string         `json:"content"`
			Reasoning *string         `json:"reasoning_content"`
			ToolCalls []ToolCallDelta `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *Usage `json:"usage"`
}

type errorBody struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}
