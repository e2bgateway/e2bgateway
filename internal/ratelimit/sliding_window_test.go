package ratelimit

import (
	"testing"
	"time"
)

func TestSlidingWindow_Allow(t *testing.T) {
	sw := NewSlidingWindow(5, time.Second)

	// Should allow up to limit
	for i := 0; i < 5; i++ {
		if !sw.Allow() {
			t.Errorf("expected allow on request %d", i)
		}
	}

	// 6th should be denied
	if sw.Allow() {
		t.Error("expected deny after limit exhausted")
	}
}

func TestSlidingWindow_AllowN(t *testing.T) {
	sw := NewSlidingWindow(10, time.Second)

	if !sw.AllowN(5) {
		t.Error("expected allow for 5 requests")
	}
	if !sw.AllowN(5) {
		t.Error("expected allow for another 5 requests")
	}
	if sw.AllowN(1) {
		t.Error("expected deny when limit exhausted")
	}
}

func TestSlidingWindow_Count(t *testing.T) {
	sw := NewSlidingWindow(10, time.Second)

	if sw.Count() != 0 {
		t.Errorf("expected count 0 initially, got %d", sw.Count())
	}

	sw.Allow()
	sw.Allow()
	sw.Allow()

	if sw.Count() != 3 {
		t.Errorf("expected count 3, got %d", sw.Count())
	}
}

func TestSlidingWindow_WindowSliding(t *testing.T) {
	// Use a short window so we can test sliding behavior
	sw := NewSlidingWindow(5, 200*time.Millisecond)

	// Exhaust all 5 requests
	for i := 0; i < 5; i++ {
		if !sw.Allow() {
			t.Errorf("expected allow on request %d", i)
		}
	}
	if sw.Allow() {
		t.Error("expected deny after limit exhausted")
	}

	// Wait for the full window to pass — counts should reset
	time.Sleep(250 * time.Millisecond)

	// Now should be able to make requests again
	if !sw.Allow() {
		t.Error("expected allow after window slides past")
	}
}

func TestSlidingWindowRateLimiter_PerKey(t *testing.T) {
	swrl := NewSlidingWindowRateLimiter(2, time.Second)

	if !swrl.Allow("a") {
		t.Error("expected allow for a")
	}
	if !swrl.Allow("a") {
		t.Error("expected allow for a")
	}
	if swrl.Allow("a") {
		t.Error("expected deny for a")
	}

	// Key "b" is independent
	if !swrl.Allow("b") {
		t.Error("expected allow for b")
	}
}

func TestSlidingWindowRateLimiter_Cleanup(t *testing.T) {
	swrl := NewSlidingWindowRateLimiter(5, 100*time.Millisecond)

	swrl.Allow("old-key")
	time.Sleep(150 * time.Millisecond)
	swrl.Allow("new-key")

	swrl.Cleanup(120 * time.Millisecond)

	swrl.mu.RLock()
	_, hasOld := swrl.limiters["old-key"]
	_, hasNew := swrl.limiters["new-key"]
	swrl.mu.RUnlock()

	if hasOld {
		t.Error("expected old-key to be cleaned up")
	}
	if !hasNew {
		t.Error("expected new-key to survive")
	}
}

func TestSlidingWindow_InvalidParams(t *testing.T) {
	// Should not panic with invalid params
	sw := NewSlidingWindow(0, 0)
	if sw.limit != 1 {
		t.Errorf("expected limit 1 for zero input, got %d", sw.limit)
	}
}
