package server_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"path"
	"sort"
	"strings"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/google/go-cmp/cmp/cmpopts"

	"9fans.net/go/plan9"
	"9fans.net/go/plan9/client"
	"9fans.net/go/plan9/server"
	"9fans.net/go/plan9/server/staticfsys"
)

type stringEntry = staticfsys.Entry[string]

func TestServerOpenRead(t *testing.T) {
	fs0, err := staticfsys.New(staticfsys.Params[struct{}, string]{
		Root: map[string]stringEntry{
			"foo": {
				Content: "bar",
			},
			"info": {
				Entries: map[string]stringEntry{
					"version": {
						Content: "something new",
					},
					"other": {
						Content: strings.Repeat("a", 1024*1024),
					},
				},
			},
		},
		Open: func(f *staticfsys.Fid[struct{}, string]) (staticfsys.File, error) {
			return staticfsys.OpenString(f.Content())
		},
	})
	qt.Assert(t, err, qt.IsNil)
	c0, c1 := net.Pipe()
	errc := make(chan error, 1)
	go func() {
		err := server.Serve(context.Background(), c0, server.Fsys[*staticfsys.Fid[struct{}, string]](fs0))
		t.Logf("Serve finished; error: %v", err)
		c0.Close()
		errc <- err
	}()
	c, err := client.NewConn(c1)
	qt.Assert(t, err, qt.IsNil)
	defer c.Close()
	fs1, err := c.Attach(nil, "rog", "")
	qt.Assert(t, err, qt.IsNil)

	// Try reading a file.
	f, err := fs1.Open("/foo", plan9.OREAD)
	qt.Assert(t, err, qt.IsNil)
	data, err := io.ReadAll(f)
	qt.Assert(t, err, qt.IsNil)
	qt.Assert(t, string(data), qt.Equals, "bar")
	err = f.Close()
	qt.Assert(t, err, qt.IsNil)

	f, err = fs1.Open("/info", plan9.OREAD)
	qt.Assert(t, err, qt.IsNil)
	entries, err := f.Dirreadall()
	qt.Assert(t, err, qt.IsNil)
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})
	qt.Assert(t, entries, qt.DeepEquals, []*plan9.Dir{{
		Name: "other",
		Qid: plan9.Qid{
			Path: 4,
		},
		Uid:  "noone",
		Gid:  "noone",
		Mode: 0o444,
	}, {
		Name: "version",
		Qid: plan9.Qid{
			Path: 5,
		},
		Uid:  "noone",
		Gid:  "noone",
		Mode: 0o444,
	}})
	err = f.Close()
	qt.Assert(t, err, qt.IsNil)

	err = fs1.Close()
	qt.Assert(t, err, qt.IsNil)
	c.Release()
	err = <-errc
	qt.Assert(t, err, qt.IsNil)
}

func TestWalkDeep(t *testing.T) {
	file := stringEntry{
		Content: "something",
	}
	n := plan9.MAXWELEM * 3
	for i := n - 1; i >= 0; i-- {
		file = stringEntry{
			Entries: map[string]stringEntry{
				fmt.Sprint("dir", i): file,
			},
		}
	}
	fs0, err := staticfsys.New(staticfsys.Params[struct{}, string]{
		Root: file.Entries,
		Open: func(f *staticfsys.Fid[struct{}, string]) (staticfsys.File, error) {
			return staticfsys.OpenString(f.Content())
		},
	})
	qt.Assert(t, err, qt.IsNil)

	c0, c1 := net.Pipe()
	errc := make(chan error, 1)
	go func() {
		err := server.Serve(context.Background(), c0, server.Fsys[*staticfsys.Fid[struct{}, string]](fs0))
		t.Logf("Serve finished; error: %v", err)
		c0.Close()
		errc <- err
	}()
	// We're just using NewConn for its connection init logic;
	// we'll actually do all the message sending and receiving ourselves.
	c, err := client.NewConn(c1)
	qt.Assert(t, err, qt.IsNil)
	defer c.Close()

	fs1, err := c.Attach(nil, "rog", "")
	qt.Assert(t, err, qt.IsNil)

	for i := 0; i < n-1; i++ {
		p := ""
		for j := 0; j <= i; j++ {
			p = path.Join(p, fmt.Sprint("dir", j))
		}
		info, err := fs1.Stat(p)
		qt.Assert(t, err, qt.IsNil)
		qt.Assert(t, info, qt.CmpEquals(cmpopts.IgnoreFields(plan9.Dir{}, "Qid")), &plan9.Dir{
			Name: path.Base(p),
			Mode: 0o555 | plan9.DMDIR,
			Uid:  "noone",
			Gid:  "noone",
		})
	}
	err = fs1.Close()
	qt.Assert(t, err, qt.IsNil)
	c.Release()
	err = <-errc
	qt.Assert(t, err, qt.IsNil)
}
