//go:build linux

package vfs

import "syscall"

// ofdSetLk is F_OFD_SETLK, the non-blocking open-file-description set-lock
// command. OFD locks are owned by the open file description rather than the
// process, so they exclude a second open in the same process and are safe to
// hold across goroutines (Linux 3.15+).
const ofdSetLk = 37

// Network filesystem magic numbers from <linux/magic.h>. On these, POSIX
// advisory locks may be silently local to one host and cannot exclude another.
const (
	nfsSuperMagic = 0x6969
	smbSuperMagic = 0x517B
	cifsMagicNum  = 0xFF534D42
	smb2MagicNum  = 0xFE534D42
)

// isNetworkFS reports whether fd lives on a filesystem where advisory locks are
// unsafe across hosts.
func isNetworkFS(fd uintptr) (bool, error) {
	var st syscall.Statfs_t
	if err := syscall.Fstatfs(int(fd), &st); err != nil {
		return false, err
	}
	switch int64(st.Type) {
	case nfsSuperMagic, smbSuperMagic, cifsMagicNum, smb2MagicNum:
		return true, nil
	}
	return false, nil
}
