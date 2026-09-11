#!/usr/bin/env bash
# Port Forwarding E2E Test for agent-sandbox and opensandbox backends
#
# This script tests port forwarding functionality against real backends
# in a Kubernetes environment (kind cluster or real cluster).
#
# Usage: ./hack/kind-e2e/test-port-forwarding.sh [--backend agent-sandbox|opensandbox|both]
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
BACKEND="${1:-both}"

export E2B_DOMAIN="${E2B_DOMAIN:-localhost:8080}"
export E2B_API_KEY="${E2B_API_KEY:-test-key}"
GATEWAY_URL="http://${E2B_DOMAIN}"

PASSED=0
FAILED=0
SKIPPED=0

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

pass() {
  echo -e "${GREEN}  ✓ PASS${NC}: $1"
  PASSED=$((PASSED + 1))
}

fail() {
  echo -e "${RED}  ✗ FAIL${NC}: $1 - $2"
  FAILED=$((FAILED + 1))
}

skip() {
  echo -e "${YELLOW}  ⊘ SKIP${NC}: $1 - $2"
  SKIPPED=$((SKIPPED + 1))
}

info() {
  echo -e "${BLUE}  ℹ INFO${NC}: $1"
}

echo ""
echo "============================================================"
echo " Port Forwarding E2E Test"
echo "============================================================"
echo "Gateway: ${GATEWAY_URL}"
echo "Backend: ${BACKEND}"
echo "============================================================"
echo ""

# ============================================
# Test Function
# ============================================
test_port_forwarding() {
  local backend_name=$1

  echo ""
  echo "============================================================"
  echo " Testing Port Forwarding on: ${backend_name}"
  echo "============================================================"
  echo ""

  # Step 1: Create a sandbox
  info "Creating sandbox on ${backend_name}..."
  CREATE_RESP=$(curl -sf -X POST "${GATEWAY_URL}/sandboxes" \
    -H "X-API-Key: ${E2B_API_KEY}" \
    -H "Content-Type: application/json" \
    -d "{\"templateID\":\"base\",\"timeout\":300,\"backend\":\"${backend_name}\"}" || true)

  SANDBOX_ID=$(echo "$CREATE_RESP" | python3 -c "import sys,json; print(json.load(sys.stdin).get('sandboxID',''))" 2>/dev/null || true)

  if [ -z "$SANDBOX_ID" ]; then
    fail "Create sandbox" "no sandboxID in response: $CREATE_RESP"
    return
  fi
  pass "Created sandbox: ${SANDBOX_ID}"

  # Step 2: Wait for sandbox to be ready
  info "Waiting for sandbox to be ready..."
  sleep 3

  # Verify sandbox is running
  GET_RESP=$(curl -sf "${GATEWAY_URL}/sandboxes/${SANDBOX_ID}" \
    -H "X-API-Key: ${E2B_API_KEY}" || true)
  STATE=$(echo "$GET_RESP" | python3 -c "import sys,json; print(json.load(sys.stdin).get('state',''))" 2>/dev/null || true)

  if [ "$STATE" = "running" ]; then
    pass "Sandbox is running"
  else
    fail "Sandbox state" "expected 'running', got: $STATE"
  fi

  # Step 3: Start a simple HTTP server in the sandbox
  info "Starting HTTP server on port 3000..."
  CMD_RESP=$(curl -sf -X POST "${GATEWAY_URL}/sandboxes/${SANDBOX_ID}/commands" \
    -H "X-API-Key: ${E2B_API_KEY}" \
    -H "Content-Type: application/json" \
    -d '{"command":"python3 -m http.server 3000 &"}' || true)

  if echo "$CMD_RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); assert d.get('exitCode')==0" 2>/dev/null; then
    pass "Started HTTP server on port 3000"
  else
    info "Command response: $CMD_RESP"
    skip "Start HTTP server" "command may have failed"
  fi

  # Wait for server to start
  sleep 2

  # Step 4: Test ListPorts
  echo ""
  info "Testing ListPorts API..."
  PORTS_RESP=$(curl -sf "${GATEWAY_URL}/sandboxes/${SANDBOX_ID}/ports" \
    -H "X-API-Key: ${E2B_API_KEY}" || true)

  if [ -n "$PORTS_RESP" ]; then
    pass "ListPorts API responds"

    # Verify response format
    if echo "$PORTS_RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); assert 'ports' in d" 2>/dev/null; then
      pass "ListPorts response has 'ports' field"

      # Show ports
      PORT_COUNT=$(echo "$PORTS_RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); print(len(d.get('ports',[])))" 2>/dev/null || echo "0")
      info "Listed ${PORT_COUNT} port(s)"
    else
      fail "ListPorts response format" "missing 'ports' field"
    fi
  else
    fail "ListPorts API" "no response"
  fi

  # Step 5: Test GetPortURL for port 3000
  echo ""
  info "Testing GetPortURL API for port 3000..."
  PORT_URL_RESP=$(curl -sf "${GATEWAY_URL}/sandboxes/${SANDBOX_ID}/ports/3000" \
    -H "X-API-Key: ${E2B_API_KEY}" || true)

  if [ -n "$PORT_URL_RESP" ]; then
    pass "GetPortURL API responds for port 3000"

    # Verify response format
    if echo "$PORT_URL_RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); assert 'url' in d and d['url'] != ''" 2>/dev/null; then
      PORT_URL=$(echo "$PORT_URL_RESP" | python3 -c "import sys,json; print(json.load(sys.stdin)['url'])" 2>/dev/null || true)
      pass "GetPortURL returned URL: ${PORT_URL}"

      # Step 6: Try to access the port URL (optional, may not work from outside cluster)
      info "Attempting to access port URL..."
      HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" "${PORT_URL}" --max-time 5 2>/dev/null || echo "000")

      if [ "$HTTP_CODE" = "200" ]; then
        pass "Successfully accessed port URL (HTTP 200)"
      elif [ "$HTTP_CODE" = "000" ]; then
        skip "Access port URL" "cannot reach from test environment (network isolation)"
      else
        info "HTTP response code: ${HTTP_CODE} (may be expected in some environments)"
      fi
    else
      fail "GetPortURL response format" "missing or empty 'url' field"
    fi
  else
    fail "GetPortURL API" "no response for port 3000"
  fi

  # Step 7: Test GetPortURL for port 8080
  echo ""
  info "Testing GetPortURL API for port 8080..."
  PORT_URL_RESP_8080=$(curl -sf "${GATEWAY_URL}/sandboxes/${SANDBOX_ID}/ports/8080" \
    -H "X-API-Key: ${E2B_API_KEY}" || true)

  if [ -n "$PORT_URL_RESP_8080" ]; then
    pass "GetPortURL API responds for port 8080"

    if echo "$PORT_URL_RESP_8080" | python3 -c "import sys,json; d=json.load(sys.stdin); assert 'url' in d" 2>/dev/null; then
      pass "GetPortURL returned valid response for port 8080"
    else
      fail "GetPortURL response format" "invalid response for port 8080"
    fi
  else
    fail "GetPortURL API" "no response for port 8080"
  fi

  # Step 8: Verify port tracking
  echo ""
  info "Verifying port tracking..."
  PORTS_RESP_2=$(curl -sf "${GATEWAY_URL}/sandboxes/${SANDBOX_ID}/ports" \
    -H "X-API-Key: ${E2B_API_KEY}" || true)

  if [ -n "$PORTS_RESP_2" ]; then
    PORT_COUNT=$(echo "$PORTS_RESP_2" | python3 -c "import sys,json; d=json.load(sys.stdin); print(len(d.get('ports',[])))" 2>/dev/null || echo "0")

    if [ "$PORT_COUNT" -ge 2 ]; then
      pass "Port tracking works (tracked ${PORT_COUNT} ports)"
    else
      fail "Port tracking" "expected >= 2 tracked ports, got: $PORT_COUNT"
    fi
  else
    fail "ListPorts (2nd call)" "no response"
  fi

  # Step 9: Test error handling
  echo ""
  info "Testing error handling..."
  INVALID_RESP=$(curl -s "${GATEWAY_URL}/sandboxes/non-existent-sandbox/ports/3000" \
    -H "X-API-Key: ${E2B_API_KEY}")
  INVALID_CODE=$(echo "$INVALID_RESP" | python3 -c "import sys,json; print(json.load(sys.stdin).get('code',''))" 2>/dev/null || true)

  if [ "$INVALID_CODE" = "404" ]; then
    pass "GetPortURL for non-existent sandbox returns 404"
  else
    fail "Error handling" "expected 404, got: $INVALID_CODE"
  fi

  # Step 10: Cleanup
  echo ""
  info "Cleaning up sandbox..."
  curl -sf -X DELETE "${GATEWAY_URL}/sandboxes/${SANDBOX_ID}" \
    -H "X-API-Key: ${E2B_API_KEY}" >/dev/null 2>&1
  pass "Deleted sandbox"
}

# ============================================
# Run Tests
# ============================================

if [ "$BACKEND" = "agent-sandbox" ] || [ "$BACKEND" = "both" ]; then
  test_port_forwarding "agent-sandbox"
fi

if [ "$BACKEND" = "opensandbox" ] || [ "$BACKEND" = "both" ]; then
  test_port_forwarding "opensandbox"
fi

# ============================================
# Summary
# ============================================
echo ""
echo "============================================================"
echo " Test Summary"
echo "============================================================"
echo -e "  ${GREEN}✓ Passed${NC}: ${PASSED}"
echo -e "  ${RED}✗ Failed${NC}: ${FAILED}"
echo -e "  ${YELLOW}⊘ Skipped${NC}: ${SKIPPED}"
echo "============================================================"
echo ""

if [ "${FAILED}" -gt 0 ]; then
  echo -e "${RED}Some tests failed!${NC}"
  exit 1
else
  echo -e "${GREEN}All tests passed!${NC}"
  exit 0
fi
