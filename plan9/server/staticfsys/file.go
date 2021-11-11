package staticfsys

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
)

func AlwaysOpen[Content any](opener func(c Content) (File, error)) func(aname string) (func(Content) (File, error), error) {
	return func(aname string) (func(Content) (File, error), error) {
		return opener, nil
	}
}

// OpenBuffer returns a File implementation that treats buf as a
// regular writeable file.
// TODO provide access to the contents somehow?
func NewBuffer(maxSize int) File {
	return &bufFile{
		maxSize: maxSize,
	}
}

type bufFile struct {
	NopCloser
	maxSize int
	mu      sync.Mutex
	buf     []byte
}

func (f *bufFile) WriteAt(buf []byte, off int64) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if off < 0 {
		return 0, fmt.Errorf("negative file offset")
	}
	if off+int64(len(buf)) > int64(f.maxSize) {
		return 0, fmt.Errorf("max file size exceeded")
	}
	start := int(off)
	if start+len(buf) > len(f.buf) {
		buf1 := make([]byte, start+len(buf))
		copy(buf1, f.buf)
		f.buf = buf1
	}
	copy(f.buf[start:], buf)
	return len(buf), nil
}

func (f *bufFile) ReadAt(buf []byte, off int64) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if off >= int64(len(f.buf)) {
		return 0, io.EOF
	}
	if off < 0 {
		return 0, fmt.Errorf("negative file offset")
	}
	return copy(buf, f.buf[off:]), nil
}

func OpenString(s string) (File, error) {
	return struct {
		io.WriterAt
		io.Closer
		io.ReaderAt
	}{
		ErrorWriter{},
		NopCloser{},
		strings.NewReader(s),
	}, nil
}

func OpenBytes(b []byte) (File, error) {
	return struct {
		io.WriterAt
		io.Closer
		io.ReaderAt
	}{
		ErrorWriter{},
		NopCloser{},
		bytes.NewReader(b),
	}, nil
}

var ErrReadOnly = errors.New("read-only file")
var ErrWriteOnly = errors.New("write-only file")

type NopCloser struct{}

func (NopCloser) Close() error {
	return nil
}

type ErrorWriter struct{}

func (ErrorWriter) WriteAt(buf []byte, off int64) (int, error) {
	return 0, ErrReadOnly
}

type ErrorReader struct{}

func (ErrorReader) ReadAt(buf []byte, off int64) (int, error) {
	return 0, ErrWriteOnly
}
