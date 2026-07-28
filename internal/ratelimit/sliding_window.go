package ratelimit

import (
	"sync"
	"time"
)

// SlidingWindowLimiter uses a sliding window algorithm.
// It divides time into small windows and counts requests in each.
type SlidingWindowLimiter struct {
	mu         sync.Mutex
	windowSize time.Duration
	windows    int         // number of windows to track
	counts     []int       // request counts per window
	timestamps []time.Time // start time of each window
	limit      int         // max requests per full window span
}

// NewSlidingWindow creates a new SlidingWindowLimiter.
// limit is the max number of requests allowed within the window duration.
// window is the total time span over which the limit applies.
func NewSlidingWindow(limit int, window time.Duration) *SlidingWindowLimiter {
	if limit <= 0 {
		limit = 1
	}
	// Divide the window into 10 sub-windows for granularity
	numWindows := 10
	subWindow := window / time.Duration(numWindows)
	if subWindow <= 0 {
		subWindow = time.Millisecond
		numWindows = int(window / subWindow)
		if numWindows <= 0 {
			numWindows = 1
		}
	}

	now := time.Now()
	counts := make([]int, numWindows)
	timestamps := make([]time.Time, numWindows)
	for i := range timestamps {
		timestamps[i] = now
	}

	return &SlidingWindowLimiter{
		windowSize: subWindow,
		windows:    numWindows,
		counts:     counts,
		timestamps: timestamps,
		limit:      limit,
	}
}

// Allow checks if a single request is allowed.
func (sw *SlidingWindowLimiter) Allow() bool {
	return sw.AllowN(1)
}

// AllowN checks if n requests are allowed.
func (sw *SlidingWindowLimiter) AllowN(n int) bool {
	sw.mu.Lock()
	defer sw.mu.Unlock()

	sw.advance()

	total := sw.totalCount()
	if total+n > sw.limit {
		return false
	}

	// Add n requests to the current (latest) window
	sw.counts[sw.currentWindow()] += n
	return true
}

// Count returns the current request count within the active window span.
func (sw *SlidingWindowLimiter) Count() int {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	sw.advance()
	return sw.totalCount()
}

// advance rotates windows forward to match the current time. Must be called with mu held.
func (sw *SlidingWindowLimiter) advance() {
	now := time.Now()
	current := sw.currentWindow()

	// Determine how many sub-windows have passed since the latest window started
	latestStart := sw.timestamps[current]
	elapsed := now.Sub(latestStart)
	if elapsed < sw.windowSize {
		return
	}

	steps := int(elapsed / sw.windowSize)
	if steps > sw.windows {
		steps = sw.windows
	}

	for i := 0; i < steps; i++ {
		current = (current + 1) % sw.windows
		sw.counts[current] = 0
		sw.timestamps[current] = latestStart.Add(time.Duration(i+1) * sw.windowSize)
	}
}

// currentWindow returns the index of the most recent window. Must be called with mu held.
func (sw *SlidingWindowLimiter) currentWindow() int {
	// Find the window with the latest timestamp
	latest := 0
	for i := 1; i < sw.windows; i++ {
		if sw.timestamps[i].After(sw.timestamps[latest]) {
			latest = i
		}
	}
	return latest
}

// totalCount returns the sum of counts across all windows within the active span.
// Must be called with mu held (and after advance).
func (sw *SlidingWindowLimiter) totalCount() int {
	now := time.Now()
	cutoff := now.Add(-time.Duration(sw.windows) * sw.windowSize)
	total := 0
	for i := 0; i < sw.windows; i++ {
		if sw.timestamps[i].After(cutoff) {
			total += sw.counts[i]
		}
	}
	return total
}

// SlidingWindowRateLimiter manages per-key sliding window rate limiters.
type SlidingWindowRateLimiter struct {
	mu       sync.RWMutex
	limiters map[string]*SlidingWindowLimiter
	limit    int
	window   time.Duration
}

// NewSlidingWindowRateLimiter creates a per-key sliding window rate limiter.
func NewSlidingWindowRateLimiter(limit int, window time.Duration) *SlidingWindowRateLimiter {
	return &SlidingWindowRateLimiter{
		limiters: make(map[string]*SlidingWindowLimiter),
		limit:    limit,
		window:   window,
	}
}

// Allow checks if a request for the given key is allowed.
func (swrl *SlidingWindowRateLimiter) Allow(key string) bool {
	l := swrl.getLimiter(key)
	return l.Allow()
}

func (swrl *SlidingWindowRateLimiter) getLimiter(key string) *SlidingWindowLimiter {
	swrl.mu.RLock()
	l, ok := swrl.limiters[key]
	swrl.mu.RUnlock()
	if ok {
		return l
	}

	swrl.mu.Lock()
	defer swrl.mu.Unlock()

	if l, ok := swrl.limiters[key]; ok {
		return l
	}

	l = NewSlidingWindow(swrl.limit, swrl.window)
	swrl.limiters[key] = l
	return l
}

// Cleanup removes limiters whose latest window timestamp is older than maxAge.
func (swrl *SlidingWindowRateLimiter) Cleanup(maxAge time.Duration) {
	swrl.mu.Lock()
	defer swrl.mu.Unlock()

	cutoff := time.Now().Add(-maxAge)
	for key, l := range swrl.limiters {
		l.mu.Lock()
		latest := l.timestamps[l.currentWindow()]
		l.mu.Unlock()

		if latest.Before(cutoff) {
			delete(swrl.limiters, key)
		}
	}
}
