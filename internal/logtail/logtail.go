// Package logtail reads the end of log files and follows new lines.
package logtail

import (
	"bytes"
	"io"
	"os"
	"strings"
)

// Tail returns the last n lines of a file and the file size at the time of
// reading, which is where a Follower should continue from.
func Tail(path string, n int) ([]string, int64) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, 0
	}
	size := fi.Size()
	const chunk = 256 << 10
	start := max(size-chunk, 0)
	buf := make([]byte, size-start)
	if _, err := f.ReadAt(buf, start); err != nil && err != io.EOF {
		return nil, size
	}
	text := strings.TrimRight(string(buf), "\n")
	if text == "" || n <= 0 {
		return nil, size
	}
	lines := strings.Split(text, "\n")
	if start > 0 && len(lines) > 1 {
		lines = lines[1:] // the first line is probably cut in half
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, size
}

// Follower returns complete lines appended to a file since the last read.
type Follower struct {
	path    string
	offset  int64
	partial []byte
}

func NewFollower(path string, offset int64) *Follower {
	return &Follower{path: path, offset: offset}
}

func (f *Follower) Read() []string {
	fi, err := os.Stat(f.path)
	if err != nil {
		return nil
	}
	if fi.Size() < f.offset { // truncated or rotated
		f.offset, f.partial = 0, nil
	}
	if fi.Size() == f.offset {
		return nil
	}
	fh, err := os.Open(f.path)
	if err != nil {
		return nil
	}
	defer fh.Close()
	buf := make([]byte, fi.Size()-f.offset)
	n, _ := fh.ReadAt(buf, f.offset)
	f.offset += int64(n)
	data := append(f.partial, buf[:n]...)
	idx := bytes.LastIndexByte(data, '\n')
	if idx < 0 {
		f.partial = data
		return nil
	}
	f.partial = append([]byte(nil), data[idx+1:]...)
	return strings.Split(string(data[:idx]), "\n")
}
