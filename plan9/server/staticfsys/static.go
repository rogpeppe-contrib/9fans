package staticfsys

import (
	"context"
	"fmt"
	stdpath "path"
	"sort"
	"strings"

	"9fans.net/go/plan9"
	"9fans.net/go/plan9/server"
)

// File represents a file open for I/O.
type File interface {
	// TODO should we pass in context.Context here too, to leave options open?
	ReadAt(buf []byte, offset int64) (int, error)
	WriteAt(buf []byte, offset int64) (int, error)
	Close() error
}

var errNotFound = fmt.Errorf("file not found")

type Entry[Content any] struct {
	// Entries holds the set of entries in a directory.
	// If it's nil, the Entry represents a regular
	// file and Content defines its contents.
	Entries    map[string]Entry[Content]
	Executable bool
	Content    Content
}

// entry holds the same content as Entry but
// processed into an easy-to-serve data structure.
type entry[Content any] struct {
	qid        plan9.Qid
	name       string
	perm       uint
	executable bool
	content    Content
	entries    []*entry[Content]
}

type Params[Content any] struct {
	Root map[string]Entry[Content]

	// Open is called when an Entry is opened and passed the
	// Content field from that entry. This will not be used for
	// fids created with the Fsys.Root method or any fids derived from those -
	// in that case the open function passed into Root will be used.

	// Opener is called when an Attach call is made with the given
	// aname. It returns a function that will be used to open files
	// derived from that attach. If it returns an error, the Attach
	// will fail.
	//
	// TODO If this is nil, we could provide a default version that works for
	// some content types.
	Opener func(aname string) (func(Content) (File, error), error)

	// Uid and Gid are used for the user and group names
	// of all the files. If they're blank, "noone" will be used.
	Uid string
	Gid string
}

type Fid[Content any] struct {
	entry *entry[Content]
	open  func(Content) (File, error)
	file  File
}

func (f *Fid[Content]) Qid() plan9.Qid {
	return f.entry.qid
}

var _ server.Fsys[*Fid[struct{}]] = (*Fsys[struct{}])(nil)

// Fsys implements server.Fsys[Fid] by serving content with a static
// directory structure as defined in Params.
//
// It also implements an extra method, Root, that can be used to
// associate arbitrary data with a fid in addition to the content
// that's associated with each entry.
type Fsys[Content any] struct {
	server.ErrorFsys[*Fid[Content]]
	root     *entry[Content]
	opener   func(aname string) (func(Content) (File, error), error)
	uid, gid string
}

// New returns an instance of server.Fsys[*Fid[Content]] that serves
// a statically defined directory structure.
func New[Content any](p Params[Content]) (*Fsys[Content], error) {
	if p.Uid == "" {
		p.Uid = "noone"
	}
	if p.Gid == "" {
		p.Gid = "noone"
	}
	root := Entry[Content]{
		Entries: p.Root,
	}
	root1, _, err := calcQids(".", root, "", 1)
	if err != nil {
		return nil, fmt.Errorf("bad file tree: %v", err)
	}
	return &Fsys[Content]{
		root:   root1,
		uid:    p.Uid,
		gid:    p.Gid,
		opener: p.Opener,
	}, nil
}

func (fs *Fsys[Content]) Root(open func(c Content) (File, error)) *Fid[Content] {
	return &Fid[Content]{
		entry: fs.root,
		open:  open,
	}
}

func (fs *Fsys[Content]) Clone(f *Fid[Content]) *Fid[Content] {
	return ref(*f)
}

func (fs *Fsys[Content]) Clunk(f *Fid[Content]) {
	if f.file != nil {
		// TODO this is one of those places where we'd like to be able
		// to return an error from Clunk (or have a separate Close op for
		// open files)
		f.file.Close()
		f.file = nil
	}
}

func (fs *Fsys[Content]) Attach(ctx context.Context, _ **Fid[Content], uname, aname string) (*Fid[Content], error) {
	if fs.opener == nil {
		return nil, fmt.Errorf("cannot attach because no root open function was provided")
	}
	openFunc, err := fs.opener(aname)
	if err != nil {
		return nil, err
	}
	return &Fid[Content]{
		entry: fs.root,
		open:  openFunc,
	}, nil
}

func (fs *Fsys[Content]) Stat(ctx context.Context, f *Fid[Content]) (plan9.Dir, error) {
	return fs.makeDir(f.entry), nil
}

func (fs *Fsys[Content]) makeDir(e *entry[Content]) plan9.Dir {
	m := plan9.Perm(0o444)
	if e.executable || e.entries != nil {
		m |= 0o111
	}
	if e.entries != nil {
		m |= plan9.DMDIR
	}
	return plan9.Dir{
		Qid:  e.qid,
		Name: e.name,
		Mode: m,
		// TODO provide some way of calculating length?
		Uid: fs.uid,
		Gid: fs.gid,
	}
}

func (fs *Fsys[Content]) Walk(ctx context.Context, f *Fid[Content], name string) (*Fid[Content], error) {
	for _, e := range f.entry.entries {
		if e.name == name {
			f.entry = e
			return f, nil
		}
	}
	return nil, errNotFound
}

func (fs *Fsys[Content]) Readdir(ctx context.Context, f *Fid[Content], dir []plan9.Dir, index int) (int, error) {
	entries := f.entry.entries
	if index >= len(entries) {
		index = len(entries)
	}
	for i, e := range entries[index:] {
		dir[i] = fs.makeDir(e)
	}
	return len(entries) - index, nil
}

func (fs *Fsys[Content]) Open(ctx context.Context, f *Fid[Content], mode uint8) (*Fid[Content], uint32, error) {
	if f.entry.entries != nil {
		return f, 0, nil
	}
	file, err := f.open(f.entry.content)
	if err != nil {
		return nil, 0, err
	}
	f.file = file
	return f, 0, nil
}

func (fs *Fsys[Content]) ReadAt(ctx context.Context, f *Fid[Content], buf []byte, off int64) (int, error) {
	return f.file.ReadAt(buf, off)
}

func (fs *Fsys[Content]) WriteAt(ctx context.Context, f *Fid[Content], buf []byte, off int64) (int, error) {
	return f.file.WriteAt(buf, off)
}

func validName(s string) bool {
	return !strings.ContainsAny(s, "/")
}

func calcQids[Content any](fname string, f Entry[Content], path string, qpath uint64) (_ *entry[Content], maxQpath uint64, err error) {
	if !validName(fname) {
		return nil, 0, fmt.Errorf("file name %q in directory %q isn't valid", fname, path)
	}
	path = stdpath.Join(path, fname)
	qtype := uint8(0)
	if f.Entries != nil {
		qtype = plan9.QTDIR
	}
	qf := &entry[Content]{
		qid: plan9.Qid{
			Path: qpath,
			Type: qtype,
		},
		name:       fname,
		executable: f.Executable,
		content:    f.Content,
	}
	qpath++
	if f.Entries == nil {
		return qf, qpath, nil
	}
	// sort by name for predictability of tests.
	names := make([]string, 0, len(f.Entries))
	for name := range f.Entries {
		names = append(names, name)
	}
	sort.Strings(names)
	qf.entries = make([]*entry[Content], len(names))
	for i, name := range names {
		entry := f.Entries[name]
		e, qp, err := calcQids(name, entry, path, qpath)
		if err != nil {
			return nil, 0, err
		}
		qf.entries[i] = e
		qpath = qp
	}
	return qf, qpath, nil
}

func ref[T any](x T) *T {
	return &x
}
