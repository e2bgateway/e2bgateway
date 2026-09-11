# Port Forwarding Guide

This guide explains how to use port forwarding to access services running inside sandboxes.

## Overview

Port forwarding allows you to access web applications, APIs, and other services running inside sandbox containers. The gateway provides APIs to:

1. **List open ports** - Discover which ports are accessible in a sandbox
2. **Get port URLs** - Obtain URLs for accessing specific ports

Both `agent-sandbox` and `opensandbox` backends support port forwarding.

## API Endpoints

### List Ports

List all open ports in a sandbox:

```bash
GET /sandboxes/{sandboxID}/ports
```

**Response:**
```json
{
  "ports": [
    {"port": 3000, "ready": true},
    {"port": 8080, "ready": true}
  ]
}
```

### Get Port URL

Get the URL for accessing a specific port:

```bash
GET /sandboxes/{sandboxID}/ports/{port}
```

**Response:**
```json
{
  "url": "http://10.244.0.7:3000"
}
```

## Usage Examples

### Python SDK

```python
from e2b import Sandbox

# Create a sandbox
sandbox = Sandbox(template="code-interpreter")

# Start a web server in the background
sandbox.commands.run("python -m http.server 3000 &")

# List open ports
ports = sandbox.ports.list()
print(f"Open ports: {[p.port for p in ports]}")

# Get URL for port 3000
url = sandbox.ports.get_url(3000)
print(f"Access your app at: {url}")
```

### JavaScript SDK

```javascript
import { Sandbox } from 'e2b';

// Create a sandbox
const sandbox = await Sandbox.create({ template: 'code-interpreter' });

// Start a web server
await sandbox.commands.run('python -m http.server 3000 &');

// List open ports
const ports = await sandbox.ports.list();
console.log('Open ports:', ports.map(p => p.port));

// Get URL for port 3000
const url = await sandbox.ports.getUrl(3000);
console.log(`Access your app at: ${url}`);
```

### cURL

```bash
# List ports
curl -H "X-API-Key: your-api-key" \
  http://localhost:8080/sandboxes/sbx-123/ports

# Get port URL
curl -H "X-API-Key: your-api-key" \
  http://localhost:8080/sandboxes/sbx-123/ports/3000
```

## Backend-Specific Behavior

### agent-sandbox (Kubernetes)

For agent-sandbox, port URLs are constructed using the Pod's IP address:

```
http://<pod-ip>:<port>
```

**Requirements:**
- The gateway must have network access to Pod IPs
- Pods must be running and have assigned IPs
- Ports must be listening inside the container

### OpenSandbox

For OpenSandbox, port URLs are provided by the OpenSandbox server's endpoint API:

```
http://<opensandbox-endpoint>:<port>
```

**Note:** OpenSandbox does not provide a native API to list all open ports. The gateway tracks ports that have been accessed via `GetPortURL`, so `ListPorts` returns only ports that have been explicitly requested.

## Port Tracking

The gateway maintains a port tracker per sandbox:

- **agent-sandbox**: Tracks ports accessed via `GetPortURL`
- **opensandbox**: Tracks ports accessed via `GetPortURL`

When you call `GetPortURL` for a port, it is automatically added to the tracker and will appear in subsequent `ListPorts` calls.

## Common Use Cases

### Web Application Development

```python
# Start a React dev server
sandbox.commands.run("npm run dev &")

# Get the URL
url = sandbox.ports.get_url(3000)
print(f"Open {url} in your browser")
```

### Jupyter Notebook

```python
# Start Jupyter
sandbox.commands.run("jupyter notebook --port=8888 --no-browser &")

# Get the URL
url = sandbox.ports.get_url(8888)
print(f"Access Jupyter at: {url}")
```

### API Server

```python
# Start an API server
sandbox.commands.run("python app.py &")

# Get the URL
url = sandbox.ports.get_url(8080)
print(f"API endpoint: {url}")
```

## Troubleshooting

### Port Not Appearing in ListPorts

If a port doesn't appear in `ListPorts`:

1. **Call GetPortURL first**: The port must be accessed via `GetPortURL` to be tracked
2. **Verify the service is running**: Ensure the service is actually listening on the port inside the sandbox
3. **Check sandbox status**: The sandbox must be in "running" state

### Cannot Access Port URL

If you cannot access the URL returned by `GetPortURL`:

1. **Network connectivity**: Ensure your machine can reach the Pod IP (for agent-sandbox) or OpenSandbox endpoint
2. **Firewall rules**: Check that firewall rules allow traffic to the port
3. **Service binding**: Ensure the service is binding to `0.0.0.0` (not `127.0.0.1`)

### Port Not Ready

If a port shows `ready: false`:

1. **Wait for startup**: The service may still be starting up
2. **Check logs**: Review sandbox logs to see if the service started successfully
3. **Verify port**: Ensure the service is listening on the expected port

## Configuration

Port forwarding is automatically enabled when using backends that support it. No additional configuration is required.

For Helm deployments, you can configure common ports in `values.yaml`:

```yaml
portForwarding:
  enabled: true
  commonPorts:
    - 3000  # Web applications
    - 8080  # HTTP servers
    - 8888  # Jupyter notebooks
```

## Security Considerations

- Port URLs are internal network addresses and should not be exposed publicly
- Use proper authentication (API keys, access tokens) when accessing sandbox services
- Consider using ingress controllers with TLS termination for production deployments
- Sandbox ports are isolated per sandbox and cannot be accessed by other sandboxes

## Related Documentation

- [Getting Started](./getting-started.md)
- [Backend Setup](./backend-setup.md)
- [E2B API Documentation](https://e2b.dev/docs)
