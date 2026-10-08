package tool

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGrepFiles(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("a.go", "package main\nfunc Hello() {}\n")
	write("sub/b.go", "// Hello there\r\n")
	write("c.txt", "nothing here\n")
	write("bin.dat", "Hello\x00binary")

	matches, truncated, err := GrepFiles(root, `Hello`, 100)
	if err != nil {
		t.Fatal(err)
	}
	if truncated {
		t.Error("unexpected truncation")
	}
	// a.go line 2 and sub/b.go line 1 match; c.txt doesn't; bin.dat is binary (skipped)
	if len(matches) != 2 {
		t.Fatalf("matches = %v, want 2", matches)
	}
	if filepath.Base(matches[0].Path) != "a.go" || matches[0].Line != 2 || matches[0].Text != "func Hello() {}" {
		t.Errorf("first match = %+v", matches[0])
	}
	if filepath.Base(matches[1].Path) != "b.go" || matches[1].Line != 1 || matches[1].Text != "// Hello there" {
		t.Errorf("second match = %+v (CR should be stripped)", matches[1])
	}
}

func TestGrepFilesLimit(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "many.txt"), []byte("x\nx\nx\nx\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	matches, truncated, err := GrepFiles(root, "x", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 || !truncated {
		t.Errorf("matches = %d truncated = %v, want 2 truncated", len(matches), truncated)
	}
}

func TestGrepFilesInvalidPattern(t *testing.T) {
	if _, _, err := GrepFiles(t.TempDir(), "(", 10); err == nil {
		t.Fatal("expected error for invalid regex")
	}
}

func TestGrepFilesSkipsOversized(t *testing.T) {
	root := t.TempDir()
	big := make([]byte, maxGrepFileSize+1)
	copy(big, "match")
	if err := os.WriteFile(filepath.Join(root, "big.txt"), big, 0o600); err != nil {
		t.Fatal(err)
	}
	matches, _, err := GrepFiles(root, "match", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Errorf("oversized file should be skipped, got %v", matches)
	}
}
