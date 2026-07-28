// Package server implements the E2BGateway HTTP/WebSocket server.
package server

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
	agentsandboxadapter "github.com/e2bgateway/e2bgateway/internal/adapter/agentsandbox"
	e2bcloudadapter "github.com/e2bgateway/e2bgateway/internal/adapter/e2bcloud"
	mockadapter "github.com/e2bgateway/e2bgateway/internal/adapter/mock"
	opensandboxadapter "github.com/e2bgateway/e2bgateway/internal/adapter/opensandbox"
	v1 "github.com/e2bgateway/e2bgateway/internal/api/v1"
	"github.com/e2bgateway/e2bgateway/internal/auth"
	"github.com/e2bgateway/e2bgateway/internal/config"
	"github.com/e2bgateway/e2bgateway/internal/routing"
	gwmiddleware "github.com/e2bgateway/e2bgateway/internal/server/middleware"
)

// Server is the main E2BGateway HTTP server.
type Server struct {
	cfg        *config.Config
	httpServer *http.Server
	router     chi.Router
	registry   *adapter.Registry
	authMgr    *auth.Manager
	routeMgr   *routing.Router
}

// New creates a new Server instance.
func New(cfg *config.Config) (*Server, error) {
	s := &Server{cfg: cfg}

	// Initialize adapter registry
	s.registry = adapter.NewRegistry()
	if err := s.initAdapters(); err != nil {
		return nil, fmt.Errorf("initializing adapters: %w", err)
	}

	// Initialize auth manager
	s.authMgr = auth.NewManager(cfg.Auth)

	// Initialize routing
	s.routeMgr = routing.NewRouter(cfg.Routing, s.registry)

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

	return s, nil
}

// Start begins serving HTTP requests.
func (s *Server) Start(ctx context.Context) error {
	fmt.Printf("E2BGateway starting on %s\n", s.cfg.Server.HTTP.Address)
	if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("HTTP server error: %w", err)
	}
	return nil
}

// Stop gracefully shuts down the server.
func (s *Server) Stop(ctx context.Context) error {
	shutdownCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return s.httpServer.Shutdown(shutdownCtx)
}

// Handler returns the HTTP handler for testing.
func (s *Server) Handler() http.Handler {
	return s.router
}

// buildRouter constructs the chi router with all middleware and routes.
func (s *Server) buildRouter() chi.Router {
	r := chi.NewRouter()

	// Global middleware chain
	r.Use(middleware.RequestID)
	r.Use(gwmiddleware.RealIP)
	r.Use(gwmiddleware.RequestLogger)
	r.Use(gwmiddleware.Recovery)
	r.Use(gwmiddleware.CORS)
	r.Use(gwmiddleware.Auth(s.authMgr))
	r.Use(gwmiddleware.RateLimit(s.cfg.RateLimit))
	r.Use(gwmiddleware.AuditLog)

	// Health endpoints (no auth)
	r.Get("/healthz", v1.HealthHandler)
	r.Get("/readyz", v1.ReadyHandler(s.registry))

	// -------------------------------------------------------
	// E2B official API routes (root-level, used by SDK/CLI)
	// -------------------------------------------------------

	// Sandbox lifecycle
	r.Post("/sandboxes", v1.CreateSandboxHandler(s.registry, s.routeMgr, s.cfg.Server.EnvdDomain))
	r.Get("/sandboxes", v1.ListSandboxesHandler(s.registry, s.routeMgr))
	r.Get("/sandboxes/{sandboxID}", v1.GetSandboxHandler(s.registry, s.routeMgr))
	r.Delete("/sandboxes/{sandboxID}", v1.KillSandboxHandler(s.registry, s.routeMgr))
	r.Post("/sandboxes/{sandboxID}/pause", v1.PauseSandboxHandler(s.registry, s.routeMgr))
	r.Post("/sandboxes/{sandboxID}/resume", v1.ResumeSandboxHandler(s.registry, s.routeMgr))
	r.Post("/sandboxes/{sandboxID}/timeout", v1.SetTimeoutHandler(s.registry, s.routeMgr))
	r.Patch("/sandboxes/{sandboxID}/timeout", v1.SetTimeoutHandler(s.registry, s.routeMgr))

	// Environment variables
	r.Post("/sandboxes/{sandboxID}/envs", v1.SetEnvsHandler(s.registry, s.routeMgr))

	// Logs
	r.Get("/sandboxes/{sandboxID}/logs", v1.GetLogsHandler(s.registry, s.routeMgr))

	// Filesystem (envd-compatible paths)
	r.Post("/sandboxes/{sandboxID}/filesystem/upload", v1.UploadFileHandler(s.registry, s.routeMgr))
	r.Get("/sandboxes/{sandboxID}/filesystem/download", v1.DownloadFileHandler(s.registry, s.routeMgr))
	r.Post("/sandboxes/{sandboxID}/filesystem/list", v1.ListFilesHandler(s.registry, s.routeMgr))
	r.Post("/sandboxes/{sandboxID}/filesystem/mkdir", v1.MakeDirHandler(s.registry, s.routeMgr))
	r.Post("/sandboxes/{sandboxID}/filesystem/rm", v1.RemoveFileHandler(s.registry, s.routeMgr))
	r.Post("/sandboxes/{sandboxID}/filesystem/move", v1.MoveFileHandler(s.registry, s.routeMgr))
	// Legacy filesystem paths (backward compatibility)
	r.Post("/sandboxes/{sandboxID}/files", v1.WriteFileHandler(s.registry, s.routeMgr))
	r.Get("/sandboxes/{sandboxID}/files", v1.ReadFileHandler(s.registry, s.routeMgr))
	r.Post("/sandboxes/{sandboxID}/files/upload", v1.UploadFileHandler(s.registry, s.routeMgr))
	r.Get("/sandboxes/{sandboxID}/files/download", v1.DownloadFileHandler(s.registry, s.routeMgr))
	r.Post("/sandboxes/{sandboxID}/files/list", v1.ListFilesHandler(s.registry, s.routeMgr))
	r.Post("/sandboxes/{sandboxID}/files/make-dir", v1.MakeDirHandler(s.registry, s.routeMgr))
	r.Post("/sandboxes/{sandboxID}/files/remove", v1.RemoveFileHandler(s.registry, s.routeMgr))

	// Commands (envd-compatible)
	r.Post("/sandboxes/{sandboxID}/commands", v1.RunCommandHandler(s.registry, s.routeMgr))
	r.Get("/sandboxes/{sandboxID}/commands", v1.ListProcessesHandler(s.registry, s.routeMgr))
	r.Post("/sandboxes/{sandboxID}/commands/{processID}/kill", v1.KillProcessHandler(s.registry, s.routeMgr))
	r.Post("/sandboxes/{sandboxID}/commands/{processID}/input", v1.SendStdinHandler(s.registry, s.routeMgr))

	// Code execution
	r.Post("/sandboxes/{sandboxID}/code", v1.ExecuteCodeHandler(s.registry, s.routeMgr))

	// Processes (legacy paths)
	r.Get("/sandboxes/{sandboxID}/processes", v1.ListProcessesHandler(s.registry, s.routeMgr))
	r.Post("/sandboxes/{sandboxID}/processes/{processID}/kill", v1.KillProcessHandler(s.registry, s.routeMgr))
	r.Post("/sandboxes/{sandboxID}/processes/{processID}/stdin", v1.SendStdinHandler(s.registry, s.routeMgr))

	// Snapshots
	r.Post("/sandboxes/{sandboxID}/snapshots", v1.CreateSnapshotHandler(s.registry, s.routeMgr))
	r.Get("/sandboxes/{sandboxID}/snapshots", v1.ListSnapshotsHandler(s.registry, s.routeMgr))

	// Ports
	r.Get("/sandboxes/{sandboxID}/ports", v1.ListPortsHandler(s.registry, s.routeMgr))
	r.Get("/sandboxes/{sandboxID}/ports/{port}", v1.GetPortURLHandler(s.registry, s.routeMgr))

	// Access token
	r.Post("/sandboxes/{sandboxID}/access-token", v1.GetAccessTokenHandler(s.registry, s.routeMgr))

	// Templates
	r.Get("/templates", v1.ListTemplatesHandler(s.registry, s.routeMgr))
	r.Get("/templates/{templateID}", v1.GetTemplateHandler(s.registry, s.routeMgr))
	r.Post("/templates", v1.CreateTemplateHandler(s.registry, s.routeMgr))
	r.Delete("/templates/{templateID}", v1.DeleteTemplateHandler(s.registry, s.routeMgr))
	r.Post("/templates/{templateID}/builds", v1.TriggerBuildHandler(s.registry, s.routeMgr))
	r.Post("/templates/{templateID}/builds/{buildID}/status", v1.GetBuildStatusHandler(s.registry, s.routeMgr))
	r.Post("/templates/{templateID}/aliases", v1.CreateAliasHandler(s.registry, s.routeMgr))
	r.Delete("/templates/{templateID}/aliases/{alias}", v1.DeleteAliasHandler(s.registry, s.routeMgr))

	// Template Tags
	r.Post("/templates/{templateID}/tags", v1.CreateTagHandler(s.registry, s.routeMgr))
	r.Get("/templates/{templateID}/tags", v1.ListTagsHandler(s.registry, s.routeMgr))
	r.Delete("/templates/{templateID}/tags/{tagName}", v1.DeleteTagHandler(s.registry, s.routeMgr))

	// Warm pools
	r.Get("/warm-pools", v1.ListWarmPoolsHandler(s.registry, s.routeMgr))
	r.Post("/warm-pools", v1.CreateWarmPoolHandler(s.registry, s.routeMgr))
	r.Get("/warm-pools/{warmPoolID}", v1.GetWarmPoolHandler(s.registry, s.routeMgr))
	r.Delete("/warm-pools/{warmPoolID}", v1.DeleteWarmPoolHandler(s.registry, s.routeMgr))
	r.Post("/warm-pools/{warmPoolID}/size", v1.UpdateWarmPoolSizeHandler(s.registry, s.routeMgr))

	// -------------------------------------------------------
	// E2B v2 API routes
	// -------------------------------------------------------

	r.Route("/v2", func(r chi.Router) {
		// v2 Sandboxes
		r.Get("/sandboxes", v1.ListSandboxesHandlerV2(s.registry, s.routeMgr))
		r.Get("/sandboxes/{sandboxID}/logs", v1.GetLogsHandler(s.registry, s.routeMgr))
		r.Get("/sandboxes/{sandboxID}/metrics", v1.GetMetricsHandler(s.registry, s.routeMgr))

		// v2 Filesystem (same paths under /v2 prefix)
		r.Post("/sandboxes/{sandboxID}/filesystem/upload", v1.UploadFileHandler(s.registry, s.routeMgr))
		r.Get("/sandboxes/{sandboxID}/filesystem/download", v1.DownloadFileHandler(s.registry, s.routeMgr))
		r.Post("/sandboxes/{sandboxID}/filesystem/list", v1.ListFilesHandler(s.registry, s.routeMgr))
		r.Post("/sandboxes/{sandboxID}/filesystem/mkdir", v1.MakeDirHandler(s.registry, s.routeMgr))
		r.Post("/sandboxes/{sandboxID}/filesystem/rm", v1.RemoveFileHandler(s.registry, s.routeMgr))
		r.Post("/sandboxes/{sandboxID}/filesystem/move", v1.MoveFileHandler(s.registry, s.routeMgr))

		// v2 Templates
		r.Get("/templates", v1.ListTemplatesHandlerV2(s.registry, s.routeMgr))
		r.Post("/templates", v1.CreateTemplateHandlerV2(s.registry, s.routeMgr))
		r.Patch("/templates/{templateID}", v1.UpdateTemplateHandler(s.registry, s.routeMgr))

		// v2 Template Tags
		r.Post("/templates/{templateID}/tags", v1.CreateTagHandler(s.registry, s.routeMgr))
		r.Get("/templates/{templateID}/tags", v1.ListTagsHandler(s.registry, s.routeMgr))
		r.Delete("/templates/{templateID}/tags/{tagName}", v1.DeleteTagHandler(s.registry, s.routeMgr))
	})

	// -------------------------------------------------------
	// Legacy /api/v1 routes (backward compatibility)
	// -------------------------------------------------------

	r.Route("/api/v1", func(r chi.Router) {
		// Sandbox lifecycle
		r.Post("/sandboxes", v1.CreateSandboxHandler(s.registry, s.routeMgr, s.cfg.Server.EnvdDomain))
		r.Get("/sandboxes", v1.ListSandboxesHandler(s.registry, s.routeMgr))
		r.Get("/sandboxes/{sandboxID}", v1.GetSandboxHandler(s.registry, s.routeMgr))
		r.Delete("/sandboxes/{sandboxID}", v1.KillSandboxHandler(s.registry, s.routeMgr))
		r.Post("/sandboxes/{sandboxID}/pause", v1.PauseSandboxHandler(s.registry, s.routeMgr))
		r.Post("/sandboxes/{sandboxID}/resume", v1.ResumeSandboxHandler(s.registry, s.routeMgr))
		r.Patch("/sandboxes/{sandboxID}/timeout", v1.SetTimeoutHandler(s.registry, s.routeMgr))

		// Code execution
		r.Post("/sandboxes/{sandboxID}/code", v1.ExecuteCodeHandler(s.registry, s.routeMgr))
		r.Post("/sandboxes/{sandboxID}/code/executions", v1.StartExecutionHandler(s.registry, s.routeMgr))
		r.Get("/sandboxes/{sandboxID}/code/executions/{executionID}", v1.GetExecutionHandler(s.registry, s.routeMgr))

		// Commands
		r.Post("/sandboxes/{sandboxID}/commands", v1.RunCommandHandler(s.registry, s.routeMgr))

		// Processes
		r.Get("/sandboxes/{sandboxID}/processes", v1.ListProcessesHandler(s.registry, s.routeMgr))
		r.Post("/sandboxes/{sandboxID}/processes/{processID}/kill", v1.KillProcessHandler(s.registry, s.routeMgr))
		r.Post("/sandboxes/{sandboxID}/processes/{processID}/stdin", v1.SendStdinHandler(s.registry, s.routeMgr))

		// Filesystem
		r.Get("/sandboxes/{sandboxID}/files", v1.ReadFileHandler(s.registry, s.routeMgr))
		r.Post("/sandboxes/{sandboxID}/files", v1.WriteFileHandler(s.registry, s.routeMgr))
		r.Post("/sandboxes/{sandboxID}/files/upload", v1.UploadFileHandler(s.registry, s.routeMgr))
		r.Get("/sandboxes/{sandboxID}/files/download", v1.DownloadFileHandler(s.registry, s.routeMgr))
		r.Post("/sandboxes/{sandboxID}/files/list", v1.ListFilesHandler(s.registry, s.routeMgr))
		r.Post("/sandboxes/{sandboxID}/files/make-dir", v1.MakeDirHandler(s.registry, s.routeMgr))
		r.Post("/sandboxes/{sandboxID}/files/remove", v1.RemoveFileHandler(s.registry, s.routeMgr))

		// Snapshots
		r.Post("/sandboxes/{sandboxID}/snapshots", v1.CreateSnapshotHandler(s.registry, s.routeMgr))
		r.Get("/sandboxes/{sandboxID}/snapshots", v1.ListSnapshotsHandler(s.registry, s.routeMgr))

		// Port forwarding
		r.Get("/sandboxes/{sandboxID}/ports", v1.ListPortsHandler(s.registry, s.routeMgr))
		r.Get("/sandboxes/{sandboxID}/ports/{port}", v1.GetPortURLHandler(s.registry, s.routeMgr))

		// Access token
		r.Post("/sandboxes/{sandboxID}/access-token", v1.GetAccessTokenHandler(s.registry, s.routeMgr))

		// Templates
		r.Get("/templates", v1.ListTemplatesHandler(s.registry, s.routeMgr))
		r.Get("/templates/{templateID}", v1.GetTemplateHandler(s.registry, s.routeMgr))
		r.Post("/templates", v1.CreateTemplateHandler(s.registry, s.routeMgr))
		r.Delete("/templates/{templateID}", v1.DeleteTemplateHandler(s.registry, s.routeMgr))
		r.Post("/templates/{templateID}/builds", v1.TriggerBuildHandler(s.registry, s.routeMgr))
		r.Post("/templates/{templateID}/builds/{buildID}/status", v1.GetBuildStatusHandler(s.registry, s.routeMgr))
		r.Post("/templates/{templateID}/aliases", v1.CreateAliasHandler(s.registry, s.routeMgr))
		r.Delete("/templates/{templateID}/aliases/{alias}", v1.DeleteAliasHandler(s.registry, s.routeMgr))

		// Warm pools
		r.Get("/warm-pools", v1.ListWarmPoolsHandler(s.registry, s.routeMgr))
		r.Post("/warm-pools", v1.CreateWarmPoolHandler(s.registry, s.routeMgr))
		r.Get("/warm-pools/{warmPoolID}", v1.GetWarmPoolHandler(s.registry, s.routeMgr))
		r.Delete("/warm-pools/{warmPoolID}", v1.DeleteWarmPoolHandler(s.registry, s.routeMgr))
		r.Post("/warm-pools/{warmPoolID}/size", v1.UpdateWarmPoolSizeHandler(s.registry, s.routeMgr))
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
			a = mockadapter.New()
		case "e2b-cloud":
			a, err = e2bcloudadapter.NewAdapter(bcfg)
		case "agent-sandbox":
			a, err = agentsandboxadapter.NewAdapterFromConfig(bcfg)
		case "opensandbox":
			a, err = opensandboxadapter.NewAdapterFromConfig(bcfg)
		default:
			a, err = adapter.New(bcfg)
		}
		if err != nil {
			return fmt.Errorf("creating adapter %q: %w", bcfg.Name, err)
		}
		if err := s.registry.Register(a); err != nil {
			return fmt.Errorf("registering adapter %q: %w", bcfg.Name, err)
		}
	}
	return nil
}
