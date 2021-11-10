package clonefsys

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"9fans.net/go/plan9"
	"9fans.net/go/plan9/server"
)

func NewCloner[F server.Fid](fs server.Fsys[F], clone func(id int) F) *Cloner[F] {
	panic("TODO")
}

type Cloner[F server.Fid] struct {
	fs    cloneFsys[F]
	clone func() F
}

func (fs *Cloner[F]) FS() server.Fsys[*Fid[F]] {
	return &fs.fs
}

func (fs *Cloner[F]) Add() int {
	// TODO locking
	fs.fs.roots = append(fs.fs.roots, fs.clone())
	return len(fs.fs.roots) - 1
}

func (fs *Cloner[F]) Len() int {
	return len(fs.fs.roots)
}

func (fs *Cloner[F]) Remove(id int) error {
	if id < 0 || id >= len(fs.fs.roots) {
		return fmt.Errorf("id out of range")
	}
	fs.fs.roots[id] = *new(F)
	return nil
}

type FidType uint8

const (
	cloneRoot FidType = iota
	cloneDir
	cloneRest
)

type Fid[F server.Fid] struct {
	kind FidType
	id   int
	fid  F
}

func (f *Fid[F]) Qid() plan9.Qid {
	// TODO determine number of bits in sub-fsys.
	panic("TODO")
}

type cloneFsys[F server.Fid] struct {
	server.ErrorFsys[*Fid[F]]
	fs    server.Fsys[F]
	roots []F
	clone func() F
	depth int
}

func (fs *cloneFsys[F]) Clone(f *Fid[F]) *Fid[F] {
	f = ref(*f)
	f.fid = fs.fs.Clone(f.fid)
	return f
}

func (fs *cloneFsys[F]) Clunk(f *Fid[F]) {
	if f.kind == cloneRest {
		fs.fs.Clunk(f.fid)
	}
}

var errNotFound = errors.New("file not found")

func (fs *cloneFsys[F]) Attach(ctx context.Context, _ **Fid[F], uname, aname string) (*Fid[F], error) {
	return &Fid[F]{
		kind: cloneRoot,
	}, nil
}

func (fs *cloneFsys[F]) Stat(ctx context.Context, f *Fid[F]) (plan9.Dir, error) {
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

func (fs *cloneFsys[F]) Walk(ctx context.Context, f *Fid[F], name string) (*Fid[F], error) {
	if name == ".." {
		return fs.walkDotdot(ctx, f)
	}
	switch f.kind {
	case cloneRoot:
		id, err := strconv.Atoi(name)
		if err != nil {
			return nil, errNotFound
		}
		if id < 0 || id >= len(fs.roots) {
			// TODO lock above
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

func (fs *cloneFsys[F]) walkDotdot(ctx context.Context, f *Fid[F]) (*Fid[F], error) {
	panic("TODO")
}

func (fs *cloneFsys[F]) Open(ctx context.Context, f *Fid[F], mode uint8) (*Fid[F], uint32, error) {
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

func (fs *cloneFsys[F]) Readdir(ctx context.Context, f *Fid[F], dir []plan9.Dir, index int) (int, error) {
	switch f.kind {
	case cloneRoot:
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

func (fs *cloneFsys[F]) ReadAt(ctx context.Context, f *Fid[F], buf []byte, off int64) (int, error) {
	return fs.fs.ReadAt(ctx, f.fid, buf, off)
}

func (fs *cloneFsys[F]) entry(id int) plan9.Dir {
	panic("TODO")
}

func ref[T any](x T) *T {
	return &x
}

func isZero[F comparable](x F) bool {
	return x == *new(F)
}
