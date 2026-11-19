# Kind E2E Testing Infrastructure

End-to-end testing using [kind](https://kind.sigs.k8s.io/) (Kubernetes in Docker) with real sandbox backends.

## Architecture

```
┌────────────────────────────────────────────────────────┐
│ Kind Cluster                                           │
│                                                        │
│  ┌─────────────┐    ┌──────────────┐                   │
│  │ E2BGateway  │───▶│agent-sandbox │ (backend 1)       │
│  │  (gateway)  │    │  Controller  │                   │
│  └──────┬──────┘    └──────┬───────┘                   │
│         │                  │                           │
│         │           ┌──────┴───────┐                   │
│         │           │  Sandbox Pod │                   │
│         │           └──────────────┘                   │
│         │                                              │
│         │    ┌──────────────┐                          │
│         └───▶│ opensandbox  │ (backend 2)              │
│              │  Controller  │                          │
│              └──────┬───────┘                          │
│                     │                                  │
│              ┌──────┴───────┐                          │
│              │  Sandbox Pod │                          │
│              └──────────────┘                          │
│                                                        │
│  ┌─────────────────┐                                   │
│  │ E2E Test Runner │ ← runs multi-language examples    │
│  └─────────────────┘                                   │
└────────────────────────────────────────────────────────┘
```

## Prerequisites

- Docker
- kind (`go install sigs.k8s.io/kind@latest`)
- kubectl
- Python 3.9+ with `e2b` SDK (`pip install e2b e2b-code-interpreter`)
- Node.js 18+ with `@e2b/code-interpreter` (`npm install @e2b/code-interpreter`)

## Quick Start

```bash
# Run all E2E tests
make test-kind-e2e

# Or run step by step:
./hack/kind-e2e/setup.sh        # Create kind cluster + deploy
./hack/kind-e2e/run-tests.sh    # Run multi-language examples
./hack/kind-e2e/cleanup.sh      # Clean up
```

## Manual Testing

```bash
# After setup, port-forward the gateway
kubectl port-forward svc/e2bgateway 8080:8080 -n e2bgateway-system

# Then run examples
export E2B_DOMAIN=localhost:8080
export E2B_API_KEY=test-key
python examples/python/hello_world.py
```

## Backends

### Agent Sandbox
- Uses `sigs.k8s.io/agent-sandbox` CRDs
- Creates sandbox pods via Kubernetes custom resources
- Supports: commands, filesystem, code execution

### OpenSandbox
- Uses Alibaba OpenSandbox
- Creates sandbox pods via OpenSandbox API
- Supports: commands, filesystem, code execution

## Mock OpenSandbox Controller

The mock OpenSandbox controller (lifecycle API, ConnectRPC filesystem/process
services, Jupyter `/execute`, snapshot endpoints) exists in **two copies**
that must be kept in sync:

| Copy | Location | Used by |
|---|---|---|
| In-cluster (ConfigMap) | `test/kind-e2e/manifests/opensandbox/deployment.yaml` | CI kind E2E (`make kind-e2e-test`, GitHub Actions e2e workflow) |
| Local | `test/kind-e2e/controller_local.py` | Local verification without a cluster: `PORT=8000 python test/kind-e2e/controller_local.py` |

**Sync obligation:** when changing controller behavior (endpoints, response
shapes, status codes), apply the same change to **both** copies.

The local copy additionally carries patches that the in-cluster copy does not
have (extracted for local development on Windows):

- Endpoint URLs are constructed from the request `Host` instead of being
  hard-coded, and the listen port is configurable via the `PORT` env var.
- Windows-compatible shell handling (`_win_shell` maps to Git Bash,
  `_safe_cwd` sanitizes working directories) and `/execute` uses
  `sys.executable`.
- Fixes for upstream bugs present in the ConfigMap-embedded version
  (module-level handlers invoked via `self.`, missing `text` field in
  Jupyter `Result` events). Verify before relying on in-cluster behavior
  that mirrors these paths.
