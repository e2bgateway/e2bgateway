# OpenSandbox Adapter

This adapter integrates [alibaba/OpenSandbox](https://github.com/alibaba/OpenSandbox) as a backend for E2BGateway.

## Architecture

The adapter uses two OpenSandbox clients:

- **LifecycleClient**: Manages sandbox lifecycle (create, list, get, delete, pause, resume)
- **ExecdClient**: Handles code execution, command execution, and file operations inside sandboxes

## Configuration

Add the OpenSandbox adapter to your E2BGateway configuration:

```yaml
backends:
  - name: opensandbox
    type: opensandbox
    enabled: true
    config:
      baseURL: "http://opensandbox-lifecycle:8080/v1"
      apiKey: "your-api-key"
      execdURL: "http://opensandbox-execd:9090"
      execdToken: "your-execd-token"
```

### Configuration Fields

- `baseURL` (required): OpenSandbox Lifecycle API endpoint
- `apiKey` (optional): API key for authentication
- `execdURL` (optional): OpenSandbox Execd API endpoint for command execution
- `execdToken` (optional): Token for Execd API authentication

## Features

### Sandbox Lifecycle

- **Create**: Creates a new sandbox from a container image
- **List**: Lists all sandboxes with pagination
- **Get**: Gets sandbox details by ID
- **Delete**: Terminates a sandbox
- **Pause**: Pauses a running sandbox
- **Resume**: Resumes a paused sandbox
- **SetTimeout**: Extends sandbox expiration time

### Code Execution

Execute code in multiple languages:

```go
result, err := adapter.ExecuteCode(ctx, sandboxID, &adapter.CodeExecutionRequest{
    Code:     "print('Hello from OpenSandbox!')",
    Language: "python",
})
```

Supported languages:
- Python (default)
- JavaScript/Node.js
- Bash/Shell
- Any language with a CLI interpreter

### Command Execution

Run shell commands with streaming output:

```go
result, err := adapter.RunCommand(ctx, sandboxID, &adapter.CommandRequest{
    Command: "ls",
    Args:    []string{"-la"},
})
```

### File Operations

- **WriteFile**: Write content to a file
- **ReadFile**: Read file content
- **UploadFile**: Upload a file from io.Reader
- **DownloadFile**: Download a file as io.ReadCloser
- **ListFiles**: List directory contents
- **MakeDir**: Create directories
- **RemoveFile**: Delete files or directories

## Template Management

OpenSandbox uses container images directly and has no built-in template concept. The gateway provides a **TemplateStore abstraction layer** so E2B clients can manage templates uniformly across backends.

### Template Store

Templates, builds, aliases, and tags are stored in a pluggable `TemplateStore`:

| Implementation | Description | Persistence |
|---|---|---|
| `MemoryTemplateStore` (default) | In-process Go maps, zero external deps | Lost on restart |
| `RedisTemplateStore` | Redis-backed, shared across instances | Survives restart |
| *(future)* `EtcdTemplateStore` | etcd-backed | Survives restart |

The store is selected via config:

```yaml
backends:
  - name: opensandbox
    type: opensandbox
    config:
      baseURL: "http://opensandbox-lifecycle:8080/v1"
      templateStore:
        type: memory            # memory | redis
        # Redis config (when type: redis):
        # addr: "redis:6379"
        # password: ""
        # db: 0
        # keyPrefix: "e2bgateway:opensandbox:"
```

### Template → Image Resolution

`CreateSandbox` resolves the `TemplateID` through a 4-level precedence:

1. Static `templateToImage` config map (config-level overrides)
2. Gateway-managed template store (by `templateID`)
3. Alias → `templateID` → image URI resolution
4. Fall back to using `TemplateID` directly as the image URI

This means you can `CreateTemplate` with a Dockerfile, tag the resulting image, and then use either the template ID, alias, or tag to spawn a sandbox.

### Template CRUD

```go
// Create template (image URI derived from Dockerfile's last FROM directive)
build, err := adapter.CreateTemplate(ctx, &adapter.CreateTemplateRequest{
    Name:       "my-python",
    Dockerfile: "FROM python:3.11-slim\nRUN pip install requests",
    StartCmd:   "python -m http.server",
    CPUCount:   2,
    MemoryMB:   1024,
})

// Trigger a new build (updates image URI + startCmd)
newBuild, err := adapter.TriggerBuild(ctx, build.TemplateID, &adapter.BuildRequest{
    Dockerfile: "FROM alpine:3.20",
    StartCmd:   "/bin/sh",
})

// Create aliases and tags
_ = adapter.CreateAlias(ctx, build.TemplateID, "latest")
_, _ = adapter.CreateTag(ctx, build.TemplateID, &adapter.TagRequest{
    Name:    "v1.0",
    BuildID: build.BuildID,
})

// Spawn a sandbox by template ID, alias, or raw image URI
sandbox, _ := adapter.CreateSandbox(ctx, &adapter.CreateSandboxRequest{
    TemplateID: "latest",  // alias → template → image URI
})
```

## Differences from E2B

1. **Template abstraction layer**: OpenSandbox uses container images directly, but the gateway provides full E2B-compatible template CRUD (create/delete), build management, aliases, and tags via the `TemplateStore`. The image URI for a template is derived from the Dockerfile's **last** `FROM` directive (supporting multi-stage builds).
2. **Sandbox IDs**: Uses OpenSandbox's native sandbox IDs instead of generating E2B-compatible IDs.
3. **Execution Context**: Code execution uses RunCommand with language interpreters rather than native execution contexts.

## Error Handling

The adapter maps OpenSandbox errors to standard Go errors. API errors include HTTP status codes and error messages from the OpenSandbox API.

TemplateStore operations return sentinel errors (`ErrTemplateNotFound`, `ErrBuildNotFound`, `ErrAliasNotFound`, `ErrTagNotFound`) that callers can check with `errors.Is()`.

## Limitations

- `MemoryTemplateStore` loses all data on gateway restart — use `RedisTemplateStore` for production
- Execution timeout is fixed at 30 seconds (adapter interface doesn't support timeout configuration)
- No native build pipeline — builds complete synchronously and the image URI is resolved from the Dockerfile's `FROM` directive

## Implementation

- `adapter.go`: Main adapter implementation
- `factory.go`: Config parsing (including `templateStore` selection)
- `templatestore.go`: `TemplateStore` interface + `TemplateEntry` type + sentinel errors
- `memory_store.go`: In-memory `TemplateStore` implementation
- `redis_store.go`: Redis-backed `TemplateStore` implementation
- `templatestore_test.go`: Contract test suite (runs against any implementation)
- `redis_store_test.go`: Redis tests using `miniredis`
