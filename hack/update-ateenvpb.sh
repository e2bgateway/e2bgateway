#!/usr/bin/env bash
# Re-vendor the Agent Substrate ate-env generated protobuf bindings into
# internal/adapter/agentsubstrate/ateenvpb/.
#
# Usage: hack/update-ateenvpb.sh [git-ref]   (default: upstream default branch)
set -euo pipefail

REPO="agent-substrate/env"
SRC_DIR="proto/ateenv/v1alpha"
FILES=(env.pb.go env_grpc.pb.go guest.pb.go guest_grpc.pb.go)
PKG="ateenvpb"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DEST="${ROOT}/internal/adapter/agentsubstrate/${PKG}"
REF="${1:-HEAD}"

SHA="$(curl -fsSL -H 'Accept: application/vnd.github.sha' \
  "https://api.github.com/repos/${REPO}/commits/${REF}")"
if [[ ! "${SHA}" =~ ^[0-9a-f]{40}$ ]]; then
  echo "failed to resolve ${REPO}@${REF}" >&2
  exit 1
fi

TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
mkdir "${TMP}/staged"

for f in "${FILES[@]}"; do
  curl -fsSL "https://raw.githubusercontent.com/${REPO}/${SHA}/${SRC_DIR}/${f}" -o "${TMP}/${f}"
  # Only the package clause is changed.
  sed -E "s/^package [A-Za-z0-9_]+$/package ${PKG}/" "${TMP}/${f}" >"${TMP}/staged/${f}"
done

# Keep the recorded source commit in doc.go in sync.
sed -E "s#(github\.com/${REPO} @ )[0-9a-f]{40}#\1${SHA}#" "${DEST}/doc.go" >"${TMP}/staged/doc.go"

# Publish only after every download and transformation succeeds.
for f in "${FILES[@]}" doc.go; do
  cp "${TMP}/staged/${f}" "${DEST}/${f}"
done

gofmt -l "${DEST}"
(cd "${ROOT}" && go build ./internal/adapter/agentsubstrate/...)
echo "ateenvpb updated to ${REPO}@${SHA}"
echo "Review the diff and run: go test -race ./internal/adapter/agentsubstrate/"
