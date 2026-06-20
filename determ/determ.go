// Package determ provides the determinism hooks (spec 2063 doc 20): an
// injectable clock and a seeded PRNG. The substrate takes these as dependencies
// rather than calling time.Now or the global rand source directly, so crash
// tests and concurrency tests can be replayed bit for bit. A crash test that
// cannot be replayed deterministically is far less useful.
package determ

import (
	"sync/atomic"
	"time"
)

// Clock yields monotonically non-decreasing nanosecond timestamps. The real
// implementation reads the OS clock; the test implementation advances on demand.
type Clock interface {
	// Now returns the current time in nanoseconds since an arbitrary epoch.
	Now() int64
}

// PRNG is a seeded pseudo-random source. Determinism comes from the seed: the
// same seed yields the same sequence, on any platform.
type PRNG interface {
	// Uint64 returns the next 64-bit value.
	Uint64() uint64
	// Intn returns a value in [0, n).
	Intn(n int) int
}

// OSClock reads the real wall clock.
type OSClock struct{}

// Now returns the current wall-clock time in nanoseconds.
func (OSClock) Now() int64 { return time.Now().UnixNano() }

// FakeClock is a manually advanced clock for tests.
type FakeClock struct{ ns atomic.Int64 }

// NewFakeClock returns a clock starting at the given nanosecond value.
func NewFakeClock(start int64) *FakeClock {
	c := &FakeClock{}
	c.ns.Store(start)
	return c
}

// Now returns the current fake time.
func (c *FakeClock) Now() int64 { return c.ns.Load() }

// Advance moves the clock forward by d nanoseconds and returns the new value.
func (c *FakeClock) Advance(d int64) int64 { return c.ns.Add(d) }

// SplitMix64 is a small, fast, fully deterministic PRNG (Vigna's SplitMix64). It
// is not cryptographic; it exists so test randomness is reproducible.
type SplitMix64 struct{ state uint64 }

// NewPRNG returns a SplitMix64 seeded with seed.
func NewPRNG(seed uint64) *SplitMix64 { return &SplitMix64{state: seed} }

// Uint64 returns the next value in the sequence.
func (r *SplitMix64) Uint64() uint64 {
	r.state += 0x9E3779B97F4A7C15
	z := r.state
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

// Intn returns a value in [0, n), or 0 for n <= 0.
func (r *SplitMix64) Intn(n int) int {
	if n <= 0 {
		return 0
	}
	return int(r.Uint64() % uint64(n))
}
