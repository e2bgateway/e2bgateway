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