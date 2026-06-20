package vfs

import (
	"errors"
	"sync"
)

var errEOF = errors.New("search/vfs: read past end of file")

// action is the decision the fault controller makes at a fault point.
type action int

const (
	actionProceed     action = iota // do the operation normally
	actionCrashBefore               // fail immediately, nothing written/synced
	actionTear                      // write a partial (torn) prefix, then fail
	actionFsyncFail                 // fsync reports failure (fatal policy)
)

// FaultController drives deterministic fault injection. A test counts the total
// number of fault points in a workload (by running it once with TripAt = -1, a
// no-op that just counts), then re-runs the workload once per fault point with
// TripAt set to that ordinal, asserting recovery after each.
//
// The controller is deterministic: given the same workload and the same TripAt,
// it trips at the same operation every time, which is what makes a failing crash
// test replayable (doc 20 determinism).
type FaultController struct {
	mu sync.Mutex

	// TripAt is the ordinal of the fault point to trip at. -1 disables tripping
	// (the controller only counts). 0 trips at the first fault point.
	TripAt int

	// Mode selects what happens when the trip point is reached.
	Mode TripMode

	// count is the running number of fault points seen.
	count int

	// tripped records whether the trip has fired (it fires at most once).
	tripped bool
}

// TripMode is how a tripped fault manifests.
type TripMode int

const (
	// TripCrash crashes cleanly before the write (nothing of this op persists).
	TripCrash TripMode = iota
	// TripTear writes a torn prefix and then crashes (torn-write repair test).
	TripTear
	// TripFsyncFail makes the fsync at the trip point report failure.
	TripFsyncFail
)

// NewCounter returns a controller that only counts fault points (never trips),
// used to discover how many fault points a workload has.
func NewCounter() *FaultController { return &FaultController{TripAt: -1} }

// NewTrip returns a controller that trips at the given ordinal with the mode.
func NewTrip(at int, mode TripMode) *FaultController {
	return &FaultController{TripAt: at, Mode: mode}
}

// Count returns how many fault points have been observed so far.
func (fc *FaultController) Count() int {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return fc.count
}

// Tripped reports whether the controller has fired its trip.
func (fc *FaultController) Tripped() bool {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return fc.tripped
}

// onWrite is consulted at every WriteAt. It advances the counter and decides
// whether this write proceeds, crashes, or tears.
func (fc *FaultController) onWrite(int64) action {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	ord := fc.count
	fc.count++
	if fc.TripAt < 0 || fc.tripped || ord != fc.TripAt {
		return actionProceed
	}
	fc.tripped = true
	switch fc.Mode {
	case TripTear:
		return actionTear
	case TripFsyncFail:
		// An fsync-fail trip on a write point is treated as a plain crash; the
		// fsync-fail manifests at the sync point of the same ordinal scheme.
		return actionCrashBefore
	default:
		return actionCrashBefore
	}
}

// onSync is consulted at every Sync. fsync is a fault point too, both for clean
// crashes and for the fsync-fatal policy.
func (fc *FaultController) onSync() action {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	ord := fc.count
	fc.count++
	if fc.TripAt < 0 || fc.tripped || ord != fc.TripAt {
		return actionProceed
	}
	fc.tripped = true
	if fc.Mode == TripFsyncFail {
		return actionFsyncFail
	}
	return actionCrashBefore
}
