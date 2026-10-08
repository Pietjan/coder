package tool

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFixture(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestEditFile(t *testing.T) {
	t.Run("unique match replaces once", func(t *testing.T) {
		p := writeFixture(t, "a b c")
		n, err := EditFile(p, "b", "B", false)
		if err != nil || n != 1 {
			t.Fatalf("n = %d, err %v", n, err)
		}
		got, _ := ReadFile(p)
		if got != "a B c" {
			t.Errorf("content = %q", got)
		}
	})

	t.Run("absent match is ErrNotFound", func(t *testing.T) {
		p := writeFixture(t, "a b c")
		if _, err := EditFile(p, "zzz", "x", false); !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})

	t.Run("ambiguous match is ErrAmbiguous without replaceAll", func(t *testing.T) {
		p := writeFixture(t, "x y x")
		if _, err := EditFile(p, "x", "z", false); !errors.Is(err, ErrAmbiguous) {
			t.Fatalf("err = %v, want ErrAmbiguous", err)
		}
		got, _ := ReadFile(p)
		if got != "x y x" {
			t.Errorf("file mutated on error: %q", got)
		}
	})

	t.Run("replaceAll replaces every occurrence", func(t *testing.T) {
		p := writeFixture(t, "x y x")
		n, err := EditFile(p, "x", "z", true)
		if err != nil || n != 2 {
			t.Fatalf("n = %d, err %v", n, err)
		}
		got, _ := ReadFile(p)
		if got != "z y z" {
			t.Errorf("content = %q", got)
		}
	})
}

func TestReadFileLines(t *testing.T) {
	p := writeFixture(t, "one\ntwo\nthree\n")

	t.Run("full file", func(t *testing.T) {
		s, err := ReadFileLines(p, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		if s.TotalLines != 3 || s.Content != "one\ntwo\nthree" || s.Truncated {
			t.Errorf("slice = %+v", s)
		}
	})

	t.Run("inclusive range", func(t *testing.T) {
		s, err := ReadFileLines(p, 2, 3)
		if err != nil {
			t.Fatal(err)
		}
		if s.Content != "two\nthree" || s.TotalLines != 3 {
			t.Errorf("slice = %+v", s)
		}
	})

	t.Run("end past EOF clamps", func(t *testing.T) {
		s, err := ReadFileLines(p, 3, 99)
		if err != nil {
			t.Fatal(err)
		}
		if s.Content != "three" {
			t.Errorf("slice = %+v", s)
		}
	})

	t.Run("start past EOF is empty not an error", func(t *testing.T) {
		s, err := ReadFileLines(p, 10, 20)
		if err != nil {
			t.Fatal(err)
		}
		if s.Content != "" || s.TotalLines != 3 {
			t.Errorf("slice = %+v", s)
		}
	})

	t.Run("size cap truncates at a line boundary", func(t *testing.T) {
		line := strings.Repeat("a", 1024)
		big := strings.Repeat(line+"\n", maxReadSize/1024+10)
		bp := writeFixture(t, big)
		s, err := ReadFileLines(bp, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		if !s.Truncated || len(s.Content) > maxReadSize {
			t.Errorf("truncated = %v, len = %d", s.Truncated, len(s.Content))
		}
		if strings.HasSuffix(s.Content, "\n") || !strings.HasSuffix(s.Content, "a") {
			t.Errorf("cut mid-structure: %q...", s.Content[len(s.Content)-8:])
		}
	})
}

func TestWriteFileAtomicWithParents(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "deep", "nested", "f.txt")
	if err := WriteFile(p, "hello"); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFile(p)
	if err != nil || got != "hello" {
		t.Fatalf("content = %q, err %v", got, err)
	}
	// overwrite goes through the same atomic path
	if err := WriteFile(p, "world"); err != nil {
		t.Fatal(err)
	}
	got, _ = ReadFile(p)
	if got != "world" {
		t.Errorf("content = %q", got)
	}
	// no temp files left behind
	entries, err := os.ReadDir(filepath.Dir(p))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("leftover temp file %s", e.Name())
		}
	}
}

func TestDelete(t *testing.T) {
	t.Run("file", func(t *testing.T) {
		p := writeFixture(t, "x")
		if err := Delete(p, false); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Error("file still exists")
		}
	})

	t.Run("missing path is not an error", func(t *testing.T) {
		if err := Delete(filepath.Join(t.TempDir(), "nope"), false); err != nil {
			t.Fatal(err)
		}
		if err := Delete(filepath.Join(t.TempDir(), "nope"), true); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("non-empty dir needs recursive", func(t *testing.T) {
		dir := t.TempDir()
		sub := filepath.Join(dir, "sub")
		if err := os.MkdirAll(sub, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sub, "f"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := Delete(sub, false); err == nil {
			t.Fatal("expected error deleting non-empty dir without recursive")
		}
		if err := Delete(sub, true); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(sub); !errors.Is(err, os.ErrNotExist) {
			t.Error("dir still exists")
		}
	})

	t.Run("empty dir deletes without recursive", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "empty")
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := Delete(dir, false); err != nil {
			t.Fatal(err)
		}
	})
}
