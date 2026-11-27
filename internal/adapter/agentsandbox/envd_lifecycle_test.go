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
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	corefake "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
	sandboxv1beta1 "sigs.k8s.io/agent-sandbox/api/v1beta1"
	"sigs.k8s.io/agent-sandbox/clients/go/sandbox"
	agentsfake "sigs.k8s.io/agent-sandbox/clients/k8s/clientset/versioned/fake"
	extv1beta1 "sigs.k8s.io/agent-sandbox/extensions/api/v1beta1"

	"github.com/e2bgateway/e2bgateway/internal/envd"
	"github.com/e2bgateway/e2bgateway/internal/sessionstore"
)

func TestPauseInvalidatesEnvdClientBeforePodIPChanges(t *testing.T) {
	claim := annotatedClaim("claim", "e2b-id")
	claim.Status.SandboxStatus = extv1beta1.SandboxStatus{Name: "pod"}
	a, _ := newWarmPoolTestAdapter(t, claim)
	a.idMap = map[string]*sandboxEntry{"e2b-id": {claimName: "claim"}}
	a.sessionStore = sessionstore.NewMemory()
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "default"}, Status: corev1.PodStatus{PodIP: "10.0.0.2"}}
	a.k8s.CoreClient = corefake.NewSimpleClientset(pod).CoreV1()
	stale := envd.NewClient(envd.ClientConfig{BaseURL: "http://10.0.0.1:49983"})
	a.envdClients = map[string]*envdClientEntry{"e2b-id": {client: stale, createdAt: time.Now()}}
	agents := agentsfake.NewSimpleClientset()
	agents.PrependReactor("patch", "sandboxes", func(action clienttesting.Action) (bool, runtime.Object, error) {
		patch, ok := action.(clienttesting.PatchAction)
		if !ok {
			t.Fatalf("unexpected action type: %T", action)
		}
		if patch.GetName() != "pod" || string(patch.GetPatch()) != `{"spec":{"operatingMode":"Suspended"}}` {
			t.Errorf("unexpected suspension patch: %s %s", patch.GetName(), patch.GetPatch())
		}
		return true, &sandboxv1beta1.Sandbox{}, nil
	})
	a.k8s.AgentsClient = agents.AgentsV1beta1()
	if err := a.PauseSandbox(context.Background(), "e2b-id"); err != nil {
		t.Fatal(err)
	}
	client, err := a.getOrCreateEnvdClient(context.Background(), "e2b-id")
	if err != nil {
		t.Fatal(err)
	}
	if client == stale || client.BaseURL() != "http://10.0.0.2:49983" {
		t.Fatalf("still using stale endpoint: %s", client.BaseURL())
	}
	again, err := a.getOrCreateEnvdClient(context.Background(), "e2b-id")
	if err != nil || again != client {
		t.Fatalf("unchanged endpoint should reuse client: %v", err)
	}
}

func TestEnvdEnvironmentInitializedOncePerPodAndRetriedAfterFailure(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/init" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var body struct {
			EnvVars map[string]string `json:"envVars"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.EnvVars["CREATION_ENV"] != "present" {
			t.Errorf("creation env not initialized: %v", err)
		}
		if _, ok := body.EnvVars["SECRET_REF"]; ok {
			t.Error("unresolved valueFrom must not become an empty environment variable")
		}
		if calls == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	a := &Adapter{}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "first"}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Env: []corev1.EnvVar{
		{Name: "CREATION_ENV", Value: "present"},
		{Name: "SECRET_REF", ValueFrom: &corev1.EnvVarSource{}},
	}}}}}
	ctx := context.Background()
	if err := a.ensureEnvdEnvironment(ctx, "id", server.URL, "", pod); err == nil {
		t.Fatal("failed initialization must propagate")
	}
	for range 2 {
		if err := a.ensureEnvdEnvironment(ctx, "id", server.URL, "", pod); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatalf("initialization was not cached after success: %d", calls)
	}
	pod.UID = "resumed"
	if err := a.ensureEnvdEnvironment(ctx, "id", server.URL, "", pod); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("new Pod must reinitialize defaults: %d", calls)
	}
}

func TestInvalidationPreventsInFlightCacheRefill(t *testing.T) {
	for _, kill := range []bool{false, true} {
		name := "invalidate"
		if kill {
			name = "kill"
		}
		t.Run(name, func(t *testing.T) {
			claim := annotatedClaim("claim", "id")
			claim.Status.SandboxStatus = extv1beta1.SandboxStatus{Name: "pod"}
			a, _ := newWarmPoolTestAdapter(t, claim)
			a.idMap = map[string]*sandboxEntry{"id": {claimName: "claim"}}
			a.sessionStore = sessionstore.NewMemory()
			a.envdClients = make(map[string]*envdClientEntry)
			client, err := sandbox.NewClient(context.Background(), sandbox.Options{K8sHelper: a.k8s, Quiet: true})
			if err != nil {
				t.Fatal(err)
			}
			a.client = client
			started, release := make(chan struct{}), make(chan struct{})
			core := corefake.NewSimpleClientset()
			core.PrependReactor("get", "pods", func(clienttesting.Action) (bool, runtime.Object, error) {
				close(started)
				<-release
				return true, &corev1.Pod{Status: corev1.PodStatus{PodIP: "10.0.0.1"}}, nil
			})
			a.k8s.CoreClient = core.CoreV1()
			done := make(chan error, 1)
			go func() {
				_, lookupErr := a.getOrCreateEnvdClient(context.Background(), "id")
				done <- lookupErr
			}()
			<-started
			if kill {
				err = a.KillSandbox(context.Background(), "id")
			} else {
				a.invalidateEnvdClient("id")
			}
			close(release)
			lookupErr := <-done
			if err != nil {
				t.Fatal(err)
			}
			if lookupErr != nil {
				t.Fatal(lookupErr)
			}
			if len(a.envdClients) != 0 {
				t.Fatal("in-flight lookup repopulated the invalidated cache")
			}
		})
	}
}

func TestTerminatingPodIsNotCached(t *testing.T) {
	claim := annotatedClaim("claim", "id")
	claim.Status.SandboxStatus = extv1beta1.SandboxStatus{Name: "pod"}
	a, _ := newWarmPoolTestAdapter(t, claim)
	a.idMap = map[string]*sandboxEntry{"id": {claimName: "claim"}}
	a.sessionStore = sessionstore.NewMemory()
	a.envdClients = make(map[string]*envdClientEntry)
	core := corefake.NewSimpleClientset()
	terminating := true
	core.PrependReactor("get", "pods", func(clienttesting.Action) (bool, runtime.Object, error) {
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "replacement"}, Status: corev1.PodStatus{PodIP: "10.0.0.2"}}
		if terminating {
			now := metav1.Now()
			pod.UID = "old"
			pod.DeletionTimestamp = &now
			pod.Status.PodIP = "10.0.0.1"
		}
		return true, pod, nil
	})
	a.k8s.CoreClient = core.CoreV1()
	a.invalidateEnvdClient("id")
	if _, err := a.getOrCreateEnvdClient(context.Background(), "id"); err == nil {
		t.Fatal("terminating Pod must not return an endpoint")
	}
	if len(a.envdClients) != 0 {
		t.Fatal("terminating Pod endpoint was cached")
	}
	terminating = false
	client, err := a.getOrCreateEnvdClient(context.Background(), "id")
	if err != nil {
		t.Fatal(err)
	}
	if client.BaseURL() != "http://10.0.0.2:49983" {
		t.Fatalf("wrong replacement endpoint: %s", client.BaseURL())
	}
	again, err := a.getOrCreateEnvdClient(context.Background(), "id")
	if err != nil || again != client {
		t.Fatalf("replacement client was not cached: %v", err)
	}
}

func TestSlowEnvironmentInitDoesNotBlockOtherSandboxes(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			EnvVars map[string]string `json:"envVars"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.EnvVars["CASE"] == "slow" {
			close(started)
			<-release
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	defer close(release)
	a := &Adapter{}
	slow := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "slow"}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Env: []corev1.EnvVar{{Name: "CASE", Value: "slow"}}}}}}
	fast := slow.DeepCopy()
	fast.UID = "fast"
	fast.Spec.Containers[0].Env[0].Value = "fast"
	go func() { _ = a.ensureEnvdEnvironment(context.Background(), "slow", server.URL, "", slow) }()
	<-started
	done := make(chan error, 1)
	go func() { done <- a.ensureEnvdEnvironment(context.Background(), "fast", server.URL, "", fast) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("one sandbox's network request blocked another sandbox")
	}
}
