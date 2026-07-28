package mock_test

import (
	"context"
	"testing"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
	mockadapter "github.com/e2bgateway/e2bgateway/internal/adapter/mock"
)

func TestMockAdapterName(t *testing.T) {
	a := mockadapter.New()
	if a.Name() != "mock" {
		t.Errorf("expected name 'mock', got %s", a.Name())
	}
}

func TestMockAdapterHealthCheck(t *testing.T) {
	a := mockadapter.New()
	if err := a.HealthCheck(context.Background()); err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

func TestMockAdapterSandboxLifecycle(t *testing.T) {
	a := mockadapter.New()
	ctx := context.Background()

	// Create sandbox
	sbx, err := a.CreateSandbox(ctx, &adapter.CreateSandboxRequest{
		TemplateID: "base",
		Timeout:    300,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if sbx.SandboxID == "" {
		t.Error("expected non-empty sandbox ID")
	}

	// Get sandbox
	got, err := a.GetSandbox(ctx, sbx.SandboxID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.TemplateID != "base" {
		t.Errorf("expected template 'base', got %s", got.TemplateID)
	}

	// List sandboxes
	list, err := a.ListSandboxes(ctx, adapter.ListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("expected 1 sandbox, got %d", len(list))
	}

	// Pause sandbox
	if err := a.PauseSandbox(ctx, sbx.SandboxID); err != nil {
		t.Fatalf("pause: %v", err)
	}
	got, _ = a.GetSandbox(ctx, sbx.SandboxID)
	if got.Status != adapter.SandboxStatusPaused {
		t.Errorf("expected paused status, got %s", got.Status)
	}

	// Resume sandbox
	resumed, err := a.ResumeSandbox(ctx, sbx.SandboxID)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if resumed.Status != adapter.SandboxStatusRunning {
		t.Errorf("expected running status, got %s", resumed.Status)
	}

	// Set timeout
	if err := a.SetTimeout(ctx, sbx.SandboxID, 600000000000); err != nil { // 10 minutes in nanoseconds
		t.Fatalf("set timeout: %v", err)
	}

	// Kill sandbox
	if err := a.KillSandbox(ctx, sbx.SandboxID); err != nil {
		t.Fatalf("kill: %v", err)
	}

	// Verify sandbox is gone
	_, err = a.GetSandbox(ctx, sbx.SandboxID)
	if err == nil {
		t.Error("expected error after kill")
	}
}

func TestMockAdapterCodeExecution(t *testing.T) {
	a := mockadapter.New()
	ctx := context.Background()

	sbx, _ := a.CreateSandbox(ctx, &adapter.CreateSandboxRequest{TemplateID: "base"})

	// Execute code
	result, err := a.ExecuteCode(ctx, sbx.SandboxID, &adapter.CodeExecutionRequest{
		Code:     "print('hello')",
		Language: "python",
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", result.ExitCode)
	}

	// Run command
	cmdResult, err := a.RunCommand(ctx, sbx.SandboxID, &adapter.CommandRequest{
		Command: "echo hello",
	})
	if err != nil {
		t.Fatalf("run command: %v", err)
	}
	if cmdResult.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", cmdResult.ExitCode)
	}
}

func TestMockAdapterFileOperations(t *testing.T) {
	a := mockadapter.New()
	ctx := context.Background()

	sbx, _ := a.CreateSandbox(ctx, &adapter.CreateSandboxRequest{TemplateID: "base"})

	// Write file
	if err := a.WriteFile(ctx, sbx.SandboxID, &adapter.FileWriteRequest{
		Path:    "/test.txt",
		Content: []byte("hello"),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Read file
	content, err := a.ReadFile(ctx, sbx.SandboxID, "/test.txt")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if content.Path != "/test.txt" {
		t.Errorf("expected path '/test.txt', got %s", content.Path)
	}

	// List files
	files, err := a.ListFiles(ctx, sbx.SandboxID, "/")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if files == nil {
		t.Error("expected non-nil file list")
	}

	// Make dir
	if err := a.MakeDir(ctx, sbx.SandboxID, "/test-dir"); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Remove file
	if err := a.RemoveFile(ctx, sbx.SandboxID, "/test.txt"); err != nil {
		t.Fatalf("remove: %v", err)
	}
}

func TestMockAdapterTemplates(t *testing.T) {
	a := mockadapter.New()
	ctx := context.Background()

	// List templates
	templates, err := a.ListTemplates(ctx, adapter.ListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(templates) < 2 {
		t.Errorf("expected at least 2 templates, got %d", len(templates))
	}

	// Get template
	tmpl, err := a.GetTemplate(ctx, "base")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if tmpl.TemplateID != "base" {
		t.Errorf("expected template ID 'base', got %s", tmpl.TemplateID)
	}

	// Create template
	build, err := a.CreateTemplate(ctx, &adapter.CreateTemplateRequest{
		Name:     "custom",
		CPUCount: 4,
		MemoryMB: 2048,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if build.TemplateID == "" {
		t.Error("expected non-empty template ID")
	}

	// Delete template
	if err := a.DeleteTemplate(ctx, build.TemplateID); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

func TestMockAdapterWarmPools(t *testing.T) {
	a := mockadapter.New()
	ctx := context.Background()

	// Create warm pool
	pool, err := a.CreateWarmPool(ctx, &adapter.WarmPoolCreateRequest{
		TemplateID: "base",
		TargetSize: 3,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if pool.WarmPoolID == "" {
		t.Error("expected non-empty warm pool ID")
	}

	// List warm pools
	pools, err := a.ListWarmPools(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(pools) != 1 {
		t.Errorf("expected 1 warm pool, got %d", len(pools))
	}

	// Get warm pool
	got, err := a.GetWarmPool(ctx, pool.WarmPoolID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.TargetSize != 3 {
		t.Errorf("expected target size 3, got %d", got.TargetSize)
	}

	// Update size
	if err := a.UpdateWarmPoolSize(ctx, pool.WarmPoolID, 5); err != nil {
		t.Fatalf("update: %v", err)
	}

	// Delete warm pool
	if err := a.DeleteWarmPool(ctx, pool.WarmPoolID); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

func TestMockAdapterSnapshots(t *testing.T) {
	a := mockadapter.New()
	ctx := context.Background()

	sbx, _ := a.CreateSandbox(ctx, &adapter.CreateSandboxRequest{TemplateID: "base"})

	// Create snapshot
	snap, err := a.CreateSnapshot(ctx, sbx.SandboxID, &adapter.SnapshotRequest{
		Name:        "test-snap",
		Description: "test",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if snap.SnapshotID == "" {
		t.Error("expected non-empty snapshot ID")
	}

	// List snapshots
	snaps, err := a.ListSnapshots(ctx, sbx.SandboxID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(snaps) != 1 {
		t.Errorf("expected 1 snapshot, got %d", len(snaps))
	}
}

func TestMockAdapterPorts(t *testing.T) {
	a := mockadapter.New()
	ctx := context.Background()

	sbx, _ := a.CreateSandbox(ctx, &adapter.CreateSandboxRequest{TemplateID: "base"})

	// List ports
	ports, err := a.ListPorts(ctx, sbx.SandboxID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(ports) < 1 {
		t.Error("expected at least 1 port")
	}

	// Get port URL
	url, err := a.GetPortURL(ctx, sbx.SandboxID, 3000)
	if err != nil {
		t.Fatalf("get url: %v", err)
	}
	if url == "" {
		t.Error("expected non-empty URL")
	}
}

func TestMockAdapterAccessToken(t *testing.T) {
	a := mockadapter.New()
	ctx := context.Background()

	sbx, _ := a.CreateSandbox(ctx, &adapter.CreateSandboxRequest{TemplateID: "base"})

	// Get access token
	token, err := a.GetAccessToken(ctx, sbx.SandboxID)
	if err != nil {
		t.Fatalf("get token: %v", err)
	}
	if token.Token == "" {
		t.Error("expected non-empty token")
	}
}

func TestMockAdapterBuilds(t *testing.T) {
	a := mockadapter.New()
	ctx := context.Background()

	// Trigger build
	build, err := a.TriggerBuild(ctx, "base", &adapter.BuildRequest{
		Dockerfile: "FROM python:3.11",
	})
	if err != nil {
		t.Fatalf("trigger: %v", err)
	}
	if build.BuildID == "" {
		t.Error("expected non-empty build ID")
	}

	// Get build status
	status, err := a.GetBuildStatus(ctx, "base", build.BuildID)
	if err != nil {
		t.Fatalf("get status: %v", err)
	}
	if status.Status != "ready" {
		t.Errorf("expected status 'ready', got %s", status.Status)
	}
}

func TestMockAdapterAliases(t *testing.T) {
	a := mockadapter.New()
	ctx := context.Background()

	// Create alias
	if err := a.CreateAlias(ctx, "base", "latest"); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Delete alias
	if err := a.DeleteAlias(ctx, "base", "latest"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// Delete non-existent alias
	if err := a.DeleteAlias(ctx, "base", "nonexistent"); err == nil {
		t.Error("expected error for non-existent alias")
	}
}

func TestMockAdapterNotFoundErrors(t *testing.T) {
	a := mockadapter.New()
	ctx := context.Background()

	// Get non-existent sandbox
	_, err := a.GetSandbox(ctx, "nonexistent")
	if err == nil {
		t.Error("expected error for non-existent sandbox")
	}

	// Kill non-existent sandbox
	if err := a.KillSandbox(ctx, "nonexistent"); err == nil {
		t.Error("expected error for non-existent sandbox")
	}

	// Pause non-existent sandbox
	if err := a.PauseSandbox(ctx, "nonexistent"); err == nil {
		t.Error("expected error for non-existent sandbox")
	}

	// Execute code on non-existent sandbox
	_, err = a.ExecuteCode(ctx, "nonexistent", &adapter.CodeExecutionRequest{})
	if err == nil {
		t.Error("expected error for non-existent sandbox")
	}

	// Get non-existent template
	_, err = a.GetTemplate(ctx, "nonexistent")
	if err == nil {
		t.Error("expected error for non-existent template")
	}

	// Get non-existent warm pool
	_, err = a.GetWarmPool(ctx, "nonexistent")
	if err == nil {
		t.Error("expected error for non-existent warm pool")
	}
}
