package server

import (
	"context"
	"fmt"
	stdpath "path"
	"strings"

	"9fans.net/go/plan9"
)

var errNotFound = fmt.Errorf("file not found")

type staticFsys struct {
	ErrorFsys[*staticFileQ]
	root *staticFileQ
}

type StaticFile struct {
	Name    string
	Exec    bool
	Content []byte
	Entries []StaticFile
}

type staticFileQ struct {
	qid     plan9.Qid
	name    string
	perm    uint
	content []byte
	entries []*staticFileQ
}

type StaticFsys = Fsys[*staticFileQ]

func NewStaticFsys(entries []StaticFile) (StaticFsys, error) {
	root := &StaticFile{
		Name:    ".",
		Entries: entries,
	}
	rootq, _, err := calcQids(root, "", 1)
	if err != nil {
		return nil, fmt.Errorf("bad file tree: %v", err)
	}
	return &staticFsys{
		root: rootq,
	}, nil
}

func (f *staticFileQ) Qid() plan9.Qid {
	return f.qid
}

func (fs *staticFsys) Clone(ctx context.Context, f *staticFileQ) (*staticFileQ, error) {
	return f, nil
}

func (fs *staticFsys) Attach(ctx context.Context, _ **staticFileQ, uname, aname string) (*staticFileQ, error) {
	return fs.root, nil
}

func (fs *staticFsys) Stat(ctx context.Context, f *staticFileQ) (plan9.Dir, error) {
	return fs.makeDir(f), nil
}

func (fs *staticFsys) makeDir(f *staticFileQ) plan9.Dir {
	return plan9.Dir{
		Qid:  f.qid,
		Name: f.name,
	}
	//	Type   uint16
	//	Dev    uint32
	//	Qid    Qid
	//	Mode   Perm
	//	Atime  uint32
	//	Mtime  uint32
	//	Length uint64
	//	Name   string
	//	Uid    string
	//	Gid    string
	//	Muid   string
}

func (fs *staticFsys) Walk(ctx context.Context, f *staticFileQ, name string) (*staticFileQ, error) {
	for _, e := range f.entries {
		if e.name == name {
			return e, nil
		}
	}
	return nil, errNotFound
}

func (fs *staticFsys) Readdir(ctx context.Context, f *staticFileQ, dir []plan9.Dir, index int) (int, error) {
	if index >= len(f.entries) {
		index = len(f.entries)
	}
	for i, e := range f.entries[index:] {
		dir[i] = fs.makeDir(e)
	}
	return len(f.entries) - index, nil
}

func (fs *staticFsys) Open(ctx context.Context, f *staticFileQ, mode uint8) (*staticFileQ, uint32, error) {
	// caller has already checked file perms, so just allow it.
	return f, 8192, nil
}

func (fs *staticFsys) ReadAt(ctx context.Context, f *staticFileQ, buf []byte, off int64) (int, error) {
	if off > int64(len(f.content)) {
		off = int64(len(f.content))
	}
	return copy(buf, f.content[off:]), nil
}

func validName(s string) bool {
	return !strings.ContainsAny(s, "/")
}

func calcQids(f *StaticFile, path string, qpath uint64) (_ *staticFileQ, maxQpath uint64, err error) {
	if !validName(f.Name) {
		return nil, 0, fmt.Errorf("file name %q in directory %q isn't valid", f.Name, path)
	}
	path = stdpath.Join(path, f.Name)
	if f.Content != nil && f.Entries != nil {
		return nil, 0, fmt.Errorf("%q has both content and entries set", path)
	}
	if f.Content == nil && f.Entries == nil {
		// Default to an empty file unless we're at the root (qpath == 0)
		panic("no content, no entries")
	}
	qtype := uint8(0)
	if f.Entries != nil {
		qtype = plan9.QTDIR
	}
	qf := &staticFileQ{
		qid: plan9.Qid{
			Path: qpath,
			Type: qtype,
		},
		name:    f.Name,
		content: f.Content,
		entries: make([]*staticFileQ, len(f.Entries)),
	}
	qpath++
	for i := range qf.entries {
		e, qp, err := calcQids(&f.Entries[i], path, qpath)
		if err != nil {
			return nil, 0, err
		}
		qf.entries[i] = e
		qpath = qp
	}
	return qf, qpath, nil
}
