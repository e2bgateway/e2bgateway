package adapter_test

import (
	"context"
	"testing"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
	mockadapter "github.com/e2bgateway/e2bgateway/internal/adapter/mock"
)

func TestMockAdapter_SandboxLifecycle(t *testing.T) {
	a := mockadapter.New()
	ctx := context.Background()

	// Health check
	if err := a.HealthCheck(ctx); err != nil {
		t.Fatalf("HealthCheck() error: %v", err)
	}

	// Create sandbox
	sbx, err := a.CreateSandbox(ctx, &adapter.CreateSandboxRequest{
		TemplateID: "base",
		Timeout:    300, // seconds
	})
	if err != nil {
		t.Fatalf("CreateSandbox() error: %v", err)
	}
	if sbx.SandboxID == "" {
		t.Error("expected non-empty sandbox ID")
	}
	if sbx.Status != adapter.SandboxStatusRunning {
		t.Errorf("expected status running, got %s", sbx.Status)
	}

	// Get sandbox
	got, err := a.GetSandbox(ctx, sbx.SandboxID)
	if err != nil {
		t.Fatalf("GetSandbox() error: %v", err)
	}
	if got.SandboxID != sbx.SandboxID {
		t.Errorf("expected ID %s, got %s", sbx.SandboxID, got.SandboxID)
	}

	// List sandboxes
	list, err := a.ListSandboxes(ctx, adapter.ListOptions{})
	if err != nil {
		t.Fatalf("ListSandboxes() error: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("expected 1 sandbox, got %d", len(list))
	}

	// Pause sandbox
	if err := a.PauseSandbox(ctx, sbx.SandboxID); err != nil {
		t.Fatalf("PauseSandbox() error: %v", err)
	}
	got, _ = a.GetSandbox(ctx, sbx.SandboxID)
	if got.Status != adapter.SandboxStatusPaused {
		t.Errorf("expected status paused, got %s", got.Status)
	}

	// Resume sandbox
	resumed, err := a.ResumeSandbox(ctx, sbx.SandboxID)
	if err != nil {
		t.Fatalf("ResumeSandbox() error: %v", err)
	}
	if resumed.Status != adapter.SandboxStatusRunning {
		t.Errorf("expected status running after resume, got %s", resumed.Status)
	}

	// Kill sandbox
	if err := a.KillSandbox(ctx, sbx.SandboxID); err != nil {
		t.Fatalf("KillSandbox() error: %v", err)
	}
	_, err = a.GetSandbox(ctx, sbx.SandboxID)
	if err == nil {
		t.Error("expected error getting killed sandbox")
	}
}

func TestMockAdapter_CodeExecution(t *testing.T) {
	a := mockadapter.New()
	ctx := context.Background()

	sbx, _ := a.CreateSandbox(ctx, &adapter.CreateSandboxRequest{TemplateID: "base"})

	result, err := a.ExecuteCode(ctx, sbx.SandboxID, &adapter.CodeExecutionRequest{
		Code: "print('hello')",
	})
	if err != nil {
		t.Fatalf("ExecuteCode() error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", result.ExitCode)
	}
	if result.Stdout == "" {
		t.Error("expected non-empty stdout")
	}
}

func TestMockAdapter_Templates(t *testing.T) {
	a := mockadapter.New()
	ctx := context.Background()

	templates, err := a.ListTemplates(ctx, adapter.ListOptions{})
	if err != nil {
		t.Fatalf("ListTemplates() error: %v", err)
	}
	if len(templates) < 1 {
		t.Error("expected at least 1 template")
	}

	tmpl, err := a.GetTemplate(ctx, "base")
	if err != nil {
		t.Fatalf("GetTemplate() error: %v", err)
	}
	if tmpl.TemplateID != "base" {
		t.Errorf("expected template ID 'base', got %s", tmpl.TemplateID)
	}
}

func TestRegistry(t *testing.T) {
	reg := adapter.NewRegistry()
	a := mockadapter.New()

	if err := reg.Register(a); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	// Duplicate registration
	if err := reg.Register(a); err == nil {
		t.Error("expected error for duplicate registration")
	}

	// Get
	got, ok := reg.Get("mock")
	if !ok {
		t.Error("expected to find 'mock' adapter")
	}
	if got.Name() != "mock" {
		t.Errorf("expected name 'mock', got %s", got.Name())
	}

	// List
	list := reg.List()
	if len(list) != 1 {
		t.Errorf("expected 1 adapter, got %d", len(list))
	}

	// Health check all
	results := reg.HealthCheckAll(context.Background())
	if err := results["mock"]; err != nil {
		t.Errorf("expected healthy mock, got error: %v", err)
	}
}
