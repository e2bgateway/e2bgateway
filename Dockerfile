# Build stage — cross-compile on the builder's native platform ($BUILDPLATFORM)
# so arm64 builds never run the Go compiler under QEMU emulation.
FROM --platform=$BUILDPLATFORM golang:1.27.3@sha256:e519d3b0a5b285e5aa53ab9eef02bc6bbb9cb04608389b743a136a9ce75f0d1e AS builder

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
FROM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf

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