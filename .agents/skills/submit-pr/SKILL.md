---
name: submit-pr
description: Submit a pull request for E2BGateway. Use when opening a PR — covers commit messages, PR titles, descriptions, required checks, and review etiquette.
license: Apache-2.0
metadata:
  author: The E2BGateway Authors
---

# Submit PR Skill for E2BGateway

Use this skill when creating, reviewing, or updating a pull request for E2BGateway.

## Workflow

1. **Branch off `main`** — all PRs target `main`.
2. **Make atomic commits** — one logical change per commit. See "Commit Messages" below.
3. **Run all local gates before submitting** — every one must pass before the PR is opened:
   ```bash
   make fmt            # format
   make vet            # go vet
   make lint           # golangci-lint (same version as CI: v2.14.0)
   make openapi-check  # openapi.yaml vs route table
   make test           # unit tests with -race
   make build          # linux/amd64 binary compiles
   ```
   `make pre-commit` covers fmt + vet + lint + test; run `make openapi-check` and `make build` in addition.
   Do not open the PR with any gate failing — if a failure pre-exists on `main` and is unrelated to the change, note it in the PR body instead of silently skipping it.
4. **Include the design doc** — every PR must ship a design markdown committed in the branch:
   - Spec: `docs/superpowers/specs/YYYY-MM-DD-<topic>-design.md` (see the `design-doc` skill)
   - Plan: `docs/superpowers/plans/YYYY-MM-DD-<topic>.md`
   - Trivial changes (typo fixes, dep bumps) may skip the spec, but the PR body must say why.
5. **Update relevant docs**:
   - `AGENTS.md` — if behavior, interface, or config changed
   - `configs/e2bgateway-example.yaml` — if config schema changed
   - `docs/api/openapi.yaml` — if HTTP API changed (`make openapi-check` must pass)
6. **Open the PR** with a descriptive title and body. See "PR Format" below.
7. **Wait for CI** — `check` workflow (lint + test) and `e2e` workflow must pass.
8. **Address review comments** — push fixup commits, then rebase-squash before merge if requested.

## Commit Messages

Follow [Conventional Commits](https://www.conventionalcommits.org/) (loosely — tooling is not enforced yet, but the structure is):

```
<type>(<scope>): <short summary>

<optional body — explain WHY, not WHAT>

<optional footer — Closes #123, BREAKING CHANGE, etc.>
```

### Types

| Type | When |
|---|---|
| `feat` | New feature (new adapter method, new endpoint, new config) |
| `fix` | Bug fix |
| `refactor` | Code restructuring without behavior change |
| `test` | Adding or fixing tests only |
| `docs` | Documentation only (AGENTS.md, design docs, README) |
| `chore` | Dep bumps, CI config, tooling |
| `ci` | GitHub Actions, Dependabot config |
| `perf` | Performance improvement |
| `style` | Formatting, whitespace (no logic change) |

### Scopes

Common scopes in E2BGateway:

- `adapter` — interface changes affecting all backends
- `agentsandbox` — agent-sandbox adapter only
- `opensandbox` — opensandbox adapter only
- `e2bcloud` — e2b-cloud adapter only
- `envd` — envd data plane proxy
- `auth` — authentication
- `routing` — backend selection
- `config` — config loading/validation
- `api` — HTTP handlers / DTOs
- `deps` — dependency updates

### Examples

```
feat(adapter): add ValidateAccessToken to SandboxAdapter interface

Server-side token validation in the envd proxy requires each adapter to
verify tokens before the proxy forwards requests.

Closes #23

fix(agentsandbox): escape shell args in MakeDir and RemoveFiles

User-controlled paths were concatenated directly into shell commands,
allowing shell injection via sandbox paths with metacharacters.
```

### Anti-patterns

| ❌ Bad | ✅ Good |
|---|---|
| `update code` | `fix(agentsandbox): escape shell args in MakeDir` |
| `WIP` (in a PR branch — fine in local commits) | `feat(opensandbox): add dual-mode access token` |
| `fix bug` | `fix(envd): return 401 on missing X-Access-Token` |
| `changes per review` | `refactor(adapter): extract lifecycleClient interface` |

## PR Title

Same format as commit messages. Many repos auto-generate the squash commit from the PR title, so treat it as the eventual commit message.

```
feat(opensandbox): hybrid access token with OSEP-0011 signed endpoint support
```

## PR Body

Use this template:

```markdown
## Summary
<1-3 sentences: what does this PR do and why?>

Closes #<issue>

## Changes
- <bullet list of major changes — NOT a file-by-file diff dump>
- ...

## Testing
- [ ] Tests written first (TDD — see `.agents/skills/testing`); each new test was watched failing before the implementation landed
- [ ] Unit tests added/updated (`go test ./... -race`)
- [ ] Integration tests added/updated (if adapter changed)
- [ ] E2E tests added/updated (if API/proxy changed)
- [ ] All local gates pass: `make fmt`, `make vet`, `make lint`, `make openapi-check`, `make test`, `make build`

## Documentation
- [ ] Design doc committed in this branch: `docs/superpowers/specs/YYYY-MM-DD-<topic>-design.md` (required; trivial changes must state why they are exempt)
- [ ] Implementation plan committed: `docs/superpowers/plans/YYYY-MM-DD-<topic>.md`
- [ ] `AGENTS.md` updated (if behavior/config/interface changed)
- [ ] `configs/e2bgateway-example.yaml` updated (if config schema changed)
- [ ] `docs/api/openapi.yaml` updated (if HTTP API changed; `make openapi-check` passes)

## Checklist
- [ ] No new `gocyclo` warnings
- [ ] `golangci-lint` clean
- [ ] All adapters implement new interface methods (if interface changed)
- [ ] Backward compatible (or breaking change called out)
```

## Required CI Checks

All PRs must pass before merge:

| Check | What it does | Local equivalent |
|---|---|---|
| `check / Lint` | `golangci-lint run` + `go vet ./...` + `go mod tidy` diff check | `make lint && make vet` |
| `check / Test (Go <version>)` | Unit tests with `-race` across Go version matrix | `make test` |
| `e2e / ...` | E2E tests in Kind cluster against real backends | `make kind-e2e-test` |

## Review Etiquette

### For authors

- **Small PRs merge faster** — split large features into stacked PRs (interface first → implementation → docs).
- **Self-review first** — read your own diff before requesting review.
- **Respond to every comment** — even "done" — so reviewers can track.
- **Don't force-push during active review** — push fixup commits; offer to squash before merge.

### For reviewers

- **Distinguish blockers from nits** — prefix with `[blocking]` or `[nit]`.
- **Ask, don't tell** — "What happens if X?" is more useful than "Change this to Y."
- **Check the security angle** — every user-input path should be sanitized; reference the `security-coding` skill.

## Merge Strategy

E2BGateway uses **squash merge** to keep `main` history clean. The PR title becomes the squash commit subject; the PR body becomes the commit body.

## Dep Bumps

Dependabot handles most dependency updates. When reviewing a dep bump PR:

1. Check the changelog for breaking changes.
2. Verify `go mod tidy` is clean.
3. Run `make test` — CI will also run it.
4. For `sigs.k8s.io/agent-sandbox` bumps: check if the CRD schema changed; if so, update Helm chart and example configs.

## Examples in This Repo

Recent PRs following this pattern:
- `#44` — `feat(opensandbox): hybrid access token with OSEP-0011 signed endpoint support`
- `#43` — `feat(opensandbox): implement GetAccessToken with server-side validation`
- `#42` — `feat(adapter): implement GetAccessToken for agent-sandbox with server-side validation`
- `#15` — `fix: comprehensive adapter improvements - security, functionality, and robustness`

See [references/REFERENCE.md](references/REFERENCE.md) for CI details and troubleshooting.
