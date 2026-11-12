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
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/agent-sandbox/clients/go/sandbox"
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

func TestCreateWarmPool_Success(t *testing.T) {
	a, cs := newWarmPoolTestAdapter(t, sandboxTemplate("base"))
	wp, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "base", TargetSize: 3, WarmPoolID: "wp-base-test",
	})
	if err != nil {
		t.Fatalf("CreateWarmPool() error = %v", err)
	}
	if wp.WarmPoolID != "wp-base-test" || wp.TemplateID != "base" || wp.TargetSize != 3 {
		t.Errorf("CreateWarmPool() = %+v, want id=wp-base-test template=base size=3", wp)
	}
	// CRD is actually written with the right fields.
	got, err := cs.ExtensionsV1beta1().SandboxWarmPools("default").
		Get(context.Background(), "wp-base-test", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("CRD not created: %v", err)
	}
	if got.Spec.Replicas == nil || *got.Spec.Replicas != 3 {
		t.Errorf("spec.replicas = %v, want 3", got.Spec.Replicas)
	}
	if got.Spec.TemplateRef.Name != "base" {
		t.Errorf("spec.sandboxTemplateRef.name = %q, want base", got.Spec.TemplateRef.Name)
	}
	// Runtime warmPoolMap is updated so the new pool is immediately
	// consumable by CreateSandbox without a gateway restart.
	if a.warmPoolMap["base"] != "wp-base-test" {
		t.Errorf("warmPoolMap[base] = %q, want wp-base-test", a.warmPoolMap["base"])
	}
}

func TestCreateWarmPool_GeneratesValidID(t *testing.T) {
	a, _ := newWarmPoolTestAdapter(t, sandboxTemplate("base"))
	wp, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "base", TargetSize: 1,
	})
	if err != nil {
		t.Fatalf("CreateWarmPool() error = %v", err)
	}
	if err := adapter.ValidateWarmPoolID(wp.WarmPoolID); err != nil {
		t.Errorf("generated ID invalid: %v", err)
	}
}

func TestCreateWarmPool_TemplateMissing(t *testing.T) {
	a, _ := newWarmPoolTestAdapter(t) // no SandboxTemplate registered
	_, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "ghost", TargetSize: 1,
	})
	if err == nil {
		t.Fatal("CreateWarmPool() = nil error, want template-not-found error")
	}
}

func TestCreateWarmPool_Duplicate(t *testing.T) {
	a, _ := newWarmPoolTestAdapter(t, sandboxTemplate("base"))
	req := &adapter.WarmPoolCreateRequest{TemplateID: "base", TargetSize: 1, WarmPoolID: "wp-dup"}
	if _, err := a.CreateWarmPool(context.Background(), req); err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err := a.CreateWarmPool(context.Background(), req)
	if !errors.Is(err, adapter.ErrWarmPoolAlreadyExists) {
		t.Errorf("second create err = %v, want ErrWarmPoolAlreadyExists", err)
	}
}

func TestCreateWarmPool_ClampsSize(t *testing.T) {
	a, cs := newWarmPoolTestAdapter(t, sandboxTemplate("base"))
	wp, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "base", TargetSize: 100000, WarmPoolID: "wp-clamp",
	})
	if err != nil {
		t.Fatalf("CreateWarmPool() error = %v", err)
	}
	if wp.TargetSize != 256 {
		t.Errorf("TargetSize = %d, want 256", wp.TargetSize)
	}
	got, err := cs.ExtensionsV1beta1().SandboxWarmPools("default").
		Get(context.Background(), "wp-clamp", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if *got.Spec.Replicas != 256 {
		t.Errorf("spec.replicas = %d, want 256", *got.Spec.Replicas)
	}
}

func TestCreateWarmPool_RejectsNegativeSize(t *testing.T) {
	a, _ := newWarmPoolTestAdapter(t, sandboxTemplate("base"))
	_, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "base", TargetSize: -1, WarmPoolID: "wp-neg",
	})
	if err == nil {
		t.Fatal("CreateWarmPool(-1) = nil, want error")
	}
}

func TestCreateWarmPool_InvalidExplicitID(t *testing.T) {
	a, _ := newWarmPoolTestAdapter(t, sandboxTemplate("base"))
	_, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "base", TargetSize: 1, WarmPoolID: "Bad ID",
	})
	if err == nil {
		t.Fatal("CreateWarmPool() = nil, want invalid-ID error")
	}
}

func TestCreateWarmPool_MissingTemplateID(t *testing.T) {
	a, _ := newWarmPoolTestAdapter(t)
	if _, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{TargetSize: 1}); err == nil {
		t.Fatal("CreateWarmPool() without templateID = nil, want error")
	}
	if _, err := a.CreateWarmPool(context.Background(), nil); err == nil {
		t.Fatal("CreateWarmPool(nil) = nil, want error")
	}
}

func TestCreateWarmPool_TemplateMapping(t *testing.T) {
	a, _ := newWarmPoolTestAdapter(t, sandboxTemplate("real-tmpl"))
	a.templateToSandboxTemplate["e2b-tmpl"] = "real-tmpl"
	wp, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "e2b-tmpl", TargetSize: 1, WarmPoolID: "wp-mapped",
	})
	if err != nil {
		t.Fatalf("CreateWarmPool() error = %v", err)
	}
	if wp.TemplateID != "e2b-tmpl" {
		t.Errorf("TemplateID = %q, want e2b-tmpl (E2B side)", wp.TemplateID)
	}
}

func TestCreateWarmPool_UpdateStrategyApplied(t *testing.T) {
	a, cs := newWarmPoolTestAdapter(t, sandboxTemplate("base"))
	a.warmPoolUpdateStrategy = "Recreate"
	if _, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "base", TargetSize: 1, WarmPoolID: "wp-strategy",
	}); err != nil {
		t.Fatalf("CreateWarmPool() error = %v", err)
	}
	got, err := cs.ExtensionsV1beta1().SandboxWarmPools("default").
		Get(context.Background(), "wp-strategy", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Spec.UpdateStrategy == nil || got.Spec.UpdateStrategy.Type != extv1beta1.RecreateSandboxWarmPoolUpdateStrategyType {
		t.Errorf("updateStrategy = %+v, want Recreate", got.Spec.UpdateStrategy)
	}
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
	if err != nil {
		t.Fatalf("GetWarmPool() error = %v", err)
	}
	if wp.Status != "ready" || wp.CurrentSize != 3 || wp.TargetSize != 3 {
		t.Errorf("GetWarmPool() = %+v, want status=ready sizes=3/3", wp)
	}
	if wp.TemplateID != "base" {
		t.Errorf("TemplateID = %q, want base", wp.TemplateID)
	}
}

func TestGetWarmPool_NotFound(t *testing.T) {
	a, _ := newWarmPoolTestAdapter(t)
	_, err := a.GetWarmPool(context.Background(), "nope")
	if !errors.Is(err, adapter.ErrWarmPoolNotFound) {
		t.Errorf("GetWarmPool() err = %v, want ErrWarmPoolNotFound", err)
	}
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
		{"zero replicas is ready", ptr(0), 1, 0, 1, "ready"},
		{"zero replicas not yet observed", ptr(0), 2, 3, 1, "pending"},
		{"nil replicas is ready", nil, 1, 0, 1, "ready"},
		{"not yet observed", ptr(3), 2, 0, 1, "pending"},
		{"all ready", ptr(3), 1, 3, 1, "ready"},
		{"partially ready", ptr(3), 1, 1, 1, "scaling"},
		{"none ready", ptr(3), 1, 0, 1, "pending"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			swp := &extv1beta1.SandboxWarmPool{
				ObjectMeta: metav1.ObjectMeta{Generation: tt.gen},
				Spec:       extv1beta1.SandboxWarmPoolSpec{Replicas: tt.spec},
				Status:     extv1beta1.SandboxWarmPoolStatus{ReadyReplicas: tt.ready, ObservedGeneration: tt.obsGen},
			}
			if got := warmPoolStatus(swp); got != tt.want {
				t.Errorf("warmPoolStatus() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestListWarmPools(t *testing.T) {
	r2 := int32(2)
	objs := []runtime.Object{
		&extv1beta1.SandboxWarmPool{
			ObjectMeta: metav1.ObjectMeta{Name: "wp-a", Namespace: "default", Generation: 1},
			Spec: extv1beta1.SandboxWarmPoolSpec{
				Replicas: &r2, TemplateRef: extv1beta1.SandboxTemplateRef{Name: "base"},
			},
			Status: extv1beta1.SandboxWarmPoolStatus{ReadyReplicas: 2, ObservedGeneration: 1},
		},
		&extv1beta1.SandboxWarmPool{
			ObjectMeta: metav1.ObjectMeta{Name: "wp-b", Namespace: "default", Generation: 1},
			Spec: extv1beta1.SandboxWarmPoolSpec{
				Replicas: &r2, TemplateRef: extv1beta1.SandboxTemplateRef{Name: "code"},
			},
			Status: extv1beta1.SandboxWarmPoolStatus{ReadyReplicas: 0, ObservedGeneration: 1},
		},
	}
	a, _ := newWarmPoolTestAdapter(t, objs...)
	pools, err := a.ListWarmPools(context.Background())
	if err != nil {
		t.Fatalf("ListWarmPools() error = %v", err)
	}
	if len(pools) != 2 {
		t.Fatalf("ListWarmPools() returned %d pools, want 2", len(pools))
	}
	byID := map[string]*adapter.WarmPool{pools[0].WarmPoolID: pools[0], pools[1].WarmPoolID: pools[1]}
	if byID["wp-a"].Status != "ready" || byID["wp-b"].Status != "pending" {
		t.Errorf("statuses = %q/%q, want ready/pending", byID["wp-a"].Status, byID["wp-b"].Status)
	}
}

func TestListWarmPools_Empty(t *testing.T) {
	a, _ := newWarmPoolTestAdapter(t)
	pools, err := a.ListWarmPools(context.Background())
	if err != nil {
		t.Fatalf("ListWarmPools() error = %v", err)
	}
	if pools == nil || len(pools) != 0 {
		t.Errorf("ListWarmPools() = %v, want empty non-nil slice", pools)
	}
}

func TestDeleteWarmPool(t *testing.T) {
	r1 := int32(1)
	swp := &extv1beta1.SandboxWarmPool{
		ObjectMeta: metav1.ObjectMeta{Name: "wp-del", Namespace: "default"},
		Spec: extv1beta1.SandboxWarmPoolSpec{
			Replicas: &r1, TemplateRef: extv1beta1.SandboxTemplateRef{Name: "base"},
		},
	}
	a, cs := newWarmPoolTestAdapter(t, swp)
	a.warmPoolMap["base"] = "wp-del"

	if err := a.DeleteWarmPool(context.Background(), "wp-del"); err != nil {
		t.Fatalf("DeleteWarmPool() error = %v", err)
	}
	if _, err := cs.ExtensionsV1beta1().SandboxWarmPools("default").
		Get(context.Background(), "wp-del", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("CRD still exists after delete (err = %v)", err)
	}
	// Consumption mapping is cleaned up so CreateSandbox stops routing to
	// the deleted pool.
	if _, ok := a.warmPoolMap["base"]; ok {
		t.Error("warmPoolMap still references deleted pool")
	}
}

func TestDeleteWarmPool_NotFound(t *testing.T) {
	a, _ := newWarmPoolTestAdapter(t)
	if err := a.DeleteWarmPool(context.Background(), "nope"); !errors.Is(err, adapter.ErrWarmPoolNotFound) {
		t.Errorf("DeleteWarmPool() err = %v, want ErrWarmPoolNotFound", err)
	}
}

func TestUpdateWarmPoolSize(t *testing.T) {
	r1 := int32(1)
	swp := &extv1beta1.SandboxWarmPool{
		ObjectMeta: metav1.ObjectMeta{Name: "wp-up", Namespace: "default"},
		Spec: extv1beta1.SandboxWarmPoolSpec{
			Replicas: &r1, TemplateRef: extv1beta1.SandboxTemplateRef{Name: "base"},
		},
	}
	a, cs := newWarmPoolTestAdapter(t, swp)
	if err := a.UpdateWarmPoolSize(context.Background(), "wp-up", 5); err != nil {
		t.Fatalf("UpdateWarmPoolSize() error = %v", err)
	}
	got, err := cs.ExtensionsV1beta1().SandboxWarmPools("default").
		Get(context.Background(), "wp-up", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get after patch: %v", err)
	}
	if *got.Spec.Replicas != 5 {
		t.Errorf("spec.replicas = %d, want 5", *got.Spec.Replicas)
	}
}

func TestUpdateWarmPoolSize_Clamps(t *testing.T) {
	r1 := int32(1)
	swp := &extv1beta1.SandboxWarmPool{
		ObjectMeta: metav1.ObjectMeta{Name: "wp-clamp2", Namespace: "default"},
		Spec: extv1beta1.SandboxWarmPoolSpec{
			Replicas: &r1, TemplateRef: extv1beta1.SandboxTemplateRef{Name: "base"},
		},
	}
	a, cs := newWarmPoolTestAdapter(t, swp)
	if err := a.UpdateWarmPoolSize(context.Background(), "wp-clamp2", 100000); err != nil {
		t.Fatalf("UpdateWarmPoolSize() error = %v", err)
	}
	got, err := cs.ExtensionsV1beta1().SandboxWarmPools("default").
		Get(context.Background(), "wp-clamp2", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if *got.Spec.Replicas != 256 {
		t.Errorf("spec.replicas = %d, want 256 (clamped)", *got.Spec.Replicas)
	}
}

func TestUpdateWarmPoolSize_NegativeRejected(t *testing.T) {
	a, _ := newWarmPoolTestAdapter(t)
	if err := a.UpdateWarmPoolSize(context.Background(), "wp-up", -1); err == nil {
		t.Error("UpdateWarmPoolSize(-1) = nil, want error")
	}
}

func TestUpdateWarmPoolSize_NotFound(t *testing.T) {
	a, _ := newWarmPoolTestAdapter(t)
	if err := a.UpdateWarmPoolSize(context.Background(), "nope", 3); !errors.Is(err, adapter.ErrWarmPoolNotFound) {
		t.Errorf("UpdateWarmPoolSize() err = %v, want ErrWarmPoolNotFound", err)
	}
}

// --- Review remediation tests (PR #82) ---

func TestCreateWarmPool_AnnotationPreservesTemplateIdentity(t *testing.T) {
	a, cs := newWarmPoolTestAdapter(t, sandboxTemplate("real-tmpl"))
	// Ambiguous mapping: two E2B template IDs → one SandboxTemplate.
	a.templateToSandboxTemplate["template-a"] = "real-tmpl"
	a.templateToSandboxTemplate["template-b"] = "real-tmpl"

	if _, err := a.CreateWarmPool(context.Background(), &adapter.WarmPoolCreateRequest{
		TemplateID: "template-a", TargetSize: 1, WarmPoolID: "wp-identity",
	}); err != nil {
		t.Fatalf("CreateWarmPool() error = %v", err)
	}

	// The CRD carries the E2B template ID as an annotation.
	got, err := cs.ExtensionsV1beta1().SandboxWarmPools("default").
		Get(context.Background(), "wp-identity", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Annotations[warmPoolTemplateAnnotation] != "template-a" {
		t.Errorf("annotation = %q, want template-a", got.Annotations[warmPoolTemplateAnnotation])
	}

	// Read paths report the annotated ID, not an arbitrary reverse-map hit.
	wp, err := a.GetWarmPool(context.Background(), "wp-identity")
	if err != nil {
		t.Fatalf("GetWarmPool() error = %v", err)
	}
	if wp.TemplateID != "template-a" {
		t.Errorf("TemplateID = %q, want template-a (ambiguous reverse mapping must not win)", wp.TemplateID)
	}
}

func TestResolveTemplateRef_LowercaseFallback(t *testing.T) {
	a, _ := newWarmPoolTestAdapter(t)
	// Viper lowercases map keys on config load.
	a.templateToSandboxTemplate["mytemplate"] = "real-tmpl"
	if got := a.resolveTemplateRef("MyTemplate"); got != "real-tmpl" {
		t.Errorf("resolveTemplateRef(MyTemplate) = %q, want real-tmpl", got)
	}
	// Exact match still wins.
	a.templateToSandboxTemplate["MyTemplate"] = "exact-tmpl"
	if got := a.resolveTemplateRef("MyTemplate"); got != "exact-tmpl" {
		t.Errorf("resolveTemplateRef(MyTemplate) = %q, want exact-tmpl", got)
	}
	// Unmapped falls back to the ID itself.
	if got := a.resolveTemplateRef("other"); got != "other" {
		t.Errorf("resolveTemplateRef(other) = %q, want other", got)
	}
}

func TestRestoreWarmPoolRouting(t *testing.T) {
	r1 := int32(1)
	annotated := &extv1beta1.SandboxWarmPool{
		ObjectMeta: metav1.ObjectMeta{
			Name: "wp-created", Namespace: "default",
			Annotations: map[string]string{warmPoolTemplateAnnotation: "tmpl-x"},
		},
		Spec: extv1beta1.SandboxWarmPoolSpec{
			Replicas: &r1, TemplateRef: extv1beta1.SandboxTemplateRef{Name: "tmpl-x"},
		},
	}
	plain := &extv1beta1.SandboxWarmPool{
		ObjectMeta: metav1.ObjectMeta{Name: "wp-operator", Namespace: "default"},
		Spec: extv1beta1.SandboxWarmPoolSpec{
			Replicas: &r1, TemplateRef: extv1beta1.SandboxTemplateRef{Name: "ops-tmpl"},
		},
	}
	a, _ := newWarmPoolTestAdapter(t, annotated, plain)
	// Config-provided entry that must take precedence over restored ones.
	a.warmPoolMap["tmpl-x"] = "wp-from-config"

	a.restoreWarmPoolRouting(context.Background())

	if a.warmPoolMap["tmpl-x"] != "wp-from-config" {
		t.Errorf("config entry overwritten: warmPoolMap[tmpl-x] = %q, want wp-from-config", a.warmPoolMap["tmpl-x"])
	}
	// Pool without the annotation is operator-managed: not routed.
	for tmpl, pool := range a.warmPoolMap {
		if pool == "wp-operator" {
			t.Errorf("operator pool wp-operator unexpectedly routed via template %q", tmpl)
		}
	}
}

func TestRestoreWarmPoolRouting_AddsAnnotated(t *testing.T) {
	r1 := int32(1)
	annotated := &extv1beta1.SandboxWarmPool{
		ObjectMeta: metav1.ObjectMeta{
			Name: "wp-created", Namespace: "default",
			Annotations: map[string]string{warmPoolTemplateAnnotation: "tmpl-y"},
		},
		Spec: extv1beta1.SandboxWarmPoolSpec{
			Replicas: &r1, TemplateRef: extv1beta1.SandboxTemplateRef{Name: "tmpl-y"},
		},
	}
	a, _ := newWarmPoolTestAdapter(t, annotated)
	a.restoreWarmPoolRouting(context.Background())
	if a.warmPoolMap["tmpl-y"] != "wp-created" {
		t.Errorf("warmPoolMap[tmpl-y] = %q, want wp-created", a.warmPoolMap["tmpl-y"])
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
				_ = a.DeleteWarmPool(ctx, "wp-race")
				_, _ = a.ListWarmPools(ctx)
			}
		}()
	}
	for range 4 {
		<-done
	}
}
