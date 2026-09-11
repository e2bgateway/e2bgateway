# E2BGateway Helm Chart

[E2BGateway](https://github.com/e2bgateway/e2bgateway) is a stateless, horizontally-scalable API gateway that provides full compatibility with the E2B Client SDK protocol. It transparently routes requests to diverse underlying sandbox runtimes.

## TL;DR

```bash
helm repo add e2bgateway https://e2bgateway.github.io/charts
helm install my-e2bgateway e2bgateway/e2bgateway
```

## Introduction

This chart bootstraps an E2BGateway deployment on a [Kubernetes](http://kubernetes.io) cluster using the [Helm](https://helm.sh) package manager.

## Prerequisites

- Kubernetes 1.23+
- Helm 3.8.0+
- PV provisioner support in the underlying infrastructure (optional)
- At least one sandbox backend configured:
  - [agent-sandbox](https://github.com/kubernetes-sigs/agent-sandbox)
  - [OpenSandbox](https://github.com/alibaba/OpenSandbox)
  - E2B Cloud (SaaS)

## Installing the Chart

To install the chart with the release name `my-e2bgateway`:

```bash
helm install my-e2bgateway ./deploy/helm/e2bgateway
```

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
| `replicaCount` | Number of replicas | `3` |
| `image.repository` | Image repository | `ghcr.io/e2bgateway/e2bgateway` |
| `image.pullPolicy` | Image pull policy | `IfNotPresent` |
| `image.tag` | Image tag (defaults to appVersion) | `""` |

### Service Parameters

| Name | Description | Value |
|------|-------------|-------|
| `service.type` | Service type | `ClusterIP` |
| `service.httpPort` | HTTP API port | `8080` |
| `service.httpsPort` | HTTPS API port | `8443` |
| `service.metricsPort` | Metrics port | `9090` |

### Ingress Parameters

| Name | Description | Value |
|------|-------------|-------|
| `ingress.enabled` | Enable ingress | `false` |
| `ingress.className` | Ingress class name | `""` |
| `ingress.annotations` | Ingress annotations | `{}` |
| `ingress.hosts` | Ingress hosts | `[{host: sandbox.local, paths: [{path: /, pathType: Prefix}]}]` |
| `ingress.tls` | Ingress TLS configuration | `[]` |

### Port Forwarding Parameters

| Name | Description | Value |
|------|-------------|-------|
| `portForwarding.enabled` | Enable port forwarding support | `true` |
| `portForwarding.commonPorts` | List of commonly used ports | `[3000, 8080, 8888]` |

**Port Forwarding**: Port forwarding allows access to web applications and services running inside sandbox containers. The gateway provides APIs to:
- List open ports: `GET /sandboxes/{id}/ports`
- Get port URL: `GET /sandboxes/{id}/ports/{port}`

Both `agent-sandbox` and `opensandbox` backends support port forwarding.

### Backend Configuration

Configure backends in the `config.backends` section:

```yaml
config:
  backends:
    - name: agent-sandbox
      type: agent-sandbox
      enabled: true
      config:
        namespace: sandbox-system
        defaultTemplate: code-interpreter
    - name: opensandbox
      type: opensandbox
      enabled: false
      config:
        endpoint: "http://opensandbox-controller.sandbox-system:8080"
```

### Authentication Parameters

| Name | Description | Value |
|------|-------------|-------|
| `config.auth.methods` | Authentication methods | `[{type: apikey, keys: [test-key]}]` |
| `existingSecret` | Existing secret with API keys | `""` |

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
| `autoscaling.enabled` | Enable autoscaling | `false` |
| `autoscaling.minReplicas` | Minimum replicas | `3` |
| `autoscaling.maxReplicas` | Maximum replicas | `20` |
| `autoscaling.targetCPUUtilizationPercentage` | Target CPU utilization | `70` |

## Configuration

See [values.yaml](./values.yaml) for the full list of configurable parameters.

### Example: Production Deployment

```yaml
replicaCount: 5

image:
  repository: ghcr.io/e2bgateway/e2bgateway
  tag: "v1.0.0"

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
  tls:
    - secretName: sandbox-tls
      hosts:
        - sandbox.example.com

portForwarding:
  enabled: true
  commonPorts:
    - 3000  # Web applications
    - 8080  # HTTP servers
    - 8888  # Jupyter notebooks
    - 5000  # Flask apps

config:
  backends:
    - name: agent-sandbox
      type: agent-sandbox
      enabled: true
      config:
        namespace: sandbox-system
        defaultTemplate: code-interpreter
    - name: opensandbox
      type: opensandbox
      enabled: true
      config:
        endpoint: "http://opensandbox-controller.sandbox-system:8080"
        useSignedEndpoint: true  # Use OSEP-0011 signed endpoints

  auth:
    methods:
      - type: apikey
        keys:
          - "production-api-key-1"
          - "production-api-key-2"

  rateLimit:
    enabled: true
    defaultRPM: 60

  routing:
    defaultBackend: agent-sandbox
    strategy: round-robin

resources:
  requests:
    cpu: 500m
    memory: 512Mi
  limits:
    cpu: 2000m
    memory: 2Gi

autoscaling:
  enabled: true
  minReplicas: 5
  maxReplicas: 50
  targetCPUUtilizationPercentage: 70
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
