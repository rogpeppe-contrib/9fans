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

type Params[Context, Content any] struct {
	Root map[string]Entry[Content]

	// Open is called when a Fid is opened. I/O operations on the
	// open fid will be invoked on the returned File. If this is nil,
	// New will return an error.
	//
	// TODO If this is nil, we could provide a default version that works for
	// some content types.
	Open func(*Fid[Context, Content]) (File, error)

	// ContextForAttach returns the context data to associate with
	// a fid created with an Attach call. If it's nil, the zero value
	// for Context will be used.
	ContextForAttach func(uname, aname string) (Context, error)

	// Uid and Gid are used for the user and group names
	// of all the files. If they're blank, "noone" will be used.
	Uid string
	Gid string
}

type Fid[Context, Content any] struct {
	entry   *entry[Content]
	file    File
	context Context
}

func (f *Fid[Context, Content]) Qid() plan9.Qid {
	return f.entry.qid
}

func (f *Fid[Context, Content]) Content() Content {
	return f.entry.content
}

func (f *Fid[Context, Content]) Context() Context {
	return f.context
}

// fsys implements server.FsysInner by serving content with a static
// directory structure as defined in Params.
type fsys[Context, Content any] struct {
	server.ErrorFsys[*Fid[Context, Content]]
	root             *entry[Content]
	contextForAttach func(uname, aname string) (Context, error)
	open             func(*Fid[Context, Content]) (File, error)
	uid, gid         string
}

// New returns an instance of server.FsysInner that serves
// a statically defined directory structure.
func New[Context, Content any](p Params[Context, Content]) (server.FsysInner[*Fid[Context, Content], Context], error) {
	if p.Uid == "" {
		p.Uid = "noone"
	}
	if p.Gid == "" {
		p.Gid = "noone"
	}
	if p.Open == nil {
		return nil, fmt.Errorf("no Open parameter provided")
	}
	root := Entry[Content]{
		Entries: p.Root,
	}
	root1, _, err := calcQids(".", root, "", 1)
	if err != nil {
		return nil, fmt.Errorf("bad file tree: %v", err)
	}
	return &fsys[Context, Content]{
		root:             root1,
		uid:              p.Uid,
		gid:              p.Gid,
		open:             p.Open,
		contextForAttach: p.ContextForAttach,
	}, nil
}

func (fs *fsys[Context, Content]) AttachInner(ctx context.Context, c Context) (*Fid[Context, Content], error) {
	return &Fid[Context, Content]{
		entry:   fs.root,
		context: c,
	}, nil
}

func (fs *fsys[Context, Content]) Clone(f *Fid[Context, Content]) *Fid[Context, Content] {
	return ref(*f)
}

func (fs *fsys[Context, Content]) Clunk(f *Fid[Context, Content]) {
	if f.file != nil {
		// TODO this is one of those places where we'd like to be able
		// to return an error from Clunk (or have a separate Close op for
		// open files)
		f.file.Close()
		f.file = nil
	}
}

func (fs *fsys[Context, Content]) Attach(ctx context.Context, _ **Fid[Context, Content], uname, aname string) (*Fid[Context, Content], error) {
	var c Context
	if fs.contextForAttach != nil {
		c1, err := fs.contextForAttach(uname, aname)
		if err != nil {
			return nil, err
		}
		c = c1
	}
	return &Fid[Context, Content]{
		entry:   fs.root,
		context: c,
	}, nil
}

func (fs *fsys[Context, Content]) Stat(ctx context.Context, f *Fid[Context, Content]) (plan9.Dir, error) {
	return fs.makeDir(f.entry), nil
}

func (fs *fsys[Context, Content]) makeDir(e *entry[Content]) plan9.Dir {
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

func (fs *fsys[Context, Content]) Walk(ctx context.Context, f *Fid[Context, Content], name string) (*Fid[Context, Content], error) {
	for _, e := range f.entry.entries {
		if e.name == name {
			f.entry = e
			return f, nil
		}
	}
	return nil, errNotFound
}

func (fs *fsys[Context, Content]) Readdir(ctx context.Context, f *Fid[Context, Content], dir []plan9.Dir, index int) (int, error) {
	entries := f.entry.entries
	if index >= len(entries) {
		index = len(entries)
	}
	for i, e := range entries[index:] {
		dir[i] = fs.makeDir(e)
	}
	return len(entries) - index, nil
}

func (fs *fsys[Context, Content]) Open(ctx context.Context, f *Fid[Context, Content], mode uint8) (*Fid[Context, Content], uint32, error) {
	if f.entry.entries != nil {
		return f, 0, nil
	}
	file, err := fs.open(f)
	if err != nil {
		return nil, 0, err
	}
	f.file = file
	return f, 0, nil
}

func (fs *fsys[Context, Content]) ReadAt(ctx context.Context, f *Fid[Context, Content], buf []byte, off int64) (int, error) {
	return f.file.ReadAt(buf, off)
}

func (fs *fsys[Context, Content]) WriteAt(ctx context.Context, f *Fid[Context, Content], buf []byte, off int64) (int, error) {
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
