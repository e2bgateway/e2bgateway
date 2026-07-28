package mock

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Fake CRD types
// ---------------------------------------------------------------------------

// FakeSandboxCRD represents a simulated sandbox custom resource.
type FakeSandboxCRD struct {
	Name      string
	Namespace string
	Status    string // "Running", "Paused", "Stopped"
	Template  string
	Labels    map[string]string
	CreatedAt time.Time
}

// FakeTemplateCRD represents a simulated template custom resource.
type FakeTemplateCRD struct {
	Name      string
	Namespace string
	BuildID   string
	Tags      []string
}

// ---------------------------------------------------------------------------
// MockK8sClient
// ---------------------------------------------------------------------------

// MockK8sClient simulates Kubernetes API interactions for sandbox and template
// custom resources.  It stores objects in in-memory maps keyed by
// "namespace/name" and supports error injection for negative-path testing.
type MockK8sClient struct {
	mu        sync.Mutex
	sandboxes map[string]*FakeSandboxCRD // key: namespace/name
	templates map[string]*FakeTemplateCRD

	// Error injection – when non-nil the corresponding operation fails
	// immediately with this error.
	CreateErr error
	GetErr    error
	ListErr   error
	DeleteErr error
	UpdateErr error
}

// NewMockK8sClient returns a MockK8sClient with empty object stores.
func NewMockK8sClient() *MockK8sClient {
	return &MockK8sClient{
		sandboxes: make(map[string]*FakeSandboxCRD),
		templates: make(map[string]*FakeTemplateCRD),
	}
}

// crdKey builds the map key for a namespaced object.
func crdKey(namespace, name string) string {
	return namespace + "/" + name
}

// ---------------------------------------------------------------------------
// Sandbox CRD operations
// ---------------------------------------------------------------------------

// CreateSandboxCRD stores a new sandbox CRD.
func (m *MockK8sClient) CreateSandboxCRD(_ context.Context, sb *FakeSandboxCRD) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.CreateErr != nil {
		return m.CreateErr
	}
	key := crdKey(sb.Namespace, sb.Name)
	if _, exists := m.sandboxes[key]; exists {
		return fmt.Errorf("sandbox CRD %q already exists", key)
	}
	m.sandboxes[key] = sb
	return nil
}

// GetSandboxCRD retrieves a sandbox CRD by namespace and name.
func (m *MockK8sClient) GetSandboxCRD(_ context.Context, namespace, name string) (*FakeSandboxCRD, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.GetErr != nil {
		return nil, m.GetErr
	}
	sb, ok := m.sandboxes[crdKey(namespace, name)]
	if !ok {
		return nil, fmt.Errorf("sandbox CRD %s/%s not found", namespace, name)
	}
	return sb, nil
}

// ListSandboxCRDs returns all sandbox CRDs in the given namespace.
func (m *MockK8sClient) ListSandboxCRDs(_ context.Context, namespace string) ([]*FakeSandboxCRD, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ListErr != nil {
		return nil, m.ListErr
	}
	var out []*FakeSandboxCRD
	for _, sb := range m.sandboxes {
		if namespace == "" || sb.Namespace == namespace {
			out = append(out, sb)
		}
	}
	return out, nil
}

// DeleteSandboxCRD removes a sandbox CRD.
func (m *MockK8sClient) DeleteSandboxCRD(_ context.Context, namespace, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.DeleteErr != nil {
		return m.DeleteErr
	}
	key := crdKey(namespace, name)
	if _, ok := m.sandboxes[key]; !ok {
		return fmt.Errorf("sandbox CRD %s/%s not found", namespace, name)
	}
	delete(m.sandboxes, key)
	return nil
}

// UpdateSandboxStatus changes the status field of an existing sandbox CRD.
func (m *MockK8sClient) UpdateSandboxStatus(_ context.Context, namespace, name, status string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.UpdateErr != nil {
		return m.UpdateErr
	}
	sb, ok := m.sandboxes[crdKey(namespace, name)]
	if !ok {
		return fmt.Errorf("sandbox CRD %s/%s not found", namespace, name)
	}
	sb.Status = status
	return nil
}

// ---------------------------------------------------------------------------
// Template CRD operations
// ---------------------------------------------------------------------------

// CreateTemplateCRD stores a new template CRD.
func (m *MockK8sClient) CreateTemplateCRD(_ context.Context, tmpl *FakeTemplateCRD) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.CreateErr != nil {
		return m.CreateErr
	}
	key := crdKey(tmpl.Namespace, tmpl.Name)
	if _, exists := m.templates[key]; exists {
		return fmt.Errorf("template CRD %q already exists", key)
	}
	m.templates[key] = tmpl
	return nil
}

// GetTemplateCRD retrieves a template CRD by namespace and name.
func (m *MockK8sClient) GetTemplateCRD(_ context.Context, namespace, name string) (*FakeTemplateCRD, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.GetErr != nil {
		return nil, m.GetErr
	}
	tmpl, ok := m.templates[crdKey(namespace, name)]
	if !ok {
		return nil, fmt.Errorf("template CRD %s/%s not found", namespace, name)
	}
	return tmpl, nil
}

// ListTemplateCRDs returns all template CRDs in the given namespace.
func (m *MockK8sClient) ListTemplateCRDs(_ context.Context, namespace string) ([]*FakeTemplateCRD, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ListErr != nil {
		return nil, m.ListErr
	}
	var out []*FakeTemplateCRD
	for _, tmpl := range m.templates {
		if namespace == "" || tmpl.Namespace == namespace {
			out = append(out, tmpl)
		}
	}
	return out, nil
}

// DeleteTemplateCRD removes a template CRD.
func (m *MockK8sClient) DeleteTemplateCRD(_ context.Context, namespace, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.DeleteErr != nil {
		return m.DeleteErr
	}
	key := crdKey(namespace, name)
	if _, ok := m.templates[key]; !ok {
		return fmt.Errorf("template CRD %s/%s not found", namespace, name)
	}
	delete(m.templates, key)
	return nil
}
