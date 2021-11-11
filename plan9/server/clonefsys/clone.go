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

type fidType uint8

const (
	cloneRoot fidType = iota
	cloneDir
	cloneRest
	cloneMax
)

var qidBits = bits.Len(uint(cloneMax))

type Fid[F server.Fid] struct {
	kind fidType
	id   int
	fid  F
}

func (f *Fid[F]) Qid() plan9.Qid {
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

type Fsys[F server.Fid] struct {
	server.ErrorFsys[*Fid[F]]
	mu    sync.Mutex
	fs    server.Fsys[F]
	roots []F
	clone func() F
	depth int
}

func New[F server.Fid](fs server.Fsys[F]) *Fsys[F] {
	return &Fsys[F]{
		fs: fs,
	}
}

func (fs *Fsys[F]) AddDir(f F) int {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.roots = append(fs.roots, f)
	return len(fs.roots) - 1
}

func (fs *Fsys[F]) Len() int {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return len(fs.roots)
}

func (fs *Fsys[F]) RemoveDir(id int) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if id < 0 || id >= len(fs.roots) {
		return fmt.Errorf("id out of range")
	}
	fs.roots[id] = *new(F)
	return nil
}

func (fs *Fsys[F]) Clone(f *Fid[F]) *Fid[F] {
	f = ref(*f)
	if f.kind != cloneRoot {
		f.fid = fs.fs.Clone(f.fid)
	}
	return f
}

func (fs *Fsys[F]) Clunk(f *Fid[F]) {
	if f.kind != cloneRoot {
		fs.fs.Clunk(f.fid)
	}
}

var errNotFound = errors.New("file not found")

func (fs *Fsys[F]) Attach(ctx context.Context, _ **Fid[F], uname, aname string) (*Fid[F], error) {
	return &Fid[F]{
		kind: cloneRoot,
	}, nil
}

func (fs *Fsys[F]) Stat(ctx context.Context, f *Fid[F]) (plan9.Dir, error) {
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

func (fs *Fsys[F]) Walk(ctx context.Context, f *Fid[F], name string) (*Fid[F], error) {
	if name == ".." {
		return fs.walkDotdot(ctx, f)
	}
	switch f.kind {
	case cloneRoot:
		id, err := strconv.Atoi(name)
		if err != nil {
			return nil, errNotFound
		}
		fs.mu.Lock()
		defer fs.mu.Unlock()
		if id < 0 || id >= len(fs.roots) {
			return nil, errNotFound
		}
		return &Fid[F]{
			kind: cloneDir,
			id:   id,
			fid:  fs.fs.Clone(fs.roots[id]),
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

func (fs *Fsys[F]) walkDotdot(ctx context.Context, f *Fid[F]) (*Fid[F], error) {
	panic("TODO")
}

func (fs *Fsys[F]) Open(ctx context.Context, f *Fid[F], mode uint8) (*Fid[F], uint32, error) {
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

func (fs *Fsys[F]) Readdir(ctx context.Context, f *Fid[F], dir []plan9.Dir, index int) (int, error) {
	switch f.kind {
	case cloneRoot:
		fs.mu.Lock()
		defer fs.mu.Unlock()
		i := 0
		for e := index; e < len(fs.roots); e++ {
			if i >= len(dir) {
				break
			}
			if isZero(fs.roots[e]) {
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

func (fs *Fsys[F]) ReadAt(ctx context.Context, f *Fid[F], buf []byte, off int64) (int, error) {
	return fs.fs.ReadAt(ctx, f.fid, buf, off)
}

func (fs *Fsys[F]) entry(id int) plan9.Dir {
	panic("TODO")
}

func ref[T any](x T) *T {
	return &x
}

func isZero[F comparable](x F) bool {
	return x == *new(F)
}
