package vfs

import "os"

// OS is the real-filesystem backend used in production.
type OS struct{}

// NewOS returns a VFS backed by the operating system filesystem.
func NewOS() VFS { return OS{} }

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

func (OS) Remove(name string) error {
	err := os.Remove(name)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (OS) Exists(name string) bool {
	_, err := os.Stat(name)
	return err == nil
}

type osFile struct{ f *os.File }

func (o *osFile) ReadAt(p []byte, off int64) (int, error)  { return o.f.ReadAt(p, off) }
func (o *osFile) WriteAt(p []byte, off int64) (int, error) { return o.f.WriteAt(p, off) }
func (o *osFile) Truncate(n int64) error                   { return o.f.Truncate(n) }
func (o *osFile) Sync() error                              { return o.f.Sync() }
func (o *osFile) Close() error                             { return o.f.Close() }

func (o *osFile) Size() (int64, error) {
	st, err := o.f.Stat()
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}
