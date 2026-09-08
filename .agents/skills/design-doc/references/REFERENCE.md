# Design Doc Reference

## File Locations

| Artifact | Path | Purpose |
|---|---|---|
| Design spec | `docs/superpowers/specs/YYYY-MM-DD-<topic>-design.md` | What & why — reviewed before implementation |
| Implementation plan | `docs/superpowers/plans/YYYY-MM-DD-<topic>.md` | How & when — drives the PR(s) |
| Top-level architecture | `docs/design/README.md` | System-wide architecture overview |
| OpenAPI | `docs/api/openapi.yaml` | HTTP API contract |
| Developer guide | `AGENTS.md` | Day-to-day dev reference |

## Spec Template — Minimal

```markdown
# <Feature Title>

**Issue**: #<issue>
**Date**: YYYY-MM-DD
**Priority**: P0 / P1 / P2

## Problem Statement
<2-3 sentences: what's broken, who's blocked, why now>

## Design Goals
- <goal 1>
- <goal 2>

## Non-Goals
- <what we explicitly defer>

## Architecture
<interface changes, data flow, ASCII diagram if useful>

## Security Considerations
<tokens, injection, threat model>

## Testing Strategy
<which layers, which behaviors>

## Open Questions
- [ ] <question 1> — owner: @name
```

## Plan Template — Minimal

```markdown
# <Feature Title> — Implementation Plan

**Spec**: [link]
**Issue**: #<issue>

## Phase 1: Interface Changes
- `internal/adapter/interface.go`: add method X
- All adapters: implement stub (agentsandbox, opensandbox, e2bcloud, mock)
- Unit tests for interface contract

## Phase 2: Adapter Implementation
- Primary adapter (<name>): full implementation
- Integration tests

## Phase 3: envd Proxy / Server
- `internal/server/envd_proxy.go`: wire in validation
- E2E tests

## Phase 4: Documentation
- `AGENTS.md`: add new section
- `configs/e2bgateway-example.yaml`: add new config
- `docs/api/openapi.yaml`: update schema

## Checklist
- [ ] Spec reviewed
- [ ] All adapters updated
- [ ] Unit / integration / E2E tests passing with `-race`
- [ ] `AGENTS.md` updated
- [ ] Config example updated
- [ ] `golangci-lint` clean
```

## Priority Guide

| Level | Meaning | When to use |
|---|---|---|
| P0 | Blocks core functionality | SDK cannot connect, security hole, data loss |
| P1 | Degrades experience | Missing feature, poor performance, flaky test |
| P2 | Nice to have | Refactor, docs improvement, new backend |

## Review Process

1. Author opens spec PR (or issue with spec attached)
2. Reviewers check: goals are measurable, non-goals are explicit, security considered, testing strategy is concrete
3. Spec approved → author writes plan
4. Plan reviewed → implementation begins
5. Implementation PR references both spec and plan
6. After merge, spec + plan stay in `docs/superpowers/` as historical record
