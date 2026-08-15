package optimize

import (
	"context"
	"math"
	"sync"
)

// MemoryBudget limits the estimated memory used by concurrent image decodes.
// It does not include the PDF object graph or memory owned by the Go runtime.
type MemoryBudget struct {
	mu        sync.Mutex
	changed   chan struct{}
	capacity  int64
	available int64
}

func NewMemoryBudget(capacity int64) *MemoryBudget {
	return &MemoryBudget{capacity: capacity, available: capacity, changed: make(chan struct{})}
}

func (b *MemoryBudget) acquire(ctx context.Context, bytes int64) (bool, error) {
	if b == nil || b.capacity <= 0 {
		return true, nil
	}
	if bytes <= 0 || bytes > b.capacity {
		return false, nil
	}
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		b.mu.Lock()
		if bytes <= b.available {
			b.available -= bytes
			b.mu.Unlock()
			return true, nil
		}
		changed := b.changed
		b.mu.Unlock()
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-changed:
		}
	}
}

func (b *MemoryBudget) release(bytes int64) {
	if b == nil || b.capacity <= 0 || bytes <= 0 || bytes > b.capacity {
		return
	}
	b.mu.Lock()
	b.available += bytes
	close(b.changed)
	b.changed = make(chan struct{})
	b.mu.Unlock()
}

func estimatedImageMemory(width, height int, transparent bool) int64 {
	if width <= 0 || height <= 0 || int64(width) > math.MaxInt64/int64(height) {
		return math.MaxInt64
	}
	bytesPerPixel := int64(12) // decoded source, resized image, and encoder work
	if transparent {
		bytesPerPixel = 16 // color image plus decoded and resized soft mask
	}
	pixels := int64(width) * int64(height)
	if pixels > math.MaxInt64/bytesPerPixel {
		return math.MaxInt64
	}
	return pixels * bytesPerPixel
}
