# Submit PR Reference

## CI Workflows

### `check.yml` — Lint + Test (required)

Triggered on push to `main`/`master` and on PRs.

**Jobs**:

| Job | Steps | Local equivalent |
|---|---|---|
| `Lint` | `golangci-lint run` (v2.12.2) → `go vet ./...` → `go mod tidy` diff check | `make lint && make vet` |
| `Test (Go <matrix>)` | `go test -race ./...` across Go version matrix | `make test` |

### `e2e.yml` — End-to-End (required)

Runs full E2E tests in a Kind cluster against both `agent-sandbox` and `opensandbox` backends. Includes Go, Python, JavaScript, and cURL examples.

Local equivalent:
```bash
make kind-e2e-setup && make kind-e2e-test && make kind-e2e-cleanup
```

### `release.yml`

Tag-triggered. Builds and publishes Docker images and release binaries. Not relevant for PR authoring.

## PR Size Guidelines

| Size | Files changed | Review time | When to use |
|---|---|---|---|
| **XS** | 1-3 | < 15 min | Typo, dep bump, single-line fix |
| **S** | 4-10 | 15-30 min | Small feature, focused refactor |
| **M** | 11-25 | 30-60 min | Feature with tests + docs |
| **L** | 26-50 | 1-2 hr | Large feature — consider splitting |
| **XL** | 50+ | 2+ hr | Almost always should be split |

**Stacking PRs** is preferred over large PRs. Example for a new adapter feature:

1. PR 1: interface change + stub implementations in all adapters
2. PR 2: primary adapter implementation + integration tests
3. PR 3: envd proxy wiring + E2E tests + `AGENTS.md` update

## Common CI Failures and Fixes

| Failure | Fix |
|---|---|
| `golangci-lint` fails | Run `golangci-lint run` locally; most issues are auto-fixable with `golangci-lint run --fix` |
| `go mod tidy` diff check fails | Run `go mod tidy` and commit the resulting `go.mod`/`go.sum` changes |
| `go vet` fails | Fix the reported issue (usually printf format or unreachable code) |
| `-race` test failure | Data race — add mutex, use channels, or fix shared-state access |
| `gocyclo` warning | Extract helper functions; keep cyclomatic complexity < 15 |
| E2E fails in CI only | Check Kind cluster setup; reproduce with `make kind-e2e-test` locally |
| `helm lint` fails | Run `make helm-lint` and fix chart syntax |

## Squash Merge Convention

E2BGateway uses **squash merge**. The PR title becomes the commit subject, the PR body becomes the commit body. So:

- PR title must follow Conventional Commits format
- PR body should explain the *why*, not re-list the diff
- `Closes #123` in the PR body auto-closes the issue on merge

## Dependabot

Dep bump PRs are auto-generated. When reviewing:

1. Check the upstream changelog for breaking changes
2. For `sigs.k8s.io/agent-sandbox`: verify CRD schema hasn't changed; if it has, update Helm chart + example configs
3. For `github.com/go-chi/chi/v5`: check route handler signature changes
4. For `go.uber.org/zap` / `go.opentelemetry.io/*`: usually safe; verify `make test` passes

## Labels

Currently no label taxonomy is enforced. If/when added, common labels would be:

- `area/adapter`, `area/envd`, `area/auth`, `area/routing`, `area/api`
- `type/feat`, `type/fix`, `type/chore`, `type/docs`
- `size/XS` through `size/XL`
- `do-not-merge/hold`, `do-not-merge/wip`
