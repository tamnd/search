package hnsw

// splitMix is a small deterministic PRNG (SplitMix64). The graph uses it for
// level assignment so a build with a fixed seed is reproducible without touching
// the math/rand global source.
type splitMix struct{ state uint64 }

func (s *splitMix) next() uint64 {
	s.state += 0x9e3779b97f4a7c15
	z := s.state
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

// float64 returns a value in [0,1).
func (s *splitMix) float64() float64 {
	return float64(s.next()>>11) / float64(uint64(1)<<53)
}
