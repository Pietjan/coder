// Package testopenai is a scripted OpenAI-compatible chat-completions
// server for tests, copied from toto and extended. The last message
// drives the script:
//
//	user "echo: <x>"             → streams <x> as two content chunks
//	user "tool: <name> <json>"   → emits one tool call, arguments split
//	                               across chunks to exercise merging
//	user "tools: <name> <json>; <name> <json>"
//	                             → emits several tool calls in one reply
//	user "describe: a,b"         → emits a `tools` describe call for a,b
//	user "toolong"               → 400 context_length_exceeded
//	user "hang"                  → streams one chunk, then blocks until
//	                               the client goes away
//	tool result                  → streams "tool finished: <result>"
//	                               (several results: the last one)
//
// Every reply ends with a usage block (prompt 10, completion 5).
package testopenai

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
)

type message struct {
	Role       string `json:"role"`
	Content    string `json:"content"`
	ToolCallID string `json:"tool_call_id,omitempty"`
}

type tool struct {
	Function struct {
		Name       string                     `json:"name"`
		Parameters map[string]json.RawMessage `json:"parameters"`
	} `json:"function"`
}

type request struct {
	Model    string    `json:"model"`
	Messages []message `json:"messages"`
	Tools    []tool    `json:"tools"`
	Stream   bool      `json:"stream"`
}

// NewServer returns the fake endpoint; use URL()+"/v1" as the base URL.
func NewServer() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", handle)
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"fake-model"}]}`))
	})
	return httptest.NewServer(mux)
}

func handle(w http.ResponseWriter, r *http.Request) {
	var req request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// LM Studio rejects tool definitions whose parameters lack "properties";
	// mirror that strictness so no-arg tools can't slip through untested.
	for i, t := range req.Tools {
		if _, ok := t.Function.Parameters["properties"]; !ok {
			http.Error(w, fmt.Sprintf("tool %d (%s): parameters.properties is required", i, t.Function.Name), http.StatusBadRequest)
			return
		}
	}
	// The model only sees tools it was given: a call to one not in the
	// request is a scripting mistake worth failing loudly on.
	offered := map[string]bool{}
	for _, t := range req.Tools {
		offered[t.Function.Name] = true
	}

	last := req.Messages[len(req.Messages)-1]
	if last.Role == "tool" {
		w.Header().Set("Content-Type", "text/event-stream")
		streamText(w, "tool finished: "+last.Content)
		return
	}
	switch {
	case strings.HasPrefix(last.Content, "echo: "):
		w.Header().Set("Content-Type", "text/event-stream")
		streamText(w, strings.TrimPrefix(last.Content, "echo: "))
	case strings.HasPrefix(last.Content, "tool: "), strings.HasPrefix(last.Content, "tools: "):
		_, spec, _ := strings.Cut(last.Content, ": ")
		var calls []call
		for _, part := range strings.Split(spec, ";") {
			name, args, _ := strings.Cut(strings.TrimSpace(part), " ")
			if args == "" {
				args = "{}"
			}
			if !offered[name] {
				http.Error(w, "tool "+name+" was not offered", http.StatusBadRequest)
				return
			}
			calls = append(calls, call{name, args})
		}
		w.Header().Set("Content-Type", "text/event-stream")
		streamToolCalls(w, calls)
	case strings.HasPrefix(last.Content, "describe: "):
		names := strings.Split(strings.TrimPrefix(last.Content, "describe: "), ",")
		args, _ := json.Marshal(map[string]any{"action": "describe", "names": names})
		w.Header().Set("Content-Type", "text/event-stream")
		streamToolCalls(w, []call{{"tools", string(args)}})
	case last.Content == "toolong":
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"the conversation does not fit the context","type":"invalid_request_error","code":"context_length_exceeded"}}`))
	case last.Content == "hang":
		w.Header().Set("Content-Type", "text/event-stream")
		chunk(w, map[string]any{"role": "assistant", "content": "partial"}, "")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	default:
		w.Header().Set("Content-Type", "text/event-stream")
		streamText(w, "unknown command: "+last.Content)
	}
}

type call struct{ name, args string }

func chunk(w http.ResponseWriter, delta map[string]any, finish string) {
	choice := map[string]any{"index": 0, "delta": delta}
	if finish != "" {
		choice["finish_reason"] = finish
	}
	frame := map[string]any{"id": "cmpl-1", "object": "chat.completion.chunk", "choices": []any{choice}}
	b, _ := json.Marshal(frame)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
}

func done(w http.ResponseWriter) {
	_, _ = fmt.Fprint(w, `data: {"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`+"\n\n")
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
}

func streamText(w http.ResponseWriter, text string) {
	half := len(text) / 2
	chunk(w, map[string]any{"role": "assistant", "content": text[:half]}, "")
	chunk(w, map[string]any{"content": text[half:]}, "")
	chunk(w, map[string]any{}, "stop")
	done(w)
}

func streamToolCalls(w http.ResponseWriter, calls []call) {
	for i, c := range calls {
		half := len(c.args) / 2
		chunk(w, map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
			"index": i, "id": fmt.Sprintf("call-%d", i+1), "type": "function",
			"function": map[string]any{"name": c.name, "arguments": c.args[:half]},
		}}}, "")
		chunk(w, map[string]any{"tool_calls": []any{map[string]any{
			"index":    i,
			"function": map[string]any{"arguments": c.args[half:]},
		}}}, "")
	}
	chunk(w, map[string]any{}, "tool_calls")
	done(w)
}
