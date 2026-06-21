// Package quantize implements the two vector compression schemes the engine uses
// for dense-vector search (spec 2063 doc 15 §7, §17): scalar int8 quantization
// and product quantization (PQ). It is stateless math over slices; callers own
// the config and the trained codebook and decide where the bytes are stored.
//
// Int8 quantization maps every component of every vector through one global
// affine transform (a single min and scale for the whole field) so a float32
// vector becomes one int8 per dimension, a 4x size cut. PQ splits a vector into m
// equal subspaces and replaces each with the id of the nearest of 256 trained
// centroids, an m-byte code regardless of dimension.
package quantize

import (
	"encoding/binary"
	"errors"
	"math"

	"github.com/tamnd/search/vector"
)

// Int8Quantizer is the global affine transform for scalar int8 quantization. A
// component x encodes to clamp(round((x-Min)/Scale), 0, 255) - 128, an int8; the
// inverse is q*... it decodes back to (code+128)*Scale + Min. One quantizer
// covers a whole field's vectors so codes from different vectors share a frame.
type Int8Quantizer struct {
	Min   float32
	Scale float32
}

// TrainInt8 fits an Int8Quantizer to a set of vectors by taking the global
// minimum and maximum component over all of them and spreading the 256 levels
// across that range (doc 15 §7.1). An empty set yields the identity-ish frame
// (scale 1), which encodes everything to the zero code.
func TrainInt8(vectors [][]float32) Int8Quantizer {
	if len(vectors) == 0 {
		return Int8Quantizer{Min: 0, Scale: 1}
	}
	mn := float32(math.MaxFloat32)
	mx := float32(-math.MaxFloat32)
	for _, v := range vectors {
		for _, x := range v {
			if x < mn {
				mn = x
			}
			if x > mx {
				mx = x
			}
		}
	}
	scale := (mx - mn) / 255.0
	if scale == 0 {
		// Every component is identical; any non-zero scale keeps decode exact.
		scale = 1
	}
	return Int8Quantizer{Min: mn, Scale: scale}
}

// Encode quantizes one float32 vector to int8 codes.
func (q Int8Quantizer) Encode(v []float32) []int8 {
	out := make([]int8, len(v))
	for i, x := range v {
		level := int32(math.Round(float64((x - q.Min) / q.Scale)))
		level = max(level, 0)
		level = min(level, 255)
		out[i] = int8(level - 128)
	}
	return out
}

// Decode reconstructs an approximate float32 vector from int8 codes.
func (q Int8Quantizer) Decode(codes []int8) []float32 {
	out := make([]float32, len(codes))
	for i, c := range codes {
		out[i] = (float32(int32(c)+128))*q.Scale + q.Min
	}
	return out
}

// EncodeAll quantizes a slice of vectors, returning the flat int8 buffer (row
// major, dims components per vector) and the dimension.
func (q Int8Quantizer) EncodeAll(vectors [][]float32, dims int) []int8 {
	out := make([]int8, len(vectors)*dims)
	for i, v := range vectors {
		copy(out[i*dims:(i+1)*dims], q.Encode(v))
	}
	return out
}

// Marshal serializes the quantizer frame (Min, Scale) to 8 bytes.
func (q Int8Quantizer) Marshal() []byte {
	var b [8]byte
	binary.LittleEndian.PutUint32(b[0:4], math.Float32bits(q.Min))
	binary.LittleEndian.PutUint32(b[4:8], math.Float32bits(q.Scale))
	return b[:]
}

// UnmarshalInt8 reads a frame produced by Marshal.
func UnmarshalInt8(b []byte) (Int8Quantizer, error) {
	if len(b) < 8 {
		return Int8Quantizer{}, errors.New("quantize: short int8 frame")
	}
	return Int8Quantizer{
		Min:   math.Float32frombits(binary.LittleEndian.Uint32(b[0:4])),
		Scale: math.Float32frombits(binary.LittleEndian.Uint32(b[4:8])),
	}, nil
}

// dotInt8Frame returns the float32 dot product approximation between two int8
// codes encoded under the same frame, decoding on the fly. It exists so the
// search path can score int8 codes without materializing two float32 vectors,
// while the kernel in package vector stays a pure int8 routine.
func (q Int8Quantizer) dotInt8Frame(a, b []int8) float32 {
	_ = vector.DotInt8 // keep the pure kernel referenced for callers that want it
	var sum float32
	for i := range a {
		av := (float32(int32(a[i])+128))*q.Scale + q.Min
		bv := (float32(int32(b[i])+128))*q.Scale + q.Min
		sum += av * bv
	}
	return sum
}

// Dot returns the dot product of two int8 codes under this frame.
func (q Int8Quantizer) Dot(a, b []int8) float32 { return q.dotInt8Frame(a, b) }

// L2Sq returns the squared Euclidean distance between two int8 codes under this
// frame.
func (q Int8Quantizer) L2Sq(a, b []int8) float32 {
	var sum float32
	for i := range a {
		av := (float32(int32(a[i])+128))*q.Scale + q.Min
		bv := (float32(int32(b[i])+128))*q.Scale + q.Min
		d := av - bv
		sum += d * d
	}
	return sum
}
