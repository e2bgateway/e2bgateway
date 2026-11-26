## What does this PR do?

<!-- One or two sentences describing the change. -->

## Related issues

<!-- e.g. Closes #123 -->

## Checklist

- [ ] Tests written first (TDD); each new test was watched failing before the implementation landed
- [ ] Design doc committed in this branch: `docs/superpowers/specs/YYYY-MM-DD-<topic>-design.md` (trivial changes must state why they are exempt)
- [ ] All local gates pass: `make fmt`, `make vet`, `make lint`, `make openapi-check`, `make test`, `make build`
- [ ] New or changed HTTP routes are reflected in `docs/api/openapi.yaml` — `make openapi-check` passes. Internal-only routes must be added to the exemption list in `internal/server/openapi_drift_test.go` with a reason.
- [ ] Docs updated if behavior or config changed (`AGENTS.md`, `configs/e2bgateway-example.yaml`)
- [ ] Commit messages follow Conventional Commits (see `.agents/skills/submit-pr`)
