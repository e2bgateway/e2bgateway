# Build stage
FROM golang:1.23-alpine AS builder

RUN apk add --no-cache git make

WORKDIR /app

# Cache dependencies
COPY go.mod go.sum ./
RUN go mod download

# Build binary
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 make build

# Runtime stage
FROM gcr.io/distroless/static:nonroot

LABEL org.opencontainers.image.source="https://github.com/e2bgateway/e2bgateway"
LABEL org.opencontainers.image.description="E2B-compatible API Gateway for AI Agent Sandboxes"
LABEL org.opencontainers.image.licenses="Apache-2.0"

WORKDIR /

COPY --from=builder /app/bin/e2bgateway /e2bgateway

USER 65532:65532

EXPOSE 8080 8443 9090

ENTRYPOINT ["/e2bgateway"]
CMD ["--config", "/etc/e2bgateway/config.yaml"]
