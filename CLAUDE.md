# CLAUDE.md — Claude Code Entry Point for E2BGateway

> **Read this first**: All developer guidance lives in [`AGENTS.md`](./AGENTS.md).
> This file exists only so Claude Code discovers it automatically at the repo root.

## Quick Reference

| Question | Where to look |
|---|---|
| How is the gateway structured? | [`AGENTS.md` → Architecture](./AGENTS.md#architecture) |
| What does the `SandboxAdapter` interface look like? | [`AGENTS.md` → SandboxAdapter Interface](./AGENTS.md#sandboxadapter-interface) |
| How do I add a new backend? | [`AGENTS.md` → Adding a New Backend](./AGENTS.md#adding-a-new-backend) |
| How do I build / test / run locally? | [`AGENTS.md` → Build & Development Commands](./AGENTS.md#build--development-commands) |
| How do I write a design doc? | `.agents/skills/design-doc/SKILL.md` |
| How do I submit a PR? | `.agents/skills/submit-pr/SKILL.md` |
| What are the security rules? | [`AGENTS.md` → Coding Conventions](./AGENTS.md#coding-conventions) and `.agents/skills/security-coding/SKILL.md` |
| How do I write tests? | `.agents/skills/testing/SKILL.md` |
| How do I add a new adapter? | `.agents/skills/adapter-development/SKILL.md` |

## Available Agent Skills

Skills are in `.agents/skills/`. Each contains a `SKILL.md` (instructions) and a `references/` directory (detailed reference material).

| Skill | When to use |
|---|---|
| `design-doc` | Writing a design spec or implementation plan for a new feature |
| `submit-pr` | Opening a PR — commit messages, PR body, required checks |
| `adapter-development` | Adding a new sandbox adapter backend |
| `security-coding` | Writing code that touches user input, shell commands, file ops, or tokens |
| `testing` | Writing unit, integration, or E2E tests |

## Project Snapshot

- **Language**: Go 1.26+
- **Module**: `github.com/e2bgateway/e2bgateway`
- **Entry point**: `cmd/e2bgateway/main.go` → `internal/cmd/root.go`
- **Backends**: agent-sandbox, opensandbox, e2b-cloud, mock
- **Primary CI**: `make pre-commit` (fmt + vet + lint + test)

## Claude Code Tips for This Repo

1. **Always read `AGENTS.md`** before answering architectural questions — it is the single source of truth.
2. **Security-sensitive code** (anything under `internal/adapter/`, `internal/server/envd_proxy.go`, or touching shell/file/pid paths): invoke the `security-coding` skill.
3. **Non-trivial features**: invoke the `design-doc` skill *before* writing code; pair spec + plan.
4. **Before opening a PR**: invoke the `submit-pr` skill for commit-message and PR-body conventions.
5. **Interface changes to `SandboxAdapter`**: you must update *all* four adapters (agentsandbox, opensandbox, e2bcloud, mock) — the `adapter-development` skill lists them.
6. **Config changes**: Viper lowercases map keys; adapter factories must check both `camelCase` and `lowercase` variants.
