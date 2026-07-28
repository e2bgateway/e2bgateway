.PHONY: build run test lint lint-fix fmt vet clean docker-build docker-push helm-lint kind-e2e-setup kind-e2e-test kind-e2e-cleanup test-kind-e2e

# Binary name
BINARY_NAME := e2bgateway
# Docker image
DOCKER_REPO := ghcr.io/e2bgateway/e2bgateway
DOCKER_TAG ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
# Go parameters
GOCMD := go
GOBUILD := $(GOCMD) build
GOTEST := $(GOCMD) test
GOMOD := $(GOCMD) mod
GOVET := $(GOCMD) vet
GOFMT := gofmt
LDFLAGS := -ldflags "-X main.version=$(DOCKER_TAG) -X main.buildDate=$(shell date -u +%Y-%m-%dT%H:%M:%SZ)"

# Build the binary
build:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GOBUILD) $(LDFLAGS) -o bin/$(BINARY_NAME) ./cmd/e2bgateway

# Build for local development
build-local:
	$(GOBUILD) $(LDFLAGS) -o bin/$(BINARY_NAME) ./cmd/e2bgateway

# Run the gateway locally
run: build-local
	./bin/$(BINARY_NAME) --config configs/e2bgateway-default.yaml

# Run tests
test:
	$(GOTEST) -v -race -coverprofile=coverage.out ./...

# Run short tests only
test-short:
	$(GOTEST) -v -short ./...

# Run E2E tests
test-e2e:
	$(GOTEST) -v -tags=e2e -timeout 30m ./test/e2e/...

# Coverage report
coverage: test
	$(GOCMD) tool cover -html=coverage.out -o coverage.html

# Lint
lint:
	golangci-lint run ./...

# Lint with auto-fix
lint-fix:
	golangci-lint run --fix ./...

# Format code
fmt:
	golangci-lint fmt ./...

# Vet
vet:
	$(GOVET) ./...

# Clean
clean:
	rm -rf bin/ coverage.out coverage.html

# Tidy modules
tidy:
	$(GOMOD) tidy

# Docker build
docker-build:
	docker build -t $(DOCKER_REPO):$(DOCKER_TAG) .
	docker tag $(DOCKER_REPO):$(DOCKER_TAG) $(DOCKER_REPO):latest

# Docker push
docker-push:
	docker push $(DOCKER_REPO):$(DOCKER_TAG)
	docker push $(DOCKER_REPO):latest

# Helm lint
helm-lint:
	helm lint deploy/helm/e2bgateway

# Helm template (render without install)
helm-template:
	helm template e2bgateway deploy/helm/e2bgateway

# Generate code (if needed in future)
generate:
	$(GOCMD) generate ./...

# Update CRD/client code from agent-sandbox
update-deps:
	$(GOMOD) download
	$(GOMOD) tidy

# Install development tools
install-tools:
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
	go install sigs.k8s.io/controller-tools/cmd/controller-gen@latest

# Pre-commit checks
pre-commit: fmt vet lint test

# Full CI pipeline
ci: tidy vet lint test docker-build

# Kind E2E testing
kind-e2e-setup:
	./hack/kind-e2e/setup.sh

kind-e2e-test:
	./hack/kind-e2e/run-tests.sh

kind-e2e-cleanup:
	./hack/kind-e2e/cleanup.sh

# Full kind E2E cycle: setup + test + cleanup
test-kind-e2e: kind-e2e-setup kind-e2e-test kind-e2e-cleanup
