package ratelimit

import (
	"context"
	"fmt"
	"time"
)

// RedisClient is the interface needed for Redis-backed rate limiting.
type RedisClient interface {
	Eval(ctx context.Context, script string, keys []string, args ...interface{}) (interface{}, error)
	Set(ctx context.Context, key string, value interface{}, expiration time.Duration) error
	Get(ctx context.Context, key string) (string, error)
	Del(ctx context.Context, keys ...string) error
}

// RedisLimiter is a distributed rate limiter backed by Redis.
// It uses a Lua script for atomic token bucket operations.
type RedisLimiter struct {
	client RedisClient
	prefix string
	rate   float64
	burst  int
}

// tokenBucketScript is a Lua script that atomically performs token bucket operations.
// KEYS[1] = the rate limit key
// ARGV[1] = rate (tokens per second)
// ARGV[2] = burst (max tokens)
// ARGV[3] = requested tokens
// ARGV[4] = current unix timestamp in seconds (with fractional part)
// Returns: 1 if allowed, 0 if denied
const tokenBucketScript = `
local key = KEYS[1]
local rate = tonumber(ARGV[1])
local burst = tonumber(ARGV[2])
local requested = tonumber(ARGV[3])
local now = tonumber(ARGV[4])

local bucket = redis.call('HMGET', key, 'tokens', 'last_time')
local tokens = tonumber(bucket[1])
local last_time = tonumber(bucket[2])

if tokens == nil then
    tokens = burst
    last_time = now
end

local elapsed = now - last_time
if elapsed < 0 then
    elapsed = 0
end

tokens = tokens + elapsed * rate
if tokens > burst then
    tokens = burst
end

if tokens >= requested then
    tokens = tokens - requested
    redis.call('HMSET', key, 'tokens', tokens, 'last_time', now)
    redis.call('EXPIRE', key, math.ceil(burst / rate) + 1)
    return 1
else
    redis.call('HMSET', key, 'tokens', tokens, 'last_time', now)
    redis.call('EXPIRE', key, math.ceil(burst / rate) + 1)
    return 0
end
`

// NewRedisLimiter creates a new Redis-backed rate limiter.
func NewRedisLimiter(client RedisClient, prefix string, rate float64, burst int) *RedisLimiter {
	if rate <= 0 {
		rate = 1
	}
	if burst <= 0 {
		burst = 1
	}
	return &RedisLimiter{
		client: client,
		prefix: prefix,
		rate:   rate,
		burst:  burst,
	}
}

// Allow checks if a single request is allowed for the given key.
func (rl *RedisLimiter) Allow(ctx context.Context, key string) bool {
	return rl.AllowN(ctx, key, 1)
}

// AllowN checks if n requests are allowed for the given key.
func (rl *RedisLimiter) AllowN(ctx context.Context, key string, n int) bool {
	redisKey := fmt.Sprintf("%s:%s", rl.prefix, key)
	now := float64(time.Now().UnixNano()) / 1e9

	result, err := rl.client.Eval(ctx, tokenBucketScript, []string{redisKey},
		rl.rate, rl.burst, n, now)
	if err != nil {
		return false
	}

	val, ok := result.(int64)
	if !ok {
		return false
	}
	return val == 1
}
