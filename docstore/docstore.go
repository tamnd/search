// Package docstore is the stored-field document store (spec 2063 doc 06 §6). It
// answers the retrieval question: given a doc-id, return the field values for
// that document. Each document is encoded as a MessagePack map (field name to
// raw value) and grouped into blocks of up to 512 documents; a block is stored
// in the catalog under the NSDocStore namespace, keyed by block number.
//
// A block is compressed with Zstd (level 3) only once its raw content reaches at
// least CompressThreshold bytes (doc 06 §6.5, roadmap §5.7): tiny payloads
// compress poorly, so small blocks are stored raw and a one-byte codec tag
// records which form was used.
//
// At S2 the store writes one document per block (no batching), which keeps the
// write path trivial; batching up to a full block before flushing arrives with
// the S3 flush path. The block codec already handles many documents per block,
// so the batched path will reuse it unchanged.
package docstore

import (
	"encoding/binary"
	"fmt"

	"github.com/tamnd/search/msgpack"
)

// BlockSize is the maximum number of documents in one block.
const BlockSize = 512

// CompressThreshold is the minimum raw block size, in bytes, at which a block is
// Zstd-compressed. Smaller blocks are stored uncompressed.
const CompressThreshold = 16 * 1024

// Block codec tags, stored as the first byte of a persisted block.
const (
	codecRaw  byte = 0x00
	codecZstd byte = 0x01
)

// KV is the catalog surface the store needs: namespaced get and put. The
// catalog.Catalog type satisfies it.
type KV interface {
	Get(ns byte, key []byte) ([]byte, bool, error)
	Put(ns byte, key, val []byte) error
}

// Store reads and writes stored-field documents over a catalog KV.
type Store struct {
	kv KV
	ns byte
}

// New returns a store backed by kv, using the given catalog namespace (normally
// catalog.NSDocStore).
func New(kv KV, ns byte) *Store {
	return &Store{kv: kv, ns: ns}
}

// Put stores doc under docID. At S2 each document occupies its own block.
func (s *Store) Put(docID uint64, doc map[string]any) error {
	enc, err := msgpack.Marshal(doc)
	if err != nil {
		return fmt.Errorf("docstore: encode doc %d: %w", docID, err)
	}
	blob, err := EncodeBlock([][]byte{enc})
	if err != nil {
		return err
	}
	return s.kv.Put(s.ns, blockKey(docID), blob)
}

// Get returns the document stored under docID, or nil and false if absent.
func (s *Store) Get(docID uint64) (map[string]any, bool, error) {
	blob, ok, err := s.kv.Get(s.ns, blockKey(docID))
	if err != nil || !ok {
		return nil, false, err
	}
	docs, err := DecodeBlock(blob)
	if err != nil {
		return nil, false, err
	}
	if len(docs) == 0 {
		return nil, false, fmt.Errorf("docstore: empty block for doc %d", docID)
	}
	v, _, err := msgpack.Unmarshal(docs[0])
	if err != nil {
		return nil, false, fmt.Errorf("docstore: decode doc %d: %w", docID, err)
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, false, fmt.Errorf("docstore: doc %d is %T, want map", docID, v)
	}
	return m, true, nil
}

// blockKey is the big-endian uint64 catalog key for a block number. At S2 the
// block number is the doc-id (one document per block).
func blockKey(n uint64) []byte {
	var k [8]byte
	binary.BigEndian.PutUint64(k[:], n)
	return k[:]
}

// EncodeBlock frames the given pre-encoded documents into one block: a varint
// document count, then each document length-prefixed with a varint, optionally
// Zstd-compressed when the framed payload reaches CompressThreshold. The first
// byte of the result is the codec tag.
func EncodeBlock(docs [][]byte) ([]byte, error) {
	var raw []byte
	raw = binary.AppendUvarint(raw, uint64(len(docs)))
	for _, d := range docs {
		raw = binary.AppendUvarint(raw, uint64(len(d)))
		raw = append(raw, d...)
	}
	if len(raw) < CompressThreshold {
		return append([]byte{codecRaw}, raw...), nil
	}
	enc, err := getEncoder()
	if err != nil {
		return nil, err
	}
	compressed := enc.EncodeAll(raw, nil)
	return append([]byte{codecZstd}, compressed...), nil
}

// DecodeBlock reverses EncodeBlock, returning the framed documents.
func DecodeBlock(blob []byte) ([][]byte, error) {
	if len(blob) == 0 {
		return nil, fmt.Errorf("docstore: empty block")
	}
	codec, body := blob[0], blob[1:]
	var raw []byte
	switch codec {
	case codecRaw:
		raw = body
	case codecZstd:
		dec, err := getDecoder()
		if err != nil {
			return nil, err
		}
		raw, err = dec.DecodeAll(body, nil)
		if err != nil {
			return nil, fmt.Errorf("docstore: zstd decode: %w", err)
		}
	default:
		return nil, fmt.Errorf("docstore: unknown block codec 0x%02x", codec)
	}

	n, off := binary.Uvarint(raw)
	if off <= 0 {
		return nil, fmt.Errorf("docstore: bad block document count")
	}
	docs := make([][]byte, 0, n)
	for range n {
		l, m := binary.Uvarint(raw[off:])
		if m <= 0 {
			return nil, fmt.Errorf("docstore: bad document length prefix")
		}
		off += m
		if off+int(l) > len(raw) {
			return nil, fmt.Errorf("docstore: truncated block document")
		}
		docs = append(docs, raw[off:off+int(l)])
		off += int(l)
	}
	return docs, nil
}
