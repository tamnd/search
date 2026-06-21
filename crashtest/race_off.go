//go:build !race

package crashtest

// raceEnabled is false in ordinary builds; see race_on.go.
const raceEnabled = false
