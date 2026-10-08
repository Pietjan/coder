// fileops.go: project-scoped file operations, copied from toto's
// internal/fileops. A thin, platform-portable layer over the os package;
// path containment (keeping operations inside a project directory) is
// Jail's job, in project.go.

package tool

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DirEntry describes one entry returned by a directory listing.
type DirEntry struct {
	Name    string    `json:"name"`
	IsDir   bool      `json:"is_dir,omitempty"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}

// FileSlice is the result of reading (a line range of) a file. Truncated
// is set when the content was cut short by the read size cap rather than
// by the requested range.
type FileSlice struct {
	Content    string
	TotalLines int
	Truncated  bool
}

// GrepMatch is one matching line from a content search.
type GrepMatch struct {
	Path string // as walked; Project re-bases it onto the project root
	Line int    // 1-based
	Text string
}

// ErrNotFound is returned by EditFile when the search string is absent.
var ErrNotFound = errors.New("string not found in file")

// ErrAmbiguous is returned by EditFile when the search string occurs more
// than once and replaceAll is false.
var ErrAmbiguous = errors.New("string occurs more than once in file")

// maxReadSize caps how much file content a single read returns; agents
// should narrow with a line range instead of paging megabytes.
const maxReadSize = 256 << 10 // 256 KiB

// ReadFile reads the entire contents of a file
func ReadFile(path string) (string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: reading caller paths is the purpose; jailed at the hub layer
	return string(data), err
}

// ReadFileLines reads a 1-based inclusive line range of a file. start <= 0
// means from the first line; end <= 0 or past EOF means through the last
// line. The result is truncated (and flagged) when it exceeds maxReadSize.
func ReadFileLines(path string, start, end int) (FileSlice, error) {
	content, err := ReadFile(path)
	if err != nil {
		return FileSlice{}, err
	}

	lines := strings.Split(content, "\n")
	// a trailing newline yields one empty phantom line; don't count it
	total := len(lines)
	if total > 0 && lines[total-1] == "" && strings.HasSuffix(content, "\n") {
		total--
	}

	if start <= 0 {
		start = 1
	}
	if end <= 0 || end > total {
		end = total
	}
	if start > total || start > end {
		return FileSlice{TotalLines: total}, nil
	}

	slice := strings.Join(lines[start-1:end], "\n")
	truncated := false
	if len(slice) > maxReadSize {
		cut := slice[:maxReadSize]
		if i := strings.LastIndexByte(cut, '\n'); i > 0 {
			cut = cut[:i]
		}
		slice = cut
		truncated = true
	}
	return FileSlice{Content: slice, TotalLines: total, Truncated: truncated}, nil
}

// WriteFile writes content to a file atomically (temp file + rename in the
// same directory), creating parent directories as needed.
func WriteFile(path, content string) error {
	if err := MkdirAll(filepath.Dir(path)); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }() // cleanup on failure

	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// EditFile replaces oldStr with newStr in a file and reports how many
// replacements were made. Without replaceAll the match must be unique:
// zero occurrences is ErrNotFound, more than one is ErrAmbiguous.
func EditFile(path, oldStr, newStr string, replaceAll bool) (int, error) {
	content, err := ReadFile(path)
	if err != nil {
		return 0, err
	}
	count := strings.Count(content, oldStr)
	switch {
	case count == 0:
		return 0, ErrNotFound
	case count > 1 && !replaceAll:
		return 0, ErrAmbiguous
	}
	if err := WriteFile(path, strings.ReplaceAll(content, oldStr, newStr)); err != nil {
		return 0, err
	}
	return count, nil
}

// ListDir returns files in a directory
func ListDir(path string) ([]DirEntry, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	files := make([]DirEntry, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			return nil, err
		}
		files = append(files, DirEntry{
			Name:    e.Name(),
			IsDir:   e.IsDir(),
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
	}
	return files, nil
}

// Glob finds files matching a pattern
func Glob(pattern string) ([]string, error) {
	return filepath.Glob(pattern)
}

// MkdirAll creates a directory and parents
func MkdirAll(path string) error {
	return os.MkdirAll(path, 0o750)
}

// Delete removes a file, or a directory when recursive is set. Deleting a
// missing path is not an error; deleting a non-empty directory without
// recursive is.
func Delete(path string, recursive bool) error {
	if recursive {
		info, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.IsDir() {
			return os.RemoveAll(path)
		}
	}
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// MoveFile renames/moves a file (atomic on same filesystem)
func MoveFile(src, dst string) error {
	return os.Rename(src, dst)
}

// CopyFile copies a file (preserves mode)
func CopyFile(src, dst string) (err error) {
	in, err := os.Open(src) //nolint:gosec // G304: operating on caller paths is the purpose; jailed at the hub layer
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	info, err := in.Stat()
	if err != nil {
		return err
	}

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode()) //nolint:gosec // G304: see above
	if err != nil {
		return err
	}
	// a failed close can mean lost writes, so surface it if Copy didn't
	// already fail
	defer func() {
		if cerr := out.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	_, err = io.Copy(out, in)
	return err
}
