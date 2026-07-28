package cmd

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/e2bgateway/e2bgateway/internal/config"
	"github.com/e2bgateway/e2bgateway/internal/observability"
	"github.com/e2bgateway/e2bgateway/internal/server"
)

func newServeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the E2BGateway server",
		Long:  "Start the E2BGateway HTTP/WebSocket server with configured backends.",
		RunE:  runServe,
	}
	return cmd
}

func runServe(cmd *cobra.Command, args []string) error {
	configPath, _ := cmd.Flags().GetString("config")

	// Load configuration
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Initialize observability
	otelShutdown, err := observability.Init(cmd.Context(), cfg.Observability)
	if err != nil {
		return fmt.Errorf("failed to initialize observability: %w", err)
	}
	defer func() { _ = otelShutdown(cmd.Context()) }()

	// Create and start server
	srv, err := server.New(cfg)
	if err != nil {
		return fmt.Errorf("failed to create server: %w", err)
	}

	// Graceful shutdown
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Start(ctx)
	}()

	select {
	case <-ctx.Done():
		fmt.Fprintln(os.Stderr, "Shutting down gracefully...")
		return srv.Stop(cmd.Context())
	case err := <-errCh:
		return err
	}
}
