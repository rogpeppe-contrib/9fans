package server

import (
	"context"

	"9fans.net/go/plan9"
)

// Fid represents a handle to a server-side file.
//
// The zero instance of a Fid is considered to represent
// "no fid" and should not otherwise be used as a valid value.
type Fid any

// FsysInner represents a filesystem that can be wrapped by another
// filesystem (such as clonefsys.Fsys).
type FsysInner[F Fid, C any] interface {
	Fsys[F]
	// AttachInner sets dst to a fid that is associated with the
	// given "attach context" c. This method is not called
	// directly by Server as part of any 9P call, but is
	// used to propagate fid-specific data through a fileserver
	// tree.
	AttachInner(ctx context.Context, dst *F, c C) error
}

// Fsys represents the interface that must be implemented
// in order to provide a 9p server.
type Fsys[F any] interface {
	// Clone makes a copy of src and puts it into dsr.
	// Note that this method will
	// be called more often than actual Tclone calls
	// (for example, a Twalk call will always invoke Clone
	// before walking).
	//
	// A fid that's been opened will never be cloned.
	//
	// This method must be safe to call concurrently.
	Clone(dst, src *F)

	// Clunk discards an instance of F. Clunk will never be called while there are any running
	// I/O methods on f.
	//
	// This method will never be called concurrently on the same f.
	Clunk(f *F)

	// Qid returns the Qid for the file.
	Qid(f *F) plan9.Qid

	// Auth sets dst to  a new open auth fid associated with the given user and attach name.
	// It must represent a file with the QTAUTH bit set.
	//
	// This method must be safe to call concurrently.
	Auth(ctx context.Context, dst *F, uname, aname string) error

	// Attach attaches to a new tree, and sets dst to
	// an instance of F representing the root.
	// If auth is non-nil, it holds the auth fid.
	//
	// This method must be safe to call concurrently.
	Attach(ctx context.Context, dst *F, auth *F, uname, aname string) error

	// Stat returns information about the file, which may be open or not.
	//
	// This method must be safe to call concurrently.
	Stat(ctx context.Context, f *F) (plan9.Dir, error)

	// Wstat writes metadata about the file.
	//
	// This method must be safe to call concurrently.
	// TODO should we make this exclusive?
	Wstat(ctx context.Context, f *F, dir plan9.Dir) error

	// Walk walks f to the named element within the directory
	// represented by f .
	//
	// This method will never be called concurrently on the same f.
	Walk(ctx context.Context, f *F, name string) error

	// Open prepares a fid for I/O and returns its  associated iounit.
	// After it's been opened, no methods will be called other
	// than Readdir (if it's a directory), ReadAt or WriteAt (if it's a file)
	// or Clunk.
	//
	// This method will never be called concurrently on the same f.
	Open(ctx context.Context, f *F, mode uint8) (uint32, error)

	// Readdir reads directory entries from an open directory into
	// dir, starting at the number of entries into the directory.
	// It returns the number of entries read.
	//
	// This method will never be called concurrently on the same f.
	Readdir(ctx context.Context, f *F, dir []plan9.Dir, entryIndex int) (int, error)

	// ReadAt reads data from f into buf at the file offset off.
	//
	// This method must be safe to call concurrently.
	ReadAt(ctx context.Context, f *F, buf []byte, off int64) (int, error)

	// WriteAt writes data from buf into f at the file offset off.
	//
	// This method must be safe to call concurrently.
	WriteAt(ctx context.Context, f *F, buf []byte, off int64) (int, error)

	// Remove removes the file represented by f. Unlike 9p remove,
	// this does not imply a clunk - the Clunk method will be explicitly
	// called immediately after Remove.
	//
	// This method will never be called concurrently on the same f.
	Remove(ctx context.Context, f *F) error

	// Close is called when the entire server instance is being torn down.
	Close() error
}

// Synchronous returns the set of message types for which
// operations on f  will always return immediately.
// Operations in this list will be called synchronously
// within the server (no other methods will be called until the method
// returns)
//Synchronous(f F) OpSet

// QidBits returns how many bits of Qid path space
// this server uses (counting from least significant upwards).
// This enables Fsys implementations to wrap other Fsys
// without worrying about QID path clashes.
//QidBits() int
