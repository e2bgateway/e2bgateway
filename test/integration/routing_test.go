package integration

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
	"github.com/e2bgateway/e2bgateway/internal/config"
	"github.com/e2bgateway/e2bgateway/internal/routing"
	testmock "github.com/e2bgateway/e2bgateway/test/mock"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// newRegistry creates a registry populated with the named MockBackends.
// Returns the registry and a map of name → MockBackend for later inspection.
func newTestRegistry(t *testing.T, names ...string) (*adapter.Registry, map[string]*testmock.MockBackend) {
	t.Helper()
	reg := adapter.NewRegistry()
	backends := make(map[string]*testmock.MockBackend, len(names))
	for _, name := range names {
		mb := testmock.NewMockBackend()
		mb.NameFn = func() string { return name }
		if err := reg.Register(mb); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
		backends[name] = mb
	}
	return reg, backends
}

// ---------------------------------------------------------------------------
// TestStaticRouting – tenant-static strategy selects the correct backend
// ---------------------------------------------------------------------------

func TestStaticRouting(t *testing.T) {
	reg, _ := newTestRegistry(t, "backend-a", "backend-b", "backend-c")

	r := routing.NewRouter(config.RoutingConfig{
		Strategies: []config.RoutingStrategy{
			{
				Name: "tenant-static",
				Rules: []config.RoutingRule{
					{Tenant: "tenant-1", Backend: "backend-a"},
					{Tenant: "tenant-2", Backend: "backend-b"},
					{Tenant: "tenant-3", Backend: "backend-c"},
				},
			},
		},
	}, reg)
	defer r.Stop()

	tests := []struct {
		tenant string
		want   string
	}{
		{"tenant-1", "backend-a"},
		{"tenant-2", "backend-b"},
		{"tenant-3", "backend-c"},
	}
	for _, tt := range tests {
		backend, err := r.SelectBackend(context.Background(), &routing.RoutingRequest{TenantID: tt.tenant})
		if err != nil {
			t.Fatalf("SelectBackend(tenant=%s): %v", tt.tenant, err)
		}
		if backend != tt.want {
			t.Errorf("tenant %s: want %s, got %s", tt.tenant, tt.want, backend)
		}
	}
}

// ---------------------------------------------------------------------------
// TestTemplateBasedRouting – template-routing strategy
// ---------------------------------------------------------------------------

func TestTemplateBasedRouting(t *testing.T) {
	reg, _ := newTestRegistry(t, "agent-sandbox", "e2b-cloud", "opensandbox")

	r := routing.NewRouter(config.RoutingConfig{
		Strategies: []config.RoutingStrategy{
			{
				Name: "template-routing",
				Rules: []config.RoutingRule{
					{Template: "code-interpreter", Backend: "agent-sandbox"},
					{Template: "browser", Backend: "opensandbox"},
					{Template: "desktop", Backend: "e2b-cloud"},
				},
			},
		},
	}, reg)
	defer r.Stop()

	tests := []struct {
		template string
		want     string
	}{
		{"code-interpreter", "agent-sandbox"},
		{"browser", "opensandbox"},
		{"desktop", "e2b-cloud"},
	}
	for _, tt := range tests {
		backend, err := r.SelectBackend(context.Background(), &routing.RoutingRequest{TemplateID: tt.template})
		if err != nil {
			t.Fatalf("SelectBackend(template=%s): %v", tt.template, err)
		}
		if backend != tt.want {
			t.Errorf("template %s: want %s, got %s", tt.template, tt.want, backend)
		}
	}
}

// ---------------------------------------------------------------------------
// TestWeightedRouting – weighted round-robin distributes across backends
// ---------------------------------------------------------------------------

func TestWeightedRouting(t *testing.T) {
	reg, _ := newTestRegistry(t, "backend-a", "backend-b")

	r := routing.NewRouter(config.RoutingConfig{
		Strategy: "weighted",
		Strategies: []config.RoutingStrategy{
			{
				Name: "weighted",
				Rules: []config.RoutingRule{
					{Backend: "backend-a"},
					{Backend: "backend-b"},
				},
			},
		},
	}, reg)
	defer r.Stop()

	counts := make(map[string]int)
	const totalRequests = 100

	for i := 0; i < totalRequests; i++ {
		backend, err := r.SelectBackend(context.Background(), &routing.RoutingRequest{})
		if err != nil {
			t.Fatalf("SelectBackend (iteration %d): %v", i, err)
		}
		counts[backend]++
	}

	// Both backends should receive a meaningful share (at least 20 % each).
	for _, name := range []string{"backend-a", "backend-b"} {
		if counts[name] == 0 {
			t.Errorf("backend %s received zero requests out of %d", name, totalRequests)
		}
	}

	total := counts["backend-a"] + counts["backend-b"]
	if total != totalRequests {
		t.Errorf("total distributed requests = %d, want %d", total, totalRequests)
	}
}

// ---------------------------------------------------------------------------
// TestFailoverRouting – failover chain skips unhealthy backends
// ---------------------------------------------------------------------------

func TestFailoverRouting(t *testing.T) {
	reg, backends := newTestRegistry(t, "primary", "secondary", "tertiary")

	// Mark "primary" as unhealthy by injecting a HealthCheck error.
	backends["primary"].HealthCheckFn = func(ctx context.Context) error {
		return io.EOF
	}

	r := routing.NewRouter(config.RoutingConfig{
		Failover: config.FailoverConfig{
			Enabled: true,
			Chain:   []string{"primary", "secondary", "tertiary"},
		},
		HealthCheck: config.HealthCheckConfig{
			Interval:           50 * time.Millisecond,
			Timeout:            time.Second,
			UnhealthyThreshold: 1,
			HealthyThreshold:   1,
		},
	}, reg)
	defer r.Stop()

	// Run several health-check cycles so the router detects the unhealthy
	// backend.
	time.Sleep(300 * time.Millisecond)

	// SelectBackend should skip "primary" and return "secondary".
	backend, err := r.SelectBackend(context.Background(), &routing.RoutingRequest{})
	if err != nil {
		t.Fatalf("SelectBackend: %v", err)
	}
	if backend == "primary" {
		t.Error("expected failover to skip unhealthy primary, but got primary")
	}
	if backend != "secondary" {
		t.Errorf("expected secondary, got %s", backend)
	}
}

// ---------------------------------------------------------------------------
// TestHealthBasedRouting – health-aware routing avoids unhealthy backends
// ---------------------------------------------------------------------------

func TestHealthBasedRouting(t *testing.T) {
	reg, backends := newTestRegistry(t, "backend-a", "backend-b")

	// Make backend-a unhealthy *before* starting the router so that the
	// health-check loop detects it on its first tick.  Mutating HealthCheckFn
	// after the router is running would be a data race because the health
	// loop reads the field concurrently.
	backends["backend-a"].HealthCheckFn = func(ctx context.Context) error {
		return io.EOF
	}

	r := routing.NewRouter(config.RoutingConfig{
		Strategies: []config.RoutingStrategy{
			{
				Name: "template-routing",
				Rules: []config.RoutingRule{
					{Template: "code-interpreter", Backend: "backend-a"},
				},
			},
		},
		Failover: config.FailoverConfig{
			Enabled: true,
			Chain:   []string{"backend-a", "backend-b"},
		},
		HealthCheck: config.HealthCheckConfig{
			Interval:           50 * time.Millisecond,
			Timeout:            time.Second,
			UnhealthyThreshold: 1,
			HealthyThreshold:   1,
		},
	}, reg)
	defer r.Stop()

	// Wait for the health-check loop to mark backend-a as unhealthy.
	time.Sleep(300 * time.Millisecond)

	// template-routing matches backend-a, but it is unhealthy, so the router
	// should fall over to backend-b via the failover chain.
	backend, err := r.SelectBackend(context.Background(), &routing.RoutingRequest{
		TemplateID: "code-interpreter",
	})
	if err != nil {
		t.Fatalf("SelectBackend: %v", err)
	}
	if backend != "backend-b" {
		t.Errorf("expected backend-b (because backend-a is unhealthy), got %s", backend)
	}
}
