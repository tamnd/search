package postings

import (
	"encoding/binary"
	"fmt"
)

// NoMore is the sentinel doc-id returned once a postings iterator is exhausted.
const NoMore = uint32(0xFFFFFFFF)

// Encode serializes a term's postings into a doc stream and a parallel position
// stream. docs must be ascending and unique; freqs[i] is the term frequency of
// docs[i]. positions may be nil (positions not indexed); otherwise it must have
// one slice per document and positions[i] must contain freqs[i] entries in
// ascending order. The returned posBlob is empty when positions is nil.
func Encode(docs, freqs []uint32, positions [][]uint32) (docBlob, posBlob []byte, err error) {
	if len(docs) != len(freqs) {
		return nil, nil, fmt.Errorf("postings: %d docs but %d freqs", len(docs), len(freqs))
	}
	hasPos := positions != nil
	if hasPos && len(positions) != len(docs) {
		return nil, nil, fmt.Errorf("postings: %d docs but %d position lists", len(docs), len(positions))
	}

	// Build the position stream and record each document's byte offset into it.
	posOffsets := make([]uint32, len(docs))
	if hasPos {
		for i := range docs {
			posOffsets[i] = uint32(len(posBlob))
			var prev uint32
			for j, p := range positions[i] {
				if j == 0 {
					posBlob = binary.AppendUvarint(posBlob, uint64(p))
				} else {
					posBlob = binary.AppendUvarint(posBlob, uint64(p-prev))
				}
				prev = p
			}
		}
	}

	// Encode doc blocks and collect skip entries.
	type skipEntry struct {
		lastDoc uint32
		offset  uint32
	}
	var blocks []byte
	var skips []skipEntry
	var prevLast uint32
	for start := 0; start < len(docs); start += BlockSize {
		end := min(start+BlockSize, len(docs))
		n := end - start

		deltas := make([]uint32, n)
		base := prevLast
		for i := range n {
			deltas[i] = docs[start+i] - base
			base = docs[start+i]
		}
		offset := uint32(len(blocks))
		blocks = appendSub(blocks, pforEncode(deltas))
		blocks = appendSub(blocks, pforEncode(freqs[start:end]))
		if hasPos {
			blocks = appendSub(blocks, pforEncode(posOffsets[start:end]))
		}
		skips = append(skips, skipEntry{lastDoc: docs[end-1], offset: offset})
		prevLast = docs[end-1]
	}

	var hdr []byte
	hdr = binary.AppendUvarint(hdr, uint64(len(docs)))
	hdr = binary.AppendUvarint(hdr, uint64(len(skips)))
	if hasPos {
		hdr = append(hdr, 1)
	} else {
		hdr = append(hdr, 0)
	}
	for _, s := range skips {
		hdr = binary.AppendUvarint(hdr, uint64(s.lastDoc))
		hdr = binary.AppendUvarint(hdr, uint64(s.offset))
	}
	docBlob = append(hdr, blocks...)
	return docBlob, posBlob, nil
}

// appendSub appends a length-prefixed sub-block.
func appendSub(dst, sub []byte) []byte {
	dst = binary.AppendUvarint(dst, uint64(len(sub)))
	return append(dst, sub...)
}

// Reader iterates a term's postings, decoding one block at a time.
type Reader struct {
	docCount   int
	hasPos     bool
	skipLast   []uint32 // last doc-id of each block
	skipOffset []uint32 // byte offset of each block within blocks region
	blocks     []byte   // blocks region
	posBlob    []byte

	curBlock  int      // index of the loaded block, -1 before first load
	docIDs    []uint32 // absolute doc-ids of loaded block
	freqs     []uint32
	posOffs   []uint32
	posInBlk  int
	curDoc    uint32
	curFreq   uint32
	exhausted bool
}

// Open parses a postings blob produced by Encode. posBlob may be nil when the
// term has no indexed positions.
func Open(docBlob, posBlob []byte) (*Reader, error) {
	p := 0
	docCount, m := binary.Uvarint(docBlob[p:])
	if m <= 0 {
		return nil, fmt.Errorf("postings: bad doc count")
	}
	p += m
	blockCount, m := binary.Uvarint(docBlob[p:])
	if m <= 0 {
		return nil, fmt.Errorf("postings: bad block count")
	}
	p += m
	if p >= len(docBlob) {
		return nil, fmt.Errorf("postings: missing position flag")
	}
	hasPos := docBlob[p] == 1
	p++

	r := &Reader{
		docCount: int(docCount),
		hasPos:   hasPos,
		posBlob:  posBlob,
		curBlock: -1,
	}
	for range blockCount {
		last, mm := binary.Uvarint(docBlob[p:])
		if mm <= 0 {
			return nil, fmt.Errorf("postings: bad skip last-doc")
		}
		p += mm
		off, mo := binary.Uvarint(docBlob[p:])
		if mo <= 0 {
			return nil, fmt.Errorf("postings: bad skip offset")
		}
		p += mo
		r.skipLast = append(r.skipLast, uint32(last))
		r.skipOffset = append(r.skipOffset, uint32(off))
	}
	r.blocks = docBlob[p:]
	return r, nil
}

// Count returns the number of documents in the postings list (the doc frequency).
func (r *Reader) Count() int { return r.docCount }

// Positional reports whether this list carries token positions.
func (r *Reader) Positional() bool { return r.hasPos }

// blockLen returns the document count of block i.
func (r *Reader) blockLen(i int) int {
	return min(r.docCount-i*BlockSize, BlockSize)
}

// loadBlock decodes block i into the reader's scratch arrays.
func (r *Reader) loadBlock(i int) error {
	n := r.blockLen(i)
	data := r.blocks[r.skipOffset[i]:]
	p := 0

	readSub := func() ([]byte, error) {
		l, m := binary.Uvarint(data[p:])
		if m <= 0 {
			return nil, fmt.Errorf("postings: bad sub-block length")
		}
		p += m
		if p+int(l) > len(data) {
			return nil, fmt.Errorf("postings: truncated sub-block")
		}
		sub := data[p : p+int(l)]
		p += int(l)
		return sub, nil
	}

	docSub, err := readSub()
	if err != nil {
		return err
	}
	deltas, err := pforDecode(docSub, n)
	if err != nil {
		return err
	}
	var base uint32
	if i > 0 {
		base = r.skipLast[i-1]
	}
	r.docIDs = make([]uint32, n)
	for j := range n {
		base += deltas[j]
		r.docIDs[j] = base
	}

	freqSub, err := readSub()
	if err != nil {
		return err
	}
	r.freqs, err = pforDecode(freqSub, n)
	if err != nil {
		return err
	}

	if r.hasPos {
		posSub, err := readSub()
		if err != nil {
			return err
		}
		r.posOffs, err = pforDecode(posSub, n)
		if err != nil {
			return err
		}
	}

	r.curBlock = i
	r.posInBlk = 0
	return nil
}

// Next advances to the next document, returning its doc-id, term frequency, and
// whether a document was found. After exhaustion it returns NoMore, 0, false.
func (r *Reader) Next() (doc, freq uint32, ok bool, err error) {
	if r.exhausted {
		return NoMore, 0, false, nil
	}
	if r.curBlock < 0 {
		if r.docCount == 0 {
			r.exhausted = true
			return NoMore, 0, false, nil
		}
		if err := r.loadBlock(0); err != nil {
			return 0, 0, false, err
		}
	} else {
		r.posInBlk++
		if r.posInBlk >= r.blockLen(r.curBlock) {
			next := r.curBlock + 1
			if next >= len(r.skipLast) {
				r.exhausted = true
				return NoMore, 0, false, nil
			}
			if err := r.loadBlock(next); err != nil {
				return 0, 0, false, err
			}
		}
	}
	r.curDoc = r.docIDs[r.posInBlk]
	r.curFreq = r.freqs[r.posInBlk]
	return r.curDoc, r.curFreq, true, nil
}

// SkipTo advances to the first document with doc-id >= target, returning it. It
// uses the skip list to jump whole blocks. If no such document exists it returns
// NoMore, 0, false.
func (r *Reader) SkipTo(target uint32) (doc, freq uint32, ok bool, err error) {
	if r.exhausted {
		return NoMore, 0, false, nil
	}
	// Find the first block whose last doc-id is >= target.
	blk := 0
	for blk < len(r.skipLast) && r.skipLast[blk] < target {
		blk++
	}
	if blk >= len(r.skipLast) {
		r.exhausted = true
		return NoMore, 0, false, nil
	}
	if blk != r.curBlock {
		if err := r.loadBlock(blk); err != nil {
			return 0, 0, false, err
		}
	}
	// Scan forward within the block. When already positioned in this block past
	// some docs, do not rewind before the current cursor.
	n := r.blockLen(blk)
	for r.posInBlk < n && r.docIDs[r.posInBlk] < target {
		r.posInBlk++
	}
	if r.posInBlk >= n {
		// Should not happen given skipLast >= target, but guard anyway.
		return r.Next()
	}
	r.curDoc = r.docIDs[r.posInBlk]
	r.curFreq = r.freqs[r.posInBlk]
	return r.curDoc, r.curFreq, true, nil
}

// Positions returns the positions of the current document. It is valid only
// after a successful Next or SkipTo and only when the term was indexed with
// positions.
func (r *Reader) Positions() ([]uint32, error) {
	if !r.hasPos {
		return nil, fmt.Errorf("postings: term has no indexed positions")
	}
	if r.curBlock < 0 {
		return nil, fmt.Errorf("postings: Positions before Next")
	}
	off := r.posOffs[r.posInBlk]
	out := make([]uint32, r.curFreq)
	p := int(off)
	var prev uint32
	for i := range int(r.curFreq) {
		d, m := binary.Uvarint(r.posBlob[p:])
		if m <= 0 {
			return nil, fmt.Errorf("postings: truncated positions")
		}
		p += m
		if i == 0 {
			prev = uint32(d)
		} else {
			prev += uint32(d)
		}
		out[i] = prev
	}
	return out, nil
}
