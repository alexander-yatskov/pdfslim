package optimize

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMemoryBudgetRejectsOversizedImage(t *testing.T) {
	b := NewMemoryBudget(100)
	if acquired, err := b.acquire(context.Background(), 101); err != nil || acquired {
		t.Fatal("oversized image acquired memory budget")
	}
	if acquired, err := b.acquire(context.Background(), 100); err != nil || !acquired {
		t.Fatal("image fitting the budget was rejected")
	}
	b.release(100)
	if acquired, err := b.acquire(context.Background(), 100); err != nil || !acquired {
		t.Fatal("released budget was not available")
	}
}

func TestMemoryBudgetAcquireStopsOnCancellation(t *testing.T) {
	b := NewMemoryBudget(100)
	if acquired, err := b.acquire(context.Background(), 100); err != nil || !acquired {
		t.Fatal("initial acquisition failed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := b.acquire(ctx, 100)
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("acquire error = %v, want context cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("acquire did not stop after cancellation")
	}
	b.release(100)
}

func TestMemoryBudgetRejectsCanceledAcquisitionWhenCapacityIsAvailable(t *testing.T) {
	b := NewMemoryBudget(100)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if acquired, err := b.acquire(ctx, 100); !errors.Is(err, context.Canceled) || acquired {
		t.Fatalf("acquire = %v, %v; want false, context cancellation", acquired, err)
	}
}

func TestEstimatedImageMemory(t *testing.T) {
	if got := estimatedImageMemory(100, 200, false); got != 100*200*12 {
		t.Fatalf("opaque estimate = %d", got)
	}
	if got := estimatedImageMemory(100, 200, true); got != 100*200*16 {
		t.Fatalf("transparent estimate = %d", got)
	}
}
