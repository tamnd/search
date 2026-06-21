//go:build darwin

package vfs

import "syscall"

// ofdSetLk is F_OFD_SETLK on macOS, the non-blocking open-file-description
// set-lock command. Like Linux's OFD locks, it is owned by the open file
// description, so a second open in the same process conflicts and the lock is
// safe across goroutines.
const ofdSetLk = 90

// isNetworkFS reports whether fd lives on a filesystem where advisory locks are
// unsafe across hosts. macOS reports the filesystem type by name in statfs.
func isNetworkFS(fd uintptr) (bool, error) {
	var st syscall.Statfs_t
	if err := syscall.Fstatfs(int(fd), &st); err != nil {
		return false, err
	}
	switch fstypename(st.Fstypename[:]) {
	case "nfs", "smbfs", "webdav", "afpfs", "ftp":
		return true, nil
	}
	return false, nil
}

// fstypename turns the NUL-terminated int8 filesystem-name array from statfs
// into a Go string.
func fstypename(b []int8) string {
	buf := make([]byte, 0, len(b))
	for _, c := range b {
		if c == 0 {
			break
		}
		buf = append(buf, byte(c))
	}
	return string(buf)
}
