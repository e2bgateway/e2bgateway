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

package agentsandbox

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	extv1beta1 "sigs.k8s.io/agent-sandbox/extensions/api/v1beta1"
)

func annotatedClaim(name, sandboxID string) *extv1beta1.SandboxClaim {
	return &extv1beta1.SandboxClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			Annotations: map[string]string{
				defaultSandboxIDAnnotation: sandboxID,
			},
		},
	}
}

// fullIdentityClaim carries every annotation CreateSandbox writes: sandbox
// ID, template ID and JSON-encoded metadata.
func fullIdentityClaim(name, sandboxID, templateID, metadataJSON string) *extv1beta1.SandboxClaim {
	c := annotatedClaim(name, sandboxID)
	c.Annotations[sandboxTemplateAnnotation] = templateID
	c.Annotations[sandboxMetadataAnnotation] = metadataJSON
	return c
}

// TestResolveClaimNameRecoversFromAnnotation covers the multi-replica /
// restart path: the in-process idMap misses, so the mapping must be
// recovered from the claim annotation via the K8s API and re-populated
// locally (issue #90: replica B got 502 "claim not found" for a sandbox
// created on replica A).
func TestResolveClaimNameRecoversFromAnnotation(t *testing.T) {
	a, _ := newWarmPoolTestAdapter(t, annotatedClaim("claim-abc", "sbx-cross-replica"))
	a.idMap = make(map[string]*sandboxEntry)
	ctx := context.Background()

	got, err := a.resolveClaimName(ctx, "sbx-cross-replica")
	if err != nil {
		t.Fatalf("resolveClaimName: %v", err)
	}
	if got != "claim-abc" {
		t.Fatalf("resolved claim %q, want claim-abc", got)
	}

	// The lookup must have re-populated the local map...
	a.idMapMu.RLock()
	entry, ok := a.idMap["sbx-cross-replica"]
	a.idMapMu.RUnlock()
	if !ok || entry.claimName != "claim-abc" {
		t.Fatalf("idMap not re-populated: %+v", entry)
	}

	// ...so the second resolve hits the fast path with the same result.
	got, err = a.resolveClaimName(ctx, "sbx-cross-replica")
	if err != nil || got != "claim-abc" {
		t.Fatalf("second resolve: (%q, %v)", got, err)
	}
}

func TestResolveClaimNameUnknownFallsBackToID(t *testing.T) {
	a, _ := newWarmPoolTestAdapter(t, annotatedClaim("claim-abc", "sbx-other"))
	a.idMap = make(map[string]*sandboxEntry)

	got, err := a.resolveClaimName(context.Background(), "sbx-unknown")
	if err != nil {
		t.Fatalf("resolveClaimName: %v", err)
	}
	if got != "sbx-unknown" {
		t.Errorf("fallback should return the ID itself, got %q", got)
	}
}

func TestAnnotateClaimIdentity(t *testing.T) {
	claim := &extv1beta1.SandboxClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "claim-xyz", Namespace: "default"},
	}
	a, cs := newWarmPoolTestAdapter(t, claim)

	err := a.annotateClaimIdentity(context.Background(), "claim-xyz", "sbx-annotated", "code-interpreter",
		map[string]string{"team": "infra", "env": "prod"})
	if err != nil {
		t.Fatalf("annotateClaimIdentity: %v", err)
	}

	got, err := cs.ExtensionsV1beta1().SandboxClaims("default").Get(context.Background(), "claim-xyz", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get claim: %v", err)
	}
	if got.Annotations[defaultSandboxIDAnnotation] != "sbx-annotated" {
		t.Errorf("sandbox-id annotation not written: %v", got.Annotations)
	}
	if got.Annotations[sandboxTemplateAnnotation] != "code-interpreter" {
		t.Errorf("template annotation not written: %v", got.Annotations)
	}
	if got.Annotations[sandboxMetadataAnnotation] != `{"env":"prod","team":"infra"}` {
		t.Errorf("metadata annotation: %q", got.Annotations[sandboxMetadataAnnotation])
	}

	// The annotations must make the claim resolvable by sandbox ID...
	a.idMap = make(map[string]*sandboxEntry)
	name, err := a.resolveClaimName(context.Background(), "sbx-annotated")
	if err != nil || name != "claim-xyz" {
		t.Fatalf("resolve after annotate: (%q, %v)", name, err)
	}
	// ...and the recovered idMap entry must carry the FULL identity, so
	// GetSandbox on this replica matches the creation response.
	a.idMapMu.RLock()
	entry := a.idMap["sbx-annotated"]
	a.idMapMu.RUnlock()
	if entry == nil {
		t.Fatal("idMap entry not re-populated")
	}
	if entry.templateID != "code-interpreter" {
		t.Errorf("recovered templateID: %q", entry.templateID)
	}
	if entry.metadata["team"] != "infra" || entry.metadata["env"] != "prod" {
		t.Errorf("recovered metadata: %v", entry.metadata)
	}
}

// Recovered entries must restore templateID and metadata from the claim
// annotations even when nothing was annotated by THIS process (the
// cross-replica / restart case CodeRabbit flagged: GetSandbox returned an
// empty TemplateID and nil Metadata for recovered sandboxes).
func TestResolveClaimNameRestoresFullIdentity(t *testing.T) {
	claim := fullIdentityClaim("claim-full", "sbx-full", "base", `{"owner":"alice"}`)
	a, _ := newWarmPoolTestAdapter(t, claim)
	a.idMap = make(map[string]*sandboxEntry)

	name, err := a.resolveClaimName(context.Background(), "sbx-full")
	if err != nil || name != "claim-full" {
		t.Fatalf("resolve: (%q, %v)", name, err)
	}
	a.idMapMu.RLock()
	entry := a.idMap["sbx-full"]
	a.idMapMu.RUnlock()
	if entry == nil || entry.templateID != "base" || entry.metadata["owner"] != "alice" {
		t.Fatalf("recovered entry incomplete: %+v", entry)
	}
}

// Oversized metadata must be skipped with a warning, not fail creation.
func TestAnnotateClaimIdentityOversizedMetadata(t *testing.T) {
	claim := &extv1beta1.SandboxClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "claim-big", Namespace: "default"},
	}
	a, cs := newWarmPoolTestAdapter(t, claim)

	big := strings.Repeat("x", maxMetadataAnnotationBytes+1)
	if err := a.annotateClaimIdentity(context.Background(), "claim-big", "sbx-big", "base", map[string]string{"blob": big}); err != nil {
		t.Fatalf("oversized metadata must not fail the annotation: %v", err)
	}
	got, err := cs.ExtensionsV1beta1().SandboxClaims("default").Get(context.Background(), "claim-big", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get claim: %v", err)
	}
	if _, present := got.Annotations[sandboxMetadataAnnotation]; present {
		t.Error("oversized metadata must be skipped, not stored")
	}
	if got.Annotations[defaultSandboxIDAnnotation] != "sbx-big" || got.Annotations[sandboxTemplateAnnotation] != "base" {
		t.Errorf("identity annotations must still be written: %v", got.Annotations)
	}
}
