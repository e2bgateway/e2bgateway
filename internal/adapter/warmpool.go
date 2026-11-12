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

package adapter

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Sentinel errors for warm pool CRUD. Handlers currently use a
// first-success-wins adapter iteration and do not map these to HTTP status
// codes yet; they exist so adapters share consistent semantics and tests can
// assert with errors.Is.
var (
	ErrWarmPoolNotFound      = errors.New("warm pool not found")
	ErrWarmPoolAlreadyExists = errors.New("warm pool already exists")
)

// DefaultMaxWarmPoolSize caps TargetSize to prevent resource exhaustion (DoS).
const DefaultMaxWarmPoolSize = 256

// warmPoolIDMaxLen mirrors the Kubernetes metadata.name limit so the same ID
// works as a SandboxWarmPool name and as an OpenSandbox pool / Redis key.
const warmPoolIDMaxLen = 63

// warmPoolIDPattern is K8s-name compatible (RFC 1123 label).
var warmPoolIDPattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// warmPoolSlugInvalidChars matches runs of characters not allowed in a
// generated slug.
var warmPoolSlugInvalidChars = regexp.MustCompile(`[^a-z0-9]+`)

// ValidateWarmPoolID returns an error if id is not a valid warm pool ID.
func ValidateWarmPoolID(id string) error {
	if len(id) > warmPoolIDMaxLen || !warmPoolIDPattern.MatchString(id) {
		return fmt.Errorf("invalid warm pool ID %q: must match %s and be at most %d characters",
			id, warmPoolIDPattern, warmPoolIDMaxLen)
	}
	return nil
}

// GenerateWarmPoolID derives a deterministic, valid ID: wp-{slug}-{hash8}.
func GenerateWarmPoolID(templateID string) string {
	sum := sha256.Sum256([]byte(templateID))
	hash := hex.EncodeToString(sum[:])[:8]

	// Slug: lowercase, invalid chars collapsed to "-", trimmed, capped so the
	// total length stays within warmPoolIDMaxLen.
	slug := strings.ToLower(templateID)
	slug = warmPoolSlugInvalidChars.ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")
	if slugMax := warmPoolIDMaxLen - len("wp--") - len(hash); len(slug) > slugMax {
		slug = strings.TrimRight(slug[:slugMax], "-")
	}
	if slug == "" {
		slug = "pool"
	}
	return "wp-" + slug + "-" + hash
}

// ClampTargetSize validates n >= 0 and caps it at limit. limit <= 0 falls
// back to DefaultMaxWarmPoolSize.
func ClampTargetSize(n, limit int) (int, error) {
	if n < 0 {
		return 0, fmt.Errorf("targetSize must be >= 0, got %d", n)
	}
	if limit <= 0 {
		limit = DefaultMaxWarmPoolSize
	}
	if n > limit {
		return limit, nil
	}
	return n, nil
}
