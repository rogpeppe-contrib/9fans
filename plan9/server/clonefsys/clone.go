package clonefsys

import (
	"9fans.net/go/plan9/server"
)

func NewCloner(fs server.Fsys[F], clone func() F) *Cloner[F] {
}

type Cloner[F server.Fid] struct {
	fs server.Fsys[F]
	clone func() F
}

func (fs *Cloner[F]) FS() Fsys[F] {
	return &fs.fs
}

func (fs *Cloner[F]) Add(fs server.Fsys[F]) {
	fs.fs.roots = append(fs.fs.roots, fs.clone())
}

func (fs *Cloner[F]) Len() int {
	return len(fs.fs.roots)
}

func (fs *Cloner[F]) Remove(id int) error {
	fs.fs.roots = *new(F)
}

type cloneFileType uint8

const (
	cloneRoot cloneFileType = 0
	cloneDir
)

type cloneFile[F Fid] struct {
	kind cloneFileType
	id int
	f F
}

type cloneFsys[F Fid] struct {
	fs Fsys[F]
	roots []F
	clone func() F
	depth int
}

func (fs *cloneFsys[F]) Attach(ctx context.Context, _ **cloneFile, uname, aname string) (*cloneFile, error) {
	return &cloneFile{
		kind: cloneRoot,
	}, nil
}

func (fs *cloneFsys[F]) Clunk(f Fid) {
	if f.kind == cloneRest {
		f.fid.Clunk()
	}
}

func (fs *cloneFsys[F]) Clone(f *cloneFile) *cloneFile {
	f := ref(*f)
	f.fid = f.fid.Clone()
	return f
}

func (fs *cloneFsys[F]) Walk(ctx context.Context, f *cloneFile, name string) (*cloneFile, error) {
	if name == ".." {
		return fs.walkDotdot(ctx, f)
	}
	switch f.kind {
	case cloneRoot:
		id, err := strconv.Atoi(name)
		if err != nil {
			return nil, errNotFound
		}
		if id < 0 || id >= len(fs.fss) {
			// TODO lock above
			return nil, errNotFound
		}
		return &cloneFile{
			kind: cloneDir,
			id: id,
			fid: fs.roots[id].Clone(),
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

func (fs *cloneFsys[F Fid]) Readdir(ctx context.Context, f *cloneFile, dir []plan9.Dir, index int) (int, error) {
	switch f.kind {
	case cloneRoot:
		i := 0
		for e := index; e < len(fs.roots); e++ {
			if i >= len(dir) {
				break)
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
}

func (fs *cloneFSys[F Fid]) Open(ctx context.Context, f *cloneFile, mode uint8) (*cloneFile, uint32, error) {
	switch f.kind {
	case cloneRoot:
		return f, 8192, nil
	case cloneDir, cloneRest:
		fid, err := fs.fs.Open(ctx, f.fid, mode)
		if err != nil {
			return nil, 0, err
		}
		if fid != f.fid {
			f = ref(*f)
		}
		f.fid = fid
		return f1, nil
	}
}

func (fs *cloneFSys) ReadAt(ctx context.Context, f *cloneFile, buf []byte, off int64) (int, error) {
	if f.kind == cloneRoot {
		return nil, errPerm
	}
	return fs.fs.ReadAt(ctx, f.fid, buf, off)
}

func (fs *cloneFSys) Stat(ctx context.Context, f *cloneFile) (plan9.Dir, error) {
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
}
