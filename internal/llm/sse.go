package llm

import (
	"bufio"
	"bytes"
	"io"
)

// readEvents calls fn with the data of every server-sent event in r until
// the stream ends or fn returns an error. Multi-line data is joined with
// newlines, as the SSE spec says; comments and other fields are ignored.
func readEvents(r io.Reader, fn func(data []byte) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	var data [][]byte
	flush := func() error {
		if len(data) == 0 {
			return nil
		}
		d := bytes.Join(data, []byte("\n"))
		data = data[:0]
		return fn(d)
	}
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		if line[0] == ':' {
			continue
		}
		if rest, ok := bytes.CutPrefix(line, []byte("data:")); ok {
			data = append(data, bytes.Clone(bytes.TrimPrefix(rest, []byte(" "))))
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return flush()
}
