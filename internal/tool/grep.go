package tool

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
)

// maxGrepFileSize skips files larger than this; grepping multi-megabyte
// blobs is rarely what an agent wants and keeps the walk bounded.
const maxGrepFileSize = 8 << 20 // 8 MiB

// GrepFiles returns the lines under root whose contents match the regular
// expression pattern, in lexical path order (WalkDir order), each with its
// 1-based line number. Directories, unreadable files, oversized files, and
// binary files (those containing a NUL byte) are skipped. At most limit
// matches are returned; truncated reports whether the cap cut the search
// short. Match paths are absolute (relative to nothing); the hub layer
// re-bases them onto the project root. Pure Go — no platform-specific
// dependencies.
func GrepFiles(root, pattern string, limit int) (matches []GrepMatch, truncated bool, err error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, false, err
	}

	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil //nolint:nilerr // skip entries we can't stat, keep walking
		}
		if !info.Mode().IsRegular() || info.Size() > maxGrepFileSize {
			return nil
		}
		data, err := os.ReadFile(path) //nolint:gosec // G304: grepping caller files is the purpose; paths are jailed at the hub layer
		if err != nil {
			return nil //nolint:nilerr // unreadable file: skip, don't fail the whole search
		}
		if bytes.IndexByte(data, 0) != -1 {
			return nil // binary file
		}
		for i, line := range bytes.Split(data, []byte("\n")) {
			if !re.Match(line) {
				continue
			}
			if len(matches) >= limit {
				truncated = true
				return filepath.SkipAll
			}
			matches = append(matches, GrepMatch{
				Path: path,
				Line: i + 1,
				Text: string(bytes.TrimSuffix(line, []byte("\r"))),
			})
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return matches, truncated, nil
}
