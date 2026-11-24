# Contributing to E2BGateway

See [AGENTS.md](AGENTS.md) for the architecture, coding conventions, and test commands.

## Example checklist

- [ ] When a PR changes a user-facing API, SDK behavior, or backend capability, update the affected Go, Python, JavaScript, and TypeScript examples. This includes stdin (#35), asynchronous code execution (#106), terminal/PTY, and new backends such as CubeSandbox, AgentENV, and Agent Substrate as those capabilities land.
- [ ] Use the same scenario name in each language. Document backend prerequisites, the run command, and expected output in each entry point, and update the [example matrix](examples/README.md) when adding a scenario.
- [ ] Build the [TypeScript examples](examples/README.md#typescript-examples) and run `python hack/examples-smoke.py --typescript`. For changes to SDK or backend-specific behavior, also run the affected examples against a backend that implements it.
