package server

import (
	"context"
	"io"
	"net"
	"testing"

	"9fans.net/go/plan9"
	"9fans.net/go/plan9/client"
	qt "github.com/frankban/quicktest"
)

func TestServer(t *testing.T) {
	fs0, err := NewStaticFsys([]StaticFile{{
		Name:    "foo",
		Content: []byte("bar"),
	}})
	qt.Assert(t, err, qt.IsNil)
	c0, c1 := net.Pipe()
	errc := make(chan error, 1)
	go func() {
		err := Serve(context.Background(), c0, fs0)
		t.Logf("Serve finished; error: %v", err)
		c0.Close()
		errc <- err
	}()
	c, err := client.NewConn(c1)
	qt.Assert(t, err, qt.IsNil)
	defer c.Close()
	fs1, err := c.Attach(nil, "rog", "")
	qt.Assert(t, err, qt.IsNil)
	f, err := fs1.Open("/foo", plan9.OREAD)
	qt.Assert(t, err, qt.IsNil)
	data, err := io.ReadAll(f)
	qt.Assert(t, err, qt.IsNil)
	qt.Assert(t, string(data), qt.Equals, "bar")
	err = f.Close()
	qt.Assert(t, err, qt.IsNil)
	err = fs1.Close()
	qt.Assert(t, err, qt.IsNil)
	c.Release()
	err = <-errc
	qt.Assert(t, err, qt.IsNil)
}
