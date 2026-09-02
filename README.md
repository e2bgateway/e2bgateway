<div align="center">

# E2BGateway

[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/github/go-mod/go-version/e2bgateway/e2bgateway)](https://github.com/e2bgateway/e2bgateway/blob/main/go.mod)
[![Release](https://img.shields.io/github/v/release/e2bgateway/e2bgateway)](https://github.com/e2bgateway/e2bgateway/releases)
[![Stars](https://img.shields.io/github/stars/e2bgateway/e2bgateway?style=social)](https://github.com/e2bgateway/e2bgateway/stargazers)

</div>

E2BGateway acts as an abstraction gateway layer for AI agent sandboxes. It provides a fully compatible interface aligned with the official E2B client protocol, transparently routing requests to diverse underlying agent runtime implementations such as [agent-sandbox](https://github.com/kubernetes-sigs/agent-sandbox) and OpenSandbox.

## Why E2BGateway?

- **Eliminate vendor lock-in** — Write once, run against any sandbox backend
- **Zero-code migration** — Existing E2B SDK users connect with only a URL change
- **Multi-backend routing** — Dynamic routing, load balancing, and failover between sandbox clusters
- **Production ready** — Auth, rate limiting, audit logging, and OpenTelemetry observability built-in

## Quick Start

```python
import os

# Point E2B SDK at your E2BGateway instance
os.environ["E2B_API_URL"] = "https://sandbox.example.com"
os.environ["E2B_API_KEY"] = "your-api-key"

from e2b_code_interpreter import Sandbox

# Standard E2B code — no modifications needed
sbx = Sandbox.create(template="code-interpreter")
result = sbx.run_code("print('Hello from E2BGateway!')")
print(result.text)
sbx.kill()
```

## Supported Backends

| Backend | Type | Status | Description |
|---|---|---|---|
| [E2B Cloud](https://e2b.dev) | SaaS passthrough | ✅ Supported | Transparent proxy to official E2B API |
| [agent-sandbox](https://github.com/kubernetes-sigs/agent-sandbox) | K8s CRD-based | ✅ Supported | Kubernetes-native sandbox via SandboxClaim CRDs with warm pool |
| [OpenSandbox](https://github.com/alibaba/OpenSandbox) | Container-based | ✅ Supported | Alibaba's open-source container sandbox runtime |
| [CubeSandbox](https://github.com/cubesandbox/cubesandbox) | Container-based | 🚧 Planned | High-performance container sandbox with plugin-based integration |
| [AgentENV](https://github.com/kvcache-ai/AgentENV) | Environment-based | 🚧 Planned | Agent environment platform with plugin-based integration |

## Architecture

```
E2B SDK Client (Python/JS)
        │
        │  HTTPS (E2B Protocol)
        ▼
┌─────────────────────────────┐
│       E2BGateway            │
│  ┌───────────────────────┐  │
│  │ Auth → RateLimit →    │  │
│  │ Router → Translator   │  │
│  └───────────┬───────────┘  │
│              │              │
│  ┌───────────┴───────────┐  │
│  │   Backend Adapters    │  │
│  │  ┌─────┐ ┌─────┐      │  │
│  │  │E2B  │ │K8s  │ ...  │  │
│  │  └──┬──┘ └──┬──┘      │  │
│  └─────┼───────┼──────-──┘  │
└────────┼───────┼─────-──────┘
         │       │
         ▼       ▼
    E2B Cloud  K8s Cluster
```

See the [full architecture design](docs/design/README.md) for details.

## API Compatibility

E2BGateway implements the complete E2B REST API:

- `POST/GET/DELETE /api/v1/sandboxes` — Sandbox lifecycle
- `POST /api/v1/sandboxes/{id}/code` — Code execution
- `POST /api/v1/sandboxes/{id}/commands` — Shell commands
- `POST/GET /api/v1/sandboxes/{id}/files/*` — Filesystem operations
- `GET/POST/DELETE /api/v1/templates` — Template management
- WebSocket channels for streaming code execution, terminals, and port forwarding

## Build & Run

```bash
# Build
make build

# Run locally
make run

# Run tests
make test

# Docker
make docker-build

# Helm
helm install e2bgateway deploy/helm/e2bgateway
```

## Configuration

See [configs/e2bgateway-default.yaml](configs/e2bgateway-default.yaml) for the default configuration and [configs/e2bgateway-example.yaml](configs/e2bgateway-example.yaml) for a full example with all backends.

## Documentation

- [Architecture Design](docs/design/README.md)
- [Getting Started Guide](docs/guides/getting-started.md)
- [Backend Setup](docs/guides/backend-setup.md)
- [Migration Guide](docs/guides/migration.md)
- [OpenAPI Specification](docs/api/openapi.yaml)

## Related Projects

- [kubernetes-sigs/agent-sandbox](https://github.com/kubernetes-sigs/agent-sandbox) — K8s-native sandbox CRDs
- [E2B](https://e2b.dev) — Cloud sandbox platform for AI agents
- [OpenSandbox](https://github.com/alibaba/OpenSandbox) — Alibaba's open-source container sandbox runtime
- [CubeSandbox](https://github.com/cubesandbox/cubesandbox) — High-performance container sandbox
- [AgentENV](https://github.com/kvcache-ai/AgentENV) — Agent environment platform
- [KEP-539.2](https://github.com/kubernetes-sigs/agent-sandbox/tree/main/docs/keps/539.2-runtime-standardization) — Runtime standardization interface

## License

Apache License 2.0. See [LICENSE](LICENSE).
