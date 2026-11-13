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
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"
	sandboxv1beta1 "sigs.k8s.io/agent-sandbox/api/v1beta1"
	"sigs.k8s.io/agent-sandbox/clients/go/sandbox"
	agentsfake "sigs.k8s.io/agent-sandbox/clients/k8s/clientset/versioned/fake"
	extfake "sigs.k8s.io/agent-sandbox/clients/k8s/extensions/clientset/versioned/fake"
	extv1beta1 "sigs.k8s.io/agent-sandbox/extensions/api/v1beta1"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
)

// newWarmPoolTestAdapter builds an Adapter backed by a fake extensions
// clientset, mirroring how the production Adapter reaches the API server
// through k8s.ExtensionsClient.
func newWarmPoolTestAdapter(t *testing.T, objs ...runtime.Object) (*Adapter, *extfake.Clientset) {
	t.Helper()
	cs := extfake.NewSimpleClientset(objs...)
	a := &Adapter{
		name:                      "agentsandbox",
		namespace:                 "default",
		k8s:                       &sandbox.K8sHelper{ExtensionsClient: cs.ExtensionsV1beta1()},
		warmPoolMap:               make(map[string]string),
		warmPoolConfig:            make(map[string]string),
		templateToSandboxTemplate: make(map[string]string),
		maxWarmPoolSize:           256,
	}
	return a, cs
}

func sandboxTemplate(name string) *extv1beta1.SandboxTemplate {
	return &extv1beta1.SandboxTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
	}
}

// warmPoolCRD builds an annotated SandboxWarmPool as created by
// CreateWarmPool, with the given creation time (stamped both as the K8s
// CreationTimestamp and as the gateway's RFC3339Nano annotation).
func warmPoolCRD(name, templateRef string, replicas int32, created time.Time) *extv1beta1.SandboxWarmPool {
	r := replicas
	return &extv1beta1.SandboxWarmPool{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         "default",
			CreationTimestamp: metav1.NewTime(created),
			Annotations: map[string]string{
				warmPoolTemplateAnnotation:  templateRef,
				warmPoolCreatedAtAnnotation: created.UTC().Format(time.RFC3339Nano),
			},
		},
		Spec: extv1beta1.SandboxWarmPoolSpec{
			Replicas:    &r,
			TemplateRef: extv1beta1.SandboxTemplateRef{Name: templateRef},
		},
	}
}

func TestCreateWarmPool_Success(t *testing.T) {
	a, cs := newWarmPoolTestAdapter(t, sandboxTemplate("base"))

	wp, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "base", TargetSize: 3, WarmPoolID: "wp-base-test",
	})
	require.NoError(t, err)
	assert.Equal(t, "wp-base-test", wp.WarmPoolID)
	assert.Equal(t, "base", wp.TemplateID)
	assert.Equal(t, 3, wp.TargetSize)

	// CRD is actually written with the right fields.
	got, err := cs.ExtensionsV1beta1().SandboxWarmPools("default").
		Get(context.Background(), "wp-base-test", metav1.GetOptions{})
	require.NoError(t, err, "CRD not created")
	require.NotNil(t, got.Spec.Replicas)
	assert.Equal(t, int32(3), *got.Spec.Replicas)
	assert.Equal(t, "base", got.Spec.TemplateRef.Name)

	// Runtime warmPoolMap is updated so the new pool is immediately
	// consumable by CreateSandbox without a gateway restart.
	assert.Equal(t, "wp-base-test", a.warmPoolMap["base"])
}

func TestCreateWarmPool_GeneratesValidID(t *testing.T) {
	a, _ := newWarmPoolTestAdapter(t, sandboxTemplate("base"))

	wp, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "base", TargetSize: 1,
	})
	require.NoError(t, err)
	assert.NoError(t, adapter.ValidateWarmPoolID(wp.WarmPoolID))
}

func TestCreateWarmPool_TemplateMissing(t *testing.T) {
	a, _ := newWarmPoolTestAdapter(t) // no SandboxTemplate registered

	_, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "ghost", TargetSize: 1,
	})
	require.Error(t, err, "want template-not-found error")
}

func TestCreateWarmPool_Duplicate(t *testing.T) {
	a, _ := newWarmPoolTestAdapter(t, sandboxTemplate("base"))
	req := &adapter.WarmPoolCreateRequest{TemplateID: "base", TargetSize: 1, WarmPoolID: "wp-dup"}
	_, err := a.CreateWarmPool(context.Background(), req)
	require.NoError(t, err, "first create")

	_, err = a.CreateWarmPool(context.Background(), req)
	assert.ErrorIs(t, err, adapter.ErrWarmPoolAlreadyExists)
}

func TestCreateWarmPool_ClampsSize(t *testing.T) {
	a, cs := newWarmPoolTestAdapter(t, sandboxTemplate("base"))

	wp, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "base", TargetSize: 100000, WarmPoolID: "wp-clamp",
	})
	require.NoError(t, err)
	assert.Equal(t, 256, wp.TargetSize)

	got, err := cs.ExtensionsV1beta1().SandboxWarmPools("default").
		Get(context.Background(), "wp-clamp", metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, got.Spec.Replicas)
	assert.Equal(t, int32(256), *got.Spec.Replicas)
}

func TestCreateWarmPool_RejectsNegativeSize(t *testing.T) {
	a, _ := newWarmPoolTestAdapter(t, sandboxTemplate("base"))

	_, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "base", TargetSize: -1, WarmPoolID: "wp-neg",
	})
	require.Error(t, err)
}

func TestCreateWarmPool_InvalidExplicitID(t *testing.T) {
	a, _ := newWarmPoolTestAdapter(t, sandboxTemplate("base"))

	_, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "base", TargetSize: 1, WarmPoolID: "Bad ID",
	})
	require.Error(t, err, "want invalid-ID error")
}

func TestCreateWarmPool_MissingTemplateID(t *testing.T) {
	a, _ := newWarmPoolTestAdapter(t)

	_, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{TargetSize: 1})
	require.Error(t, err)
	_, err = a.CreateWarmPool(context.Background(), nil)
	require.Error(t, err)
}

func TestCreateWarmPool_TemplateMapping(t *testing.T) {
	a, _ := newWarmPoolTestAdapter(t, sandboxTemplate("real-tmpl"))
	a.templateToSandboxTemplate["e2b-tmpl"] = "real-tmpl"

	wp, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "e2b-tmpl", TargetSize: 1, WarmPoolID: "wp-mapped",
	})
	require.NoError(t, err)
	assert.Equal(t, "e2b-tmpl", wp.TemplateID, "want the E2B-side ID")
}

func TestCreateWarmPool_UpdateStrategyApplied(t *testing.T) {
	a, cs := newWarmPoolTestAdapter(t, sandboxTemplate("base"))
	a.warmPoolUpdateStrategy = "Recreate"

	_, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "base", TargetSize: 1, WarmPoolID: "wp-strategy",
	})
	require.NoError(t, err)

	got, err := cs.ExtensionsV1beta1().SandboxWarmPools("default").
		Get(context.Background(), "wp-strategy", metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, got.Spec.UpdateStrategy)
	assert.Equal(t, extv1beta1.RecreateSandboxWarmPoolUpdateStrategyType, got.Spec.UpdateStrategy.Type)
}

func TestGetWarmPool_Found(t *testing.T) {
	replicas := int32(3)
	swp := &extv1beta1.SandboxWarmPool{
		ObjectMeta: metav1.ObjectMeta{Name: "wp-1", Namespace: "default", Generation: 1},
		Spec: extv1beta1.SandboxWarmPoolSpec{
			Replicas:    &replicas,
			TemplateRef: extv1beta1.SandboxTemplateRef{Name: "base"},
		},
		Status: extv1beta1.SandboxWarmPoolStatus{ReadyReplicas: 3, ObservedGeneration: 1},
	}
	a, _ := newWarmPoolTestAdapter(t, swp)

	wp, err := a.GetWarmPool(context.Background(), "wp-1")
	require.NoError(t, err)
	assert.Equal(t, adapter.WarmPoolStatusReady, wp.Status)
	assert.Equal(t, 3, wp.CurrentSize)
	assert.Equal(t, 3, wp.TargetSize)
	assert.Equal(t, "base", wp.TemplateID)
}

func TestGetWarmPool_NotFound(t *testing.T) {
	a, _ := newWarmPoolTestAdapter(t)

	_, err := a.GetWarmPool(context.Background(), "nope")
	assert.ErrorIs(t, err, adapter.ErrWarmPoolNotFound)
}

func TestWarmPoolStatus_Derivation(t *testing.T) {
	ptr := func(i int32) *int32 { return &i }
	tests := []struct {
		name   string
		spec   *int32
		gen    int64
		ready  int32
		obsGen int64
		want   string
	}{
		{"zero replicas is ready", ptr(0), 1, 0, 1, adapter.WarmPoolStatusReady},
		{"nil replicas is ready", nil, 1, 0, 1, adapter.WarmPoolStatusReady},
		{"zero replicas not yet observed", ptr(0), 2, 3, 1, adapter.WarmPoolStatusPending},
		{"not yet observed", ptr(3), 2, 0, 1, adapter.WarmPoolStatusPending},
		{"all ready", ptr(3), 1, 3, 1, adapter.WarmPoolStatusReady},
		{"partially ready", ptr(3), 1, 1, 1, adapter.WarmPoolStatusScaling},
		{"none ready", ptr(3), 1, 0, 1, adapter.WarmPoolStatusPending},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			swp := &extv1beta1.SandboxWarmPool{
				ObjectMeta: metav1.ObjectMeta{Generation: tt.gen},
				Spec:       extv1beta1.SandboxWarmPoolSpec{Replicas: tt.spec},
				Status:     extv1beta1.SandboxWarmPoolStatus{ReadyReplicas: tt.ready, ObservedGeneration: tt.obsGen},
			}
			assert.Equal(t, tt.want, warmPoolStatus(swp))
		})
	}
}

func TestListWarmPools(t *testing.T) {
	now := time.Now()
	ready := warmPoolCRD("wp-a", "base", 2, now)
	ready.Generation = 1
	ready.Status = extv1beta1.SandboxWarmPoolStatus{ReadyReplicas: 2, ObservedGeneration: 1}
	filling := warmPoolCRD("wp-b", "code", 2, now)
	filling.Generation = 1
	filling.Status = extv1beta1.SandboxWarmPoolStatus{ReadyReplicas: 0, ObservedGeneration: 1}

	a, _ := newWarmPoolTestAdapter(t, ready, filling)
	pools, err := a.ListWarmPools(context.Background())
	require.NoError(t, err)
	require.Len(t, pools, 2)

	byID := map[string]*adapter.WarmPool{pools[0].WarmPoolID: pools[0], pools[1].WarmPoolID: pools[1]}
	assert.Equal(t, adapter.WarmPoolStatusReady, byID["wp-a"].Status)
	assert.Equal(t, adapter.WarmPoolStatusPending, byID["wp-b"].Status)
}

func TestListWarmPools_Empty(t *testing.T) {
	a, _ := newWarmPoolTestAdapter(t)

	pools, err := a.ListWarmPools(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, pools)
	assert.Empty(t, pools)
}

func TestDeleteWarmPool(t *testing.T) {
	a, cs := newWarmPoolTestAdapter(t, warmPoolCRD("wp-del", "base", 1, time.Now()))
	a.warmPoolMap["base"] = "wp-del"

	require.NoError(t, a.DeleteWarmPool(context.Background(), "wp-del", nil))

	_, err := cs.ExtensionsV1beta1().SandboxWarmPools("default").
		Get(context.Background(), "wp-del", metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "CRD still exists after delete (err = %v)", err)

	// Consumption mapping is cleaned up so CreateSandbox stops routing to
	// the deleted pool.
	_, stillMapped := a.warmPoolMap["base"]
	assert.False(t, stillMapped, "warmPoolMap still references deleted pool")
}

func TestDeleteWarmPool_NotFound(t *testing.T) {
	a, _ := newWarmPoolTestAdapter(t)

	err := a.DeleteWarmPool(context.Background(), "nope", nil)
	assert.ErrorIs(t, err, adapter.ErrWarmPoolNotFound)
}

func TestUpdateWarmPoolSize(t *testing.T) {
	a, cs := newWarmPoolTestAdapter(t, warmPoolCRD("wp-up", "base", 1, time.Now()))

	require.NoError(t, a.UpdateWarmPoolSize(context.Background(), "wp-up", 5))

	got, err := cs.ExtensionsV1beta1().SandboxWarmPools("default").
		Get(context.Background(), "wp-up", metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, got.Spec.Replicas)
	assert.Equal(t, int32(5), *got.Spec.Replicas)
}

func TestUpdateWarmPoolSize_Clamps(t *testing.T) {
	a, cs := newWarmPoolTestAdapter(t, warmPoolCRD("wp-clamp2", "base", 1, time.Now()))

	require.NoError(t, a.UpdateWarmPoolSize(context.Background(), "wp-clamp2", 100000))

	got, err := cs.ExtensionsV1beta1().SandboxWarmPools("default").
		Get(context.Background(), "wp-clamp2", metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, got.Spec.Replicas)
	assert.Equal(t, int32(256), *got.Spec.Replicas)
}

func TestUpdateWarmPoolSize_NegativeRejected(t *testing.T) {
	a, _ := newWarmPoolTestAdapter(t)

	assert.Error(t, a.UpdateWarmPoolSize(context.Background(), "wp-up", -1))
}

func TestUpdateWarmPoolSize_NotFound(t *testing.T) {
	a, _ := newWarmPoolTestAdapter(t)

	err := a.UpdateWarmPoolSize(context.Background(), "nope", 3)
	assert.ErrorIs(t, err, adapter.ErrWarmPoolNotFound)
}

// --- Review remediation tests (PR #82) ---

func TestCreateWarmPool_AnnotationPreservesTemplateIdentity(t *testing.T) {
	a, cs := newWarmPoolTestAdapter(t, sandboxTemplate("real-tmpl"))
	// Ambiguous mapping: two E2B template IDs → one SandboxTemplate.
	a.templateToSandboxTemplate["template-a"] = "real-tmpl"
	a.templateToSandboxTemplate["template-b"] = "real-tmpl"

	_, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "template-a", TargetSize: 1, WarmPoolID: "wp-identity",
	})
	require.NoError(t, err)

	// The CRD carries the E2B template ID as an annotation.
	got, err := cs.ExtensionsV1beta1().SandboxWarmPools("default").
		Get(context.Background(), "wp-identity", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "template-a", got.Annotations[warmPoolTemplateAnnotation])

	// Read paths report the annotated ID, not an arbitrary reverse-map hit.
	wp, err := a.GetWarmPool(context.Background(), "wp-identity")
	require.NoError(t, err)
	assert.Equal(t, "template-a", wp.TemplateID,
		"ambiguous reverse mapping must not change the pool's template identity")
}

func TestResolveTemplateRef(t *testing.T) {
	tests := []struct {
		name       string
		mapping    map[string]string
		templateID string
		want       string
	}{
		{
			name:       "exact match wins",
			mapping:    map[string]string{"MyTemplate": "exact-tmpl", "mytemplate": "lower-tmpl"},
			templateID: "MyTemplate",
			want:       "exact-tmpl",
		},
		{
			name:       "lowercased fallback (Viper lowercases config map keys)",
			mapping:    map[string]string{"mytemplate": "lower-tmpl"},
			templateID: "MyTemplate",
			want:       "lower-tmpl",
		},
		{
			name:       "unmapped falls back to the ID itself",
			mapping:    map[string]string{"other": "x"},
			templateID: "plain",
			want:       "plain",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, _ := newWarmPoolTestAdapter(t)
			a.templateToSandboxTemplate = tt.mapping
			assert.Equal(t, tt.want, a.resolveTemplateRef(tt.templateID))
		})
	}
}

func TestRestoreWarmPoolRouting_ConfigWinsAndSkipsUnannotated(t *testing.T) {
	annotated := &extv1beta1.SandboxWarmPool{
		ObjectMeta: metav1.ObjectMeta{
			Name: "wp-created", Namespace: "default",
			Annotations: map[string]string{warmPoolTemplateAnnotation: "tmpl-x"},
		},
		Spec: extv1beta1.SandboxWarmPoolSpec{TemplateRef: extv1beta1.SandboxTemplateRef{Name: "tmpl-x"}},
	}
	plain := &extv1beta1.SandboxWarmPool{
		ObjectMeta: metav1.ObjectMeta{Name: "wp-operator", Namespace: "default"},
		Spec:       extv1beta1.SandboxWarmPoolSpec{TemplateRef: extv1beta1.SandboxTemplateRef{Name: "ops-tmpl"}},
	}
	a, _ := newWarmPoolTestAdapter(t, annotated, plain)
	// Config-provided entry that must take precedence over restored ones.
	a.warmPoolConfig["tmpl-x"] = "wp-from-config"
	a.warmPoolMap["tmpl-x"] = "wp-from-config"

	a.refreshWarmPoolRouting(context.Background())

	assert.Equal(t, "wp-from-config", a.warmPoolMap["tmpl-x"], "config entry must not be overwritten")
	for tmpl, pool := range a.warmPoolMap {
		assert.NotEqual(t, "wp-operator", pool,
			"operator pool without annotation must not be routed (template %q)", tmpl)
	}
}

func TestRestoreWarmPoolRouting_AddsAnnotated(t *testing.T) {
	annotated := &extv1beta1.SandboxWarmPool{
		ObjectMeta: metav1.ObjectMeta{
			Name: "wp-created", Namespace: "default",
			Annotations: map[string]string{warmPoolTemplateAnnotation: "tmpl-y"},
		},
		Spec: extv1beta1.SandboxWarmPoolSpec{TemplateRef: extv1beta1.SandboxTemplateRef{Name: "tmpl-y"}},
	}
	a, _ := newWarmPoolTestAdapter(t, annotated)

	a.refreshWarmPoolRouting(context.Background())

	assert.Equal(t, "wp-created", a.warmPoolMap["tmpl-y"])
}

func TestRestoreWarmPoolRouting_PrefersNewestPool(t *testing.T) {
	base := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	wpOld := warmPoolCRD("wp-old", "base", 0, base)
	wpNew := warmPoolCRD("wp-new", "base", 10, base.Add(time.Hour))

	// Listed oldest-first and newest-first: restored routing must not depend
	// on List ordering when a template has several CRUD-created pools.
	for name, objs := range map[string][]runtime.Object{
		"old first": {wpOld, wpNew},
		"new first": {wpNew, wpOld},
	} {
		t.Run(name, func(t *testing.T) {
			a, _ := newWarmPoolTestAdapter(t, objs...)
			a.refreshWarmPoolRouting(context.Background())
			assert.Equal(t, "wp-new", a.warmPoolMap["base"],
				"restore must deterministically pick the most recently created pool")
		})
	}
}

func TestWarmPoolCRUD_ConcurrentCreateDeleteSmoke(t *testing.T) {
	a, _ := newWarmPoolTestAdapter(t, sandboxTemplate("base"))
	ctx := context.Background()
	done := make(chan struct{})
	for range 4 {
		go func() {
			defer func() { done <- struct{}{} }()
			for range 10 {
				_, _ = a.CreateWarmPool(ctx, &adapter.WarmPoolCreateRequest{
					TemplateID: "base", TargetSize: 1, WarmPoolID: "wp-race",
				})
				_ = a.DeleteWarmPool(ctx, "wp-race", nil)
				_, _ = a.ListWarmPools(ctx)
			}
		}()
	}
	for range 4 {
		<-done
	}
}

func TestRestoreWarmPoolRouting_NanosecondStampBeatsSecondPrecision(t *testing.T) {
	// metav1.Time serializes at second precision: two pools created within
	// the same second have identical CreationTimestamps. The gateway's
	// RFC3339Nano annotation must still identify the later one.
	sameSecond := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	wpA := warmPoolCRD("wp-a", "base", 0, sameSecond)
	wpA.Annotations[warmPoolCreatedAtAnnotation] = sameSecond.Format(time.RFC3339Nano)
	wpZ := warmPoolCRD("wp-z", "base", 10, sameSecond) // same second...
	wpZ.Annotations[warmPoolCreatedAtAnnotation] = sameSecond.Add(500 * time.Millisecond).Format(time.RFC3339Nano)

	for name, objs := range map[string][]runtime.Object{
		"a first": {wpA, wpZ},
		"z first": {wpZ, wpA},
	} {
		t.Run(name, func(t *testing.T) {
			a, _ := newWarmPoolTestAdapter(t, objs...)
			a.refreshWarmPoolRouting(context.Background())
			assert.Equal(t, "wp-z", a.warmPoolMap["base"],
				"the pool with the later gateway stamp must win regardless of list order")
		})
	}
}

func TestRestoreWarmPoolRouting_ExactTieBreaksByName(t *testing.T) {
	// Pathological case: identical stamps down to the nanosecond (or both
	// missing the annotation with same-second CreationTimestamps). The pick
	// must still be deterministic across list orders.
	ts := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	wpA := warmPoolCRD("wp-a", "base", 1, ts)
	wpB := warmPoolCRD("wp-b", "base", 1, ts)

	for name, objs := range map[string][]runtime.Object{
		"a first": {wpA, wpB},
		"b first": {wpB, wpA},
	} {
		t.Run(name, func(t *testing.T) {
			a, _ := newWarmPoolTestAdapter(t, objs...)
			a.refreshWarmPoolRouting(context.Background())
			assert.Equal(t, "wp-b", a.warmPoolMap["base"],
				"exact ties must break deterministically (lexicographically greater name)")
		})
	}
}

func TestRestoreWarmPoolRouting_FallsBackToCreationTimestamp(t *testing.T) {
	// Pools created before the stamp annotation existed carry only the K8s
	// CreationTimestamp; restore must still order them correctly.
	base := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	mkLegacy := func(name string, created time.Time) *extv1beta1.SandboxWarmPool {
		p := warmPoolCRD(name, "base", 1, created)
		delete(p.Annotations, warmPoolCreatedAtAnnotation)
		return p
	}
	wpOld := mkLegacy("wp-old", base)
	wpNew := mkLegacy("wp-new", base.Add(time.Hour))

	a, _ := newWarmPoolTestAdapter(t, wpNew, wpOld)
	a.refreshWarmPoolRouting(context.Background())
	assert.Equal(t, "wp-new", a.warmPoolMap["base"])
}

// --- Round-3 review remediation tests (PR #82) ---

func TestRefreshWarmPoolRouting_PicksUpExternalChanges(t *testing.T) {
	ctx := context.Background()
	a, cs := newWarmPoolTestAdapter(t)
	ext := cs.ExtensionsV1beta1().SandboxWarmPools("default")

	// Another gateway replica creates a pool directly on the API server.
	_, err := ext.Create(ctx, warmPoolCRD("wp-ext", "base", 2, time.Now()), metav1.CreateOptions{})
	require.NoError(t, err)

	a.refreshWarmPoolRouting(ctx)
	assert.Equal(t, "wp-ext", a.warmPoolMap["base"],
		"routing must converge on pools created through other replicas")

	// The other replica deletes it again.
	require.NoError(t, ext.Delete(ctx, "wp-ext", metav1.DeleteOptions{}))
	a.refreshWarmPoolRouting(ctx)
	_, stillRouted := a.warmPoolMap["base"]
	assert.False(t, stillRouted, "stale route to a deleted pool must be dropped")
}

func TestResolveWarmPool_RefreshesWhenStale(t *testing.T) {
	ctx := context.Background()
	a, cs := newWarmPoolTestAdapter(t)
	// Routing loaded long ago (zero time is far past the TTL).
	ext := cs.ExtensionsV1beta1().SandboxWarmPools("default")
	_, err := ext.Create(ctx, warmPoolCRD("wp-ttl", "base", 1, time.Now()), metav1.CreateOptions{})
	require.NoError(t, err)

	got := a.resolveWarmPool(ctx, "base")
	assert.Equal(t, "wp-ttl", got, "stale routing must refresh before resolving")
	assert.WithinDuration(t, time.Now(), a.warmPoolRoutingAt, time.Minute)

	// Fresh routing does not re-list: delete behind the scenes and resolve
	// again — the cached route is still returned until the TTL expires.
	require.NoError(t, ext.Delete(ctx, "wp-ttl", metav1.DeleteOptions{}))
	assert.Equal(t, "wp-ttl", a.resolveWarmPool(ctx, "base"))
}

func TestDeleteWarmPool_ReroutesToRemainingPool(t *testing.T) {
	ctx := context.Background()
	a, _ := newWarmPoolTestAdapter(t, sandboxTemplate("base"))

	_, err := a.CreateWarmPool(ctx, &adapter.WarmPoolCreateRequest{
		TemplateID: "base", TargetSize: 0, WarmPoolID: "wp-old",
	})
	require.NoError(t, err)
	// Ensure distinct creation stamps regardless of clock resolution.
	time.Sleep(2 * time.Millisecond)
	_, err = a.CreateWarmPool(ctx, &adapter.WarmPoolCreateRequest{
		TemplateID: "base", TargetSize: 10, WarmPoolID: "wp-new",
	})
	require.NoError(t, err)
	require.Equal(t, "wp-new", a.warmPoolMap["base"])

	// Deleting the routed pool must fall back to the surviving pool for the
	// same template, not to the raw template ID.
	require.NoError(t, a.DeleteWarmPool(ctx, "wp-new", nil))
	assert.Equal(t, "wp-old", a.warmPoolMap["base"])
}

func TestCreateWarmPool_ConfigRouteNotDisplaced(t *testing.T) {
	ctx := context.Background()
	a, _ := newWarmPoolTestAdapter(t, sandboxTemplate("base"))
	// Operator pinned this template to a specific pool via config.
	a.warmPoolConfig["base"] = "wp-pinned"
	a.warmPoolMap["base"] = "wp-pinned"

	_, err := a.CreateWarmPool(ctx, &adapter.WarmPoolCreateRequest{
		TemplateID: "base", TargetSize: 1, WarmPoolID: "wp-crud",
	})
	require.NoError(t, err)
	assert.Equal(t, "wp-pinned", a.warmPoolMap["base"],
		"config overrides must not be displaced by CRUD-created pools")
}

func TestDeleteWarmPool_DropsCachedRoutesWhenRefreshFails(t *testing.T) {
	a, cs := newWarmPoolTestAdapter(t, warmPoolCRD("wp-1", "base", 2, time.Now()))
	a.warmPoolMap["base"] = "wp-1"

	// Fail the post-delete routing refresh (List). The delete itself uses the
	// "delete" verb and stays successful.
	cs.PrependReactor("list", "sandboxwarmpools", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewInternalError(errors.New("list boom"))
	})

	require.NoError(t, a.DeleteWarmPool(context.Background(), "wp-1", nil))
	_, stillRouted := a.warmPoolMap["base"]
	assert.False(t, stillRouted,
		"cache must not keep pointing at the deleted pool when the refresh fails")
}

func TestResolveWarmPool_BackoffAfterFailedRefresh(t *testing.T) {
	a, cs := newWarmPoolTestAdapter(t)
	var lists atomic.Int32
	cs.PrependReactor("list", "sandboxwarmpools", func(k8stesting.Action) (bool, runtime.Object, error) {
		lists.Add(1)
		return true, nil, apierrors.NewInternalError(errors.New("list boom"))
	})
	// Make the routing cache stale so resolveWarmPool wants to refresh.
	a.warmPoolMapMu.Lock()
	a.warmPoolRoutingAt = time.Now().Add(-2 * warmPoolRoutingTTL)
	a.warmPoolMapMu.Unlock()

	_ = a.resolveWarmPool(context.Background(), "base")
	assert.Equal(t, int32(1), lists.Load(), "first stale resolve attempts a refresh")

	_ = a.resolveWarmPool(context.Background(), "base")
	assert.Equal(t, int32(1), lists.Load(),
		"refresh within the failure backoff window must not re-list synchronously")

	// Simulate the backoff window elapsing.
	a.warmPoolMapMu.Lock()
	a.warmPoolRoutingLastAttempt = time.Now().Add(-2 * warmPoolRoutingRetryBackoff)
	a.warmPoolMapMu.Unlock()

	_ = a.resolveWarmPool(context.Background(), "base")
	assert.Equal(t, int32(2), lists.Load(), "refresh retries once the backoff has elapsed")
}

// newWarmPoolForceTestAdapter extends newWarmPoolTestAdapter with a fake
// agents clientset so force-delete tests can seed pool Sandboxes.
//
// Sandboxes are seeded via Create, not the NewSimpleClientset constructor:
// the client-go v0.37 tracker pluralizes the "Sandbox" kind as "sandboxs"
// when presetting objects, so constructor-seeded sandboxes are unreachable
// through the generated client (which requests "sandboxes"). Create goes
// through the real GVR.
func newWarmPoolForceTestAdapter(t *testing.T, extObjs []runtime.Object, sbObjs ...*sandboxv1beta1.Sandbox) (*Adapter, *extfake.Clientset, *agentsfake.Clientset) {
	t.Helper()
	a, ecs := newWarmPoolTestAdapter(t, extObjs...)
	acs := agentsfake.NewSimpleClientset()
	for _, sb := range sbObjs {
		_, err := acs.AgentsV1beta1().Sandboxes(sb.Namespace).
			Create(context.Background(), sb, metav1.CreateOptions{})
		require.NoError(t, err, "seeding sandbox %s", sb.Name)
	}
	a.k8s.AgentsClient = acs.AgentsV1beta1()
	return a, ecs, acs
}

// poolSandbox builds a Sandbox labeled as a member of poolID. controllerKind
// "" leaves it an orphan; "SandboxWarmPool" marks it pool-owned (unclaimed);
// "SandboxClaim" marks it claimed (adopted — the claim controller re-parents
// adopted sandboxes).
func poolSandbox(name, poolID, controllerKind string) *sandboxv1beta1.Sandbox {
	sb := &sandboxv1beta1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			Labels: map[string]string{
				sandboxv1beta1.SandboxWarmPoolLabel: warmPoolNameHash(poolID),
			},
		},
	}
	if controllerKind != "" {
		isController := true
		sb.OwnerReferences = []metav1.OwnerReference{{
			APIVersion: "agents.x-k8s.io/v1beta1",
			Kind:       controllerKind,
			Name:       poolID,
			UID:        "uid-1",
			Controller: &isController,
		}}
	}
	return sb
}

func TestDeleteWarmPool_ForceDeletesUnclaimedSandboxes(t *testing.T) {
	pool := warmPoolCRD("wp-1", "base", 3, time.Now())
	a, _, acs := newWarmPoolForceTestAdapter(t,
		[]runtime.Object{pool},
		poolSandbox("sb-pool", "wp-1", "SandboxWarmPool"),
		poolSandbox("sb-orphan", "wp-1", ""),
		poolSandbox("sb-claimed", "wp-1", "SandboxClaim"),
	)
	a.warmPoolMap["base"] = "wp-1"

	require.NoError(t, a.DeleteWarmPool(context.Background(), "wp-1",
		&adapter.WarmPoolDeleteOptions{Force: true}))

	sandboxes := acs.AgentsV1beta1().Sandboxes("default")
	_, err := sandboxes.Get(context.Background(), "sb-pool", metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "pool-owned unclaimed sandbox must be deleted")
	_, err = sandboxes.Get(context.Background(), "sb-orphan", metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "orphaned unclaimed sandbox must be deleted")
	_, err = sandboxes.Get(context.Background(), "sb-claimed", metav1.GetOptions{})
	assert.NoError(t, err, "claimed sandbox must never be touched")
}

func TestDeleteWarmPool_GracefulDoesNotDeleteSandboxesDirectly(t *testing.T) {
	pool := warmPoolCRD("wp-1", "base", 3, time.Now())
	a, _, acs := newWarmPoolForceTestAdapter(t,
		[]runtime.Object{pool},
		poolSandbox("sb-pool", "wp-1", "SandboxWarmPool"),
	)

	require.NoError(t, a.DeleteWarmPool(context.Background(), "wp-1", nil))

	_, err := acs.AgentsV1beta1().Sandboxes("default").
		Get(context.Background(), "sb-pool", metav1.GetOptions{})
	assert.NoError(t, err, "graceful delete leaves sandbox reclaim to the controller/GC")
}

func TestDeleteWarmPool_ForceUsesBackgroundPropagation(t *testing.T) {
	pool := warmPoolCRD("wp-1", "base", 1, time.Now())
	a, ecs, _ := newWarmPoolForceTestAdapter(t, []runtime.Object{pool})

	require.NoError(t, a.DeleteWarmPool(context.Background(), "wp-1",
		&adapter.WarmPoolDeleteOptions{Force: true}))

	for _, action := range ecs.Actions() {
		if action.GetVerb() == "delete" && action.GetResource().Resource == "sandboxwarmpools" {
			da := action.(k8stesting.DeleteActionImpl)
			require.NotNil(t, da.DeleteOptions.PropagationPolicy)
			assert.Equal(t, metav1.DeletePropagationBackground, *da.DeleteOptions.PropagationPolicy)
			return
		}
	}
	t.Fatal("no sandboxwarmpools delete action recorded")
}

func TestWarmPoolNameHash(t *testing.T) {
	// Pins compatibility with the agent-sandbox controller's NameHash
	// (FNV-1a 32 rendered as 8 lowercase hex chars), which produces the
	// agents.x-k8s.io/warm-pool-sandbox label value force delete selects on.
	assert.Equal(t, "a389afb4", warmPoolNameHash("wp-1"))
	assert.Equal(t, "244dc158", warmPoolNameHash("my-pool"))
}
