#!/usr/bin/env bash
# Setup kind cluster and deploy all components for E2E testing.
#
# Usage: ./hack/kind-e2e/setup.sh [--backend agent-sandbox|opensandbox|both]
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
BACKEND="${1:-both}"
CLUSTER_NAME="e2bgateway-e2e"

echo "=== E2BGateway Kind E2E Setup ==="
echo "Backend: ${BACKEND}"

# Check prerequisites
for cmd in kind kubectl docker; do
  if ! command -v "$cmd" &>/dev/null; then
    echo "ERROR: $cmd is required but not installed."
    exit 1
  fi
done

# 1. Create kind cluster
echo ""
echo "--- Creating kind cluster ---"
if kind get clusters 2>/dev/null | grep -q "^${CLUSTER_NAME}$"; then
  echo "Cluster ${CLUSTER_NAME} already exists, deleting..."
  kind delete cluster --name "${CLUSTER_NAME}"
fi
kind create cluster --name "${CLUSTER_NAME}" --config "${SCRIPT_DIR}/kind-config.yaml"
echo "Cluster created."

# Wait for cluster to be ready
kubectl wait --for=condition=Ready nodes --all --timeout=120s

# 2. Build and load E2BGateway image
echo ""
echo "--- Building E2BGateway image ---"
docker build -t e2bgateway:local "${PROJECT_ROOT}"
kind load docker-image e2bgateway:local --name "${CLUSTER_NAME}"
echo "Image loaded."

# 3. Create namespaces
echo ""
echo "--- Creating namespaces ---"
kubectl create namespace e2bgateway-system --dry-run=client -o yaml | kubectl apply -f -
kubectl create namespace sandbox-system --dry-run=client -o yaml | kubectl apply -f -

# 4. Deploy backend(s)
echo ""
echo "--- Deploying backends ---"

deploy_agent_sandbox() {
  echo "Deploying agent-sandbox backend..."
  kubectl apply -f "${SCRIPT_DIR}/manifests/agent-sandbox/" --namespace sandbox-system
  kubectl wait --for=condition=Available deployment/agent-sandbox-controller \
    --namespace sandbox-system --timeout=120s || true
  echo "agent-sandbox deployed."
}

deploy_opensandbox() {
  echo "Deploying opensandbox backend..."
  kubectl apply -f "${SCRIPT_DIR}/manifests/opensandbox/" --namespace sandbox-system
  kubectl wait --for=condition=Available deployment/opensandbox-controller \
    --namespace sandbox-system --timeout=120s || true
  echo "opensandbox deployed."
}

case "${BACKEND}" in
  agent-sandbox)
    deploy_agent_sandbox
    ;;
  opensandbox)
    deploy_opensandbox
    ;;
  both|*)
    deploy_agent_sandbox
    deploy_opensandbox
    ;;
esac

# 5. Deploy E2BGateway
echo ""
echo "--- Deploying E2BGateway ---"
kubectl apply -f "${SCRIPT_DIR}/manifests/gateway/" --namespace e2bgateway-system

# Wait for gateway to be ready
kubectl wait --for=condition=Available deployment/e2bgateway \
  --namespace e2bgateway-system --timeout=120s

# 6. Set up port-forward in background
echo ""
echo "--- Setting up port-forward ---"
# Kill any existing port-forward
pkill -f "kubectl port-forward.*e2bgateway" 2>/dev/null || true
kubectl port-forward svc/e2bgateway 8080:8080 -n e2bgateway-system &
PORT_FORWARD_PID=$!
echo "${PORT_FORWARD_PID}" > "${SCRIPT_DIR}/.port-forward.pid"
echo "Port-forward started (PID: ${PORT_FORWARD_PID})"

# Wait for gateway to respond
echo "Waiting for gateway..."
for i in $(seq 1 30); do
  if curl -s http://localhost:8080/healthz >/dev/null 2>&1; then
    echo "Gateway is ready!"
    break
  fi
  if [ "$i" -eq 30 ]; then
    echo "ERROR: Gateway did not become ready in 30s"
    exit 1
  fi
  sleep 1
done

echo ""
echo "=== Setup Complete ==="
echo ""
echo "Gateway URL: http://localhost:8080"
echo "API Key: test-key"
echo ""
echo "Run tests with:"
echo "  ./hack/kind-e2e/run-tests.sh"
echo ""
echo "Or manually:"
echo "  export E2B_DOMAIN=localhost:8080"
echo "  export E2B_API_KEY=test-key"
echo "  python examples/python/hello_world.py"
