package clonefsys_test

import (
	"context"
	"fmt"
	"io"
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
	staticFS, err := staticfsys.New(staticfsys.Params[entryType]{
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
	})
	qt.Assert(t, err, qt.IsNil)
	cloneFS := clonefsys.New(server.Fsys[*staticfsys.Fid[entryType]](staticFS))
	n := cloneFS.AddDir(staticFS.Root(func(e entryType) (staticfsys.File, error) {
		return staticfsys.OpenString(fmt.Sprintf("first clone %v", e))
	}))
	qt.Assert(t, n, qt.Equals, 0)
	n = cloneFS.AddDir(staticFS.Root(func(e entryType) (staticfsys.File, error) {
		return staticfsys.OpenString(fmt.Sprintf("second clone %v", e))
	}))
	qt.Assert(t, n, qt.Equals, 1)
	c0, c1 := net.Pipe()
	errc := make(chan error, 1)
	go func() {
		err := server.Serve(context.Background(), c0, server.Fsys[*clonefsys.Fid[*staticfsys.Fid[entryType]]](cloneFS))
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
	qt.Assert(t, readFile(t, fs1, "0/foo"), qt.Equals, `first clone foo content`)
	qt.Assert(t, readFile(t, fs1, "1/info/version"), qt.Equals, `second clone version content`)

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
