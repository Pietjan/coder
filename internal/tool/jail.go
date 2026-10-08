package tool

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Jail keeps every path inside one directory. Copied from toto's
// resolveRoot/relRoot: a cleaned-prefix check that does not follow
// symlinks.
type Jail struct{ Root string }

// NewJail makes Root absolute.
func NewJail(root string) Jail {
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = filepath.Clean(root)
	}
	return Jail{Root: abs}
}

// Resolve turns a project-relative (or absolute, inside the project) path
// into an absolute one, refusing anything that escapes the root.
func (j Jail) Resolve(path string) (string, error) {
	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(j.Root, path)
	}
	abs = filepath.Clean(abs)
	if abs != j.Root && !strings.HasPrefix(abs, j.Root+string(os.PathSeparator)) {
		return "", fmt.Errorf("path %q escapes the project", path)
	}
	return abs, nil
}

// Rel renders an absolute path project-relative with forward slashes.
func (j Jail) Rel(abs string) string {
	rel, err := filepath.Rel(j.Root, abs)
	if err != nil {
		return abs
	}
	return filepath.ToSlash(rel)
}
