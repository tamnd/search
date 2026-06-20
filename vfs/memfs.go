package vfs

import (
	"sync"
)

// Mem is an in-memory VFS with fault injection. It models the full fault
// taxonomy the crash-recovery tests need (doc 20): clean crashes at any write or
// sync boundary, torn writes (a sector-granular partial write), and fsync
// failures. Files live entirely in memory; a "crash" is modeled by snapshotting
// the durable state (what survived the last successful Sync of each file, plus
// any post-sync writes that did reach storage) and discarding volatile state.
//
// The model is sector-oriented: a torn write writes only a prefix of whole
// sectors, mirroring how real storage tears at sector boundaries.
type Mem struct {
	mu    sync.Mutex
	files map[string]*memData
	// faults, when set, controls injection. nil means no injection (plain RAM).
	faults *FaultController
}

// SectorSize is the granularity of torn-write tearing.
const SectorSize = 512

// memData is the persistent bytes of one file (the "media").
type memData struct {
	data []byte
}

// NewMem returns an in-memory VFS with no fault injection.
func NewMem() *Mem {
	return &Mem{files: make(map[string]*memData)}
}

// NewMemWithFaults returns an in-memory VFS driven by the given controller.
func NewMemWithFaults(fc *FaultController) *Mem {
	return &Mem{files: make(map[string]*memData), faults: fc}
}

// Faults returns the fault controller (may be nil).
func (m *Mem) Faults() *FaultController { return m.faults }

// Attach installs a fault controller on an existing VFS (whose media may already
// hold a database). This lets a crash test seed a clean, committed file, then
// inject faults only over the transaction workload that follows, isolating the
// crash campaign to the commit path rather than file creation.
func (m *Mem) Attach(fc *FaultController) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.faults = fc
}

func (m *Mem) Open(name string, create bool) (File, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.files[name]
	if !ok {
		if !create {
			return nil, ErrNotExist
		}
		d = &memData{}
		m.files[name] = d
	}
	return &memFile{fs: m, d: d}, nil
}

func (m *Mem) Remove(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.files, name)
	return nil
}

func (m *Mem) Exists(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.files[name]
	return ok
}

// Snapshot copies the entire media (all files' durable bytes) so a test can
// reopen a fresh VFS positioned at the crash point. Volatile, never-persisted
// state is whatever the file objects buffered but did not write through; in this
// model WriteAt writes straight to media (write-through), and durability is
// governed by what the fault controller permits, so a Snapshot taken at a crash
// point is exactly the post-crash media.
func (m *Mem) Snapshot() *Mem {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := &Mem{files: make(map[string]*memData, len(m.files))}
	for name, d := range m.files {
		nb := make([]byte, len(d.data))
		copy(nb, d.data)
		cp.files[name] = &memData{data: nb}
	}
	return cp
}

type memFile struct {
	fs *Mem
	d  *memData
}

func (f *memFile) ReadAt(p []byte, off int64) (int, error) {
	f.fs.mu.Lock()
	defer f.fs.mu.Unlock()
	if off >= int64(len(f.d.data)) {
		// Reads past EOF return zeros for the in-bounds part; here it's all OOB.
		return 0, errEOF
	}
	n := copy(p, f.d.data[off:])
	if n < len(p) {
		// zero-fill the tail to mimic a sparse/truncated read boundary
		for i := n; i < len(p); i++ {
			p[i] = 0
		}
	}
	return len(p), nil
}

func (f *memFile) WriteAt(p []byte, off int64) (int, error) {
	f.fs.mu.Lock()
	defer f.fs.mu.Unlock()

	// Fault injection: decide whether this write crashes, tears, or proceeds.
	action := actionProceed
	if f.fs.faults != nil {
		action = f.fs.faults.onWrite(int64(len(p)))
	}

	switch action {
	case actionCrashBefore:
		return 0, ErrInjectedCrash
	case actionTear:
		// Write only a whole-sector prefix, then crash. Model a torn write.
		torn := tornPrefix(len(p))
		f.growAndCopy(p[:torn], off)
		return torn, ErrInjectedCrash
	default:
		f.growAndCopy(p, off)
		return len(p), nil
	}
}

// growAndCopy writes p at off into the media, growing it if needed. Caller holds
// the mutex.
func (f *memFile) growAndCopy(p []byte, off int64) {
	end := off + int64(len(p))
	if end > int64(len(f.d.data)) {
		grown := make([]byte, end)
		copy(grown, f.d.data)
		f.d.data = grown
	}
	copy(f.d.data[off:], p)
}

func (f *memFile) Truncate(n int64) error {
	f.fs.mu.Lock()
	defer f.fs.mu.Unlock()
	if n < int64(len(f.d.data)) {
		f.d.data = f.d.data[:n]
	} else if n > int64(len(f.d.data)) {
		grown := make([]byte, n)
		copy(grown, f.d.data)
		f.d.data = grown
	}
	return nil
}

func (f *memFile) Sync() error {
	if f.fs.faults != nil {
		switch f.fs.faults.onSync() {
		case actionFsyncFail:
			return ErrInjectedCrash
		case actionCrashBefore:
			return ErrInjectedCrash
		}
	}
	return nil
}

func (f *memFile) Size() (int64, error) {
	f.fs.mu.Lock()
	defer f.fs.mu.Unlock()
	return int64(len(f.d.data)), nil
}

func (f *memFile) Close() error { return nil }

func tornPrefix(n int) int {
	sectors := n / SectorSize
	if sectors <= 1 {
		return 0 // a single sector tears to nothing
	}
	return (sectors / 2) * SectorSize
}
