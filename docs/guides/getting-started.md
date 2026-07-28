# Getting Started with E2BGateway

This guide walks you through deploying E2BGateway and creating your first sandbox.

## Prerequisites

| Requirement | Minimum Version | Notes |
|---|---|---|
| **Go** | 1.21+ | Only required when building from source |
| **Docker** | 20.10+ | Used for the quick-start container |
| **kubectl** | 1.25+ | Required for Kubernetes deployment |
| **Helm** | 3.10+ | Required for Helm-based installation |
| **An E2B SDK** | Python ≥ 0.14 / JS ≥ 0.1 | Or any HTTP client for raw API usage |

You also need at least one sandbox backend. Supported backends:

- **E2B Cloud** — managed SaaS (requires an E2B API key)
- **agent-sandbox** — self-hosted Kubernetes CRD-based runtime
- **OpenSandbox** — self-hosted container-based runtime
- **Mock** — in-memory adapter for testing

---

## Quick Start with Docker

The fastest way to run E2BGateway locally.

### 1. Create a configuration file

```yaml
# config.yaml
server:
  http:
    address: "0.0.0.0:8080"

backends:
  - name: mock
    type: mock
    enabled: true

auth:
  providers:
    - type: apikey
      headerName: X-API-Key
      secretRef: "dev-api-key-change-me"

rateLimit:
  enabled: false

routing:
  defaultBackend: mock
  strategy: static
```

### 2. Run the gateway

```bash
docker run -d \
  --name e2bgateway \
  -p 8080:8080 \
  -v "$(pwd)/config.yaml:/etc/e2bgateway/config.yaml:ro" \
  ghcr.io/e2bgateway/e2bgateway:latest
```

### 3. Verify

```bash
curl http://localhost:8080/healthz
# {"status":"ok","timestamp":"..."}
```

---

## Quick Start with Kubernetes (Helm)

### 1. Add the Helm repository

```bash
helm repo add e2bgateway https://charts.e2bgateway.io
helm repo update
```

### 2. Create a values file

```yaml
# values.yaml
replicaCount: 2

config:
  backends:
    - name: e2b-cloud
      type: e2b-cloud
      enabled: true
      config:
        apiKey: "sk-e2b-..."

  auth:
    providers:
      - type: apikey
        headerName: X-API-Key
        secretRef: "your-api-key"

  routing:
    defaultBackend: e2b-cloud
    strategy: static

image:
  repository: ghcr.io/e2bgateway/e2bgateway
  tag: latest

service:
  type: ClusterIP
  port: 8080

ingress:
  enabled: true
  className: nginx
  hosts:
    - host: gateway.example.com
      paths:
        - path: /
          pathType: Prefix
  tls:
    - secretName: gateway-tls
      hosts:
        - gateway.example.com
```

### 3. Install

```bash
helm install e2bgateway e2bgateway/e2bgateway \
  -f values.yaml \
  --namespace e2bgateway \
  --create-namespace
```

### 4. Verify

```bash
kubectl -n e2bgateway rollout status deployment/e2bgateway
kubectl -n e2bgateway port-forward svc/e2bgateway 8080:8080 &
curl http://localhost:8080/readyz
```

---

## Configuration Basics

E2BGateway uses a single YAML configuration file. Environment variables prefixed with `E2BGW_` override config values.

Key configuration sections:

| Section | Purpose |
|---|---|
| `server` | Listen address, timeouts, TLS |
| `backends` | One or more sandbox backend adapters |
| `auth` | Authentication providers (API key, JWT, mTLS) |
| `routing` | Strategy for directing requests to backends |
| `rateLimit` | Per-tenant and global rate limits |
| `cache` | Response caching (memory or Redis) |
| `observability` | Metrics, logging, OpenTelemetry tracing |

Example with environment variable overrides:

```bash
export E2BGW_SERVER_HTTP_ADDRESS=0.0.0.0:9090
export E2BGW_ROUTING_STRATEGY=template-based
e2bgateway --config /etc/e2bgateway/config.yaml
```

See [Backend Setup](./backend-setup.md) for detailed adapter configuration.

---

## First Sandbox Creation

### Using curl

```bash
# Create a sandbox
curl -X POST http://localhost:8080/sandboxes \
  -H "Content-Type: application/json" \
  -H "X-API-Key: dev-api-key-change-me" \
  -d '{
    "templateID": "base",
    "timeout": 300,
    "memoryMB": 512,
    "cpuCount": 2,
    "envVars": {
      "HELLO": "world"
    }
  }'
```

Response:

```json
{
  "sandboxID": "abc123xyz",
  "templateID": "base",
  "envdVersion": "0.1.0",
  "envdAccessToken": "eyJhbG..."
}
```

```bash
# Run a command
curl -X POST http://localhost:8080/sandboxes/abc123xyz/commands \
  -H "Content-Type: application/json" \
  -H "X-API-Key: dev-api-key-change-me" \
  -d '{"command": "echo Hello from sandbox"}'
```

```json
{
  "stdout": "Hello from sandbox\n",
  "stderr": "",
  "exitCode": 0
}
```

```bash
# List running sandboxes
curl http://localhost:8080/sandboxes \
  -H "X-API-Key: dev-api-key-change-me"

# Kill the sandbox
curl -X DELETE http://localhost:8080/sandboxes/abc123xyz \
  -H "X-API-Key: dev-api-key-change-me"
```

---

## Using the E2B Python SDK

The E2B Python SDK works with E2BGateway by setting the `domain` parameter to your gateway URL.

### Install

```bash
pip install e2b
```

### Connect to the gateway

```python
from e2b import Sandbox

# Point the SDK at your gateway instead of api.e2b.dev
sandbox = Sandbox(
    template="base",
    domain="http://localhost:8080",   # your gateway URL
    api_key="dev-api-key-change-me",  # your gateway API key
)

# Run a command
result = sandbox.commands.run("echo 'Hello from E2BGateway!'")
print(result.stdout)  # Hello from E2BGateway!

# Execute Python code
execution = sandbox.run_code("print(2 + 2)")
print(execution.text)  # 4

# File operations
sandbox.files.write("/tmp/hello.txt", "Hello, World!")
content = sandbox.files.read("/tmp/hello.txt")
print(content)  # Hello, World!

# Cleanup
sandbox.kill()
```

### Async usage

```python
import asyncio
from e2b import AsyncSandbox

async def main():
    sandbox = await AsyncSandbox.create(
        template="base",
        domain="http://localhost:8080",
        api_key="dev-api-key-change-me",
    )
    result = await sandbox.commands.run("uname -a")
    print(result.stdout)
    await sandbox.kill()

asyncio.run(main())
```

---

## Using the E2B JavaScript SDK

### Install

```bash
npm install e2b
```

### Connect to the gateway

```typescript
import { Sandbox } from "e2b";

// Point the SDK at your gateway
const sandbox = await Sandbox.create({
  template: "base",
  domain: "http://localhost:8080",   // your gateway URL
  apiKey: "dev-api-key-change-me",   // your gateway API key
});

// Run a command
const result = await sandbox.commands.run("echo 'Hello from E2BGateway!'");
console.log(result.stdout);

// Execute JavaScript code
const execution = await sandbox.runCode("console.log(2 + 2)");
console.log(execution.text);

// File operations
await sandbox.files.write("/tmp/hello.txt", "Hello, World!");
const content = await sandbox.files.read("/tmp/hello.txt");
console.log(content);

// Cleanup
await sandbox.kill();
```

### With environment variables

```typescript
const sandbox = await Sandbox.create({
  template: "base",
  domain: "https://gateway.example.com",
  apiKey: process.env.E2B_API_KEY,
});
```

---

## Framework Integration

E2BGateway is compatible with AI agent frameworks that use E2B SDKs under the hood.

### LangChain / LangGraph

```python
from langchain.e2b import E2BTools

# Set the API base URL to your gateway
import os
os.environ["E2B_API_KEY"] = "dev-api-key-change-me"
os.environ["E2B_DOMAIN"] = "http://localhost:8080"

# Use E2B tools as usual — requests go through your gateway
tools = E2BTools()
```

### CrewAI

```python
from crewai.tools import E2BSandboxTool

# Configure via environment
os.environ["E2B_DOMAIN"] = "http://localhost:8080"
os.environ["E2B_API_KEY"] = "dev-api-key-change-me"
```

---

## Next Steps

- **[Backend Setup](./backend-setup.md)** — Configure E2B Cloud, agent-sandbox, or OpenSandbox adapters
- **[Migration Guide](./migration.md)** — Migrate from E2B Cloud to self-hosted backends
- **[OpenAPI Specification](../api/openapi.yaml)** — Full API reference
- **[Architecture Design](../design/README.md)** — Deep-dive into the gateway internals
