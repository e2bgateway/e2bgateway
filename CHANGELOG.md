# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## Unreleased

* [FEATURE] Horizontal scaling: new top-level `sessionStore` config (`memory` | `redis`) persists envd access tokens and the sandbox→backend routing map in a shared store, so any replica can serve data-plane requests and tokens survive gateway restarts. Includes SetNX token arbitration (one live token per sandbox cluster-wide), real (not re-stamped) `expiresAt`, 404 on envd-proxy routing miss with multiple backends (was: arbitrary backend → misleading 401), and 502 on session-store outages. `replicas > 1` / HPA requires `sessionStore.type: redis`; deploy manifests and overlays updated accordingly. #90
* [BUGFIX] **Breaking (security):** `auth.providers[].secretRef` (apikey) now loads API keys from a JSON file at the given path (Kubernetes: mount a Secret as a volume — see the helm `existingSecret` value; non-Kubernetes: any file on disk). The hardcoded `test-key-1`/`test-key-admin` fallback is removed — an unresolvable `secretRef` now fails startup, and setting both `keys` and `secretRef` is a config error. #92
* [FEATURE] Serve Prometheus metrics on the main HTTP port (`observability.metrics`): HTTP request/latency, sandbox lifecycle, envd proxy, access-token failures, warm pool acquire, backend health and active sandbox gauges. #93

## 0.1.0 / 2026-11-16

> **Note:** Initial release of E2BGateway, an E2B-compatible API gateway that routes E2B SDK traffic to pluggable sandbox backends: agent-sandbox (Kubernetes CRD), OpenSandbox, E2B Cloud, and a mock adapter for testing. Deploy via Helm (`deploy/helm/e2bgateway`) or Kustomize (`deploy/kustomize`).

* [FEATURE] Full E2B API compatibility: sandbox lifecycle, code execution, command running, filesystem operations, template management, snapshots, access-token authentication, WebSocket streaming, rate limiting, and OpenTelemetry observability.
* [FEATURE] Add port forwarding APIs (`GET /sandboxes/{id}/ports`, `GET /sandboxes/{id}/ports/{port}`) for agent-sandbox (pod-IP URLs) and opensandbox (server-provided endpoints), with per-sandbox port tracking. #47
* [FEATURE] Add a native envd data plane (`internal/envd`) using ConnectRPC to talk directly to envd daemons in sandbox pods for process, filesystem, and file transfer operations. #48
* [FEATURE] Add access tokens with server-side validation for agent-sandbox #42 and opensandbox #43, plus an opensandbox hybrid mode using OSEP-0011 signed endpoints (`useSignedEndpoint`). #44
* [FEATURE] Add WebSocket-based streaming code execution (`ExecuteCodeStream`) for the e2b-cloud adapter. #45
* [FEATURE] Add create-time environment variable injection for agent-sandbox #50 and runtime `SetEnvs` for opensandbox. #59
* [FEATURE] Add template CRUD, builds, and aliases to the agent-sandbox adapter backed by `SandboxTemplate` resources. #62
* [FEATURE] Add E2B-compatible template management to the opensandbox adapter with a pluggable `TemplateStore` (in-memory default, Redis for multi-instance persistence); `CreateSandbox` resolves `TemplateID` via static map, template store, alias, then raw image fallback. #63
* [FEATURE] Add JWT authentication with static JWKS (RS256/ES256, `sub`/`aud`/`iss`/`exp` claims, space-separated `scope`). #64
* [FEATURE] Add warm pool CRUD for agent-sandbox (`SandboxWarmPool` CRD, `OnReplenish`/`Recreate` update strategies) #82 and opensandbox (SDK PoolManager with optional Redis state store), including force-delete protection for pool-owned sandboxes. #84
* [FEATURE] Add optional HTTPS listener (`server.https`) required by the Python/JS E2B SDKs when TLS is terminated at the gateway. #48
* [FEATURE] Publish the sandbox image to GHCR. #46
* [CHANGE] Align the agent-sandbox backend with `sigs.k8s.io/agent-sandbox` v1.0.11 and remove the legacy SDK-handle data-plane fallback; envd is now the only content data plane, and the SDK handle remains solely for the cold-start `CreateSandbox` tunnel path via sandbox-router. #86
* [CHANGE] Adopt the OpenSandbox Go SDK v1.2.1: pool destroy via SDK PoolManager, lifecycle operation retries, and removal of the redundant sandbox cache. #87 #88
* [CHANGE] SandboxTemplate pods now require `securityContext.privileged: true` (CAP_SYS_RESOURCE) so envd's process wrapper can write `/proc/[pid]/oom_score_adj`. #48
* [ENHANCEMENT] Run Python/JS/Go SDK data-plane E2E suites in CI against both agent-sandbox and opensandbox, and add gateway-restart routing recovery coverage for agent-sandbox. #86
* [BUGFIX] Fix the ConnectRPC envelope protocol (1-byte flags + 4-byte length framing instead of plain JSON) and envd file uploads (`/files` expects the path as a URL query parameter). #48
* [BUGFIX] Comprehensive adapter fixes covering security, functionality, and robustness across all backends. #15
