// Package ratelimit provides a token bucket rate limiter.
package ratelimit

import (
	"sync"
	"time"
)

// Limiter is a token bucket rate limiter.
type Limiter struct {
	mu       sync.Mutex
	rate     float64   // tokens per second
	burst    int       // max tokens
	tokens   float64   // current tokens
	lastTime time.Time // last refill time
}

// New creates a new Limiter with the given rate (tokens/sec) and burst size.
func New(rate float64, burst int) *Limiter {
	if rate <= 0 {
		rate = 1
	}
	if burst <= 0 {
		burst = 1
	}
	return &Limiter{
		rate:     rate,
		burst:    burst,
		tokens:   float64(burst),
		lastTime: time.Now(),
	}
}

// Allow checks if a request is allowed (consumes one token).
func (l *Limiter) Allow() bool {
	return l.AllowN(1)
}

// AllowN checks if n tokens are available.
func (l *Limiter) AllowN(n int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.refill()

	if l.tokens < float64(n) {
		return false
	}
	l.tokens -= float64(n)
	return true
}

// Tokens returns the current number of available tokens.
func (l *Limiter) Tokens() float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.refill()
	return l.tokens
}

func (l *Limiter) refill() {
	now := time.Now()
	elapsed := now.Sub(l.lastTime).Seconds()
	l.tokens += elapsed * l.rate
	if l.tokens > float64(l.burst) {
		l.tokens = float64(l.burst)
	}
	l.lastTime = now
}

// RateLimiter manages per-key rate limiting.
type RateLimiter struct {
	mu       sync.RWMutex
	limiters map[string]*Limiter
	rate     float64
	burst    int
}

// NewRateLimiter creates a rate limiter that manages per-key limiters.
func NewRateLimiter(rate float64, burst int) *RateLimiter {
	return &RateLimiter{
		limiters: make(map[string]*Limiter),
		rate:     rate,
		burst:    burst,
	}
}

// Allow checks if a request for the given key is allowed.
func (rl *RateLimiter) Allow(key string) bool {
	l := rl.getLimiter(key)
	return l.Allow()
}

func (rl *RateLimiter) getLimiter(key string) *Limiter {
	rl.mu.RLock()
	l, ok := rl.limiters[key]
	rl.mu.RUnlock()
	if ok {
		return l
	}

	rl.mu.Lock()
	defer rl.mu.Unlock()

	// Double-check after acquiring write lock
	if l, ok := rl.limiters[key]; ok {
		return l
	}

	l = New(rl.rate, rl.burst)
	rl.limiters[key] = l
	return l
}

// Cleanup removes limiters that haven't been used recently.
func (rl *RateLimiter) Cleanup(maxAge time.Duration) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	cutoff := time.Now().Add(-maxAge)
	for key, l := range rl.limiters {
		l.mu.Lock()
		if l.lastTime.Before(cutoff) {
			delete(rl.limiters, key)
		}
		l.mu.Unlock()
	}
}
