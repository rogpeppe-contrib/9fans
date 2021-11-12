package clonefsys_test

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"testing"

	qt "github.com/frankban/quicktest"

	"9fans.net/go/plan9"
	"9fans.net/go/plan9/client"
	"9fans.net/go/plan9/server"
	"9fans.net/go/plan9/server/clonefsys"
	"9fans.net/go/plan9/server/staticfsys"
)

type entryType int

const (
	entryFoo entryType = iota
	entryInfoVersion
	entryInfoOther
)

func (e entryType) String() string {
	switch e {
	case entryFoo:
		return "foo content"
	case entryInfoVersion:
		return "version content"
	case entryInfoOther:
		return "other content"
	}
	panic("unreachable")
}

type entry = staticfsys.Entry[entryType]

func TestCloneStatic(t *testing.T) {
	staticFS, err := staticfsys.New(staticfsys.Params[int, entryType]{
		Root: map[string]entry{
			"foo": {
				Content: entryFoo,
			},
			"info": {
				Entries: map[string]entry{
					"version": {
						Content: entryInfoVersion,
					},
					"other": {
						Content: entryInfoOther,
					},
				},
			},
		},
		Open: func(f *staticfsys.Fid[int, entryType]) (staticfsys.File, error) {
			return staticfsys.OpenString(fmt.Sprintf("clone %d, entry %v", f.Context(), f.Content()))
		},
	})
	qt.Assert(t, err, qt.IsNil)
	cloneFS := clonefsys.New(staticFS, func(struct{}) clonefsys.Provider[int] {
		return newSimpleProvider(2, func(i int) (int, bool) {
			return i, true
		})
	})
	c0, c1 := net.Pipe()
	errc := make(chan error, 1)
	go func() {
		// TODO This type conversion won't be necessary if/when https://github.com/golang/go/issues/41176
		// is fixed.
		err := server.Serve[*clonefsys.Fid[*staticfsys.Fid[int, entryType], struct{}]](context.Background(), c0, cloneFS)
		t.Logf("Serve finished; error: %v", err)
		c0.Close()
		errc <- err
	}()
	c, err := client.NewConn(c1)
	qt.Assert(t, err, qt.IsNil)
	defer c.Close()
	fs1, err := c.Attach(nil, "rog", "xxx")
	qt.Assert(t, err, qt.IsNil)

	// Try reading some files
	qt.Assert(t, readFile(t, fs1, "0/foo"), qt.Equals, `clone 0, entry foo content`)
	qt.Assert(t, readFile(t, fs1, "1/info/version"), qt.Equals, `clone 1, entry version content`)

	err = fs1.Close()
	qt.Assert(t, err, qt.IsNil)
	c.Release()
	err = <-errc
	qt.Assert(t, err, qt.IsNil)
}

func TestCloneNested(t *testing.T) {
	type fcontext struct {
		inner int
		outer int
	}
	staticFS, err := staticfsys.New(staticfsys.Params[fcontext, entryType]{
		Root: map[string]entry{
			"foo": {
				Content: entryFoo,
			},
		},
		Open: func(f *staticfsys.Fid[fcontext, entryType]) (staticfsys.File, error) {
			return staticfsys.OpenString(fmt.Sprintf("clone %d/%d, entry %v", f.Context().outer, f.Context().inner, f.Content()))
		},
	})
	qt.Assert(t, err, qt.IsNil)
	cloneFS1 := clonefsys.New(staticFS, func(outer int) clonefsys.Provider[fcontext] {
		return newSimpleProvider(2, func(inner int) (fcontext, bool) {
			return fcontext{
				inner: inner,
				outer: outer,
			}, true
		})
	})
	cloneFS0 := clonefsys.New(cloneFS1, func(struct{}) clonefsys.Provider[int] {
		return newSimpleProvider(3, func(i int) (int, bool) {
			return i, true
		})
	})
	c0, c1 := net.Pipe()
	errc := make(chan error, 1)
	go func() {
		// TODO This type conversion won't be necessary if/when https://github.com/golang/go/issues/41176
		// is fixed.
		err := server.Serve[*clonefsys.Fid[*clonefsys.Fid[*staticfsys.Fid[fcontext, entryType], int], struct{}]](context.Background(), c0, cloneFS0)
		t.Logf("Serve finished; error: %v", err)
		c0.Close()
		errc <- err
	}()
	c, err := client.NewConn(c1)
	qt.Assert(t, err, qt.IsNil)
	defer c.Close()
	fs1, err := c.Attach(nil, "rog", "xxx")
	qt.Assert(t, err, qt.IsNil)

	// Try reading some files
	qt.Assert(t, readFile(t, fs1, "2/1/foo"), qt.Equals, `clone 2/1, entry foo content`)

	err = fs1.Close()
	qt.Assert(t, err, qt.IsNil)
	c.Release()
	err = <-errc
	qt.Assert(t, err, qt.IsNil)
}

func readFile(t *testing.T, fs *client.Fsys, name string) string {
	f, err := fs.Open(name, plan9.OREAD)
	qt.Assert(t, err, qt.IsNil)
	defer func() {
		if err := f.Close(); err != nil {
			t.Errorf("close failed: %v", err)
		}
	}()
	data, err := io.ReadAll(f)
	qt.Assert(t, err, qt.IsNil)
	return string(data)
}

type simpleProvider[T any] struct {
	n   int
	get func(i int) (T, bool)
}

func newSimpleProvider[T any](n int, get func(i int) (T, bool)) clonefsys.Provider[T] {
	return simpleProvider[T]{
		n:   n,
		get: get,
	}
}

func (p simpleProvider[T]) Len() int {
	return p.n
}

func (p simpleProvider[T]) Get(id int) (T, bool) {
	log.Printf("%T.Get %d (n %d)", p, id, p.n)
	if id < 0 || id >= p.n {
		return *new(T), false
	}
	return p.get(id)
}
