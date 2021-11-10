package staticfsys

import (
	"bytes"
	"errors"
	"io"
	"strings"
)

func AlwaysOpen[Content any](opener func(c Content) (File, error)) func(aname string) (func(Content) (File, error), error) {
	return func(aname string) (func(Content) (File, error), error) {
		return opener, nil
	}
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

var ErrReadOnly = errors.New("read only file")

type NopCloser struct{}

func (NopCloser) Close() error {
	return nil
}

type ErrorWriter struct{}

func (ErrorWriter) WriteAt(buf []byte, off int64) (int, error) {
	return 0, ErrReadOnly
}
