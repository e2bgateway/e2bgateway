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

## Template Mapping

OpenSandbox uses container images directly. The `templateID` in E2B API maps to the container image URI:

```go
// Create a Python sandbox
sandbox, err := adapter.CreateSandbox(ctx, &adapter.CreateSandboxRequest{
    TemplateID: "python:3.12",
})
```

## Differences from E2B

1. **No Templates**: OpenSandbox doesn't have a template concept. Templates map directly to container images.
2. **Sandbox IDs**: Uses OpenSandbox's native sandbox IDs instead of generating E2B-compatible IDs.
3. **Execution Context**: Code execution uses RunCommand with language interpreters rather than native execution contexts.

## Error Handling

The adapter maps OpenSandbox errors to standard Go errors. API errors include HTTP status codes and error messages from the OpenSandbox API.

## Limitations

- File operations use shell commands when native APIs are not available
- No support for OpenSandbox's advanced features (network policies, credential vaults) in the basic adapter interface
- Execution timeout is fixed at 30 seconds (adapter interface doesn't support timeout configuration)
