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

package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
	agentsandboxadapter "github.com/e2bgateway/e2bgateway/internal/adapter/agentsandbox"
	agentsubstrateadapter "github.com/e2bgateway/e2bgateway/internal/adapter/agentsubstrate"
	e2bcloudadapter "github.com/e2bgateway/e2bgateway/internal/adapter/e2bcloud"
	mockadapter "github.com/e2bgateway/e2bgateway/internal/adapter/mock"
	opensandboxadapter "github.com/e2bgateway/e2bgateway/internal/adapter/opensandbox"
	v1 "github.com/e2bgateway/e2bgateway/internal/api/v1"
	"github.com/e2bgateway/e2bgateway/internal/auth"
	"github.com/e2bgateway/e2bgateway/internal/config"
	"github.com/e2bgateway/e2bgateway/internal/metrics"
	"github.com/e2bgateway/e2bgateway/internal/routing"
	gwmiddleware "github.com/e2bgateway/e2bgateway/internal/server/middleware"
	"github.com/e2bgateway/e2bgateway/internal/sessionstore"
)

// shutdownTimeout bounds the graceful drain of the HTTP and HTTPS listeners.
var shutdownTimeout = 30 * time.Second

// watcherStopTimeout bounds the join of the certificate watcher separately
// from the listener drain, so a drain that consumes the whole shutdown budget
// cannot let Stop return while a reload is still in flight.
var watcherStopTimeout = 5 * time.Second

// Server is the main E2BGateway HTTP server.
type Server struct {
	cfg           *config.Config
	httpServer    *http.Server
	httpsServer   *http.Server
	httpsReloader *certReloader
	router        chi.Router
	registry      *adapter.Registry
	authMgr       *auth.Manager
	routeMgr      *routing.Router
	sessionStore  sessionstore.Store

	// mu guards the watcher lifecycle fields below: they are written by
	// Start and read by Stop, which may run on different goroutines without
	// other synchronization.
	mu        sync.Mutex
	stopWatch context.CancelFunc
	watchDone chan struct{}
	stopped   bool
}

// New creates a new Server instance.
func New(cfg *config.Config) (*Server, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validating config: %w", err)
	}
	s := &Server{cfg: cfg}

	// Load the HTTPS certificate before anything that can be slow or carry
	// side effects (e.g. the session store's redis ping): a missing or
	// invalid certificate must fail startup immediately.
	if cfg.Server.HTTPS.Address != "" && cfg.Server.HTTPS.CertFile != "" && cfg.Server.HTTPS.KeyFile != "" {
		reloader, reloadErr := newCertReloader(cfg.Server.HTTPS.CertFile, cfg.Server.HTTPS.KeyFile, cfg.Server.HTTPS.ReloadInterval)
		if reloadErr != nil {
			return nil, fmt.Errorf("loading HTTPS certificate: %w", reloadErr)
		}
		s.httpsReloader = reloader
	}

	// Shared data-plane session store (envd tokens + sandbox→backend routing).
	// type: memory (default) keeps single-replica behavior; type: redis
	// enables horizontal scaling.
	store, err := sessionstore.New(cfg.SessionStore)
	if err != nil {
		return nil, fmt.Errorf("initializing session store: %w", err)
	}
	s.sessionStore = store

	// Initialize adapter registry
	s.registry = adapter.NewRegistry(store, cfg.SessionStore.RoutingTTL)
	if err := s.initAdapters(); err != nil {
		return nil, fmt.Errorf("initializing adapters: %w", err)
	}

	// Initialize auth manager
	s.authMgr, err = auth.NewManager(cfg.Auth)
	if err != nil {
		return nil, fmt.Errorf("initializing auth: %w", err)
	}

	// Initialize routing
	s.routeMgr = routing.NewRouter(cfg.Routing, s.registry)

	// Initialize process-global metrics recorder (no-op when disabled).
	metrics.Init(cfg.Observability.Metrics, metrics.Sources{
		SandboxCounts: func() map[string]int {
			if sb := s.registry.SandboxBackend(); sb != nil {
				return sb.CountByBackend()
			}
			return nil
		},
		BackendHealth: func() map[string]bool {
			if s.routeMgr != nil {
				return s.routeMgr.GetHealthStatus()
			}
			return nil
		},
	})

	// metrics helpers are no-ops before Init, so the initial load above
	// could not export the expiry gauge: publish it now so the gauge exists
	// right after startup, before the first rotation.
	if s.httpsReloader != nil {
		s.httpsReloader.publishExpiry()
	}

	// Build HTTP router
	s.router = s.buildRouter()

	// Create HTTP server
	s.httpServer = &http.Server{
		Addr:         cfg.Server.HTTP.Address,
		Handler:      s.router,
		ReadTimeout:  cfg.Server.HTTP.ReadTimeout,
		WriteTimeout: cfg.Server.HTTP.WriteTimeout,
		IdleTimeout:  cfg.Server.HTTP.IdleTimeout,
	}

	// Create HTTPS server if configured
	if s.httpsReloader != nil {
		s.httpsServer = &http.Server{
			Addr:         cfg.Server.HTTPS.Address,
			Handler:      s.router,
			ReadTimeout:  cfg.Server.HTTP.ReadTimeout,
			WriteTimeout: cfg.Server.HTTP.WriteTimeout,
			IdleTimeout:  cfg.Server.HTTP.IdleTimeout,
			TLSConfig: &tls.Config{
				GetCertificate: s.httpsReloader.GetCertificate,
				MinVersion:     tls.VersionTLS12,
			},
		}
	}

	return s, nil
}

// Start begins serving HTTP (and HTTPS if configured) requests.
func (s *Server) Start(ctx context.Context) error {
	errCh := make(chan error, 2)

	// Start HTTP server
	go func() {
		fmt.Printf("E2BGateway HTTP starting on %s\n", s.cfg.Server.HTTP.Address)
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("HTTP server error: %w", err)
		}
	}()

	// Start HTTPS server if configured
	var stopWatch context.CancelFunc
	if s.httpsServer != nil {
		// Derive a child context so the watcher can also be stopped by
		// Stop when Start was called with a context that is never cancelled
		// (e.g. context.Background()).
		watchCtx, watchCancel := context.WithCancel(ctx)
		watchDone := make(chan struct{})
		stopWatch = watchCancel
		s.setWatcher(watchCancel, watchDone)
		go func() {
			defer close(watchDone)
			s.httpsReloader.watch(watchCtx)
		}()
		go func() {
			fmt.Printf("E2BGateway HTTPS starting on %s\n", s.cfg.Server.HTTPS.Address)
			if err := s.httpsServer.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
				errCh <- fmt.Errorf("HTTPS server error: %w", err)
			}
		}()
	}

	// Wait for first error or context cancellation
	select {
	case err := <-errCh:
		if stopWatch != nil {
			stopWatch()
		}
		return err
	case <-ctx.Done():
		return nil
	}
}

// setWatcher records the certificate watcher's cancellation function and
// completion channel. When Stop already ran, a registration arriving after
// shutdown is cancelled immediately instead of being left active.
func (s *Server) setWatcher(cancel context.CancelFunc, done chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Record the completion channel in both cases, so a caller can join a
	// watcher that is cancelled here because it registered after Stop.
	s.watchDone = done
	if s.stopped {
		cancel()
		return
	}
	s.stopWatch = cancel
}

// stopWatcher cancels the certificate watcher, if one is running, and
// records the stopped state so late registrations from Start are cancelled
// as well.
func (s *Server) stopWatcher() {
	s.mu.Lock()
	s.stopped = true
	cancel := s.stopWatch
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// waitWatcher waits for the certificate watcher goroutine to exit and
// reports whether it exited before ctx was done. A nil completion channel
// means no watcher is running and there is nothing to wait for.
func (s *Server) waitWatcher(ctx context.Context) bool {
	s.mu.Lock()
	done := s.watchDone
	s.mu.Unlock()
	if done == nil {
		return true
	}
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
}

// Stop gracefully shuts down the server(s).
func (s *Server) Stop(ctx context.Context) error {
	// Cancel the certificate watcher before anything else: the context
	// passed to Start may never be cancelled. Cancelling does not wait, so a
	// reload still in progress (e.g. a slow certificate read) can neither
	// keep the listeners accepting connections nor eat into the budget for
	// draining them.
	s.stopWatcher()

	shutdownCtx, cancel := context.WithTimeout(ctx, shutdownTimeout)
	defer cancel()
	if s.httpsServer != nil {
		_ = s.httpsServer.Shutdown(shutdownCtx)
	}
	err := s.httpServer.Shutdown(shutdownCtx)

	// Waiting for the watcher to exit (bounded by watcherStopTimeout)
	// normally guarantees no reload logs or file reads race with work that
	// follows Stop. The watcher gets its own budget: a graceful drain can
	// consume the whole shutdown timeout (WebSocket and ConnectRPC
	// connections may hold on until the deadline), and sharing it would let
	// Stop return while a reload is still in flight. If the join budget
	// expires, an in-flight reload may still complete and log after Stop
	// returns; the warning below makes that visible. If the caller's context
	// is already done, the wait is best-effort.
	watcherCtx, watcherCancel := context.WithTimeout(ctx, watcherStopTimeout)
	defer watcherCancel()
	if !s.waitWatcher(watcherCtx) {
		slog.Warn("certificate watcher did not exit before shutdown completed",
			"reason", watcherCtx.Err())
	}
	return err
}

// Handler returns the HTTP handler for testing.
func (s *Server) Handler() http.Handler {
	return s.router
}

// authorized wraps a registered control-plane route with its required scope.
// The no-provider mode remains anonymous for local development and existing tests.
func (s *Server) authorized(scope string, handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if len(s.cfg.Auth.Providers) == 0 {
			handler(w, r)
			return
		}
		tenant, ok := auth.TenantFromContext(r.Context())
		if !ok {
			gwmiddleware.WriteError(w, http.StatusUnauthorized, "Authentication required")
			return
		}
		if !auth.HasScope(tenant, scope) {
			gwmiddleware.WriteError(w, http.StatusForbidden, "Insufficient permissions")
			return
		}
		handler(w, r)
	}
}

// buildRouter constructs the chi router with all middleware and routes.
func (s *Server) buildRouter() chi.Router {
	r := chi.NewRouter()
	sandboxRead := func(h http.HandlerFunc) http.HandlerFunc { return s.authorized("sandbox:read", h) }
	sandboxWrite := func(h http.HandlerFunc) http.HandlerFunc { return s.authorized("sandbox:write", h) }
	templateRead := func(h http.HandlerFunc) http.HandlerFunc { return s.authorized("template:read", h) }
	templateWrite := func(h http.HandlerFunc) http.HandlerFunc { return s.authorized("template:write", h) }
	warmPoolRead := func(h http.HandlerFunc) http.HandlerFunc { return s.authorized("warm-pool:read", h) }
	warmPoolWrite := func(h http.HandlerFunc) http.HandlerFunc { return s.authorized("warm-pool:write", h) }

	// Configured Prometheus scrape path (shared by the metrics middleware
	// self-skip and the route registration below).
	mpath := s.cfg.Observability.Metrics.Path
	if mpath == "" {
		mpath = "/metrics"
	}

	// Global middleware chain.
	// Metrics is registered OUTSIDE Recovery so that recovered panics are
	// observed: Recovery turns the panic into a 500 below Metrics, which
	// then records the request and its latency.
	r.Use(middleware.RequestID)
	r.Use(gwmiddleware.RealIP)
	r.Use(gwmiddleware.RequestLogger)
	r.Use(gwmiddleware.Metrics(mpath))
	r.Use(gwmiddleware.Recovery)
	r.Use(gwmiddleware.CORS)
	r.Use(gwmiddleware.Auth(s.authMgr))
	r.Use(gwmiddleware.RateLimit(s.cfg.RateLimit))
	r.Use(gwmiddleware.AuditLog)

	// Health endpoints (no auth)
	r.Get("/healthz", v1.HealthHandler)
	r.Get("/readyz", v1.ReadyHandler(s.registry))

	// Prometheus metrics endpoint (unauthenticated; exempted in middleware).
	if s.cfg.Observability.Metrics.Enabled {
		r.Get(mpath, metrics.Handler().ServeHTTP)
	}

	// -------------------------------------------------------
	// E2B official API routes (root-level, used by SDK/CLI)
	// -------------------------------------------------------

	// Sandbox lifecycle
	r.Post("/sandboxes", sandboxWrite(v1.CreateSandboxHandler(s.registry, s.routeMgr, s.cfg.Server.EnvdDomain)))
	r.Get("/sandboxes", sandboxRead(v1.ListSandboxesHandler(s.registry, s.routeMgr)))
	r.Get("/sandboxes/{sandboxID}", sandboxRead(v1.GetSandboxHandler(s.registry, s.routeMgr)))
	r.Delete("/sandboxes/{sandboxID}", sandboxWrite(v1.KillSandboxHandler(s.registry, s.routeMgr)))
	r.Post("/sandboxes/{sandboxID}/pause", sandboxWrite(v1.PauseSandboxHandler(s.registry, s.routeMgr)))
	r.Post("/sandboxes/{sandboxID}/resume", sandboxWrite(v1.ResumeSandboxHandler(s.registry, s.routeMgr)))
	r.Post("/sandboxes/{sandboxID}/timeout", sandboxWrite(v1.SetTimeoutHandler(s.registry, s.routeMgr)))
	r.Patch("/sandboxes/{sandboxID}/timeout", sandboxWrite(v1.SetTimeoutHandler(s.registry, s.routeMgr)))

	// Environment variables
	r.Post("/sandboxes/{sandboxID}/envs", sandboxWrite(v1.SetEnvsHandler(s.registry, s.routeMgr)))

	// Logs
	r.Get("/sandboxes/{sandboxID}/logs", sandboxRead(v1.GetLogsHandler(s.registry, s.routeMgr)))

	// Filesystem (envd-compatible paths)
	r.Post("/sandboxes/{sandboxID}/filesystem/upload", sandboxWrite(v1.UploadFileHandler(s.registry, s.routeMgr)))
	r.Get("/sandboxes/{sandboxID}/filesystem/download", sandboxRead(v1.DownloadFileHandler(s.registry, s.routeMgr)))
	r.Post("/sandboxes/{sandboxID}/filesystem/list", sandboxRead(v1.ListFilesHandler(s.registry, s.routeMgr)))
	r.Post("/sandboxes/{sandboxID}/filesystem/mkdir", sandboxWrite(v1.MakeDirHandler(s.registry, s.routeMgr)))
	r.Post("/sandboxes/{sandboxID}/filesystem/rm", sandboxWrite(v1.RemoveFileHandler(s.registry, s.routeMgr)))
	r.Post("/sandboxes/{sandboxID}/filesystem/move", sandboxWrite(v1.MoveFileHandler(s.registry, s.routeMgr)))
	// Legacy filesystem paths (backward compatibility)
	r.Post("/sandboxes/{sandboxID}/files", sandboxWrite(v1.WriteFileHandler(s.registry, s.routeMgr)))
	r.Get("/sandboxes/{sandboxID}/files", sandboxRead(v1.ReadFileHandler(s.registry, s.routeMgr)))
	r.Post("/sandboxes/{sandboxID}/files/upload", sandboxWrite(v1.UploadFileHandler(s.registry, s.routeMgr)))
	r.Get("/sandboxes/{sandboxID}/files/download", sandboxRead(v1.DownloadFileHandler(s.registry, s.routeMgr)))
	r.Post("/sandboxes/{sandboxID}/files/list", sandboxRead(v1.ListFilesHandler(s.registry, s.routeMgr)))
	r.Post("/sandboxes/{sandboxID}/files/make-dir", sandboxWrite(v1.MakeDirHandler(s.registry, s.routeMgr)))
	r.Post("/sandboxes/{sandboxID}/files/remove", sandboxWrite(v1.RemoveFileHandler(s.registry, s.routeMgr)))

	// Commands (envd-compatible)
	r.Post("/sandboxes/{sandboxID}/commands", sandboxWrite(v1.RunCommandHandler(s.registry, s.routeMgr)))
	r.Get("/sandboxes/{sandboxID}/commands", sandboxRead(v1.ListProcessesHandler(s.registry, s.routeMgr)))
	r.Post("/sandboxes/{sandboxID}/commands/{processID}/kill", sandboxWrite(v1.KillProcessHandler(s.registry, s.routeMgr)))
	r.Post("/sandboxes/{sandboxID}/commands/{processID}/input", sandboxWrite(v1.SendStdinHandler(s.registry, s.routeMgr)))

	// Code execution
	r.Post("/sandboxes/{sandboxID}/code", sandboxWrite(v1.ExecuteCodeHandler(s.registry, s.routeMgr)))

	// WebSocket streaming (E2B SDK compatible — ConnectRPC + WebSocket dual mode)
	r.Get("/sandboxes/{sandboxID}/ws", sandboxWrite(v1.ExecuteCodeStreamHandler(s.registry, s.routeMgr)))

	// Processes (legacy paths)
	r.Get("/sandboxes/{sandboxID}/processes", sandboxRead(v1.ListProcessesHandler(s.registry, s.routeMgr)))
	r.Post("/sandboxes/{sandboxID}/processes/{processID}/kill", sandboxWrite(v1.KillProcessHandler(s.registry, s.routeMgr)))
	r.Post("/sandboxes/{sandboxID}/processes/{processID}/stdin", sandboxWrite(v1.SendStdinHandler(s.registry, s.routeMgr)))

	// Snapshots
	r.Post("/sandboxes/{sandboxID}/snapshots", sandboxWrite(v1.CreateSnapshotHandler(s.registry, s.routeMgr)))
	r.Get("/sandboxes/{sandboxID}/snapshots", sandboxRead(v1.ListSnapshotsHandler(s.registry, s.routeMgr)))

	// Ports
	r.Get("/sandboxes/{sandboxID}/ports", sandboxRead(v1.ListPortsHandler(s.registry, s.routeMgr)))
	r.Get("/sandboxes/{sandboxID}/ports/{port}", sandboxRead(v1.GetPortURLHandler(s.registry, s.routeMgr)))

	// Access token
	r.Post("/sandboxes/{sandboxID}/access-token", sandboxWrite(v1.GetAccessTokenHandler(s.registry, s.routeMgr)))

	// Templates
	r.Get("/templates", templateRead(v1.ListTemplatesHandler(s.registry, s.routeMgr)))
	r.Get("/templates/{templateID}", templateRead(v1.GetTemplateHandler(s.registry, s.routeMgr)))
	r.Post("/templates", templateWrite(v1.CreateTemplateHandler(s.registry, s.routeMgr)))
	r.Delete("/templates/{templateID}", templateWrite(v1.DeleteTemplateHandler(s.registry, s.routeMgr)))
	r.Post("/templates/{templateID}/builds", templateWrite(v1.TriggerBuildHandler(s.registry, s.routeMgr)))
	r.Post("/templates/{templateID}/builds/{buildID}/status", templateRead(v1.GetBuildStatusHandler(s.registry, s.routeMgr)))
	r.Post("/templates/{templateID}/aliases", templateWrite(v1.CreateAliasHandler(s.registry, s.routeMgr)))
	r.Delete("/templates/{templateID}/aliases/{alias}", templateWrite(v1.DeleteAliasHandler(s.registry, s.routeMgr)))

	// Template Tags
	r.Post("/templates/{templateID}/tags", templateWrite(v1.CreateTagHandler(s.registry, s.routeMgr)))
	r.Get("/templates/{templateID}/tags", templateRead(v1.ListTagsHandler(s.registry, s.routeMgr)))
	r.Delete("/templates/{templateID}/tags/{tagName}", templateWrite(v1.DeleteTagHandler(s.registry, s.routeMgr)))

	// Warm pools
	r.Get("/warm-pools", warmPoolRead(v1.ListWarmPoolsHandler(s.registry, s.routeMgr)))
	r.Post("/warm-pools", warmPoolWrite(v1.CreateWarmPoolHandler(s.registry, s.routeMgr)))
	r.Get("/warm-pools/{warmPoolID}", warmPoolRead(v1.GetWarmPoolHandler(s.registry, s.routeMgr)))
	r.Delete("/warm-pools/{warmPoolID}", warmPoolWrite(v1.DeleteWarmPoolHandler(s.registry, s.routeMgr)))
	r.Post("/warm-pools/{warmPoolID}/size", warmPoolWrite(v1.UpdateWarmPoolSizeHandler(s.registry, s.routeMgr)))

	// -------------------------------------------------------
	// E2B v2 API routes
	// -------------------------------------------------------

	r.Route("/v2", func(r chi.Router) {
		// v2 Sandboxes
		r.Get("/sandboxes", sandboxRead(v1.ListSandboxesHandlerV2(s.registry, s.routeMgr)))
		r.Get("/sandboxes/{sandboxID}/logs", sandboxRead(v1.GetLogsHandler(s.registry, s.routeMgr)))
		r.Get("/sandboxes/{sandboxID}/metrics", sandboxRead(v1.GetMetricsHandler(s.registry, s.routeMgr)))

		// v2 Filesystem (same paths under /v2 prefix)
		r.Post("/sandboxes/{sandboxID}/filesystem/upload", sandboxWrite(v1.UploadFileHandler(s.registry, s.routeMgr)))
		r.Get("/sandboxes/{sandboxID}/filesystem/download", sandboxRead(v1.DownloadFileHandler(s.registry, s.routeMgr)))
		r.Post("/sandboxes/{sandboxID}/filesystem/list", sandboxRead(v1.ListFilesHandler(s.registry, s.routeMgr)))
		r.Post("/sandboxes/{sandboxID}/filesystem/mkdir", sandboxWrite(v1.MakeDirHandler(s.registry, s.routeMgr)))
		r.Post("/sandboxes/{sandboxID}/filesystem/rm", sandboxWrite(v1.RemoveFileHandler(s.registry, s.routeMgr)))
		r.Post("/sandboxes/{sandboxID}/filesystem/move", sandboxWrite(v1.MoveFileHandler(s.registry, s.routeMgr)))

		// v2 Templates
		r.Get("/templates", templateRead(v1.ListTemplatesHandlerV2(s.registry, s.routeMgr)))
		r.Post("/templates", templateWrite(v1.CreateTemplateHandlerV2(s.registry, s.routeMgr)))
		r.Patch("/templates/{templateID}", templateWrite(v1.UpdateTemplateHandler(s.registry, s.routeMgr)))

		// v2 Template Tags
		r.Post("/templates/{templateID}/tags", templateWrite(v1.CreateTagHandler(s.registry, s.routeMgr)))
		r.Get("/templates/{templateID}/tags", templateRead(v1.ListTagsHandler(s.registry, s.routeMgr)))
		r.Delete("/templates/{templateID}/tags/{tagName}", templateWrite(v1.DeleteTagHandler(s.registry, s.routeMgr)))
	})

	// -------------------------------------------------------
	// Legacy /api/v1 routes (backward compatibility)
	// -------------------------------------------------------

	r.Route("/api/v1", func(r chi.Router) {
		// Sandbox lifecycle
		r.Post("/sandboxes", sandboxWrite(v1.CreateSandboxHandler(s.registry, s.routeMgr, s.cfg.Server.EnvdDomain)))
		r.Get("/sandboxes", sandboxRead(v1.ListSandboxesHandler(s.registry, s.routeMgr)))
		r.Get("/sandboxes/{sandboxID}", sandboxRead(v1.GetSandboxHandler(s.registry, s.routeMgr)))
		r.Delete("/sandboxes/{sandboxID}", sandboxWrite(v1.KillSandboxHandler(s.registry, s.routeMgr)))
		r.Post("/sandboxes/{sandboxID}/pause", sandboxWrite(v1.PauseSandboxHandler(s.registry, s.routeMgr)))
		r.Post("/sandboxes/{sandboxID}/resume", sandboxWrite(v1.ResumeSandboxHandler(s.registry, s.routeMgr)))
		r.Patch("/sandboxes/{sandboxID}/timeout", sandboxWrite(v1.SetTimeoutHandler(s.registry, s.routeMgr)))

		// Code execution
		r.Post("/sandboxes/{sandboxID}/code", sandboxWrite(v1.ExecuteCodeHandler(s.registry, s.routeMgr)))
		r.Post("/sandboxes/{sandboxID}/code/executions", sandboxWrite(v1.StartExecutionHandler(s.registry, s.routeMgr)))
		r.Get("/sandboxes/{sandboxID}/code/executions/{executionID}", sandboxRead(v1.GetExecutionHandler(s.registry, s.routeMgr)))
		// WebSocket streaming
		r.Get("/sandboxes/{sandboxID}/ws", sandboxWrite(v1.ExecuteCodeStreamHandler(s.registry, s.routeMgr)))

		// Commands
		r.Post("/sandboxes/{sandboxID}/commands", sandboxWrite(v1.RunCommandHandler(s.registry, s.routeMgr)))

		// Processes
		r.Get("/sandboxes/{sandboxID}/processes", sandboxRead(v1.ListProcessesHandler(s.registry, s.routeMgr)))
		r.Post("/sandboxes/{sandboxID}/processes/{processID}/kill", sandboxWrite(v1.KillProcessHandler(s.registry, s.routeMgr)))
		r.Post("/sandboxes/{sandboxID}/processes/{processID}/stdin", sandboxWrite(v1.SendStdinHandler(s.registry, s.routeMgr)))

		// Filesystem
		r.Get("/sandboxes/{sandboxID}/files", sandboxRead(v1.ReadFileHandler(s.registry, s.routeMgr)))
		r.Post("/sandboxes/{sandboxID}/files", sandboxWrite(v1.WriteFileHandler(s.registry, s.routeMgr)))
		r.Post("/sandboxes/{sandboxID}/files/upload", sandboxWrite(v1.UploadFileHandler(s.registry, s.routeMgr)))
		r.Get("/sandboxes/{sandboxID}/files/download", sandboxRead(v1.DownloadFileHandler(s.registry, s.routeMgr)))
		r.Post("/sandboxes/{sandboxID}/files/list", sandboxRead(v1.ListFilesHandler(s.registry, s.routeMgr)))
		r.Post("/sandboxes/{sandboxID}/files/make-dir", sandboxWrite(v1.MakeDirHandler(s.registry, s.routeMgr)))
		r.Post("/sandboxes/{sandboxID}/files/remove", sandboxWrite(v1.RemoveFileHandler(s.registry, s.routeMgr)))

		// Snapshots
		r.Post("/sandboxes/{sandboxID}/snapshots", sandboxWrite(v1.CreateSnapshotHandler(s.registry, s.routeMgr)))
		r.Get("/sandboxes/{sandboxID}/snapshots", sandboxRead(v1.ListSnapshotsHandler(s.registry, s.routeMgr)))

		// Port forwarding
		r.Get("/sandboxes/{sandboxID}/ports", sandboxRead(v1.ListPortsHandler(s.registry, s.routeMgr)))
		r.Get("/sandboxes/{sandboxID}/ports/{port}", sandboxRead(v1.GetPortURLHandler(s.registry, s.routeMgr)))

		// Access token
		r.Post("/sandboxes/{sandboxID}/access-token", sandboxWrite(v1.GetAccessTokenHandler(s.registry, s.routeMgr)))

		// Templates
		r.Get("/templates", templateRead(v1.ListTemplatesHandler(s.registry, s.routeMgr)))
		r.Get("/templates/{templateID}", templateRead(v1.GetTemplateHandler(s.registry, s.routeMgr)))
		r.Post("/templates", templateWrite(v1.CreateTemplateHandler(s.registry, s.routeMgr)))
		r.Delete("/templates/{templateID}", templateWrite(v1.DeleteTemplateHandler(s.registry, s.routeMgr)))
		r.Post("/templates/{templateID}/builds", templateWrite(v1.TriggerBuildHandler(s.registry, s.routeMgr)))
		r.Post("/templates/{templateID}/builds/{buildID}/status", templateRead(v1.GetBuildStatusHandler(s.registry, s.routeMgr)))
		r.Post("/templates/{templateID}/aliases", templateWrite(v1.CreateAliasHandler(s.registry, s.routeMgr)))
		r.Delete("/templates/{templateID}/aliases/{alias}", templateWrite(v1.DeleteAliasHandler(s.registry, s.routeMgr)))

		// Warm pools
		r.Get("/warm-pools", warmPoolRead(v1.ListWarmPoolsHandler(s.registry, s.routeMgr)))
		r.Post("/warm-pools", warmPoolWrite(v1.CreateWarmPoolHandler(s.registry, s.routeMgr)))
		r.Get("/warm-pools/{warmPoolID}", warmPoolRead(v1.GetWarmPoolHandler(s.registry, s.routeMgr)))
		r.Delete("/warm-pools/{warmPoolID}", warmPoolWrite(v1.DeleteWarmPoolHandler(s.registry, s.routeMgr)))
		r.Post("/warm-pools/{warmPoolID}/size", warmPoolWrite(v1.UpdateWarmPoolSizeHandler(s.registry, s.routeMgr)))
	})

	// -------------------------------------------------------
	// envd data plane proxy (ConnectRPC) — MUST be last so it
	// doesn't shadow any API routes above.
	// The E2B SDK talks ConnectRPC to {port}-{sandboxID}.{domain};
	// this catch-all reverse-proxies those requests to the envd
	// daemon running inside the sandbox container.
	// -------------------------------------------------------
	r.HandleFunc("/*", s.envdProxyHandler().ServeHTTP)

	return r
}

// initAdapters initializes all configured backend adapters.
func (s *Server) initAdapters() error {
	for _, bcfg := range s.cfg.Backends {
		if !bcfg.Enabled {
			continue
		}
		var a adapter.SandboxAdapter
		var err error

		switch bcfg.Type {
		case "mock":
			a = mockadapter.New(s.registry)
		case "e2b-cloud":
			a, err = e2bcloudadapter.NewAdapter(bcfg, s.registry)
		case "agent-sandbox":
			a, err = agentsandboxadapter.NewAdapterFromConfig(bcfg, s.registry)
		case "agent-substrate":
			a, err = agentsubstrateadapter.NewAdapterFromConfig(bcfg, s.registry)
		case "opensandbox":
			a, err = opensandboxadapter.NewAdapterFromConfig(bcfg, s.registry)
		default:
			return fmt.Errorf("unknown adapter type: %s", bcfg.Type)
		}
		if err != nil {
			return fmt.Errorf("creating adapter %q: %w", bcfg.Name, err)
		}
		// Inject the session store so replicas mint and validate access tokens
		// against the same state.
		if aware, isAware := a.(sessionstore.Aware); isAware {
			aware.SetSessionStore(s.sessionStore)
		}
		if err := s.registry.Register(a); err != nil {
			return fmt.Errorf("registering adapter %q: %w", bcfg.Name, err)
		}
	}
	return nil
}
