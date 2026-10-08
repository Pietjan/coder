package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func sse(w http.ResponseWriter, frames ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, f := range frames {
		_, _ = fmt.Fprintf(w, "data: %s\n\n", f)
	}
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
}

func TestCompleteMergesToolCallsAndUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path %s", r.URL.Path)
		}
		var req Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if req.StreamOptions == nil || !req.StreamOptions.IncludeUsage {
			t.Error("include_usage not requested")
		}
		sse(w,
			`{"choices":[{"delta":{"role":"assistant","reasoning_content":"think"}}]}`,
			`{"choices":[{"delta":{"content":"hel"}}]}`,
			`{"choices":[{"delta":{"content":"lo"}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"read_file","arguments":"{\"pa"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"c2","type":"function","function":{"name":"grep","arguments":"{}"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"th\":\"a\"}"}}]}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
		)
	}))
	defer srv.Close()

	c := New(srv.URL+"/v1/", "")
	var last string
	comp, err := c.Complete(context.Background(), Request{Model: "m"}, func(content, _ string) { last = content })
	if err != nil {
		t.Fatal(err)
	}
	if comp.Content != "hello" || last != "hello" || comp.Reasoning != "think" {
		t.Errorf("content %q last %q reasoning %q", comp.Content, last, comp.Reasoning)
	}
	if comp.FinishReason != "tool_calls" {
		t.Errorf("finish %q", comp.FinishReason)
	}
	if len(comp.ToolCalls) != 2 || comp.ToolCalls[0].ID != "c1" || comp.ToolCalls[0].Function.Arguments != `{"path":"a"}` || comp.ToolCalls[1].Function.Name != "grep" {
		t.Errorf("tool calls %+v", comp.ToolCalls)
	}
	if comp.Usage == nil || comp.Usage.TotalTokens != 15 {
		t.Errorf("usage %+v", comp.Usage)
	}
}

func TestAPIErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/chat/completions":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"too long","type":"invalid_request_error","code":"context_length_exceeded"}}`))
		default:
			http.Error(w, "nope", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := New(srv.URL+"/v1", "")
	_, err := c.Complete(context.Background(), Request{Model: "m"}, nil)
	if !IsContextLength(err) {
		t.Fatalf("want context length error, got %v", err)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest || !strings.Contains(err.Error(), "too long") {
		t.Errorf("got %v", err)
	}

	_, err = c.Models(context.Background())
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound || !strings.Contains(err.Error(), "nope") {
		t.Errorf("got %v", err)
	}
}

func TestStreamCancel(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"a"}}]}`+"\n\n")
		w.(http.Flusher).Flush()
		<-block
	}))
	defer srv.Close()
	defer close(block)

	ctx, cancel := context.WithCancel(context.Background())
	c := New(srv.URL+"/v1", "")
	_, err := c.Complete(ctx, Request{Model: "m"}, func(content, _ string) {
		if content == "a" {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want Canceled, got %v", err)
	}
}

func TestModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("auth %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"a"},{"id":"b"}]}`))
	}))
	defer srv.Close()
	c := New(srv.URL+"/v1", "k")
	id, err := c.FirstModel(context.Background())
	if err != nil || id != "a" {
		t.Fatalf("got %q %v", id, err)
	}
}
