// Package fst is the term dictionary: a finite-state transducer mapping each
// indexed term to a uint64 output (spec 2063 doc 08). The output is a byte
// offset into the term's postings, so a single lookup both confirms a term
// exists and locates its postings list.
//
// The builder follows the incremental minimal-acyclic-FST construction
// (Daciuk et al. 2000): terms are added in strictly ascending order, and as the
// common prefix with the next term shrinks the diverged suffix states are
// frozen and registered. Equivalent suffixes (same outgoing arcs, same output,
// same finality) share one compiled representation, so the dictionary stays
// compact. Outputs use the sum monoid: the output of a term is the sum of the
// arc outputs along its path plus the final output of its accepting state, and
// common output is pushed toward the root so shared arcs can carry it.
//
// Compilation is done on freeze: because states are frozen deepest-first, a
// state's targets are already compiled when it is, so the byte buffer holds
// children before parents and the root is compiled last. A node records its
// arcs' absolute target offsets, which are therefore always smaller than the
// node's own offset.
//
// The reader walks the compiled buffer arc by arc. Enumeration (full, prefix,
// range) is materialized: a depth-first walk in label order yields terms in
// lexicographic order. Streaming iterators and the dense-node and Levenshtein
// optimizations from the spec are deferred; the materialized form is correct
// and adequate for the corpus sizes the engine flushes per segment.
package fst

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
)

// MaxTermLen is the maximum length, in bytes, of a term the builder accepts. It
// matches Lucene's limit (doc 08 §17.2).
const MaxTermLen = 32766

// node flag bits in a compiled node header.
const (
	flagFinal       = 1 << 0
	flagHasFinalOut = 1 << 1
)

// ErrUnsortedInput is returned by Builder.Add when terms are not supplied in
// strictly ascending byte order.
var ErrUnsortedInput = errors.New("fst: terms must be added in strictly ascending order")

// builderArc is an outgoing edge from a builder state.
type builderArc struct {
	label  byte
	output uint64
	target *builderState // pre-compile target; nil once compiled
	// targetOff is the compiled offset of target, valid after the target is frozen.
	targetOff uint64
}

// builderState is a mutable FST state held on the active suffix path.
type builderState struct {
	arcs        []builderArc
	isFinal     bool
	finalOutput uint64
}

// Builder constructs an FST from terms added in ascending order.
type Builder struct {
	// frontier[i] is the state reached after consuming the first i bytes of the
	// last-added term; frontier[0] is the (not-yet-final) root.
	frontier []*builderState
	prev     []byte
	buf      []byte            // compiled node bytes, children before parents
	registry map[string]uint64 // node signature -> compiled offset
	rootOff  uint64
	added    bool // whether any term has been added (distinguishes the empty term)
	finished bool
}

// NewBuilder returns an empty builder.
func NewBuilder() *Builder {
	return &Builder{
		frontier: []*builderState{{}},
		registry: make(map[string]uint64),
	}
}

// Add inserts term with the given output. Terms must be added in strictly
// ascending byte order and may not exceed MaxTermLen bytes.
func (b *Builder) Add(term []byte, output uint64) error {
	if b.finished {
		return errors.New("fst: Add after Finish")
	}
	if len(term) > MaxTermLen {
		return fmt.Errorf("fst: term length %d exceeds max %d", len(term), MaxTermLen)
	}
	if b.added && bytesCompare(term, b.prev) <= 0 {
		return ErrUnsortedInput
	}

	prefix := commonPrefixLen(b.prev, term)

	// Freeze the suffix of the previous term that the new term diverges from.
	if err := b.freezeFrom(prefix); err != nil {
		return err
	}

	// Extend the frontier with fresh states for the new term's suffix.
	for i := prefix; i < len(term); i++ {
		ns := &builderState{}
		cur := b.frontier[i]
		cur.arcs = append(cur.arcs, builderArc{label: term[i], target: ns})
		b.frontier = append(b.frontier[:i+1], ns)
	}

	last := b.frontier[len(term)]
	last.isFinal = true

	// Push outputs: subtract the output already committed along the common
	// prefix, and carry the remainder down the diverging arc.
	b.pushOutput(term, output, prefix)

	b.prev = append(b.prev[:0], term...)
	b.added = true
	return nil
}

// pushOutput distributes output along term's path using the sum monoid. Common
// output stays on shared prefix arcs; the diverging arc carries the remainder.
func (b *Builder) pushOutput(term []byte, output uint64, prefix int) {
	out := output
	for i := range prefix {
		arc := lastArcTo(b.frontier[i], term[i])
		common := min(arc.output, out)
		// Move the excess of the existing arc output further down the tree so the
		// shared arc keeps only the common part.
		excess := arc.output - common
		if excess != 0 {
			pushDown(arc.target, excess)
		}
		arc.output = common
		out -= common
	}
	if prefix < len(term) {
		arc := lastArcTo(b.frontier[prefix], term[prefix])
		arc.output = out
	} else {
		// term is a prefix of prev (cannot happen given strict ordering) or the
		// whole output lands on the final state.
		b.frontier[len(term)].finalOutput = out
	}
}

// pushDown adds delta to every outgoing arc output and the final output of s, so
// the total output of paths through s is unchanged when the incoming arc output
// is reduced by delta.
func pushDown(s *builderState, delta uint64) {
	for i := range s.arcs {
		s.arcs[i].output += delta
	}
	if s.isFinal {
		s.finalOutput += delta
	}
}

// freezeFrom compiles and registers frontier states from the deepest one down to
// depth prefix+1, linking each into its parent's last arc.
func (b *Builder) freezeFrom(prefix int) error {
	for i := len(b.prev); i > prefix; i-- {
		off, err := b.compile(b.frontier[i])
		if err != nil {
			return err
		}
		parentArc := &b.frontier[i-1].arcs[len(b.frontier[i-1].arcs)-1]
		parentArc.target = nil
		parentArc.targetOff = off
	}
	b.frontier = b.frontier[:prefix+1]
	return nil
}

// compile freezes one state: it reuses an equivalent registered node or appends a
// new compiled node, returning the node's offset.
func (b *Builder) compile(s *builderState) (uint64, error) {
	sig := signature(s)
	if off, ok := b.registry[sig]; ok {
		return off, nil
	}
	off := uint64(len(b.buf))
	b.buf = encodeNode(b.buf, s)
	b.registry[sig] = off
	return off, nil
}

// Finish freezes the remaining frontier and returns the completed FST.
func (b *Builder) Finish() (*FST, error) {
	if b.finished {
		return nil, errors.New("fst: already finished")
	}
	if !b.added {
		// Empty dictionary: a single non-final root.
		off, err := b.compile(&builderState{})
		if err != nil {
			return nil, err
		}
		b.rootOff = off
	} else {
		if err := b.freezeFrom(0); err != nil {
			return nil, err
		}
		off, err := b.compile(b.frontier[0])
		if err != nil {
			return nil, err
		}
		b.rootOff = off
	}
	b.finished = true
	return &FST{data: b.buf, root: b.rootOff}, nil
}

// signature is a stable key identifying an equivalence class of compiled states.
func signature(s *builderState) string {
	var buf []byte
	var flags byte
	if s.isFinal {
		flags |= flagFinal
	}
	buf = append(buf, flags)
	buf = binary.AppendUvarint(buf, s.finalOutput)
	buf = binary.AppendUvarint(buf, uint64(len(s.arcs)))
	for i := range s.arcs {
		buf = append(buf, s.arcs[i].label)
		buf = binary.AppendUvarint(buf, s.arcs[i].output)
		buf = binary.AppendUvarint(buf, s.arcs[i].targetOff)
	}
	return string(buf)
}

// encodeNode appends the compiled byte form of s to dst. Arcs are sorted by
// label so the reader and the iterator see them in order.
func encodeNode(dst []byte, s *builderState) []byte {
	sort.Slice(s.arcs, func(i, j int) bool { return s.arcs[i].label < s.arcs[j].label })
	var flags byte
	if s.isFinal {
		flags |= flagFinal
	}
	if s.finalOutput != 0 {
		flags |= flagHasFinalOut
	}
	dst = append(dst, flags)
	if flags&flagHasFinalOut != 0 {
		dst = binary.AppendUvarint(dst, s.finalOutput)
	}
	dst = binary.AppendUvarint(dst, uint64(len(s.arcs)))
	for i := range s.arcs {
		dst = append(dst, s.arcs[i].label)
		dst = binary.AppendUvarint(dst, s.arcs[i].output)
		dst = binary.AppendUvarint(dst, s.arcs[i].targetOff)
	}
	return dst
}

func lastArcTo(s *builderState, label byte) *builderArc {
	a := &s.arcs[len(s.arcs)-1]
	if a.label != label {
		// Defensive: the active path's last arc always carries the term byte.
		for i := range s.arcs {
			if s.arcs[i].label == label {
				return &s.arcs[i]
			}
		}
	}
	return a
}

func commonPrefixLen(a, b []byte) int {
	n := min(len(b), len(a))
	for i := range n {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

func bytesCompare(a, b []byte) int {
	n := min(len(b), len(a))
	for i := range n {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	default:
		return 0
	}
}
