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
