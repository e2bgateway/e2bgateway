package cache

import (
	"context"
	"fmt"
	"time"
)

// RedisCacheClient is the interface needed for Redis-backed caching.
type RedisCacheClient interface {
	Set(ctx context.Context, key string, value interface{}, expiration time.Duration) error
	Get(ctx context.Context, key string) (string, error)
	Del(ctx context.Context, keys ...string) error
	Exists(ctx context.Context, keys ...string) (int64, error)
}

// RedisCache is a distributed cache backed by Redis.
type RedisCache struct {
	client     RedisCacheClient
	prefix     string
	defaultTTL time.Duration
}

// NewRedisCache creates a new Redis-backed cache.
func NewRedisCache(client RedisCacheClient, prefix string, defaultTTL time.Duration) *RedisCache {
	if defaultTTL <= 0 {
		defaultTTL = 30 * time.Second
	}
	return &RedisCache{
		client:     client,
		prefix:     prefix,
		defaultTTL: defaultTTL,
	}
}

// Get retrieves a value from the cache. Returns (value, true, nil) on hit,
// ("", false, nil) on miss, or ("", false, err) on error.
func (rc *RedisCache) Get(ctx context.Context, key string) (string, bool, error) {
	redisKey := rc.prefixed(key)
	val, err := rc.client.Get(ctx, redisKey)
	if err != nil {
		return "", false, err
	}
	// A nil/empty result typically means key not found.
	// Redis returns "nil bulk string" for missing keys; go-redis returns redis.Nil error.
	// We treat empty string as a miss for simplicity.
	if val == "" {
		return "", false, nil
	}
	return val, true, nil
}

// Set stores a value with the default TTL.
func (rc *RedisCache) Set(ctx context.Context, key string, value string) error {
	return rc.SetWithTTL(ctx, key, value, rc.defaultTTL)
}

// SetWithTTL stores a value with a custom TTL.
func (rc *RedisCache) SetWithTTL(ctx context.Context, key string, value string, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = rc.defaultTTL
	}
	redisKey := rc.prefixed(key)
	return rc.client.Set(ctx, redisKey, value, ttl)
}

// Delete removes a key from the cache.
func (rc *RedisCache) Delete(ctx context.Context, key string) error {
	redisKey := rc.prefixed(key)
	return rc.client.Del(ctx, redisKey)
}

// Exists checks if a key exists in the cache.
func (rc *RedisCache) Exists(ctx context.Context, key string) (bool, error) {
	redisKey := rc.prefixed(key)
	n, err := rc.client.Exists(ctx, redisKey)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func (rc *RedisCache) prefixed(key string) string {
	if rc.prefix == "" {
		return key
	}
	return fmt.Sprintf("%s:%s", rc.prefix, key)
}
