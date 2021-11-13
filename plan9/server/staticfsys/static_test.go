package staticfsys_test

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

func TestServerReadWithThreadedData(t *testing.T) {
	type attachData struct {
		aname string
		other staticfsys.File
	}
	fs0, err := staticfsys.New(staticfsys.Params[attachData, entryType]{
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
		ContextForAttach: func(uname, aname string) (attachData, error) {
			return attachData{
				aname: aname,
				other: staticfsys.NewBuffer(1024),
			}, nil
		},
		Open: func(f *staticfsys.Fid[attachData, entryType]) (staticfsys.File, error) {
			c := f.Context()
			t := f.Content()
			if t == entryInfoOther {
				return c.other, nil
			}
			return staticfsys.OpenString(fmt.Sprintf("aname=%q %v", c.aname, t))
		},
	})
	qt.Assert(t, err, qt.IsNil)
	c0, c1 := net.Pipe()
	errc := make(chan error, 1)
	go func() {
		err := server.Serve(context.Background(), c0, server.Fsys[staticfsys.Fid[attachData, entryType]](fs0))
		t.Logf("Serve finished; error: %v", err)
		c0.Close()
		errc <- err
	}()
	c, err := client.NewConn(c1)
	qt.Assert(t, err, qt.IsNil)
	defer c.Close()
	fs1, err := c.Attach(nil, "rog", "xxx")
	qt.Assert(t, err, qt.IsNil)

	// Try reading a file.
	f, err := fs1.Open("/foo", plan9.OREAD)
	qt.Assert(t, err, qt.IsNil)
	data, err := io.ReadAll(f)
	qt.Assert(t, err, qt.IsNil)
	qt.Check(t, string(data), qt.Equals, `aname="xxx" foo content`)
	err = f.Close()
	qt.Assert(t, err, qt.IsNil)

	// Try the rewritable file..
	f, err = fs1.Open("/info/other", plan9.ORDWR)
	qt.Assert(t, err, qt.IsNil)

	data, err = io.ReadAll(f)
	qt.Assert(t, err, qt.IsNil)
	qt.Check(t, string(data), qt.Equals, "")

	n, err := f.Write([]byte("some content"))
	qt.Assert(t, err, qt.IsNil)
	qt.Assert(t, n, qt.Equals, len("some content"))
	f.Seek(0, 0)

	data, err = io.ReadAll(f)
	qt.Assert(t, err, qt.IsNil)
	qt.Check(t, string(data), qt.Equals, "some content")

	err = f.Close()
	qt.Assert(t, err, qt.IsNil)

	// Check that the content is still there when the file is reopened.
	f, err = fs1.Open("/info/other", plan9.ORDWR)
	qt.Assert(t, err, qt.IsNil)
	data, err = io.ReadAll(f)
	qt.Assert(t, err, qt.IsNil)
	qt.Check(t, string(data), qt.Equals, "some content")
	err = f.Close()
	qt.Assert(t, err, qt.IsNil)

	// Check that the content is different in a different attach session.
	fs2, err := c.Attach(nil, "rog", "yyy")
	qt.Assert(t, err, qt.IsNil)
	f, err = fs2.Open("/info/other", plan9.OREAD)
	qt.Assert(t, err, qt.IsNil)
	data, err = io.ReadAll(f)
	qt.Assert(t, err, qt.IsNil)
	qt.Check(t, string(data), qt.Equals, ``)
	err = f.Close()
	qt.Assert(t, err, qt.IsNil)
	err = fs2.Close()
	qt.Assert(t, err, qt.IsNil)

	err = fs1.Close()
	qt.Assert(t, err, qt.IsNil)
	c.Release()
	err = <-errc
	qt.Assert(t, err, qt.IsNil)
}
