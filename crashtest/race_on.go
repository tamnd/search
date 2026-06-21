//go:build race

package crashtest

// raceEnabled is true when the binary is built with the race detector. The bulk
// 10k-cycle campaign replays its workload tens of thousands of times, which is
// impractically slow under race instrumentation, so it is reserved for the
// non-race extended profile; the per-path campaigns still run under race.
const raceEnabled = true
