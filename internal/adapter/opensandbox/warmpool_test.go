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
	"testing"

	opensandbox "github.com/alibaba/OpenSandbox/sdks/sandbox/go"
)

func TestResolveImageURI_StaticMap(t *testing.T) {
	a := &Adapter{templateToImage: map[string]string{"base": "registry.example.com/base:v1"}}
	got, err := a.resolveImageURI(context.Background(), "base")
	if err != nil {
		t.Fatalf("resolveImageURI() error = %v", err)
	}
	if got != "registry.example.com/base:v1" {
		t.Errorf("resolveImageURI() = %q, want registry.example.com/base:v1", got)
	}
}

func TestResolveImageURI_FallbackToTemplateID(t *testing.T) {
	a := &Adapter{templateToImage: map[string]string{}}
	got, err := a.resolveImageURI(context.Background(), "some/image:latest")
	if err != nil {
		t.Fatalf("resolveImageURI() error = %v", err)
	}
	if got != "some/image:latest" {
		t.Errorf("resolveImageURI() = %q, want some/image:latest", got)
	}
}

func TestResolveImageURI_TemplateStore(t *testing.T) {
	store := NewMemoryTemplateStore()
	a := &Adapter{templateToImage: map[string]string{}, templates: store}
	// Unknown template falls back to the template ID itself.
	got, err := a.resolveImageURI(context.Background(), "unknown")
	if err != nil {
		t.Fatalf("resolveImageURI() error = %v", err)
	}
	if got != "unknown" {
		t.Errorf("resolveImageURI() = %q, want unknown", got)
	}
}

func TestPoolConnectionConfig(t *testing.T) {
	a := &Adapter{baseURL: "https://sandbox.example.com:8443", apiKey: "k"}
	cc, err := a.poolConnectionConfig()
	if err != nil {
		t.Fatalf("poolConnectionConfig() error = %v", err)
	}
	if cc.Protocol != "https" || cc.Domain != "sandbox.example.com:8443" || cc.APIKey != "k" {
		t.Errorf("poolConnectionConfig() = %+v", cc)
	}
}

func TestPoolConnectionConfig_Invalid(t *testing.T) {
	a := &Adapter{baseURL: "not a url"}
	if _, err := a.poolConnectionConfig(); err == nil {
		t.Error("poolConnectionConfig() = nil error, want parse error")
	}
}

func TestPoolStatus_Mapping(t *testing.T) {
	tests := []struct {
		name string
		snap *opensandbox.PoolSnapshot
		want string
	}{
		{"running full", &opensandbox.PoolSnapshot{
			LifecycleState: opensandbox.PoolLifecycleRunning, IdleCount: 3, MaxIdle: 3}, "ready"},
		{"running filling", &opensandbox.PoolSnapshot{
			LifecycleState: opensandbox.PoolLifecycleRunning, IdleCount: 1, MaxIdle: 3}, "scaling"},
		{"running degraded", &opensandbox.PoolSnapshot{
			LifecycleState: opensandbox.PoolLifecycleRunning, HealthState: opensandbox.PoolDegraded,
			IdleCount: 3, MaxIdle: 3}, "degraded"},
		{"starting", &opensandbox.PoolSnapshot{
			LifecycleState: opensandbox.PoolLifecycleStarting}, "pending"},
		{"not started", &opensandbox.PoolSnapshot{
			LifecycleState: opensandbox.PoolLifecycleNotStarted}, "pending"},
		{"stopped", &opensandbox.PoolSnapshot{
			LifecycleState: opensandbox.PoolLifecycleStopped}, "destroyed"},
		{"draining", &opensandbox.PoolSnapshot{
			LifecycleState: opensandbox.PoolLifecycleDraining}, "destroyed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := poolStatus(tt.snap); got != tt.want {
				t.Errorf("poolStatus() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildPoolStateStore_DefaultMemory(t *testing.T) {
	a := &Adapter{}
	store, err := a.buildPoolStateStore()
	if err != nil {
		t.Fatalf("buildPoolStateStore() error = %v", err)
	}
	if store != nil {
		t.Error("buildPoolStateStore() with no config = non-nil, want nil (SDK memory default)")
	}
}

func TestBuildPoolStateStore_ExplicitMemory(t *testing.T) {
	a := &Adapter{poolStoreCfg: &PoolStateStoreConfig{Type: "memory"}}
	store, err := a.buildPoolStateStore()
	if err != nil {
		t.Fatalf("buildPoolStateStore() error = %v", err)
	}
	if store != nil {
		t.Error("buildPoolStateStore(memory) = non-nil, want nil (SDK memory default)")
	}
}

func TestBuildPoolStateStore_RedisRequiresAddr(t *testing.T) {
	a := &Adapter{poolStoreCfg: &PoolStateStoreConfig{Type: "redis"}}
	if _, err := a.buildPoolStateStore(); err == nil {
		t.Error("buildPoolStateStore() redis without addr = nil error")
	}
}

func TestBuildPoolStateStore_UnknownType(t *testing.T) {
	a := &Adapter{poolStoreCfg: &PoolStateStoreConfig{Type: "etcd"}}
	if _, err := a.buildPoolStateStore(); err == nil {
		t.Error("buildPoolStateStore(etcd) = nil error, want unknown-type error")
	}
}
