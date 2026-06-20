// Package msgpack is a minimal, dependency-free MessagePack codec for the value
// shapes the engine persists: the field schema (doc 06 §2) and the per-document
// stored map (doc 06 §6.2). It is deliberately small. It encodes and decodes
// nil, bool, signed and unsigned integers, float64, UTF-8 strings, raw bytes,
// arrays, and string-keyed maps, which is the entire surface the document model
// and the schema need. It is not a general MessagePack implementation: it does
// not support extension types, non-string map keys, or float32 wire forms on
// encode (float32 inputs are widened to float64).
//
// Keeping this in-tree, rather than taking a dependency, keeps the on-disk
// document and schema formats fully under the engine's control: the byte layout
// of a stored document is defined here and nowhere else.
package msgpack

import (
	"errors"
	"fmt"
	"math"
)

// ErrShortBuffer is returned when the input ends in the middle of a value.
var ErrShortBuffer = errors.New("msgpack: unexpected end of input")

// Marshal encodes v into a MessagePack byte slice. The supported Go types are
// nil, bool, all signed and unsigned integer kinds, float32/float64, string,
// []byte, []any, map[string]any, and map[string]string. An unsupported type is
// an error.
func Marshal(v any) ([]byte, error) {
	var e encoder
	if err := e.encode(v); err != nil {
		return nil, err
	}
	return e.buf, nil
}

// Unmarshal decodes the first MessagePack value in data, returning the Go value
// and the number of bytes consumed. Integers decode to int64 (or uint64 when the
// value exceeds the int64 range), floats to float64, strings to string, binary
// to []byte, arrays to []any, and maps to map[string]any.
func Unmarshal(data []byte) (any, int, error) {
	d := decoder{buf: data}
	v, err := d.decode()
	if err != nil {
		return nil, 0, err
	}
	return v, d.pos, nil
}

type encoder struct {
	buf []byte
}

func (e *encoder) encode(v any) error {
	switch x := v.(type) {
	case nil:
		e.buf = append(e.buf, 0xc0)
	case bool:
		if x {
			e.buf = append(e.buf, 0xc3)
		} else {
			e.buf = append(e.buf, 0xc2)
		}
	case int:
		e.encodeInt(int64(x))
	case int8:
		e.encodeInt(int64(x))
	case int16:
		e.encodeInt(int64(x))
	case int32:
		e.encodeInt(int64(x))
	case int64:
		e.encodeInt(x)
	case uint:
		e.encodeUint(uint64(x))
	case uint8:
		e.encodeUint(uint64(x))
	case uint16:
		e.encodeUint(uint64(x))
	case uint32:
		e.encodeUint(uint64(x))
	case uint64:
		e.encodeUint(x)
	case float32:
		e.encodeFloat(float64(x))
	case float64:
		e.encodeFloat(x)
	case string:
		e.encodeStr(x)
	case []byte:
		e.encodeBin(x)
	case []any:
		e.encodeArrayHeader(len(x))
		for _, el := range x {
			if err := e.encode(el); err != nil {
				return err
			}
		}
	case map[string]any:
		e.encodeMapHeader(len(x))
		for _, k := range sortedKeys(x) {
			e.encodeStr(k)
			if err := e.encode(x[k]); err != nil {
				return err
			}
		}
	case map[string]string:
		e.encodeMapHeader(len(x))
		for _, k := range sortedStringKeys(x) {
			e.encodeStr(k)
			e.encodeStr(x[k])
		}
	default:
		return fmt.Errorf("msgpack: unsupported type %T", v)
	}
	return nil
}

func (e *encoder) encodeInt(i int64) {
	if i >= 0 {
		e.encodeUint(uint64(i))
		return
	}
	switch {
	case i >= -32:
		e.buf = append(e.buf, byte(i))
	case i >= math.MinInt8:
		e.buf = append(e.buf, 0xd0, byte(i))
	case i >= math.MinInt16:
		e.buf = append(e.buf, 0xd1)
		e.appendU16(uint16(i))
	case i >= math.MinInt32:
		e.buf = append(e.buf, 0xd2)
		e.appendU32(uint32(i))
	default:
		e.buf = append(e.buf, 0xd3)
		e.appendU64(uint64(i))
	}
}

func (e *encoder) encodeUint(u uint64) {
	switch {
	case u <= 0x7f:
		e.buf = append(e.buf, byte(u))
	case u <= math.MaxUint8:
		e.buf = append(e.buf, 0xcc, byte(u))
	case u <= math.MaxUint16:
		e.buf = append(e.buf, 0xcd)
		e.appendU16(uint16(u))
	case u <= math.MaxUint32:
		e.buf = append(e.buf, 0xce)
		e.appendU32(uint32(u))
	default:
		e.buf = append(e.buf, 0xcf)
		e.appendU64(u)
	}
}

func (e *encoder) encodeFloat(f float64) {
	e.buf = append(e.buf, 0xcb)
	e.appendU64(math.Float64bits(f))
}

func (e *encoder) encodeStr(s string) {
	n := len(s)
	switch {
	case n <= 31:
		e.buf = append(e.buf, 0xa0|byte(n))
	case n <= math.MaxUint8:
		e.buf = append(e.buf, 0xd9, byte(n))
	case n <= math.MaxUint16:
		e.buf = append(e.buf, 0xda)
		e.appendU16(uint16(n))
	default:
		e.buf = append(e.buf, 0xdb)
		e.appendU32(uint32(n))
	}
	e.buf = append(e.buf, s...)
}

func (e *encoder) encodeBin(b []byte) {
	n := len(b)
	switch {
	case n <= math.MaxUint8:
		e.buf = append(e.buf, 0xc4, byte(n))
	case n <= math.MaxUint16:
		e.buf = append(e.buf, 0xc5)
		e.appendU16(uint16(n))
	default:
		e.buf = append(e.buf, 0xc6)
		e.appendU32(uint32(n))
	}
	e.buf = append(e.buf, b...)
}

func (e *encoder) encodeArrayHeader(n int) {
	switch {
	case n <= 15:
		e.buf = append(e.buf, 0x90|byte(n))
	case n <= math.MaxUint16:
		e.buf = append(e.buf, 0xdc)
		e.appendU16(uint16(n))
	default:
		e.buf = append(e.buf, 0xdd)
		e.appendU32(uint32(n))
	}
}

func (e *encoder) encodeMapHeader(n int) {
	switch {
	case n <= 15:
		e.buf = append(e.buf, 0x80|byte(n))
	case n <= math.MaxUint16:
		e.buf = append(e.buf, 0xde)
		e.appendU16(uint16(n))
	default:
		e.buf = append(e.buf, 0xdf)
		e.appendU32(uint32(n))
	}
}

func (e *encoder) appendU16(v uint16) {
	e.buf = append(e.buf, byte(v>>8), byte(v))
}

func (e *encoder) appendU32(v uint32) {
	e.buf = append(e.buf, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

func (e *encoder) appendU64(v uint64) {
	e.buf = append(e.buf,
		byte(v>>56), byte(v>>48), byte(v>>40), byte(v>>32),
		byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

type decoder struct {
	buf []byte
	pos int
}

func (d *decoder) decode() (any, error) {
	c, err := d.readByte()
	if err != nil {
		return nil, err
	}
	switch {
	case c <= 0x7f: // positive fixint
		return int64(c), nil
	case c >= 0xe0: // negative fixint
		return int64(int8(c)), nil
	case c >= 0xa0 && c <= 0xbf: // fixstr
		return d.readString(int(c & 0x1f))
	case c >= 0x90 && c <= 0x9f: // fixarray
		return d.readArray(int(c & 0x0f))
	case c >= 0x80 && c <= 0x8f: // fixmap
		return d.readMap(int(c & 0x0f))
	}
	switch c {
	case 0xc0:
		return nil, nil
	case 0xc2:
		return false, nil
	case 0xc3:
		return true, nil
	case 0xcc:
		return d.readUint(1)
	case 0xcd:
		return d.readUint(2)
	case 0xce:
		return d.readUint(4)
	case 0xcf:
		return d.readUint(8)
	case 0xd0:
		v, err := d.readRaw(1)
		return castInt(v, int8Width), err
	case 0xd1:
		v, err := d.readRaw(2)
		return castInt(v, int16Width), err
	case 0xd2:
		v, err := d.readRaw(4)
		return castInt(v, int32Width), err
	case 0xd3:
		v, err := d.readRaw(8)
		return castInt(v, int64Width), err
	case 0xca:
		v, err := d.readRaw(4)
		if err != nil {
			return nil, err
		}
		return float64(math.Float32frombits(uint32(v))), nil
	case 0xcb:
		v, err := d.readRaw(8)
		if err != nil {
			return nil, err
		}
		return math.Float64frombits(v), nil
	case 0xd9:
		n, err := d.readLen(1)
		if err != nil {
			return nil, err
		}
		return d.readString(n)
	case 0xda:
		n, err := d.readLen(2)
		if err != nil {
			return nil, err
		}
		return d.readString(n)
	case 0xdb:
		n, err := d.readLen(4)
		if err != nil {
			return nil, err
		}
		return d.readString(n)
	case 0xc4:
		n, err := d.readLen(1)
		if err != nil {
			return nil, err
		}
		return d.readBin(n)
	case 0xc5:
		n, err := d.readLen(2)
		if err != nil {
			return nil, err
		}
		return d.readBin(n)
	case 0xc6:
		n, err := d.readLen(4)
		if err != nil {
			return nil, err
		}
		return d.readBin(n)
	case 0xdc:
		n, err := d.readLen(2)
		if err != nil {
			return nil, err
		}
		return d.readArray(n)
	case 0xdd:
		n, err := d.readLen(4)
		if err != nil {
			return nil, err
		}
		return d.readArray(n)
	case 0xde:
		n, err := d.readLen(2)
		if err != nil {
			return nil, err
		}
		return d.readMap(n)
	case 0xdf:
		n, err := d.readLen(4)
		if err != nil {
			return nil, err
		}
		return d.readMap(n)
	}
	return nil, fmt.Errorf("msgpack: unknown tag 0x%02x at offset %d", c, d.pos-1)
}

type intWidth int

const (
	int8Width intWidth = iota
	int16Width
	int32Width
	int64Width
)

// castInt reinterprets the raw big-endian bytes of a signed integer of the given
// width as a signed int64.
func castInt(u uint64, w intWidth) any {
	switch w {
	case int8Width:
		return int64(int8(u))
	case int16Width:
		return int64(int16(u))
	case int32Width:
		return int64(int32(u))
	default:
		return int64(u)
	}
}

func (d *decoder) readByte() (byte, error) {
	if d.pos >= len(d.buf) {
		return 0, ErrShortBuffer
	}
	c := d.buf[d.pos]
	d.pos++
	return c, nil
}

// readRaw reads n big-endian bytes and returns them as a uint64.
func (d *decoder) readRaw(n int) (uint64, error) {
	if d.pos+n > len(d.buf) {
		return 0, ErrShortBuffer
	}
	var u uint64
	for i := range n {
		u = u<<8 | uint64(d.buf[d.pos+i])
	}
	d.pos += n
	return u, nil
}

// readUint reads an n-byte unsigned integer, returning it as an int64 when it
// fits the int64 range and as a uint64 otherwise, so positive integers decode to
// int64 like the fixint and signed forms do.
func (d *decoder) readUint(n int) (any, error) {
	u, err := d.readRaw(n)
	if err != nil {
		return nil, err
	}
	if u <= math.MaxInt64 {
		return int64(u), nil
	}
	return u, nil
}

func (d *decoder) readLen(n int) (int, error) {
	if d.pos+n > len(d.buf) {
		return 0, ErrShortBuffer
	}
	var u uint64
	for i := range n {
		u = u<<8 | uint64(d.buf[d.pos+i])
	}
	d.pos += n
	return int(u), nil
}

func (d *decoder) readString(n int) (string, error) {
	if d.pos+n > len(d.buf) {
		return "", ErrShortBuffer
	}
	s := string(d.buf[d.pos : d.pos+n])
	d.pos += n
	return s, nil
}

func (d *decoder) readBin(n int) ([]byte, error) {
	if d.pos+n > len(d.buf) {
		return nil, ErrShortBuffer
	}
	b := make([]byte, n)
	copy(b, d.buf[d.pos:d.pos+n])
	d.pos += n
	return b, nil
}

func (d *decoder) readArray(n int) ([]any, error) {
	out := make([]any, n)
	for i := range n {
		v, err := d.decode()
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

func (d *decoder) readMap(n int) (map[string]any, error) {
	out := make(map[string]any, n)
	for range n {
		k, err := d.decode()
		if err != nil {
			return nil, err
		}
		ks, ok := k.(string)
		if !ok {
			return nil, fmt.Errorf("msgpack: non-string map key %T", k)
		}
		v, err := d.decode()
		if err != nil {
			return nil, err
		}
		out[ks] = v
	}
	return out, nil
}
