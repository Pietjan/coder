package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/a-h/templ"
	"github.com/pietjan/shuttle"

	"github.com/pietjan/coder/internal/agent"
)

// keys of the transcript's items

const keyCur = "cur"

func keyUser(e agent.Event) string      { return fmt.Sprintf("u-%d", e.Seq) }
func keyAssistant(e agent.Event) string { return fmt.Sprintf("a-%d", e.Seq) }
func keyTool(e agent.Event) string      { return fmt.Sprintf("t-%d", e.Seq) }
func keyApproval(e agent.Event) string  { return fmt.Sprintf("p-%d", e.Seq) }
func keyError(e agent.Event) string     { return fmt.Sprintf("e-%d", e.Seq) }

// withID wraps an item in the element shuttle addresses it by.
func withID(id string, c templ.Component) templ.Component {
	return identified{id: id, c: c}
}

type identified struct {
	id string
	c  templ.Component
}

func (i identified) Render(ctx context.Context, w io.Writer) error {
	if _, err := fmt.Fprintf(w, `<div id="%s">`, i.id); err != nil {
		return err
	}
	if err := i.c.Render(ctx, w); err != nil {
		return err
	}
	_, err := io.WriteString(w, "</div>")
	return err
}

func idOf(c templ.Component) string {
	if i, ok := c.(identified); ok {
		return i.id
	}
	return ""
}

// pretty renders tool input for the card.
func pretty(raw json.RawMessage) string {
	var buf bytes.Buffer
	if json.Indent(&buf, raw, "", "  ") != nil {
		return string(raw)
	}
	return buf.String()
}

// preview is the one-line summary of a tool call's input.
func preview(raw json.RawMessage) string {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return clip(string(raw), 80)
	}
	for _, k := range []string{"path", "command", "pattern", "action", "from", "names"} {
		if v, ok := m[k]; ok {
			return clip(fmt.Sprint(v), 80)
		}
	}
	return clip(string(raw), 80)
}

func clip(s string, n int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// clipOutput bounds what a tool card shows.
func clipOutput(s string) string {
	const limit = 8 << 10
	if len(s) > limit {
		return s[:limit] + "\n… (" + fmt.Sprint(len(s)-limit) + " more bytes)"
	}
	return s
}

// rawOpt and rawAttr let shuttle bindings land on plain HTML elements in
// the templates: attrs(shuttle.OnClick(ctx, rawAttr, fn))... spreads them.
type rawOpt = func(templ.Attributes)

var rawAttr shuttle.Attrs[rawOpt] = func(key string, val ...string) rawOpt {
	return func(a templ.Attributes) {
		v := ""
		if len(val) > 0 {
			v = val[0]
		}
		a[key] = v
	}
}

func attrs(opts ...rawOpt) templ.Attributes {
	a := templ.Attributes{}
	for _, o := range opts {
		o(a)
	}
	return a
}
