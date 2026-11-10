#!/usr/bin/env bash
# Run agent-gateway on this machine with a demo provider, so the console has data to show.
#   ./scripts/run-local.sh            # demo: local mock provider + 2 demo models + an API key
#   DEMO=0 ./scripts/run-local.sh     # no demo data; add your real providers in the console
#   PORT=9090 ./scripts/run-local.sh  # another port
# Data and the generated .env.local live next to this script's repo root. Stop with Ctrl-C.
set -euo pipefail
cd "$(dirname "$0")/.."
PORT=${PORT:-8080}
DEMO=${DEMO:-1}
B="http://127.0.0.1:$PORT"

case "$(uname -s)-$(uname -m)" in
  Darwin-arm64) SUFFIX=darwin-arm64 ;;
  Darwin-x86_64) SUFFIX=darwin-amd64 ;;
  Linux-x86_64) SUFFIX=linux-amd64 ;;
  Linux-aarch64 | Linux-arm64) SUFFIX=linux-arm64 ;;
  *) echo "unsupported platform $(uname -s)-$(uname -m); build with: make build" >&2; exit 1 ;;
esac
GW=bin/gateway-$SUFFIX
MOCK=bin/mockprovider-$SUFFIX
if [ ! -x "$GW" ]; then
  if command -v go >/dev/null; then
    echo "building $GW"
    GOFLAGS=-mod=mod CGO_ENABLED=0 go build -o "$GW" ./cmd/gateway
    CGO_ENABLED=0 go build -o "$MOCK" ./tools/mockprovider
  else
    echo "missing $GW and no Go toolchain to build it" >&2; exit 1
  fi
fi
# macOS marks downloaded files as quarantined; the binaries are unsigned.
[ "$(uname -s)" = Darwin ] && xattr -d com.apple.quarantine "$GW" "$MOCK" 2>/dev/null || true

if [ ! -f .env.local ]; then
  rnd() { od -An -tx1 -N"$1" /dev/urandom | tr -d ' \n'; }
  PW="demo-$(rnd 6)"
  cat > .env.local <<EOF
MASTER_KEY=$(rnd 32)
ADMIN_EMAIL=admin@local.test
ADMIN_PASSWORD=$PW
EOF
  chmod 600 .env.local
fi
set -a; . ./.env.local; set +a
export LISTEN_ADDR="127.0.0.1:$PORT" PUBLIC_URL="http://localhost:$PORT" DATA_DIR="${DATA_DIR:-./data}"

MOCK_PID="" GW_PID=""
cleanup() { kill $MOCK_PID $GW_PID 2>/dev/null || true; }
trap cleanup EXIT INT TERM

if [ "$DEMO" = 1 ]; then
  "$MOCK" & MOCK_PID=$!
fi
"$GW" > gateway.log 2>&1 & GW_PID=$!
for _ in $(seq 1 50); do curl -fs "$B/healthz" >/dev/null 2>&1 && break; sleep 0.2; done
curl -fs "$B/healthz" >/dev/null || { echo "gateway did not start; see gateway.log" >&2; tail -20 gateway.log >&2; exit 1; }

KEY_LINE=""
if [ "$DEMO" = 1 ]; then
  JAR=$(mktemp)
  H=(-s -b "$JAR" -c "$JAR" -H "Content-Type: application/json" -H "X-GW-CSRF: 1")
  id() { grep -o '"id":"[^"]*"' | head -1 | cut -d'"' -f4; }
  curl "${H[@]}" -X POST "$B/api/auth/login" -d "{\"email\":\"$ADMIN_EMAIL\",\"password\":\"$ADMIN_PASSWORD\"}" >/dev/null
  if ! curl "${H[@]}" "$B/api/admin/providers" | grep -q '"name":"demo-mock"'; then
    PROV=$(curl "${H[@]}" -X POST "$B/api/admin/providers" \
      -d '{"name":"demo-mock","type":"openai","base_url":"http://127.0.0.1:9901","keys":[{"value":"not-needed"}],"enabled":true}' | id)
    for m in "demo/fast:Demo Fast:150000:600000:128000" "demo/large:Demo Large:2500000:10000000:200000"; do
      IFS=: read -r mid name pin pout ctx <<< "$m"
      curl "${H[@]}" -X POST "$B/api/admin/models" -d "{\"id\":\"$mid\",\"name\":\"$name\",\"description\":\"Answers from the local mock provider\",\"context_length\":$ctx,\"input_price_micros\":$pin,\"output_price_micros\":$pout,\"targets\":[{\"provider_id\":\"$PROV\",\"model\":\"mock\"}],\"enabled\":true}" >/dev/null
    done
    ORG=$(curl "${H[@]}" "$B/api/auth/me" | id)
    SECRET=$(curl "${H[@]}" -X POST "$B/api/orgs/$ORG/keys" -d '{"name":"demo"}' | grep -o '"secret":"[^"]*"' | cut -d'"' -f4)
    echo "$SECRET" > .demo-key
    sleep 2.5 # the gateway picks up new models within 2 s
    for _ in $(seq 1 20); do
      curl -s "$B/v1/chat/completions" -H "Authorization: Bearer $SECRET" -d '{"model":"demo/fast","messages":[{"role":"user","content":"hello"}]}' >/dev/null
    done
  fi
  rm -f "$JAR"
  [ -f .demo-key ] && KEY_LINE="Demo API key: $(cat .demo-key)"
fi

cat <<EOF

  agent-gateway is running.

  Console:   http://localhost:$PORT/app/
  Catalog:   http://localhost:$PORT/
  Sign in:   $ADMIN_EMAIL / $ADMIN_PASSWORD     (saved in .env.local)
  $KEY_LINE

  Try it:    curl http://localhost:$PORT/v1/chat/completions -H "Authorization: Bearer <key>" \\
               -d '{"model":"demo/fast","messages":[{"role":"user","content":"hi"}]}'

  Logs in gateway.log. Press Ctrl-C to stop.
EOF
wait $GW_PID
