---
name: design-doc
description: Write design specs and implementation plans for E2BGateway features. Use when designing a new feature, adapter, protocol extension, or non-trivial change that needs review before implementation.
license: Apache-2.0
metadata:
  author: The E2BGateway Authors
---

# Design Doc Skill for E2BGateway

Use this skill to write, review, or update design specs and implementation plans for E2BGateway features.

## Workflow

1. **Scope the change** — Is it a new adapter, new API endpoint, protocol extension, security fix, or refactor?
2. **Pick the right artifact**:
   - Trivial fix (typo, dep bump, single-line bug): no design doc needed, just a PR.
   - Medium change (new config flag, small refactor): PR description is enough.
   - Non-trivial feature (new adapter method, new endpoint, protocol change, security-sensitive change): write a **spec** + **plan**.
3. **Write the spec** → `docs/superpowers/specs/YYYY-MM-DD-<topic>-design.md`
4. **Write the plan** → `docs/superpowers/plans/YYYY-MM-DD-<topic>.md`
5. **Link the spec in the PR** that implements it.
6. After merge, archive obsolete specs only if they become misleading; prefer leaving them as historical record.

## Two-Artifact Model

### Spec (the "what" and "why")

File: `docs/superpowers/specs/YYYY-MM-DD-<topic>-design.md`

Required sections:

```markdown
# <Feature Title>

**Issue**: #<issue> — <issue title>
**Date**: YYYY-MM-DD
**Priority**: P0 / P1 / P2
**Approach**: <short label, e.g., "A — Per-adapter token cache + SandboxAdapter interface extension">

---

## 1. Problem Statement
What is broken or missing. Why it matters now.

## 2. Design Goals
- Bullet list of concrete, measurable goals
- Include non-goals explicitly ("We are NOT solving X in this iteration")

## 3. Architecture
### 3.1 Interface changes
### 3.2 Adapter behavior
### 3.3 Data flow
Use ASCII diagrams or sequence diagrams where helpful.

## 4. API Surface
HTTP endpoints, request/response schemas, error codes.
Reference `internal/api/dto/` for wire format.

## 5. Configuration
New YAML fields, env vars, defaults, validation rules.
Remember: Viper lowercases map keys — document both camelCase and lowercase handling.

## 6. Security Considerations
- Token lifetime, rotation, revocation
- Input sanitization (shell injection, heredoc, PID validation)
- Threat model for the change

## 7. Observability
- New metrics, log fields, traces
- Include `sandboxID`, `adapter`, `requestID` in structured logs

## 8. Testing Strategy
Which layers (unit / integration / E2E) cover which behaviors.

## 9. Migration / Rollout
Backward compatibility, feature flags, gradual rollout plan.

## 10. Open Questions
Things still to be decided — with owners and deadlines.
```

### Plan (the "how" and "when")

File: `docs/superpowers/plans/YYYY-MM-DD-<topic>.md`

Required sections:

```markdown
# <Feature Title> — Implementation Plan

**Spec**: [link to spec]
**Issue**: #<issue>

---

## Overview
One paragraph: what we're building and why.

## Phase 1: Foundation
- Files to create/modify
- Interface changes
- Unit tests

## Phase 2: Implementation
- Adapter logic
- Integration tests

## Phase 3: Integration
- envd proxy changes
- E2E tests

## Phase 4: Documentation
- Update `AGENTS.md`
- Update `configs/e2bgateway-example.yaml`
- Update OpenAPI docs (`docs/api/openapi.yaml`)

## Checklist
- [ ] Spec reviewed
- [ ] All adapters implement new interface methods (incl. mock, e2bcloud)
- [ ] Unit tests pass with `-race`
- [ ] Integration tests pass for each affected adapter
- [ ] E2E tests added and passing
- [ ] `AGENTS.md` updated
- [ ] Config example updated
- [ ] OpenAPI docs updated
- [ ] No new `gocyclo` warnings
- [ ] `golangci-lint` clean
```

## Writing Principles

1. **Bilingual is OK** — E2BGateway's existing docs mix Chinese and English. Follow the language the issue was opened in; code identifiers stay English.
2. **Concrete over abstract** — show Go snippets, not prose descriptions of Go code.
3. **One decision per section** — don't bundle interface changes with config changes in the same paragraph.
4. **Reference, don't repeat** — link to `AGENTS.md` sections, not duplicate content.
5. **Date-stamp everything** — filenames and headers both.

## Common Pitfalls

| Pitfall | How to avoid |
|---|---|
| Spec without plan → implementation stalls | Always pair spec + plan for non-trivial work |
| Plan without spec → design debates in PR review | Spec first, review, then plan |
| Forgetting to update all adapters | Plan checklist includes all adapters explicitly |
| Viper case-sensitivity bugs | Spec documents both `camelCase` and `lowercase` parsing |
| Missing `mock` / `e2bcloud` stubs | Plan checklist includes "all adapters implement new method" |
| E2E test skipped | Plan includes E2E test cases with expected behavior |
| `AGENTS.md` drift | Plan includes "update AGENTS.md" as explicit phase |

## Examples in This Repo

- **Spec**: `docs/superpowers/specs/2026-09-08-access-token-implementation-design.md`
- **Plan**: `docs/superpowers/plans/2026-09-08-access-token-implementation.md`
- **Plan**: `docs/superpowers/plans/2026-09-08-opensandbox-access-token-hybrid.md`

See [references/REFERENCE.md](references/REFERENCE.md) for detailed templates and reference material.
