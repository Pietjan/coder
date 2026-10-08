package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

// Client talks to one OpenAI-compatible base URL, e.g.
// http://127.0.0.1:8080/v1.
type Client struct {
	BaseURL string
	APIKey  string
	// HTTP has no timeout: a local model may prefill for minutes. Cancel
	// the context to stop a request.
	HTTP *http.Client
}

// New returns a client for baseURL; a trailing slash is dropped.
func New(baseURL, apiKey string) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), APIKey: apiKey, HTTP: &http.Client{}}
}

func (c *Client) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, r)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, c.BaseURL+path, err)
	}
	if resp.StatusCode/100 != 2 {
		defer func() { _ = resp.Body.Close() }()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		apiErr := &APIError{Status: resp.StatusCode, Body: raw}
		var eb errorBody
		if json.Unmarshal(raw, &eb) == nil {
			apiErr.Message, apiErr.Type, apiErr.Code = eb.Error.Message, eb.Error.Type, eb.Error.Code
		}
		return nil, apiErr
	}
	return resp, nil
}

// Models lists the model ids the server offers.
func (c *Client) Models(ctx context.Context) ([]string, error) {
	resp, err := c.do(ctx, http.MethodGet, "/models", nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("models: %w", err)
	}
	ids := make([]string, 0, len(body.Data))
	for _, m := range body.Data {
		ids = append(ids, m.ID)
	}
	return ids, nil
}

// Stream runs a streaming completion and calls fn for every chunk. A
// cancelled context surfaces as context.Canceled.
func (c *Client) Stream(ctx context.Context, req Request, fn func(Delta) error) error {
	req.Stream = true
	resp, err := c.do(ctx, http.MethodPost, "/chat/completions", req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	err = readEvents(resp.Body, func(data []byte) error {
		if bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
			return nil
		}
		var ch chunk
		if err := json.Unmarshal(data, &ch); err != nil {
			return fmt.Errorf("stream: bad chunk %q: %w", truncate(data, 200), err)
		}
		var d Delta
		if ch.Usage != nil {
			d.Usage = ch.Usage
		}
		if len(ch.Choices) > 0 {
			ch0 := ch.Choices[0]
			if ch0.Delta.Content != nil {
				d.Content = *ch0.Delta.Content
			}
			if ch0.Delta.Reasoning != nil {
				d.Reasoning = *ch0.Delta.Reasoning
			}
			d.ToolCalls = ch0.Delta.ToolCalls
			if ch0.FinishReason != nil {
				d.FinishReason = *ch0.FinishReason
			}
		} else if d.Usage == nil {
			return nil
		}
		return fn(d)
	})
	if err != nil && ctx.Err() != nil {
		return context.Canceled
	}
	return err
}

// Complete streams one completion, reporting the accumulated content and
// reasoning as they grow, and returns the merged reply: tool-call
// fragments joined by index, usage from the final chunk.
func (c *Client) Complete(ctx context.Context, req Request, onDelta func(content, reasoning string)) (Completion, error) {
	req.StreamOptions = &StreamOptions{IncludeUsage: true}
	var content, reasoning strings.Builder
	calls := map[int]*ToolCall{}
	var out Completion
	err := c.Stream(ctx, req, func(d Delta) error {
		if d.Content != "" || d.Reasoning != "" {
			content.WriteString(d.Content)
			reasoning.WriteString(d.Reasoning)
			if onDelta != nil {
				onDelta(content.String(), reasoning.String())
			}
		}
		for _, tc := range d.ToolCalls {
			call, ok := calls[tc.Index]
			if !ok {
				call = &ToolCall{Type: "function"}
				calls[tc.Index] = call
			}
			if tc.ID != "" {
				call.ID = tc.ID
			}
			if tc.Function.Name != "" {
				call.Function.Name = tc.Function.Name
			}
			call.Function.Arguments += tc.Function.Arguments
		}
		if d.FinishReason != "" {
			out.FinishReason = d.FinishReason
		}
		if d.Usage != nil {
			out.Usage = d.Usage
		}
		return nil
	})
	if err != nil {
		return Completion{}, err
	}
	out.Content = content.String()
	out.Reasoning = reasoning.String()
	idx := make([]int, 0, len(calls))
	for i := range calls {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	for n, i := range idx {
		call := *calls[i]
		if call.ID == "" {
			call.ID = fmt.Sprintf("call_%d", n+1)
		}
		out.ToolCalls = append(out.ToolCalls, call)
	}
	return out, nil
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}

// ErrNoModels is returned by FirstModel when the server lists none.
var ErrNoModels = errors.New("the server lists no models")

// FirstModel returns the first model id the server lists.
func (c *Client) FirstModel(ctx context.Context) (string, error) {
	ids, err := c.Models(ctx)
	if err != nil {
		return "", err
	}
	if len(ids) == 0 {
		return "", ErrNoModels
	}
	return ids[0], nil
}
