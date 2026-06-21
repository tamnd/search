package agg

import "sort"

// tdigest is a merging t-digest for approximate quantile estimation (doc 14
// §7.4). It keeps a set of centroids, each a (mean, weight) pair, and bounds the
// total centroid count with a compression parameter so the structure stays
// small while remaining accurate near the distribution's tails. Incoming values
// are buffered as singleton centroids and folded in when the buffer fills.
type tdigest struct {
	compression float64
	centroids   []centroid
	buffer      []float64
	count       float64
}

type centroid struct {
	mean   float64
	weight float64
}

func newTDigest(compression float64) *tdigest {
	if compression < 20 {
		compression = 20
	}
	return &tdigest{compression: compression}
}

// add records one value.
func (d *tdigest) add(x float64) {
	d.buffer = append(d.buffer, x)
	d.count++
	if len(d.buffer) >= 1000 {
		d.flush()
	}
}

// flush merges the buffered values into the centroid set and compresses.
func (d *tdigest) flush() {
	if len(d.buffer) == 0 {
		return
	}
	for _, x := range d.buffer {
		d.centroids = append(d.centroids, centroid{mean: x, weight: 1})
	}
	d.buffer = d.buffer[:0]
	d.compress()
}

// compress sorts the centroids by mean and merges adjacent ones while the
// running quantile position allows, bounding each merged centroid's weight by
// the t-digest size limit k(q) = 4*N*compression*q*(1-q).
func (d *tdigest) compress() {
	if len(d.centroids) <= 1 {
		return
	}
	sort.Slice(d.centroids, func(i, j int) bool { return d.centroids[i].mean < d.centroids[j].mean })

	total := 0.0
	for _, c := range d.centroids {
		total += c.weight
	}

	merged := make([]centroid, 0, len(d.centroids))
	cur := d.centroids[0]
	soFar := 0.0
	for i := 1; i < len(d.centroids); i++ {
		c := d.centroids[i]
		// Quantile at the midpoint of the merged centroid if c is absorbed.
		q := (soFar + (cur.weight+c.weight)/2) / total
		limit := 4 * total * d.compression * q * (1 - q) / total
		if limit < 1 {
			limit = 1
		}
		if cur.weight+c.weight <= limit {
			w := cur.weight + c.weight
			cur.mean = (cur.mean*cur.weight + c.mean*c.weight) / w
			cur.weight = w
		} else {
			merged = append(merged, cur)
			soFar += cur.weight
			cur = c
		}
	}
	merged = append(merged, cur)
	d.centroids = merged
}

// quantile returns the estimated value at quantile q in [0,1].
func (d *tdigest) quantile(q float64) float64 {
	d.flush()
	if len(d.centroids) == 0 {
		return 0
	}
	if len(d.centroids) == 1 {
		return d.centroids[0].mean
	}
	total := 0.0
	for _, c := range d.centroids {
		total += c.weight
	}
	target := q * total
	// Walk the centroids, accumulating weight, and interpolate between the means
	// of the two centroids straddling the target rank.
	cum := 0.0
	for i, c := range d.centroids {
		next := cum + c.weight
		center := cum + c.weight/2
		if target <= center {
			if i == 0 {
				return c.mean
			}
			prev := d.centroids[i-1]
			prevCenter := cum - prev.weight/2
			frac := (target - prevCenter) / (center - prevCenter)
			return prev.mean + frac*(c.mean-prev.mean)
		}
		cum = next
	}
	return d.centroids[len(d.centroids)-1].mean
}
