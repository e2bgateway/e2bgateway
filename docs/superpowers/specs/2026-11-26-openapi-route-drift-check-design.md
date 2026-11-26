# OpenAPI Route Drift Check

**Issue**: #118 — [v0.2.0][change] OpenAPI spec 与路由漂移校验及补全
**Date**: 2026-11-26
**Priority**: P2
**Approach**: Go-test-based inventory diff (chi.Walk vs openapi.yaml) with a self-checking exemption list, wired into the existing `check` workflow

---

## 1. Problem Statement

`docs/api/openapi.yaml` is the public API contract, but nothing verifies it against the
chi route table in `internal/server/http.go`. Drift has already appeared: the `/v2`
filesystem routes and the `/sandboxes/{sandboxID}/ws` streaming endpoint are registered
but undocumented, while several documented operations lack error-response definitions.
Contract drift breaks the SDK compatibility promise, and today it is only caught (if at
all) in review.

## 2. Design Goals

- CI fails when a route is registered but missing from the spec ("new route, no spec").
- CI fails when the spec documents an endpoint that is not registered ("ghost endpoint").
- CI fails when an operation documents no error response referencing the shared
  `Error`/`AuthError` schemas.
- An exemption list covers internal/legacy endpoints; every exemption must carry a
  reason and must match a live route, so the list cannot silently rot.
- Close the current gaps: document the 6 `/v2` filesystem operations and the `/ws`
  upgrade endpoint (with `x-websocket`), and add error responses to the 18 operations
  missing them.
- `make openapi-check` runs the gates locally; the `check` workflow runs it on every PR.

**Non-goals**

- No spec-first code generation or request validation middleware — the spec stays
  hand-written; this change only enforces consistency.
- No documentation of the `/api/v1/` legacy subtree (deprecated mirror of the
  root-level API; its async-execution handlers are stubs pending #106) — exempt instead.
- No website API Reference rendering (depends on website repo, #101).
- v2 snapshot routes and tenants (#104/#106) do not exist in the router; nothing to
  document until they land.

## 3. Architecture

### 3.1 Interface changes

None. No production code changes; the gates live in a new test file
`internal/server/openapi_drift_test.go` (package `server`, so it can walk the
unexported `srv.router`).

### 3.2 Data flow

```
chi router (built by server.New with a mock-backend test config)
        │  chi.Walk → "METHOD /path" set          ← actual
        ▼
  diff (both directions, minus exemptions) ──────► fail on drift
        ▲
        │  yaml parse → "METHOD /path" set        ← contract
docs/api/openapi.yaml
```

Second gate: parse the spec generically, collect `components/responses/*` entries whose
bodies reference `schemas/Error` or `schemas/AuthError`, then require every operation
(except `GET /healthz`, `GET /readyz` probes) to have at least one `>=400`/`default`
response resolving to one of those.

### 3.3 Exemption mechanism

- `exemptRoutes`: exact `"METHOD /path"` keys (`ALL` = any method), each with a reason
  string. Covers `/*` (envd data-plane catch-all proxy), `GET /metrics`, and the 10
  legacy root-level `/files*` + `/processes*` aliases of documented routes.
- `exemptPrefixes`: subtree exemptions; covers `/api/v1/`.
- Stale-exemption check: an exemption matching no registered route fails the test.

## 4. API Surface

Spec additions (documentation only — routes already exist):

- `GET /sandboxes/{sandboxID}/ws` — `x-websocket: true`, 101/400/401/500 responses;
  payload protocol defined by the E2B envd protocol, referenced in the description.
- `POST /v2/sandboxes/{sandboxID}/filesystem/upload|list|mkdir|rm|move`,
  `GET /v2/sandboxes/{sandboxID}/filesystem/download` — mirror the root-level
  filesystem entries (OpenAPI 3.0.3 cannot `$ref` path items), `V2` operationIds,
  new `v2 Filesystem` tag.
- 18 existing operations gain `"401": Unauthorized` + `"500": InternalServerError`
  refs (plus `404` where semantically appropriate).

## 5. Configuration

None. `go.yaml.in/yaml/v3` moves from indirect to direct dependency (already vendored
in `go.sum`; `go mod tidy` handles it).

## 6. Security Considerations

No runtime surface changes. The test builds a server with the mock backend and
`127.0.0.1:0` address and never listens. Exemption reasons are required so that
exempting a route stays a deliberate, reviewable act.

## 7. Observability

None. Failure output is the test report: sorted lists of missing routes, ghost
endpoints, stale exemptions, and operations without error responses.

## 8. Testing Strategy

The deliverable *is* tests (TDD end-to-end):

- Task 1 lands `TestOpenAPISpecMatchesRoutes` **red** — verified to fail with exactly
  the 7 known missing routes before any spec change.
- Task 2 fills the spec gaps → green.
- Task 3 lands `TestOpenAPIErrorResponses` **red** (exactly the 18 known violations),
  fixes the spec → green.
- Task 4 exposes both via `make openapi-check` (`-run 'TestOpenAPI' -count=1`) and adds
  the step to the `check` workflow's test job; a PR template reminds contributors to
  update the spec when adding routes.

## 9. Migration / Rollout

None — CI-only change plus documentation. Existing routes are untouched. If a future
PR adds a route without a spec entry, `make openapi-check` fails with instructions
(add to spec, or add an exemption with a reason).

## 10. Open Questions

- When #106 (async executions) lands, the `/api/v1/` prefix exemption should be
  narrowed and the executions endpoints documented (or promoted to a non-legacy
  prefix). Tracked by #106, not this change.
