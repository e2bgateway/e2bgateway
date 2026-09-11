# Port Forwarding E2E Testing Guide

This guide explains how to test port forwarding functionality against real sandbox backends.

## Overview

Port forwarding E2E tests verify that the gateway can:
1. List open ports in a sandbox
2. Get accessible URLs for specific ports
3. Track ports that have been accessed
4. Handle errors correctly (non-existent sandboxes, etc.)

Tests run against **real backends** (agent-sandbox and/or opensandbox) in a Kubernetes environment.

## Test Files

### 1. `test-port-forwarding.sh`

**Purpose**: Dedicated port forwarding test script for real backends

**Features**:
- Tests both `agent-sandbox` and `opensandbox` backends
- Starts an HTTP server inside the sandbox
- Verifies ListPorts and GetPortURL APIs
- Attempts to access the port URL (if network allows)
- Tests error handling for invalid sandboxes
- Validates port tracking mechanism

**Usage**:
```bash
# Test both backends
./hack/kind-e2e/test-port-forwarding.sh both

# Test only agent-sandbox
./hack/kind-e2e/test-port-forwarding.sh agent-sandbox

# Test only opensandbox
./hack/kind-e2e/test-port-forwarding.sh opensandbox
```

**Example Output**:
```
============================================================
 Port Forwarding E2E Test
============================================================
Gateway: http://localhost:8080
Backend: both
============================================================

============================================================
 Testing Port Forwarding on: agent-sandbox
============================================================

  ℹ INFO: Creating sandbox on agent-sandbox...
  ✓ PASS: Created sandbox: abc-123-def
  ✓ PASS: Sandbox is running
  ℹ INFO: Starting HTTP server on port 3000...
  ✓ PASS: Started HTTP server on port 3000

  ℹ INFO: Testing ListPorts API...
  ✓ PASS: ListPorts API responds
  ✓ PASS: ListPorts response has 'ports' field
  ℹ INFO: Listed 0 port(s)

  ℹ INFO: Testing GetPortURL API for port 3000...
  ✓ PASS: GetPortURL API responds for port 3000
  ✓ PASS: GetPortURL returned URL: http://10.244.0.7:3000
  ℹ INFO: Attempting to access port URL...
  ✓ PASS: Successfully accessed port URL (HTTP 200)

  ℹ INFO: Testing GetPortURL API for port 8080...
  ✓ PASS: GetPortURL API responds for port 8080
  ✓ PASS: GetPortURL returned valid response for port 8080

  ℹ INFO: Verifying port tracking...
  ✓ PASS: Port tracking works (tracked 2 ports)

  ℹ INFO: Testing error handling...
  ✓ PASS: GetPortURL for non-existent sandbox returns 404

  ℹ INFO: Cleaning up sandbox...
  ✓ PASS: Deleted sandbox

============================================================
 Test Summary
============================================================
  ✓ Passed: 15
  ✗ Failed: 0
  ⊘ Skipped: 0
============================================================
```

### 2. `run-tests.sh`

**Purpose**: General E2E test suite (includes port forwarding tests)

**Features**:
- Runs comprehensive E2E tests including port forwarding
- Tests sandbox lifecycle, commands, code execution, filesystem, etc.
- Port forwarding tests are integrated into Test 2 (Sandbox Lifecycle)

**Usage**:
```bash
# Run all tests (includes port forwarding)
./hack/kind-e2e/run-tests.sh

# Run tests for specific backend
./hack/kind-e2e/run-tests.sh agent-sandbox
./hack/kind-e2e/run-tests.sh opensandbox
```

**Port Forwarding Tests Included**:
- `GET /sandboxes/{id}/ports` - List ports
- `GET /sandboxes/{id}/ports/3000` - Get port URL
- `GET /sandboxes/{id}/ports/8080` - Get port URL
- Port tracking verification
- Error handling for non-existent sandboxes

## Prerequisites

### For Kind Cluster Testing

1. **Docker** installed and running
2. **kind** installed: `go install sigs.k8s.io/kind@latest`
3. **kubectl** installed
4. **Python 3.9+** with `e2b` SDK: `pip install e2b e2b-code-interpreter`

### For Real Cluster Testing

1. **kubectl** configured to access your cluster
2. **E2BGateway** deployed with at least one backend enabled
3. **Port forwarding** set up: `kubectl port-forward svc/e2bgateway 8080:8080 -n e2bgateway-system`

## Setup

### Option 1: Kind Cluster (Local Testing)

```bash
# Create kind cluster and deploy gateway
make kind-e2e-setup

# Wait for all pods to be ready
kubectl wait --for=condition=ready pod -l app=e2bgateway -n e2bgateway-system --timeout=300s

# Port forward the gateway
kubectl port-forward svc/e2bgateway 8080:8080 -n e2bgateway-system &
```

### Option 2: Real Cluster

```bash
# Deploy gateway using Helm
helm install e2bgateway ./deploy/helm/e2bgateway \
  --namespace e2bgateway-system \
  --create-namespace

# Port forward the gateway
kubectl port-forward svc/e2bgateway 8080:8080 -n e2bgateway-system &
```

## Running Tests

### Quick Test (Port Forwarding Only)

```bash
# Test port forwarding on both backends
export E2B_DOMAIN=localhost:8080
export E2B_API_KEY=test-key
./hack/kind-e2e/test-port-forwarding.sh both
```

### Full E2E Test Suite

```bash
# Run all E2E tests (includes port forwarding)
export E2B_DOMAIN=localhost:8080
export E2B_API_KEY=test-key
./hack/kind-e2e/run-tests.sh
```

### Test Specific Backend

```bash
# Test agent-sandbox only
./hack/kind-e2e/test-port-forwarding.sh agent-sandbox

# Test opensandbox only
./hack/kind-e2e/test-port-forwarding.sh opensandbox
```

## Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `E2B_DOMAIN` | Gateway address | `localhost:8080` |
| `E2B_API_KEY` | API key for authentication | `test-key` |

## Test Coverage

### API Endpoints Tested

1. **ListPorts**: `GET /sandboxes/{sandboxID}/ports`
   - Returns list of open ports
   - Response format validation
   - Empty list for unknown sandboxes

2. **GetPortURL**: `GET /sandboxes/{sandboxID}/ports/{port}`
   - Returns accessible URL for port
   - URL format validation
   - Error handling (404 for unknown sandboxes)

3. **Port Tracking**
   - Ports accessed via GetPortURL are tracked
   - Tracked ports appear in subsequent ListPorts calls
   - Port tracker cleaned up on sandbox deletion

### Backend-Specific Behavior

#### agent-sandbox
- URL format: `http://{pod-ip}:{port}`
- Requires network access to Pod IPs
- Ports constructed from Pod IP and port number

#### opensandbox
- URL format: Provided by OpenSandbox server
- Supports both gateway tokens and OSEP-0011 signed endpoints
- May require specific network configuration

## Troubleshooting

### Tests Fail with "no response"

**Cause**: Gateway not accessible

**Solution**:
```bash
# Check if gateway is running
kubectl get pods -n e2bgateway-system

# Verify port forwarding
kubectl port-forward svc/e2bgateway 8080:8080 -n e2bgateway-system

# Test gateway connectivity
curl http://localhost:8080/healthz
```

### Port URL Not Accessible

**Cause**: Network isolation or firewall

**Solutions**:
1. Ensure test machine can reach Pod IPs (for agent-sandbox)
2. Check firewall rules allow traffic to sandbox ports
3. Verify service inside sandbox is binding to `0.0.0.0`

### Port Tracking Not Working

**Cause**: Adapter not tracking ports

**Solution**:
- Verify adapter implementation includes `portTracker` field
- Check that `GetPortURL` adds ports to tracker
- Ensure `ListPorts` returns tracked ports

### Sandbox Not Ready

**Cause**: Backend not properly configured

**Solution**:
```bash
# Check backend pods
kubectl get pods -n sandbox-system

# Check gateway logs
kubectl logs -l app=e2bgateway -n e2bgateway-system

# Verify backend configuration
kubectl get configmap e2bgateway -n e2bgateway-system -o yaml
```

## CI/CD Integration

### GitHub Actions Example

```yaml
name: Port Forwarding E2E
on: [push, pull_request]

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3

      - name: Setup Kind
        uses: helm/kind-action@v1

      - name: Deploy Gateway
        run: make kind-e2e-setup

      - name: Run Port Forwarding Tests
        run: |
          kubectl port-forward svc/e2bgateway 8080:8080 -n e2bgateway-system &
          sleep 5
          ./hack/kind-e2e/test-port-forwarding.sh both
```

## Related Documentation

- [Port Forwarding Guide](../../docs/guides/port-forwarding.md)
- [Kind E2E Testing](../../test/kind-e2e/README.md)
- [Backend Setup](../../docs/guides/backend-setup.md)

## Support

For issues or questions:
- GitHub Issues: https://github.com/e2bgateway/e2bgateway/issues
- Documentation: https://github.com/e2bgateway/e2bgateway/tree/main/docs
