#!/usr/bin/env bash
# Run Polyrail on this machine. A fresh data dir gets the production model catalog (disabled
# providers, no keys); add a key to a provider in the console and enable it to use its models.
#   ./scripts/run-local.sh            # production catalog, no demo data
#   DEMO=1 ./scripts/run-local.sh     # also a local mock provider + demo models + an API key
#   PORT=9090 ./scripts/run-local.sh  # another port
# Data and the generated .env.local live next to this script's repo root. Stop with Ctrl-C.
set -euo pipefail
cd "$(dirname "$0")/.."
PORT=${PORT:-8080}
DEMO=${DEMO:-0}
B="http://127.0.0.1:$PORT"

case "$(uname -s)-$(uname -m)" in
  Darwin-arm64) SUFFIX=darwin-arm64 ;;
  Darwin-x86_64) SUFFIX=darwin-amd64 ;;
  Linux-x86_64) SUFFIX=linux-amd64 ;;
  Linux-aarch64 | Linux-arm64) SUFFIX=linux-arm64 ;;
  *) echo "unsupported platform $(uname -s)-$(uname -m); build with: make build" >&2; exit 1 ;;
esac
GW=bin/polyrail-$SUFFIX
MOCK=bin/mockprovider-$SUFFIX
if [ ! -x "$GW" ]; then
  if command -v go >/dev/null; then
    echo "building $GW"
    GOFLAGS=-mod=mod CGO_ENABLED=0 go build -o "$GW" ./cmd/polyrail
    CGO_ENABLED=0 go build -o "$MOCK" ./tools/mockprovider
  else
    echo "missing $GW and no Go toolchain to build it" >&2; exit 1
  fi
fi
if [ "$(uname -s)" = Darwin ]; then
  # macOS kills ("Killed: 9") a signed binary whose bytes were replaced in place, e.g. by an
  # update that overwrote bin/. Run fresh copies (new files, so no stale signature cache),
  # drop the download quarantine flag and re-sign them ad hoc.
  mkdir -p .run
  for b in "$GW" "$MOCK"; do
    rm -f ".run/$(basename "$b")"
    cp "$b" ".run/$(basename "$b")"
    xattr -d com.apple.quarantine ".run/$(basename "$b")" 2>/dev/null || true
    command -v codesign >/dev/null && codesign --force --sign - ".run/$(basename "$b")" 2>/dev/null || true
  done
  GW=".run/$(basename "$GW")"
  MOCK=".run/$(basename "$MOCK")"
fi

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
"$GW" > polyrail.log 2>&1 & GW_PID=$!
for _ in $(seq 1 50); do curl -fs "$B/healthz" >/dev/null 2>&1 && break; sleep 0.2; done
curl -fs "$B/healthz" >/dev/null || { echo "polyrail did not start; see polyrail.log" >&2; tail -20 polyrail.log >&2; exit 1; }

KEY_LINE=""
if [ "$DEMO" = 1 ]; then
  JAR=$(mktemp)
  H=(-s -b "$JAR" -c "$JAR" -H "Content-Type: application/json" -H "X-GW-CSRF: 1")
  id() { grep -o '"id":"[^"]*"' | head -1 | cut -d'"' -f4; }
  curl "${H[@]}" -X POST "$B/api/auth/login" -d "{\"email\":\"$ADMIN_EMAIL\",\"password\":\"$ADMIN_PASSWORD\"}" >/dev/null
  PROVIDERS=$(curl "${H[@]}" "$B/api/admin/providers") # not piped into grep -q: an early grep exit fails the pipe under pipefail
  if [[ "$PROVIDERS" != *'"name":"demo-mock"'* ]]; then
    PROV=$(curl "${H[@]}" -X POST "$B/api/admin/providers" \
      -d '{"name":"demo-mock","type":"openai","base_url":"http://127.0.0.1:9901","keys":[{"value":"not-needed"}],"enabled":true}' | id)
    for m in "demo/fast:Demo Fast:chat:150000:600000:0" "demo/large:Demo Large:chat:2500000:10000000:0" \
             "demo/voice-tts:Demo Voice (text to speech):speech:15000000:0:0" "demo/whisper-stt:Demo Whisper (speech to text):transcription:0:0:6000" \
             "demo/embed:Demo Embed:embedding:20000:0:0" "demo/image:Demo Image:image:0:0:0" "demo/rerank:Demo Rerank:rerank:0:0:0"; do
      IFS=: read -r mid name kind pin pout pmin <<< "$m"
      curl "${H[@]}" -X POST "$B/api/admin/models" -d "{\"id\":\"$mid\",\"name\":\"$name\",\"kind\":\"$kind\",\"description\":\"Answers from the local mock provider\",\"input_price_micros\":$pin,\"output_price_micros\":$pout,\"audio_minute_price_micros\":$pmin,\"targets\":[{\"provider_id\":\"$PROV\",\"model\":\"mock\"}],\"enabled\":true}" >/dev/null
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
MODEL=openai/gpt-4o-mini
[ "$DEMO" = 1 ] && MODEL=demo/fast

cat <<EOF

  Polyrail is running.

  Console:   http://localhost:$PORT/app/
  Catalog:   http://localhost:$PORT/
  Sign in:   $ADMIN_EMAIL / $ADMIN_PASSWORD     (saved in .env.local)
  $KEY_LINE

  Models:    the model catalog is loaded but hidden until a provider is on. In the console open
             Platform > Providers, edit a provider (e.g. openai), paste your API key and enable it;
             its models then appear in the catalog and the API. Create an API key under Keys.
             Data dir from an older version? Platform > Models > Load model catalog adds it.

  Try it:    curl http://localhost:$PORT/v1/chat/completions -H "Authorization: Bearer <key>" \\
               -d '{"model":"$MODEL","messages":[{"role":"user","content":"hi"}]}'

  Logs in polyrail.log. Press Ctrl-C to stop.
EOF
wait $GW_PID
