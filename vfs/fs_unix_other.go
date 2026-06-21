//go:build unix && !linux && !darwin

package vfs

import "syscall"

// ofdSetLk falls back to F_SETLK on unix platforms without open-file-description
// locks. These are process-owned, so they still exclude another process but not
// a second open in the same process; the engine's own writeMu covers the
// in-process case.
const ofdSetLk = syscall.F_SETLK

// isNetworkFS cannot classify the filesystem on these platforms, so it reports
// local and lets the lock proceed.
func isNetworkFS(fd uintptr) (bool, error) { return false, nil }
