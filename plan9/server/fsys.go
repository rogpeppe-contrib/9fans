package server

import (
	"context"

	"9fans.net/go/plan9"
)

// Fid represents a handle to a server-side file.
//
// The zero instance of a Fid is considered to represent
// "no fid" and should not otherwise be used as a valid value.
type Fid interface {
	comparable
	Qid() plan9.Qid
}

// Fsys represents the interface that must be implemented
// in order to provide a 9p server.
//
// Some methods (specifically Walk and Open) can choose
// whether or not to return a new instance of F. If they do,
// the old one will be clunked.
type Fsys[F Fid] interface {
	// Auth returns a new auth fid associated with the given user and attach name.
	// TODO should the returned fid be considered open?
	Auth(ctx context.Context, uname, aname string) (F, error)
	// Attach attaches to the root of a new tree, returning a new

	Attach(ctx context.Context, auth *F, uname, aname string) (F, error)
	Stat(ctx context.Context, f F) (plan9.Dir, error)
	Wstat(ctx context.Context, f F, dir plan9.Dir) error
	Clone(ctx context.Context, f F) (F, error)

	// Walk walks to the named element within the directory
	// represented by f and returns a handle to that element.
	Walk(ctx context.Context, f F, name string) (F, error)

	// Open prepares a fid for I/O.
	// After it's been opened, no methods will be called other
	// than Readdir (if it's a directory), ReadAt or WriteAt (if it's a file)
	// or Clunk.
	// Open returns the opened file and the its associated iounit.
	Open(ctx context.Context, f F, mode uint8) (F, uint32, error)
	Readdir(ctx context.Context, f F, dir []plan9.Dir, entryIndex int) (int, error)
	ReadAt(ctx context.Context, f F, buf []byte, off int64) (int, error)
	WriteAt(ctx context.Context, f F, buf []byte, off int64) (int, error)

	// Clunk discards an instance of F. Clunk will never be called while there are any running
	// I/O methods on f.
	Clunk(ctx context.Context, f F)

	Remove(ctx context.Context, f F) error
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

	// Close is called when the entire server instance is being torn down.
	Close() error
}
