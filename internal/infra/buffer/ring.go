// Package buffer provides an in-memory ring buffer for Snapshots.
// It decouples the collect cadence from the ship cadence and absorbs
// short API outages without data loss (up to capacity).
package buffer

import (
	"log/slog"
	"sync"

	"github.com/keywatch/agent/internal/domain"
)

// Ring is a fixed-capacity, thread-safe ring buffer of *domain.Snapshot.
// When the buffer is full and Enqueue is called, the oldest entry is
// silently dropped to make room — this is intentional. The agent must
// never consume unbounded memory.
type Ring struct {
	mu       sync.Mutex
	items    []*domain.Snapshot
	head     int // next write position
	tail     int // next read position
	count    int
	capacity int
	logger   *slog.Logger

	droppedTotal int64
}

// New creates a Ring with the given capacity.
// capacity must be > 0.
func New(capacity int, logger *slog.Logger) *Ring {
	if capacity <= 0 {
		capacity = 200 // safe default
	}
	return &Ring{
		items:    make([]*domain.Snapshot, capacity),
		capacity: capacity,
		logger:   logger,
	}
}

// Enqueue adds a Snapshot to the buffer. If the buffer is full,
// the oldest entry is discarded and the new one takes its place.
func (r *Ring) Enqueue(s *domain.Snapshot) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.count == r.capacity {
		// Drop oldest: advance tail past the overwritten slot.
		r.tail = (r.tail + 1) % r.capacity
		r.count--
		r.droppedTotal++
		r.logger.Warn("ring buffer full — oldest snapshot dropped",
			slog.Int64("total_dropped", r.droppedTotal),
			slog.Int("capacity", r.capacity),
		)
	}

	r.items[r.head] = s
	r.head = (r.head + 1) % r.capacity
	r.count++
}

// Dequeue removes and returns the oldest Snapshot.
// Returns (nil, false) if the buffer is empty.
func (r *Ring) Dequeue() (*domain.Snapshot, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.count == 0 {
		return nil, false
	}

	s := r.items[r.tail]
	r.items[r.tail] = nil // release reference for GC
	r.tail = (r.tail + 1) % r.capacity
	r.count--
	return s, true
}

// Len returns the current number of buffered Snapshots.
func (r *Ring) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.count
}

// DroppedTotal returns the cumulative number of Snapshots dropped due to overflow.
func (r *Ring) DroppedTotal() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.droppedTotal
}
