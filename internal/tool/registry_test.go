package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestJail(t *testing.T) {
	root := t.TempDir()
	j := NewJail(root)
	for _, ok := range []string{".", "a", "a/b/../c", root, filepath.Join(root, "x")} {
		if _, err := j.Resolve(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"..", "../x", "a/../../x", filepath.Dir(root), root + "2"} {
		if _, err := j.Resolve(bad); err == nil {
			t.Errorf("%q escaped", bad)
		}
	}
	if got := j.Rel(filepath.Join(root, "a", "b")); got != "a/b" {
		t.Errorf("Rel = %q", got)
	}
}

func TestRegistryDefsAndActivate(t *testing.T) {
	r := NewRegistry(Project(t.TempDir())...)
	if defs := r.Defs(); len(defs) != 1 || defs[0].Function.Name != MetaName {
		t.Fatalf("initial defs %+v", defs)
	}
	cat := r.Catalog()
	names := make([]string, 0, len(cat))
	for _, s := range cat {
		names = append(names, s.Name)
	}
	want := []string{"read_file", "write_file", "edit_file", "list_dir", "glob", "grep", "mkdir", "delete", "move_file", "copy_file", "run_command"}
	if !slices.Equal(names, want) {
		t.Errorf("catalog %v", names)
	}
	added, unknown := r.Activate("grep", "nope", "read_file", "grep", MetaName)
	if !slices.Equal(added, []string{"grep", "read_file"}) || !slices.Equal(unknown, []string{"nope"}) {
		t.Errorf("added %v unknown %v", added, unknown)
	}
	defs := r.Defs()
	if len(defs) != 3 || defs[0].Function.Name != MetaName || defs[1].Function.Name != "read_file" || defs[2].Function.Name != "grep" {
		t.Errorf("defs after activate: %+v", defs)
	}
	for _, d := range defs {
		var m map[string]any
		if err := json.Unmarshal(d.Function.Parameters, &m); err != nil {
			t.Fatal(err)
		}
		if _, ok := m["properties"]; !ok {
			t.Errorf("%s: schema has no properties: %s", d.Function.Name, d.Function.Parameters)
		}
	}
	if !slices.Equal(r.Active(), []string{"grep", "read_file"}) {
		t.Errorf("active %v", r.Active())
	}
}

func TestMetaTool(t *testing.T) {
	r := NewRegistry(Project(t.TempDir())...)
	ctx := context.Background()
	out, err := r.Call(ctx, MetaName, json.RawMessage(`{"action":"list"}`))
	if err != nil {
		t.Fatal(err)
	}
	var list []Summary
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 11 || !list[0].ReadOnly || list[1].ReadOnly {
		t.Errorf("list %+v", list)
	}

	out, err = r.Call(ctx, MetaName, json.RawMessage(`{"action":"describe","names":["edit_file","bogus"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"old_string"`) || !strings.Contains(out, "unknown tools: bogus") {
		t.Errorf("describe: %s", out)
	}
	if !slices.Equal(r.Active(), []string{"edit_file"}) {
		t.Errorf("active %v", r.Active())
	}
	if _, err := r.Call(ctx, MetaName, json.RawMessage(`{"action":"describe"}`)); err == nil {
		t.Error("describe without names should fail")
	}
	if _, err := r.Call(ctx, "bogus", nil); err == nil {
		t.Error("unknown tool should fail")
	}
}

func TestProjectTools(t *testing.T) {
	root := t.TempDir()
	r := NewRegistry(Project(root)...)
	ctx := context.Background()
	call := func(name, args string) string {
		t.Helper()
		out, err := r.Call(ctx, name, json.RawMessage(args))
		if err != nil {
			t.Fatalf("%s %s: %v", name, args, err)
		}
		return out
	}
	call("write_file", `{"path":"a/b.txt","content":"one\ntwo\nthree\n"}`)
	if out := call("read_file", `{"path":"a/b.txt","start_line":2,"end_line":2}`); !strings.Contains(out, `"content": "two"`) {
		t.Errorf("read: %s", out)
	}
	if out := call("edit_file", `{"path":"a/b.txt","old_string":"two","new_string":"2"}`); !strings.Contains(out, `"replacements": 1`) {
		t.Errorf("edit: %s", out)
	}
	if _, err := r.Call(ctx, "edit_file", json.RawMessage(`{"path":"a/b.txt","old_string":"zzz","new_string":"2"}`)); err == nil {
		t.Error("edit of a missing string should fail")
	}
	if out := call("grep", `{"pattern":"^2$"}`); !strings.Contains(out, "a/b.txt:2: 2") {
		t.Errorf("grep: %s", out)
	}
	if out := call("glob", `{"pattern":"a/*.txt"}`); !strings.Contains(out, `"a/b.txt"`) {
		t.Errorf("glob: %s", out)
	}
	if out := call("list_dir", `{"path":"a"}`); !strings.Contains(out, `"name": "b.txt"`) {
		t.Errorf("list: %s", out)
	}
	call("move_file", `{"from":"a/b.txt","to":"c/d.txt"}`)
	call("copy_file", `{"from":"c/d.txt","to":"c/e.txt"}`)
	call("mkdir", `{"path":"m/n"}`)
	call("delete", `{"path":"c","recursive":true}`)
	if _, err := os.Stat(filepath.Join(root, "c")); !os.IsNotExist(err) {
		t.Error("c not deleted")
	}
	if _, err := r.Call(ctx, "delete", json.RawMessage(`{"path":".","recursive":true}`)); err == nil {
		t.Error("deleting the project should fail")
	}
	if _, err := r.Call(ctx, "read_file", json.RawMessage(`{"path":"../x"}`)); err == nil {
		t.Error("escape should fail")
	}
	if out := call("run_command", `{"command":"echo hi; exit 3"}`); !strings.Contains(out, `"exit_code": 3`) || !strings.Contains(out, "hi") {
		t.Errorf("run: %s", out)
	}
}
