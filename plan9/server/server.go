package server

import (
	"constraints"
	"context"
	"fmt"
	"io"
	"log"
	"sync"

	"9fans.net/go/plan9"
)

type fid[F Fid] struct {
	id       uint32
	mu       sync.Mutex
	fid      F
	inUse    bool
	open     bool
	attached bool
	opened   bool

	// The following fields apply only when the fid is open.

	// openMode holds the mode that the fid was opened in.
	// It's not mutated after the fid is open, so it can be accessed
	// without obtaining fid.mu.
	openMode uint8

	// iounit holds the iounit of the file. Like openMode, it's not mutated
	// after the fid is open.
	iounit uint32

	// dirOffset holds the next directory byte offset. Guarded by mu.
	dirOffset int64

	// dirIndex holds the next directory entry index.
	dirIndex int

	// dirEntries holds remaining entries returned by Fsys.Readdir.
	dirEntries []plan9.Dir

	// dirEntryBuf holds a buffer of directory entries.
	dirEntryBuf []plan9.Dir
}

func (f *fid[F]) attach(fsf F) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attached = true
	f.fid = fsf
}

func (f *fid[F]) done() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.inUse {
		panic("fid.done called on fid that's not in use")
	}
	f.inUse = false
}

type server[F Fid] struct {
	fs   Fsys[F]
	conn io.ReadWriter
	mu   sync.Mutex
	fids map[uint32]*fid[F]
}

func Serve[F Fid](ctx context.Context, conn io.ReadWriter, fs Fsys[F]) error {
	srv := &server[F]{
		conn: conn,
		fs:   fs,
		fids: make(map[uint32]*fid[F]),
	}
	defer fs.Close()
	m, err := plan9.ReadFcall(conn)
	if err != nil {
		return err
	}
	if m.Type != plan9.Tversion {
		return fmt.Errorf("first message is %v not Tversion", m.Type)
	}
	if m.Version != "9P2000" {
		srv.sendMessage(&plan9.Fcall{
			Type:    plan9.Rversion,
			Tag:     m.Tag,
			Version: "unknown",
		})
		return fmt.Errorf("unknown version %q", m.Version)
	}
	srv.sendMessage(&plan9.Fcall{
		Type:    plan9.Rversion,
		Tag:     m.Tag,
		Version: m.Version,
		Msize:   m.Msize,
	})
	for {
		m, err := plan9.ReadFcall(conn)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		op := operations[m.Type]
		if op == nil {
			srv.sendError(m.Tag, fmt.Errorf("bad operation type"))
			continue
		}
		if err := op(srv, ctx, m); err != nil {
			srv.sendError(m.Tag, err)
		}
	}
}

// TODO	Auth(ctx context.Context, uname, aname string) (F, error)

func (srv *server[F]) handleAttach(ctx context.Context, m *plan9.Fcall) error {
	var afidp *F
	if m.Afid != plan9.NOFID {
		afid, err := srv.getFid(m.Afid, fNotOpen)
		if err != nil {
			return err
		}
		// TODO we should be able to cope with a client that
		// sends a clunk for a fid while an operation on that fid
		// is still in progress. What should we do in that case?
		// Delay the clunk until the operation(s) have completed?
		// In that case, we'll need to be able to wait for operations
		// on any given fid to complete.
		f := afid.fid
		afidp = &f
	}
	fid, err := srv.newFid(m.Fid)
	if err != nil {
		return err
	}
	//ctx = srv.newContext(ctx, m.Tag) TODO when flush is implemented
	go func() {
		f, err := srv.fs.Attach(ctx, afidp, m.Uname, m.Aname)
		if err != nil {
			srv.delFid(fid)
			srv.sendError(m.Tag, err)
			return
		}
		if !f.Qid().IsDir() {
			srv.delFid(fid)
			srv.sendError(m.Tag, fmt.Errorf("root is not a directory"))
			return
		}
		// TODO sanity check that f.Qid returns a directory?
		fid.attach(f)
		srv.sendMessage(&plan9.Fcall{
			Type: plan9.Rattach,
			Tag:  m.Tag,
			Qid:  f.Qid(),
		})
	}()
	return nil
}

func (srv *server[F]) handleStat(ctx context.Context, m *plan9.Fcall) error {
	fid, err := srv.getFid(m.Afid, fNotOpen)
	if err != nil {
		return err
	}
	go func() {
		dir, err := srv.fs.Stat(ctx, fid.fid)
		if err != nil {
			srv.sendError(m.Tag, err)
			return
		}
		dir.Qid = fid.fid.Qid()
		stat, err := dir.Bytes()
		if err != nil {
			srv.sendError(m.Tag, fmt.Errorf("cannot marshal Dir: %v", err))
			return
		}
		srv.sendMessage(&plan9.Fcall{
			Type: plan9.Rstat,
			Tag:  m.Tag,
			Stat: stat,
		})
	}()
	return nil
}

func (srv *server[F]) handleWalk(ctx context.Context, m *plan9.Fcall) error {
	fid, err := srv.getFid(m.Fid, fExcl)
	if err != nil {
		return err
	}
	newFid := fid
	if m.Newfid != m.Fid {
		newFid, err = srv.newFid(m.Newfid)
		if err != nil {
			fid.done()
			return err
		}
	}
	go func() {
		defer fid.done()
		qids, err := srv.walk(ctx, fid, newFid, m.Wname)
		log.Printf("wname %q; got qids %v; err %v", m.Wname, qids, err)
		if len(qids) < len(m.Wname) {
			srv.delFid(newFid)
			if len(qids) == 0 {
				srv.sendError(m.Tag, err)
				return
			}
		}
		srv.sendMessage(&plan9.Fcall{
			Type: plan9.Rwalk,
			Tag:  m.Tag,
			Wqid: qids,
		})
	}()
	return nil
}

func (srv *server[F]) walk(ctx context.Context, fid, newFid *fid[F], names []string) (rqids []plan9.Qid, rerr error) {
	newf, err := srv.fs.Clone(ctx, fid.fid)
	if err != nil {
		return nil, err
	}
	defer func() {
		if len(rqids) < len(names) {
			// TODO what should we do if this fails?
			srv.fs.Clunk(ctx, newf)
		}
	}()
	qids := make([]plan9.Qid, 0, len(names))
	for _, name := range names {
		newf1, err := srv.fs.Walk(ctx, newf, name)
		if err != nil {
			return qids, err
		}
		if newf1 != newf {
			srv.fs.Clunk(ctx, newf)
			newf = newf1
		}
		qids = append(qids, newf.Qid())
	}
	newFid.attach(newf)
	return qids, nil
}

func (srv *server[F]) handleOpen(ctx context.Context, m *plan9.Fcall) error {
	fid, err := srv.getFid(m.Fid, fExcl)
	if err != nil {
		return err
	}
	if fid.fid.Qid().IsDir() && (m.Mode == plan9.OWRITE ||
		m.Mode == plan9.ORDWR ||
		m.Mode == plan9.OEXEC) {
		fid.done()
		return errPerm
	}
	// TODO handle OEXCL ?
	go func() {
		defer fid.done()
		// TODO we could potentially help out by invoking src.fs.Stat
		// and checking permissions, but that does have the potential
		// to be racy.
		f, iounit, err := srv.fs.Open(ctx, fid.fid, m.Mode)
		if err != nil {
			srv.sendError(m.Tag, err)
			return
		}
		if iounit == 0 {
			iounit = 8 * 1024
		}
		if fid.fid != f {
			srv.fs.Clunk(ctx, fid.fid)
		}
		fid.fid = f
		fid.open = true
		fid.openMode = m.Mode
		fid.iounit = iounit
		srv.sendMessage(&plan9.Fcall{
			Type:   plan9.Ropen,
			Tag:    m.Tag,
			Qid:    f.Qid(),
			Iounit: iounit,
		})
	}()
	return nil
}

func (srv *server[F]) handleRead(ctx context.Context, m *plan9.Fcall) error {
	fid, err := srv.getFid(m.Fid, fOpen)
	if err != nil {
		return err
	}
	if !canRead(fid.openMode) {
		return errPerm
	}
	isDir := fid.fid.Qid().IsDir()
	if isDir {
		fid, err = srv.getFid(m.Fid, fOpen|fExcl)
		if err != nil {
			return err
		}
	}
	offset := int64(m.Offset)
	if offset < 0 {
		return fmt.Errorf("offset too big")
	}

	go func() {
		if isDir {
			defer fid.done()
			if err := srv.readDir(ctx, m.Tag, fid, offset, m.Count); err != nil {
				srv.sendError(m.Tag, err)
			}
			return
		}
		buf := make([]byte, min(fid.iounit, m.Count))
		n, err := srv.fs.ReadAt(ctx, fid.fid, buf, offset)
		if err != nil {
			srv.sendError(m.Tag, err)
			return
		}
		srv.sendMessage(&plan9.Fcall{
			Type: plan9.Rread,
			Tag:  m.Tag,
			Data: buf[:n],
		})
	}()
	return nil
}

func (srv *server[F]) readDir(ctx context.Context, tag uint16, f *fid[F], offset int64, count uint32) error {
	// It's OK to access fields directly here because f is acquired exclusively above.
	if offset == 0 {
		f.dirOffset = 0
		f.dirIndex = 0
		f.dirEntries = nil
	} else if offset != f.dirOffset {
		return fmt.Errorf("illegal read offset in directory (got %d want %d)", offset, f.dirOffset)
	}
	limit := int(min(count, f.iounit))
	buf := make([]byte, 0, limit)
	for {
		if len(f.dirEntries) == 0 {
			if len(f.dirEntryBuf) == 0 {
				f.dirEntryBuf = make([]plan9.Dir, 16)
			}
			n, err := srv.fs.Readdir(ctx, f.fid, f.dirEntryBuf, f.dirIndex)
			if err != nil {
				return err
			}
			if n == 0 {
				break
			}
			f.dirEntries = f.dirEntryBuf[:n]
		}
		oldLen := len(buf)
		buf = f.dirEntries[0].Append(buf)
		if len(buf) > limit && oldLen == 0 {
			// The entry won't fit into the requested byte count.
			return fmt.Errorf("directory read count too small for directory entry")
		}
		if len(buf) >= limit {
			break
		}
		f.dirEntries = f.dirEntries[1:]
		f.dirIndex++
	}
	srv.sendMessage(&plan9.Fcall{
		Type: plan9.Rread,
		Tag:  tag,
		Data: buf,
	})
	f.dirOffset += int64(len(buf))
	return nil
}

func canRead(mode uint8) bool {
	switch mode &^ 3 {
	case plan9.OREAD, plan9.ORDWR, plan9.OEXEC:
		return true
	}
	return false
}

func (srv *server[F]) handleClunk(ctx context.Context, m *plan9.Fcall) error {
	// TODO wait for operations on the fid to complete (or just return an
	// error if there are other operations in progress and be damned with
	// the spec?)
	fid, err := srv.getFid(m.Fid, 0)
	if err != nil {
		return err
	}
	srv.delFid(fid) // or should this be done after calling Clunk?
	go func() {
		srv.fs.Clunk(ctx, fid.fid)
		srv.sendMessage(&plan9.Fcall{
			Type: plan9.Rclunk,
			Tag:  m.Tag,
			Fid:  m.Fid,
		})
	}()
	return nil
}

func (srv *server[F]) sendError(tag uint16, err error) {
	srv.sendMessage(&plan9.Fcall{
		Type:  plan9.Rerror,
		Tag:   tag,
		Ename: err.Error(),
	})
}

func (srv *server[F]) sendMessage(m *plan9.Fcall) {
	// TODO if there's a write error, close the server?
	plan9.WriteFcall(srv.conn, m)
}

func (srv *server[F]) handleFlush(m *plan9.Fcall) error {
	panic("TODO")
	// look for outstanding matching tag
	// if it's found, cancel its context and wait for it to return,
	// then send Rflush response.
	// if a request finds a canceled context, it doesn't
	// send its response.

}

func (srv *server[F]) newFid(id uint32) (*fid[F], error) {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	f, ok := srv.fids[id]
	if ok {
		return nil, fmt.Errorf("fid %d already in use", id)
	}
	f = &fid[F]{}
	srv.fids[id] = f
	return f, nil
}

type fidMode uint8

const (
	fExcl fidMode = 1 << iota
	fOpen
	fNotOpen
)

func (srv *server[F]) getFid(id uint32, mode fidMode) (*fid[F], error) {
	srv.mu.Lock()
	defer srv.mu.Unlock()

	f := srv.fids[id]
	if f == nil {
		return nil, fmt.Errorf("fid %d not found", id)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	// Check early on so that when the fid is acquired exclusively,
	// some fields can be modified without acquiring the mutex.
	if (mode&fExcl) != 0 && f.inUse {
		return nil, fmt.Errorf("fid is already in use")
	}
	if !f.attached {
		return nil, fmt.Errorf("fid %d is not usable yet (concurrent calls to create a new fid?)", id)
	}
	if (mode&fOpen) != 0 && !f.open {
		return nil, fmt.Errorf("fid must be opened first")
	}
	if (mode&fNotOpen) != 0 && f.open {
		return nil, fmt.Errorf("operation not allowed on open fid")
	}
	if (mode & fExcl) != 0 {
		f.inUse = true
	}
	// TODO take a reference on the fid to indicate that an
	// operation is using it, so that Clunk can wait for the
	// operations to complete.
	return f, nil
}

func (srv *server[F]) delFid(f *fid[F]) {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	delete(srv.fids, f.id)
}

// serverOps holds the server.handle* methods.
// We need to use an interface rather than use (*server).handle*
// directly because we can't use *server in a global variable
// without instantiation.
type serverOps interface {
	handleAttach(ctx context.Context, m *plan9.Fcall) error
	handleStat(ctx context.Context, m *plan9.Fcall) error
	handleWalk(ctx context.Context, m *plan9.Fcall) error
	handleOpen(ctx context.Context, m *plan9.Fcall) error
	handleRead(ctx context.Context, m *plan9.Fcall) error
	handleClunk(ctx context.Context, m *plan9.Fcall) error
}

var operations = map[uint8]func(srv serverOps, ctx context.Context, m *plan9.Fcall) error{
	//plan9.Tauth: serverOps.handleAuth,
	plan9.Tattach: serverOps.handleAttach,
	plan9.Tstat:   serverOps.handleStat,
	plan9.Twalk:   serverOps.handleWalk,
	plan9.Tread:   serverOps.handleRead,
	plan9.Topen:   serverOps.handleOpen,
	plan9.Tclunk:  serverOps.handleClunk,
}

func min[T constraints.Ordered](a, b T) T {
	if a < b {
		return a
	}
	return b
}
