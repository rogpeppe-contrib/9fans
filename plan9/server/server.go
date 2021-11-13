package server

import (
	"constraints"
	"context"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"9fans.net/go/plan9"
)

const debug = false

type fid[Fid any] struct {
	id uint32

	// 1 for the fid table + 1 for every operation currently running on it.
	// The fid is clunked when it drops to zero.
	refCount int32

	// mu guards the rest of the fields in the fid.
	mu rwMutex

	// fid holds the associated Fsys data.
	fid Fid

	// attached holds whether the fid has been attached
	// (i.e. whether the fid field is valid)
	attached bool

	// open holds whether the fid has been opened.
	open bool

	// openMode holds the mode that the fid was opened in.
	openMode uint8

	// iounit holds the iounit of the file.
	iounit uint32

	// dirMu guards concurrent reads on a directory.
	// TODO make this into a mutex with locks that can be canceled.
	dirMu sync.Mutex

	// dirOffset holds the next directory byte offset. Guarded by mu.
	dirOffset int64

	// dirIndex holds the next directory entry index.
	dirIndex int

	// dirEntries holds remaining entries returned by Fsys.Readdir.
	dirEntries []plan9.Dir

	// dirEntryBuf holds a buffer of directory entries.
	dirEntryBuf []plan9.Dir
}

type xtag[Fid any] struct {
	m *plan9.Fcall
	// fid holds the existing fid associated with the operation, if any.
	fid *fid[Fid]
	// excl holds whether the above fid has been exclusively locked.
	excl bool
	// newFid holds the new fid being created by the operation, if any.
	newFid *fid[Fid]
}

type server[Fid any] struct {
	fs         Fsys[Fid]
	conn       io.ReadWriter
	mu         sync.Mutex
	fids       map[uint32]*fid[Fid]
	operations map[uint8]func(srv *server[Fid], ctx context.Context, t *xtag[Fid], m *plan9.Fcall) error
}

func Serve[Fid any](ctx context.Context, conn io.ReadWriter, fs Fsys[Fid]) error {
	srv := &server[Fid]{
		conn: conn,
		fs:   fs,
		fids: make(map[uint32]*fid[Fid]),
		operations: map[uint8]func(srv *server[Fid], ctx context.Context, t *xtag[Fid], m *plan9.Fcall) error{
			//plan9.Tauth: (*server[F]).handleAuth,
			plan9.Tattach: (*server[Fid]).handleAttach,
			plan9.Tstat:   (*server[Fid]).handleStat,
			plan9.Twalk:   (*server[Fid]).handleWalk,
			plan9.Tread:   (*server[Fid]).handleRead,
			plan9.Twrite:  (*server[Fid]).handleWrite,
			plan9.Topen:   (*server[Fid]).handleOpen,
			plan9.Tclunk:  (*server[Fid]).handleClunk,
		},
	}
	defer fs.Close()
	m, err := srv.readMessage()
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
		m, err := srv.readMessage()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		t := srv.newTag(ctx, m)
		if t == nil {
			continue
		}
		op := srv.operations[m.Type]
		if op == nil {
			srv.replyError(t, fmt.Errorf("bad operation type %v", m.Type))
			continue
		}
		if err := op(srv, ctx, t, m); err != nil {
			srv.replyError(t, err)
		}
	}
}

// Auth(ctx context.Context, uname, aname string) (F, error)

func (srv *server[Fid]) handleAttach(ctx context.Context, t *xtag[Fid], m *plan9.Fcall) error {
	//ctx = srv.newContext(ctx, m.Tag) TODO when flush is implemented
	go func() {
		var afid *Fid
		if t.fid != nil {
			afid = &t.fid.fid
		}
		err := srv.fs.Attach(ctx, &t.newFid.fid, afid, m.Uname, m.Aname)
		if err != nil {
			srv.replyError(t, err)
			return
		}
		t.newFid.attached = true
		q := srv.fs.Qid(&t.newFid.fid)
		if !q.IsDir() {
			srv.replyError(t, fmt.Errorf("root is not a directory"))
			return
		}
		srv.reply(t, &plan9.Fcall{
			Type: plan9.Rattach,
			Qid:  q,
		})
	}()
	return nil
}

func (srv *server[Fid]) handleStat(ctx context.Context, t *xtag[Fid], m *plan9.Fcall) error {
	go func() {
		dir, err := srv.fs.Stat(ctx, &t.fid.fid)
		if err != nil {
			srv.replyError(t, err)
			return
		}
		dir.Qid = srv.fs.Qid(&t.fid.fid)
		stat, err := dir.Bytes()
		if err != nil {
			srv.replyError(t, fmt.Errorf("cannot marshal Dir: %v", err))
			return
		}
		srv.reply(t, &plan9.Fcall{
			Type: plan9.Rstat,
			Stat: stat,
		})
	}()
	return nil
}

func (srv *server[Fid]) handleWalk(ctx context.Context, t *xtag[Fid], m *plan9.Fcall) error {
	if t.fid.open {
		return fmt.Errorf("cannot walk open fid")
	}
	go func() {
		qids, err := srv.walk(ctx, t.fid, t.newFid, m.Wname)
		if len(qids) == 0 && len(m.Wname) > 0 {
			srv.replyError(t, err)
			return
		}
		srv.reply(t, &plan9.Fcall{
			Type: plan9.Rwalk,
			Wqid: qids,
		})
	}()
	return nil
}

func (srv *server[Fid]) walk(ctx context.Context, fid, newFid *fid[Fid], names []string) (rqids []plan9.Qid, rerr error) {
	var newf *Fid
	if newFid != nil {
		newf = &newFid.fid
	} else {
		// Make a temporary clone so that we don't
		// side-effect the original if we fail half way through
		// walking.
		newf = new(Fid)
	}
	srv.fs.Clone(newf, &fid.fid)
	qids := make([]plan9.Qid, 0, len(names))
	for _, name := range names {
		if err := srv.fs.Walk(ctx, newf, name); err != nil {
			srv.fs.Clunk(newf)
			return qids, err
		}

		qids = append(qids, srv.fs.Qid(newf))
	}
	if newFid != nil {
		newFid.attached = true
	} else {
		srv.fs.Clunk(&fid.fid)
		srv.fs.Clone(&fid.fid, newf)
	}
	return qids, nil
}

func (srv *server[Fid]) handleOpen(ctx context.Context, t *xtag[Fid], m *plan9.Fcall) error {
	if t.fid.open {
		return fmt.Errorf("fid is already open")
	}
	if srv.isDir(t.fid) && (m.Mode == plan9.OWRITE ||
		m.Mode == plan9.ORDWR ||
		m.Mode == plan9.OEXEC) {
		return errPerm
	}
	// TODO handle OEXCL ?
	go func() {
		// TODO we could potentially help out by invoking src.fs.Stat
		// and checking permissions, but that does have the potential
		// to be racy.
		iounit, err := srv.fs.Open(ctx, &t.fid.fid, m.Mode)
		if err != nil {
			srv.replyError(t, err)
			return
		}
		if iounit == 0 {
			iounit = 8 * 1024
		}
		t.fid.open = true
		t.fid.openMode = m.Mode
		t.fid.iounit = iounit
		srv.reply(t, &plan9.Fcall{
			Type:   plan9.Ropen,
			Qid:    srv.fs.Qid(&t.fid.fid),
			Iounit: iounit,
		})
	}()
	return nil
}

func (srv *server[Fid]) handleRead(ctx context.Context, t *xtag[Fid], m *plan9.Fcall) error {
	if !t.fid.open {
		return fmt.Errorf("fid not open")
	}
	if !canRead(t.fid.openMode) {
		return errPerm
	}
	offset := int64(m.Offset)
	if offset < 0 || offset+int64(m.Count) < 0 {
		return fmt.Errorf("offset too big")
	}
	go func() {
		if srv.isDir(t.fid) {
			err := srv.readDir(ctx, t, offset, m.Count)
			if err != nil {
				srv.replyError(t, err)
			}
			return
		}
		buf := make([]byte, min(t.fid.iounit, m.Count))
		n, err := srv.fs.ReadAt(ctx, &t.fid.fid, buf, offset)
		if err != nil && err != io.EOF && n == 0 {
			srv.replyError(t, err)
			return
		}
		// TODO We might be ignoring an error here if it's returned along
		// with some bytes, but we'll hope that if the client
		// reissues the read they'll probably get the error again.
		srv.reply(t, &plan9.Fcall{
			Type: plan9.Rread,
			Data: buf[:n],
		})
	}()
	return nil
}

func (srv *server[Fid]) handleWrite(ctx context.Context, t *xtag[Fid], m *plan9.Fcall) error {
	if !t.fid.open {
		return fmt.Errorf("fid not open")
	}
	if !canWrite(t.fid.openMode) {
		return fmt.Errorf("cannot write; omode %v", t.fid.openMode)
		return errPerm
	}
	if srv.isDir(t.fid) {
		return fmt.Errorf("cannot write to a directory")
	}
	offset := int64(m.Offset)
	if offset < 0 || offset+int64(len(m.Data)) < 0 {
		return fmt.Errorf("offset too big")
	}
	go func() {
		n, err := srv.fs.WriteAt(ctx, &t.fid.fid, m.Data, offset)
		if err != nil {
			// TODO We're ignoring the fact that WriteAt might have written
			// some bytes here.
			srv.replyError(t, err)
			return
		}
		srv.reply(t, &plan9.Fcall{
			Type:  plan9.Rwrite,
			Count: uint32(n),
		})
	}()
	return nil
}

func (srv *server[Fid]) readDir(ctx context.Context, t *xtag[Fid], offset int64, count uint32) error {
	f := t.fid
	// Acquire an exclusive lock so that we can mutate directory reading state without
	// worrying about concurrent Tread operations.
	// TODO use context-aware lock
	//	if !t.fid.dirMu.Lock(ctx) {
	//		return ctx.Err()
	//	}
	t.fid.dirMu.Lock()
	defer t.fid.dirMu.Unlock()
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
			n, err := srv.fs.Readdir(ctx, &f.fid, f.dirEntryBuf, f.dirIndex)
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
	srv.reply(t, &plan9.Fcall{
		Type: plan9.Rread,
		Data: buf,
	})
	f.dirOffset += int64(len(buf))
	return nil
}

func (srv *server[Fid]) handleClunk(ctx context.Context, t *xtag[Fid], m *plan9.Fcall) error {
	go func() {
		srv.delFid(t.fid)
		srv.reply(t, &plan9.Fcall{
			Type: plan9.Rclunk,
			Fid:  m.Fid,
		})
	}()
	return nil
}

func (srv *server[Fid]) replyError(t *xtag[Fid], err error) {
	srv.reply(t, &plan9.Fcall{
		Type:  plan9.Rerror,
		Ename: err.Error(),
	})
}

func (srv *server[Fid]) reply(t *xtag[Fid], m *plan9.Fcall) {
	m.Tag = t.m.Tag
	fail := m.Type == plan9.Rerror || m.Type == plan9.Rwalk && len(m.Wqid) < len(m.Wname)
	srv.releaseTag(t, !fail)
	srv.sendMessage(m)
}

func (srv *server[Fid]) sendMessage(m *plan9.Fcall) {
	if debug {
		fmt.Printf("-> %v\n", m)
	}
	// TODO if there's a write error, close the server?
	plan9.WriteFcall(srv.conn, m)
}

func (srv *server[Fid]) readMessage() (*plan9.Fcall, error) {
	m, err := plan9.ReadFcall(srv.conn)
	if err != nil {
		return nil, err
	}
	if debug {
		fmt.Printf("<- %v\n", m)
	}
	return m, nil
}

func (srv *server[Fid]) handleFlush(m *plan9.Fcall) error {
	panic("TODO")
	// look for outstanding matching tag
	// if it's found, cancel its context and wait for it to return,
	// then send Rflush response.
	// if a request finds a canceled context, it doesn't
	// send its response.

	// Also, remember that if an operation is flushed and we don't
	// send its reply, we need to drop its fid reference.
}

func (srv *server[Fid]) newFid(id uint32) (*fid[Fid], error) {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	f, ok := srv.fids[id]
	if ok {
		return nil, fmt.Errorf("fid %d already in use", id)
	}
	f = &fid[Fid]{
		id: id,
	}
	srv.fids[id] = f
	return f, nil
}

func (srv *server[Fid]) releaseFid(f *fid[Fid]) {
	if atomic.AddInt32(&f.refCount, -1) == 0 && f.attached {
		srv.fs.Clunk(&f.fid)
	}
}

func (srv *server[Fid]) delFid(f *fid[Fid]) {
	srv.mu.Lock()
	if _, ok := srv.fids[f.id]; !ok {
		panic("delete of fid that's not in the fid table")
	}
	delete(srv.fids, f.id)
	srv.mu.Unlock()
	srv.releaseFid(f)
}

// newTag returns a new xtag instance for the operation implied by m.
//
// It acquires references to any fids in m and locks them appropriately.
// When the tag is released (with releaseTag), the references will be
// dropped and the locks unlocked.
func (srv *server[Fid]) newTag(ctx context.Context, m *plan9.Fcall) *xtag[Fid] {
	// TODO add the tag to srv.tags.
	t := &xtag[Fid]{
		m: m,
	}
	if err := srv.initTag(t, m); err != nil {
		srv.replyError(t, err)
		return nil
	}
	return t
}

func (srv *server[Fid]) initTag(t *xtag[Fid], m *plan9.Fcall) error {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	var nfid uint32 = plan9.NOFID
	switch m.Type {
	case plan9.Tauth:
		nfid = m.Afid
	case plan9.Twalk:
		if m.Newfid != m.Fid {
			nfid = m.Newfid
		}
	case plan9.Tattach:
		nfid = m.Fid
	}
	if nfid != plan9.NOFID {
		f, ok := srv.fids[nfid]
		if ok {
			return fmt.Errorf("fid %d already in use", nfid)
		}
		f = &fid[Fid]{
			id: nfid,
			// One reference for the fid table and one for the xtag.
			refCount: 2,
		}
		f.mu.lock(nil)
		srv.fids[nfid] = f
		t.newFid = f
	}
	var fid uint32 = plan9.NOFID
	switch m.Type {
	case plan9.Tversion,
		plan9.Tauth,
		plan9.Tflush:
		// The above operations don't refer to an existing fid.
	case plan9.Tattach:
		fid = m.Afid
	default:
		// All other operations refer to an existing fid.
		if m.Fid == plan9.NOFID {
			return fmt.Errorf("invalid fid %d", m.Fid)
		}
		fid = m.Fid
	}
	if fid == plan9.NOFID {
		// No fid to acquire, so we're all done.
		return nil
	}
	f := srv.fids[fid]
	if f == nil {
		return fmt.Errorf("invalid fid %d", fid)
	}
	excl := false
	// Determine whether it's an operation that modifies some of the content of the fid
	// and so requires an exclusive lock.
	switch m.Type {
	case plan9.Topen,
		plan9.Tremove,
		plan9.Tcreate,
		plan9.Tclunk:
		excl = true
	case plan9.Twalk:
		excl = m.Fid == m.Newfid
	}
	onFail := func() {}
	if m.Type == plan9.Tclunk || m.Type == plan9.Tremove {
		// For clunk and remove, the fid is clunked regardless of whether
		// the operation failed or not.
		// When we can't take out an exclusive lock, we don't
		// want to clunk the fid here, because that would imply
		// invoking srv.fs.Clunk here, which has potential for deadlock
		// because we're holding the global lock (and blocking the
		// main loop), so we delete the fid from the fid table while
		// the internal rwmutex lock is held, because otherwise there's
		// a race beteen failing to acquire the mutex and dropping the
		// refcount.
		//
		// Note that we know that there must be a reference to the fid
		// on failure because newTag always increments the reference count
		// when it acquires the mutex.
		//
		// TODO the onFail thing rather breaks the rwMutex abstraction.
		// Perhaps we'd be better hoisting the reader count directly up into
		// the fid type?
		onFail = func() {
			atomic.AddInt32(&f.refCount, -1)
			delete(srv.fids, fid)
		}
	}
	var ok bool
	if excl {
		ok = f.mu.lock(onFail)
	} else {
		ok = f.mu.rlock(onFail)
	}
	if !ok {
		return fmt.Errorf("fid in use")
	}
	t.excl = excl
	t.fid = f
	// Add a fid reference for the  tag.
	atomic.AddInt32(&f.refCount, 1)
	return nil
}

func (srv *server[Fid]) releaseTag(t *xtag[Fid], success bool) {
	if t.fid != nil {
		if t.excl {
			t.fid.mu.unlock()
		} else {
			t.fid.mu.runlock()
		}
		srv.releaseFid(t.fid)
		t.fid = nil
	}
	if t.newFid == nil {
		return
	}
	// newFid is always acquired exclusively.
	t.newFid.mu.unlock()
	srv.releaseFid(t.newFid)
	if success {
		return
	}
	// The request was asking to create a new fid, but failed,
	// so remove it from the table.
	srv.delFid(t.newFid)
}

func (srv *server[Fid]) isDir(f *fid[Fid]) bool {
	return srv.fs.Qid(&f.fid).IsDir()
}

func canRead(mode uint8) bool {
	switch mode & 3 {
	case plan9.OREAD, plan9.ORDWR, plan9.OEXEC:
		return true
	}
	return false
}

func canWrite(mode uint8) bool {
	switch mode & 3 {
	case plan9.OWRITE, plan9.ORDWR:
		return true
	}
	return false
}

func min[T constraints.Ordered](a, b T) T {
	if a < b {
		return a
	}
	return b
}
