# Multi-stage build: compile envd from e2b-dev/infra (Apache 2.0) and embed
# it into a sandbox base image. envd exposes the ConnectRPC data-plane that
# E2B Python/JS SDKs talk to on port 49983.
#
# Build:
#   docker build -t ghcr.io/e2bgateway/sandbox-base:python3.11 \
#     --build-arg BASE_IMAGE=python:3.11-slim images/sandbox/

ARG BASE_IMAGE=python:3.11-slim
ARG ENVD_VERSION=2026.16

# ── Stage 1: build envd ─────────────────────────────────────────────────────
FROM golang:1.26.2 AS builder

ARG ENVD_VERSION

RUN apt-get update && apt-get install -y --no-install-recommends git ca-certificates && rm -rf /var/lib/apt/lists/*

# Clone the infra repo at a pinned tag and build the static envd binary.
RUN git clone --depth 1 --branch "${ENVD_VERSION}" \
      https://github.com/e2b-dev/infra.git /infra

WORKDIR /infra/packages/envd

# envd depends on packages/shared via a local replace directive, so we must
# build from the module root.  The replace directive in go.mod points to
# ../shared which is already inside the cloned tree.
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$(go env GOHOSTARCH) \
      go build -a -ldflags "-s -w" -o /out/envd .

# ── Stage 2: sandbox image with envd ───────────────────────────────────────
FROM ${BASE_IMAGE}

COPY --from=builder /out/envd /usr/bin/envd
COPY entrypoint.sh /usr/local/bin/entrypoint.sh

RUN chmod +x /usr/bin/envd /usr/local/bin/entrypoint.sh && \
    mkdir -p /run/e2b

EXPOSE 49983

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
