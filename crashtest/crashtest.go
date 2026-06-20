// Package crashtest is the crash-recovery campaign harness for the durable
// substrate (spec 2063 doc 05 §8, doc 20). It seeds a committed index, counts
// every fault point a write workload reaches, then replays that workload once per
// fault point per failure mode, crashing at exactly that point and asserting the
// reopened index recovers to an atomic state: either the pre-workload version in
// full or the post-workload version in full, never a torn mixture.
//
// The whole campaign is deterministic. Fault injection trips at a fixed ordinal,
// the clock is fake, and the media is in-memory, so a failing cycle is named by
// its (ordinal, mode) pair and replays identically.
package crashtest

import (
	"fmt"

	"github.com/tamnd/search"
	"github.com/tamnd/search/determ"
	"github.com/tamnd/search/vfs"
)

// Workload describes one crash campaign.
type Workload struct {
	// PageSize is the page size for the seeded file; 0 uses the default.
	PageSize uint32
	// Seed establishes the committed pre-workload state. It runs once, fault
	// free, and its result is the baseline every trial starts from.
	Seed func(db *search.DB) error
	// Mutate is the workload run under fault injection. Each trial runs it once
	// with a crash armed at one fault point; it is expected to fail partway.
	Mutate func(db *search.DB) error
	// Verify runs on every recovered index and must accept any atomic outcome.
	// It is handed the recovered DB plus whether the post-workload state is
	// present, so it can assert the matching all-or-nothing invariant.
	Verify func(db *search.DB, post bool) error
	// Post reports, given a recovered DB, whether the workload's effects are
	// present (true) or the baseline still stands (false). It must never see a
	// partial state; returning an error means the recovered state is torn.
	Post func(db *search.DB) (bool, error)
}

// Failure is one recovered state that violated atomicity.
type Failure struct {
	Ordinal int
	Mode    vfs.TripMode
	Err     error
}

// Report summarizes a campaign.
type Report struct {
	FaultPoints int
	Cycles      int
	Failures    []Failure
}

// allModes is the failure taxonomy exercised at every fault point.
var allModes = []vfs.TripMode{vfs.TripCrash, vfs.TripTear, vfs.TripFsyncFail}

const idxName = "idx.sx"

// Run executes the campaign and returns its report. A harness-level error (the
// seed itself failing, or a recovered index that will not even open) is returned
// as the error; per-cycle atomicity violations are collected in Report.Failures.
func Run(wl Workload) (Report, error) {
	baseline, err := seed(wl)
	if err != nil {
		return Report{}, fmt.Errorf("seed: %w", err)
	}

	points, err := countFaultPoints(wl, baseline)
	if err != nil {
		return Report{}, fmt.Errorf("count fault points: %w", err)
	}

	rep := Report{FaultPoints: points}
	for ord := range points {
		for _, mode := range allModes {
			rep.Cycles++
			if f := runCycle(wl, baseline, ord, mode); f != nil {
				rep.Failures = append(rep.Failures, *f)
			}
		}
	}
	return rep, nil
}

// seed builds the committed baseline media on a clean in-memory VFS.
func seed(wl Workload) (*vfs.Mem, error) {
	mem := vfs.NewMem()
	db, err := openDB(mem, wl.PageSize)
	if err != nil {
		return nil, err
	}
	if err := wl.Seed(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := db.Close(); err != nil {
		return nil, err
	}
	return mem.Snapshot(), nil
}

// countFaultPoints runs the workload once with a counting-only controller to
// learn how many write and sync boundaries it reaches.
func countFaultPoints(wl Workload, baseline *vfs.Mem) (int, error) {
	mem := baseline.Snapshot()
	db, err := openDB(mem, wl.PageSize)
	if err != nil {
		return 0, err
	}
	defer func() { _ = db.Close() }()
	fc := vfs.NewCounter()
	mem.Attach(fc)
	// The fault-free workload must succeed; only its boundary count matters.
	if err := wl.Mutate(db); err != nil {
		return 0, fmt.Errorf("fault-free mutate failed: %w", err)
	}
	return fc.Count(), nil
}

// runCycle crashes the workload at one fault point and verifies recovery. It
// returns a Failure if the recovered index is not atomic, or nil on success.
func runCycle(wl Workload, baseline *vfs.Mem, ord int, mode vfs.TripMode) *Failure {
	mem := baseline.Snapshot()
	db, err := openDB(mem, wl.PageSize)
	if err != nil {
		return &Failure{ord, mode, fmt.Errorf("open before mutate: %w", err)}
	}
	mem.Attach(NewTripFor(ord, mode))
	// The mutate is expected to fail at the armed point; the abandoned handle is
	// discarded without closing (closing could write past the crash).
	_ = wl.Mutate(db)

	// The post-crash media is exactly what reached storage; reopen it clean.
	crashed := mem.Snapshot()
	rdb, err := openDB(crashed, wl.PageSize)
	if err != nil {
		return &Failure{ord, mode, fmt.Errorf("reopen after crash: %w", err)}
	}
	defer func() { _ = rdb.Close() }()

	post, err := wl.Post(rdb)
	if err != nil {
		return &Failure{ord, mode, fmt.Errorf("torn state: %w", err)}
	}
	if err := wl.Verify(rdb, post); err != nil {
		return &Failure{ord, mode, fmt.Errorf("verify (post=%v): %w", post, err)}
	}
	return nil
}

// NewTripFor builds a trip controller for one fault point and mode.
func NewTripFor(ord int, mode vfs.TripMode) *vfs.FaultController {
	return vfs.NewTrip(ord, mode)
}

// openDB opens the campaign index over mem with a deterministic clock and salt.
func openDB(mem *vfs.Mem, pageSize uint32) (*search.DB, error) {
	return search.Open(idxName, search.Options{
		VFS:      mem,
		PageSize: pageSize,
		Clock:    determ.NewFakeClock(0),
		SaltSeed: 1,
	})
}
