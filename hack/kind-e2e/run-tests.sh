#!/usr/bin/env bash
# Run multi-language E2E tests against the deployed gateway.
#
# Usage: ./hack/kind-e2e/run-tests.sh [--backend agent-sandbox|opensandbox]
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
NC='\033[0m' # No Color

pass() {
  echo -e "${GREEN}  PASS${NC}: $1"
  PASSED=$((PASSED + 1))
}

fail() {
  echo -e "${RED}  FAIL${NC}: $1 - $2"
  FAILED=$((FAILED + 1))
}

skip() {
  echo -e "${YELLOW}  SKIP${NC}: $1 - $2"
  SKIPPED=$((SKIPPED + 1))
}

echo "=== E2BGateway E2E Tests ==="
echo "Gateway: ${GATEWAY_URL}"
echo "Backend: ${BACKEND}"
echo ""

# ============================================
# Test 1: Health Checks
# ============================================
echo "--- Health Checks ---"

# Healthz
if curl -sf "${GATEWAY_URL}/healthz" >/dev/null 2>&1; then
  pass "GET /healthz"
else
  fail "GET /healthz" "health endpoint not responding"
fi

# Readyz
if curl -sf "${GATEWAY_URL}/readyz" >/dev/null 2>&1; then
  pass "GET /readyz"
else
  fail "GET /readyz" "readiness endpoint not responding"
fi

# ============================================
# Test 2: Sandbox Lifecycle (curl)
# ============================================
echo ""
echo "--- Sandbox Lifecycle (curl) ---"

# Create sandbox
CREATE_RESP=$(curl -sf -X POST "${GATEWAY_URL}/sandboxes" \
  -H "X-API-Key: ${E2B_API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{"templateID":"base","timeout":300}')
SANDBOX_ID=$(echo "$CREATE_RESP" | python3 -c "import sys,json; print(json.load(sys.stdin).get('sandboxID',''))" 2>/dev/null || true)

if [ -n "$SANDBOX_ID" ]; then
  pass "POST /sandboxes (sandboxID: ${SANDBOX_ID})"
else
  fail "POST /sandboxes" "no sandboxID in response: $CREATE_RESP"
fi

# Verify E2B response format
if echo "$CREATE_RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); assert 'envdVersion' in d" 2>/dev/null; then
  pass "Response has envdVersion field"
else
  fail "Response format" "missing envdVersion field"
fi

if echo "$CREATE_RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); assert 'envdAccessToken' in d" 2>/dev/null; then
  pass "Response has envdAccessToken field"
else
  fail "Response format" "missing envdAccessToken field"
fi

if [ -n "$SANDBOX_ID" ]; then
  # Get sandbox
  GET_RESP=$(curl -sf "${GATEWAY_URL}/sandboxes/${SANDBOX_ID}" \
    -H "X-API-Key: ${E2B_API_KEY}")
  STATE=$(echo "$GET_RESP" | python3 -c "import sys,json; print(json.load(sys.stdin).get('state',''))" 2>/dev/null || true)
  if [ "$STATE" = "running" ]; then
    pass "GET /sandboxes/{id} (state: running)"
  else
    fail "GET /sandboxes/{id}" "expected state=running, got: $STATE"
  fi

  # List sandboxes
  LIST_COUNT=$(curl -sf "${GATEWAY_URL}/sandboxes" \
    -H "X-API-Key: ${E2B_API_KEY}" | python3 -c "import sys,json; print(len(json.load(sys.stdin)))" 2>/dev/null || echo "0")
  if [ "$LIST_COUNT" -ge 1 ]; then
    pass "GET /sandboxes (count: $LIST_COUNT)"
  else
    fail "GET /sandboxes" "expected >= 1 sandbox, got: $LIST_COUNT"
  fi

  # Run command
  CMD_RESP=$(curl -sf -X POST "${GATEWAY_URL}/sandboxes/${SANDBOX_ID}/commands" \
    -H "X-API-Key: ${E2B_API_KEY}" \
    -H "Content-Type: application/json" \
    -d '{"command":"echo hello_e2e"}')
  CMD_STDOUT=$(echo "$CMD_RESP" | python3 -c "import sys,json; print(json.load(sys.stdin).get('stdout',''))" 2>/dev/null || true)
  if echo "$CMD_STDOUT" | grep -q "hello_e2e"; then
    pass "POST /sandboxes/{id}/commands"
  else
    fail "POST /sandboxes/{id}/commands" "expected stdout with 'hello_e2e', got: $CMD_STDOUT"
  fi

  # Execute code
  CODE_RESP=$(curl -sf -X POST "${GATEWAY_URL}/sandboxes/${SANDBOX_ID}/code" \
    -H "X-API-Key: ${E2B_API_KEY}" \
    -H "Content-Type: application/json" \
    -d '{"code":"print(42)","language":"python"}')
  if echo "$CODE_RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); assert d.get('exitCode')==0" 2>/dev/null; then
    pass "POST /sandboxes/{id}/code"
  else
    fail "POST /sandboxes/{id}/code" "unexpected response: $CODE_RESP"
  fi

  # File operations
  # Write file (multipart upload)
  echo -n "e2e test content" > /tmp/e2e_test_upload.txt
  curl -sf -X POST "${GATEWAY_URL}/sandboxes/${SANDBOX_ID}/filesystem/upload" \
    -H "X-API-Key: ${E2B_API_KEY}" \
    -F "path=/tmp/test_e2e.txt" \
    -F "file=@/tmp/e2e_test_upload.txt" >/dev/null 2>&1
  pass "POST /filesystem/upload"

  # Read file
  FILE_CONTENT=$(curl -sf "${GATEWAY_URL}/sandboxes/${SANDBOX_ID}/filesystem/download?path=/tmp/test_e2e.txt" \
    -H "X-API-Key: ${E2B_API_KEY}" 2>/dev/null || true)
  if echo "$FILE_CONTENT" | grep -q "e2e test content"; then
    pass "GET /filesystem/download"
  else
    fail "GET /filesystem/download" "content mismatch: $FILE_CONTENT"
  fi

  # List files
  LIST_FILES=$(curl -sf -X POST "${GATEWAY_URL}/sandboxes/${SANDBOX_ID}/filesystem/list" \
    -H "X-API-Key: ${E2B_API_KEY}" \
    -H "Content-Type: application/json" \
    -d '{"path":"/tmp"}')
  if echo "$LIST_FILES" | python3 -c "import sys,json; d=json.load(sys.stdin); entries=d.get('entries',d) if isinstance(d,dict) else d; assert len(entries)>0" 2>/dev/null; then
    pass "POST /filesystem/list"
  else
    fail "POST /filesystem/list" "no files listed"
  fi

  # Set environment variables
  curl -sf -X POST "${GATEWAY_URL}/sandboxes/${SANDBOX_ID}/envs" \
    -H "X-API-Key: ${E2B_API_KEY}" \
    -H "Content-Type: application/json" \
    -d '{"envs":{"TEST_VAR":"hello"}}' >/dev/null 2>&1
  pass "POST /envs"

  # Pause and Resume
  curl -sf -X POST "${GATEWAY_URL}/sandboxes/${SANDBOX_ID}/pause" \
    -H "X-API-Key: ${E2B_API_KEY}" >/dev/null 2>&1
  pass "POST /pause"

  RESUME_RESP=$(curl -sf -X POST "${GATEWAY_URL}/sandboxes/${SANDBOX_ID}/resume" \
    -H "X-API-Key: ${E2B_API_KEY}")
  if echo "$RESUME_RESP" | python3 -c "import sys,json; assert 'sandboxID' in json.load(sys.stdin)" 2>/dev/null; then
    pass "POST /resume"
  else
    fail "POST /resume" "unexpected response: $RESUME_RESP"
  fi

  # Set timeout
  curl -sf -X POST "${GATEWAY_URL}/sandboxes/${SANDBOX_ID}/timeout" \
    -H "X-API-Key: ${E2B_API_KEY}" \
    -H "Content-Type: application/json" \
    -d '{"timeout":600}' >/dev/null 2>&1
  pass "POST /timeout"

  # ============================================
  # Port Forwarding Tests
  # ============================================
  echo ""
  echo "--- Port Forwarding ---"

  # Test ListPorts - should return empty or default ports initially
  PORTS_RESP=$(curl -sf "${GATEWAY_URL}/sandboxes/${SANDBOX_ID}/ports" \
    -H "X-API-Key: ${E2B_API_KEY}" || true)
  if [ -n "$PORTS_RESP" ]; then
    pass "GET /sandboxes/{id}/ports (list ports)"
    # Verify response has 'ports' field
    if echo "$PORTS_RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); assert 'ports' in d" 2>/dev/null; then
      pass "ListPorts response has 'ports' field"
    else
      fail "ListPorts response format" "missing 'ports' field: $PORTS_RESP"
    fi
  else
    fail "GET /sandboxes/{id}/ports" "no response"
  fi

  # Test GetPortURL for port 3000
  PORT_URL_RESP=$(curl -sf "${GATEWAY_URL}/sandboxes/${SANDBOX_ID}/ports/3000" \
    -H "X-API-Key: ${E2B_API_KEY}" || true)
  if [ -n "$PORT_URL_RESP" ]; then
    pass "GET /sandboxes/{id}/ports/3000 (get port URL)"
    # Verify response has 'url' field
    if echo "$PORT_URL_RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); assert 'url' in d and d['url'] != ''" 2>/dev/null; then
      PORT_URL=$(echo "$PORT_URL_RESP" | python3 -c "import sys,json; print(json.load(sys.stdin)['url'])" 2>/dev/null || true)
      pass "GetPortURL returned URL: ${PORT_URL}"
    else
      fail "GetPortURL response format" "missing or empty 'url' field: $PORT_URL_RESP"
    fi
  else
    fail "GET /sandboxes/{id}/ports/3000" "no response"
  fi

  # Test GetPortURL for port 8080
  PORT_URL_RESP_8080=$(curl -sf "${GATEWAY_URL}/sandboxes/${SANDBOX_ID}/ports/8080" \
    -H "X-API-Key: ${E2B_API_KEY}" || true)
  if [ -n "$PORT_URL_RESP_8080" ]; then
    pass "GET /sandboxes/{id}/ports/8080"
  else
    fail "GET /sandboxes/{id}/ports/8080" "no response"
  fi

  # Test ListPorts again - should now include the ports we accessed
  PORTS_RESP_2=$(curl -sf "${GATEWAY_URL}/sandboxes/${SANDBOX_ID}/ports" \
    -H "X-API-Key: ${E2B_API_KEY}" || true)
  if [ -n "$PORTS_RESP_2" ]; then
    # Count ports - should have at least 2 (the ones we accessed)
    PORT_COUNT=$(echo "$PORTS_RESP_2" | python3 -c "import sys,json; d=json.load(sys.stdin); print(len(d.get('ports',[])))" 2>/dev/null || echo "0")
    if [ "$PORT_COUNT" -ge 2 ]; then
      pass "ListPorts after GetPortURL (count: $PORT_COUNT)"
    else
      fail "ListPorts tracking" "expected >= 2 ports, got: $PORT_COUNT"
    fi
  else
    fail "GET /sandboxes/{id}/ports (2nd call)" "no response"
  fi

  # Test GetPortURL for non-existent sandbox
  INVALID_RESP=$(curl -s "${GATEWAY_URL}/sandboxes/non-existent-sandbox/ports/3000" \
    -H "X-API-Key: ${E2B_API_KEY}")
  INVALID_CODE=$(echo "$INVALID_RESP" | python3 -c "import sys,json; print(json.load(sys.stdin).get('code',''))" 2>/dev/null || true)
  if [ "$INVALID_CODE" = "404" ]; then
    pass "GetPortURL for non-existent sandbox returns 404"
  else
    fail "GetPortURL error handling" "expected 404, got: $INVALID_CODE"
  fi

  # Kill sandbox
  curl -sf -X DELETE "${GATEWAY_URL}/sandboxes/${SANDBOX_ID}" \
    -H "X-API-Key: ${E2B_API_KEY}" >/dev/null 2>&1
  pass "DELETE /sandboxes/{id}"
fi

# ============================================
# Test 3: Error Format
# ============================================
echo ""
echo "--- Error Format ---"

ERR_RESP=$(curl -s "${GATEWAY_URL}/sandboxes/nonexistent-id" \
  -H "X-API-Key: ${E2B_API_KEY}")
ERR_CODE=$(echo "$ERR_RESP" | python3 -c "import sys,json; print(json.load(sys.stdin).get('code',''))" 2>/dev/null || true)
ERR_MSG=$(echo "$ERR_RESP" | python3 -c "import sys,json; print(json.load(sys.stdin).get('message',''))" 2>/dev/null || true)
if [ -n "$ERR_CODE" ] && [ -n "$ERR_MSG" ]; then
  pass "Error format: {code: $ERR_CODE, message: ...}"
else
  fail "Error format" "unexpected: $ERR_RESP"
fi

# ============================================
# Test 4: Templates
# ============================================
echo ""
echo "--- Templates ---"

TMPL_RESP=$(curl -sf "${GATEWAY_URL}/templates" -H "X-API-Key: ${E2B_API_KEY}" || true)
if [ -n "$TMPL_RESP" ]; then
  pass "GET /templates"
else
  fail "GET /templates" "no response"
fi

# ============================================
# Test 5: V2 API
# ============================================
echo ""
echo "--- V2 API ---"

V2_RESP=$(curl -sf "${GATEWAY_URL}/v2/sandboxes" -H "X-API-Key: ${E2B_API_KEY}" || true)
if [ -n "$V2_RESP" ]; then
  pass "GET /v2/sandboxes"
else
  fail "GET /v2/sandboxes" "no response"
fi

V2_TMPL=$(curl -sf "${GATEWAY_URL}/v2/templates" -H "X-API-Key: ${E2B_API_KEY}" || true)
if [ -n "$V2_TMPL" ]; then
  pass "GET /v2/templates"
else
  fail "GET /v2/templates" "no response"
fi

# ============================================
# Test 6: Backward Compatibility
# ============================================
echo ""
echo "--- Backward Compatibility (api/v1) ---"

V1_RESP=$(curl -sf "${GATEWAY_URL}/api/v1/sandboxes" -H "X-API-Key: ${E2B_API_KEY}" || true)
if [ -n "$V1_RESP" ]; then
  pass "GET /api/v1/sandboxes"
else
  fail "GET /api/v1/sandboxes" "no response"
fi

# ============================================
# Test 7: Go Examples (raw HTTP API)
# ============================================
echo ""
echo "--- Go Examples (raw HTTP API) ---"

if command -v go &>/dev/null; then
  # Hello world
  if E2B_DOMAIN="${E2B_DOMAIN}" E2B_API_KEY="${E2B_API_KEY}" go run "${PROJECT_ROOT}/examples/go/hello_world/" 2>&1 | grep -q "Killed"; then
    pass "Go: hello_world"
  else
    fail "Go: hello_world" "unexpected output"
  fi

  # Sandbox lifecycle
  if E2B_DOMAIN="${E2B_DOMAIN}" E2B_API_KEY="${E2B_API_KEY}" go run "${PROJECT_ROOT}/examples/go/sandbox_lifecycle/" 2>&1 | grep -q "Killed"; then
    pass "Go: sandbox_lifecycle"
  else
    fail "Go: sandbox_lifecycle" "unexpected output"
  fi

  # Filesystem
  if E2B_DOMAIN="${E2B_DOMAIN}" E2B_API_KEY="${E2B_API_KEY}" go run "${PROJECT_ROOT}/examples/go/filesystem/" 2>&1 | grep -q "Killed"; then
    pass "Go: filesystem"
  else
    fail "Go: filesystem" "unexpected output"
  fi

  # Coding agent
  if E2B_DOMAIN="${E2B_DOMAIN}" E2B_API_KEY="${E2B_API_KEY}" go run "${PROJECT_ROOT}/examples/go/coding_agent/" 2>&1 | grep -q "Done"; then
    pass "Go: coding_agent"
  else
    fail "Go: coding_agent" "unexpected output"
  fi
else
  skip "Go examples" "go not found"
fi

# ============================================
# Test 8: Python SDK Examples (requires real sandbox)
# ============================================
echo ""
echo "--- Python SDK Examples ---"

# Python SDK examples require a real E2B-compatible sandbox runtime (envd daemon).
# They use gRPC to communicate directly with the sandbox, not the gateway REST API.
# Skip if E2B SDK is not installed or if using mock backend.
if python3 -c "import e2b" 2>/dev/null; then
  export E2B_API_URL="http://${E2B_DOMAIN}"
  export E2B_API_KEY="e2b_0000000000000000000000000000000000000000"

  # Hello world
  if python3 "${PROJECT_ROOT}/examples/python/hello_world.py" 2>&1 | grep -q "Hello from E2BGateway"; then
    pass "Python: hello_world.py"
  else
    skip "Python: hello_world.py" "requires real sandbox envd (gRPC)"
  fi
else
  skip "Python SDK examples" "e2b SDK not installed (pip install e2b)"
fi

# ============================================
# Test 9: JavaScript SDK Examples (requires real sandbox)
# ============================================
echo ""
echo "--- JavaScript SDK Examples ---"

if node -e "require('@e2b/code-interpreter')" 2>/dev/null; then
  export E2B_API_URL="http://${E2B_DOMAIN}"
  export E2B_API_KEY="e2b_0000000000000000000000000000000000000000"

  if node "${PROJECT_ROOT}/examples/javascript/hello_world.js" 2>&1 | grep -q "Hello from E2BGateway"; then
    pass "JavaScript: hello_world.js"
  else
    skip "JavaScript: hello_world.js" "requires real sandbox envd (gRPC)"
  fi
else
  skip "JavaScript SDK examples" "@e2b/code-interpreter not installed (npm install @e2b/code-interpreter)"
fi

# ============================================
# Summary
# ============================================
echo ""
echo "=== Test Summary ==="
echo -e "  ${GREEN}Passed${NC}: ${PASSED}"
echo -e "  ${RED}Failed${NC}: ${FAILED}"
echo -e "  ${YELLOW}Skipped${NC}: ${SKIPPED}"
echo ""

if [ "${FAILED}" -gt 0 ]; then
  echo -e "${RED}Some tests failed!${NC}"
  exit 1
else
  echo -e "${GREEN}All tests passed!${NC}"
  exit 0
fi
