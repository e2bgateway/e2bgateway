# cURL Examples - E2BGateway API

All examples assume:
```bash
export GATEWAY_URL=http://localhost:8080
export E2B_API_KEY=test-key
```

## Sandbox Lifecycle

### Create Sandbox
```bash
curl -X POST "$GATEWAY_URL/sandboxes" \
  -H "X-API-Key: $E2B_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "templateID": "python:3.11-slim",
    "timeout": 300
  }'
```

Expected response:
```json
{
  "sandboxID": "abc123",
  "templateID": "python:3.11-slim",
  "clientID": "client-1",
  "envdVersion": "0.1.0",
  "envdAccessToken": "envd_abc123_..."
}
```

### List Sandboxes
```bash
curl "$GATEWAY_URL/sandboxes" \
  -H "X-API-Key: $E2B_API_KEY"
```

### Get Sandbox
```bash
curl "$GATEWAY_URL/sandboxes/{sandboxID}" \
  -H "X-API-Key: $E2B_API_KEY"
```

### Pause Sandbox
```bash
curl -X POST "$GATEWAY_URL/sandboxes/{sandboxID}/pause" \
  -H "X-API-Key: $E2B_API_KEY"
```

### Resume Sandbox
```bash
curl -X POST "$GATEWAY_URL/sandboxes/{sandboxID}/resume" \
  -H "X-API-Key: $E2B_API_KEY"
```

### Set Timeout
```bash
curl -X POST "$GATEWAY_URL/sandboxes/{sandboxID}/timeout" \
  -H "X-API-Key: $E2B_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"timeout": 600}'
```

### Kill Sandbox
```bash
curl -X DELETE "$GATEWAY_URL/sandboxes/{sandboxID}" \
  -H "X-API-Key: $E2B_API_KEY"
```

## Code Execution

### Execute Code
```bash
curl -X POST "$GATEWAY_URL/sandboxes/{sandboxID}/code" \
  -H "X-API-Key: $E2B_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "code": "print(\"Hello from sandbox!\")",
    "language": "python"
  }'
```

## Commands

### Run Command
```bash
curl -X POST "$GATEWAY_URL/sandboxes/{sandboxID}/commands" \
  -H "X-API-Key: $E2B_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "cmd": "echo Hello World",
    "cwd": "/tmp"
  }'
```

### List Processes
```bash
curl "$GATEWAY_URL/sandboxes/{sandboxID}/commands" \
  -H "X-API-Key: $E2B_API_KEY"
```

## Filesystem

### Upload File
```bash
curl -X POST "$GATEWAY_URL/sandboxes/{sandboxID}/filesystem/upload" \
  -H "X-API-Key: $E2B_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "path": "/tmp/hello.txt",
    "content": "Hello World!"
  }'
```

### Download File
```bash
curl "$GATEWAY_URL/sandboxes/{sandboxID}/filesystem/download?path=/tmp/hello.txt" \
  -H "X-API-Key: $E2B_API_KEY"
```

### List Files
```bash
curl -X POST "$GATEWAY_URL/sandboxes/{sandboxID}/filesystem/list" \
  -H "X-API-Key: $E2B_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"path": "/tmp"}'
```

### Create Directory
```bash
curl -X POST "$GATEWAY_URL/sandboxes/{sandboxID}/filesystem/mkdir" \
  -H "X-API-Key: $E2B_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"path": "/tmp/my_project"}'
```

### Remove File
```bash
curl -X POST "$GATEWAY_URL/sandboxes/{sandboxID}/filesystem/rm" \
  -H "X-API-Key: $E2B_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"path": "/tmp/hello.txt"}'
```

### Move File
```bash
curl -X POST "$GATEWAY_URL/sandboxes/{sandboxID}/filesystem/move" \
  -H "X-API-Key: $E2B_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "source": "/tmp/old.txt",
    "destination": "/tmp/new.txt"
  }'
```

## Environment Variables

### Set Environment Variables
```bash
curl -X POST "$GATEWAY_URL/sandboxes/{sandboxID}/envs" \
  -H "X-API-Key: $E2B_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "envs": {"MY_VAR": "hello", "MY_KEY": "value"}
  }'
```

## Templates

### List Templates
```bash
curl "$GATEWAY_URL/templates" \
  -H "X-API-Key: $E2B_API_KEY"
```

### Get Template
```bash
curl "$GATEWAY_URL/templates/base" \
  -H "X-API-Key: $E2B_API_KEY"
```

### Template Tags
```bash
# List tags
curl "$GATEWAY_URL/templates/base/tags" \
  -H "X-API-Key: $E2B_API_KEY"

# Create tag
curl -X POST "$GATEWAY_URL/templates/base/tags" \
  -H "X-API-Key: $E2B_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"name": "v1.0", "buildID": "build-123"}'

# Delete tag
curl -X DELETE "$GATEWAY_URL/templates/base/tags/v1.0" \
  -H "X-API-Key: $E2B_API_KEY"
```

## V2 API

### V2 List Sandboxes
```bash
curl "$GATEWAY_URL/v2/sandboxes" \
  -H "X-API-Key: $E2B_API_KEY"
```

### V2 Get Logs
```bash
curl "$GATEWAY_URL/v2/sandboxes/{sandboxID}/logs" \
  -H "X-API-Key: $E2B_API_KEY"
```

### V2 Get Metrics
```bash
curl "$GATEWAY_URL/v2/sandboxes/{sandboxID}/metrics" \
  -H "X-API-Key: $E2B_API_KEY"
```

## Error Format

All errors follow the E2B format:
```json
{
  "code": 404,
  "message": "Sandbox 'abc123' not found"
}
```

## Health Checks
```bash
# Health check
curl "$GATEWAY_URL/healthz"

# Readiness check
curl "$GATEWAY_URL/readyz"
```
