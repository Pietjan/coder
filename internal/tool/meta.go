package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// MetaName is the one tool every request carries.
const MetaName = "tools"

type metaArgs struct {
	Action string   `json:"action" jsonschema:"list: every available tool with a one-line description. describe: the full parameter schemas of the named tools, which also makes them callable for the rest of this session"`
	Names  []string `json:"names,omitempty" jsonschema:"with describe: the tools to describe"`
}

type described struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	ReadOnly    bool            `json:"read_only,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

// meta builds the `tools` tool over this registry.
func (r *Registry) meta() Tool {
	return Tool{
		Name: MetaName,
		Description: "Discover the other tools. action \"list\" names them all with a one-line description; " +
			"action \"describe\" with names returns their parameter schemas and makes them callable for the rest of this session. " +
			"Describe only the tools you need.",
		Schema:   Schema[metaArgs](),
		ReadOnly: true,
		Run: func(_ context.Context, args json.RawMessage) (string, error) {
			a, err := decode[metaArgs](args)
			if err != nil {
				return "", err
			}
			switch a.Action {
			case "list":
				return result(r.Catalog())
			case "describe":
				if len(a.Names) == 0 {
					return "", fmt.Errorf("describe needs names; call list first")
				}
				_, unknown := r.Activate(a.Names...)
				var out []described
				for _, name := range a.Names {
					t, ok := r.Get(name)
					if !ok || name == MetaName {
						continue
					}
					out = append(out, described{Name: t.Name, Description: t.Description, ReadOnly: t.ReadOnly, Parameters: t.Schema})
				}
				text, err := result(out)
				if err != nil {
					return "", err
				}
				if len(unknown) > 0 {
					text += fmt.Sprintf("\n\nunknown tools: %s (call list to see the names)", strings.Join(unknown, ", "))
				}
				return text, nil
			default:
				return "", fmt.Errorf("unknown action %q: use list or describe", a.Action)
			}
		},
	}
}
