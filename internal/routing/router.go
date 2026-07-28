// Package routing implements request routing and backend selection for E2BGateway.
package routing

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
	"github.com/e2bgateway/e2bgateway/internal/config"
)

var logger *zap.Logger

func init() {
	logger, _ = zap.NewProduction()
}

// RoutingRequest contains the context for selecting a backend.
type RoutingRequest struct {
	TenantID   string
	TemplateID string
	Method     string
	Path       string
}

// Router selects the appropriate backend adapter for each request.
type Router struct {
	cfg      config.RoutingConfig
	registry *adapter.Registry

	// Health tracking
	healthStatus map[string]*backendHealth
	healthMu     sync.RWMutex
	stopHealth   chan struct{}

	// Weighted round-robin state
	rrCounter atomic.Uint64
}

type backendHealth struct {
	healthy          bool
	consecutiveFails int
	consecutiveOK    int
	lastCheck        time.Time
}

// NewRouter creates a new Router.
func NewRouter(cfg config.RoutingConfig, registry *adapter.Registry) *Router {
	r := &Router{
		cfg:          cfg,
		registry:     registry,
		healthStatus: make(map[string]*backendHealth),
		stopHealth:   make(chan struct{}),
	}

	// Initialize health status for all backends
	for _, a := range registry.List() {
		r.healthStatus[a.Name()] = &backendHealth{healthy: true}
	}

	// Start health check loop if configured
	if cfg.HealthCheck.Interval > 0 {
		go r.healthCheckLoop()
	}

	return r
}

// Stop stops the health check goroutine.
func (r *Router) Stop() {
	close(r.stopHealth)
}

// healthCheckLoop periodically checks backend health.
func (r *Router) healthCheckLoop() {
	interval := r.cfg.HealthCheck.Interval
	if interval == 0 {
		interval = 10 * time.Second
	}
	timeout := r.cfg.HealthCheck.Timeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-r.stopHealth:
			return
		case <-ticker.C:
			r.checkAllBackends(timeout)
		}
	}
}

func (r *Router) checkAllBackends(timeout time.Duration) {
	adapters := r.registry.List()
	for _, a := range adapters {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		err := a.HealthCheck(ctx)
		cancel()

		r.healthMu.Lock()
		bh, ok := r.healthStatus[a.Name()]
		if !ok {
			bh = &backendHealth{}
			r.healthStatus[a.Name()] = bh
		}

		if err != nil {
			bh.consecutiveFails++
			bh.consecutiveOK = 0
			threshold := r.cfg.HealthCheck.UnhealthyThreshold
			if threshold == 0 {
				threshold = 3
			}
			if bh.consecutiveFails >= threshold {
				if bh.healthy {
					logger.Warn("backend became unhealthy",
						zap.String("backend", a.Name()),
						zap.Error(err),
					)
				}
				bh.healthy = false
			}
		} else {
			bh.consecutiveOK++
			bh.consecutiveFails = 0
			threshold := r.cfg.HealthCheck.HealthyThreshold
			if threshold == 0 {
				threshold = 2
			}
			if bh.consecutiveOK >= threshold {
				if !bh.healthy {
					logger.Info("backend recovered",
						zap.String("backend", a.Name()),
					)
				}
				bh.healthy = true
			}
		}
		bh.lastCheck = time.Now()
		r.healthMu.Unlock()
	}
}

// SelectBackend chooses a backend adapter based on the routing configuration.
func (r *Router) SelectBackend(ctx context.Context, req *RoutingRequest) (string, error) {
	// Try strategy-specific routing first
	if backend, ok := r.tryStrategies(req); ok {
		if r.isHealthy(backend) {
			return backend, nil
		}
		// If matched backend is unhealthy, try failover
		if r.cfg.Failover.Enabled {
			return r.tryFailover()
		}
		// Return it anyway if failover is disabled
		return backend, nil
	}

	// Try weighted round-robin if configured
	if r.cfg.Strategy == "weighted" {
		if backend, ok := r.tryWeightedRoundRobin(); ok {
			return backend, nil
		}
	}

	// Try priority/failover chain
	if r.cfg.Strategy == "priority" || r.cfg.Failover.Enabled {
		return r.tryFailover()
	}

	// Fall back to default backend
	if r.cfg.DefaultBackend != "" {
		if _, ok := r.registry.Get(r.cfg.DefaultBackend); ok {
			return r.cfg.DefaultBackend, nil
		}
	}

	// Fall back to first healthy backend
	adapters := r.registry.List()
	for _, a := range adapters {
		if r.isHealthy(a.Name()) {
			return a.Name(), nil
		}
	}

	// Last resort: any backend
	if len(adapters) > 0 {
		return adapters[0].Name(), nil
	}

	return "", fmt.Errorf("no backend available")
}

// tryStrategies iterates through configured strategies.
func (r *Router) tryStrategies(req *RoutingRequest) (string, bool) {
	for _, strategy := range r.cfg.Strategies {
		switch strategy.Name {
		case "template-routing", "template-based":
			if req.TemplateID != "" {
				for _, rule := range strategy.Rules {
					if rule.Template == req.TemplateID {
						if _, ok := r.registry.Get(rule.Backend); ok {
							return rule.Backend, true
						}
					}
				}
			}
		case "tenant-static":
			if req.TenantID != "" {
				for _, rule := range strategy.Rules {
					if rule.Tenant == req.TenantID {
						if _, ok := r.registry.Get(rule.Backend); ok {
							return rule.Backend, true
						}
					}
				}
			}
		}
	}
	return "", false
}

// tryWeightedRoundRobin selects a backend using weighted round-robin.
func (r *Router) tryWeightedRoundRobin() (string, bool) {
	// Collect healthy backends with weights from rules
	type weightedBackend struct {
		name   string
		weight int
	}

	var backends []weightedBackend
	totalWeight := 0

	for _, strategy := range r.cfg.Strategies {
		if strategy.Name != "weighted" {
			continue
		}
		for _, rule := range strategy.Rules {
			if r.isHealthy(rule.Backend) {
				if _, ok := r.registry.Get(rule.Backend); ok {
					weight := 1 // default weight
					backends = append(backends, weightedBackend{name: rule.Backend, weight: weight})
					totalWeight += weight
				}
			}
		}
	}

	if len(backends) == 0 {
		return "", false
	}

	// Select using counter modulo
	counter := r.rrCounter.Add(1)
	idx := int(counter) % totalWeight
	for _, b := range backends {
		idx -= b.weight
		if idx < 0 {
			return b.name, true
		}
	}

	return backends[0].name, true
}

// tryFailover iterates through the failover chain.
func (r *Router) tryFailover() (string, error) {
	chain := r.cfg.Failover.Chain
	if len(chain) == 0 {
		// Build chain from all registered backends
		for _, a := range r.registry.List() {
			chain = append(chain, a.Name())
		}
	}

	for _, backend := range chain {
		if _, ok := r.registry.Get(backend); ok {
			if r.isHealthy(backend) {
				return backend, nil
			}
		}
	}

	// If all backends unhealthy, return first one anyway
	if len(chain) > 0 {
		return chain[0], nil
	}

	return "", fmt.Errorf("no healthy backend in failover chain")
}

// isHealthy checks if a backend is healthy.
func (r *Router) isHealthy(name string) bool {
	r.healthMu.RLock()
	defer r.healthMu.RUnlock()

	bh, ok := r.healthStatus[name]
	if !ok {
		return true // unknown backends assumed healthy
	}
	return bh.healthy
}

// GetHealthStatus returns the health status of all backends.
func (r *Router) GetHealthStatus() map[string]bool {
	r.healthMu.RLock()
	defer r.healthMu.RUnlock()

	result := make(map[string]bool, len(r.healthStatus))
	for name, bh := range r.healthStatus {
		result[name] = bh.healthy
	}
	return result
}
