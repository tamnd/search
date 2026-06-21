//go:build unix

package vfs

import (
	"io"
	"syscall"
)

// Lock takes an advisory whole-file lock through fcntl. It uses open-file-
// description locks where the platform offers them (Linux and macOS), so two
// open handles in the same process conflict the same way two processes do,
// which is what the engine needs to exclude a second opener and to be safe
// across goroutines. A conflicting lock returns ErrLocked rather than blocking.
func (o *osFile) Lock(exclusive bool) error {
	if net, err := isNetworkFS(o.f.Fd()); err == nil && net {
		return ErrUnsupportedFilesystem
	}
	typ := int16(syscall.F_RDLCK)
	if exclusive {
		typ = int16(syscall.F_WRLCK)
	}
	lk := syscall.Flock_t{Type: typ, Whence: io.SeekStart, Start: 0, Len: 0}
	err := syscall.FcntlFlock(o.f.Fd(), ofdSetLk, &lk)
	if err == syscall.EACCES || err == syscall.EAGAIN {
		return ErrLocked
	}
	return err
}

// Unlock releases the whole-file lock.
func (o *osFile) Unlock() error {
	lk := syscall.Flock_t{Type: int16(syscall.F_UNLCK), Whence: io.SeekStart, Start: 0, Len: 0}
	return syscall.FcntlFlock(o.f.Fd(), ofdSetLk, &lk)
}
