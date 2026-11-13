<div align="center">

<img alt="E2BGateway" src="https://avatars.githubusercontent.com/u/309727905?s=400&v=4" width="200">

# E2BGateway

<div>
  <a href="LICENSE">
    <img src="https://img.shields.io/badge/License-Apache2.0-brightgreen.svg?style=flat" alt="License: Apache 2.0">
  </a>
  <a href="https://github.com/e2bgateway/e2bgateway">
    <img src="https://img.shields.io/github/stars/e2bgateway/e2bgateway.svg?style=flat&logo=github&label=Stars" alt="Stars">
  </a>
  <a href="https://discord.gg/9PnGKvwSb">
    <img src="https://img.shields.io/badge/Join%20Discord-6D28D9?style=flat&logo=discord&logoColor=white" alt="Discord">
  </a>
  <a href="https://github.com/e2bgateway/e2bgateway/releases">
    <img src="https://img.shields.io/github/v/release/e2bgateway/e2bgateway?style=flat&label=Latest%20Release&color=6D28D9" alt="Latest Release">
  </a>
  <a href="https://github.com/e2bgateway/e2bgateway/blob/main/go.mod">
    <img src="https://img.shields.io/github/go-mod/go-version/e2bgateway/e2bgateway?style=flat&logo=go&label=Go&color=00ADD8" alt="Go Version">
  </a>
  <a href="https://deepwiki.com/e2bgateway/e2bgateway"><img src="https://deepwiki.com/badge.svg" alt="Ask DeepWiki"></a>
  <a href="https://codespaces.new/e2bgateway/e2bgateway">
    <img src="https://github.com/codespaces/badge.svg" alt="Open in Github Codespaces" style="max-width: 100%;" height="20">
  </a>
</div>

<div>
  The <strong>E2B-compatible</strong> gateway for AI agent sandboxes — one API, any backend.
</div>

</div>
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
| [Agent Substrate](https://github.com/agent-substrate/substrate) | K8s gVisor/microVM | 🚧 Planned | Zero-trust sandbox runtime with actor/worker multiplexing and sub-500ms resume |

## Architecture

<img width="1678" height="937" alt="image" src="https://github.com/user-attachments/assets/b8602923-ed7b-4a7d-b028-07bd61c5cff1" />



See the [full architecture design](docs/design/README.md) for details.

## API Compatibility

E2BGateway implements the complete E2B REST API:

- `POST/GET/DELETE /api/v1/sandboxes` — Sandbox lifecycle
- `POST /api/v1/sandboxes/{id}/code` — Code execution
- `POST /api/v1/sandboxes/{id}/commands` — Shell commands
- `POST/GET /api/v1/sandboxes/{id}/files/*` — Filesystem operations
- `GET /api/v1/sandboxes/{id}/ports` — List open ports (port forwarding)
- `GET /api/v1/sandboxes/{id}/ports/{port}` — Get port URL (port forwarding)
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

**HTTPS**: The E2B Python and JS SDKs require HTTPS connections. Configure the optional HTTPS listener in your config:

```yaml
server:
  http:
    address: "0.0.0.0:8080"
  https:
    address: "0.0.0.0:8443"
    certFile: "/etc/e2bgateway-tls/tls.crt"
    keyFile: "/etc/e2bgateway-tls/tls.key"
```

## Documentation

- [Architecture Design](docs/design/README.md)
- [Getting Started Guide](docs/guides/getting-started.md)
- [Backend Setup](docs/guides/backend-setup.md)
- [Port Forwarding Guide](docs/guides/port-forwarding.md)
- [Migration Guide](docs/guides/migration.md)
- [OpenAPI Specification](docs/api/openapi.yaml)

## Related Projects

- [kubernetes-sigs/agent-sandbox](https://github.com/kubernetes-sigs/agent-sandbox) — K8s-native sandbox CRDs
- [E2B](https://e2b.dev) — Cloud sandbox platform for AI agents
- [OpenSandbox](https://github.com/alibaba/OpenSandbox) — Alibaba's open-source container sandbox runtime
- [CubeSandbox](https://github.com/cubesandbox/cubesandbox) — High-performance container sandbox
- [AgentENV](https://github.com/kvcache-ai/AgentENV) — Agent environment platform
- [Agent Substrate](https://github.com/agent-substrate/substrate) — Zero-trust Kubernetes sandbox runtime for massive agent fleets
- [KEP-539.2](https://github.com/kubernetes-sigs/agent-sandbox/tree/main/docs/keps/539.2-runtime-standardization) — Runtime standardization interface

## Contributing

For instructions on how to contribute to the E2BGateway project, see the [CONTRIBUTION.md](https://github.com/e2bgateway/community/blob/main/CONTRIBUTION.md) file in the [community repo](https://github.com/e2bgateway/community).

## Community

Come talk to us on the [E2BGateway Discord](https://discord.gg/9PnGKvwSb)!

To report security issues, please use [GitHub Security Advisories](https://github.com/e2bgateway/e2bgateway/security/advisories/new) on this repository.

## Community Meetings

To join a community meeting, add the [E2BGateway calendar](https://calendar.google.com/calendar/u/0/embed?src=ae6ae6d93cce74ae42d1be68bcab8154ffbc582cf1bdcef49069afcb090478ca@group.calendar.google.com&ctz=Asia/Hong_Kong) to your Google account. Then, you can find event details on the calendar.

Community meetings are held on [Google Meet](https://meet.google.com/ccm-ssac-ncn). Everyone is welcome! Meeting notes and agendas are published in the [community meeting doc](https://docs.google.com/document/d/1HibRm0qScSt96UEYX4UNUWzFpHpfNon41AwwhpjIA9M/edit?usp=sharing).

## Roadmap

E2BGateway is currently in active development. If you'd like a feature that's missing, open an issue in our [GitHub repo](https://github.com/e2bgateway/e2bgateway/issues).

## Contributors

Thanks to all contributors who are helping to make E2BGateway better.

<a href="https://github.com/e2bgateway/e2bgateway/graphs/contributors">
  <img src="https://contrib.rocks/image?repo=e2bgateway/e2bgateway" alt="View project contributors" />
</a>

## License

Apache License 2.0. See [LICENSE](LICENSE).
