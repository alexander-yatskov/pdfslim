package optimize

import "testing"

func TestMemoryBudgetRejectsOversizedImage(t *testing.T) {
	b := NewMemoryBudget(100)
	if b.acquire(101) {
		t.Fatal("oversized image acquired memory budget")
	}
	if !b.acquire(100) {
		t.Fatal("image fitting the budget was rejected")
	}
	b.release(100)
	if !b.acquire(100) {
		t.Fatal("released budget was not available")
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
