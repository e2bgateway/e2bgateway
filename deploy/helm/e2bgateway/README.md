# E2BGateway Helm Chart

[E2BGateway](https://github.com/e2bgateway/e2bgateway) is an API gateway that provides full compatibility with the E2B Client SDK protocol. It transparently routes requests to diverse underlying sandbox runtimes.

> **Scaling note:** running more than one replica requires the shared session store: set `config.sessionStore.type: redis` (with `addr`) so envd access tokens and the sandbox→backend routing map are shared across replicas and survive restarts. Migrate in two steps — enable redis while keeping `replicaCount: 1` and the `Recreate` strategy first, then scale out (see `values.yaml`). With the default `sessionStore.type: memory`, keep a single replica: cross-replica data-plane requests would fail token validation (401).

## TL;DR

```bash
# OCI artifact from GHCR (published with every release tag)
helm install my-e2bgateway oci://ghcr.io/e2bgateway/charts/e2bgateway --version 0.1.0-rc.1
```

> **One-time maintainer step (first chart publication):** GHCR packages are
> **private by default** on first push, and `GITHUB_TOKEN` cannot change
> package visibility. Until an org admin sets `charts/e2bgateway` to public
> (package settings → Change visibility), anonymous installs fail — use
> `helm registry login ghcr.io` with a PAT that has `read:packages`, or
> download the `.tgz` from the GitHub Release instead.

## Introduction

This chart bootstraps an E2BGateway deployment on a [Kubernetes](http://kubernetes.io) cluster using the [Helm](https://helm.sh) package manager.

## Prerequisites

- Kubernetes 1.23+
- Helm 3.8.0+
- At least one sandbox backend configured:
  - [agent-sandbox](https://github.com/kubernetes-sigs/agent-sandbox) **v1.0.11** — the chart does not install it; deploy the upstream CRDs (`sandboxes.agents.x-k8s.io`, `sandboxclaims`/`sandboxtemplates`/`sandboxwarmpools.extensions.agents.x-k8s.io`), the controller (`agent-sandbox-controller:v1.0.11`) and the sandbox-router (`sandbox-router-go:v1.0.11`) first. The router is required by the cold-start `CreateSandbox` path (requests carrying `envs`); point `config.backends[].config.gatewayName` / `gatewayNamespace` / `apiURL` at it if it is not reachable under the default name.
  - [OpenSandbox](https://github.com/alibaba/OpenSandbox)
  - E2B Cloud (SaaS)

## Installing the Chart

To install the chart with the release name `my-e2bgateway`:

```bash
# From GHCR (OCI) — recommended; replace the version with the desired release tag
helm install my-e2bgateway oci://ghcr.io/e2bgateway/charts/e2bgateway --version 0.1.0-rc.1

# Or from a local checkout
helm install my-e2bgateway ./deploy/helm/e2bgateway
```

Packaged charts (`.tgz`) are also attached to each [GitHub Release](https://github.com/e2bgateway/e2bgateway/releases).

The command deploys E2BGateway on the Kubernetes cluster in the default configuration. The [Parameters](#parameters) section lists the parameters that can be configured during installation.

## Uninstalling the Chart

To uninstall/delete the `my-e2bgateway` deployment:

```bash
helm delete my-e2bgateway
```

The command removes all the Kubernetes components associated with the chart and deletes the release.

## Parameters

### Global Parameters

| Name | Description | Value |
|------|-------------|-------|
| `replicaCount` | Number of replicas (keep at 1, see Scaling note) | `1` |
| `image.repository` | Image repository | `ghcr.io/e2bgateway/e2bgateway` |
| `image.pullPolicy` | Image pull policy | `IfNotPresent` |
| `image.tag` | Image tag (defaults to appVersion) | `""` |
| `rbac.create` | Create ClusterRole/ClusterRoleBinding for the agent-sandbox backend (names suffixed with the release namespace) | `true` |
| `strategy` | Deployment update strategy — `Recreate` avoids old/new pod overlap with disjoint in-memory token caches (see Scaling note) | `{type: Recreate}` |

### Service Parameters

| Name | Description | Value |
|------|-------------|-------|
| `service.type` | Service type | `ClusterIP` |
| `service.httpPort` | HTTP API port | `8080` |
| `service.httpsPort` | HTTPS API port (only served when `config.server.https` is configured) | `8443` |

### Ingress Parameters

| Name | Description | Value |
|------|-------------|-------|
| `ingress.enabled` | Enable ingress | `false` |
| `ingress.className` | Ingress class name | `""` |
| `ingress.annotations` | Ingress annotations | `{}` |
| `ingress.hosts` | Ingress hosts | `[{host: sandbox.local, paths: [{path: /, pathType: Prefix}]}]` |
| `ingress.tls` | Ingress TLS configuration | `[]` |

### Port Forwarding

Port forwarding requires no configuration — it is automatically available on backends that support it. The gateway provides APIs to:
- List open ports: `GET /sandboxes/{id}/ports`
- Get port URL: `GET /sandboxes/{id}/ports/{port}`

Both `agent-sandbox` and `opensandbox` backends support port forwarding.

### Backend Configuration

Configure backends in the `config.backends` section (rendered verbatim into the gateway ConfigMap). Key reference: [`configs/e2bgateway-example.yaml`](../../../configs/e2bgateway-example.yaml) and the adapter factories.

```yaml
config:
  server:
    # Base domain for envd host-based routing ({sandboxID}.<envdDomain>).
    envdDomain: "sandbox.example.com"
  backends:
    - name: agent-sandbox
      type: agent-sandbox
      enabled: true
      config:
        namespace: sandbox-system
        # Warm pool CRUD (SandboxWarmPool CRDs):
        # warmPoolUpdateStrategy: OnReplenish   # OnReplenish | Recreate
        # maxWarmPoolSize: 256
        # templateToWarmPool:
        #   code-interpreter: my-warm-pool
        # templateToSandboxTemplate:
        #   code-interpreter: my-sandbox-template
    - name: opensandbox
      type: opensandbox
      enabled: false
      config:
        baseURL: "http://opensandbox-controller.sandbox-system:8080"  # required
        templateStore:
          type: memory              # memory | redis
        warmPool:
          maxWarmPoolSize: 256
          stateStore:
            type: memory            # memory | redis
```

> **Note:** the opensandbox backend reads `baseURL` — the legacy `endpoint` key is not recognized and the adapter fails to start without `baseURL`. agent-sandbox runs in-cluster only (pod ServiceAccount); keys like `kubeconfig`, `sandboxClass`, `defaultTemplate` and `timeout` are not read.

### Authentication Parameters

| Name | Description | Value |
|------|-------------|-------|
| `config.auth.providers` | Authentication providers (`apikey` and/or `jwt` with static JWKS) | `[{type: apikey, keys: [test-key]}]` |
| `existingSecret` | Existing Secret with API keys; mounted read-only at `/etc/e2bgateway/auth`. Must contain a `keys.json` data key (JSON object mapping API key → `{tenantID, apiKeyID, scopes, metadata}`). Use with `auth.providers[].secretRef: /etc/e2bgateway/auth/keys.json` | `""` |

### Resource Parameters

| Name | Description | Value |
|------|-------------|-------|
| `resources.requests.cpu` | CPU request | `100m` |
| `resources.requests.memory` | Memory request | `128Mi` |
| `resources.limits.cpu` | CPU limit | `500m` |
| `resources.limits.memory` | Memory limit | `512Mi` |

### Autoscaling Parameters

| Name | Description | Value |
|------|-------------|-------|
| `autoscaling.enabled` | Enable autoscaling (keep disabled, see Scaling note) | `false` |
| `autoscaling.minReplicas` | Minimum replicas | `1` |
| `autoscaling.maxReplicas` | Maximum replicas | `1` |
| `autoscaling.targetCPUUtilizationPercentage` | Target CPU utilization | `70` |

### Observability Parameters

| Name | Description | Value |
|------|-------------|-------|
| `config.observability.metrics.enabled` | Serve Prometheus metrics on the main HTTP port (see [Monitoring](#monitoring)) | `true` |
| `config.observability.metrics.path` | Metrics endpoint path (auth/rate-limit/audit exemptions match the literal `/metrics`; keep the default if you change this) | `/metrics` |
| `config.observability.metrics.prefix` | Prometheus namespace for all metric names, e.g. `e2bgw_http_requests_total` | `e2bgw` |

## Configuration

See [values.yaml](./values.yaml) for the full list of configurable parameters.

### Example: Production Deployment

```yaml
# Single replica — see the Scaling note at the top of this README.
replicaCount: 1

image:
  repository: ghcr.io/e2bgateway/e2bgateway
  tag: "0.1.0"   # pin to a released tag

service:
  type: LoadBalancer
  httpPort: 8080

ingress:
  enabled: true
  className: nginx
  annotations:
    cert-manager.io/cluster-issuer: letsencrypt-prod
  hosts:
    - host: sandbox.example.com
      paths:
        - path: /
          pathType: Prefix
    # SDK envd traffic uses {sandboxID}.sandbox.example.com hosts — an exact
    # ingress host does NOT match subdomains, so a wildcard rule is required.
    - host: "*.sandbox.example.com"
      paths:
        - path: /
          pathType: Prefix
  tls:
    - secretName: sandbox-tls
      hosts:
        - sandbox.example.com
        - "*.sandbox.example.com"   # certificate must cover the wildcard

config:
  server:
    # Wildcard DNS must point *.sandbox.example.com at the ingress/LB for
    # SDK host-based envd routing ({sandboxID}.sandbox.example.com).
    envdDomain: "sandbox.example.com"
    # https:
    #   address: "0.0.0.0:8443"
    #   certFile: "/etc/e2bgateway/certs/tls.crt"
    #   keyFile: "/etc/e2bgateway/certs/tls.key"

  backends:
    - name: agent-sandbox
      type: agent-sandbox
      enabled: true
      config:
        namespace: sandbox-system
        warmPoolUpdateStrategy: OnReplenish
        maxWarmPoolSize: 256
    - name: opensandbox
      type: opensandbox
      enabled: true
      config:
        baseURL: "http://opensandbox-controller.sandbox-system:8080"
        useSignedEndpoint: true  # Use OSEP-0011 signed endpoints
        templateStore:
          type: redis            # shared across restarts
          addr: "redis:6379"
        warmPool:
          maxWarmPoolSize: 256
          stateStore:
            type: redis
            addr: "redis:6379"

  auth:
    providers:
      # Either inline `keys` (dev) or `secretRef` (a JSON file path — mount a
      # Secret via `existingSecret`). Setting both is a startup error.
      - type: apikey
        keys:
          - "production-api-key-1"
          - "production-api-key-2"
        headerName: "X-API-Key"
      # With existingSecret: e2bgw-api-keys (Secret data key `keys.json`):
      # - type: apikey
      #   secretRef: /etc/e2bgateway/auth/keys.json
      #   headerName: "X-API-Key"

  rateLimit:
    enabled: true
    backend: redis
    defaultLimit:
      requestsPerMinute: 200
      burstSize: 50
    redis:
      address: "redis:6379"

  routing:
    defaultBackend: agent-sandbox
    strategy: static               # static | template-based | weighted | priority

resources:
  requests:
    cpu: 500m
    memory: 512Mi
  limits:
    cpu: 2000m
    memory: 2Gi
```

## Port Forwarding Usage

### Python SDK

```python
from e2b import Sandbox

# Create a sandbox
sandbox = Sandbox(template="code-interpreter")

# Start a web server
sandbox.commands.run("python -m http.server 3000 &")

# List open ports
ports = sandbox.ports.list()
print(f"Open ports: {[p.port for p in ports]}")

# Get URL for port 3000
url = sandbox.ports.get_url(3000)
print(f"Access your app at: {url}")
```

### JavaScript SDK

```javascript
import { Sandbox } from 'e2b';

const sandbox = await Sandbox.create({ template: 'code-interpreter' });
await sandbox.commands.run('python -m http.server 3000 &');

const ports = await sandbox.ports.list();
const url = await sandbox.ports.getUrl(3000);
console.log(`Access your app at: ${url}`);
```

For detailed port forwarding documentation, see [Port Forwarding Guide](../../../docs/guides/port-forwarding.md).

## Monitoring

The gateway serves Prometheus metrics on the **main HTTP port** (no separate
metrics port) at `config.observability.metrics.path` (default `/metrics`),
without authentication. Metric names are prefixed with
`config.observability.metrics.prefix` (default `e2bgw`).

| Metric (after prefix) | Type | Labels | Description |
|------|------|--------|-------------|
| `http_requests_total` | Counter | `route`, `method`, `status` | HTTP requests served by the gateway API |
| `http_request_duration_seconds` | Histogram | `route`, `method` | HTTP request latency |
| `sandbox_lifecycle_total` | Counter | `backend`, `operation`, `result` | Sandbox lifecycle operations (create/delete/pause/resume) |
| `sandbox_active` | Gauge | `backend` | Currently tracked sandboxes per backend (computed at scrape time) |
| `backend_health` | Gauge | `backend` | Backend health status from the routing layer (computed at scrape time) |
| `envd_proxy_requests_total` | Counter | `backend`, `status_class` | envd data-plane proxy requests (1xx–5xx) |
| `envd_proxy_duration_seconds` | Histogram | `backend` | envd data-plane proxy latency |
| `access_token_validation_failures_total` | Counter | `backend`, `reason` | envd access-token validation failures (missing/invalid) |
| `warm_pool_acquire_total` | Counter | `backend`, `result` | Warm pool acquire outcomes (hit/miss/fallback/bypass) |
| `tls_certificate_reloads_total` | Counter | `result` | HTTPS certificate hot reloads (success/failure); the startup load is not counted |
| `tls_certificate_expiry_timestamp_seconds` | Gauge | — | Expiry time (NotAfter) of the served HTTPS certificate as a Unix timestamp; absent when HTTPS is not configured |

### Scraping via pod annotations

If your Prometheus discovers pods by annotation, set:

```yaml
podAnnotations:
  prometheus.io/scrape: "true"
  prometheus.io/port: "8080"
  prometheus.io/path: "/metrics"
```

### ServiceMonitor (kube-prometheus-stack)

```yaml
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: e2bgateway
  labels:
    app.kubernetes.io/name: e2bgateway   # match your release's service labels
spec:
  selector:
    matchLabels:
      app.kubernetes.io/name: e2bgateway
  endpoints:
    - port: http            # main HTTP service port (service.httpPort)
      path: /metrics        # must match config.observability.metrics.path
      interval: 30s
```

### Alerting on HTTPS certificates

When `config.server.https` is configured, the gateway keeps serving the last
successfully loaded certificate — even after it expires — until a valid
replacement appears on disk. Alert on the exported expiry timestamp and on
persistent reload failures rather than relying on the warning logs:

```yaml
apiVersion: monitoring.coreos.com/v1
kind: PrometheusRule
metadata:
  name: e2bgateway-tls
spec:
  groups:
    - name: e2bgateway-tls
      rules:
        - alert: E2BGatewayCertificateExpiringSoon
          expr: e2bgw_tls_certificate_expiry_timestamp_seconds - time() < 7 * 24 * 3600
          for: 1h
          labels:
            severity: warning
          annotations:
            summary: E2BGateway HTTPS certificate expires in less than 7 days
        - alert: E2BGatewayCertificateReloadFailing
          # Every reload tick counts a failure while the files on disk stay
          # unloadable, so keep the range longer than https.reloadInterval.
          expr: rate(e2bgw_tls_certificate_reloads_total{result="failure"}[5m]) > 0
          for: 10m
          labels:
            severity: warning
          annotations:
            summary: E2BGateway cannot load its rotated HTTPS certificate
```

## Troubleshooting

### Port Forwarding Not Working

1. **Verify backend support**: Ensure your backend (agent-sandbox or opensandbox) is properly configured
2. **Check network connectivity**: Ensure the gateway can reach Pod IPs (for agent-sandbox) or OpenSandbox endpoints
3. **Review logs**: Check gateway logs for errors related to port forwarding
4. **Verify service**: Ensure the service inside the sandbox is actually running and listening on the expected port

### Gateway Not Accessible

1. **Check service type**: If using `ClusterIP`, use port-forward or enable ingress
2. **Verify ingress**: If ingress is enabled, check ingress controller logs
3. **Review firewall**: Ensure firewall rules allow traffic to the gateway

## Upgrading

```bash
helm upgrade my-e2bgateway ./deploy/helm/e2bgateway
```

## Uninstalling

```bash
helm delete my-e2bgateway
```

## Contributing

Contributions are welcome! Please see [CONTRIBUTING.md](../../../CONTRIBUTING.md) for details.

## License

This chart is licensed under the Apache 2.0 License. See [LICENSE](../../../LICENSE) for details.

## Related Documentation

- [Architecture Design](../../../docs/design/README.md)
- [Getting Started Guide](../../../docs/guides/getting-started.md)
- [Backend Setup](../../../docs/guides/backend-setup.md)
- [Port Forwarding Guide](../../../docs/guides/port-forwarding.md)
- [OpenAPI Specification](../../../docs/api/openapi.yaml)
