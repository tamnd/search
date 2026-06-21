package hnsw

// minHeap is a binary min-heap of candidates ordered by ascending distance: the
// nearest candidate is at the root. It is the frontier of the best-first search.
type minHeap struct{ items []candidate }

func (h *minHeap) len() int { return len(h.items) }

func (h *minHeap) push(c candidate) {
	h.items = append(h.items, c)
	i := len(h.items) - 1
	for i > 0 {
		parent := (i - 1) / 2
		if h.items[parent].dist <= h.items[i].dist {
			break
		}
		h.items[parent], h.items[i] = h.items[i], h.items[parent]
		i = parent
	}
}

func (h *minHeap) pop() candidate {
	n := len(h.items)
	top := h.items[0]
	h.items[0] = h.items[n-1]
	h.items = h.items[:n-1]
	h.down(0)
	return top
}

func (h *minHeap) down(i int) {
	n := len(h.items)
	for {
		l, r := 2*i+1, 2*i+2
		smallest := i
		if l < n && h.items[l].dist < h.items[smallest].dist {
			smallest = l
		}
		if r < n && h.items[r].dist < h.items[smallest].dist {
			smallest = r
		}
		if smallest == i {
			return
		}
		h.items[i], h.items[smallest] = h.items[smallest], h.items[i]
		i = smallest
	}
}

// maxHeap is a binary max-heap of candidates ordered by descending distance: the
// farthest kept result is at the root, so it is cheap to evict the worst when the
// result set overflows ef.
type maxHeap struct{ items []candidate }

func (h *maxHeap) len() int { return len(h.items) }

func (h *maxHeap) peek() candidate { return h.items[0] }

func (h *maxHeap) push(c candidate) {
	h.items = append(h.items, c)
	i := len(h.items) - 1
	for i > 0 {
		parent := (i - 1) / 2
		if h.items[parent].dist >= h.items[i].dist {
			break
		}
		h.items[parent], h.items[i] = h.items[i], h.items[parent]
		i = parent
	}
}

func (h *maxHeap) pop() candidate {
	n := len(h.items)
	top := h.items[0]
	h.items[0] = h.items[n-1]
	h.items = h.items[:n-1]
	h.down(0)
	return top
}

func (h *maxHeap) down(i int) {
	n := len(h.items)
	for {
		l, r := 2*i+1, 2*i+2
		largest := i
		if l < n && h.items[l].dist > h.items[largest].dist {
			largest = l
		}
		if r < n && h.items[r].dist > h.items[largest].dist {
			largest = r
		}
		if largest == i {
			return
		}
		h.items[i], h.items[largest] = h.items[largest], h.items[i]
		i = largest
	}
}

// drainSorted empties the max-heap and returns its candidates ordered nearest
// first, the order callers want for results and entry points.
func (h *maxHeap) drainSorted() []candidate {
	out := make([]candidate, h.len())
	for i := len(out) - 1; i >= 0; i-- {
		out[i] = h.pop()
	}
	return out
}
