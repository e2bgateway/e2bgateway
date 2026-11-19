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
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtime "k8s.io/apimachinery/pkg/runtime"
	schema "k8s.io/apimachinery/pkg/runtime/schema"
	k8stesting "k8s.io/client-go/testing"

	sandboxv1beta1 "sigs.k8s.io/agent-sandbox/api/v1beta1"
	"sigs.k8s.io/agent-sandbox/clients/go/sandbox"
	agentsfake "sigs.k8s.io/agent-sandbox/clients/k8s/clientset/versioned/fake"
	extfake "sigs.k8s.io/agent-sandbox/clients/k8s/extensions/clientset/versioned/fake"
	extv1beta1 "sigs.k8s.io/agent-sandbox/extensions/api/v1beta1"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
)

// fakeSnapshotStore emulates the API-server state that the snapshot code
// path touches: the Sandbox CR (sbx-1) and the SandboxClaim (e2b-test-1)
// used to resolve its CR name.
//
// The fake clientset scheme does not register the agent-sandbox Go types
// (the generated fake only wires the metav1 group), so objects cannot be
// seeded via NewSimpleClientset; instead, reactors serve and persist the
// objects. The update reactor reproduces the real API server's optimistic
// concurrency: an update whose snapshot annotation is missing a record that
// is currently stored is rejected with 409 Conflict. (The fake tracker
// itself replaces unconditionally and would never conflict.)
type fakeSnapshotStore struct {
	mu sync.Mutex

	sb    *sandboxv1beta1.Sandbox
	claim *extv1beta1.SandboxClaim

	updates         int
	conflicts       int
	injectConflicts int
}

func newSnapshotTestAdapter(t *testing.T, sbAnnotations map[string]string) (*Adapter, *fakeSnapshotStore) {
	t.Helper()

	store := &fakeSnapshotStore{
		sb: &sandboxv1beta1.Sandbox{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "sbx-1",
				Namespace:   "default",
				Annotations: sbAnnotations,
			},
		},
		claim: &extv1beta1.SandboxClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "e2b-test-1",
				Namespace: "default",
			},
			Status: extv1beta1.SandboxClaimStatus{
				SandboxStatus: extv1beta1.SandboxStatus{
					Name: "sbx-1",
				},
			},
		},
	}

	agentsCS := agentsfake.NewSimpleClientset()
	extCS := extfake.NewSimpleClientset()

	agentsCS.PrependReactor("get", "sandboxes", func(action k8stesting.Action) (bool, runtime.Object, error) {
		store.mu.Lock()
		defer store.mu.Unlock()
		return true, store.sb.DeepCopy(), nil
	})
	agentsCS.PrependReactor("update", "sandboxes", func(action k8stesting.Action) (bool, runtime.Object, error) {
		ua, ok := action.(k8stesting.UpdateAction)
		if !ok {
			return false, nil, nil
		}
		in, ok := ua.GetObject().(*sandboxv1beta1.Sandbox)
		if !ok {
			return false, nil, nil
		}
		store.mu.Lock()
		defer store.mu.Unlock()
		store.updates++
		if store.injectConflicts > 0 {
			store.injectConflicts--
			store.conflicts++
			return true, nil, newSandboxConflict(action, in.Name)
		}
		if staleSnapshotUpdate(in, store.sb) {
			store.conflicts++
			return true, nil, newSandboxConflict(action, in.Name)
		}
		store.sb = in.DeepCopy()
		return true, store.sb.DeepCopy(), nil
	})
	agentsCS.PrependReactor("patch", "sandboxes", func(action k8stesting.Action) (bool, runtime.Object, error) {
		pa, ok := action.(k8stesting.PatchAction)
		if !ok {
			return false, nil, nil
		}
		var patch struct {
			Spec struct {
				OperatingMode sandboxv1beta1.SandboxOperatingMode `json:"operatingMode"`
			} `json:"spec"`
		}
		if err := json.Unmarshal(pa.GetPatch(), &patch); err != nil {
			return true, nil, err
		}
		store.mu.Lock()
		defer store.mu.Unlock()
		store.sb.Spec.OperatingMode = patch.Spec.OperatingMode
		return true, store.sb.DeepCopy(), nil
	})
	extCS.PrependReactor("get", "sandboxclaims", func(action k8stesting.Action) (bool, runtime.Object, error) {
		store.mu.Lock()
		defer store.mu.Unlock()
		return true, store.claim.DeepCopy(), nil
	})

	a := &Adapter{
		name:      "agentsandbox-test",
		namespace: "default",
		k8s: &sandbox.K8sHelper{
			AgentsClient:     agentsCS.AgentsV1beta1(),
			ExtensionsClient: extCS.ExtensionsV1beta1(),
		},
	}

	handle := &fakeSuspendResume{store: store}
	a.lifecycleHandleFn = func(ctx context.Context, sandboxID string) (suspendResumeHandle, error) {
		return handle, nil
	}
	return a, store
}

// fakeSuspendResume emulates the SDK handle's lifecycle methods together
// with the controller's reactions: Suspend patches spec.operatingMode and
// records the Suspended condition the real controller writes once the pod
// terminates; Resume restores Running and readiness.
type fakeSuspendResume struct {
	store *fakeSnapshotStore
	calls int
}

func (f *fakeSuspendResume) Suspend(ctx context.Context, opts ...sandbox.CallOption) error {
	f.store.mu.Lock()
	defer f.store.mu.Unlock()
	f.calls++
	if f.store.sb.Spec.OperatingMode == sandboxv1beta1.SandboxOperatingModeSuspended {
		return nil
	}
	f.store.sb.Spec.OperatingMode = sandboxv1beta1.SandboxOperatingModeSuspended
	f.store.setConditionLocked(sandboxv1beta1.SandboxConditionSuspended)
	return nil
}

func (f *fakeSuspendResume) Resume(ctx context.Context, opts ...sandbox.CallOption) error {
	f.store.mu.Lock()
	defer f.store.mu.Unlock()
	f.calls++
	f.store.sb.Spec.OperatingMode = sandboxv1beta1.SandboxOperatingModeRunning
	f.store.setConditionLocked(sandboxv1beta1.SandboxConditionReady)
	return nil
}

// setConditionLocked records a True condition the way the controller writes
// it, stamping the current generation as ObservedGeneration. Callers must
// hold store.mu.
func (s *fakeSnapshotStore) setConditionLocked(condType sandboxv1beta1.ConditionType) {
	s.sb.Generation++
	for i := range s.sb.Status.Conditions {
		if s.sb.Status.Conditions[i].Type == string(condType) {
			s.sb.Status.Conditions[i].Status = metav1.ConditionTrue
			s.sb.Status.Conditions[i].ObservedGeneration = s.sb.Generation
			return
		}
	}
	s.sb.Status.Conditions = append(s.sb.Status.Conditions, metav1.Condition{
		Type:               string(condType),
		Status:             metav1.ConditionTrue,
		ObservedGeneration: s.sb.Generation,
	})
}

func newSandboxConflict(action k8stesting.Action, name string) error {
	return k8serrors.NewConflict(
		schema.GroupResource{Group: action.GetResource().Group, Resource: action.GetResource().Resource},
		name, errors.New("the object has been modified; please apply your changes to the latest version and try again"))
}

// staleSnapshotUpdate reports whether the incoming object's snapshot
// annotation is missing records that are currently stored — i.e. a write
// based on an outdated read, which a real API server rejects with 409.
func staleSnapshotUpdate(incoming, stored *sandboxv1beta1.Sandbox) bool {
	var storedRecords []snapshotRecord
	if stored.Annotations != nil {
		_ = json.Unmarshal([]byte(stored.Annotations[annotationSnapshots]), &storedRecords)
	}
	if len(storedRecords) == 0 {
		return false
	}
	var incomingRecords []snapshotRecord
	if incoming.Annotations != nil {
		_ = json.Unmarshal([]byte(incoming.Annotations[annotationSnapshots]), &incomingRecords)
	}
	ids := make(map[string]bool, len(incomingRecords))
	for _, rec := range incomingRecords {
		ids[rec.SnapshotID] = true
	}
	for _, rec := range storedRecords {
		if !ids[rec.SnapshotID] {
			return true
		}
	}
	return false
}

// TestCreateSnapshot_ConflictRetry verifies that a 409 Conflict from the API
// server during the annotation update is retried: the read-modify-write
// re-reads the CR and succeeds on the second attempt instead of failing the
// request or dropping the record.
func TestCreateSnapshot_ConflictRetry(t *testing.T) {
	a, store := newSnapshotTestAdapter(t, nil)
	store.injectConflicts = 1

	snap, err := a.CreateSnapshot(context.Background(), "e2b-test-1", &adapter.SnapshotRequest{Name: "snap-a"})
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	if snap.SnapshotID == "" {
		t.Error("expected non-empty SnapshotID")
	}
	if store.updates != 2 {
		t.Errorf("expected update to be attempted twice (conflict + retry), got %d", store.updates)
	}
	if store.conflicts != 1 {
		t.Errorf("expected exactly 1 injected conflict, got %d", store.conflicts)
	}

	snapshots, err := a.ListSnapshots(context.Background(), "e2b-test-1")
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(snapshots) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(snapshots))
	}
	if snapshots[0].SnapshotID != snap.SnapshotID {
		t.Errorf("SnapshotID = %q, want %q", snapshots[0].SnapshotID, snap.SnapshotID)
	}
}

// TestCreateSnapshot_Concurrent_NoLostRecords fires concurrent creations and
// rejects stale updates exactly like a real API server. Every returned
// snapshot ID must survive in the final annotation — no silently dropped
// records (which the previous merge-patch implementation lost).
func TestCreateSnapshot_Concurrent_NoLostRecords(t *testing.T) {
	a, store := newSnapshotTestAdapter(t, nil)

	const n = 8
	ids := make([]string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			snap, err := a.CreateSnapshot(context.Background(), "e2b-test-1", &adapter.SnapshotRequest{Name: fmt.Sprintf("snap-%d", i)})
			if err != nil {
				t.Errorf("CreateSnapshot #%d: %v", i, err)
				return
			}
			ids[i] = snap.SnapshotID
		}(i)
	}
	wg.Wait()

	if store.conflicts == 0 {
		t.Error("expected at least one optimistic-concurrency conflict under contention")
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	var stored []snapshotRecord
	_ = json.Unmarshal([]byte(store.sb.Annotations[annotationSnapshots]), &stored)
	storedIDs := make(map[string]bool, len(stored))
	for _, rec := range stored {
		storedIDs[rec.SnapshotID] = true
	}
	for i, id := range ids {
		if id == "" {
			t.Fatalf("snapshot #%d returned no ID", i)
		}
		if !storedIDs[id] {
			t.Errorf("snapshot %q (from call #%d) was dropped by concurrent creates", id, i)
		}
	}
	if len(stored) != n {
		t.Errorf("expected %d distinct stored snapshots, got %d", n, len(stored))
	}
}

// TestListSnapshots_CorruptedAnnotationDegrades verifies the graceful
// degradation path: a corrupted annotation yields an empty list instead of
// blocking listing forever.
func TestListSnapshots_CorruptedAnnotationDegrades(t *testing.T) {
	a, _ := newSnapshotTestAdapter(t, map[string]string{annotationSnapshots: "{not valid json"})

	snapshots, err := a.ListSnapshots(context.Background(), "e2b-test-1")
	if err != nil {
		t.Fatalf("ListSnapshots with corrupted annotation: %v", err)
	}
	if len(snapshots) != 0 {
		t.Errorf("expected empty snapshot list, got %d", len(snapshots))
	}
}

// TestCreateSnapshot_CorruptedAnnotationErrors verifies that creation does
// NOT silently overwrite a corrupted annotation: it fails with an explicit
// error and leaves the stored value untouched.
func TestCreateSnapshot_CorruptedAnnotationErrors(t *testing.T) {
	raw := "{not valid json"
	a, store := newSnapshotTestAdapter(t, map[string]string{annotationSnapshots: raw})

	if _, err := a.CreateSnapshot(context.Background(), "e2b-test-1", &adapter.SnapshotRequest{Name: "x"}); err == nil {
		t.Fatal("expected explicit error for corrupted annotation, got nil")
	}
	if store.updates != 0 {
		t.Errorf("create must not attempt to write over corrupted data, got %d updates", store.updates)
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if store.sb.Annotations[annotationSnapshots] != raw {
		t.Errorf("annotation was modified: %q", store.sb.Annotations[annotationSnapshots])
	}
}

// TestPauseSandbox_SuspendsViaSDK verifies that pausing goes through the
// SDK handle's Suspend (kubernetes-sigs/agent-sandbox#2256) and that a
// second pause is a no-op on the already-suspended CR.
func TestPauseSandbox_SuspendsViaSDK(t *testing.T) {
	a, store := newSnapshotTestAdapter(t, nil)

	if err := a.PauseSandbox(context.Background(), "e2b-test-1"); err != nil {
		t.Fatalf("PauseSandbox: %v", err)
	}

	store.mu.Lock()
	mode := store.sb.Spec.OperatingMode
	store.mu.Unlock()
	if mode != sandboxv1beta1.SandboxOperatingModeSuspended {
		t.Errorf("operatingMode = %q, want Suspended", mode)
	}

	// Second pause must short-circuit on the CR read, not the handle.
	if err := a.PauseSandbox(context.Background(), "e2b-test-1"); err != nil {
		t.Fatalf("second PauseSandbox: %v", err)
	}
}

// TestResumeSandbox_UsesSDKResume verifies that resuming a suspended sandbox
// goes through the SDK handle's Resume, which restores Running and readiness.
func TestResumeSandbox_UsesSDKResume(t *testing.T) {
	a, store := newSnapshotTestAdapter(t, nil)
	store.mu.Lock()
	store.sb.Spec.OperatingMode = sandboxv1beta1.SandboxOperatingModeSuspended
	store.mu.Unlock()

	sbx, err := a.ResumeSandbox(context.Background(), "e2b-test-1")
	if err != nil {
		t.Fatalf("ResumeSandbox: %v", err)
	}

	store.mu.Lock()
	mode := store.sb.Spec.OperatingMode
	store.mu.Unlock()
	if mode != sandboxv1beta1.SandboxOperatingModeRunning {
		t.Errorf("operatingMode = %q, want Running", mode)
	}
	if sbx.Status != adapter.SandboxStatusRunning {
		t.Errorf("status = %q, want running", sbx.Status)
	}
}

// TestResumeSandbox_FallbackWhenHandleUnavailable verifies the restart path:
// when no SDK handle can be attached (the client's registry is empty and the
// pod is gone), the CR is patched directly and the response reports the
// sandbox as starting rather than failing.
func TestResumeSandbox_FallbackWhenHandleUnavailable(t *testing.T) {
	a, store := newSnapshotTestAdapter(t, nil)
	store.mu.Lock()
	store.sb.Spec.OperatingMode = sandboxv1beta1.SandboxOperatingModeSuspended
	store.mu.Unlock()

	a.lifecycleHandleFn = func(ctx context.Context, sandboxID string) (suspendResumeHandle, error) {
		return nil, errors.New("no handle: fresh gateway process")
	}

	sbx, err := a.ResumeSandbox(context.Background(), "e2b-test-1")
	if err != nil {
		t.Fatalf("ResumeSandbox fallback: %v", err)
	}

	store.mu.Lock()
	mode := store.sb.Spec.OperatingMode
	store.mu.Unlock()
	if mode != sandboxv1beta1.SandboxOperatingModeRunning {
		t.Errorf("operatingMode = %q, want Running after fallback patch", mode)
	}
	// The replacement pod has not come up yet, so the honest status is
	// starting, not running.
	if sbx.Status != adapter.SandboxStatusStarting {
		t.Errorf("status = %q, want starting", sbx.Status)
	}
}

// TestCreateSnapshot_SuspendsSandbox verifies that snapshot creation suspends
// the sandbox (E2B semantics: snapshotting pauses the sandbox) before the
// record is appended.
func TestCreateSnapshot_SuspendsSandbox(t *testing.T) {
	a, store := newSnapshotTestAdapter(t, nil)

	if _, err := a.CreateSnapshot(context.Background(), "e2b-test-1", &adapter.SnapshotRequest{Name: "snap"}); err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}

	store.mu.Lock()
	mode := store.sb.Spec.OperatingMode
	store.mu.Unlock()
	if mode != sandboxv1beta1.SandboxOperatingModeSuspended {
		t.Errorf("operatingMode = %q, want Suspended after snapshot", mode)
	}

	snapshots, err := a.ListSnapshots(context.Background(), "e2b-test-1")
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(snapshots) != 1 {
		t.Errorf("expected 1 snapshot record, got %d", len(snapshots))
	}
}
