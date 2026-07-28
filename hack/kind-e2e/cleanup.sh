#!/usr/bin/env bash
# Cleanup kind cluster and associated resources.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CLUSTER_NAME="e2bgateway-e2e"

echo "=== Cleaning up Kind E2E environment ==="

# Kill port-forward
if [ -f "${SCRIPT_DIR}/.port-forward.pid" ]; then
  PID=$(cat "${SCRIPT_DIR}/.port-forward.pid")
  kill "$PID" 2>/dev/null || true
  rm -f "${SCRIPT_DIR}/.port-forward.pid"
  echo "Port-forward stopped."
fi

# Also kill any stray port-forwards
pkill -f "kubectl port-forward.*e2bgateway" 2>/dev/null || true

# Delete kind cluster
if kind get clusters 2>/dev/null | grep -q "^${CLUSTER_NAME}$"; then
  kind delete cluster --name "${CLUSTER_NAME}"
  echo "Kind cluster deleted."
else
  echo "Kind cluster not found, skipping."
fi

echo "Cleanup complete."
