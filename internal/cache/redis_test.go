package cache

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// mockRedisCacheClient implements RedisCacheClient for testing.
type mockRedisCacheClient struct {
	mu    sync.Mutex
	store map[string]string
}

func newMockRedisCacheClient() *mockRedisCacheClient {
	return &mockRedisCacheClient{
		store: make(map[string]string),
	}
}

func (m *mockRedisCacheClient) Set(_ context.Context, key string, value interface{}, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.store[key] = fmt.Sprintf("%v", value)
	return nil
}

func (m *mockRedisCacheClient) Get(_ context.Context, key string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	val, ok := m.store[key]
	if !ok {
		return "", nil
	}
	return val, nil
}

func (m *mockRedisCacheClient) Del(_ context.Context, keys ...string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, k := range keys {
		delete(m.store, k)
	}
	return nil
}

func (m *mockRedisCacheClient) Exists(_ context.Context, keys ...string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var count int64
	for _, k := range keys {
		if _, ok := m.store[k]; ok {
			count++
		}
	}
	return count, nil
}

// errMockRedisCacheClient always returns errors.
type errMockRedisCacheClient struct{}

func (e *errMockRedisCacheClient) Set(_ context.Context, _ string, _ interface{}, _ time.Duration) error {
	return errors.New("set error")
}
func (e *errMockRedisCacheClient) Get(_ context.Context, _ string) (string, error) {
	return "", errors.New("get error")
}
func (e *errMockRedisCacheClient) Del(_ context.Context, _ ...string) error {
	return errors.New("del error")
}
func (e *errMockRedisCacheClient) Exists(_ context.Context, _ ...string) (int64, error) {
	return 0, errors.New("exists error")
}

func TestRedisCache_SetAndGet(t *testing.T) {
	mock := newMockRedisCacheClient()
	rc := NewRedisCache(mock, "cache", time.Minute)
	ctx := context.Background()

	if err := rc.Set(ctx, "key1", "value1"); err != nil {
		t.Fatalf("unexpected set error: %v", err)
	}

	val, found, err := rc.Get(ctx, "key1")
	if err != nil {
		t.Fatalf("unexpected get error: %v", err)
	}
	if !found {
		t.Error("expected key to be found")
	}
	if val != "value1" {
		t.Errorf("expected 'value1', got '%s'", val)
	}
}

func TestRedisCache_Miss(t *testing.T) {
	mock := newMockRedisCacheClient()
	rc := NewRedisCache(mock, "cache", time.Minute)
	ctx := context.Background()

	val, found, err := rc.Get(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Error("expected miss")
	}
	if val != "" {
		t.Errorf("expected empty string on miss, got '%s'", val)
	}
}

func TestRedisCache_SetWithTTL(t *testing.T) {
	mock := newMockRedisCacheClient()
	rc := NewRedisCache(mock, "cache", time.Minute)
	ctx := context.Background()

	err := rc.SetWithTTL(ctx, "key1", "value1", 5*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	val, found, err := rc.Get(ctx, "key1")
	if err != nil || !found || val != "value1" {
		t.Errorf("expected value1 to be found, got val=%s found=%v err=%v", val, found, err)
	}
}

func TestRedisCache_Delete(t *testing.T) {
	mock := newMockRedisCacheClient()
	rc := NewRedisCache(mock, "cache", time.Minute)
	ctx := context.Background()

	_ = rc.Set(ctx, "key1", "value1")
	if err := rc.Delete(ctx, "key1"); err != nil {
		t.Fatalf("unexpected delete error: %v", err)
	}

	_, found, _ := rc.Get(ctx, "key1")
	if found {
		t.Error("expected miss after delete")
	}
}

func TestRedisCache_Exists(t *testing.T) {
	mock := newMockRedisCacheClient()
	rc := NewRedisCache(mock, "cache", time.Minute)
	ctx := context.Background()

	exists, err := rc.Exists(ctx, "key1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exists {
		t.Error("expected key not to exist")
	}

	_ = rc.Set(ctx, "key1", "value1")
	exists, err = rc.Exists(ctx, "key1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !exists {
		t.Error("expected key to exist")
	}
}

func TestRedisCache_Prefix(t *testing.T) {
	mock := newMockRedisCacheClient()
	rc := NewRedisCache(mock, "myprefix", time.Minute)
	ctx := context.Background()

	_ = rc.Set(ctx, "key1", "value1")

	// Verify the key is stored with prefix in the mock
	mock.mu.Lock()
	_, hasPrefixed := mock.store["myprefix:key1"]
	_, hasRaw := mock.store["key1"]
	mock.mu.Unlock()

	if !hasPrefixed {
		t.Error("expected key to be stored with prefix")
	}
	if hasRaw {
		t.Error("expected raw key without prefix not to exist")
	}
}

func TestRedisCache_NoPrefix(t *testing.T) {
	mock := newMockRedisCacheClient()
	rc := NewRedisCache(mock, "", time.Minute)
	ctx := context.Background()

	_ = rc.Set(ctx, "key1", "value1")

	mock.mu.Lock()
	_, hasKey := mock.store["key1"]
	mock.mu.Unlock()

	if !hasKey {
		t.Error("expected key without prefix when prefix is empty")
	}
}

func TestRedisCache_Errors(t *testing.T) {
	mock := &errMockRedisCacheClient{}
	rc := NewRedisCache(mock, "cache", time.Minute)
	ctx := context.Background()

	if err := rc.Set(ctx, "k", "v"); err == nil {
		t.Error("expected set error")
	}
	if err := rc.SetWithTTL(ctx, "k", "v", time.Second); err == nil {
		t.Error("expected setwithttl error")
	}
	if _, _, err := rc.Get(ctx, "k"); err == nil {
		t.Error("expected get error")
	}
	if err := rc.Delete(ctx, "k"); err == nil {
		t.Error("expected delete error")
	}
	if _, err := rc.Exists(ctx, "k"); err == nil {
		t.Error("expected exists error")
	}
}

func TestRedisCache_DefaultTTL(t *testing.T) {
	mock := newMockRedisCacheClient()
	rc := NewRedisCache(mock, "cache", 0)
	if rc.defaultTTL != 30*time.Second {
		t.Errorf("expected default TTL 30s for zero input, got %v", rc.defaultTTL)
	}
}
