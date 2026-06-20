package schema

import (
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"time"
)

// Order-preserving numeric term encoding (spec 2063 doc 06 §5, doc 09). S4 has no
// dedicated points or doc-values range structure yet, so numeric, date, and
// boolean fields are made range-searchable by indexing each value as a single
// fixed-width term whose lexicographic byte order matches the value's natural
// order. A RangeQuery then becomes a term range scan over the field's FST, reusing
// the same inverted-index machinery as text and keyword fields. The encoding is
// the standard sortable form: integers flip the sign bit, floats flip the sign
// bit for positives and all bits for negatives.

// EncodeLong returns the 8-byte order-preserving term for a signed 64-bit value.
func EncodeLong(v int64) string {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(v)^0x8000000000000000)
	return string(b[:])
}

// EncodeDouble returns the 8-byte order-preserving term for a 64-bit float. NaN
// is normalized to its canonical bit pattern so it sorts consistently at the top.
func EncodeDouble(v float64) string {
	bits := math.Float64bits(v)
	if bits&0x8000000000000000 != 0 {
		bits = ^bits // negative: flip everything so larger magnitude sorts lower
	} else {
		bits ^= 0x8000000000000000 // positive: flip only the sign bit
	}
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], bits)
	return string(b[:])
}

// EncodeBool returns the order-preserving term for a boolean (false < true).
func EncodeBool(v bool) string {
	if v {
		return "\x01"
	}
	return "\x00"
}

// NumericTerm encodes a value for an indexed field of a numeric, date, or boolean
// type into its order-preserving term, coercing the Go value as needed. It
// returns false when the field type is not one this encoding covers.
func NumericTerm(t FieldType, v any) (string, bool, error) {
	switch t {
	case TypeLong:
		n, err := toInt64(v)
		if err != nil {
			return "", false, err
		}
		return EncodeLong(n), true, nil
	case TypeDouble:
		f, err := toFloat64(v)
		if err != nil {
			return "", false, err
		}
		return EncodeDouble(f), true, nil
	case TypeDate:
		n, err := toEpochNanos(v)
		if err != nil {
			return "", false, err
		}
		return EncodeLong(n), true, nil
	case TypeBoolean:
		b, err := toBool(v)
		if err != nil {
			return "", false, err
		}
		return EncodeBool(b), true, nil
	default:
		return "", false, nil
	}
}

// ParseNumericBound parses a textual range bound into the field's term form. An
// empty string means the bound is open and returns ok=false.
func ParseNumericBound(t FieldType, s string) (term string, ok bool, err error) {
	if s == "" || s == "*" {
		return "", false, nil
	}
	switch t {
	case TypeLong:
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return "", false, fmt.Errorf("schema: bad long bound %q: %w", s, err)
		}
		return EncodeLong(n), true, nil
	case TypeDouble:
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return "", false, fmt.Errorf("schema: bad double bound %q: %w", s, err)
		}
		return EncodeDouble(f), true, nil
	case TypeDate:
		n, err := toEpochNanos(s)
		if err != nil {
			return "", false, err
		}
		return EncodeLong(n), true, nil
	case TypeBoolean:
		b, err := toBool(s)
		if err != nil {
			return "", false, err
		}
		return EncodeBool(b), true, nil
	default:
		return "", false, fmt.Errorf("schema: field type %q is not numerically rangeable", t)
	}
}

func toInt64(v any) (int64, error) {
	switch n := v.(type) {
	case int:
		return int64(n), nil
	case int32:
		return int64(n), nil
	case int64:
		return n, nil
	case uint:
		return int64(n), nil
	case uint32:
		return int64(n), nil
	case uint64:
		return int64(n), nil
	case float32:
		return int64(n), nil
	case float64:
		return int64(n), nil
	case string:
		return strconv.ParseInt(n, 10, 64)
	default:
		return 0, fmt.Errorf("schema: cannot use %T as long", v)
	}
}

func toFloat64(v any) (float64, error) {
	switch n := v.(type) {
	case float64:
		return n, nil
	case float32:
		return float64(n), nil
	case int:
		return float64(n), nil
	case int32:
		return float64(n), nil
	case int64:
		return float64(n), nil
	case uint64:
		return float64(n), nil
	case string:
		return strconv.ParseFloat(n, 64)
	default:
		return 0, fmt.Errorf("schema: cannot use %T as double", v)
	}
}

func toBool(v any) (bool, error) {
	switch b := v.(type) {
	case bool:
		return b, nil
	case string:
		return strconv.ParseBool(b)
	default:
		return false, fmt.Errorf("schema: cannot use %T as boolean", v)
	}
}

// toEpochNanos coerces a date value into unix nanoseconds. It accepts an RFC3339
// string, an integer count of nanoseconds, or a time.Time.
func toEpochNanos(v any) (int64, error) {
	switch d := v.(type) {
	case time.Time:
		return d.UnixNano(), nil
	case string:
		t, err := time.Parse(time.RFC3339, d)
		if err != nil {
			// Fall back to an integer count of nanoseconds in string form.
			if n, ierr := strconv.ParseInt(d, 10, 64); ierr == nil {
				return n, nil
			}
			return 0, fmt.Errorf("schema: bad date %q: %w", d, err)
		}
		return t.UnixNano(), nil
	default:
		return toInt64(v)
	}
}
