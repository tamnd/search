package search

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"

	"github.com/tamnd/search/catalog"
	"github.com/tamnd/search/docstore"
	"github.com/tamnd/search/segment"
)

// FileInfo is a geometry and bookkeeping summary of a .sx file, gathered from
// the header and the live meta page. It is what `sx info` reports and what an
// embedder reads to monitor an index without scanning its contents.
type FileInfo struct {
	Path             string
	PageSize         uint32
	PageCount        uint32
	FileBytes        int64
	FormatVersion    uint32
	EngineVersionMin uint16 // FormatVersionCompatMin: oldest engine that may open this file
	Creator          string
	CreatedEpoch     uint64
	TxnID            uint64 // live meta generation
	CatalogRoot      uint32
	SegmentCount     uint32
	DocCount         uint64
	DeletedDocCount  uint64
	LastDocID        uint64
	SchemaVersion    uint32
}

// Info returns a header and meta summary of the open index. The geometry and
// version fields come from the header; the segment and document counts come from
// the live segment manifest, since those are the authoritative counts an indexer
// maintains. It reads segment metadata only, not segment bodies, so it stays
// cheap on an index of any size.
func (db *DB) Info() (FileInfo, error) {
	h := db.pgr.Header()
	m := db.pgr.Meta()
	fi := FileInfo{
		Path:             db.path,
		PageSize:         db.pgr.PageSize(),
		PageCount:        db.pgr.PageCount(),
		FormatVersion:    h.FormatVersion,
		EngineVersionMin: h.FormatVersionCompatMin,
		Creator:          cString(h.CreatorString[:]),
		CreatedEpoch:     h.FileCreateEpoch,
		TxnID:            m.TxnID,
		CatalogRoot:      m.CatalogRoot,
		LastDocID:        m.LastDocID,
		SchemaVersion:    m.SchemaVersion,
	}
	fi.FileBytes = int64(fi.PageSize) * int64(fi.PageCount)

	err := db.View(func(t *Txn) error {
		c := t.Catalog()
		set, serr := segment.LoadSet(c)
		if serr != nil {
			return serr
		}
		fi.SegmentCount = uint32(set.Len())
		for _, s := range set.Segments() {
			sm := s.Meta()
			fi.DocCount += uint64(sm.DocCount)
		}
		deleted, derr := set.DeletedDocIDs(c)
		if derr != nil {
			return derr
		}
		fi.DeletedDocCount = uint64(len(deleted))
		return nil
	})
	if err != nil {
		return FileInfo{}, err
	}
	return fi, nil
}

// IndexStats is a structural and runtime summary of an open index: the geometry
// and counts from Info, the per-segment breakdown from Segments, plus the
// freelist, snapshot, and term totals that an operator watches to judge whether
// the index needs compaction or vacuum. It is what `sx stats` reports.
type IndexStats struct {
	FileInfo
	// FreePages is the durable freelist count from the live meta page: pages
	// that are free on disk and reusable by the next write without growing the
	// file. It does not include scratch pages freed within an open transaction.
	FreePages uint64
	// PendingFreePages is the number of pages freed by a committed write but
	// still pinned by a live read snapshot, so not yet returned to the freelist.
	// A persistently high value means a long-lived reader is holding back
	// reclamation.
	PendingFreePages int
	// ActiveReaders is the number of open read snapshots.
	ActiveReaders int
	// OldestReaderTxn is the transaction id of the oldest pinned snapshot, or 0
	// when no reader is open. Pages freed at or after this txn cannot be reused.
	OldestReaderTxn uint64
	// TotalTerms is the sum of per-field term counts across every live segment.
	// Terms shared between segments are counted once per segment, so this is an
	// upper bound on the distinct vocabulary, not the union.
	TotalTerms uint64
	// Segments is the per-segment summary, ordered by id.
	Segments []SegmentInfo
}

// Stats gathers a structural and runtime summary of the open index. It reads the
// header, the live meta page, and every segment's metadata (not its bodies), so
// it stays cheap on an index of any size, and it samples the live reader and
// freelist bookkeeping under the same lock the pager uses, so the snapshot and
// reclamation figures are a consistent instant.
func (db *DB) Stats() (IndexStats, error) {
	fi, err := db.Info()
	if err != nil {
		return IndexStats{}, err
	}
	segs, err := db.Segments()
	if err != nil {
		return IndexStats{}, err
	}
	st := IndexStats{FileInfo: fi, Segments: segs}
	st.FreePages = db.pgr.Meta().FreelistCount
	for _, s := range segs {
		for _, f := range s.Fields {
			st.TotalTerms += f.TermCount
		}
	}

	db.rmu.Lock()
	st.PendingFreePages = len(db.pendingFree)
	st.ActiveReaders = len(db.readers)
	for txn := range db.readers {
		if st.OldestReaderTxn == 0 || txn < st.OldestReaderTxn {
			st.OldestReaderTxn = txn
		}
	}
	db.rmu.Unlock()
	return st, nil
}

// cString trims a NUL-padded fixed byte array to its string content.
func cString(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

// VerifyReport is the outcome of an integrity check. Errors holds a
// human-readable line per problem found; an empty Errors with a nil error from
// Verify means the file passed every check that was run.
type VerifyReport struct {
	Deep         bool
	PageCount    uint32
	CatalogKeys  int
	Segments     int
	Fields       int
	Terms        int
	PostingsRead int
	LiveDocs     int
	Errors       []string
}

// OK reports whether the verification found no problems.
func (r VerifyReport) OK() bool { return len(r.Errors) == 0 }

// Verify walks the live structure of the index and reports any corruption it
// finds. It always validates the catalog B+tree and every value it holds (each
// page is read through the pager, which checks both checksums), then loads the
// segment manifest and every segment's term dictionary, which forces a read of
// each FST block. With deep set it additionally reads every term's postings
// list, turning the check into a full scan of the inverted index.
//
// Verify gathers problems rather than stopping at the first: it returns a report
// whose Errors lists every fault, and a non-nil error only when the index cannot
// be opened for reading at all. Free pages, which legitimately hold stale bytes,
// are not brute-force scanned; the walk only touches pages reachable from the
// live meta, which is what integrity means for a copy-on-write file.
func (db *DB) Verify(deep bool) (VerifyReport, error) {
	rep := VerifyReport{Deep: deep, PageCount: db.pgr.PageCount()}
	err := db.View(func(t *Txn) error {
		c := t.Catalog()

		// Walk every catalog namespace. Scan reads each B+tree node and resolves
		// each value (including overflow chains) through the checksum-verifying
		// pager, so a corrupt tree page or value surfaces here.
		for _, ns := range catalogNamespaces {
			if serr := c.Scan(ns, func(key, val []byte) bool {
				rep.CatalogKeys++
				return true
			}); serr != nil {
				rep.Errors = append(rep.Errors, fmt.Sprintf("catalog namespace %#x: %v", ns, serr))
			}
		}

		// Count live documents from the external-id map, which holds exactly the
		// documents a delete has not removed.
		if serr := c.Scan(catalog.NSExternalID, func(key, val []byte) bool {
			rep.LiveDocs++
			return true
		}); serr != nil {
			rep.Errors = append(rep.Errors, fmt.Sprintf("external-id map: %v", serr))
		}

		set, serr := segment.LoadSet(c)
		if serr != nil {
			rep.Errors = append(rep.Errors, fmt.Sprintf("segment manifest: %v", serr))
			return nil
		}
		for _, s := range set.Segments() {
			rep.Segments++
			verifySegment(c, s, deep, &rep)
		}
		return nil
	})
	return rep, err
}

// verifySegment reads every field's term dictionary in a segment and, when deep,
// every term's postings list, appending any read fault to the report.
func verifySegment(c *catalog.Catalog, s *segment.Segment, deep bool, rep *VerifyReport) {
	m := s.Meta()
	for _, fm := range m.Fields {
		fr, ok, err := s.Field(c, fm.Name)
		if err != nil {
			rep.Errors = append(rep.Errors, fmt.Sprintf("segment %d field %q: %v", m.ID, fm.Name, err))
			continue
		}
		if !ok {
			continue
		}
		rep.Fields++
		terms, err := fr.Terms()
		if err != nil {
			rep.Errors = append(rep.Errors, fmt.Sprintf("segment %d field %q terms: %v", m.ID, fm.Name, err))
			continue
		}
		rep.Terms += len(terms)
		if !deep {
			continue
		}
		for _, term := range terms {
			r, ok, err := fr.Postings(term)
			if err != nil {
				rep.Errors = append(rep.Errors, fmt.Sprintf("segment %d field %q term %q postings: %v", m.ID, fm.Name, term, err))
				continue
			}
			if !ok {
				continue
			}
			for {
				_, _, ok, err := r.Next()
				if err != nil {
					rep.Errors = append(rep.Errors, fmt.Sprintf("segment %d field %q term %q scan: %v", m.ID, fm.Name, term, err))
					break
				}
				if !ok {
					break
				}
				rep.PostingsRead++
			}
		}
	}
}

// catalogNamespaces is the set of catalog namespaces Verify walks. It is the
// stored-data side of the catalog; the doc-id counters in NSDocID are tiny and
// covered incidentally, and NSExternalID is scanned separately for the live-doc
// count.
var catalogNamespaces = []byte{
	catalog.NSMeta,
	catalog.NSFieldMeta,
	catalog.NSSegmentManifest,
	catalog.NSDeletionState,
	catalog.NSStats,
	catalog.NSSchema,
	catalog.NSDocStore,
}

// Export writes every live document to w as JSON Lines, one compact object per
// line, and returns the number of documents written. Documents are streamed as
// they are read, so memory stays flat regardless of index size. The external id
// of each document is restored under the schema's primary-key field so the
// output round-trips back through Index.
func (db *DB) Export(w io.Writer) (int, error) {
	bw := bufio.NewWriter(w)
	enc := json.NewEncoder(bw)
	idField := "_id"
	if s, err := db.Schema(); err == nil && s.PrimaryKey() != "" {
		idField = s.PrimaryKey()
	}

	n := 0
	err := db.View(func(t *Txn) error {
		c := t.Catalog()
		store := docstore.New(c, catalog.NSDocStore)
		var scanErr error
		serr := c.Scan(catalog.NSExternalID, func(key, val []byte) bool {
			extID := string(key)
			docID := beUint64(val)
			doc, ok, err := store.Get(docID)
			if err != nil {
				scanErr = fmt.Errorf("doc %q: %w", extID, err)
				return false
			}
			if !ok {
				return true
			}
			if _, has := doc[idField]; !has {
				doc[idField] = extID
			}
			if err := enc.Encode(doc); err != nil {
				scanErr = fmt.Errorf("encode %q: %w", extID, err)
				return false
			}
			n++
			return true
		})
		if scanErr != nil {
			return scanErr
		}
		return serr
	})
	if err != nil {
		return n, err
	}
	return n, bw.Flush()
}

// beUint64 decodes a big-endian uint64, matching how doc-ids are stored in the
// external-id map. A short slice decodes as zero, which Get then misses.
func beUint64(b []byte) uint64 {
	if len(b) < 8 {
		return 0
	}
	return uint64(b[0])<<56 | uint64(b[1])<<48 | uint64(b[2])<<40 | uint64(b[3])<<32 |
		uint64(b[4])<<24 | uint64(b[5])<<16 | uint64(b[6])<<8 | uint64(b[7])
}
