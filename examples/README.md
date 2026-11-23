# E2BGateway examples

Run commands from the repository root. For a local HTTP gateway on port 8080, set these variables before running the examples.

```bash
export E2B_DOMAIN=localhost:8080
export E2B_API_URL=http://localhost:8080
export E2B_SANDBOX_URL=http://localhost:8080
export E2B_API_KEY=test-key
```

Go requires Go 1.26 or newer. Install the Python SDK and the dependencies for the examples you plan to run in a virtual environment. Use Node.js 20 or newer for the JavaScript examples.

```bash
python -m pip install e2b e2b-code-interpreter requests
npm ci --prefix examples/javascript
```

```bash
python examples/python/hello_world.py
node examples/javascript/hello_world.js
go run ./examples/go/hello_world
```

The Python and JavaScript SDK examples require a backend that supports the SDK operations shown. For example, `code_execution` in SDK mode requires a sandbox template with the Jupyter service. `E2B_API_URL` points the SDK and REST examples at the gateway API. `E2B_SANDBOX_URL` directs SDK data-plane requests through the local gateway. For remote deployments, set the API URL to your gateway and set the sandbox URL only if your deployment uses a shared data-plane proxy. Use HTTPS for remote gateways. See the [backend setup guide](../docs/guides/backend-setup.md) for runtime and template configuration.

## Example matrix

Each example includes its backend requirements, command, and expected output. For a scenario named `NAME`, run `go run ./examples/go/NAME`, `python examples/python/NAME.py`, or `node examples/javascript/NAME.js`.

| Scenario | What it demonstrates | Go | Python | JavaScript |
| --- | --- | :---: | :---: | :---: |
| `ai_code_interpreter` | Carry state between steps in a code workflow | ✓ | ✓ | ✓ |
| `cloud_browser` | Start a web service and expose a sandbox port | ✓ | ✓ | ✓ |
| `code_execution` | Run code and read the output. Go also supports `--stream` over WebSocket. | ✓ | ✓ | ✓ |
| `coding_agent` | Write a project and run its tests in a sandbox | ✓ | ✓ | ✓ |
| `commands` | Run commands and inspect output and exit status | ✓ | ✓ | ✓ |
| `connect_sandbox` | Reconnect to a running sandbox by ID and recover its state | ✓ | ✓ | ✓ |
| `custom_template` | Create a template, check build status, and manage an alias and a tag | ✓ | ✓ | ✓ |
| `data_analysis` | Analyze sample data inside a sandbox | ✓ | ✓ | ✓ |
| `filesystem` | Write, read, and manage sandbox files | ✓ | ✓ | ✓ |
| `hello_world` | Create a sandbox, run a greeting command, and clean up | ✓ | ✓ | ✓ |
| `sandbox_lifecycle` | Create, inspect, pause, resume, and delete a sandbox | ✓ | ✓ | ✓ |
| `templates` | Discover the templates available through the gateway | ✓ | ✓ | ✓ |
| `warm_pools` | Create, resize, and delete a warm pool | ✓ | ✓ | ✓ |
| `web_scraping` | Fetch a page and extract data inside a sandbox | ✓ | ✓ | ✓ |

Go uses [`pkg/client`](../pkg/client) for sandbox and template operations. The warm pool example calls the gateway REST API directly. For direct HTTP examples, see the [cURL examples](curl/api_examples.sh).

## Mock smoke tests

```bash
python hack/examples-smoke.py
```

The runner checks that the three languages have the same scenario names, builds the gateway, and starts an isolated mock backend on `127.0.0.1:18080`. It checks exit status and scenario output for `hello_world`, `code_execution`, `filesystem`, `templates`, and `custom_template` in all three languages, plus Go WebSocket execution, reconnection, and lifecycle. It shuts down the gateway after the run. The `Examples smoke (mock)` job runs the same script on pushes and pull requests targeting the main branches.

For `hello_world`, `code_execution`, and `filesystem`, the Python and JavaScript runner passes `--rest` to exercise the gateway's JSON endpoints with the standard library or built-in `fetch`. No SDK installation is needed for this suite. The mock echoes commands and code and performs real in-memory file writes and reads. The Kind E2E workflow exercises the SDK paths against real sandbox backends.

## Backend Configuration (agent-sandbox)

The examples work against any backend. For the agent-sandbox backend, the
gateway config looks like:

```yaml
backends:
  - name: agent-sandbox
    type: agent-sandbox
    enabled: true
    config:
      namespace: agent-sandbox-system
      # Default claim pool for templates without an explicit mapping.
      warmPoolName: base
      # Optional: E2B template ID -> SandboxTemplate resource name
      # (default: the template ID is used as the SandboxTemplate name).
      templateToSandboxTemplate:
        my-e2b-template: my-sandbox-template
      # Optional: operator-pinned template -> warm pool routing overrides.
      # These always win over pools created via the warm-pool CRUD API.
      templateToWarmPool:
        my-e2b-template: my-pool
      # OnReplenish (CRD default) or Recreate; applied to CRUD-created pools.
      warmPoolUpdateStrategy: OnReplenish
      maxWarmPoolSize: 256
```

Sandbox pods must embed the envd daemon (the gateway's only content data
plane for this backend); build the reference image with:

```bash
docker build -t sandbox-with-envd:local images/sandbox/
```
