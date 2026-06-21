package vfs

import "errors"

// Locker is the optional capability a File implements when it supports advisory
// whole-file locking for multi-process exclusion. The OS backend implements it
// on platforms with open-file-description locks; the in-memory backend does not,
// since a single process owns it. The pager type-asserts for this interface and
// skips locking when a backend does not provide it.
type Locker interface {
	// Lock takes an advisory whole-file lock: shared when exclusive is false,
	// exclusive when true. It is non-blocking. If another open file description
	// (in this or another process) holds a conflicting lock it returns
	// ErrLocked. On a filesystem where advisory locks are not safe it returns
	// ErrUnsupportedFilesystem.
	Lock(exclusive bool) error
	// Unlock releases a lock taken by Lock. Closing the file also releases it.
	Unlock() error
}

// ErrLocked is returned when a conflicting advisory lock is already held, so the
// open would race another writer.
var ErrLocked = errors.New("search/vfs: index is locked by another process")

// ErrUnsupportedFilesystem is returned when the file lives on a filesystem where
// advisory locks cannot be trusted to exclude other hosts, such as NFS. The
// caller may proceed with locking disabled by opting out explicitly.
var ErrUnsupportedFilesystem = errors.New("search/vfs: advisory locks are unsafe on this filesystem")
