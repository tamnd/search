// Package wal is the write-ahead log (spec 2063 doc 05). At S0 it contributes
// only the SyncLevel type that the pager and the public Options reference; the
// log itself, group commit, and crash recovery are built at S1. Keeping the
// type here from the start means the public API surface does not change shape
// when the implementation lands.
package wal

// SyncLevel selects the durability discipline a write transaction uses on
// commit. The zero value is the safe default: a full fsync on every commit.
type SyncLevel uint8

const (
	// SyncFull fsyncs the WAL (and, at checkpoint, the main file) on every
	// commit. After commit returns, the transaction is durable. This is the
	// default and the only level that honors the crash-safety contract in full.
	SyncFull SyncLevel = iota

	// SyncNormal fsyncs the WAL on commit but defers the main-file fsync to
	// checkpoint. A crash can lose only un-checkpointed work, never corrupt the
	// file. Built at S1.
	SyncNormal

	// SyncOff performs no fsync. Fast and unsafe; a crash may lose recent
	// commits. Intended for bulk-load-then-checkpoint workflows. Built at S1.
	SyncOff
)

// String returns the human-readable name of the sync level.
func (s SyncLevel) String() string {
	switch s {
	case SyncFull:
		return "full"
	case SyncNormal:
		return "normal"
	case SyncOff:
		return "off"
	default:
		return "unknown"
	}
}
