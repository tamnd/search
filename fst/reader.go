package fst

import (
	"encoding/binary"
	"fmt"
)

// FST is a compiled, read-only finite-state transducer.
type FST struct {
	data []byte
	root uint64
}

// Bytes returns the serialized FST: an 8-byte little-endian root offset followed
// by the compiled node buffer. Open reverses this.
func (f *FST) Bytes() []byte {
	out := make([]byte, 8+len(f.data))
	binary.LittleEndian.PutUint64(out[:8], f.root)
	copy(out[8:], f.data)
	return out
}

// Open parses the serialized form produced by FST.Bytes.
func Open(b []byte) (*FST, error) {
	if len(b) < 8 {
		return nil, fmt.Errorf("fst: serialized form too short (%d bytes)", len(b))
	}
	root := binary.LittleEndian.Uint64(b[:8])
	data := b[8:]
	if root > uint64(len(data)) {
		return nil, fmt.Errorf("fst: root offset %d out of range %d", root, len(data))
	}
	return &FST{data: data, root: root}, nil
}

// node is a parsed FST state.
type node struct {
	isFinal     bool
	finalOutput uint64
	arcs        []nodeArc
}

type nodeArc struct {
	label  byte
	output uint64
	target uint64
}

// readNode parses the node at off.
func (f *FST) readNode(off uint64) (node, error) {
	if off >= uint64(len(f.data)) {
		return node{}, fmt.Errorf("fst: node offset %d out of range", off)
	}
	p := off
	flags := f.data[p]
	p++
	var n node
	n.isFinal = flags&flagFinal != 0
	if flags&flagHasFinalOut != 0 {
		v, m := binary.Uvarint(f.data[p:])
		if m <= 0 {
			return node{}, fmt.Errorf("fst: bad final output at %d", p)
		}
		n.finalOutput = v
		p += uint64(m)
	}
	count, m := binary.Uvarint(f.data[p:])
	if m <= 0 {
		return node{}, fmt.Errorf("fst: bad arc count at %d", p)
	}
	p += uint64(m)
	n.arcs = make([]nodeArc, 0, count)
	for range count {
		if p >= uint64(len(f.data)) {
			return node{}, fmt.Errorf("fst: truncated arc at %d", p)
		}
		label := f.data[p]
		p++
		out, mm := binary.Uvarint(f.data[p:])
		if mm <= 0 {
			return node{}, fmt.Errorf("fst: bad arc output at %d", p)
		}
		p += uint64(mm)
		tgt, mt := binary.Uvarint(f.data[p:])
		if mt <= 0 {
			return node{}, fmt.Errorf("fst: bad arc target at %d", p)
		}
		p += uint64(mt)
		n.arcs = append(n.arcs, nodeArc{label: label, output: out, target: tgt})
	}
	return n, nil
}

// Lookup returns the output for term and whether the term is in the dictionary.
func (f *FST) Lookup(term []byte) (uint64, bool, error) {
	off := f.root
	var out uint64
	for _, c := range term {
		n, err := f.readNode(off)
		if err != nil {
			return 0, false, err
		}
		arc, ok := findArc(n.arcs, c)
		if !ok {
			return 0, false, nil
		}
		out += arc.output
		off = arc.target
	}
	n, err := f.readNode(off)
	if err != nil {
		return 0, false, err
	}
	if !n.isFinal {
		return 0, false, nil
	}
	return out + n.finalOutput, true, nil
}

// findArc returns the arc labelled c. Arcs are sorted, so a binary search keeps
// lookup logarithmic in the fan-out.
func findArc(arcs []nodeArc, c byte) (nodeArc, bool) {
	lo, hi := 0, len(arcs)
	for lo < hi {
		mid := (lo + hi) / 2
		switch {
		case arcs[mid].label < c:
			lo = mid + 1
		case arcs[mid].label > c:
			hi = mid
		default:
			return arcs[mid], true
		}
	}
	return nodeArc{}, false
}

// Entry is one (term, output) pair from an enumeration.
type Entry struct {
	Term   []byte
	Output uint64
}

// All returns every (term, output) pair in lexicographic order.
func (f *FST) All() ([]Entry, error) {
	return f.collect(f.root, nil, 0)
}

// PrefixScan returns every term that starts with prefix, in lexicographic order.
func (f *FST) PrefixScan(prefix []byte) ([]Entry, error) {
	off := f.root
	var out uint64
	for _, c := range prefix {
		n, err := f.readNode(off)
		if err != nil {
			return nil, err
		}
		arc, ok := findArc(n.arcs, c)
		if !ok {
			return nil, nil
		}
		out += arc.output
		off = arc.target
	}
	return f.collect(off, append([]byte(nil), prefix...), out)
}

// RangeScan returns every term t with lo <= t < hi, in lexicographic order. A nil
// lo means unbounded below; a nil hi means unbounded above.
func (f *FST) RangeScan(lo, hi []byte) ([]Entry, error) {
	all, err := f.All()
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, e := range all {
		if lo != nil && bytesCompare(e.Term, lo) < 0 {
			continue
		}
		if hi != nil && bytesCompare(e.Term, hi) >= 0 {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

// collect performs a depth-first, label-ordered walk from off, emitting an entry
// at every accepting state. prefix is the term bytes consumed to reach off and
// acc is the accumulated output there.
func (f *FST) collect(off uint64, prefix []byte, acc uint64) ([]Entry, error) {
	var out []Entry
	var walk func(off uint64, term []byte, acc uint64) error
	walk = func(off uint64, term []byte, acc uint64) error {
		n, err := f.readNode(off)
		if err != nil {
			return err
		}
		if n.isFinal {
			t := append([]byte(nil), term...)
			out = append(out, Entry{Term: t, Output: acc + n.finalOutput})
		}
		for _, arc := range n.arcs {
			if err := walk(arc.target, append(term, arc.label), acc+arc.output); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(off, prefix, acc); err != nil {
		return nil, err
	}
	return out, nil
}
