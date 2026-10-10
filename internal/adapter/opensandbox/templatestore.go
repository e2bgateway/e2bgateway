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
	"time"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
)

// TemplateStore abstracts persistence for OpenSandbox's template, build,
// alias, and tag metadata. OpenSandbox itself uses container images and has
// no native template concept; the gateway provides the abstraction.
//
// Implementations:
//   - MemoryTemplateStore: in-process map storage (default, zero external deps)
//   - RedisTemplateStore: Redis-backed persistent storage (multi-instance safe)
//   - (future) EtcdTemplateStore: etcd-backed storage
//
// All methods accept a context.Context so network-backed implementations can
// honor deadlines, cancellation, and tracing. Implementations must be safe for
// concurrent use.
type TemplateStore interface {
	// --- Templates ---

	// CreateTemplate stores a new template. Returns ErrTemplateExists if a
	// template with the same ID already exists.
	CreateTemplate(ctx context.Context, entry *TemplateEntry) error

	// GetTemplate retrieves a template by ID. Returns ErrTemplateNotFound if
	// the template does not exist.
	GetTemplate(ctx context.Context, templateID string) (*TemplateEntry, error)

	// ListTemplates returns all templates. Implementations should return them
	// in a stable order (e.g. sorted by CreatedAt).
	ListTemplates(ctx context.Context) ([]*TemplateEntry, error)

	// UpdateTemplate applies an update function to an existing template.
	// Returns ErrTemplateNotFound if the template does not exist. The update
	// function must not be called after UpdateTemplate returns.
	UpdateTemplate(ctx context.Context, templateID string, update func(*TemplateEntry) error) error

	// DeleteTemplate removes a template. Returns ErrTemplateNotFound if the
	// template does not exist. Cascading deletes of associated builds,
	// aliases, and tags are the caller's responsibility (via the dedicated
	// methods below).
	DeleteTemplate(ctx context.Context, templateID string) error

	// --- Builds ---

	// SaveBuild stores a build and associates it with its template.
	SaveBuild(ctx context.Context, templateID string, build *adapter.BuildStatus) error

	// GetBuild retrieves a build by buildID. Returns ErrBuildNotFound if the
	// build does not exist.
	GetBuild(ctx context.Context, buildID string) (*adapter.BuildStatus, error)

	// ListBuilds returns all builds for a template.
	ListBuilds(ctx context.Context, templateID string) ([]*adapter.BuildStatus, error)

	// DeleteBuilds removes all builds for a template. No error if none exist.
	DeleteBuilds(ctx context.Context, templateID string) error

	// --- Aliases ---

	// AddAlias associates an alias with a template. Idempotent: adding an
	// existing alias is a no-op (returns nil).
	AddAlias(ctx context.Context, templateID, alias string) error

	// RemoveAlias removes an alias. Returns ErrAliasNotFound if the alias
	// does not exist for the template.
	RemoveAlias(ctx context.Context, templateID, alias string) error

	// ListAliases returns all aliases for a template.
	ListAliases(ctx context.Context, templateID string) ([]string, error)

	// ResolveAlias returns the templateID for an alias. Returns
	// ErrAliasNotFound if the alias does not exist.
	ResolveAlias(ctx context.Context, alias string) (string, error)

	// DeleteAliases removes all aliases for a template. No error if none
	// exist.
	DeleteAliases(ctx context.Context, templateID string) error

	// --- Tags ---

	// SaveTag creates or replaces a tag on a template (keyed by tag name).
	SaveTag(ctx context.Context, templateID string, tag *adapter.Tag) error

	// ListTags returns all tags for a template.
	ListTags(ctx context.Context, templateID string) ([]*adapter.Tag, error)

	// DeleteTag removes a tag by name. Returns ErrTagNotFound if the tag
	// does not exist.
	DeleteTag(ctx context.Context, templateID, tagName string) error

	// DeleteTags removes all tags for a template. No error if none exist.
	DeleteTags(ctx context.Context, templateID string) error

	// --- Lifecycle ---

	// Close releases any resources held by the store (connections, etc).
	Close() error
}

// TemplateEntry stores metadata for a gateway-managed template.
type TemplateEntry struct {
	TemplateID string            `json:"templateID"`
	Name       string            `json:"name"`
	ImageURI   string            `json:"imageURI"`
	Dockerfile string            `json:"dockerfile,omitempty"`
	StartCmd   string            `json:"startCmd,omitempty"`
	CPUCount   int               `json:"cpuCount,omitempty"`
	MemoryMB   int               `json:"memoryMB,omitempty"`
	BuildID    string            `json:"buildID,omitempty"`
	CreatedAt  time.Time         `json:"createdAt"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

// Sentinel errors for TemplateStore operations. Implementations should return
// these (or wrap them with %w) so callers can use errors.Is().
var (
	ErrTemplateNotFound = errSentinel("template not found")
	ErrTemplateExists   = errSentinel("template already exists")
	ErrBuildNotFound    = errSentinel("build not found")
	ErrAliasNotFound    = errSentinel("alias not found")
	ErrTagNotFound      = errSentinel("tag not found")
)

// errSentinel is a simple error type for sentinel errors.
type errSentinel string

func (e errSentinel) Error() string { return string(e) }
