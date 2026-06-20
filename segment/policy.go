package segment

import "sort"

// DefaultTierThreshold is the number of segments a tier may hold before the
// tiered policy compacts it.
const DefaultTierThreshold = 4

// tierGrowth is the document-count ratio between adjacent tiers: a segment falls
// into tier t when its document count is in [tierGrowth^t, tierGrowth^(t+1)).
const tierGrowth = 4

// TieredPolicy decides which segments to compact. Segments are grouped into
// tiers by document count; a tier with more than Threshold segments is compacted
// by merging its smallest segments into one larger segment, which lands in a
// higher tier. This keeps the segment count logarithmic in the document count
// (spec 2063 doc 10 §6).
type TieredPolicy struct {
	Threshold int
}

// NewTieredPolicy returns a policy with the default threshold.
func NewTieredPolicy() TieredPolicy { return TieredPolicy{Threshold: DefaultTierThreshold} }

// threshold returns the configured threshold or the default when unset.
func (p TieredPolicy) threshold() int {
	if p.Threshold <= 0 {
		return DefaultTierThreshold
	}
	return p.Threshold
}

// tierOf returns the tier index of a segment from its document count.
func tierOf(docCount uint32) int {
	t := 0
	n := docCount / tierGrowth
	for n > 0 {
		t++
		n /= tierGrowth
	}
	return t
}

// Select returns a group of segments to compact, or nil when no tier is over its
// threshold. It picks the most overfull tier and returns its Threshold smallest
// segments, so the merge does the least work that still reduces the count.
func (p TieredPolicy) Select(set *SegmentSet) []*Segment {
	th := p.threshold()
	byTier := map[int][]*Segment{}
	for _, s := range set.Segments() {
		t := tierOf(s.meta.DocCount)
		byTier[t] = append(byTier[t], s)
	}

	bestTier, bestCount := -1, 0
	for t, segs := range byTier {
		if len(segs) > th && len(segs) > bestCount {
			bestTier, bestCount = t, len(segs)
		}
	}
	if bestTier < 0 {
		return nil
	}

	segs := byTier[bestTier]
	sort.Slice(segs, func(i, j int) bool {
		if segs[i].meta.DocCount != segs[j].meta.DocCount {
			return segs[i].meta.DocCount < segs[j].meta.DocCount
		}
		return segs[i].meta.ID < segs[j].meta.ID
	})
	return segs[:th]
}
