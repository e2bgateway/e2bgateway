package cache

import (
	"testing"
	"time"
)

func TestCache_SetAndGet(t *testing.T) {
	c := New(10, time.Minute)

	c.Set("key1", "value1")
	c.Set("key2", 42)

	v, ok := c.Get("key1")
	if !ok || v != "value1" {
		t.Errorf("expected 'value1', got %v (ok=%v)", v, ok)
	}

	v, ok = c.Get("key2")
	if !ok || v != 42 {
		t.Errorf("expected 42, got %v (ok=%v)", v, ok)
	}
}

func TestCache_Miss(t *testing.T) {
	c := New(10, time.Minute)

	_, ok := c.Get("nonexistent")
	if ok {
		t.Error("expected miss for nonexistent key")
	}
}

func TestCache_Expiry(t *testing.T) {
	c := New(10, 50*time.Millisecond)

	c.Set("key1", "value1")

	v, ok := c.Get("key1")
	if !ok || v != "value1" {
		t.Fatalf("expected value before expiry")
	}

	time.Sleep(60 * time.Millisecond)

	_, ok = c.Get("key1")
	if ok {
		t.Error("expected miss after expiry")
	}
}

func TestCache_Eviction(t *testing.T) {
	c := New(3, time.Minute)

	c.Set("a", 1)
	c.Set("b", 2)
	c.Set("c", 3)
	c.Set("d", 4) // Should evict "a"

	_, ok := c.Get("a")
	if ok {
		t.Error("expected 'a' to be evicted")
	}

	v, ok := c.Get("d")
	if !ok || v != 4 {
		t.Errorf("expected 'd' to be present")
	}
}

func TestCache_LRU(t *testing.T) {
	c := New(3, time.Minute)

	c.Set("a", 1)
	c.Set("b", 2)
	c.Set("c", 3)

	// Access "a" to make it most recently used
	c.Get("a")

	// Add "d" — should evict "b" (least recently used)
	c.Set("d", 4)

	_, ok := c.Get("b")
	if ok {
		t.Error("expected 'b' to be evicted (LRU)")
	}

	_, ok = c.Get("a")
	if !ok {
		t.Error("expected 'a' to survive (recently accessed)")
	}
}

func TestCache_Delete(t *testing.T) {
	c := New(10, time.Minute)

	c.Set("key1", "value1")
	c.Delete("key1")

	_, ok := c.Get("key1")
	if ok {
		t.Error("expected miss after delete")
	}
}

func TestCache_Len(t *testing.T) {
	c := New(10, time.Minute)

	c.Set("a", 1)
	c.Set("b", 2)
	if c.Len() != 2 {
		t.Errorf("expected len 2, got %d", c.Len())
	}
}

func TestCache_Cleanup(t *testing.T) {
	c := New(10, 50*time.Millisecond)

	c.Set("a", 1)
	c.Set("b", 2)

	time.Sleep(60 * time.Millisecond)

	c.Set("c", 3) // Not expired

	c.Cleanup()

	if c.Len() != 1 {
		t.Errorf("expected len 1 after cleanup, got %d", c.Len())
	}
}

func TestCache_CustomTTL(t *testing.T) {
	c := New(10, time.Minute)

	c.SetWithTTL("short", "val", 50*time.Millisecond)
	c.Set("long", "val")

	time.Sleep(60 * time.Millisecond)

	_, ok := c.Get("short")
	if ok {
		t.Error("expected 'short' to expire")
	}

	_, ok = c.Get("long")
	if !ok {
		t.Error("expected 'long' to survive")
	}
}

func TestCache_Update(t *testing.T) {
	c := New(10, time.Minute)

	c.Set("key", "v1")
	c.Set("key", "v2")

	v, ok := c.Get("key")
	if !ok || v != "v2" {
		t.Errorf("expected 'v2', got %v", v)
	}

	if c.Len() != 1 {
		t.Errorf("expected len 1 after update, got %d", c.Len())
	}
}
