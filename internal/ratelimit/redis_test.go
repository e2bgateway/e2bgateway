package ratelimit

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// mockRedisClient implements RedisClient for testing.
type mockRedisClient struct {
	mu   sync.Mutex
	data map[string]map[string]float64
	eval func(ctx context.Context, script string, keys []string, args ...interface{}) (interface{}, error)
}

func newMockRedisClient() *mockRedisClient {
	return &mockRedisClient{
		data: make(map[string]map[string]float64),
	}
}

func (m *mockRedisClient) Eval(ctx context.Context, script string, keys []string, args ...interface{}) (interface{}, error) {
	if m.eval != nil {
		return m.eval(ctx, script, keys, args...)
	}
	// Default mock: simulate token bucket in-memory
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(keys) == 0 {
		return nil, errors.New("no keys")
	}
	key := keys[0]
	rate := toFloat64(args[0])
	burst := int(toFloat64(args[1]))
	requested := int(toFloat64(args[2]))
	now := toFloat64(args[3])

	bucket, ok := m.data[key]
	if !ok {
		bucket = map[string]float64{
			"tokens":    float64(burst),
			"last_time": now,
		}
		m.data[key] = bucket
	}

	tokens := bucket["tokens"]
	lastTime := bucket["last_time"]

	elapsed := now - lastTime
	if elapsed < 0 {
		elapsed = 0
	}
	tokens += elapsed * rate
	if tokens > float64(burst) {
		tokens = float64(burst)
	}

	if tokens >= float64(requested) {
		tokens -= float64(requested)
		bucket["tokens"] = tokens
		bucket["last_time"] = now
		return int64(1), nil
	}

	bucket["tokens"] = tokens
	bucket["last_time"] = now
	return int64(0), nil
}

func (m *mockRedisClient) Set(_ context.Context, _ string, _ interface{}, _ time.Duration) error {
	return nil
}

func (m *mockRedisClient) Get(_ context.Context, _ string) (string, error) {
	return "", nil
}

func (m *mockRedisClient) Del(_ context.Context, _ ...string) error {
	return nil
}

func toFloat64(v interface{}) float64 {
	switch val := v.(type) {
	case float64:
		return val
	case int:
		return float64(val)
	case int64:
		return float64(val)
	default:
		return 0
	}
}

func TestRedisLimiter_Allow(t *testing.T) {
	mock := newMockRedisClient()
	rl := NewRedisLimiter(mock, "test", 10, 5)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		if !rl.Allow(ctx, "key1") {
			t.Errorf("expected allow on request %d", i)
		}
	}

	if rl.Allow(ctx, "key1") {
		t.Error("expected deny after burst exhausted")
	}
}

func TestRedisLimiter_AllowN(t *testing.T) {
	mock := newMockRedisClient()
	rl := NewRedisLimiter(mock, "test", 10, 10)
	ctx := context.Background()

	if !rl.AllowN(ctx, "key1", 5) {
		t.Error("expected allow for 5 tokens")
	}
	if !rl.AllowN(ctx, "key1", 5) {
		t.Error("expected allow for another 5 tokens")
	}
	if rl.AllowN(ctx, "key1", 1) {
		t.Error("expected deny when exhausted")
	}
}

func TestRedisLimiter_PerKey(t *testing.T) {
	mock := newMockRedisClient()
	rl := NewRedisLimiter(mock, "test", 10, 2)
	ctx := context.Background()

	if !rl.Allow(ctx, "a") {
		t.Error("expected allow for a")
	}
	if !rl.Allow(ctx, "a") {
		t.Error("expected allow for a")
	}
	if rl.Allow(ctx, "a") {
		t.Error("expected deny for a")
	}

	if !rl.Allow(ctx, "b") {
		t.Error("expected allow for b")
	}
}

func TestRedisLimiter_ErrorHandling(t *testing.T) {
	mock := newMockRedisClient()
	mock.eval = func(_ context.Context, _ string, _ []string, _ ...interface{}) (interface{}, error) {
		return nil, errors.New("redis error")
	}
	rl := NewRedisLimiter(mock, "test", 10, 5)
	ctx := context.Background()

	if rl.Allow(ctx, "key1") {
		t.Error("expected deny on redis error")
	}
}

func TestRedisLimiter_InvalidParams(t *testing.T) {
	mock := newMockRedisClient()
	rl := NewRedisLimiter(mock, "test", 0, 0)
	if rl.rate != 1 {
		t.Errorf("expected rate 1, got %f", rl.rate)
	}
	if rl.burst != 1 {
		t.Errorf("expected burst 1, got %d", rl.burst)
	}
}
