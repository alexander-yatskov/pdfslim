package optimize

import (
	"math"
	"sync"
)

// MemoryBudget limits the estimated memory used by concurrent image decodes.
// It does not include the PDF object graph or memory owned by the Go runtime.
type MemoryBudget struct {
	mu        sync.Mutex
	cond      *sync.Cond
	capacity  int64
	available int64
}

func NewMemoryBudget(capacity int64) *MemoryBudget {
	b := &MemoryBudget{capacity: capacity, available: capacity}
	b.cond = sync.NewCond(&b.mu)
	return b
}

func (b *MemoryBudget) acquire(bytes int64) bool {
	if b == nil || b.capacity <= 0 {
		return true
	}
	if bytes <= 0 || bytes > b.capacity {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for bytes > b.available {
		b.cond.Wait()
	}
	b.available -= bytes
	return true
}

func (b *MemoryBudget) release(bytes int64) {
	if b == nil || b.capacity <= 0 || bytes <= 0 || bytes > b.capacity {
		return
	}
	b.mu.Lock()
	b.available += bytes
	b.mu.Unlock()
	b.cond.Broadcast()
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
