# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

#### Initial Release Features

- Full E2B API compatibility
- Support for multiple backends:
  - E2B Cloud (passthrough)
  - agent-sandbox (Kubernetes CRD)
  - OpenSandbox (Alibaba)
  - Mock adapter (testing)
- Sandbox lifecycle management
- Code execution and command running
- Filesystem operations
- Template management
- Warm pools
- Snapshots
- Access token authentication
- WebSocket support for streaming
- OpenTelemetry observability
- Rate limiting and authentication
- Helm chart for Kubernetes deployment

#### Port Forwarding Support (Issue #26)

**Priority**: P0 - Critical for Web Application Exposure

Implemented complete port forwarding functionality for both `agent-sandbox` and `opensandbox` backends, enabling users to access web applications and services running inside sandboxes.

**API Endpoints**:
- `GET /sandboxes/{sandboxID}/ports` - List open ports in a sandbox
- `GET /sandboxes/{sandboxID}/ports/{port}` - Get public URL for a specific port

**Backend Implementations**:

1. **agent-sandbox** (`internal/adapter/agentsandbox/adapter.go`):
   - Implemented `ListPorts()` method with port tracking
   - Implemented `GetPortURL()` method that constructs URLs using Pod IP addresses
   - Added `portTracker` field to track accessed ports per sandbox
   - Automatic cleanup of port tracker on sandbox deletion
   - URL format: `http://{pod-ip}:{port}`

2. **opensandbox** (`internal/adapter/opensandbox/adapter.go`):
   - Implemented `ListPorts()` method with port tracking
   - Implemented `GetPortURL()` method using OpenSandbox's `GetEndpoint` API
   - Supports both gateway-generated tokens and OSEP-0011 signed endpoints
   - Added `portTracker` field to track accessed ports per sandbox
   - Automatic cleanup of port tracker on sandbox deletion
   - URL format: Provided by OpenSandbox server

**Port Tracking Mechanism**:
Since neither `agent-sandbox` nor `opensandbox` provide native APIs to list all open ports, the gateway implements a port tracking mechanism:
- Ports are automatically tracked when accessed via `GetPortURL`
- `ListPorts` returns all tracked ports for a sandbox
- Port tracker is cleaned up when sandbox is deleted

**Testing**:
- Added comprehensive unit tests for both adapters
- Added end-to-end tests in `test/e2e/e2b_api_test.go`
- Added dedicated port forwarding E2E test script for real backends
- Tests verify:
  - Empty port list for unknown sandboxes
  - Port tracking when `GetPortURL` is called
  - Multiple ports can be tracked per sandbox
  - Port cleanup on sandbox deletion

**Documentation**:
- Created comprehensive port forwarding guide: `docs/guides/port-forwarding.md`
- Updated `README.md` with port forwarding API endpoints
- Added usage examples for Python, JavaScript, and cURL
- Documented backend-specific behavior and troubleshooting

**Configuration**:
- Updated Helm chart `values.yaml` with port forwarding configuration
- Added `portForwarding` section with common ports documentation
- No additional configuration required - feature is automatically enabled

**Migration Notes**:
- Existing deployments will automatically gain port forwarding capabilities
- No configuration changes required
- Port tracking is per-sandbox and does not persist across gateway restarts

**Security Considerations**:
- Port URLs are internal network addresses
- Proper authentication (API keys, access tokens) required
- Consider using ingress controllers with TLS for production deployments
- Sandbox ports are isolated per sandbox

#### SDK Data Plane E2E Testing (Issue #27)

**Priority**: P0 — SDK data plane previously never verified in CI

Enabled Python/JS SDK E2E tests in CI by removing `SKIP_SDK_TESTS=1` and enhancing the mock OpenSandbox controller to implement the ConnectRPC protocol and Jupyter-like code execution endpoints.

**Changes**:

1. **CI Workflow** (`.github/workflows/e2e.yml`):
   - Removed `SKIP_SDK_TESTS=1` and `SKIP_DATA_PLANE_TESTS=1` from the `e2e-agent-sandbox` job
   - Removed `SKIP_SDK_TESTS=1` from the `e2e-opensandbox` job
   - SDK examples now run against both backends in CI

2. **Mock OpenSandbox Controller ConnectRPC Support** (`test/kind-e2e/manifests/opensandbox/deployment.yaml`):
   - Rewrote the inline Python mock controller to implement the ConnectRPC protocol (JSON codec, matching the E2B SDK wire format)
   - **Unary RPCs** (`filesystem.Filesystem/*`): `Content-Type: application/json` + `Connect-Protocol-Version: 1`
     - `ListDir`, `Stat`, `MakeDir`, `Remove`, `Move`
   - **Server-streaming RPCs** (`process.Process/Start`): `Content-Type: application/connect+json` with envelope framing (1 byte flags + 4 byte length + JSON message)
     - Real-time streaming via `subprocess.Popen` + reader threads
     - Event flow: `start` → `data.stdout`/`data.stderr` (base64) → `end.exitCode` → end-stream trailer
   - Switched to `ThreadingHTTPServer` for concurrent request handling
   - Mounted mock controller as a ConfigMap instead of inline script

3. **Mock Jupyter Endpoint**:
   - Added `POST /proxy/{port}/execute` endpoint simulating the `e2b_code_interpreter` Jupyter protocol
   - NDJSON response with event types: `stdout`, `stderr`, `result`, `error`, `number_of_executions`
   - Executes Python (`python3 -c`) or JavaScript (`node -e`) code and streams output

4. **SDK Example Patches** (for CI URL override):
   - `examples/python/code_execution.py`: patches `Sandbox._jupyter_url` to use `E2B_SANDBOX_URL` when set
   - `examples/javascript/code_execution.js`: patches `Sandbox.prototype.jupyterUrl` via `Object.defineProperty`
   - Without the patch, the SDK constructs Jupyter URLs as `https://{port}-{sandboxID}.{domain}` which doesn't resolve in CI (no wildcard DNS, no TLS)

5. **Test Script** (`hack/kind-e2e/run-examples-e2e.sh`):
   - Updated skip messages to be backend-agnostic
   - All SDK examples now included: `hello_world`, `sandbox_lifecycle`, `commands`, `code_execution`, `filesystem`

**Result**:
- ✅ Python SDK: 5/5 examples passing in CI
- ✅ JavaScript SDK: 5/5 examples passing in CI
- ✅ Go SDK: 4/4 examples passing in CI
- ✅ cURL API tests: all passing
- SDK data plane (ConnectRPC + Jupyter) now verified in CI on every PR
