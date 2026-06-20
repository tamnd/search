package pager

import "github.com/tamnd/search/page"

// offset returns the byte offset of page id in the file.
func (p *Pager) offset(id page.PageID) int64 { return int64(id) * int64(p.pageSize) }

// writeRawPage0 writes the file header into page 0 and zero-pads the rest of the
// page. Page 0 is special: it has no common page header, it is the header
// structure at byte 0 with its own internal CRC.
func (p *Pager) writeRawPage0() error {
	buf := make([]byte, p.pageSize)
	copy(buf, p.header.Marshal())
	_, err := p.f.WriteAt(buf, 0)
	return err
}

// writeMeta serializes a meta page into slot (page 1 or 2): a common page header
// of type PageMeta wrapping the 96-byte meta body, with the body's own CRC and
// the page checksums. The page_txn_id in the common header mirrors the meta's
// txn id (doc 02 §5.2).
func (p *Pager) writeMeta(slot page.PageID, m page.Meta) error {
	buf := make([]byte, p.pageSize)
	m.MarshalInto(page.Body(buf))
	hdr := page.NewPageHeader(page.PageMeta, p.pageSize, m.TxnID)
	page.WritePage(buf, hdr)
	_, err := p.f.WriteAt(buf, p.offset(slot))
	return err
}

// writeZeroPage writes a fully zeroed page at id. A zeroed meta slot fails its
// CRC check and is treated as invalid by the selection algorithm.
func (p *Pager) writeZeroPage(id page.PageID) error {
	buf := make([]byte, p.pageSize)
	_, err := p.f.WriteAt(buf, p.offset(id))
	return err
}

// readMetaSlot reads the meta body from page 1 or 2 and parses it. It returns
// the meta and whether the slot is valid. An invalid slot (bad CRC, torn write,
// never written) is reported as not-ok rather than an error so the selection
// algorithm can fall back to the other slot.
func (p *Pager) readMetaSlot(slot page.PageID) (page.Meta, bool) {
	buf := make([]byte, p.pageSize)
	if _, err := p.f.ReadAt(buf, p.offset(slot)); err != nil {
		return page.Meta{}, false
	}
	return page.ParseMeta(page.Body(buf))
}

// AllocPage extends the file by one page of the given type and returns its id.
// At S0 allocation is append-only: it bumps the high-water mark and writes a
// checksummed, empty typed page so the file actually grows and a subsequent
// ReadPage of the new page succeeds. The freelist-backed allocator arrives at
// S1. The new high-water mark is not durable until a meta commit records it.
func (p *Pager) AllocPage(typ page.PageType) (page.PageID, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.readOnly {
		return 0, ErrReadOnly
	}
	id := page.PageID(p.pageCount)
	buf := make([]byte, p.pageSize)
	hdr := page.NewPageHeader(typ, p.pageSize, p.meta.TxnID)
	hdr.BodyLength = 0
	page.WritePage(buf, hdr)
	if _, err := p.f.WriteAt(buf, p.offset(id)); err != nil {
		return 0, err
	}
	p.pageCount++
	return id, nil
}

// CommitMeta publishes m as the new live meta by writing it into the stale meta
// slot and fsyncing, the second barrier of the two-fsync commit protocol (doc
// 05 §4). The caller must have already written and fsynced every data page m
// references; this call is the single atomic flip that makes the new version
// current. On a crash before the fsync completes, the stale slot is left invalid
// (bad CRC or torn) and the previous live slot still wins meta selection, so the
// commit is all-or-nothing. On success the pager adopts m, the new slot, and the
// new high-water mark.
func (p *Pager) CommitMeta(m page.Meta) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.readOnly {
		return ErrReadOnly
	}
	slot := page.PageID(2)
	if p.metaSlot == 2 {
		slot = 1
	}
	if err := p.writeMeta(slot, m); err != nil {
		return err
	}
	if err := p.f.Sync(); err != nil {
		return err
	}
	p.meta = m
	p.metaSlot = slot
	p.pageCount = m.PageCount
	return nil
}

// SyncData fsyncs the file, the first barrier of the commit protocol: it makes
// every data page written in this transaction durable before the meta flip.
func (p *Pager) SyncData() error { return p.f.Sync() }

// ReadPage reads page id into a fresh buffer and verifies both checksums. It
// rejects page 0 (the file header is not a common page) and any id past the
// high-water mark. A checksum failure returns page.ErrPageChecksumFail; the
// pager never returns a corrupt page to a caller.
func (p *Pager) ReadPage(id page.PageID) ([]byte, error) {
	if id == 0 {
		return nil, ErrZeroPage
	}
	p.mu.Lock()
	hwm := p.pageCount
	p.mu.Unlock()
	if uint32(id) >= hwm {
		return nil, ErrPageOutOfRange
	}
	buf := make([]byte, p.pageSize)
	if _, err := p.f.ReadAt(buf, p.offset(id)); err != nil {
		return nil, err
	}
	if _, err := page.ReadHeader(buf); err != nil {
		return nil, err
	}
	if !page.VerifyBody(buf) {
		return nil, page.ErrPageChecksumFail
	}
	return buf, nil
}

// WritePage writes a full page buffer at id, computing both checksums from the
// supplied common page header and the body already present in buf. The buffer
// must be exactly one page; its body bytes (buf[32:]) are taken as-is, so any
// unused tail must be zero for a deterministic checksum. WritePage rejects page
// 0 and read-only pagers. It does not extend the high-water mark; allocate with
// AllocPage first.
func (p *Pager) WritePage(id page.PageID, buf []byte, hdr page.PageHeader) error {
	if id == 0 {
		return ErrZeroPage
	}
	if len(buf) != int(p.pageSize) {
		return page.ErrShortBuffer
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.readOnly {
		return ErrReadOnly
	}
	if uint32(id) >= p.pageCount {
		return ErrPageOutOfRange
	}
	page.WritePage(buf, hdr)
	_, err := p.f.WriteAt(buf, p.offset(id))
	return err
}
