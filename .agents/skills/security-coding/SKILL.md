---
name: security-coding
description: Security coding practices for E2BGateway. Use when writing or reviewing code that handles user input, shell commands, file operations, PIDs, tokens, or any security-sensitive path.
license: Apache-2.0
metadata:
  author: The E2BGateway Authors
---

# Security Coding Skill for E2BGateway

Use this skill when writing or reviewing any code that touches:

- Shell command construction
- Process IDs (PIDs)
- File read/write/upload/download
- Binary data handling
- Environment variables
- Access tokens
- User-controlled paths, names, or content

## Workflow

1. **Identify the input source** — is it from the SDK, an HTTP request, a config file, or internal?
2. **Classify the sink** — shell command, filesystem, network, log, token store?
3. **Apply the matching rule below** — never skip sanitization.
4. **Write a negative test** — verify malicious input is rejected.
5. **Write positive edge-case tests** — verify benign-but-weird input works (paths with spaces, unicode, etc.).

## Rules

### Rule 1: Shell Command Construction

**NEVER** concatenate user input directly into shell commands.

```go
// ❌ WRONG — shell injection via userInput
cmd := "mkdir -p " + userInput

// ✅ CORRECT — use shellQuote()
cmd := "mkdir -p " + shellQuote(userInput)
```

**Where to apply**: `MakeDir`, `RemoveFile`, `RemoveFiles`, `RunCommand`, `MoveFile`, any adapter method that builds a shell command.

**`shellQuote()` semantics**: wraps the value in single quotes, escaping embedded single quotes via `'\''`. See existing implementations:
- `internal/adapter/agentsandbox/adapter.go`

### Rule 2: PID Validation

**NEVER** use a PID string in a command without strict numeric validation.

```go
// ✅ CORRECT — strict numeric parse
var pid int
var extra string
n, err := fmt.Sscanf(processID, "%d%s", &pid, &extra)
if n != 1 || (err != nil && err != io.EOF) {
    return fmt.Errorf("invalid process ID: must be numeric, got %q", processID)
}
```

**Where to apply**: `KillProcess`, `GetProcess`, any method that takes a process ID from the SDK.

### Rule 3: File Writing — No Heredoc

**NEVER** use shell heredoc for writing file content — vulnerable to content injection if content contains the delimiter.

```go
// ❌ WRONG — content containing "EOF" breaks the command
cmd := fmt.Sprintf("cat > %s << 'EOF'\n%s\nEOF", path, content)

// ✅ CORRECT — binary-safe upload via SDK
return a.UploadFile(ctx, sandboxID, &adapter.FileUploadRequest{
    Path:   path,
    Reader: io.NopCloser(bytes.NewReader(content)),
})
```

**Where to apply**: `WriteFile` in every adapter.

### Rule 4: Binary Data Preservation

**NEVER** cast `[]byte` to `string` for binary data — corrupts non-UTF8 content.

```go
// ❌ WRONG — corrupts binary data
return io.NopCloser(strings.NewReader(string(data)))

// ✅ CORRECT — preserves all bytes
return io.NopCloser(bytes.NewReader(data))
```

**Where to apply**: `ReadFile`, `DownloadFile`, any method that returns file content.

### Rule 5: Environment Variable Persistence

Write to persistent files for cross-shell persistence. Use proper quoting.

```go
// ✅ CORRECT
envLines := make([]string, 0, len(envs))
for k, v := range envs {
    envLines = append(envLines, fmt.Sprintf("%s=%s", k, shellQuote(v)))
}
content := strings.Join(envLines, "\n") + "\n"
cmd := fmt.Sprintf("echo %s >> /etc/environment", shellQuote(content))
```

### Rule 6: Access Tokens

- **Token format**: `envd_{sandboxID}_{32-hex-chars}` — predictable prefix, random suffix.
- **Lifetime**: 1 hour, cached in LRU (10k entries).
- **Binding**: tokens are bound to a specific `sandboxID`; cross-sandbox validation must fail.
- **Transport**: SDK sends via `X-Access-Token` header; envd proxy validates before forwarding.
- **Rotation**: on cache miss after expiry, generate a fresh token.
- **Revocation**: kill sandbox → cache entry is orphaned (TTL handles cleanup).

### Rule 7: Token Validation in envd Proxy

The envd proxy MUST validate tokens before forwarding:

```go
// internal/server/envd_proxy.go

token := r.Header.Get("X-Access-Token")
if token == "" {
    http.Error(w, `{"code":401,"message":"missing access token"}`, http.StatusUnauthorized)
    return
}

valid, err := a.ValidateAccessToken(r.Context(), sandboxID, token)
if err != nil || !valid {
    http.Error(w, `{"code":401,"message":"invalid access token"}`, http.StatusUnauthorized)
    return
}
```

### Rule 8: Path Traversal

Validate user-supplied paths do not escape the sandbox root:

```go
// ✅ Reject absolute paths and ".." components
if filepath.IsAbs(userPath) || strings.Contains(userPath, "..") {
    return fmt.Errorf("invalid path: %q", userPath)
}
clean := filepath.Clean(userPath)
```

## Review Checklist

When reviewing code that handles user input, check:

- [ ] Shell commands use `shellQuote()` on all user-controlled parts
- [ ] PIDs are strictly numeric-validated
- [ ] File writes use binary-safe methods (no heredoc)
- [ ] Binary data is not cast through `string`
- [ ] Paths are sanitized (no `..`, no absolute paths)
- [ ] Tokens are validated in the envd proxy before forwarding
- [ ] Negative tests exist for every sanitization rule
- [ ] Error messages do not leak internal details

## Historical Security Fixes

Reference these when reviewing similar code:

| Issue | Fix | PR |
|---|---|---|
| Shell injection in MakeDir/RemoveFile/RunCommand | Added `shellQuote()` | #15 |
| Heredoc injection in WriteFile | Replaced with `UploadFile` | #15 |
| TOCTOU race in `getOrCreateExecdClient` | Double-check locking + cleanup in Kill | #15 |
| PID validation bypass | Strict `fmt.Sscanf("%d%s")` | #15 |
| envd proxy accepted requests without tokens | Added `ValidateAccessToken` call | #42 |

## Testing Security Code

```go
// Example: PID validation table-driven test
func TestKillProcess_Validation(t *testing.T) {
    tests := []struct {
        name      string
        processID string
        wantErr   bool
    }{
        {"valid PID", "1234", false},
        {"shell injection", "123; rm -rf /", true},
        {"empty", "", true},
        {"negative", "-1", false},    // negative PIDs are valid (process groups)
        {"trailing text", "123abc", true},
        {"spaces", "123 456", true},
        {"unicode", "𝟭𝟮𝟯", true},     // full-width digits rejected
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            var pid int
            var extra string
            n, err := fmt.Sscanf(tt.processID, "%d%s", &pid, &extra)
            shouldFail := n != 1 || (err != nil && err != io.EOF)
            assert.Equal(t, tt.wantErr, shouldFail)
        })
    }
}
```

See [references/REFERENCE.md](references/REFERENCE.md) for the full `shellQuote()` implementation and more examples.
