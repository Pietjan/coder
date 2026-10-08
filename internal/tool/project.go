package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"
)

// grepMatchLimit caps how many matching lines a single grep returns.
const grepMatchLimit = 200

// Argument and result shapes, copied from toto's mcpserv tool definitions.

type readFileArgs struct {
	Path      string `json:"path" jsonschema:"path to the file to read, relative to the project"`
	StartLine int    `json:"start_line,omitempty" jsonschema:"first line to read (1-based, inclusive); omit to read from the start"`
	EndLine   int    `json:"end_line,omitempty" jsonschema:"last line to read (1-based, inclusive); omit to read to the end"`
}

type fileView struct {
	Path       string `json:"path"`
	Content    string `json:"content"`
	TotalLines int    `json:"total_lines"`
	Truncated  bool   `json:"truncated,omitempty"`
}

type writeFileArgs struct {
	Path    string `json:"path" jsonschema:"path to the file to write, relative to the project; parent directories are created"`
	Content string `json:"content" jsonschema:"content to write"`
}

type editFileArgs struct {
	Path       string `json:"path" jsonschema:"path to the file to edit, relative to the project"`
	OldString  string `json:"old_string" jsonschema:"exact string to replace; must match uniquely unless replace_all is set"`
	NewString  string `json:"new_string" jsonschema:"replacement string"`
	ReplaceAll bool   `json:"replace_all,omitempty" jsonschema:"replace every occurrence instead of requiring a unique match"`
}

type editView struct {
	Path         string `json:"path"`
	Replacements int    `json:"replacements"`
}

type listDirArgs struct {
	Path string `json:"path" jsonschema:"path to the directory to list, relative to the project; \".\" is the project itself"`
}

type globArgs struct {
	Pattern string `json:"pattern" jsonschema:"glob pattern relative to the project, e.g. cmd/*.go (no **)"`
}

type grepArgs struct {
	Pattern string `json:"pattern" jsonschema:"Go regular expression to search file contents for"`
	Root    string `json:"root,omitempty" jsonschema:"directory to search under, relative to the project; omit for the whole project"`
}

type grepView struct {
	Matches   []string `json:"matches"`
	Truncated bool     `json:"truncated,omitempty"`
}

type mkdirArgs struct {
	Path string `json:"path" jsonschema:"directory to create (with parents), relative to the project"`
}

type deleteArgs struct {
	Path      string `json:"path" jsonschema:"path to delete, relative to the project"`
	Recursive bool   `json:"recursive,omitempty" jsonschema:"required to delete a non-empty directory"`
}

type moveArgs struct {
	From string `json:"from" jsonschema:"current path, relative to the project"`
	To   string `json:"to" jsonschema:"new path, relative to the project"`
}

type runCommandArgs struct {
	Command        string `json:"command" jsonschema:"shell command to run in the project directory"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" jsonschema:"kill the command after this many seconds (default 120, max 600)"`
}

type runCommandView struct {
	Output    string `json:"output"`
	ExitCode  int    `json:"exit_code"`
	TimedOut  bool   `json:"timed_out,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// handler adapts a typed function to Tool.Run.
func handler[A any](fn func(ctx context.Context, a A) (any, error)) func(context.Context, json.RawMessage) (string, error) {
	return func(ctx context.Context, args json.RawMessage) (string, error) {
		a, err := decode[A](args)
		if err != nil {
			return "", err
		}
		v, err := fn(ctx, a)
		if err != nil {
			return "", err
		}
		return result(v)
	}
}

// Project returns the file and shell tools over root.
func Project(root string) []Tool {
	j := NewJail(root)
	two := func(from, to string) (string, string, error) {
		src, err := j.Resolve(from)
		if err != nil {
			return "", "", err
		}
		dst, err := j.Resolve(to)
		if err != nil {
			return "", "", err
		}
		return src, dst, nil
	}
	return []Tool{
		{
			Name: "read_file",
			Description: "Read a file (optionally a 1-based inclusive line range). " +
				"Large content is truncated; narrow with start_line/end_line.",
			Schema: Schema[readFileArgs](), ReadOnly: true,
			Run: handler(func(_ context.Context, a readFileArgs) (any, error) {
				p, err := j.Resolve(a.Path)
				if err != nil {
					return nil, err
				}
				s, err := ReadFileLines(p, a.StartLine, a.EndLine)
				if err != nil {
					return nil, err
				}
				return fileView{Path: a.Path, Content: s.Content, TotalLines: s.TotalLines, Truncated: s.Truncated}, nil
			}),
		},
		{
			Name:        "write_file",
			Description: "Write a whole file (atomic; creates parent directories). Prefer edit_file for changes to an existing file.",
			Schema:      Schema[writeFileArgs](),
			Run: handler(func(_ context.Context, a writeFileArgs) (any, error) {
				p, err := j.Resolve(a.Path)
				if err != nil {
					return nil, err
				}
				return nil, WriteFile(p, a.Content)
			}),
		},
		{
			Name: "edit_file",
			Description: "Replace an exact string in a file. Errors if old_string is not found, or matches more than " +
				"once without replace_all — make it unique with surrounding context.",
			Schema: Schema[editFileArgs](),
			Run: handler(func(_ context.Context, a editFileArgs) (any, error) {
				p, err := j.Resolve(a.Path)
				if err != nil {
					return nil, err
				}
				n, err := EditFile(p, a.OldString, a.NewString, a.ReplaceAll)
				if err != nil {
					return nil, err
				}
				return editView{Path: a.Path, Replacements: n}, nil
			}),
		},
		{
			Name: "list_dir", Description: "List the contents of a directory.",
			Schema: Schema[listDirArgs](), ReadOnly: true,
			Run: handler(func(_ context.Context, a listDirArgs) (any, error) {
				p, err := j.Resolve(a.Path)
				if err != nil {
					return nil, err
				}
				return ListDir(p)
			}),
		},
		{
			Name: "glob", Description: "Find files matching a glob pattern; paths are returned project-relative.",
			Schema: Schema[globArgs](), ReadOnly: true,
			Run: handler(func(_ context.Context, a globArgs) (any, error) {
				p, err := j.Resolve(a.Pattern)
				if err != nil {
					return nil, err
				}
				matches, err := Glob(p)
				if err != nil {
					return nil, err
				}
				out := make([]string, 0, len(matches))
				for _, m := range matches {
					out = append(out, j.Rel(m))
				}
				return out, nil
			}),
		},
		{
			Name: "grep", Description: "Search file contents with a regular expression; matches come back as path:line: text.",
			Schema: Schema[grepArgs](), ReadOnly: true,
			Run: handler(func(_ context.Context, a grepArgs) (any, error) {
				root := a.Root
				if root == "" {
					root = "."
				}
				p, err := j.Resolve(root)
				if err != nil {
					return nil, err
				}
				matches, truncated, err := GrepFiles(p, a.Pattern, grepMatchLimit)
				if err != nil {
					return nil, err
				}
				lines := make([]string, 0, len(matches))
				for _, m := range matches {
					lines = append(lines, fmt.Sprintf("%s:%d: %s", j.Rel(m.Path), m.Line, m.Text))
				}
				return grepView{Matches: lines, Truncated: truncated}, nil
			}),
		},
		{
			Name: "mkdir", Description: "Create a directory (and parents).",
			Schema: Schema[mkdirArgs](),
			Run: handler(func(_ context.Context, a mkdirArgs) (any, error) {
				p, err := j.Resolve(a.Path)
				if err != nil {
					return nil, err
				}
				return nil, MkdirAll(p)
			}),
		},
		{
			Name: "delete", Description: "Delete a file or directory; recursive is required for a non-empty directory.",
			Schema: Schema[deleteArgs](),
			Run: handler(func(_ context.Context, a deleteArgs) (any, error) {
				p, err := j.Resolve(a.Path)
				if err != nil {
					return nil, err
				}
				if p == j.Root {
					return nil, fmt.Errorf("refusing to delete the project directory")
				}
				return nil, Delete(p, a.Recursive)
			}),
		},
		{
			Name: "move_file", Description: "Move or rename a file.",
			Schema: Schema[moveArgs](),
			Run: handler(func(_ context.Context, a moveArgs) (any, error) {
				src, dst, err := two(a.From, a.To)
				if err != nil {
					return nil, err
				}
				if err := MkdirAll(filepath.Dir(dst)); err != nil {
					return nil, err
				}
				return nil, MoveFile(src, dst)
			}),
		},
		{
			Name: "copy_file", Description: "Copy a file.",
			Schema: Schema[moveArgs](),
			Run: handler(func(_ context.Context, a moveArgs) (any, error) {
				src, dst, err := two(a.From, a.To)
				if err != nil {
					return nil, err
				}
				if err := MkdirAll(filepath.Dir(dst)); err != nil {
					return nil, err
				}
				return nil, CopyFile(src, dst)
			}),
		},
		{
			Name: "run_command",
			Description: "Run a shell command in the project directory (sh on unix, cmd on Windows): builds, tests, git, " +
				"anything the other tools do not cover. Returns combined output and the exit code; long output is truncated in the middle.",
			Schema: Schema[runCommandArgs](),
			Run: handler(func(ctx context.Context, a runCommandArgs) (any, error) {
				res, err := Run(ctx, j.Root, a.Command, time.Duration(a.TimeoutSeconds)*time.Second)
				if err != nil {
					return nil, err
				}
				return runCommandView{Output: res.Output, ExitCode: res.ExitCode, TimedOut: res.TimedOut, Truncated: res.Truncated}, nil
			}),
		},
	}
}
