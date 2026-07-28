package ratelimit

import (
	"testing"
	"time"
)

func TestLimiter_Allow(t *testing.T) {
	l := New(10, 5) // 10 tokens/sec, burst 5

	// Should allow up to burst
	for i := 0; i < 5; i++ {
		if !l.Allow() {
			t.Errorf("expected allow on request %d", i)
		}
	}

	// 6th should be denied
	if l.Allow() {
		t.Error("expected deny after burst exhausted")
	}
}

func TestLimiter_Refill(t *testing.T) {
	l := New(100, 5) // 100 tokens/sec

	// Exhaust tokens
	for i := 0; i < 5; i++ {
		l.Allow()
	}

	if l.Allow() {
		t.Error("expected deny")
	}

	// Wait for refill
	time.Sleep(60 * time.Millisecond) // Should refill ~6 tokens

	if !l.Allow() {
		t.Error("expected allow after refill")
	}
}

func TestLimiter_AllowN(t *testing.T) {
	l := New(10, 10)

	if !l.AllowN(5) {
		t.Error("expected allow for 5 tokens")
	}
	if !l.AllowN(5) {
		t.Error("expected allow for another 5 tokens")
	}
	if l.AllowN(1) {
		t.Error("expected deny when exhausted")
	}
}

func TestLimiter_Tokens(t *testing.T) {
	l := New(10, 10)

	tokens := l.Tokens()
	if tokens != 10 {
		t.Errorf("expected 10 tokens, got %f", tokens)
	}

	l.Allow()
	tokens = l.Tokens()
	if tokens < 8.9 || tokens > 9.1 {
		t.Errorf("expected ~9 tokens, got %f", tokens)
	}
}

func TestRateLimiter_PerKey(t *testing.T) {
	rl := NewRateLimiter(10, 2)

	// Key "a" gets 2 requests
	if !rl.Allow("a") {
		t.Error("expected allow for a")
	}
	if !rl.Allow("a") {
		t.Error("expected allow for a")
	}
	if rl.Allow("a") {
		t.Error("expected deny for a")
	}

	// Key "b" is independent
	if !rl.Allow("b") {
		t.Error("expected allow for b")
	}
}

func TestRateLimiter_Cleanup(t *testing.T) {
	rl := NewRateLimiter(10, 5)

	rl.Allow("old-key")
	time.Sleep(60 * time.Millisecond)
	rl.Allow("new-key")

	rl.Cleanup(50 * time.Millisecond)

	rl.mu.RLock()
	_, hasOld := rl.limiters["old-key"]
	_, hasNew := rl.limiters["new-key"]
	rl.mu.RUnlock()

	if hasOld {
		t.Error("expected old-key to be cleaned up")
	}
	if !hasNew {
		t.Error("expected new-key to survive")
	}
}
