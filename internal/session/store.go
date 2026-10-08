// Package session keeps conversations on disk: one append-only JSONL
// file per session, one agent.Event per line, the first line being the
// session header. Resume reads the file back and rebuilds the history
// from it.
package session

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pietjan/coder/internal/agent"
)

// Meta is what the sessions list shows.
type Meta struct {
	ID      string
	Project string
	Model   string
	Title   string // the first user message, shortened
	Created time.Time
	Updated time.Time
}

// Store is a directory of session files.
type Store struct{ Dir string }

// DefaultDir is ~/.coder/sessions.
func DefaultDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".coder", "sessions")
}

// Open makes the directory and returns the store.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	return &Store{Dir: dir}, nil
}

func (s *Store) path(id string) string { return filepath.Join(s.Dir, id+".jsonl") }

// ErrBadID rejects ids that are not of our making.
var ErrBadID = errors.New("bad session id")

func checkID(id string) error {
	if id == "" || strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return fmt.Errorf("%w: %q", ErrBadID, id)
	}
	return nil
}

// List returns every session, newest update first.
func (s *Store) List() ([]Meta, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return nil, err
	}
	var out []Meta
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		m, err := s.meta(strings.TrimSuffix(name, ".jsonl"))
		if err != nil {
			continue // a foreign or broken file is not a session
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out, nil
}

// meta reads just enough of a file for the list: the header, the first
// user line, and the mtime.
func (s *Store) meta(id string) (Meta, error) {
	f, err := os.Open(s.path(id))
	if err != nil {
		return Meta{}, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return Meta{}, err
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	var m Meta
	for sc.Scan() {
		var e agent.Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			break
		}
		switch e.Kind {
		case agent.KindSession:
			if e.ID != id {
				return Meta{}, fmt.Errorf("session %s: header names %q", id, e.ID)
			}
			m = Meta{ID: e.ID, Project: e.Project, Model: e.Model, Created: e.At, Updated: info.ModTime()}
		case agent.KindUser:
			m.Title = title(e.Text)
			return m, nil
		}
	}
	if m.ID == "" {
		return Meta{}, fmt.Errorf("session %s: no header", id)
	}
	m.Title = "(empty)"
	return m, nil
}

func title(text string) string {
	text = strings.TrimSpace(strings.SplitN(text, "\n", 2)[0])
	if r := []rune(text); len(r) > 60 {
		return string(r[:60]) + "…"
	}
	return text
}

// Create starts a new session file and returns its writer.
func (s *Store) Create(project, model string) (*Writer, Meta, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, Meta{}, err
	}
	now := time.Now()
	id := now.Format("20060102T150405") + "-" + hex.EncodeToString(b[:])
	f, err := os.OpenFile(s.path(id), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, Meta{}, err
	}
	w := &Writer{f: f, bw: bufio.NewWriter(f)}
	if _, err := w.Append(agent.Event{Kind: agent.KindSession, ID: id, Project: project, Model: model, At: now}); err != nil {
		_ = f.Close()
		return nil, Meta{}, err
	}
	return w, Meta{ID: id, Project: project, Model: model, Title: "(empty)", Created: now, Updated: now}, nil
}

// Open reads a session's events and returns a writer that appends to it.
func (s *Store) Open(id string) (*Writer, []agent.Event, error) {
	if err := checkID(id); err != nil {
		return nil, nil, err
	}
	events, err := Read(s.path(id))
	if err != nil {
		return nil, nil, err
	}
	if len(events) == 0 || events[0].Kind != agent.KindSession {
		return nil, nil, fmt.Errorf("session %s: no header", id)
	}
	f, err := os.OpenFile(s.path(id), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, nil, err
	}
	return &Writer{f: f, bw: bufio.NewWriter(f), seq: events[len(events)-1].Seq}, events, nil
}

// Read parses a session file. A corrupt last line (a crash mid-write) is
// dropped; corruption anywhere else is an error.
func Read(path string) ([]agent.Event, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: the store's own directory
	if err != nil {
		return nil, err
	}
	lines := bytes.Split(data, []byte("\n"))
	var out []agent.Event
	for i, line := range lines {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var e agent.Event
		if err := json.Unmarshal(line, &e); err != nil {
			if i >= len(lines)-2 {
				break
			}
			return nil, fmt.Errorf("%s line %d: %w", filepath.Base(path), i+1, err)
		}
		out = append(out, e)
	}
	return out, nil
}

// Writer appends events to one session file.
type Writer struct {
	mu  sync.Mutex
	f   *os.File
	bw  *bufio.Writer
	seq int64
}

// Append writes e as one line, numbering and timestamping it, and
// flushes. It returns the event as written.
func (w *Writer) Append(e agent.Event) (agent.Event, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.seq++
	e.Seq = w.seq
	if e.At.IsZero() {
		e.At = time.Now()
	}
	b, err := json.Marshal(e)
	if err != nil {
		return e, err
	}
	if _, err := w.bw.Write(append(b, '\n')); err != nil {
		return e, err
	}
	return e, w.bw.Flush()
}

// Close flushes and closes the file.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.bw.Flush(); err != nil {
		_ = w.f.Close()
		return err
	}
	return w.f.Close()
}
