package services

import "sync"

const geoAddressQueueCap = 10000

// geoAddressQueue holds proxy addresses waiting for GeoIP enrichment.
type geoAddressQueue struct {
	ch      chan string
	pending map[string]struct{}
	mu      sync.Mutex
}

func newGeoAddressQueue() *geoAddressQueue {
	return &geoAddressQueue{
		ch:      make(chan string, geoAddressQueueCap),
		pending: make(map[string]struct{}),
	}
}

// Enqueue adds addresses that are not already queued. Returns count added.
func (q *geoAddressQueue) Enqueue(addresses []string) int {
	if len(addresses) == 0 {
		return 0
	}

	var batch []string
	q.mu.Lock()
	for _, addr := range addresses {
		if _, ok := q.pending[addr]; ok {
			continue
		}
		q.pending[addr] = struct{}{}
		batch = append(batch, addr)
	}
	q.mu.Unlock()

	added := 0
	for _, addr := range batch {
		select {
		case q.ch <- addr:
			added++
		default:
			// Queue full — drop from pending so a later drain can retry.
			q.mu.Lock()
			delete(q.pending, addr)
			q.mu.Unlock()
		}
	}
	return added
}

// Len returns channel depth plus pending markers (approx in-flight queue size).
func (q *geoAddressQueue) Len() int {
	q.mu.Lock()
	n := len(q.pending)
	q.mu.Unlock()
	return n + len(q.ch)
}

// Release removes addresses from the pending set after processing.
func (q *geoAddressQueue) Release(addresses []string) {
	q.mu.Lock()
	for _, addr := range addresses {
		delete(q.pending, addr)
	}
	q.mu.Unlock()
}
