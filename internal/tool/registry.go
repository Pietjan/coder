// Package tool is what the agent can do: a registry of named, schema'd
// functions over a project directory. The model sees one tool at first,
// `tools`, which lists the rest and hands out their schemas on request;
// every schema handed out stays in the request for the rest of the
// session.
package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/pietjan/coder/internal/llm"
)

// Tool is one function the model can call.
type Tool struct {
	Name        string
	Description string
	// Schema is the JSON schema of the arguments: an object, always with
	// a "properties" member (strict servers reject one without).
	Schema json.RawMessage
	// ReadOnly tools run without asking the user.
	ReadOnly bool
	Run      func(ctx context.Context, args json.RawMessage) (string, error)
}

// Summary is what `tools list` shows for a tool.
type Summary struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	ReadOnly    bool   `json:"read_only,omitempty"`
}

// Registry holds the tools and which of them the model has asked to see.
type Registry struct {
	mu     sync.Mutex
	tools  map[string]Tool
	order  []string
	active map[string]bool
}

// NewRegistry registers the meta-tool and then tools.
func NewRegistry(tools ...Tool) *Registry {
	r := &Registry{tools: map[string]Tool{}, active: map[string]bool{}}
	r.Register(r.meta())
	for _, t := range tools {
		r.Register(t)
	}
	return r
}

// Register adds or replaces a tool.
func (r *Registry) Register(t Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.tools[t.Name]; !ok {
		r.order = append(r.order, t.Name)
	}
	t.Schema = normalizeSchema(t.Schema)
	r.tools[t.Name] = t
}

// Catalog lists every tool but the meta-tool, in registration order.
func (r *Registry) Catalog() []Summary {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Summary, 0, len(r.order))
	for _, name := range r.order {
		if name == MetaName {
			continue
		}
		t := r.tools[name]
		out = append(out, Summary{Name: t.Name, Description: t.Description, ReadOnly: t.ReadOnly})
	}
	return out
}

// Activate makes names part of every later Defs. It reports which names
// were newly activated and which are not tools at all.
func (r *Registry) Activate(names ...string) (added, unknown []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, name := range names {
		switch {
		case name == MetaName:
		case r.tools[name].Name == "":
			unknown = append(unknown, name)
		case !r.active[name]:
			r.active[name] = true
			added = append(added, name)
		}
	}
	return added, unknown
}

// Active lists the activated tools, sorted.
func (r *Registry) Active() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.active))
	for name := range r.active {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

// Get looks a tool up by name.
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.tools[name]
	return t, ok
}

// Defs is the tools array of a request: the meta-tool and the activated
// tools, in registration order.
func (r *Registry) Defs() []llm.ToolDef {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []llm.ToolDef
	for _, name := range r.order {
		if name != MetaName && !r.active[name] {
			continue
		}
		t := r.tools[name]
		out = append(out, llm.ToolDef{Type: "function", Function: llm.FunctionDef{
			Name: t.Name, Description: t.Description, Parameters: t.Schema,
		}})
	}
	return out
}

// Call runs the named tool.
func (r *Registry) Call(ctx context.Context, name string, args json.RawMessage) (string, error) {
	t, ok := r.Get(name)
	if !ok {
		return "", fmt.Errorf("unknown tool %q; call tools with action list", name)
	}
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	return t.Run(ctx, args)
}

// Schema derives a tool's argument schema from a struct's `json` and
// `jsonschema:"description"` tags.
func Schema[T any]() json.RawMessage {
	s, err := jsonschema.For[T](nil)
	if err != nil {
		panic(err)
	}
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return normalizeSchema(b)
}

// normalizeSchema guarantees the schema is an object with a "properties"
// member; strict endpoints (LM Studio) reject tool definitions without
// one, which no-argument tools otherwise produce.
func normalizeSchema(schema json.RawMessage) json.RawMessage {
	empty := json.RawMessage(`{"type":"object","properties":{}}`)
	var m map[string]any
	if err := json.Unmarshal(schema, &m); err != nil || m == nil {
		return empty
	}
	if _, ok := m["properties"]; ok {
		return schema
	}
	m["properties"] = map[string]any{}
	fixed, err := json.Marshal(m)
	if err != nil {
		return empty
	}
	return fixed
}

// decode parses tool arguments into T.
func decode[T any](args json.RawMessage) (T, error) {
	var v T
	if len(args) == 0 {
		return v, nil
	}
	if err := json.Unmarshal(args, &v); err != nil {
		return v, fmt.Errorf("bad arguments: %w", err)
	}
	return v, nil
}

// result renders a tool's reply as indented JSON.
func result(v any) (string, error) {
	if v == nil {
		return "ok", nil
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}
