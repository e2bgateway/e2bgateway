# E2BGateway Examples

Multi-language examples demonstrating E2B API compatibility through E2BGateway.

All examples work with the official [E2B Python SDK](https://pypi.org/project/e2b/), [E2B JavaScript SDK](https://www.npmjs.com/package/@e2b/code-interpreter), and directly via HTTP/curl against E2BGateway.

## Prerequisites

- E2BGateway running (default: `http://localhost:8080`)
- API key configured (set `E2B_API_KEY` environment variable)

## Quick Start

```bash
# Set your gateway URL and API key
export E2B_DOMAIN=localhost:8080
export E2B_API_KEY=your-api-key

# Python
python examples/python/hello_world.py

# JavaScript
node examples/javascript/hello_world.js

# Go
go run examples/go/hello_world/main.go

# cURL
curl -X POST http://localhost:8080/sandboxes \
  -H "X-API-Key: $E2B_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"templateID":"base"}'
```

## Examples

| Example | Description | Python | JavaScript | Go | cURL |
|---------|-------------|--------|------------|-----|------|
| Hello World | Basic sandbox creation | ✅ | ✅ | ✅ | ✅ |
| Sandbox Lifecycle | Create, pause, resume, kill | ✅ | ✅ | ✅ | ✅ |
| Code Execution | Execute Python/JS code | ✅ | ✅ | ✅ | ✅ |
| Filesystem | File CRUD operations | ✅ | ✅ | ✅ | ✅ |
| Commands | Run shell commands | ✅ | ✅ | ✅ | ✅ |
| Templates | Template management | ✅ | ✅ | ✅ | ✅ |
| AI Code Interpreter | Interactive code session | ✅ | ✅ | - | - |
| Data Analysis | Data processing in sandbox | ✅ | ✅ | - | - |
| Web Scraping | Fetch and parse web content | ✅ | ✅ | - | - |
| Warm Pools | Manage pre-warmed sandbox pools (REST) | ✅ | ✅ | ✅ | ✅ |

## Running Against E2BGateway

All examples use the `E2B_DOMAIN` environment variable to point to your E2BGateway instance instead of the official E2B cloud:

```bash
export E2B_DOMAIN=your-gateway.example.com
```

Or for local development:
```bash
export E2B_DOMAIN=localhost:8080
export E2B_API_KEY=test-key
```

## Backend Configuration (agent-sandbox)

The examples work against any backend. For the agent-sandbox backend, the
gateway config looks like:

```yaml
backends:
  - name: agent-sandbox
    type: agent-sandbox
    enabled: true
    config:
      namespace: agent-sandbox-system
      # Default claim pool for templates without an explicit mapping.
      warmPoolName: base
      # Optional: E2B template ID -> SandboxTemplate resource name
      # (default: the template ID is used as the SandboxTemplate name).
      templateToSandboxTemplate:
        my-e2b-template: my-sandbox-template
      # Optional: operator-pinned template -> warm pool routing overrides.
      # These always win over pools created via the warm-pool CRUD API.
      templateToWarmPool:
        my-e2b-template: my-pool
      # OnReplenish (CRD default) or Recreate; applied to CRUD-created pools.
      warmPoolUpdateStrategy: OnReplenish
      maxWarmPoolSize: 256
```

Sandbox pods must embed the envd daemon (the gateway's only content data
plane for this backend); build the reference image with:

```bash
docker build -t sandbox-with-envd:local images/sandbox/
```
