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

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN { FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n\n"} /^[a-zA-Z_0-9-]+:.*##/ { printf "  \033[36m%-25s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Building
##############

.PHONY: build
build: ## Build the binary for linux/amd64.
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GOBUILD) $(LDFLAGS) -o bin/$(BINARY_NAME) ./cmd/e2bgateway

.PHONY: build-local
build-local: ## Build the binary for the local platform.
	$(GOBUILD) $(LDFLAGS) -o bin/$(BINARY_NAME) ./cmd/e2bgateway

.PHONY: run
run: build-local ## Run the gateway locally.
	./bin/$(BINARY_NAME) --config configs/e2bgateway-default.yaml

##@ Testing
############

.PHONY: test
test: ## Run all unit tests.
	$(GOTEST) -v -race -coverprofile=coverage.out ./...

.PHONY: test-short
test-short: ## Run short tests only.
	$(GOTEST) -v -short ./...

.PHONY: test-e2e
test-e2e: ## Run E2E tests.
	$(GOTEST) -v -tags=e2e -timeout 30m ./test/e2e/...

.PHONY: coverage
coverage: test ## Generate an HTML coverage report.
	$(GOCMD) tool cover -html=coverage.out -o coverage.html

##@ Code Quality
#################

.PHONY: lint
lint: ## Run the linter.
	golangci-lint run ./...

.PHONY: lint-fix
lint-fix: ## Run the linter with auto-fix.
	golangci-lint run --fix ./...

.PHONY: fmt
fmt: ## Format Go code.
	golangci-lint fmt ./...

.PHONY: vet
vet: ## Vet Go code.
	$(GOVET) ./...

.PHONY: pre-commit
pre-commit: fmt vet lint test ## Run all pre-commit checks.

.PHONY: ci
ci: tidy vet lint test docker-build ## Run the full CI pipeline.

##@ Development
################

.PHONY: clean
clean: ## Remove build artifacts.
	rm -rf bin/ coverage.out coverage.html

.PHONY: tidy
tidy: ## Tidy Go modules.
	$(GOMOD) tidy

.PHONY: generate
generate: ## Run Go generate.
	$(GOCMD) generate ./...

.PHONY: update-deps
update-deps: ## Download and tidy Go modules.
	$(GOMOD) download
	$(GOMOD) tidy

.PHONY: install-tools
install-tools: ## Install development tools.
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
	go install sigs.k8s.io/controller-tools/cmd/controller-gen@latest

##@ Docker
###########

.PHONY: docker-build
docker-build: ## Build the Docker image.
	docker build -t $(DOCKER_REPO):$(DOCKER_TAG) .
	docker tag $(DOCKER_REPO):$(DOCKER_TAG) $(DOCKER_REPO):latest

.PHONY: docker-push
docker-push: ## Push the Docker image.
	docker push $(DOCKER_REPO):$(DOCKER_TAG)
	docker push $(DOCKER_REPO):latest

##@ Helm
#########

.PHONY: helm-lint
helm-lint: ## Lint the Helm chart.
	helm lint deploy/helm/e2bgateway

.PHONY: helm-template
helm-template: ## Render the Helm chart without installing.
	helm template e2bgateway deploy/helm/e2bgateway

##@ Kind E2E
#############

.PHONY: kind-e2e-setup
kind-e2e-setup: ## Set up the Kind cluster for E2E tests.
	./hack/kind-e2e/setup.sh

.PHONY: kind-e2e-test
kind-e2e-test: ## Run E2E tests on Kind.
	./hack/kind-e2e/run-tests.sh

.PHONY: kind-e2e-cleanup
kind-e2e-cleanup: ## Clean up the Kind E2E environment.
	./hack/kind-e2e/cleanup.sh

.PHONY: test-kind-e2e
test-kind-e2e: kind-e2e-setup kind-e2e-test kind-e2e-cleanup ## Run the full Kind E2E cycle (setup + test + cleanup).
