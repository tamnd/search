package wal

import (
	"testing"

	"github.com/tamnd/search/vfs"
)

// FuzzWALReplay writes a valid WAL header followed by arbitrary bytes and asserts
// recovery never panics and always honours the commit invariant: the frames it
// returns are exactly the prefix up to and including the last COMMIT_MARKER, so a
// torn or partial frame past the last commit is never surfaced.
func FuzzWALReplay(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0x00, 0x01, 0x02, 0x03})
	f.Add(make([]byte, FrameHeaderSize))
	f.Fuzz(func(t *testing.T, tail []byte) {
		const salt = 0x5151515151515151
		fsys := vfs.NewMem()

		// Lay down a well-formed header so Open succeeds, then append the fuzzed
		// bytes as the (possibly garbage) frame region.
		w, err := Create(fsys, "f.wal", salt, 4096, SyncOff)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if err := w.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		fh, err := fsys.Open("f.wal", false)
		if err != nil {
			t.Fatalf("Open file: %v", err)
		}
		if len(tail) > 0 {
			if _, err := fh.WriteAt(tail, int64(HeaderSize)); err != nil {
				t.Fatalf("WriteAt: %v", err)
			}
		}
		_ = fh.Sync()
		_ = fh.Close()

		wr, err := Open(fsys, "f.wal", salt)
		if err != nil {
			// A corrupt header is a legitimate refusal, not a panic.
			return
		}
		frames, err := wr.Recover()
		_ = wr.Close()
		if err != nil {
			return
		}
		// Invariant: recovered frames end exactly at the last commit marker. If
		// any commit marker is present, the final frame must be one.
		lastCommit := -1
		for i, fr := range frames {
			if fr.Op == OpCommitMarker {
				lastCommit = i
			}
		}
		if lastCommit != len(frames)-1 {
			t.Fatalf("recovered %d frames but last commit is at %d: torn tail leaked", len(frames), lastCommit)
		}
	})
}
