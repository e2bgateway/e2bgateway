# Backend Setup Guide

E2BGateway routes sandbox API requests to one or more backend adapters. This guide covers configuring each supported adapter type, multi-backend setups, routing strategies, and health checking.

---

## Adapter Architecture Overview

```
                   ┌─────────────────────────────────────┐
                   │          E2BGateway Router          │
                   │ (strategy + health-aware selection) │
                   └──────────┬──────────────────────────┘
                              │
              ┌───────────────┼───────────────┐
              ▼               ▼               ▼
     ┌──────────────┐ ┌──────────────┐ ┌──────────────┐
     │  E2B Cloud   │ │agent-sandbox │ │ OpenSandbox  │
     │   Adapter    │ │   Adapter    │ │   Adapter    │
     └──────┬───────┘ └──────┬───────┘ └──────┬───────┘
            │                │                 │
            ▼                ▼                 ▼
     E2B Cloud API    K8s API (CRDs)    OpenSandbox API
```

Each adapter implements the `SandboxAdapter` interface, translating E2B protocol calls into the backend's native API. Adapters are registered in a central `Registry` and selected per-request by the `Router` based on the configured strategy.

All adapters share a common configuration envelope:

```yaml
backends:
  - name: <unique-name>
    type: <adapter-type>
    enabled: true
    config:
      # adapter-specific settings
```

---

## E2B Cloud Adapter

The E2B Cloud adapter acts as a passthrough to the official E2B SaaS API. It is the simplest adapter to configure and is ideal for development or hybrid setups.

### Prerequisites

- An E2B account with an API key (obtain from <https://e2b.dev/dashboard>)

### Configuration

```yaml
backends:
  - name: e2b-cloud
    type: e2b-cloud
    enabled: true
    config:
      # Required: your E2B API key
      apiKey: "sk-e2b-your-api-key-here"

      # Optional: override the E2B API base URL (e.g. for proxied setups)
      # baseUrl: "https://api.e2b.dev"

      # Optional: request timeout for backend calls
      # timeout: "30s"
```

### Environment variable

```bash
export E2BGW_BACKENDS_0_CONFIG_APIKEY=sk-e2b-your-api-key-here
```

### What it supports

| Feature | Supported |
|---|---|
| Sandbox lifecycle | Full |
| Code execution | Full |
| Filesystem ops | Full |
| Commands | Full |
| Templates | Read-only (templates managed in E2B dashboard) |
| Snapshots | Full |
| Warm pools | Full |
| Port forwarding | Full |

---

## Agent-Sandbox Adapter

The agent-sandbox adapter communicates with a Kubernetes cluster where the agent-sandbox CRD controller is installed. Sandbox operations map to CRUD operations on `Sandbox` custom resources.

### Prerequisites

- A Kubernetes cluster (1.25+)
- `kubectl` configured with cluster access
- Helm 3.10+ (for installing the CRD controller)

### Step 1: Install the agent-sandbox CRD controller

```bash
helm repo add agent-sandbox https://charts.agent-sandbox.io
helm repo update

helm install agent-sandbox agent-sandbox/controller \
  --namespace agent-sandbox \
  --create-namespace
```

Verify the CRDs are installed:

```bash
kubectl get crd | grep sandbox
# sandboxes.agent-sandbox.io
```

### Step 2: Create RBAC for E2BGateway

E2BGateway needs permissions to manage Sandbox custom resources:

```yaml
# rbac.yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: e2bgateway
  namespace: e2bgateway
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: e2bgateway-sandbox-manager
rules:
  - apiGroups: ["agent-sandbox.io"]
    resources: ["sandboxes"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: ["agent-sandbox.io"]
    resources: ["sandboxes/status"]
    verbs: ["get", "patch"]
  - apiGroups: [""]
    resources: ["pods", "pods/log", "pods/exec"]
    verbs: ["get", "list", "create"]
  - apiGroups: [""]
    resources: ["services"]
    verbs: ["get", "list", "create", "delete"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: e2bgateway-sandbox-manager
subjects:
  - kind: ServiceAccount
    name: e2bgateway
    namespace: e2bgateway
roleRef:
  kind: ClusterRole
  name: e2bgateway-sandbox-manager
  apiGroup: rbac.authorization.k8s.io
```

```bash
kubectl apply -f rbac.yaml
```

### Step 3: Configure the adapter

```yaml
backends:
  - name: agent-sandbox
    type: agent-sandbox
    enabled: true
    config:
      # Kubernetes namespace for sandbox resources
      namespace: "sandbox-runtime"

      # Optional: use in-cluster config (when running inside K8s)
      # Leave blank or set to "incluster" for in-cluster
      kubeconfig: ""

      # Optional: default sandbox class (maps to a SandboxClass CR)
      # defaultClass: "standard"

      # Optional: labels applied to all sandbox pods
      # podLabels:
      #   app.kubernetes.io/managed-by: e2bgateway

      # Optional: resource defaults
      # defaultResources:
      #   cpu: "1"
      #   memory: "512Mi"
```

### Running outside the cluster

If E2BGateway runs outside Kubernetes, provide a kubeconfig path:

```yaml
backends:
  - name: agent-sandbox
    type: agent-sandbox
    enabled: true
    config:
      namespace: "sandbox-runtime"
      kubeconfig: "/path/to/kubeconfig"
```

---

## OpenSandbox Adapter

The OpenSandbox adapter connects to a self-hosted OpenSandbox runtime that provides a native REST API.

### Prerequisites

- An OpenSandbox deployment (see [OpenSandbox documentation](https://github.com/opensandbox/opensandbox))

### Step 1: Deploy OpenSandbox

```bash
# Using Docker Compose
git clone https://github.com/opensandbox/opensandbox.git
cd opensandbox
docker compose up -d
```

Verify the runtime is reachable:

```bash
curl http://localhost:9000/healthz
```

### Step 2: Configure the adapter

```yaml
backends:
  - name: opensandbox
    type: opensandbox
    enabled: true
    config:
      # Required: OpenSandbox API endpoint
      apiEndpoint: "http://opensandbox:9000"

      # Optional: authentication token for OpenSandbox API
      # authToken: "opensandbox-token"

      # Optional: request timeout
      # timeout: "30s"

      # Optional: default sandbox image
      # defaultImage: "opensandbox/base:latest"
```

### Multi-instance deployment

For high availability, deploy multiple OpenSandbox instances and configure them as separate backends:

```yaml
backends:
  - name: opensandbox-az1
    type: opensandbox
    enabled: true
    config:
      apiEndpoint: "http://opensandbox-az1:9000"

  - name: opensandbox-az2
    type: opensandbox
    enabled: true
    config:
      apiEndpoint: "http://opensandbox-az2:9000"
```

---

## Multi-Backend Configuration

E2BGateway supports running multiple backends simultaneously. This enables scenarios like:

- **Hybrid cloud**: Route specific templates to E2B Cloud, others to self-hosted runtimes
- **Geographic routing**: Route by region to reduce latency
- **Gradual migration**: Run E2B Cloud and self-hosted in parallel during migration
- **Cost optimization**: Route burst traffic to cloud, baseline to self-hosted

### Example configuration

```yaml
backends:
  - name: e2b-cloud
    type: e2b-cloud
    enabled: true
    config:
      apiKey: "sk-e2b-..."

  - name: agent-sandbox-prod
    type: agent-sandbox
    enabled: true
    config:
      namespace: "sandbox-prod"

  - name: opensandbox-dev
    type: opensandbox
    enabled: true
    config:
      apiEndpoint: "http://opensandbox-dev:9000"

routing:
  defaultBackend: agent-sandbox-prod
  strategy: template-based
  strategies:
    - name: template-routes
      rules:
        # Route specific templates to E2B Cloud
        - template: "premium-gpu"
          backend: "e2b-cloud"
        - template: "data-science"
          backend: "e2b-cloud"
        # Route dev templates to OpenSandbox
        - template: "dev-preview"
          backend: "opensandbox-dev"
```

---

## Routing Strategies

The `routing.strategy` field determines how requests are assigned to backends.

### Static

All requests go to the `defaultBackend`. This is the simplest and most common configuration.

```yaml
routing:
  defaultBackend: e2b-cloud
  strategy: static
```

### Template-Based

Routes requests to different backends based on the template ID used in the request. Rules are evaluated in order; the first match wins.

```yaml
routing:
  defaultBackend: agent-sandbox
  strategy: template-based
  strategies:
    - name: template-routes
      rules:
        - template: "python-ml"
          backend: "e2b-cloud"
        - template: "nodejs-*"
          backend: "opensandbox-prod"
```

Glob patterns (`*`) are supported in template names.

### Weighted

Distributes traffic across backends by percentage weights. Useful for gradual migrations or cost balancing.

```yaml
routing:
  strategy: weighted
  strategies:
    - name: weighted-split
      rules:
        - backend: "e2b-cloud"
          # 70% of traffic
        - backend: "agent-sandbox"
          # 30% of traffic
```

> **Note:** Weights are specified as integer ratios (e.g. 70/30) that are normalized to 100%.

### Priority

Attempts backends in priority order. If the primary backend is unavailable (failed health check), traffic fails over to the next backend in the chain.

```yaml
routing:
  strategy: priority
  strategies:
    - name: primary-failover
      rules:
        - backend: "agent-sandbox"    # primary
        - backend: "e2b-cloud"        # fallback
```

This works in conjunction with the `failover` configuration:

```yaml
routing:
  failover:
    enabled: true
    chain:
      - agent-sandbox
      - e2b-cloud
```

---

## Health Checking

E2BGateway periodically checks backend health to support routing decisions and the `/readyz` endpoint.

### Configuration

```yaml
routing:
  healthCheck:
    # How often to probe each backend
    interval: 10s

    # Timeout for each health check request
    timeout: 5s

    # Number of consecutive failures before marking unhealthy
    unhealthyThreshold: 3

    # Number of consecutive successes before marking healthy again
    healthyThreshold: 2
```

### Health check behavior

| Adapter | Health check method |
|---|---|
| E2B Cloud | `GET /health` against the E2B API |
| agent-sandbox | `kubectl get --raw /healthz` against the API server |
| OpenSandbox | `GET /healthz` against the OpenSandbox endpoint |
| Mock | Always healthy |

### Integration with Kubernetes probes

Map gateway health endpoints to Kubernetes liveness and readiness probes:

```yaml
# In your Helm values or Deployment spec
livenessProbe:
  httpGet:
    path: /healthz
    port: 8080
  initialDelaySeconds: 5
  periodSeconds: 10

readinessProbe:
  httpGet:
    path: /readyz
    port: 8080
  initialDelaySeconds: 10
  periodSeconds: 5
```

- `/healthz` returns `200` if the gateway process is running (no backend checks).
- `/readyz` returns `200` only if at least one backend is healthy.

---

## Troubleshooting

### Backend shows as unhealthy

1. Check the gateway logs: `kubectl logs -n e2bgateway deployment/e2bgateway`
2. Verify the backend is reachable from the gateway pod
3. Increase `healthCheck.timeout` if the backend has high latency
4. Decrease `healthCheck.unhealthyThreshold` for faster detection (at the cost of flapping)

### Requests returning 502

A 502 indicates the selected backend returned an error or timed out. Check:

1. Backend adapter logs for the specific request
2. Backend service health
3. Network connectivity between gateway and backend
4. Request timeout settings (`server.http.writeTimeout`)

### No backends configured

If the gateway fails to start with `"at least one backend must be enabled"`:

1. Ensure at least one backend has `enabled: true`
2. Verify backend names are unique
3. Check that backend types are spelled correctly (`e2b-cloud`, `agent-sandbox`, `opensandbox`, `mock`)
