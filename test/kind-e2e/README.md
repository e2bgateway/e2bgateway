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
