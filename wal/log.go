package wal

import (
	"errors"

	"github.com/tamnd/search/checksum"
	"github.com/tamnd/search/page"
	"github.com/tamnd/search/vfs"
)

// The write-ahead log is a sidecar file (conventionally "<name>-wal") of
// append-only frames (spec 2063 doc 05 §6). Each frame is a self-describing,
// independently checksummed record of one logical operation; a COMMIT_MARKER
// frame seals a transaction. Recovery replays frames up to the last sealed
// transaction and discards a torn or partial tail, so a crash mid-append can
// never surface a half-written operation.
//
// At S1 the catalog commit path is made crash-atomic by the COW B+tree plus the
// atomic meta flip (doc 05 §4), which is the two-fsync data-then-meta barrier;
// the meta page is the single source of truth after a crash. The WAL is built
// here as the durable group-commit log for logical document operations and is
// wired as the document-durability path when documents arrive at S2. It is a
// complete, independently crash-tested component: frame encode/decode, append
// with fsync, recovery scan that stops at the first damaged frame, and
// checkpoint truncation.

// WAL file and frame geometry.
const (
	// HeaderSize is the fixed WAL file header length.
	HeaderSize = 64
	// FrameHeaderSize is the fixed per-frame header length; the payload follows.
	FrameHeaderSize = 40
)

// walMagic is the first four header bytes: ASCII "WAL1".
var walMagic = [4]byte{'W', 'A', 'L', '1'}

// formatVersion is the WAL on-disk version.
const formatVersion uint32 = 1

// header field offsets.
const (
	whMagic   = 0
	whVersion = 4
	whSalt    = 8
	whPageSz  = 16
	whCRC     = 60
)

// frame field offsets.
const (
	fhSeq         = 0
	fhTxnID       = 8
	fhOpType      = 16
	fhFlags       = 18
	fhPayloadLen  = 20
	fhDocID       = 24
	fhHeaderCRC   = 32
	fhPayloadCRC  = 36
	fhHeaderCover = 32 // header checksum covers bytes [0,32)
)

// OpType tags what a frame records (doc 05 §6.3).
type OpType uint16

const (
	// OpAddDoc records the addition or replacement of a document.
	OpAddDoc OpType = 0x01
	// OpDeleteDoc records the deletion of a document by id.
	OpDeleteDoc OpType = 0x02
	// OpUpdateMeta records a catalog metadata change.
	OpUpdateMeta OpType = 0x03
	// OpCommitMarker seals the preceding frames as one transaction.
	OpCommitMarker OpType = 0x04
	// OpCheckpoint records a checkpoint boundary.
	OpCheckpoint OpType = 0x05
)

// Errors from the WAL.
var (
	// ErrBadWALHeader is returned when the sidecar header is missing, the wrong
	// magic, the wrong version, or fails its checksum.
	ErrBadWALHeader = errors.New("search/wal: bad or missing WAL header")
	// ErrSaltMismatch is returned when the WAL salt does not match the database,
	// meaning the sidecar belongs to a different file or a previous incarnation.
	ErrSaltMismatch = errors.New("search/wal: WAL salt does not match the database")
)

// Frame is one decoded WAL record. Seq is assigned by Append; the caller
// supplies TxnID, Op, Flags, DocID, and the opaque Payload.
type Frame struct {
	Seq     uint64
	TxnID   uint64
	Op      OpType
	Flags   uint16
	DocID   uint64
	Payload []byte
}

// WAL is an open write-ahead log over one sidecar file.
type WAL struct {
	f    vfs.File
	salt uint64
	sync SyncLevel
	seq  uint64 // next frame sequence number
	end  int64  // current append offset (end of the last good frame)
}

// Create initializes a fresh WAL sidecar at path, writing the header with salt
// and truncating any prior content. It returns an open, empty log.
func Create(fsys vfs.VFS, path string, salt uint64, pageSize uint32, sync SyncLevel) (*WAL, error) {
	f, err := fsys.Open(path, true)
	if err != nil {
		return nil, err
	}
	if err := f.Truncate(0); err != nil {
		_ = f.Close()
		return nil, err
	}
	w := &WAL{f: f, salt: salt, sync: sync, seq: 0, end: HeaderSize}
	if err := w.writeHeader(pageSize); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return nil, err
	}
	return w, nil
}

// Open opens an existing WAL sidecar, validating the header against salt. The
// log is positioned for recovery; call Recover to read the committed frames and
// to position the append offset after the last good frame.
func Open(fsys vfs.VFS, path string, salt uint64) (*WAL, error) {
	f, err := fsys.Open(path, false)
	if err != nil {
		return nil, err
	}
	hb := make([]byte, HeaderSize)
	if _, err := f.ReadAt(hb, 0); err != nil {
		_ = f.Close()
		return nil, ErrBadWALHeader
	}
	if [4]byte{hb[0], hb[1], hb[2], hb[3]} != walMagic {
		_ = f.Close()
		return nil, ErrBadWALHeader
	}
	if page.U32(hb[whVersion:]) != formatVersion {
		_ = f.Close()
		return nil, ErrBadWALHeader
	}
	if !checksum.Verify(hb[:whCRC], page.U32(hb[whCRC:])) {
		_ = f.Close()
		return nil, ErrBadWALHeader
	}
	if page.U64(hb[whSalt:]) != salt {
		_ = f.Close()
		return nil, ErrSaltMismatch
	}
	return &WAL{f: f, salt: salt, sync: SyncFull, seq: 0, end: HeaderSize}, nil
}

// writeHeader writes the 64-byte WAL header.
func (w *WAL) writeHeader(pageSize uint32) error {
	hb := make([]byte, HeaderSize)
	copy(hb[whMagic:], walMagic[:])
	page.PutU32(hb[whVersion:], formatVersion)
	page.PutU64(hb[whSalt:], w.salt)
	page.PutU32(hb[whPageSz:], pageSize)
	page.PutU32(hb[whCRC:], checksum.Sum(hb[:whCRC]))
	_, err := w.f.WriteAt(hb, 0)
	return err
}

// Append writes one frame to the end of the log and assigns its Seq. It does not
// fsync; call Commit to seal a transaction durably. The returned Seq is the
// frame's sequence number.
func (w *WAL) Append(fr Frame) (uint64, error) {
	fr.Seq = w.seq
	buf := make([]byte, FrameHeaderSize+len(fr.Payload))
	page.PutU64(buf[fhSeq:], fr.Seq)
	page.PutU64(buf[fhTxnID:], fr.TxnID)
	page.PutU16(buf[fhOpType:], uint16(fr.Op))
	page.PutU16(buf[fhFlags:], fr.Flags)
	page.PutU32(buf[fhPayloadLen:], uint32(len(fr.Payload)))
	page.PutU64(buf[fhDocID:], fr.DocID)
	copy(buf[FrameHeaderSize:], fr.Payload)
	page.PutU32(buf[fhHeaderCRC:], checksum.Sum(buf[:fhHeaderCover]))
	page.PutU32(buf[fhPayloadCRC:], checksum.Sum(buf[FrameHeaderSize:]))
	if _, err := w.f.WriteAt(buf, w.end); err != nil {
		return 0, err
	}
	w.end += int64(len(buf))
	w.seq++
	return fr.Seq, nil
}

// Commit appends a COMMIT_MARKER for txnID and fsyncs per the sync level, making
// every frame up to and including the marker durable.
func (w *WAL) Commit(txnID uint64) error {
	if _, err := w.Append(Frame{TxnID: txnID, Op: OpCommitMarker}); err != nil {
		return err
	}
	if w.sync == SyncOff {
		return nil
	}
	return w.f.Sync()
}

// Recover scans the log from the start, returning every frame that belongs to a
// fully committed transaction: frames up to and including the last
// COMMIT_MARKER. A torn, short, or checksum-failing frame ends the scan, and any
// frames after the last marker (an interrupted transaction) are discarded. The
// append offset and sequence counter are positioned just past the last good
// frame so logging can resume.
func (w *WAL) Recover() ([]Frame, error) {
	size, err := w.f.Size()
	if err != nil {
		return nil, err
	}
	var (
		all          []Frame
		off          = int64(HeaderSize)
		lastMarker   = -1   // index in all of the last COMMIT_MARKER
		offAfterGood = off  // append offset after the last committed frame
		seqAfterGood uint64 // seq counter after the last committed frame
	)
	for off+FrameHeaderSize <= size {
		hb := make([]byte, FrameHeaderSize)
		if _, err := w.f.ReadAt(hb, off); err != nil {
			break
		}
		if !checksum.Verify(hb[:fhHeaderCover], page.U32(hb[fhHeaderCRC:])) {
			break // torn or garbage header: end of the good log
		}
		plen := int64(page.U32(hb[fhPayloadLen:]))
		if off+FrameHeaderSize+plen > size {
			break // payload truncated
		}
		payload := make([]byte, plen)
		if plen > 0 {
			if _, err := w.f.ReadAt(payload, off+FrameHeaderSize); err != nil {
				break
			}
		}
		if !checksum.Verify(payload, page.U32(hb[fhPayloadCRC:])) {
			break // torn payload
		}
		fr := Frame{
			Seq:     page.U64(hb[fhSeq:]),
			TxnID:   page.U64(hb[fhTxnID:]),
			Op:      OpType(page.U16(hb[fhOpType:])),
			Flags:   page.U16(hb[fhFlags:]),
			DocID:   page.U64(hb[fhDocID:]),
			Payload: payload,
		}
		all = append(all, fr)
		off += FrameHeaderSize + plen
		if fr.Op == OpCommitMarker {
			lastMarker = len(all) - 1
			offAfterGood = off
			seqAfterGood = fr.Seq + 1
		}
	}
	w.end = offAfterGood
	w.seq = seqAfterGood
	if lastMarker < 0 {
		return nil, nil
	}
	return all[:lastMarker+1], nil
}

// Checkpoint resets the log to empty after its frames have been folded into the
// main file. It truncates back to the header and rewrites it, so the next
// Append starts a fresh generation. The caller is responsible for having made
// the main file durable first.
func (w *WAL) Checkpoint(pageSize uint32) error {
	if err := w.f.Truncate(HeaderSize); err != nil {
		return err
	}
	if err := w.writeHeader(pageSize); err != nil {
		return err
	}
	w.seq = 0
	w.end = HeaderSize
	if w.sync != SyncOff {
		return w.f.Sync()
	}
	return nil
}

// FrameCount returns the number of frames appended since the last checkpoint.
func (w *WAL) FrameCount() uint64 { return w.seq }

// Sync flushes the log durably.
func (w *WAL) Sync() error { return w.f.Sync() }

// Close closes the sidecar file.
func (w *WAL) Close() error {
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}
