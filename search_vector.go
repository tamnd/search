package search

import (
	"encoding/binary"
	"fmt"
	"math"
	"sort"

	"github.com/tamnd/search/catalog"
	"github.com/tamnd/search/docstore"
	"github.com/tamnd/search/exec"
	"github.com/tamnd/search/hnsw"
	"github.com/tamnd/search/quantize"
	"github.com/tamnd/search/query"
	"github.com/tamnd/search/schema"
	"github.com/tamnd/search/segment"
	"github.com/tamnd/search/vector"
)

// Dense-vector flush and search (spec 2063 doc 15, milestone S7). At flush a
// segment's dense_vector fields are turned into one self-describing blob per
// field: an HNSW graph over the batch's vectors plus, when the field configures
// quantization, the trained int8 or product-quantization codes. The blob rides
// the catalog key/value seam under NSSegVectors keyed by (segment id, field),
// the same documented deviation the FST, postings, and doc-values layers make
// instead of the page-extent layout in doc 15 §6.
//
// Traversal deviation (doc 15 §7): the graph is built and searched in float32
// for recall quality. The quantized codes are persisted as the compact vector
// representation and round-tripped, but quantized-graph traversal and ADC
// scoring are a follow-on; the query path scores against the float32 vectors the
// graph already carries. This is the same float32-first choice the recall tests
// validate.

// vecMagic and vecVersion tag a per-field dense-vector blob.
var vecMagic = [4]byte{'S', 'X', 'V', 'S'}

const vecVersion = 1

// quant mode bytes stored in the blob header. They mirror the schema
// quantization strings: none keeps only the float32 graph, the int8 and pq modes
// add a quantized sidecar.
const (
	quantModeNone byte = 0
	quantModeInt8 byte = 1
	quantModePQ   byte = 2
)

// quantMode maps a schema quantization string to its stored mode byte.
func quantMode(q string) byte {
	switch q {
	case schema.QuantInt8, schema.QuantInt8Rerank:
		return quantModeInt8
	case schema.QuantPQ, schema.QuantPQRerank:
		return quantModePQ
	default:
		return quantModeNone
	}
}

// schemaHasVectorValues reports whether any document in the batch carries a value
// for a dense_vector field. A batch that indexes only vectors produces no
// inverted terms, so flushBatch consults this to decide whether a segment must be
// written anyway.
func schemaHasVectorValues(s *schema.Schema, entries []docEntry) bool {
	for _, f := range s.Fields {
		if f.Type != schema.TypeDenseVector {
			continue
		}
		for _, e := range entries {
			if v, ok := e.doc[f.Name]; ok && v != nil {
				return true
			}
		}
	}
	return false
}

// flushVectors builds and stores the dense-vector index for every dense_vector
// field of a freshly flushed segment. Vectors are collected from the raw document
// bodies and carry the global internal doc-id, so a graph result maps straight
// back to the doc store with no per-segment remapping.
func flushVectors(c *catalog.Catalog, s *schema.Schema, segID uint64, entries []docEntry) error {
	for _, f := range s.Fields {
		if f.Type != schema.TypeDenseVector {
			continue
		}
		docIDs, vecs, err := collectVectors(entries, f, f.Opts.Dims)
		if err != nil {
			return err
		}
		if len(vecs) == 0 {
			continue
		}
		blob, err := buildVectorBlob(f, docIDs, vecs)
		if err != nil {
			return err
		}
		if err := writeVecBlob(c, segID, f.Name, blob); err != nil {
			return err
		}
	}
	return nil
}

// vecChunkPayload is the maximum bytes of a dense-vector blob stored under one
// catalog key. A graph plus its float32 vectors easily exceeds a page, and the
// catalog tree stores only page-sized values, so the blob is split into chunks
// keyed by (segment id, field, chunk index). The payload stays well under the
// smallest supported page (4096 bytes) so a chunk always fits a leaf.
const vecChunkPayload = 2000

// segVecChunkKey builds the (segment id, field, chunk index) key for the
// dense-vector namespace.
func segVecChunkKey(id uint64, field string, chunk uint32) []byte {
	out := make([]byte, 8+len(field)+4)
	binary.BigEndian.PutUint64(out[:8], id)
	copy(out[8:8+len(field)], field)
	binary.BigEndian.PutUint32(out[8+len(field):], chunk)
	return out
}

// writeVecBlob stores a dense-vector blob as a chain of chunks. Chunk 0 carries a
// 4-byte chunk count prefix so the reader knows how many to fetch without
// scanning.
func writeVecBlob(c *catalog.Catalog, segID uint64, field string, blob []byte) error {
	n := (len(blob) + vecChunkPayload - 1) / vecChunkPayload
	if n == 0 {
		n = 1
	}
	for i := 0; i < n; i++ {
		lo := i * vecChunkPayload
		hi := min(lo+vecChunkPayload, len(blob))
		var val []byte
		if i == 0 {
			var hdr [4]byte
			binary.BigEndian.PutUint32(hdr[:], uint32(n))
			val = append(val, hdr[:]...)
		}
		val = append(val, blob[lo:hi]...)
		if err := c.Put(catalog.NSSegVectors, segVecChunkKey(segID, field, uint32(i)), val); err != nil {
			return err
		}
	}
	return nil
}

// readVecBlob reassembles a dense-vector blob from its chunks, or false when the
// field has no vectors in the segment.
func readVecBlob(kv segment.KV, segID uint64, field string) ([]byte, bool, error) {
	first, ok, err := kv.Get(catalog.NSSegVectors, segVecChunkKey(segID, field, 0))
	if err != nil || !ok {
		return nil, false, err
	}
	if len(first) < 4 {
		return nil, false, fmt.Errorf("vecstore: short header for segment %d field %q", segID, field)
	}
	n := binary.BigEndian.Uint32(first[:4])
	out := make([]byte, 0, int(n)*vecChunkPayload)
	out = append(out, first[4:]...)
	for i := uint32(1); i < n; i++ {
		b, ok, err := kv.Get(catalog.NSSegVectors, segVecChunkKey(segID, field, i))
		if err != nil {
			return nil, false, err
		}
		if !ok {
			return nil, false, fmt.Errorf("vecstore: missing chunk %d of %d for segment %d field %q", i, n, segID, field)
		}
		out = append(out, b...)
	}
	return out, true, nil
}

// collectVectors gathers the vectors of one dense_vector field from the batch,
// paired with their global doc-ids. A document missing the field is skipped; a
// value of the wrong dimension or one holding NaN/Inf is rejected so a malformed
// vector cannot poison the graph.
func collectVectors(entries []docEntry, f schema.Field, dims int) ([]uint32, [][]float32, error) {
	docIDs := make([]uint32, 0, len(entries))
	vecs := make([][]float32, 0, len(entries))
	for _, e := range entries {
		raw, ok := e.doc[f.Name]
		if !ok || raw == nil {
			continue
		}
		v, err := toVector(raw, dims)
		if err != nil {
			return nil, nil, fmt.Errorf("field %q: %w", f.Name, err)
		}
		if i, _, bad := vector.HasNaNOrInf(v); bad {
			return nil, nil, fmt.Errorf("field %q: vector element %d is NaN or Inf", f.Name, i)
		}
		docIDs = append(docIDs, uint32(e.docID))
		vecs = append(vecs, v)
	}
	return docIDs, vecs, nil
}

// toVector converts a stored field value to a float32 vector of the field's
// dimension. It accepts a []float32, a []float64, or a []any of numbers (the form
// a JSON array decodes to).
func toVector(v any, dims int) ([]float32, error) {
	var out []float32
	switch t := v.(type) {
	case []float32:
		out = make([]float32, len(t))
		copy(out, t)
	case []float64:
		out = make([]float32, len(t))
		for i, e := range t {
			out[i] = float32(e)
		}
	case []any:
		out = make([]float32, len(t))
		for i, e := range t {
			fv, err := schema.ToFloat64(e)
			if err != nil {
				return nil, fmt.Errorf("vector element %d: %w", i, err)
			}
			out[i] = float32(fv)
		}
	default:
		return nil, fmt.Errorf("vector value has unsupported type %T", v)
	}
	if len(out) != dims {
		return nil, fmt.Errorf("vector has %d dims, want %d", len(out), dims)
	}
	return out, nil
}

// buildVectorBlob builds the HNSW graph (and, when configured, the quantized
// sidecar) for a field's vectors and serializes them into one self-describing
// blob.
func buildVectorBlob(f schema.Field, docIDs []uint32, vecs [][]float32) ([]byte, error) {
	metric := hnsw.ParseMetric(f.Opts.Metric)
	params := hnsw.DefaultParams(f.Opts.M, f.Opts.EfConstruction)
	g := hnsw.New(params, metric, f.Opts.Dims)
	for i, v := range vecs {
		g.Add(docIDs[i], v)
	}
	graphBlob := g.Marshal()

	mode := quantMode(f.Opts.Quantization)
	var quantBlob []byte
	var err error
	switch mode {
	case quantModeInt8:
		quantBlob = buildInt8Sidecar(vecs)
	case quantModePQ:
		quantBlob, err = buildPQSidecar(f, vecs)
		if err != nil {
			// A corpus too small to train a codebook degrades to no quantization
			// rather than failing the flush; the float32 graph still serves queries.
			mode = quantModeNone
			quantBlob = nil
		}
	}

	var out []byte
	out = append(out, vecMagic[:]...)
	out = append(out, vecVersion, mode)
	out = binary.AppendUvarint(out, uint64(len(graphBlob)))
	out = append(out, graphBlob...)
	out = binary.AppendUvarint(out, uint64(len(quantBlob)))
	out = append(out, quantBlob...)
	return out, nil
}

// buildInt8Sidecar trains a global int8 quantizer over the vectors and returns
// its 8-byte frame followed by the row-major codes in ordinal order.
func buildInt8Sidecar(vecs [][]float32) []byte {
	q := quantize.TrainInt8(vecs)
	dims := len(vecs[0])
	codes := q.EncodeAll(vecs, dims)
	out := q.Marshal()
	for _, c := range codes {
		out = append(out, byte(c))
	}
	return out
}

// buildPQSidecar trains a product-quantization codebook over the vectors and
// returns the codebook frame (length-prefixed) followed by the per-vector codes.
func buildPQSidecar(f schema.Field, vecs [][]float32) ([]byte, error) {
	m := pqSubspaces(f.Opts.Dims)
	cb, err := quantize.TrainCodebook(vecs, quantize.PQConfig{
		M:      m,
		Dims:   f.Opts.Dims,
		Sample: len(vecs),
		Iters:  20,
		Seed:   1,
	})
	if err != nil {
		return nil, err
	}
	cbBlob := cb.Marshal()
	out := binary.AppendUvarint(nil, uint64(len(cbBlob)))
	out = append(out, cbBlob...)
	out = append(out, cb.EncodeAll(vecs)...)
	return out, nil
}

// pqSubspaces picks a subspace count that divides dims, preferring 8 and falling
// back to the largest power-of-two divisor at most 8.
func pqSubspaces(dims int) int {
	for _, m := range []int{8, 4, 2, 1} {
		if dims%m == 0 {
			return m
		}
	}
	return 1
}

// vecIndex is a loaded per-segment dense-vector index for one field.
type vecIndex struct {
	graph *hnsw.Graph
	mode  byte
	quant []byte // raw sidecar bytes, decoded lazily by the rerank path
}

// openVecIndex loads the dense-vector blob for a field in a segment, or false
// when the segment has no vectors for the field.
func openVecIndex(kv segment.KV, segID uint64, field string) (*vecIndex, bool, error) {
	b, ok, err := readVecBlob(kv, segID, field)
	if err != nil || !ok {
		return nil, false, err
	}
	if len(b) < 6 || [4]byte{b[0], b[1], b[2], b[3]} != vecMagic {
		return nil, false, fmt.Errorf("vecstore: bad magic for segment %d field %q", segID, field)
	}
	if b[4] != vecVersion {
		return nil, false, fmt.Errorf("vecstore: unsupported version %d", b[4])
	}
	mode := b[5]
	p := 6
	graphLen, n := binary.Uvarint(b[p:])
	if n <= 0 {
		return nil, false, fmt.Errorf("vecstore: bad graph length")
	}
	p += n
	if p+int(graphLen) > len(b) {
		return nil, false, fmt.Errorf("vecstore: truncated graph")
	}
	g, err := hnsw.Load(b[p : p+int(graphLen)])
	if err != nil {
		return nil, false, err
	}
	p += int(graphLen)
	quantLen, n := binary.Uvarint(b[p:])
	if n <= 0 {
		return nil, false, fmt.Errorf("vecstore: bad quant length")
	}
	p += n
	if p+int(quantLen) > len(b) {
		return nil, false, fmt.Errorf("vecstore: truncated quant sidecar")
	}
	return &vecIndex{graph: g, mode: mode, quant: b[p : p+int(quantLen)]}, true, nil
}

// vecResult is one scored vector neighbor with its global doc-id.
type vecResult struct {
	docID uint32
	score float32
}

// knnSearch runs k-nearest-neighbor search for a field across every live segment
// and merges the per-segment neighbors into one global top-k by score. allow, when
// non-nil, restricts results to doc-ids it accepts (the filtered-ANN pre-filter).
func knnSearch(kv segment.KV, set *segment.SegmentSet, f schema.Field, q []float32, k, ef int, allow func(uint32) bool) ([]vecResult, error) {
	if ef < k {
		ef = k
	}
	var all []vecResult
	for _, seg := range set.Segments() {
		vi, ok, err := openVecIndex(kv, seg.ID(), f.Name)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		var rs []hnsw.Result
		if f.Opts.Index {
			rs = vi.graph.Search(q, k, ef, allow)
		} else {
			rs = vi.graph.ExactSearch(q, k, allow)
		}
		for _, r := range rs {
			all = append(all, vecResult{docID: r.DocID, score: r.Score})
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].score > all[j].score })
	if len(all) > k {
		all = all[:k]
	}
	return all, nil
}

// rrfFuse combines two ranked doc-id lists with Reciprocal Rank Fusion (doc 15
// §10.3): each list contributes 1/(rrfK + rank) to a doc's score, with rank
// 1-based in list order. The fused list is returned sorted by descending score,
// trimmed to k.
func rrfFuse(lists [][]uint32, rrfK, k int) []vecResult {
	if rrfK <= 0 {
		rrfK = 60
	}
	score := make(map[uint32]float64)
	for _, list := range lists {
		for rank, id := range list {
			score[id] += 1.0 / float64(rrfK+rank+1)
		}
	}
	out := make([]vecResult, 0, len(score))
	for id, s := range score {
		out = append(out, vecResult{docID: id, score: float32(s)})
	}
	// Sort by score, breaking ties by doc-id so the order is deterministic.
	sort.Slice(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		return out[i].docID < out[j].docID
	})
	if len(out) > k {
		out = out[:k]
	}
	return out
}

// finiteScore clamps a score to a finite value so a degenerate distance never
// produces NaN in a hit.
func finiteScore(s float32) float32 {
	if math.IsNaN(float64(s)) || math.IsInf(float64(s), 0) {
		return 0
	}
	return s
}

// qSchema adapts the index schema to the query.Schema interface for validating
// dense-vector queries in this package.
type qSchema struct{ s *schema.Schema }

func (v qSchema) FieldType(name string) (string, bool) {
	f, ok := v.s.Lookup(name)
	if !ok {
		return "", false
	}
	return string(f.Type), true
}

// knnRanked runs a KNNQuery across the segment set and returns up to k neighbors
// by score, excluding deleted documents and applying the optional pre-filter. It
// is shared by the standalone kNN path and the kNN side of a hybrid query.
func (db *DB) knnRanked(c *catalog.Catalog, s *schema.Schema, set *segment.SegmentSet, se *exec.Searcher, dead []uint32, q *query.KNNQuery, k int) ([]vecResult, error) {
	if err := q.Validate(qSchema{s}); err != nil {
		return nil, err
	}
	f, ok := s.Lookup(q.Field)
	if !ok {
		return nil, &query.Error{Msg: "knn query references unknown field " + q.Field}
	}
	if len(q.Vector) != f.Opts.Dims {
		return nil, fmt.Errorf("search: knn query vector has %d dims, field %q expects %d", len(q.Vector), q.Field, f.Opts.Dims)
	}
	if hnsw.ParseMetric(f.Opts.Metric) == hnsw.Cosine && vector.Norm(q.Vector) == 0 {
		return nil, fmt.Errorf("search: knn query vector is zero under cosine metric")
	}
	ef := q.NumCandidates
	if ef <= 0 {
		ef = f.Opts.NumCandidates
	}

	deadSet := make(map[uint32]struct{}, len(dead))
	for _, d := range dead {
		deadSet[d] = struct{}{}
	}
	var userAllow map[uint32]struct{}
	if q.Filter != nil {
		var err error
		userAllow, err = db.matchingSet(se, q.Filter)
		if err != nil {
			return nil, err
		}
	}
	allow := func(d uint32) bool {
		if _, dead := deadSet[d]; dead {
			return false
		}
		if userAllow != nil {
			_, ok := userAllow[d]
			return ok
		}
		return true
	}

	res, err := knnSearch(c, set, f, q.Vector, k, ef, allow)
	if err != nil {
		return nil, err
	}
	for i := range res {
		res[i].score = finiteScore(res[i].score)
	}
	return res, nil
}

// matchingSet runs a filter query and returns the set of internal doc-ids it
// matches. The filter is collected as a full match set, not a top-k, so it can
// gate the dense-vector traversal.
func (db *DB) matchingSet(se *exec.Searcher, filter query.Query) (map[uint32]struct{}, error) {
	hits, err := se.Search(filter, int(^uint(0)>>1))
	if err != nil {
		return nil, err
	}
	out := make(map[uint32]struct{}, len(hits))
	for _, h := range hits {
		out[uint32(h.DocID)] = struct{}{}
	}
	return out, nil
}

// searchKNN executes a standalone kNN query and resolves the neighbors to hits.
func (db *DB) searchKNN(c *catalog.Catalog, s *schema.Schema, set *segment.SegmentSet, se *exec.Searcher, dead []uint32, q *query.KNNQuery, k int) ([]Hit, error) {
	res, err := db.knnRanked(c, s, set, se, dead, q, k)
	if err != nil {
		return nil, err
	}
	return resolveVecHits(c, s, res)
}

// searchHybrid executes a hybrid query: it ranks the text side and the kNN side
// independently, fuses them with RRF, and resolves the fused list to hits.
func (db *DB) searchHybrid(c *catalog.Catalog, s *schema.Schema, set *segment.SegmentSet, se *exec.Searcher, dead []uint32, q *query.HybridQuery, k int) ([]Hit, error) {
	if err := q.Validate(qSchema{s}); err != nil {
		return nil, err
	}
	window := k
	if q.K > 0 {
		window = q.K
	}
	textHits, err := se.Search(q.Text, window)
	if err != nil {
		return nil, err
	}
	textList := make([]uint32, len(textHits))
	for i, h := range textHits {
		textList[i] = uint32(h.DocID)
	}

	knnRes, err := db.knnRanked(c, s, set, se, dead, q.KNN, window)
	if err != nil {
		return nil, err
	}
	knnList := make([]uint32, len(knnRes))
	for i, r := range knnRes {
		knnList[i] = r.docID
	}

	fused := rrfFuse([][]uint32{textList, knnList}, q.RRFK, window)
	return resolveVecHits(c, s, fused)
}

// resolveVecHits resolves scored vector results to hits with stored bodies,
// dropping any whose body is missing (a concurrently deleted document).
func resolveVecHits(c *catalog.Catalog, s *schema.Schema, res []vecResult) ([]Hit, error) {
	store := docstore.New(c, catalog.NSDocStore)
	pk := s.PrimaryKey()
	hits := make([]Hit, 0, len(res))
	for _, r := range res {
		doc, ok, err := store.Get(uint64(r.docID))
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		hits = append(hits, Hit{
			DocID:      uint64(r.docID),
			ExternalID: externalID(doc, pk),
			Score:      r.score,
			Document:   doc,
		})
	}
	return hits, nil
}
