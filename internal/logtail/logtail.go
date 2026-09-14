// Package logtail reads the tail of agent log files with a byte cap
// (shared by the inbox preview and the dispatch blocked report).
package logtail

import (
	"io"
	"os"
	"strings"
)

// Tail returns up to the last maxLines lines of path, reading at most
// maxBytes from the file end. Missing files, empty logs, and non-positive
// limits yield "". The result keeps its trailing newline when present;
// a partial first line (byte-cap cut) is dropped so output starts clean.
func Tail(path string, maxLines int, maxBytes int64) string {
	if path == "" || maxLines <= 0 || maxBytes <= 0 {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil || stat.Size() == 0 {
		return ""
	}
	offset := int64(0)
	if stat.Size() > maxBytes {
		offset = stat.Size() - maxBytes
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return ""
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return ""
	}
	if offset > 0 {
		if i := strings.IndexByte(string(data), '\n'); i >= 0 {
			data = data[i+1:]
		} else {
			return ""
		}
	}
	lines := strings.Split(string(data), "\n")
	// Drop the trailing empty element from a final newline, keeping the
	// newline itself on the last content line.
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}
