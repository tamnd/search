package search

import (
	"encoding/json"
	"fmt"

	"github.com/tamnd/search/analysis"
	"github.com/tamnd/search/catalog"
	"github.com/tamnd/search/docstore"
	"github.com/tamnd/search/schema"
)

// RepairReport summarizes a best-effort repair: how many documents were carried
// into the rebuilt file, how many had to be dropped because they could not be
// read, and a human-readable line per problem encountered.
type RepairReport struct {
	// Recovered is the number of live documents written to the output file.
	Recovered int
	// Dropped is the number of documents that were present in the source's
	// external-id map but could not be read back and so were left out.
	Dropped int
	// Errors holds one line per problem; an empty slice means a clean rebuild.
	Errors []string
}

// OK reports whether the repair recovered everything with no dropped documents.
func (r RepairReport) OK() bool { return r.Dropped == 0 && len(r.Errors) == 0 }

// Repair performs a best-effort recovery of a possibly-damaged index by writing
// a freshly rebuilt file to outPath. It never modifies the source: the caller
// keeps the original to fall back on. Recovery proceeds logically rather than at
// the page level. The source is opened read-only, which already exercises the
// engine's meta-page recovery (the copy-on-write design keeps two meta pages and
// the higher valid txn wins, so a single torn meta page is survived
// transparently). Its schema and any stored custom analyzers are copied to the
// output, then every live document is scanned and reindexed one at a time; a
// document whose stored body cannot be read is recorded and skipped instead of
// aborting the whole rebuild. The result is a clean, self-contained file holding
// every document that was still readable.
//
// Divergence from the page-level salvage sketched in operations doc 21 §3.16:
// because durability here is meta-page double-buffering rather than a
// section-addressable WAL, repair rebuilds logically from the readable document
// set instead of stitching together intact segment sections. The recovered file
// is therefore a fresh, fully-checksummed index, not a byte-subset of the
// original, which is the behavior the spec calls out as acceptable ("repaired
// files are not bit-for-bit identical to the original").
func Repair(srcPath, outPath string, opt Options) (RepairReport, error) {
	srcOpt := opt
	srcOpt.ReadOnly = true
	src, err := Open(srcPath, srcOpt)
	if err != nil {
		return RepairReport{}, fmt.Errorf("open source: %w", err)
	}
	defer func() { _ = src.Close() }()

	s, err := src.Schema()
	if err != nil {
		return RepairReport{}, fmt.Errorf("read schema: %w", err)
	}

	outOpt := opt
	outOpt.ReadOnly = false
	outOpt.PageSize = src.pgr.PageSize()
	out, err := Open(outPath, outOpt)
	if err != nil {
		return RepairReport{}, fmt.Errorf("create output: %w", err)
	}

	rep, err := repairInto(src, out, s)
	closeErr := out.Close()
	if err != nil {
		return rep, err
	}
	if closeErr != nil {
		return rep, fmt.Errorf("close output: %w", closeErr)
	}
	return rep, nil
}

// repairInto copies the schema and custom analyzers from src to out and then
// reindexes every readable live document, gathering per-document problems into
// the report rather than failing the whole run.
func repairInto(src, out *DB, s *schema.Schema) (RepairReport, error) {
	var rep RepairReport

	if err := out.PutSchema(s); err != nil {
		return rep, fmt.Errorf("write schema: %w", err)
	}
	if err := copyAnalyzers(src, out, &rep); err != nil {
		return rep, err
	}

	idField := s.PrimaryKey()
	if idField == "" {
		idField = "_id"
	}

	// Scan the source's external-id map, the authoritative list of live
	// documents, and reindex each one. A document whose body cannot be read is
	// counted as dropped and skipped; the scan itself continues so a single bad
	// page does not cost the whole corpus.
	err := src.View(func(t *Txn) error {
		c := t.Catalog()
		store := docstore.New(c, catalog.NSDocStore)
		// The scan never aborts on a per-document fault: each bad document is
		// recorded and skipped so one corrupt page does not strand the rest.
		return c.Scan(catalog.NSExternalID, func(key, val []byte) bool {
			extID := string(key)
			docID := beUint64(val)
			doc, ok, err := store.Get(docID)
			if err != nil {
				rep.Dropped++
				rep.Errors = append(rep.Errors, fmt.Sprintf("doc %q: %v", extID, err))
				return true
			}
			if !ok {
				rep.Dropped++
				rep.Errors = append(rep.Errors, fmt.Sprintf("doc %q: stored body missing", extID))
				return true
			}
			if _, has := doc[idField]; !has {
				doc[idField] = extID
			}
			if _, err := out.Index([]map[string]any{doc}); err != nil {
				rep.Dropped++
				rep.Errors = append(rep.Errors, fmt.Sprintf("doc %q: reindex: %v", extID, err))
				return true
			}
			rep.Recovered++
			return true
		})
	})
	if err != nil {
		rep.Errors = append(rep.Errors, fmt.Sprintf("scan: %v", err))
	}
	return rep, nil
}

// copyAnalyzers carries every stored custom analyzer from src to out so that the
// rebuilt index analyzes text exactly as the original did. Built-in analyzers
// referenced by name need no copying.
func copyAnalyzers(src, out *DB, rep *RepairReport) error {
	return src.View(func(t *Txn) error {
		c := t.Catalog()
		var put []analysis.AnalyzerConfig
		serr := c.Scan(catalog.NSAnalyzer, func(key, val []byte) bool {
			var cfg analysis.AnalyzerConfig
			if err := json.Unmarshal(val, &cfg); err != nil {
				rep.Errors = append(rep.Errors, fmt.Sprintf("analyzer %q: %v", key, err))
				return true
			}
			put = append(put, cfg)
			return true
		})
		for _, cfg := range put {
			if err := out.PutAnalyzer(cfg); err != nil {
				rep.Errors = append(rep.Errors, fmt.Sprintf("analyzer %q: %v", cfg.Name, err))
			}
		}
		return serr
	})
}
