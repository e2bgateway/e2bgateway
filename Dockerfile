# Build stage
FROM golang:1.26.2 AS builder

ARG TARGETARCH=amd64

RUN apt-get update && apt-get install -y --no-install-recommends git make && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Cache dependencies
COPY go.mod go.sum ./
RUN go mod download

# Build binary
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags "-X main.version=$(git describe --tags --always --dirty 2>/dev/null || echo dev) -X main.buildDate=$(date -u +%Y-%m-%dT%H:%M:%SZ)" -o bin/e2bgateway ./cmd/e2bgateway

# Runtime stage
FROM alpine:3.20

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
