package clonefsys

import (
	"context"
	"errors"
	"fmt"
	"math/bits"
	"strconv"
	"sync"

	"9fans.net/go/plan9"
	"9fans.net/go/plan9/server"
)

var errNotFound = errors.New("file not found")

type fidType uint8

const (
	cloneRoot fidType = iota
	cloneDir
	cloneRest
	cloneMax
)

var qidBits = bits.Len(uint(cloneMax))

// Fid represents a fid for a file within the clone filesystem.
type Fid[F server.Fid, C0 any] struct {
	c    C0
	kind fidType
	id   int
	fid  F
}

// Qid implements server.Fid.Qid.
func (f *Fid[F, C0]) Qid() plan9.Qid {
	if f.kind == cloneRoot {
		return plan9.Qid{
			Type: plan9.QTDIR,
			Path: uint64(cloneRoot),
		}
	}
	q := f.fid.Qid()
	// TODO what to do when q.Path has significant high bits
	// that are lost with the shift?
	q.Path = (q.Path << qidBits) | uint64(f.kind)
	return q
}

// Provider is used to determine how many clones to serve
// for a given fid and what context to provide to the fids
// in the inner fs.
type Provider[C any] interface {
	// Len returns the number of clones IDs there might be.
	// IDs should remain stable across time. If a clone
	// doesn't exist any more, Get should return false
	// for its ID.
	Len() int
	// Get returns the clone with the given ID and
	// reports whether that ID exists.
	Get(id int) (C, bool)
	// TODO could require Close method so that it could
	// obtain a mutex while we ask for info.
}

type fsys[F server.Fid, C0, C1 any] struct {
	server.ErrorFsys[*Fid[F, C0]]
	mu       sync.Mutex
	fs       server.FsysInner[F, C1]
	provider func(C0) Provider[C1]
	depth    int
}

// New returns a filesystem implementation that provides some number of copies of fs,
// each in a numbered directory.
//
// C0 represents the attach context of the outer filesystem; the provider is used to
// find out how many copies of fs should be served for a given fid.
//
// When a fid is walked into one of the clones, the fs.AttachInner method is
// used to create the fid to walk into; its context argument is taken from
// a call to provider.Get.
func New[C0, C1 any, F server.Fid](fs server.FsysInner[F, C1], provider func(C0) Provider[C1]) server.FsysInner[*Fid[F, C0], C0] {
	return &fsys[F, C0, C1]{
		fs:       fs,
		provider: provider,
	}
}

func (fs *fsys[F, C0, C1]) Clone(f *Fid[F, C0]) *Fid[F, C0] {
	f = ref(*f)
	if f.kind != cloneRoot {
		f.fid = fs.fs.Clone(f.fid)
	}
	return f
}

func (fs *fsys[F, C0, C1]) Clunk(f *Fid[F, C0]) {
	if f.kind != cloneRoot {
		fs.fs.Clunk(f.fid)
	}
}

func (fs *fsys[F, C0, C1]) AttachInner(ctx context.Context, c C0) (*Fid[F, C0], error) {
	return &Fid[F, C0]{
		kind: cloneRoot,
		c:    c,
	}, nil
}

func (fs *fsys[F, C0, C1]) Attach(ctx context.Context, _ **Fid[F, C0], uname, aname string) (*Fid[F, C0], error) {
	return &Fid[F, C0]{
		kind: cloneRoot,
	}, nil
}

func (fs *fsys[F, C0, C1]) Stat(ctx context.Context, f *Fid[F, C0]) (plan9.Dir, error) {
	switch f.kind {
	case cloneRoot:
		return plan9.Dir{
			Name: ".",
			// TODO
		}, nil
	case cloneDir:
		dir, err := fs.fs.Stat(ctx, f.fid)
		if err != nil {
			return dir, err
		}
		dir.Name = fmt.Sprint(f.id)
		return dir, nil
	case cloneRest:
		return fs.fs.Stat(ctx, f.fid)
	}
	panic("unreachable")
}

func (fs *fsys[F, C0, C1]) Walk(ctx context.Context, f *Fid[F, C0], name string) (*Fid[F, C0], error) {
	if name == ".." {
		return fs.walkDotdot(ctx, f)
	}
	switch f.kind {
	case cloneRoot:
		id, err := strconv.Atoi(name)
		if err != nil || fmt.Sprint(id) != name {
			return nil, errNotFound
		}
		c1, ok := fs.provider(f.c).Get(id)
		if !ok {
			return nil, errNotFound
		}
		fid, err := fs.fs.AttachInner(ctx, c1)
		if err != nil {
			return nil, err
		}
		return &Fid[F, C0]{
			kind: cloneDir,
			// Keep the context around for walkDotdot.
			c:   f.c,
			id:  id,
			fid: fid,
		}, nil
	case cloneDir, cloneRest:
		fid, err := fs.fs.Walk(ctx, f.fid, name)
		if err != nil {
			return nil, err
		}
		if fid != f.fid {
			f = ref(*f)
		}
		f.fid = fid
		f.kind = cloneRest
		return f, nil
	default:
		panic("unreachable")
	}
}

func (fs *fsys[F, C0, C1]) walkDotdot(ctx context.Context, f *Fid[F, C0]) (*Fid[F, C0], error) {
	panic("TODO")
}

func (fs *fsys[F, C0, C1]) Open(ctx context.Context, f *Fid[F, C0], mode uint8) (*Fid[F, C0], uint32, error) {
	switch f.kind {
	case cloneRoot:
		return f, 8192, nil
	case cloneDir, cloneRest:
		fid, iounit, err := fs.fs.Open(ctx, f.fid, mode)
		if err != nil {
			return nil, 0, err
		}
		if fid != f.fid {
			f = ref(*f)
		}
		f.fid = fid
		return f, iounit, nil
	}
	panic("unreachable")
}

func (fs *fsys[F, C0, C1]) Readdir(ctx context.Context, f *Fid[F, C0], dir []plan9.Dir, index int) (int, error) {
	switch f.kind {
	case cloneRoot:
		p := fs.provider(f.c)
		n := p.Len()
		i := 0
		for e := index; e < n; e++ {
			if i >= len(dir) {
				break
			}
			if _, ok := p.Get(e); !ok {
				continue
			}
			dir[i] = fs.entry(e)
			i++
		}
		return i, nil
	case cloneDir, cloneRest:
		return fs.fs.Readdir(ctx, f.fid, dir, index)
	}
	panic("unreachable")
}

func (fs *fsys[F, C0, C1]) ReadAt(ctx context.Context, f *Fid[F, C0], buf []byte, off int64) (int, error) {
	return fs.fs.ReadAt(ctx, f.fid, buf, off)
}

func (fs *fsys[F, C0, C1]) entry(id int) plan9.Dir {
	panic("TODO")
}

func ref[T any](x T) *T {
	return &x
}

func isZero[F comparable](x F) bool {
	return x == *new(F)
}
