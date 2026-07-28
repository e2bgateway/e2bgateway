# Migration Guide

This guide covers migrating from E2B Cloud to E2BGateway, enabling you to run workloads on self-hosted or alternative backends while maintaining full compatibility with the E2B SDK ecosystem.

---

## Table of Contents

1. [Overview](#overview)
2. [Changing SDK Endpoints](#changing-sdk-endpoints)
3. [Authentication Migration](#authentication-migration)
4. [Feature Compatibility Matrix](#feature-compatibility-matrix)
5. [Known Differences](#known-differences)
6. [Step-by-Step Migration](#step-by-step-migration)
7. [Rollback Procedure](#rollback-procedure)

---

## Overview

E2BGateway is a drop-in replacement for the E2B Cloud API. It speaks the same HTTP + WebSocket protocol that E2B SDKs expect, so **no application code changes are required** in the common case. The migration consists of:

1. Deploying E2BGateway
2. Pointing your SDKs at the gateway URL instead of `api.e2b.dev`
3. Optionally configuring a self-hosted backend

```
Before:  E2B SDK  ─────────►  api.e2b.dev (E2B Cloud SaaS)
After:   E2B SDK  ─────────►  E2BGateway  ──►  Your backend (or E2B Cloud)
```

---

## Changing SDK Endpoints

### Python SDK

Set the `domain` parameter when creating a sandbox:

```python
# Before (E2B Cloud)
from e2b import Sandbox
sandbox = Sandbox(template="base", api_key="sk-e2b-...")

# After (E2BGateway)
sandbox = Sandbox(
    template="base",
    domain="https://gateway.your-domain.com",  # your gateway URL
    api_key="your-gateway-api-key",
)
```

Or set the environment variable globally:

```bash
export E2B_DOMAIN="https://gateway.your-domain.com"
export E2B_API_KEY="your-gateway-api-key"
```

### JavaScript SDK

```typescript
// Before
import { Sandbox } from "e2b";
const sandbox = await Sandbox.create({ apiKey: "sk-e2b-..." });

// After
const sandbox = await Sandbox.create({
  domain: "https://gateway.your-domain.com",
  apiKey: "your-gateway-api-key",
});
```

Or via environment variables:

```bash
export E2B_DOMAIN="https://gateway.your-domain.com"
export E2B_API_KEY="your-gateway-api-key"
```

### Raw HTTP clients

If you call the E2B API directly, update the base URL:

```bash
# Before
curl https://api.e2b.dev/sandboxes \
  -H "X-API-Key: sk-e2b-..."

# After
curl https://gateway.your-domain.com/sandboxes \
  -H "X-API-Key: your-gateway-api-key"
```

All API paths remain identical — the gateway serves the same routes at the root level (`/sandboxes`, `/templates`, etc.).

---

## Authentication Migration

### E2B Cloud authentication

E2B Cloud uses a single API key per account, passed as `X-API-Key` or as a Bearer token.

### E2BGateway authentication

E2BGateway supports multiple authentication methods and can be configured to accept the same API keys you already use.

#### Option 1: Reuse E2B API keys

Configure the gateway to accept your existing E2B API keys:

```yaml
auth:
  providers:
    - type: apikey
      headerName: X-API-Key
      secretRef: "sk-e2b-your-existing-key"
```

#### Option 2: Generate new gateway keys

Create new API keys specific to the gateway, allowing you to decouple from E2B Cloud credentials:

```yaml
auth:
  providers:
    - type: apikey
      headerName: X-API-Key
      secretRef: "new-gateway-key-1"
    - type: apikey
      headerName: X-API-Key
      secretRef: "new-gateway-key-2"
```

#### Option 3: Multi-tenant keys

Configure different keys per team or application:

```yaml
auth:
  providers:
    - type: apikey
      headerName: X-API-Key
      secretRef: "team-a-key"
    - type: apikey
      headerName: X-API-Key
      secretRef: "team-b-key"
```

#### Option 4: JWT authentication

For enterprise setups, use JWT tokens:

```yaml
auth:
  providers:
    - type: jwt
      issuer: "https://auth.your-domain.com"
      jwksURL: "https://auth.your-domain.com/.well-known/jwks.json"
      audience: "e2bgateway"
```

### Migrating API key references

Update API key references in your deployment manifests, CI/CD pipelines, and secret stores:

```bash
# Before
E2B_API_KEY=sk-e2b-...

# After
E2B_API_KEY=your-gateway-api-key
E2B_DOMAIN=https://gateway.your-domain.com
```

---

## Feature Compatibility Matrix

The following matrix shows feature support across backends when accessed through E2BGateway.

| Feature | E2B Cloud (passthrough) | agent-sandbox | OpenSandbox |
|---|:---:|:---:|:---:|
| **Sandbox Lifecycle** | | | |
| Create sandbox | Yes | Yes | Yes |
| List sandboxes | Yes | Yes | Yes |
| Get sandbox | Yes | Yes | Yes |
| Kill sandbox | Yes | Yes | Yes |
| Pause / Resume | Yes | Yes | Partial |
| Set timeout | Yes | Yes | Yes |
| **Execution** | | | |
| Run command | Yes | Yes | Yes |
| Execute code (Python) | Yes | Yes | Yes |
| Execute code (JavaScript) | Yes | Yes | Yes |
| Send stdin | Yes | Yes | Yes |
| Kill process | Yes | Yes | Yes |
| List processes | Yes | Yes | Yes |
| **Filesystem** | | | |
| Read file | Yes | Yes | Yes |
| Write file | Yes | Yes | Yes |
| Upload file | Yes | Yes | Yes |
| Download file | Yes | Yes | Yes |
| List directory | Yes | Yes | Yes |
| Make directory | Yes | Yes | Yes |
| Remove file | Yes | Yes | Yes |
| Move file | Yes | Yes | Yes |
| **Templates** | | | |
| List templates | Yes | Yes | Yes |
| Get template | Yes | Yes | Yes |
| Create template | Yes | Yes | Yes |
| Delete template | Yes | Yes | Yes |
| Build template | Yes | Partial | Yes |
| Template tags | Yes | Yes | Yes |
| **Other** | | | |
| Port forwarding | Yes | Partial | Yes |
| Snapshots | Yes | Partial | No |
| Warm pools | Yes | No | No |
| Environment variables | Yes | Yes | Yes |
| Logs | Yes | Yes | Yes |
| Metrics (v2) | Yes | Yes | Yes |
| Access token | Yes | Yes | Yes |

**Legend**: Yes = fully supported, Partial = supported with limitations, No = not supported

---

## Known Differences

While E2BGateway aims for full protocol compatibility, there are a few differences to be aware of:

### 1. Template management

When using self-hosted backends (agent-sandbox, OpenSandbox), templates are managed differently than in E2B Cloud:

- **E2B Cloud**: Templates are built and stored in E2B's cloud infrastructure. You manage them via the E2B dashboard or CLI.
- **Self-hosted**: Templates map to container images or CRD definitions in your cluster. The `POST /templates` endpoint triggers a build that creates a container image.

**Impact**: If you use custom templates, you need to rebuild them for the self-hosted backend on first migration.

### 2. Sandbox IDs

Sandbox IDs generated by E2BGateway may differ in format from those generated by E2B Cloud directly. This does not affect SDK usage since IDs are returned by the `create` call.

**Impact**: If you store sandbox IDs externally, ensure you use the IDs returned by the gateway, not pre-generated ones.

### 3. WebSocket connections

E2BGateway proxies WebSocket connections to the backend. Some advanced WebSocket features (such as terminal resize propagation) may have slightly different timing characteristics.

**Impact**: Negligible for most use cases. Interactive terminal sessions work identically.

### 4. Rate limits

E2BGateway enforces its own rate limits independently of E2B Cloud limits. When using the E2B Cloud adapter, both the gateway and E2B Cloud rate limits apply.

**Impact**: Configure gateway rate limits appropriately to avoid double-limiting.

### 5. Warm pools and snapshots

Warm pools and snapshots are fully supported when using the E2B Cloud adapter. Self-hosted backends may have limited or no support for these features (see the compatibility matrix).

### 6. API key format

E2BGateway accepts any string as an API key. It does not validate the E2B key format (`sk-e2b-...`). You can use simpler keys for self-hosted deployments.

---

## Step-by-Step Migration

### Phase 1: Deploy gateway in passthrough mode

Start by deploying E2BGateway with the E2B Cloud adapter. This gives you a chance to validate the gateway without changing backends.

```yaml
# config.yaml
backends:
  - name: e2b-cloud
    type: e2b-cloud
    enabled: true
    config:
      apiKey: "sk-e2b-your-key"

auth:
  providers:
    - type: apikey
      headerName: X-API-Key
      secretRef: "sk-e2b-your-key"  # reuse existing key

routing:
  defaultBackend: e2b-cloud
  strategy: static
```

Deploy and verify:

```bash
# Deploy gateway (Docker, Helm, or binary)
helm install e2bgateway e2bgateway/e2bgateway -f values.yaml

# Test with curl
curl https://gateway.your-domain.com/healthz

# Test sandbox creation through the gateway
curl -X POST https://gateway.your-domain.com/sandboxes \
  -H "X-API-Key: sk-e2b-your-key" \
  -H "Content-Type: application/json" \
  -d '{"templateID": "base"}'
```

### Phase 2: Switch SDKs to the gateway

Update your application configuration to point at the gateway. Start with a staging/test environment.

```bash
# Staging environment
export E2B_DOMAIN="https://gateway-staging.your-domain.com"
```

Run your integration test suite. Verify:
- Sandbox creation and destruction
- Command execution and code execution
- File operations
- Template listing
- WebSocket-based terminal sessions (if used)

### Phase 3: Switch to self-hosted backend (optional)

Once the gateway is validated, add a self-hosted backend and route traffic:

```yaml
backends:
  - name: e2b-cloud
    type: e2b-cloud
    enabled: true
    config:
      apiKey: "sk-e2b-your-key"

  - name: agent-sandbox
    type: agent-sandbox
    enabled: true
    config:
      namespace: "sandbox-runtime"

routing:
  defaultBackend: agent-sandbox
  strategy: template-based
  strategies:
    - name: migration-routes
      rules:
        # Route specific templates to self-hosted
        - template: "base"
          backend: "agent-sandbox"
        - template: "custom-*"
          backend: "agent-sandbox"
  failover:
    enabled: true
    chain:
      - agent-sandbox
      - e2b-cloud  # fallback
```

### Phase 4: Gradually shift traffic

Use weighted routing to shift traffic from E2B Cloud to self-hosted:

```yaml
routing:
  strategy: weighted
  strategies:
    - name: gradual-migration
      rules:
        - backend: "agent-sandbox"
          # Start with 10% of traffic
        - backend: "e2b-cloud"
```

Increase the self-hosted weight over days/weeks as confidence grows.

### Phase 5: Decommission E2B Cloud adapter

Once all traffic runs on self-hosted backends:

```yaml
backends:
  - name: agent-sandbox
    type: agent-sandbox
    enabled: true
    config:
      namespace: "sandbox-runtime"

routing:
  defaultBackend: agent-sandbox
  strategy: static
```

Remove the E2B Cloud API key from your secrets.

---

## Rollback Procedure

If you need to roll back to E2B Cloud at any point during the migration:

### Immediate rollback (configuration change)

1. Update the gateway configuration to route all traffic to E2B Cloud:

```yaml
routing:
  defaultBackend: e2b-cloud
  strategy: static
```

2. Restart the gateway:

```bash
# Kubernetes
kubectl -n e2bgateway rollout restart deployment/e2bgateway

# Docker
docker restart e2bgateway
```

3. Verify:

```bash
curl https://gateway.your-domain.com/sandboxes \
  -H "X-API-Key: your-key"
```

### Full rollback (remove self-hosted backend)

1. Update configuration to remove self-hosted backends
2. Ensure the E2B Cloud API key is still valid
3. Restart the gateway
4. Update SDK environment variables if you changed the API key

### Rollback checklist

- [ ] Gateway configuration updated to route to E2B Cloud
- [ ] Gateway restarted and healthy (`/readyz` returns 200)
- [ ] Sandbox creation tested via curl
- [ ] Application integration tests passing
- [ ] Monitoring dashboards show normal operation
- [ ] E2B Cloud API key is valid and has sufficient quota

### Data considerations

- **Running sandboxes**: Sandboxes on a self-hosted backend cannot be accessed through E2B Cloud. If you need to preserve running sandbox state, pause them before rolling back.
- **Templates**: Custom templates built for self-hosted backends are not available in E2B Cloud. Use the original Dockerfiles to rebuild in E2B Cloud if needed.
- **Files and snapshots**: Files stored in self-hosted sandbox volumes are not accessible from E2B Cloud. Download any needed data before rolling back.

---

## Support

If you encounter issues during migration:

1. Check the [Backend Setup Guide](./backend-setup.md) for adapter-specific configuration
2. Review the [OpenAPI specification](../api/openapi.yaml) for expected request/response formats
3. Examine gateway logs for detailed error messages
4. Open an issue at <https://github.com/e2bgateway/e2bgateway/issues>
