package vfs

import "os"

// OS is the real-filesystem backend used in production.
type OS struct{}

// NewOS returns a VFS backed by the operating system filesystem.
func NewOS() VFS { return OS{} }

// Open implements vfs.VFS by opening name on the OS filesystem, creating it when
// create is true and reporting ErrNotExist when it is absent and create is false.
func (OS) Open(name string, create bool) (File, error) {
	flag := os.O_RDWR
	if create {
		flag |= os.O_CREATE
	}
	f, err := os.OpenFile(name, flag, 0o644)
	if err != nil {
		if os.IsNotExist(err) && !create {
			return nil, ErrNotExist
		}
		return nil, err
	}
	return &osFile{f: f}, nil
}

// Remove implements vfs.VFS by deleting name; removing an absent file is not an error.
func (OS) Remove(name string) error {
	err := os.Remove(name)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Exists implements vfs.VFS by reporting whether name is present on disk.
func (OS) Exists(name string) bool {
	_, err := os.Stat(name)
	return err == nil
}

type osFile struct{ f *os.File }

// ReadAt implements vfs.File by reading len(p) bytes at off.
func (o *osFile) ReadAt(p []byte, off int64) (int, error) { return o.f.ReadAt(p, off) }

// WriteAt implements vfs.File by writing p at off.
func (o *osFile) WriteAt(p []byte, off int64) (int, error) { return o.f.WriteAt(p, off) }

// Truncate implements vfs.File by setting the file size to n bytes.
func (o *osFile) Truncate(n int64) error { return o.f.Truncate(n) }

// Sync implements vfs.File by flushing buffered writes durably.
func (o *osFile) Sync() error { return o.f.Sync() }

// Close implements vfs.File by releasing the handle.
func (o *osFile) Close() error { return o.f.Close() }

// Size implements vfs.File by returning the current file size in bytes.
func (o *osFile) Size() (int64, error) {
	st, err := o.f.Stat()
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}
