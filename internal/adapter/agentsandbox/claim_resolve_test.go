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

func TestAnnotateClaimSandboxID(t *testing.T) {
	claim := &extv1beta1.SandboxClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "claim-xyz", Namespace: "default"},
	}
	a, cs := newWarmPoolTestAdapter(t, claim)

	if err := a.annotateClaimSandboxID(context.Background(), "claim-xyz", "sbx-annotated"); err != nil {
		t.Fatalf("annotateClaimSandboxID: %v", err)
	}

	got, err := cs.ExtensionsV1beta1().SandboxClaims("default").Get(context.Background(), "claim-xyz", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get claim: %v", err)
	}
	if got.Annotations[defaultSandboxIDAnnotation] != "sbx-annotated" {
		t.Errorf("annotation not written: %v", got.Annotations)
	}

	// The annotation must make the claim resolvable by sandbox ID.
	a.idMap = make(map[string]*sandboxEntry)
	name, err := a.resolveClaimName(context.Background(), "sbx-annotated")
	if err != nil || name != "claim-xyz" {
		t.Fatalf("resolve after annotate: (%q, %v)", name, err)
	}
}
