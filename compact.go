package search

import (
	"github.com/tamnd/search/catalog"
	"github.com/tamnd/search/segment"
)

// Compact runs one round of tiered compaction and returns the number of segments
// that were merged (0 when no tier was over its threshold). It selects a group
// of segments with the tiered policy, merges them into one new segment that omits
// every deleted document, removes the old segments, and recomputes the
// index-wide statistics. The whole round runs in a single write transaction, so
// readers either see all the old segments or the one merged segment.
func (db *DB) Compact() (int, error) {
	return db.compact(segment.NewTieredPolicy())
}

// CompactAll merges every segment into one, regardless of tier. It is the
// force-merge path used by tools and tests to collapse an index to a single
// segment and reap all tombstones at once.
func (db *DB) CompactAll() (int, error) {
	var merged int
	err := db.Update(func(t *Txn) error {
		c := t.Catalog()
		set, err := segment.LoadSet(c)
		if err != nil {
			return err
		}
		if set.Len() < 2 {
			return nil
		}
		merged, err = runMerge(c, set.Segments())
		return err
	})
	return merged, err
}

// compact runs one round driven by the given policy.
func (db *DB) compact(policy segment.TieredPolicy) (int, error) {
	var merged int
	err := db.Update(func(t *Txn) error {
		c := t.Catalog()
		set, err := segment.LoadSet(c)
		if err != nil {
			return err
		}
		group := policy.Select(set)
		if len(group) == 0 {
			return nil
		}
		merged, err = runMerge(c, group)
		return err
	})
	return merged, err
}

// runMerge merges group into a new segment, removes the inputs, and recomputes
// stats. It assumes it runs inside a write transaction.
func runMerge(c *catalog.Catalog, group []*segment.Segment) (int, error) {
	newID, err := nextSegID(c)
	if err != nil {
		return 0, err
	}
	if _, err := segment.Merge(c, newID, group); err != nil {
		return 0, err
	}
	for _, s := range group {
		if err := segment.Remove(c, s); err != nil {
			return 0, err
		}
	}
	if err := segment.RecomputeStats(c); err != nil {
		return 0, err
	}
	return len(group), nil
}
