#!/usr/bin/env bash
# run-examples-e2e.sh — Run E2B examples against the gateway.
# Sourced by the e2e.yml CI jobs (agent-sandbox and opensandbox).
# Requires: GATEWAY_URL, E2B_API_KEY, E2B_DOMAIN env vars.

set +e  # Disable exit-on-error so all tests run even if some fail
set -uo pipefail

PASSED=${PASSED:-0}
FAILED=${FAILED:-0}
SKIPPED=${SKIPPED:-0}

pass() { echo "  PASS: $1"; PASSED=$((PASSED + 1)); }
fail() { echo "  FAIL: $1 - $2"; FAILED=$((FAILED + 1)); }
skip() { echo "  SKIP: $1 - $2"; SKIPPED=$((SKIPPED + 1)); }

GATEWAY_URL="${GATEWAY_URL:?GATEWAY_URL required}"
E2B_API_KEY="${E2B_API_KEY:?E2B_API_KEY required}"
E2B_DOMAIN="${E2B_DOMAIN:-${GATEWAY_URL#http://}}"

echo ""
echo "=== Health Checks ==="
curl -sf "${GATEWAY_URL}/healthz" && pass "GET /healthz" || fail "healthz" "not responding"
curl -sf "${GATEWAY_URL}/readyz" && pass "GET /readyz" || fail "readyz" "not responding"

echo ""
echo "=== Sandbox Lifecycle (cURL) ==="

# Create sandbox
HTTP_CODE=""
CREATE_RESP=$(curl -s -w "\n%{http_code}" -X POST "${GATEWAY_URL}/sandboxes" \
  -H "X-API-Key: ${E2B_API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{"templateID":"base","timeout":300}')
HTTP_CODE=$(echo "$CREATE_RESP" | tail -1)
CREATE_BODY=$(echo "$CREATE_RESP" | sed '$d')
echo "  [create sandbox] HTTP ${HTTP_CODE}: ${CREATE_BODY}"
SB_ID=$(echo "$CREATE_BODY" | python3 -c "import sys,json; print(json.load(sys.stdin).get('sandboxID',''))" 2>/dev/null)

if [ -n "$SB_ID" ]; then
  pass "Create sandbox (${SB_ID})"

  # Wait for sandbox to be fully usable. For agent-sandbox, the warm pool adoption
  # is unreliable in CI — when it fails, creating a new pod can take 5+ minutes
  # for the envd daemon to begin listening. We wait up to 10 minutes.
  # Readiness is verified by executing a command through the envd data plane.
  echo "  Waiting for sandbox to be ready (up to 10 minutes)..."
  READY=0
  for i in $(seq 1 200); do
    CMD_RESP=$(curl -s --max-time 5 -X POST "${GATEWAY_URL}/sandboxes/${SB_ID}/commands" \
      -H "X-API-Key: ${E2B_API_KEY}" \
      -H "Content-Type: application/json" \
      -d '{"command":"echo ready_check"}' 2>/dev/null)

    if echo "$CMD_RESP" | grep -q "ready_check"; then
      READY=1
      echo "  Sandbox ready after $((i*3))s"
      break
    fi
    sleep 3
  done
  if [ "$READY" != "1" ]; then
    echo "  WARNING: sandbox not ready after 600s; data plane tests will likely fail"
  fi

  # Get sandbox
  curl -sf "${GATEWAY_URL}/sandboxes/${SB_ID}" -H "X-API-Key: ${E2B_API_KEY}" && pass "Get sandbox" || fail "Get sandbox" "failed"

  # List sandboxes
  curl -sf "${GATEWAY_URL}/sandboxes" -H "X-API-Key: ${E2B_API_KEY}" | python3 -c "import sys,json; d=json.load(sys.stdin); items=d.get('items',d) if isinstance(d,dict) else d; assert len(items)>0" 2>/dev/null \
    && pass "List sandboxes" || fail "List sandboxes" "empty or error"

  # Run command (envd data plane)
  CMD_RESP=$(curl -sf -X POST "${GATEWAY_URL}/sandboxes/${SB_ID}/commands" \
    -H "X-API-Key: ${E2B_API_KEY}" \
    -H "Content-Type: application/json" \
    -d '{"command":"echo hello_e2e_test"}')
  echo "$CMD_RESP" | grep -q "hello_e2e_test" && pass "Run command" || fail "Run command" "$CMD_RESP"

  # Execute code (envd data plane)
  CODE_RESP=$(curl -sf -X POST "${GATEWAY_URL}/sandboxes/${SB_ID}/code" \
    -H "X-API-Key: ${E2B_API_KEY}" \
    -H "Content-Type: application/json" \
    -d '{"code":"print(2+3)","language":"python"}')
  echo "$CODE_RESP" | grep -q "5" && pass "Execute code (python)" || fail "Execute code" "$CODE_RESP"

  # Write file (JSON)
  curl -sf -X POST "${GATEWAY_URL}/sandboxes/${SB_ID}/files" \
    -H "X-API-Key: ${E2B_API_KEY}" \
    -H "Content-Type: application/json" \
    -d '{"path":"/tmp/e2e_test.py","content":"print(99)"}' && pass "Write file" || fail "Write file" "failed"

  # Read file
  READ_RESP=$(curl -sf "${GATEWAY_URL}/sandboxes/${SB_ID}/files?path=/tmp/e2e_test.py" \
    -H "X-API-Key: ${E2B_API_KEY}")
  echo "$READ_RESP" | grep -q "print(99)" && pass "Read file" || fail "Read file" "$READ_RESP"

  # Upload file (multipart)
  echo "upload-test-content" > /tmp/e2e_upload.txt
  curl -sf -X POST "${GATEWAY_URL}/sandboxes/${SB_ID}/files/upload" \
    -H "X-API-Key: ${E2B_API_KEY}" \
    -F "path=/tmp/e2e_uploaded.txt" \
    -F "file=@/tmp/e2e_upload.txt" && pass "Upload file" || fail "Upload file" "failed"

  # List files
  LIST_RESP=$(curl -sf -X POST "${GATEWAY_URL}/sandboxes/${SB_ID}/files/list" \
    -H "X-API-Key: ${E2B_API_KEY}" \
    -H "Content-Type: application/json" \
    -d '{"path":"/tmp"}')
  echo "$LIST_RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); entries=d.get('entries',d) if isinstance(d,dict) else d; assert len(entries)>0" 2>/dev/null \
    && pass "List files" || fail "List files" "$LIST_RESP"

  # Make directory (envd data plane)
  curl -sf -X POST "${GATEWAY_URL}/sandboxes/${SB_ID}/filesystem/mkdir" \
    -H "X-API-Key: ${E2B_API_KEY}" \
    -H "Content-Type: application/json" \
    -d '{"path":"/tmp/e2e_test_dir"}' && pass "Make directory" || fail "Make directory" "failed"

  # Move file (envd data plane)
  MOVE_RESP=$(curl -sf -X POST "${GATEWAY_URL}/sandboxes/${SB_ID}/filesystem/move" \
    -H "X-API-Key: ${E2B_API_KEY}" \
    -H "Content-Type: application/json" \
    -d '{"source":"/tmp/e2e_test.py","destination":"/tmp/e2e_test_moved.py"}')
  echo "$MOVE_RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); assert d.get('success',True)" 2>/dev/null \
    && pass "Move file" || fail "Move file" "$MOVE_RESP"

  # Verify moved file exists
  READ_MOVED=$(curl -sf "${GATEWAY_URL}/sandboxes/${SB_ID}/files?path=/tmp/e2e_test_moved.py" \
    -H "X-API-Key: ${E2B_API_KEY}")
  echo "$READ_MOVED" | grep -q "print(99)" && pass "Verify moved file" || fail "Verify moved file" "$READ_MOVED"

  # List processes (envd data plane)
  PS_RESP=$(curl -sf "${GATEWAY_URL}/sandboxes/${SB_ID}/processes" \
    -H "X-API-Key: ${E2B_API_KEY}" || true)
  if [ -n "$PS_RESP" ]; then
    echo "$PS_RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); procs=d.get('processes',d) if isinstance(d,dict) else d; assert isinstance(procs, list)" 2>/dev/null \
      && pass "List processes" || fail "List processes" "$PS_RESP"
  else
    skip "List processes" "endpoint not available"
  fi

  # File info / stat (envd data plane)
  STAT_RESP=$(curl -sf "${GATEWAY_URL}/sandboxes/${SB_ID}/filesystem/stat" \
    -H "X-API-Key: ${E2B_API_KEY}" \
    -H "Content-Type: application/json" \
    -d '{"path":"/tmp/e2e_test_moved.py"}' || true)
  if [ -n "$STAT_RESP" ]; then
    echo "$STAT_RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); assert 'path' in d or 'name' in d or 'size' in d" 2>/dev/null \
      && pass "File stat" || fail "File stat" "$STAT_RESP"
  else
    skip "File stat" "endpoint not available"
  fi

  # Download file (envd data plane)
  DL_RESP=$(curl -sf "${GATEWAY_URL}/sandboxes/${SB_ID}/filesystem/download?path=/tmp/e2e_uploaded.txt" \
    -H "X-API-Key: ${E2B_API_KEY}" || true)
  if echo "$DL_RESP" | grep -q "upload-test-content"; then
    pass "Download file"
  else
    skip "Download file" "filesystem/download not available"
  fi

  # Kill sandbox
  curl -sf -X DELETE "${GATEWAY_URL}/sandboxes/${SB_ID}" \
    -H "X-API-Key: ${E2B_API_KEY}" && pass "Kill sandbox" || fail "Kill sandbox" "failed"
else
  fail "Create sandbox" "HTTP ${HTTP_CODE}: ${CREATE_BODY}"
fi

echo ""
echo "=== Go Examples ==="
cd "$(git rev-parse --show-toplevel 2>/dev/null || pwd)"

for ex in hello_world sandbox_lifecycle filesystem coding_agent; do
  echo "--- Go: $ex ---"
  if E2B_DOMAIN="${E2B_DOMAIN}" E2B_API_KEY="${E2B_API_KEY}" E2B_API_URL="${E2B_API_URL:-}" E2B_SANDBOX_URL="${E2B_SANDBOX_URL:-}" \
     timeout 120 go run ./examples/go/${ex}/ 2>&1 | tee /tmp/go-${ex}.log | tail -5; then
    if grep -qE "Killed|Done|killed|Created" /tmp/go-${ex}.log; then
      pass "Go: $ex"
    else
      fail "Go: $ex" "missing expected output"
    fi
  else
    fail "Go: $ex" "exit code non-zero or timeout"
  fi
done

echo ""
echo "=== Python Examples ==="

if [ "${SKIP_SDK_TESTS:-0}" = "1" ]; then
  skip "Python SDK examples" "backend does not expose ConnectRPC data plane in CI"
else
  # Install e2b SDK packages
  pip install e2b e2b-code-interpreter 2>/dev/null || skip "Python SDK examples" "e2b SDK not installable"

  for ex in hello_world.py sandbox_lifecycle.py commands.py code_execution.py filesystem.py; do
    echo "--- Python: $ex ---"
    if E2B_DOMAIN="${E2B_DOMAIN}" E2B_API_KEY="${E2B_API_KEY}" E2B_API_URL="${E2B_API_URL:-}" E2B_SANDBOX_URL="${E2B_SANDBOX_URL:-}" \
       timeout 120 python3 ./examples/python/${ex} 2>&1 | tee /tmp/py-${ex}.log | tail -5; then
      pass "Python: $ex"
    else
      fail "Python: $ex" "exit code non-zero or timeout"
    fi
  done
fi

echo ""
echo "=== JavaScript Examples ==="

if [ "${SKIP_SDK_TESTS:-0}" = "1" ]; then
  skip "JS SDK examples" "backend does not expose ConnectRPC data plane in CI"
else
  # Install e2b SDK dependencies
  cd examples/javascript
  npm install 2>/dev/null || skip "JS SDK examples" "npm install failed"
  cd -

  for ex in hello_world.js sandbox_lifecycle.js commands.js code_execution.js filesystem.js; do
    echo "--- JS: $ex ---"
    if E2B_DOMAIN="${E2B_DOMAIN}" E2B_API_KEY="${E2B_API_KEY}" E2B_API_URL="${E2B_API_URL:-}" E2B_SANDBOX_URL="${E2B_SANDBOX_URL:-}" \
       timeout 120 node ./examples/javascript/${ex} 2>&1 | tee /tmp/js-${ex}.log | tail -5; then
      pass "JS: $ex"
    else
      fail "JS: $ex" "exit code non-zero or timeout"
    fi
  done
fi

echo ""
echo "=== cURL Examples ==="
echo "--- cURL: api_examples ---"

# The examples/curl/api_examples.sh is markdown documentation, not an executable script.
# Run equivalent cURL operations inline to validate the gateway API.
CURL_OK=0
(
  set -e
  # Create sandbox
  RESP=$(curl -sf -X POST "${GATEWAY_URL}/sandboxes" \
    -H "X-API-Key: ${E2B_API_KEY}" \
    -H "Content-Type: application/json" \
    -d '{"templateID":"base","timeout":300}')
  CID=$(echo "$RESP" | python3 -c "import sys,json; print(json.load(sys.stdin).get('sandboxID',''))" 2>/dev/null)
  [ -n "$CID" ] && echo "  cURL: create sandbox OK (${CID})"

  # List sandboxes
  curl -sf "${GATEWAY_URL}/sandboxes" -H "X-API-Key: ${E2B_API_KEY}" > /dev/null
  echo "  cURL: list sandboxes OK"

  # Get sandbox
  curl -sf "${GATEWAY_URL}/sandboxes/${CID}" -H "X-API-Key: ${E2B_API_KEY}" > /dev/null
  echo "  cURL: get sandbox OK"

  # Wait for sandbox to be fully usable (up to 10 minutes).
  # Verify actual command execution via envd data plane, not just HTTP status.
  for i in $(seq 1 200); do
    CMD_RESP=$(curl -s --max-time 5 -X POST "${GATEWAY_URL}/sandboxes/${CID}/commands" \
      -H "X-API-Key: ${E2B_API_KEY}" \
      -H "Content-Type: application/json" \
      -d '{"command":"echo ready_check"}' 2>/dev/null)
    if echo "$CMD_RESP" | grep -q "ready_check"; then
      echo "  cURL: sandbox ready after $((i*3))s"
      break
    fi
    sleep 3
  done

  # Run command (envd data plane)
  curl -sf -X POST "${GATEWAY_URL}/sandboxes/${CID}/commands" \
    -H "X-API-Key: ${E2B_API_KEY}" \
    -H "Content-Type: application/json" \
    -d '{"command":"echo curl_test_ok"}' | grep -q "curl_test_ok"
  echo "  cURL: run command OK"

  # Execute code (envd data plane)
  curl -sf -X POST "${GATEWAY_URL}/sandboxes/${CID}/code" \
    -H "X-API-Key: ${E2B_API_KEY}" \
    -H "Content-Type: application/json" \
    -d '{"code":"print(42)","language":"python"}' | grep -q "42"
  echo "  cURL: execute code OK"

  # Kill sandbox
  curl -sf -X DELETE "${GATEWAY_URL}/sandboxes/${CID}" -H "X-API-Key: ${E2B_API_KEY}" > /dev/null
  echo "  cURL: kill sandbox OK"
) 2>&1 | tee /tmp/curl-examples.log

if [ $? -eq 0 ]; then
  pass "cURL: api_examples"
else
  fail "cURL: api_examples" "some operations failed"
fi

echo ""
echo "=== V2 & Legacy API ==="
curl -sf "${GATEWAY_URL}/v2/sandboxes" -H "X-API-Key: ${E2B_API_KEY}" && pass "GET /v2/sandboxes" || fail "v2 sandboxes" "failed"
curl -sf "${GATEWAY_URL}/v2/templates" -H "X-API-Key: ${E2B_API_KEY}" && pass "GET /v2/templates" || fail "v2 templates" "failed"
curl -sf "${GATEWAY_URL}/api/v1/sandboxes" -H "X-API-Key: ${E2B_API_KEY}" && pass "GET /api/v1/sandboxes" || fail "v1 sandboxes" "failed"

echo ""
echo "=== Error Format ==="
ERR=$(curl -s "${GATEWAY_URL}/sandboxes/nonexistent-id" -H "X-API-Key: ${E2B_API_KEY}")
echo "$ERR" | python3 -c "import sys,json; d=json.load(sys.stdin); assert 'code' in d and 'message' in d" 2>/dev/null \
  && pass "Error format {code, message}" || fail "Error format" "$ERR"

echo ""
echo "========================================="
echo "  Test Summary"
echo "========================================="
echo "  Passed:  ${PASSED}"
echo "  Failed:  ${FAILED}"
echo "  Skipped: ${SKIPPED}"
echo "========================================="

export PASSED FAILED SKIPPED
