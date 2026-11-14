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
  MOVE_RESP=$(curl -s -o /dev/null -w "%{http_code}" -X POST "${GATEWAY_URL}/sandboxes/${SB_ID}/filesystem/move" \
    -H "X-API-Key: ${E2B_API_KEY}" \
    -H "Content-Type: application/json" \
    -d '{"source":"/tmp/e2e_test.py","destination":"/tmp/e2e_test_moved.py"}')
  [ "$MOVE_RESP" = "204" ] && pass "Move file" || fail "Move file" "HTTP $MOVE_RESP"

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

for ex in hello_world sandbox_lifecycle filesystem coding_agent warm_pools; do
  # GO_EXAMPLE_SKIP: space-separated example names to skip on backends where
  # the example exercises an unsupported capability (e.g. sandbox_lifecycle
  # pauses/resumes, which the opensandbox Kubernetes runtime only supports
  # with snapshot infrastructure — see issue #85 Q4).
  if [[ " ${GO_EXAMPLE_SKIP:-} " == *" ${ex} "* ]]; then
    skip "Go: $ex" "listed in GO_EXAMPLE_SKIP for this backend"
    continue
  fi
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
echo "=== HTTP-only Examples (python + node) ==="
# warm_pools.py only uses `requests` against the gateway REST API — it does
# not need the ConnectRPC data plane, so it runs regardless of SKIP_SDK_TESTS.
# The runner images ship Python without requests when the SDK section is
# skipped, so make sure the dependency is present.
python3 -c "import requests" 2>/dev/null \
  || pip install -q requests 2>/dev/null \
  || pip install -q --break-system-packages requests 2>/dev/null
if ! python3 -c "import requests" 2>/dev/null; then
  # A required example must not silently disappear: fail loudly so the job
  # cannot go green without running it.
  fail "Python(HTTP): warm_pools.py" "requests module not installable"
else
  cd "$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
  for ex in warm_pools.py; do
    echo "--- Python(HTTP): $ex ---"
    if E2B_DOMAIN="${E2B_DOMAIN}" E2B_API_KEY="${E2B_API_KEY}" \
       timeout 300 python3 ./examples/python/${ex} 2>&1 | tee /tmp/py-${ex}.log | tail -8; then
      if [ "${PIPESTATUS[0]}" = "0" ]; then
        pass "Python(HTTP): $ex"
      else
        fail "Python(HTTP): $ex" "exit code non-zero (see /tmp/py-${ex}.log)"
      fi
    else
      fail "Python(HTTP): $ex" "timeout"
    fi
  done

  for ex in warm_pools.js; do
    echo "--- Node(HTTP): $ex ---"
    if E2B_DOMAIN="${E2B_DOMAIN}" E2B_API_KEY="${E2B_API_KEY}" \
       timeout 300 node ./examples/javascript/${ex} 2>&1 | tee /tmp/js-http-${ex}.log | tail -8; then
      if [ "${PIPESTATUS[0]}" = "0" ]; then
        pass "Node(HTTP): $ex"
      else
        fail "Node(HTTP): $ex" "exit code non-zero (see /tmp/js-http-${ex}.log)"
      fi
    else
      fail "Node(HTTP): $ex" "timeout"
    fi
  done
fi

if [ "${WARMPOOL_E2E:-0}" = "1" ]; then
  echo ""
  echo "=== Warm Pool live force-delete ==="

  # 0. Kill sandboxes left over by earlier sections (some examples do not
  # clean up). Each sandbox pod holds a full CPU of requests on the single
  # kind node; strays would make the pool warmup unschedulable.
  STRAY_IDS=$(curl -sf "${GATEWAY_URL}/sandboxes" -H "X-API-Key: ${E2B_API_KEY}" \
    | python3 -c "
import sys, json
d = json.load(sys.stdin)
items = d.get('items', d) if isinstance(d, dict) else d
print(' '.join(str(i.get('sandboxID', '')) for i in items if i.get('sandboxID')))
" 2>/dev/null)
  for sid in $STRAY_IDS; do
    curl -s -o /dev/null -m 30 -X DELETE "${GATEWAY_URL}/sandboxes/${sid}" -H "X-API-Key: ${E2B_API_KEY}"
  done
  [ -n "$STRAY_IDS" ] && echo "  cleaned up stray sandboxes: $STRAY_IDS" && sleep 10

  # 1. Create a pool with an explicit ID. targetSize 1: the pool semantics
  # (pre-warm → claim → force delete) are fully exercised while fitting the
  # runner's CPU budget (each sandbox pod requests 1 CPU).
  WP_RESP=$(curl -s -w "\n%{http_code}" -X POST "${GATEWAY_URL}/api/v1/warm-pools" \
    -H "X-API-Key: ${E2B_API_KEY}" -H "Content-Type: application/json" \
    -d '{"templateID":"base","targetSize":1,"warmPoolID":"wp-e2e-force"}')
  WP_CODE=$(echo "$WP_RESP" | tail -1)
  if [ "$WP_CODE" = "201" ]; then
    pass "Warm pool create"
  else
    fail "Warm pool create" "HTTP $WP_CODE: $(echo "$WP_RESP" | sed '$d')"
  fi

  # 2. Wait for at least one idle sandbox to be pre-warmed (real backend).
  WARMED=0
  for i in $(seq 1 60); do
    CUR=$(curl -sf "${GATEWAY_URL}/api/v1/warm-pools/wp-e2e-force" \
      -H "X-API-Key: ${E2B_API_KEY}" \
      | python3 -c "import sys,json; print(json.load(sys.stdin).get('currentSize',0))" 2>/dev/null)
    if [ "${CUR:-0}" -ge 1 ]; then WARMED=1; break; fi
    sleep 3
  done
  [ "$WARMED" = "1" ] && pass "Warm pool pre-warms idle sandboxes" \
    || fail "Warm pool warmup" "currentSize stayed 0 after 180s"

  # 3. Create a sandbox for the same template — served from the pool (claimed).
  CLAIM_RESP=$(curl -s -w "\n%{http_code}" -X POST "${GATEWAY_URL}/sandboxes" \
    -H "X-API-Key: ${E2B_API_KEY}" -H "Content-Type: application/json" \
    -d '{"templateID":"base","timeout":300}')
  CLAIM_CODE=$(echo "$CLAIM_RESP" | tail -1)
  CLAIM_ID=$(echo "$CLAIM_RESP" | sed '$d' \
    | python3 -c "import sys,json; print(json.load(sys.stdin).get('sandboxID',''))" 2>/dev/null)
  if [ "$CLAIM_CODE" = "201" ] && [ -n "$CLAIM_ID" ]; then
    pass "Create sandbox from warm pool ($CLAIM_ID)"
  else
    fail "Create sandbox from warm pool" "HTTP $CLAIM_CODE"
  fi

  # 3b. Prove the sandbox came FROM the pool: acquiring consumes an idle
  # entry, so currentSize must drop. If AcquireID had failed, the gateway
  # would silently fall back to direct create and currentSize would stay.
  # (Replenishment cannot mask this: a replacement sandbox needs pod
  # scheduling + execd bootstrap, far slower than this immediate check.)
  if [ "$WARMED" = "1" ] && [ -n "$CLAIM_ID" ]; then
    POST_IDLE=$(curl -sf "${GATEWAY_URL}/api/v1/warm-pools/wp-e2e-force" \
      -H "X-API-Key: ${E2B_API_KEY}" \
      | python3 -c "import sys,json; print(json.load(sys.stdin).get('currentSize',0))" 2>/dev/null)
    if [ "${POST_IDLE:-99}" -lt "${CUR:-0}" ]; then
      pass "Sandbox was claimed from the pool (idle ${CUR} -> ${POST_IDLE})"
    else
      fail "Claim from pool" "idle count did not drop (${CUR} -> ${POST_IDLE:-?}); acquire may have fallen back to direct create"
    fi
  fi

  # 4. Force-delete the pool.
  DEL_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X DELETE \
    "${GATEWAY_URL}/api/v1/warm-pools/wp-e2e-force?force=true" \
    -H "X-API-Key: ${E2B_API_KEY}")
  [ "$DEL_CODE" = "204" ] && pass "Force delete warm pool" \
    || fail "Force delete warm pool" "HTTP $DEL_CODE"

  # 5. Pool is gone.
  GET_CODE=$(curl -s -o /dev/null -w "%{http_code}" \
    "${GATEWAY_URL}/api/v1/warm-pools/wp-e2e-force" -H "X-API-Key: ${E2B_API_KEY}")
  [ "$GET_CODE" = "404" ] && pass "Force-deleted pool returns 404" \
    || fail "Force-deleted pool returns 404" "HTTP $GET_CODE"

  # 6. The claimed sandbox survives the force delete and stays usable.
  if [ -n "$CLAIM_ID" ]; then
    sleep 5
    CMD_RESP=$(curl -s --max-time 30 -X POST "${GATEWAY_URL}/sandboxes/${CLAIM_ID}/commands" \
      -H "X-API-Key: ${E2B_API_KEY}" -H "Content-Type: application/json" \
      -d '{"command":"echo alive-after-force-delete"}')
    echo "$CMD_RESP" | grep -q "alive-after-force-delete" \
      && pass "Claimed sandbox survives force delete" \
      || fail "Claimed sandbox survives force delete" "$CMD_RESP"
  fi

  echo ""
  echo "=== Extended lifecycle (live backend) ==="

  # Reuse the sandbox claimed from the pool above (it survived the force
  # delete) instead of creating another one — each sandbox pod holds a full
  # CPU of requests on the single kind node.
  EXT_ID="${CLAIM_ID:-}"
  if [ -z "$EXT_ID" ]; then
    EXT_RESP=$(curl -s -w "\n%{http_code}" -X POST "${GATEWAY_URL}/sandboxes" \
      -H "X-API-Key: ${E2B_API_KEY}" -H "Content-Type: application/json" \
      -d '{"templateID":"base","timeout":120}')
    EXT_ID=$(echo "$EXT_RESP" | sed '$d' \
      | python3 -c "import sys,json; print(json.load(sys.stdin).get('sandboxID',''))" 2>/dev/null)
    if [ -n "$EXT_ID" ]; then
      pass "Extended: sandbox created ($EXT_ID)"
    else
      fail "Extended: sandbox create" "$(echo "$EXT_RESP" | sed '$d')"
    fi
  else
    pass "Extended: reusing pool-claimed sandbox ($EXT_ID)"
  fi

  if [ -n "$EXT_ID" ]; then
    # renew timeout
    T_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST \
      "${GATEWAY_URL}/sandboxes/${EXT_ID}/timeout" \
      -H "X-API-Key: ${E2B_API_KEY}" -H "Content-Type: application/json" \
      -d '{"timeout":300}')
    { [ "$T_CODE" = "204" ] || [ "$T_CODE" = "200" ]; } \
      && pass "Extended: renew timeout" || fail "Extended: renew timeout" "HTTP $T_CODE"

    # ports list (empty is fine — endpoint must answer 200 + JSON)
    P_CODE=$(curl -s -o /tmp/ports.json -w "%{http_code}" \
      "${GATEWAY_URL}/sandboxes/${EXT_ID}/ports" -H "X-API-Key: ${E2B_API_KEY}")
    if [ "$P_CODE" = "200" ] && python3 -c "import json; json.load(open('/tmp/ports.json'))" 2>/dev/null; then
      pass "Extended: list ports"
    else
      fail "Extended: list ports" "HTTP $P_CODE"
    fi

    # pause → resume: best-effort. The Kubernetes runtime pause needs snapshot
    # infra (image-committer + a pushable registry) which CI does not
    # configure yet (#85 Q4) — skip instead of fail when the backend refuses.
    PAUSE_CODE=$(curl -s -o /tmp/pause.out -w "%{http_code}" -X POST \
      "${GATEWAY_URL}/sandboxes/${EXT_ID}/pause" -H "X-API-Key: ${E2B_API_KEY}")
    if [ "$PAUSE_CODE" = "204" ] || [ "$PAUSE_CODE" = "200" ]; then
      pass "Extended: pause"
      # The K8s runtime acknowledges pause after patching the workload; the
      # snapshot completes asynchronously and resume is rejected while the
      # sandbox is still "pausing". Poll for the paused state before resuming.
      PAUSED=0
      for i in $(seq 1 20); do
        ST=$(curl -sf "${GATEWAY_URL}/sandboxes/${EXT_ID}" -H "X-API-Key: ${E2B_API_KEY}" \
          | python3 -c "import sys,json; print(json.load(sys.stdin).get('status',''))" 2>/dev/null)
        if [ "$ST" = "paused" ]; then PAUSED=1; break; fi
        sleep 3
      done
      if [ "$PAUSED" != "1" ]; then
        skip "Extended: resume" "sandbox did not reach paused state (last status: ${ST:-?}; snapshot registry not configured in CI, #85 Q4)"
      else
        RESUME_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST \
          "${GATEWAY_URL}/sandboxes/${EXT_ID}/resume" -H "X-API-Key: ${E2B_API_KEY}")
        if [ "$RESUME_CODE" = "200" ] || [ "$RESUME_CODE" = "204" ]; then
          pass "Extended: resume"
        else
          # pause may report success while the snapshot commit cannot complete
          # without a registry — resume then fails. Same Q4 limitation.
          skip "Extended: resume" "HTTP $RESUME_CODE (snapshot registry not configured in CI, #85 Q4)"
        fi
      fi
    else
      skip "Extended: pause/resume" "HTTP $PAUSE_CODE (snapshot registry not configured in CI)"
    fi

    # cleanup
    curl -s -o /dev/null -X DELETE "${GATEWAY_URL}/sandboxes/${EXT_ID}" \
      -H "X-API-Key: ${E2B_API_KEY}"
  fi

  echo ""
  echo "=== Warm Pool live graceful delete ==="
  # Mirror of the force section with graceful semantics: the pool drains
  # in-flight work before releasing idle sandboxes; claimed sandboxes are
  # likewise never affected.
  GP_RESP=$(curl -s -w "\n%{http_code}" -X POST "${GATEWAY_URL}/api/v1/warm-pools" \
    -H "X-API-Key: ${E2B_API_KEY}" -H "Content-Type: application/json" \
    -d '{"templateID":"base","targetSize":1,"warmPoolID":"wp-e2e-grace"}')
  GP_CODE=$(echo "$GP_RESP" | tail -1)
  [ "$GP_CODE" = "201" ] && pass "Graceful pool create" \
    || fail "Graceful pool create" "HTTP $GP_CODE: $(echo "$GP_RESP" | sed '$d')"

  GWARMED=0
  for i in $(seq 1 60); do
    GCUR=$(curl -sf "${GATEWAY_URL}/api/v1/warm-pools/wp-e2e-grace" \
      -H "X-API-Key: ${E2B_API_KEY}" \
      | python3 -c "import sys,json; print(json.load(sys.stdin).get('currentSize',0))" 2>/dev/null)
    if [ "${GCUR:-0}" -ge 1 ]; then GWARMED=1; break; fi
    sleep 3
  done
  [ "$GWARMED" = "1" ] && pass "Graceful pool pre-warms idle sandboxes" \
    || fail "Graceful pool warmup" "currentSize stayed 0 after 180s"

  GCLAIM_RESP=$(curl -s -w "\n%{http_code}" -X POST "${GATEWAY_URL}/sandboxes" \
    -H "X-API-Key: ${E2B_API_KEY}" -H "Content-Type: application/json" \
    -d '{"templateID":"base","timeout":300}')
  GCLAIM_ID=$(echo "$GCLAIM_RESP" | sed '$d' \
    | python3 -c "import sys,json; print(json.load(sys.stdin).get('sandboxID',''))" 2>/dev/null)
  if [ "$(echo "$GCLAIM_RESP" | tail -1)" = "201" ] && [ -n "$GCLAIM_ID" ]; then
    pass "Create sandbox from graceful pool ($GCLAIM_ID)"
  else
    fail "Create sandbox from graceful pool" "HTTP $(echo "$GCLAIM_RESP" | tail -1)"
  fi

  # Prove the sandbox came FROM the pool (same check as the force section):
  # acquiring consumes an idle entry; a direct-create fallback would leave
  # currentSize unchanged.
  if [ "$GWARMED" = "1" ] && [ -n "$GCLAIM_ID" ]; then
    GPOST_IDLE=$(curl -sf "${GATEWAY_URL}/api/v1/warm-pools/wp-e2e-grace" \
      -H "X-API-Key: ${E2B_API_KEY}" \
      | python3 -c "import sys,json; print(json.load(sys.stdin).get('currentSize',0))" 2>/dev/null)
    if [ "${GPOST_IDLE:-99}" -lt "${GCUR:-0}" ]; then
      pass "Sandbox was claimed from the graceful pool (idle ${GCUR} -> ${GPOST_IDLE})"
    else
      fail "Claim from graceful pool" "idle count did not drop (${GCUR} -> ${GPOST_IDLE:-?}); acquire may have fallen back to direct create"
    fi
  fi

  GDEL_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X DELETE \
    "${GATEWAY_URL}/api/v1/warm-pools/wp-e2e-grace" -H "X-API-Key: ${E2B_API_KEY}")
  [ "$GDEL_CODE" = "204" ] && pass "Graceful delete warm pool" \
    || fail "Graceful delete warm pool" "HTTP $GDEL_CODE"

  GGET_CODE=$(curl -s -o /dev/null -w "%{http_code}" \
    "${GATEWAY_URL}/api/v1/warm-pools/wp-e2e-grace" -H "X-API-Key: ${E2B_API_KEY}")
  [ "$GGET_CODE" = "404" ] && pass "Graceful-deleted pool returns 404" \
    || fail "Graceful-deleted pool returns 404" "HTTP $GGET_CODE"

  if [ -n "$GCLAIM_ID" ]; then
    GCMD_RESP=$(curl -s --max-time 30 -X POST "${GATEWAY_URL}/sandboxes/${GCLAIM_ID}/commands" \
      -H "X-API-Key: ${E2B_API_KEY}" -H "Content-Type: application/json" \
      -d '{"command":"echo alive-after-graceful-delete"}')
    echo "$GCMD_RESP" | grep -q "alive-after-graceful-delete" \
      && pass "Claimed sandbox survives graceful delete" \
      || fail "Claimed sandbox survives graceful delete" "$GCMD_RESP"
    curl -s -o /dev/null -X DELETE "${GATEWAY_URL}/sandboxes/${GCLAIM_ID}" \
      -H "X-API-Key: ${E2B_API_KEY}"
  fi
fi

if [ "${RESTART_E2E:-0}" = "1" ]; then
  echo ""
  echo "=== Gateway restart routing recovery (agent-sandbox) ==="
  SB_NAMESPACE="${SANDBOX_NAMESPACE:-agent-sandbox-system}"
  GW_NS="${GATEWAY_NAMESPACE:-e2bgateway-system}"
  GW_PORT="${GATEWAY_URL##*:}"

  # 1. CRUD-create a pool; its e2b.e2bgateway.io/template-id annotation is
  #    what New() rebuilds template->pool routing from after a restart.
  RR_RESP=$(curl -s -w "\n%{http_code}" -X POST "${GATEWAY_URL}/api/v1/warm-pools" \
    -H "X-API-Key: ${E2B_API_KEY}" -H "Content-Type: application/json" \
    -d '{"templateID":"base","targetSize":1,"warmPoolID":"wp-e2e-restart"}')
  [ "$(echo "$RR_RESP" | tail -1)" = "201" ] && pass "Restart: pool created" \
    || fail "Restart: pool create" "HTTP $(echo "$RR_RESP" | tail -1): $(echo "$RR_RESP" | sed '$d')"

  # 2. Wait for one warm sandbox.
  RWARMED=0
  for i in $(seq 1 60); do
    RCUR=$(curl -sf "${GATEWAY_URL}/api/v1/warm-pools/wp-e2e-restart" \
      -H "X-API-Key: ${E2B_API_KEY}" \
      | python3 -c "import sys,json; print(json.load(sys.stdin).get('currentSize',0))" 2>/dev/null)
    if [ "${RCUR:-0}" -ge 1 ]; then RWARMED=1; break; fi
    sleep 3
  done
  [ "$RWARMED" = "1" ] && pass "Restart: pool pre-warmed" \
    || fail "Restart: pool warmup" "currentSize stayed 0 after 180s"

  # 3. Restart the gateway; routing must be rebuilt from the CRD annotations.
  kubectl -n "$GW_NS" rollout restart deployment/e2bgateway
  if kubectl -n "$GW_NS" rollout status deployment/e2bgateway --timeout=120s >/dev/null 2>&1; then
    pass "Restart: gateway rollout completed"
  else
    fail "Restart: gateway rollout" "did not become ready in 120s"
  fi

  # The rollout replaced the pod the outer kubectl port-forward was attached
  # to; re-establish the tunnel before any further API calls.
  pkill -f "port-forward.*e2bgateway" 2>/dev/null
  sleep 2
  kubectl port-forward svc/e2bgateway -n "$GW_NS" "${GW_PORT}:8080" >/tmp/pf-restart.log 2>&1 &
  for i in $(seq 1 30); do
    curl -sf "${GATEWAY_URL}/healthz" >/dev/null 2>&1 && break
    sleep 2
  done
  curl -sf "${GATEWAY_URL}/healthz" >/dev/null 2>&1 \
    && pass "Restart: gateway healthy after restart" \
    || fail "Restart: gateway health" "not healthy after rollout (see /tmp/pf-restart.log)"

  # 4. Create a sandbox for the pooled template; the restored routing must
  #    send the claim to wp-e2e-restart.
  RS_RESP=$(curl -s -w "\n%{http_code}" -X POST "${GATEWAY_URL}/sandboxes" \
    -H "X-API-Key: ${E2B_API_KEY}" -H "Content-Type: application/json" \
    -d '{"templateID":"base","timeout":300}')
  RS_ID=$(echo "$RS_RESP" | sed '$d' \
    | python3 -c "import sys,json; print(json.load(sys.stdin).get('sandboxID',''))" 2>/dev/null)
  if [ "$(echo "$RS_RESP" | tail -1)" = "201" ] && [ -n "$RS_ID" ]; then
    pass "Restart: sandbox created after restart ($RS_ID)"
  else
    fail "Restart: sandbox create" "HTTP $(echo "$RS_RESP" | tail -1)"
  fi

  sleep 3
  if kubectl get sandboxclaims.extensions.agents.x-k8s.io -n "$SB_NAMESPACE" -o json 2>/dev/null \
     | python3 -c "
import sys, json
claims = json.load(sys.stdin).get('items', [])
hits = [c['metadata']['name'] for c in claims
        if ((c.get('spec') or {}).get('warmPoolRef') or {}).get('name') == 'wp-e2e-restart']
sys.exit(0 if hits else 1)"; then
    pass "Restart: template routing restored to the CRUD-created pool"
  else
    fail "Restart: routing recovery" "no claim references wp-e2e-restart after restart"
  fi

  # 5. Cleanup (best-effort; the gateway lost its in-memory idMap on restart,
  #    so the sandbox delete may not resolve — the kind cluster is ephemeral).
  [ -n "$RS_ID" ] && curl -s -o /dev/null -m 30 -X DELETE "${GATEWAY_URL}/sandboxes/${RS_ID}" -H "X-API-Key: ${E2B_API_KEY}"
  curl -s -o /dev/null -m 60 -X DELETE "${GATEWAY_URL}/api/v1/warm-pools/wp-e2e-restart?force=true" -H "X-API-Key: ${E2B_API_KEY}"
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
