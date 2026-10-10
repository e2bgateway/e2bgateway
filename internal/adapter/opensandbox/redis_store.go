// Copyright The E2BGateway Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package opensandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
)

// Compile-time assertion that RedisTemplateStore implements TemplateStore.
var _ TemplateStore = (*RedisTemplateStore)(nil)

// RedisTemplateStore is a Redis-backed TemplateStore. State survives gateway
// restarts and can be shared across multiple gateway instances.
//
// Key layout (all keys prefixed by KeyPrefix):
//
//	{p}templates                          Hash   templateID → JSON(TemplateEntry)
//	{p}builds                             Hash   buildID    → JSON(BuildStatus)
//	{p}template_builds:{templateID}       Set    buildID...
//	{p}aliases                            Hash   alias      → templateID
//	{p}template_aliases:{templateID}      Set    alias...
//	{p}tags:{templateID}                  Hash   tagName    → JSON(Tag)
type RedisTemplateStore struct {
	client    redis.UniversalClient
	keyPrefix string
}

// RedisConfig holds configuration for the RedisTemplateStore.
type RedisConfig struct {
	// Addr is the Redis server address (host:port). Required.
	Addr string
	// Password for Redis AUTH. Optional.
	Password string
	// DB is the Redis database number. Default 0.
	DB int
	// KeyPrefix is prepended to all keys. Default "e2bgateway:opensandbox:".
	KeyPrefix string
	// DialTimeout for the initial connection. Default 5s.
	DialTimeout time.Duration
	// ReadTimeout for reads. Default 3s.
	ReadTimeout time.Duration
	// WriteTimeout for writes. Default 3s.
	WriteTimeout time.Duration
}

// NewRedisTemplateStore creates a RedisTemplateStore from the given config.
// It pings the server to verify connectivity before returning.
func NewRedisTemplateStore(ctx context.Context, cfg RedisConfig) (*RedisTemplateStore, error) {
	if cfg.Addr == "" {
		return nil, fmt.Errorf("redis addr is required")
	}
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = 5 * time.Second
	}
	if cfg.ReadTimeout == 0 {
		cfg.ReadTimeout = 3 * time.Second
	}
	if cfg.WriteTimeout == 0 {
		cfg.WriteTimeout = 3 * time.Second
	}
	if cfg.KeyPrefix == "" {
		cfg.KeyPrefix = "e2bgateway:opensandbox:"
	}

	client := redis.NewClient(&redis.Options{
		Addr:         cfg.Addr,
		Password:     cfg.Password,
		DB:           cfg.DB,
		DialTimeout:  cfg.DialTimeout,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
	})

	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("redis ping: %w", err)
	}

	return &RedisTemplateStore{
		client:    client,
		keyPrefix: cfg.KeyPrefix,
	}, nil
}

// NewRedisTemplateStoreWithClient creates a RedisTemplateStore using an
// existing redis.UniversalClient. Used by tests (e.g., miniredis).
func NewRedisTemplateStoreWithClient(client redis.UniversalClient, keyPrefix string) *RedisTemplateStore {
	if keyPrefix == "" {
		keyPrefix = "e2bgateway:opensandbox:"
	}
	return &RedisTemplateStore{client: client, keyPrefix: keyPrefix}
}

// key returns the full Redis key for a given suffix.
func (s *RedisTemplateStore) key(suffix string) string {
	return s.keyPrefix + suffix
}

// --- JSON helpers ---

func marshalJSON(v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func unmarshalJSON[T any](data string) (*T, error) {
	var v T
	if err := json.Unmarshal([]byte(data), &v); err != nil {
		return nil, err
	}
	return &v, nil
}

// --- Templates ---

func (s *RedisTemplateStore) CreateTemplate(ctx context.Context, entry *TemplateEntry) error {
	value, err := marshalJSON(entry)
	if err != nil {
		return fmt.Errorf("marshal template: %w", err)
	}

	// Use HSETNX to fail if the template already exists.
	ok, err := s.client.HSetNX(ctx, s.key("templates"), entry.TemplateID, value).Result()
	if err != nil {
		return fmt.Errorf("redis HSETNX: %w", err)
	}
	if !ok {
		return fmt.Errorf("template %q: %w", entry.TemplateID, ErrTemplateExists)
	}
	return nil
}

func (s *RedisTemplateStore) GetTemplate(ctx context.Context, templateID string) (*TemplateEntry, error) {
	value, err := s.client.HGet(ctx, s.key("templates"), templateID).Result()
	if errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("template %q: %w", templateID, ErrTemplateNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("redis HGET: %w", err)
	}
	entry, err := unmarshalJSON[TemplateEntry](value)
	if err != nil {
		return nil, fmt.Errorf("unmarshal template: %w", err)
	}
	return entry, nil
}

func (s *RedisTemplateStore) ListTemplates(ctx context.Context) ([]*TemplateEntry, error) {
	all, err := s.client.HGetAll(ctx, s.key("templates")).Result()
	if err != nil {
		return nil, fmt.Errorf("redis HGETALL: %w", err)
	}

	entries := make([]*TemplateEntry, 0, len(all))
	for _, v := range all {
		entry, err := unmarshalJSON[TemplateEntry](v)
		if err != nil {
			return nil, fmt.Errorf("unmarshal template: %w", err)
		}
		entries = append(entries, entry)
	}

	// Stable order by creation time.
	slices.SortFunc(entries, func(a, b *TemplateEntry) int {
		return a.CreatedAt.Compare(b.CreatedAt)
	})
	return entries, nil
}

func (s *RedisTemplateStore) UpdateTemplate(ctx context.Context, templateID string, update func(*TemplateEntry) error) error {
	// Optimistic lock: read → modify → write with HSET (overwrite).
	// For multi-instance safety a Lua script or WATCH/MULTI would be better,
	// but HSET atomicity is acceptable for low-contention template metadata.
	value, err := s.client.HGet(ctx, s.key("templates"), templateID).Result()
	if errors.Is(err, redis.Nil) {
		return fmt.Errorf("template %q: %w", templateID, ErrTemplateNotFound)
	}
	if err != nil {
		return fmt.Errorf("redis HGET: %w", err)
	}

	entry, err := unmarshalJSON[TemplateEntry](value)
	if err != nil {
		return fmt.Errorf("unmarshal template: %w", err)
	}

	if err := update(entry); err != nil {
		return err
	}

	encoded, err := marshalJSON(entry)
	if err != nil {
		return fmt.Errorf("marshal template: %w", err)
	}
	if err := s.client.HSet(ctx, s.key("templates"), templateID, encoded).Err(); err != nil {
		return fmt.Errorf("redis HSET: %w", err)
	}
	return nil
}

func (s *RedisTemplateStore) DeleteTemplate(ctx context.Context, templateID string) error {
	n, err := s.client.HDel(ctx, s.key("templates"), templateID).Result()
	if err != nil {
		return fmt.Errorf("redis HDEL: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("template %q: %w", templateID, ErrTemplateNotFound)
	}
	return nil
}

// --- Builds ---

func (s *RedisTemplateStore) SaveBuild(ctx context.Context, templateID string, build *adapter.BuildStatus) error {
	value, err := marshalJSON(build)
	if err != nil {
		return fmt.Errorf("marshal build: %w", err)
	}

	pipe := s.client.Pipeline()
	pipe.HSet(ctx, s.key("builds"), build.BuildID, value)
	pipe.SAdd(ctx, s.key(fmt.Sprintf("template_builds:%s", templateID)), build.BuildID)
	_, err = pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("redis pipeline: %w", err)
	}
	return nil
}

func (s *RedisTemplateStore) GetBuild(ctx context.Context, buildID string) (*adapter.BuildStatus, error) {
	value, err := s.client.HGet(ctx, s.key("builds"), buildID).Result()
	if errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("build %q: %w", buildID, ErrBuildNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("redis HGET: %w", err)
	}
	build, err := unmarshalJSON[adapter.BuildStatus](value)
	if err != nil {
		return nil, fmt.Errorf("unmarshal build: %w", err)
	}
	return build, nil
}

func (s *RedisTemplateStore) ListBuilds(ctx context.Context, templateID string) ([]*adapter.BuildStatus, error) {
	buildIDs, err := s.client.SMembers(ctx, s.key(fmt.Sprintf("template_builds:%s", templateID))).Result()
	if err != nil {
		return nil, fmt.Errorf("redis SMEMBERS: %w", err)
	}
	if len(buildIDs) == 0 {
		return []*adapter.BuildStatus{}, nil
	}

	values, err := s.client.HMGet(ctx, s.key("builds"), buildIDs...).Result()
	if err != nil {
		return nil, fmt.Errorf("redis HMGET: %w", err)
	}

	result := make([]*adapter.BuildStatus, 0, len(values))
	for _, v := range values {
		if v == nil {
			continue
		}
		str, ok := v.(string)
		if !ok {
			continue
		}
		build, err := unmarshalJSON[adapter.BuildStatus](str)
		if err != nil {
			return nil, fmt.Errorf("unmarshal build: %w", err)
		}
		result = append(result, build)
	}
	return result, nil
}

func (s *RedisTemplateStore) DeleteBuilds(ctx context.Context, templateID string) error {
	setKey := s.key(fmt.Sprintf("template_builds:%s", templateID))
	buildIDs, err := s.client.SMembers(ctx, setKey).Result()
	if err != nil {
		return fmt.Errorf("redis SMEMBERS: %w", err)
	}
	if len(buildIDs) == 0 {
		return nil
	}

	pipe := s.client.Pipeline()
	pipe.HDel(ctx, s.key("builds"), buildIDs...)
	pipe.Del(ctx, setKey)
	_, err = pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("redis pipeline: %w", err)
	}
	return nil
}

// --- Aliases ---

func (s *RedisTemplateStore) AddAlias(ctx context.Context, templateID, alias string) error {
	pipe := s.client.Pipeline()
	// HSETNX for the reverse map — returns 0 if alias already exists (idempotent).
	pipe.HSetNX(ctx, s.key("aliases"), alias, templateID)
	pipe.SAdd(ctx, s.key(fmt.Sprintf("template_aliases:%s", templateID)), alias)
	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("redis pipeline: %w", err)
	}
	return nil
}

func (s *RedisTemplateStore) RemoveAlias(ctx context.Context, templateID, alias string) error {
	setKey := s.key(fmt.Sprintf("template_aliases:%s", templateID))
	removed, err := s.client.SRem(ctx, setKey, alias).Result()
	if err != nil {
		return fmt.Errorf("redis SREM: %w", err)
	}
	if removed == 0 {
		return fmt.Errorf("alias %q for template %q: %w", alias, templateID, ErrAliasNotFound)
	}
	// Best-effort cleanup of the reverse map — ignore if another template
	// still references this alias (rare but possible after race).
	_ = s.client.HDel(ctx, s.key("aliases"), alias).Err()
	return nil
}

func (s *RedisTemplateStore) ListAliases(ctx context.Context, templateID string) ([]string, error) {
	setKey := s.key(fmt.Sprintf("template_aliases:%s", templateID))
	aliases, err := s.client.SMembers(ctx, setKey).Result()
	if err != nil {
		return nil, fmt.Errorf("redis SMEMBERS: %w", err)
	}
	return aliases, nil
}

func (s *RedisTemplateStore) ResolveAlias(ctx context.Context, alias string) (string, error) {
	templateID, err := s.client.HGet(ctx, s.key("aliases"), alias).Result()
	if errors.Is(err, redis.Nil) {
		return "", fmt.Errorf("alias %q: %w", alias, ErrAliasNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("redis HGET: %w", err)
	}
	return templateID, nil
}

func (s *RedisTemplateStore) DeleteAliases(ctx context.Context, templateID string) error {
	setKey := s.key(fmt.Sprintf("template_aliases:%s", templateID))
	aliases, err := s.client.SMembers(ctx, setKey).Result()
	if err != nil {
		return fmt.Errorf("redis SMEMBERS: %w", err)
	}
	if len(aliases) == 0 {
		return nil
	}

	pipe := s.client.Pipeline()
	pipe.HDel(ctx, s.key("aliases"), aliases...)
	pipe.Del(ctx, setKey)
	_, err = pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("redis pipeline: %w", err)
	}
	return nil
}

// --- Tags ---

func (s *RedisTemplateStore) SaveTag(ctx context.Context, templateID string, tag *adapter.Tag) error {
	value, err := marshalJSON(tag)
	if err != nil {
		return fmt.Errorf("marshal tag: %w", err)
	}
	hashKey := s.key(fmt.Sprintf("tags:%s", templateID))
	if err := s.client.HSet(ctx, hashKey, tag.Name, value).Err(); err != nil {
		return fmt.Errorf("redis HSET: %w", err)
	}
	return nil
}

func (s *RedisTemplateStore) ListTags(ctx context.Context, templateID string) ([]*adapter.Tag, error) {
	hashKey := s.key(fmt.Sprintf("tags:%s", templateID))
	all, err := s.client.HGetAll(ctx, hashKey).Result()
	if err != nil {
		return nil, fmt.Errorf("redis HGETALL: %w", err)
	}

	result := make([]*adapter.Tag, 0, len(all))
	for _, v := range all {
		tag, err := unmarshalJSON[adapter.Tag](v)
		if err != nil {
			return nil, fmt.Errorf("unmarshal tag: %w", err)
		}
		result = append(result, tag)
	}
	return result, nil
}

func (s *RedisTemplateStore) DeleteTag(ctx context.Context, templateID, tagName string) error {
	hashKey := s.key(fmt.Sprintf("tags:%s", templateID))
	n, err := s.client.HDel(ctx, hashKey, tagName).Result()
	if err != nil {
		return fmt.Errorf("redis HDEL: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("tag %q for template %q: %w", tagName, templateID, ErrTagNotFound)
	}
	return nil
}

func (s *RedisTemplateStore) DeleteTags(ctx context.Context, templateID string) error {
	hashKey := s.key(fmt.Sprintf("tags:%s", templateID))
	if err := s.client.Del(ctx, hashKey).Err(); err != nil {
		return fmt.Errorf("redis DEL: %w", err)
	}
	return nil
}

// --- Lifecycle ---

func (s *RedisTemplateStore) Close() error {
	return s.client.Close()
}
