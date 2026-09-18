#!/usr/bin/env bash
# Run the same authentication probes against either Kind backend without
# changing the normal E2E gateway's credentials or rate-limit settings.
set -euo pipefail

BACKEND="${1:?usage: test-auth-failures.sh agent-sandbox|opensandbox}"
case "${BACKEND}" in
  agent-sandbox)
    BACKEND_CONFIG='namespace: agent-sandbox-system
          warmPoolName: base
          templateToWarmPool:
            base: base'
    ;;
  opensandbox)
    BACKEND_CONFIG='baseURL: http://opensandbox-controller.sandbox-system:8080/v1
          templateToImage:
            base: sandbox-with-envd:local'
    ;;
  *) echo "unknown backend: ${BACKEND}" >&2; exit 2 ;;
esac

FIXTURE="$(go run ./hack/kind-e2e/auth-fixture)"
fixture_field() {
  FIXTURE_JSON="${FIXTURE}" python3 -c 'import json,os,sys; print(json.loads(os.environ["FIXTURE_JSON"])[sys.argv[1]])' "$1"
}

cat <<YAML | kubectl apply -f -
apiVersion: v1
kind: ConfigMap
metadata:
  name: e2bgateway-auth-probe-config
  namespace: e2bgateway-system
data:
  config.yaml: |
    server:
      http:
        address: "0.0.0.0:8080"
    auth:
      providers:
        - type: apikey
          keys: ["auth-probe-key"]
        - type: jwt
          issuer: "https://kind-e2e-issuer.example.test"
          audience: "e2bgateway-kind-e2e"
          jwks: '$(fixture_field jwks)'
    rateLimit:
      enabled: true
      defaultLimit:
        requestsPerMinute: 1
        burstSize: 1
    backends:
      - name: ${BACKEND}
        type: ${BACKEND}
        enabled: true
        config:
          ${BACKEND_CONFIG}
    routing:
      defaultBackend: ${BACKEND}
      strategy: static
YAML

cat <<'YAML' | kubectl apply -f -
apiVersion: apps/v1
kind: Deployment
metadata:
  name: e2bgateway-auth-probe
  namespace: e2bgateway-system
spec:
  replicas: 1
  selector:
    matchLabels:
      app: e2bgateway-auth-probe
  template:
    metadata:
      labels:
        app: e2bgateway-auth-probe
    spec:
      serviceAccountName: e2bgateway
      containers:
        - name: e2bgateway
          image: e2bgateway:local
          imagePullPolicy: IfNotPresent
          ports:
            - containerPort: 8080
          volumeMounts:
            - name: config
              mountPath: /etc/e2bgateway
              readOnly: true
      volumes:
        - name: config
          configMap:
            name: e2bgateway-auth-probe-config
---
apiVersion: v1
kind: Service
metadata:
  name: e2bgateway-auth-probe
  namespace: e2bgateway-system
spec:
  selector:
    app: e2bgateway-auth-probe
  ports:
    - port: 8080
      targetPort: 8080
YAML

kubectl wait --for=condition=available deployment/e2bgateway-auth-probe -n e2bgateway-system --timeout=120s
kubectl port-forward svc/e2bgateway-auth-probe -n e2bgateway-system 18080:8080 >/dev/null 2>&1 &
PF_PID=$!
HEADERS_FILE="$(mktemp)"
BODY_FILE="$(mktemp)"
trap 'kill "${PF_PID}" 2>/dev/null || true; rm -f "${HEADERS_FILE}" "${BODY_FILE}"' EXIT

URL=http://localhost:18080/sandboxes
for i in $(seq 1 30); do
  if curl -sf http://localhost:18080/healthz >/dev/null 2>&1; then break; fi
  sleep 1
done

expect_status() {
  local label="$1" expected="$2"
  shift 2
  local actual
  actual="$(curl -sS -D "${HEADERS_FILE}" -o "${BODY_FILE}" -w '%{http_code}' "$@" "${URL}")"
  if [ "${actual}" != "${expected}" ]; then
    echo "${BACKEND}: ${label}: expected ${expected}, got ${actual}" >&2
    exit 1
  fi
  if [ "${expected}" != 200 ]; then
    python3 -c 'import json,sys; body=json.load(open(sys.argv[1], encoding="utf-8")); assert body["error"]["code"] == int(sys.argv[2])' "${BODY_FILE}" "${expected}"
  fi
  if [ "${expected}" = 429 ] && ! grep -qi '^Retry-After: ' "${HEADERS_FILE}"; then
    echo "${BACKEND}: ${label}: missing Retry-After header" >&2
    exit 1
  fi
  echo "${BACKEND}: ${label}: ${actual}"
}

expect_status 'missing API key' 401
expect_status 'invalid API key' 401 -H 'X-API-Key: invalid-key'
expect_status 'expired JWT' 401 -H "Authorization: Bearer $(fixture_field expired)"
expect_status 'insufficient scope' 403 -H "Authorization: Bearer $(fixture_field insufficient)"
expect_status 'valid JWT' 200 -H "Authorization: Bearer $(fixture_field valid)"
expect_status 'rate limit first request' 200 -H "Authorization: Bearer $(fixture_field rate)"
expect_status 'rate limit exceeded' 429 -H "Authorization: Bearer $(fixture_field rate)"
