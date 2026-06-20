package wal

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	"github.com/tamnd/search/vfs"
)

const testSalt = 0x0123456789ABCDEF

func mustClose(t *testing.T, w *WAL) {
	t.Helper()
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestWALAppendRecover(t *testing.T) {
	fs := vfs.NewMem()
	w, err := Create(fs, "x-wal", testSalt, 4096, SyncFull)
	if err != nil {
		t.Fatal(err)
	}
	// Two committed transactions.
	for txn := uint64(1); txn <= 2; txn++ {
		for d := range 3 {
			if _, err := w.Append(Frame{TxnID: txn, Op: OpAddDoc, DocID: txn*10 + uint64(d), Payload: fmt.Appendf(nil, "doc-%d-%d", txn, d)}); err != nil {
				t.Fatal(err)
			}
		}
		if err := w.Commit(txn); err != nil {
			t.Fatal(err)
		}
	}
	mustClose(t, w)

	// Reopen and recover: 2 txns × (3 adds + 1 marker) = 8 frames.
	w2, err := Open(fs, "x-wal", testSalt)
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, w2)
	frames, err := w2.Recover()
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 8 {
		t.Fatalf("recovered %d frames, want 8", len(frames))
	}
	if frames[0].Op != OpAddDoc || !bytes.Equal(frames[0].Payload, []byte("doc-1-0")) {
		t.Fatalf("frame 0 unexpected: %+v", frames[0])
	}
	if frames[7].Op != OpCommitMarker || frames[7].TxnID != 2 {
		t.Fatalf("last frame should seal txn 2: %+v", frames[7])
	}
}

func TestWALPartialTail(t *testing.T) {
	fs := vfs.NewMem()
	w, err := Create(fs, "x-wal", testSalt, 4096, SyncFull)
	if err != nil {
		t.Fatal(err)
	}
	// One fully committed transaction.
	if _, err := w.Append(Frame{TxnID: 1, Op: OpAddDoc, DocID: 1, Payload: []byte("committed")}); err != nil {
		t.Fatal(err)
	}
	if err := w.Commit(1); err != nil {
		t.Fatal(err)
	}
	endGood, _ := fs.Open("x-wal", false)
	goodSize, _ := endGood.Size()

	// Begin a second transaction but never seal it, then corrupt its tail to
	// simulate a torn append at the moment of a crash.
	if _, err := w.Append(Frame{TxnID: 2, Op: OpAddDoc, DocID: 2, Payload: []byte("uncommitted")}); err != nil {
		t.Fatal(err)
	}
	mustClose(t, w)
	// Tear the tail: flip a byte inside the uncommitted frame's header.
	f, _ := fs.Open("x-wal", false)
	one := make([]byte, 1)
	if _, err := f.ReadAt(one, goodSize+4); err != nil {
		t.Fatal(err)
	}
	one[0] ^= 0xFF
	if _, err := f.WriteAt(one, goodSize+4); err != nil {
		t.Fatal(err)
	}

	w2, err := Open(fs, "x-wal", testSalt)
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, w2)
	frames, err := w2.Recover()
	if err != nil {
		t.Fatal(err)
	}
	// Only the committed transaction survives: 1 add + 1 marker.
	if len(frames) != 2 {
		t.Fatalf("recovered %d frames, want 2 (torn tail discarded)", len(frames))
	}
	if frames[0].TxnID != 1 || !bytes.Equal(frames[0].Payload, []byte("committed")) {
		t.Fatalf("survivor frame wrong: %+v", frames[0])
	}
	// The append offset is positioned to overwrite the discarded tail.
	if w2.end != goodSize {
		t.Fatalf("append offset = %d, want %d (just past last committed frame)", w2.end, goodSize)
	}
}

func TestWALCheckpoint(t *testing.T) {
	fs := vfs.NewMem()
	w, err := Create(fs, "x-wal", testSalt, 4096, SyncFull)
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, w)
	for d := range 5 {
		if _, err := w.Append(Frame{TxnID: 1, Op: OpAddDoc, DocID: uint64(d)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Commit(1); err != nil {
		t.Fatal(err)
	}
	if w.FrameCount() == 0 {
		t.Fatal("frame count should be non-zero before checkpoint")
	}
	if err := w.Checkpoint(4096); err != nil {
		t.Fatal(err)
	}
	if w.FrameCount() != 0 {
		t.Fatalf("frame count = %d after checkpoint, want 0", w.FrameCount())
	}
	// A reopened, checkpointed log recovers no committed frames.
	frames, err := w.Recover()
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 0 {
		t.Fatalf("recovered %d frames after checkpoint, want 0", len(frames))
	}
}

func TestWALSaltMismatch(t *testing.T) {
	fs := vfs.NewMem()
	w, err := Create(fs, "x-wal", testSalt, 4096, SyncFull)
	if err != nil {
		t.Fatal(err)
	}
	mustClose(t, w)
	if _, err := Open(fs, "x-wal", testSalt+1); !errors.Is(err, ErrSaltMismatch) {
		t.Fatalf("open with wrong salt err = %v, want ErrSaltMismatch", err)
	}
}
