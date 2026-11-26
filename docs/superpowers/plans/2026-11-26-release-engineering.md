# Release Engineering (Issue #120) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship multi-arch image hardening, SPDX SBOMs, cosign keyless signing, commit/version injection (`--version` + `GET /versionz`), helm digest pinning, and release-verification docs.

**Architecture:** Keep the existing buildx-based `release.yml` (already multi-arch). Harden the Dockerfile (build-args version injection, `$BUILDPLATFORM` cross-compile, digest-pinned bases), add SBOM + cosign steps signing both images by digest, and plumb ldflags build info from `main.go` through cobra into a new unauthenticated `/versionz` handler.

**Tech Stack:** Go 1.26+ / cobra / chi, docker buildx, anchore/sbom-action (syft), sigstore/cosign (keyless), Helm 3.

**Spec:** `docs/superpowers/specs/2026-11-26-release-engineering-design.md`

## Global Constraints

- Branch: `feat/120-release-engineering` (already created from `origin/main`, spec committed).
- Every commit uses `git commit -s` (DCO sign-off, repo requirement).
- New `.go` files start with the Apache license header — copy lines 1–13 of `internal/cmd/version.go` verbatim.
- `make pre-commit` (fmt + vet + lint + test) must pass before the final commit; `make openapi-check` must pass after any route change (CI enforces openapi.yaml ↔ chi route consistency, #133).
- rc flow unchanged: tags containing `-` stay prerelease, no `latest`/`major`/`minor` tags for them.
- Do NOT migrate to ko/goreleaser; do NOT sign binaries; do NOT change Makefile `docker-build` to multi-arch.
- Viper lowercases map keys (not relevant here — no config-map changes).
- Error format for API handlers: `writeJSON` / `writeError` helpers in `internal/api/v1/handlers.go`.

---

### Task 1: CLI version injection (commit + buildDate + Go version)

**Files:**
- Modify: `cmd/e2bgateway/main.go:24-30`
- Modify: `internal/cmd/root.go:22-41`
- Modify: `internal/cmd/version.go:23-31`
- Modify: `Makefile:17`
- Test: `internal/cmd/version_test.go` (create)

**Interfaces:**
- Consumes: nothing (first task).
- Produces: `cmd.NewRootCommand(version, commit, buildDate string) *cobra.Command` (3 params now); `newVersionCommand(version, commit, buildDate string) *cobra.Command`; ldflags targets `main.version`, `main.commit`, `main.buildDate`. Task 2 adds `newServeCommand(info v1.BuildInfo)` called from `NewRootCommand`.

- [ ] **Step 1: Write the failing test**

Create `internal/cmd/version_test.go` (license header first, copied from `internal/cmd/version.go:1-13`):

```go
package cmd

import (
	"bytes"
	"runtime"
	"strings"
	"testing"
)

func TestVersionCommandOutput(t *testing.T) {
	cmd := newVersionCommand("v9.9.9", "abc1234", "2026-11-26T00:00:00Z")
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute version command: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"v9.9.9", "abc1234", "2026-11-26T00:00:00Z", runtime.Version()} {
		if !strings.Contains(out, want) {
			t.Errorf("version output %q missing %q", out, want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cmd/ -run TestVersionCommandOutput -v`
Expected: compile error — `newVersionCommand` takes 2 args, called with 3.

- [ ] **Step 3: Implement**

`internal/cmd/version.go` — replace the function (add `runtime` import, keep `fmt`):

```go
func newVersionCommand(version, commit, buildDate string) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintf(cmd.OutOrStdout(), "E2BGateway %s (commit %s, built %s, %s)\n",
				version, commit, buildDate, runtime.Version())
		},
	}
}
```

`internal/cmd/root.go` — change signature and call:

```go
func NewRootCommand(version, commit, buildDate string) *cobra.Command {
```

and inside: `cmd.AddCommand(newVersionCommand(version, commit, buildDate))`
(leave `newServeCommand()` unchanged in this task; Task 2 updates it).

`cmd/e2bgateway/main.go` — vars and call:

```go
var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

func main() {
	rootCmd := cmd.NewRootCommand(version, commit, buildDate)
```

Check for other callers: `grep -rn "NewRootCommand(" --include="*.go" .` — only `main.go` should call it; fix any other caller the same way.

`Makefile:17` — replace LDFLAGS:

```makefile
LDFLAGS := -ldflags "-X main.version=$(DOCKER_TAG) -X main.commit=$(shell git rev-parse --short HEAD 2>/dev/null || echo unknown) -X main.buildDate=$(shell date -u +%Y-%m-%dT%H:%M:%SZ)"
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/cmd/ -v && go build ./...`
Expected: PASS, build clean.

Run: `make build-local && ./bin/e2bgateway version && ./bin/e2bgateway --version`
Expected: `version` prints tag/commit/date/Go version; `--version` prints the cobra version line.

- [ ] **Step 5: Commit**

```bash
git add cmd/e2bgateway/main.go internal/cmd/root.go internal/cmd/version.go internal/cmd/version_test.go Makefile
git commit -s -m "feat(cli): inject commit and build date into version output (#120)"
```

---

### Task 2: `GET /versionz` endpoint

**Files:**
- Create: `internal/api/v1/version.go`
- Test: `internal/api/v1/version_test.go` (create)
- Modify: `internal/server/http.go` (Server struct ~line 54, `New` ~line 74, `buildRouter` health block ~line 353)
- Modify: `internal/server/middleware/middleware.go:34-38,133,173,204`
- Modify: `internal/cmd/serve.go:28-37,40,57`
- Modify: `internal/cmd/root.go:37`
- Modify: `docs/api/openapi.yaml` (after `/readyz` block ~line 101; schema after `HealthResponse` ~line 1621)
- Test: `internal/server/server_test.go` (append)

**Interfaces:**
- Consumes: Task 1's `NewRootCommand(version, commit, buildDate string)`.
- Produces: `v1.BuildInfo{Version, Commit, BuildDate string}`; `v1.VersionHandler(info BuildInfo) http.HandlerFunc`; `server.Option func(*Server)`; `server.WithBuildInfo(info v1.BuildInfo) Option`; `server.New(cfg *config.Config, opts ...Option)` (variadic — all 6 existing test callers keep compiling); `newServeCommand(info v1.BuildInfo) *cobra.Command`.

- [ ] **Step 1: Write the failing handler test**

Create `internal/api/v1/version_test.go` (license header first):

```go
package v1

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
)

func TestVersionHandler(t *testing.T) {
	h := VersionHandler(BuildInfo{Version: "v1.2.3", Commit: "abc1234", BuildDate: "2026-11-26T00:00:00Z"})
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/versionz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := map[string]string{
		"version":   "v1.2.3",
		"commit":    "abc1234",
		"buildDate": "2026-11-26T00:00:00Z",
		"goVersion": runtime.Version(),
	}
	for k, v := range want {
		if body[k] != v {
			t.Errorf("body[%q] = %q, want %q", k, body[k], v)
		}
	}
}

func TestVersionHandlerDefaults(t *testing.T) {
	h := VersionHandler(BuildInfo{})
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/versionz", nil))

	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["version"] != "dev" || body["commit"] != "unknown" || body["buildDate"] != "unknown" {
		t.Errorf("expected dev/unknown defaults, got %v", body)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/api/v1/ -run TestVersionHandler -v`
Expected: compile error — `VersionHandler` / `BuildInfo` undefined.

- [ ] **Step 3: Implement the handler**

Create `internal/api/v1/version.go` (license header first):

```go
package v1

import (
	"net/http"
	"runtime"
)

// BuildInfo carries ldflags-injected build metadata exposed by GET /versionz.
type BuildInfo struct {
	Version   string
	Commit    string
	BuildDate string
}

// VersionHandler handles GET /versionz. Like /healthz it requires no auth.
// Empty fields fall back to dev/unknown so plain `go build` stays useful.
func VersionHandler(info BuildInfo) http.HandlerFunc {
	if info.Version == "" {
		info.Version = "dev"
	}
	if info.Commit == "" {
		info.Commit = "unknown"
	}
	if info.BuildDate == "" {
		info.BuildDate = "unknown"
	}
	resp := map[string]string{
		"version":   info.Version,
		"commit":    info.Commit,
		"buildDate": info.BuildDate,
		"goVersion": runtime.Version(),
	}
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, resp)
	}
}
```

Run: `go test ./internal/api/v1/ -run TestVersionHandler -v` → PASS.

- [ ] **Step 4: Wire the server (failing route test first)**

Append to `internal/server/server_test.go`:

```go
func TestServerVersionEndpoint(t *testing.T) {
	srv, err := server.New(newTestConfig(false), server.WithBuildInfo(v1.BuildInfo{
		Version: "v9.9.9", Commit: "abc1234", BuildDate: "2026-11-26T00:00:00Z",
	}))
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/versionz")
	if err != nil {
		t.Fatalf("GET /versionz error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["version"] != "v9.9.9" || body["commit"] != "abc1234" {
		t.Errorf("unexpected version body: %v", body)
	}
}
```

Add import `"github.com/e2bgateway/e2bgateway/internal/api/v1"` to that test file.

Run: `go test ./internal/server/ -run TestServerVersionEndpoint -v`
Expected: compile error — `server.WithBuildInfo` undefined.

- [ ] **Step 5: Implement server wiring**

`internal/server/http.go`:

1. Add field to `Server` struct (after `sessionStore`): `buildInfo v1.BuildInfo`
2. Add above `New`:

```go
// Option customizes a Server at construction time.
type Option func(*Server)

// WithBuildInfo sets the build metadata exposed by GET /versionz.
func WithBuildInfo(info v1.BuildInfo) Option {
	return func(s *Server) { s.buildInfo = info }
}
```

3. Change `New` signature to `func New(cfg *config.Config, opts ...Option) (*Server, error)` and apply options right after `s := &Server{cfg: cfg}`:

```go
	for _, opt := range opts {
		opt(s)
	}
```

4. In `buildRouter`, after the `/readyz` line:

```go
	r.Get("/versionz", v1.VersionHandler(s.buildInfo))
```

`internal/server/middleware/middleware.go` — exempt `/versionz` exactly like `/healthz`:

1. Const block: add `versionzPath = "/versionz"`
2. Auth switch (~line 133): `case healthzPath, readyzPath, versionzPath, metricsPath:`
3. RateLimit (~line 173) and AuditLog (~line 204): append `|| r.URL.Path == versionzPath` to each condition.

- [ ] **Step 6: Wire the CLI serve path**

`internal/cmd/serve.go`:

```go
func newServeCommand(info v1.BuildInfo) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the E2BGateway server",
		Long:  "Start the E2BGateway HTTP/WebSocket server with configured backends.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServe(cmd, args, info)
		},
	}
	return cmd
}

func runServe(cmd *cobra.Command, args []string, info v1.BuildInfo) error {
```

and change the server construction line to:

```go
	srv, err := server.New(cfg, server.WithBuildInfo(info))
```

Add import `"github.com/e2bgateway/e2bgateway/internal/api/v1"`.

`internal/cmd/root.go`: add the same v1 import and change the serve registration:

```go
	cmd.AddCommand(newServeCommand(v1.BuildInfo{Version: version, Commit: commit, BuildDate: buildDate}))
```

- [ ] **Step 7: Update openapi.yaml**

Insert after the `/readyz` path block (before the next section comment):

```yaml
  /versionz:
    get:
      tags: [Health]
      operationId: versionz
      summary: Build version information
      description: Returns the gateway build version, commit, build date and Go version. No authentication required.
      security: []
      responses:
        "200":
          description: Build information
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/VersionResponse"
```

Insert after the `HealthResponse` schema in `components/schemas`:

```yaml
    VersionResponse:
      type: object
      properties:
        version:
          type: string
          example: v0.2.0
        commit:
          type: string
          example: 0a084d4
        buildDate:
          type: string
          example: "2026-11-26T00:00:00Z"
        goVersion:
          type: string
          example: go1.26.0
```

- [ ] **Step 8: Run all checks**

Run: `go test ./internal/server/ ./internal/api/v1/ ./internal/cmd/ -count=1 && make openapi-check`
Expected: PASS including `TestOpenAPISpecMatchesRoutes` (no drift, no exemption needed for `/versionz`).

- [ ] **Step 9: Commit**

```bash
git add internal/api/v1/version.go internal/api/v1/version_test.go internal/server/http.go internal/server/middleware/middleware.go internal/server/server_test.go internal/cmd/serve.go internal/cmd/root.go docs/api/openapi.yaml
git commit -s -m "feat(server): expose build metadata via unauthenticated GET /versionz (#120)"
```

---

### Task 3: Dockerfile hardening (cross-compile, build-args, digest pinning)

**Files:**
- Modify: `Dockerfile` (full rewrite of build stage; runtime stage keeps labels/user/entrypoint)

**Interfaces:**
- Consumes: Task 1's ldflags targets `main.version`, `main.commit`, `main.buildDate`.
- Produces: build-args `VERSION`, `COMMIT`, `BUILD_DATE`, `GOPROXY` (Task 4 passes them from CI).

- [ ] **Step 1: Resolve base-image digests**

Run (docker available):

```bash
docker buildx imagetools inspect golang:1.27.3 --format '{{.Manifest.Digest}}'
docker buildx imagetools inspect alpine:3.24 --format '{{.Manifest.Digest}}'
```

Fallback if docker is unavailable (Docker Hub registry API):

```bash
TOKEN=$(curl -s "https://auth.docker.io/token?service=registry.docker.io&scope=repository:library/golang:pull" | jq -r .token)
curl -sI -H "Authorization: Bearer $TOKEN" \
  -H "Accept: application/vnd.oci.image.index.v1+json,application/vnd.docker.distribution.manifest.list.v2+json" \
  "https://registry-1.docker.io/v2/library/golang/manifests/1.27.3" | grep -i docker-content-digest
```

(same for `library/alpine` / `3.24`). Record both `sha256:...` values; use them in Step 2. Keep the existing tags (`golang:1.27.3`, `alpine:3.24`) — do not bump versions in this PR.

- [ ] **Step 2: Rewrite the Dockerfile**

Replace the whole file with (substitute the two resolved digests for `<GOLANG_DIGEST>` / `<ALPINE_DIGEST>`):

```dockerfile
# Build stage — cross-compile on the builder's native platform ($BUILDPLATFORM)
# so arm64 builds never run the Go compiler under QEMU emulation.
FROM --platform=$BUILDPLATFORM golang:1.27.3@sha256:<GOLANG_DIGEST> AS builder

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
# Overridable Go module proxy; default keeps the China-friendly mirror.
ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY}

WORKDIR /app

# Cache dependencies
COPY go.mod go.sum ./
RUN go mod download

# Build binary
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildDate=${BUILD_DATE}" \
    -o bin/e2bgateway ./cmd/e2bgateway

# Runtime stage
FROM alpine:3.24@sha256:<ALPINE_DIGEST>

LABEL org.opencontainers.image.source="https://github.com/e2bgateway/e2bgateway"
LABEL org.opencontainers.image.description="E2B-compatible API Gateway for AI Agent Sandboxes"
LABEL org.opencontainers.image.licenses="Apache-2.0"

RUN adduser -D -u 65532 nonroot

WORKDIR /

COPY --from=builder /app/bin/e2bgateway /e2bgateway

USER 65532:65532

EXPOSE 8080 8443 9090

ENTRYPOINT ["/e2bgateway"]
CMD ["serve", "--config", "/etc/e2bgateway/config.yaml"]
```

Notes: the `apt-get install git make` layer is dropped (no more in-image `git describe`); plain `docker build` (no buildx) leaves `TARGETARCH` empty → native arch, still works.

- [ ] **Step 3: Verify multi-arch build**

Run:

```bash
docker buildx build --platform linux/amd64,linux/arm64 \
  --build-arg VERSION=v0.0.0-buildx-test \
  --build-arg COMMIT=$(git rev-parse --short HEAD) \
  --build-arg BUILD_DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ) \
  -t e2bgateway:buildx-test .
```

Expected: both platforms build successfully (arm64 compiles via cross-compile, no QEMU Go execution). If docker/buildx is unavailable in this environment, record that in the PR description and rely on CI; do not claim local verification.

- [ ] **Step 4: Verify version injection in the image**

Run:

```bash
docker buildx build --load \
  --build-arg VERSION=v0.0.0-buildx-test --build-arg COMMIT=$(git rev-parse --short HEAD) \
  --build-arg BUILD_DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ) \
  -t e2bgateway:buildx-test .
docker run --rm e2bgateway:buildx-test version
```

Expected: output contains `v0.0.0-buildx-test` and the short commit hash.

- [ ] **Step 5: Commit**

```bash
git add Dockerfile
git commit -s -m "build(docker): cross-compile via BUILDPLATFORM, inject version build-args, pin base digests (#120)"
```

---

### Task 4: release.yml — SBOM, cosign keyless, latest tag, binary ldflags

**Files:**
- Modify: `.github/workflows/release.yml`

**Interfaces:**
- Consumes: Task 3's Dockerfile build-args (`VERSION`, `COMMIT`, `BUILD_DATE`, `GOPROXY`); Task 1's `main.commit` ldflags target.
- Produces: release assets `e2bgateway_<tag>_sbom.spdx.json`, `sandbox-base_<tag>_sbom.spdx.json`; cosign signatures on both image digests; `:latest` tag for non-rc releases.

- [ ] **Step 1: Apply workflow edits**

1. Top-level `permissions` — add keyless signing permission:

```yaml
permissions:
  contents: write
  packages: write
  id-token: write
```

2. `release` job → "Build binaries" step — replace the `go build` invocation:

```bash
            CGO_ENABLED=0 GOOS=$GOOS GOARCH=$GOARCH \
              go build -ldflags="-s -w -X main.version=${GITHUB_REF_NAME} -X main.commit=${GITHUB_SHA} -X main.buildDate=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
              -o "$OUTPUT" ./cmd/e2bgateway
```

3. `docker` job — after the GHCR login step, add:

```yaml
      - name: Set build metadata
        id: buildmeta
        run: echo "BUILD_DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ)" >> "$GITHUB_OUTPUT"
```

4. Both metadata steps (`gateway_meta`, `sandbox_meta`) — append to `tags:`:

```yaml
            type=raw,value=latest,enable=${{ !contains(github.ref_name, '-') }}
```

5. "Build and push E2BGateway image" step — add `id: gateway_build` and build-args:

```yaml
      - name: Build and push E2BGateway image
        id: gateway_build
        uses: docker/build-push-action@v7
        with:
          context: .
          push: true
          platforms: linux/amd64,linux/arm64
          tags: ${{ steps.gateway_meta.outputs.tags }}
          labels: ${{ steps.gateway_meta.outputs.labels }}
          build-args: |
            VERSION=${{ github.ref_name }}
            COMMIT=${{ github.sha }}
            BUILD_DATE=${{ steps.buildmeta.outputs.BUILD_DATE }}
            GOPROXY=https://proxy.golang.org,direct
          cache-from: type=gha,scope=e2bgateway
          cache-to: type=gha,mode=max,scope=e2bgateway
```

6. "Build and push Sandbox image" step — add `id: sandbox_build` (everything else unchanged).

7. After the sandbox build step, append:

```yaml
      - name: SBOM — E2BGateway image (SPDX)
        uses: anchore/sbom-action@v0
        with:
          image: ghcr.io/${{ github.repository }}@${{ steps.gateway_build.outputs.digest }}
          format: spdx-json
          output-file: dist-sbom/e2bgateway_${{ github.ref_name }}_sbom.spdx.json
          upload-artifact: false
          upload-release-assets: false

      - name: SBOM — sandbox-base image (SPDX)
        uses: anchore/sbom-action@v0
        with:
          image: ghcr.io/${{ github.repository_owner }}/sandbox-base@${{ steps.sandbox_build.outputs.digest }}
          format: spdx-json
          output-file: dist-sbom/sandbox-base_${{ github.ref_name }}_sbom.spdx.json
          upload-artifact: false
          upload-release-assets: false

      - name: Install cosign
        uses: sigstore/cosign-installer@v3

      # Keyless (Fulcio/Rekor) signing by digest covers the whole manifest
      # list, i.e. every architecture, with one signature per image.
      - name: Sign images (keyless)
        run: |
          cosign sign --yes "ghcr.io/${{ github.repository }}@${{ steps.gateway_build.outputs.digest }}"
          cosign sign --yes "ghcr.io/${{ github.repository_owner }}/sandbox-base@${{ steps.sandbox_build.outputs.digest }}"

      - name: Attach SBOMs to GitHub Release
        uses: softprops/action-gh-release@v3
        with:
          files: dist-sbom/*.spdx.json
          fail_on_unmatched_files: true
```

The `helm-chart` job (`needs: [release, docker]`) is untouched — SBOM/cosign failures now block chart publication automatically.

- [ ] **Step 2: Validate workflow syntax**

Run: `go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.7 .github/workflows/release.yml`
Expected: no errors. If the module download fails, fall back to `python3 -c "import yaml,sys; yaml.safe_load(open('.github/workflows/release.yml'))"` plus a manual re-read of the diff.

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/release.yml
git commit -s -m "ci(release): SPDX SBOMs, cosign keyless signing, latest tag, full version ldflags (#120)"
```

---

### Task 5: Helm digest pinning

**Files:**
- Modify: `deploy/helm/e2bgateway/values.yaml:8-12`
- Modify: `deploy/helm/e2bgateway/templates/deployment.yaml:32`
- Modify: `deploy/helm/e2bgateway/README.md` (image values table ~line 67-69)

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `image.digest` value; when set, image renders `repository@digest` (Task 6 docs reference this).

- [ ] **Step 1: Add the value**

`values.yaml` image block becomes:

```yaml
image:
  repository: ghcr.io/e2bgateway/e2bgateway
  pullPolicy: IfNotPresent
  # Pin to a released tag in production; defaults to Chart appVersion.
  tag: ""
  # Optional digest pin (e.g. sha256:abc...). When set it takes precedence
  # over tag and the image renders as repository@digest. See
  # docs/guides/release-verification.md.
  digest: ""
```

- [ ] **Step 2: Update the deployment template**

Replace line 32 of `templates/deployment.yaml`:

```yaml
          {{- if .Values.image.digest }}
          image: "{{ .Values.image.repository }}@{{ .Values.image.digest }}"
          {{- else }}
          image: "{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}"
          {{- end }}
```

- [ ] **Step 3: Verify both renderings**

Run:

```bash
helm lint deploy/helm/e2bgateway
helm template deploy/helm/e2bgateway | grep 'image: "ghcr'
helm template deploy/helm/e2bgateway --set image.digest=sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa | grep 'image: "ghcr'
```

Expected: default renders `ghcr.io/e2bgateway/e2bgateway:<appVersion>`; with digest renders `ghcr.io/e2bgateway/e2bgateway@sha256:aaa...` (no `:tag`).

- [ ] **Step 4: Update chart README**

Add a row to the image values table after `image.tag`:

```markdown
| `image.digest` | Optional digest pin (`sha256:...`); overrides `tag` when set | `""` |
```

- [ ] **Step 5: Commit**

```bash
git add deploy/helm/e2bgateway/values.yaml deploy/helm/e2bgateway/templates/deployment.yaml deploy/helm/e2bgateway/README.md
git commit -s -m "feat(helm): support digest-pinned gateway image (#120)"
```

---

### Task 6: Release-verification docs + final gate

**Files:**
- Create: `docs/guides/release-verification.md`

**Interfaces:**
- Consumes: Task 4's signing identity (release.yml workflow) and SBOM asset names; Task 5's `image.digest`.
- Produces: user-facing verification guide (issue acceptance criterion).

- [ ] **Step 1: Write the guide**

Create `docs/guides/release-verification.md`:

````markdown
# Release Verification & Digest-Pinned Deployment

Every release tag (`vX.Y.Z`, including `vX.Y.Z-rc.N`) publishes:

- Multi-arch images (`linux/amd64` + `linux/arm64`):
  `ghcr.io/e2bgateway/e2bgateway` and `ghcr.io/e2bgateway/sandbox-base`
- SPDX SBOMs attached to the GitHub Release:
  `e2bgateway_<tag>_sbom.spdx.json`, `sandbox-base_<tag>_sbom.spdx.json`
- Cosign keyless signatures on both image digests (Sigstore Fulcio/Rekor)

## Verify image signatures

Install [cosign](https://docs.sigstore.dev/cosign/installation/), then:

```bash
cosign verify \
  --certificate-identity-regexp '^https://github\.com/e2bgateway/e2bgateway/\.github/workflows/release\.yml@refs/tags/v.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  ghcr.io/e2bgateway/e2bgateway:vX.Y.Z
```

The same command works for `ghcr.io/e2bgateway/sandbox-base:vX.Y.Z`. Verification
checks the signature against the public Rekor transparency log and pins the
signing identity to this repository's release workflow — a signature produced
anywhere else fails.

## Download the SBOM

From the release page, or via CLI:

```bash
gh release download vX.Y.Z --repo e2bgateway/e2bgateway \
  --pattern '*_sbom.spdx.json'
```

The SPDX JSON lists every Go module and OS package in the image; feed it to
your vulnerability scanner (e.g. `grype sbom:e2bgateway_vX.Y.Z_sbom.spdx.json`).

## Digest-pinned deployment

Tags are mutable; digests are not. Resolve the digest once and pin it:

```bash
DIGEST=$(docker buildx imagetools inspect \
  ghcr.io/e2bgateway/e2bgateway:vX.Y.Z --format '{{.Manifest.Digest}}')

docker pull --platform linux/arm64 "ghcr.io/e2bgateway/e2bgateway@${DIGEST}"
```

Helm (digest overrides `image.tag`):

```bash
helm install e2bgateway oci://ghcr.io/e2bgateway/charts/e2bgateway \
  --version X.Y.Z \
  --set image.digest="${DIGEST}"
```

Or in `values.yaml`:

```yaml
image:
  repository: ghcr.io/e2bgateway/e2bgateway
  digest: sha256:<digest>
```

## Check the running build

```bash
e2bgateway --version        # cobra one-liner
e2bgateway version          # version, commit, build date, Go version
curl -s http://<gateway>:8080/versionz   # same info as JSON, no auth
```
````

- [ ] **Step 2: Full verification gate**

Run: `make pre-commit && make openapi-check && make helm-lint`
Expected: all green. Fix any fmt/lint/test fallout before committing.

- [ ] **Step 3: Commit**

```bash
git add docs/guides/release-verification.md
git commit -s -m "docs(guides): release verification and digest-pinned deployment (#120)"
```

---

## Self-Review Notes

- Spec §4.1 → Task 3; §4.2 → Tasks 1+2; §4.3 → Task 4; §4.4 → Task 5; §4.5 → Task 6; §6 testing → per-task steps + Task 6 gate. No gaps.
- Type consistency: `v1.BuildInfo` fields `Version/Commit/BuildDate` used identically in Tasks 2 and 6; `server.WithBuildInfo` variadic keeps all existing `server.New(cfg)` callers compiling; `newServeCommand(info v1.BuildInfo)` matches root.go call.
- Workflow end-to-end (SBOM upload, cosign sign) only runs on a real tag push — noted for the PR description; rc tag can be used to validate, per spec §6.