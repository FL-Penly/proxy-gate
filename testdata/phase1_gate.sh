#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

go build -o /tmp/proxy-gate-test .

PORT_UP=29527
PORT_PROXY=29528
TOKEN=phase1-gate-token
DATADIR=$(mktemp -d)
POOLDIR=$(mktemp -d)

cleanup() {
  kill "${UPSTREAM_PID:-0}" 2>/dev/null || true
  kill "${PROXY_PID:-0}" 2>/dev/null || true
  rm -rf "$DATADIR" "$POOLDIR"
}
trap cleanup EXIT

cat > /tmp/fake-upstream.go <<'GO'
package main

import (
	"fmt"
	"net/http"
	"os"
)

const sse = `event: response.created
data: {"type":"response.created","response":{"id":"resp_gate","model":"gpt-5","status":"in_progress"}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"PHASE_1_PASS"}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_gate","model":"gpt-5","status":"completed","usage":{"input_tokens":100,"input_tokens_details":{"cached_tokens":50},"output_tokens":25,"output_tokens_details":{"reasoning_tokens":5},"total_tokens":125}}}

`

func main() {
	addr := os.Args[1]
	http.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("openai-model", "gpt-5")
		w.WriteHeader(200)
		fmt.Fprint(w, sse)
	})
	if err := http.ListenAndServe(addr, nil); err != nil {
		os.Exit(1)
	}
}
GO

go run /tmp/fake-upstream.go "127.0.0.1:$PORT_UP" >/tmp/fake-upstream.log 2>&1 &
UPSTREAM_PID=$!

until curl -fsS "http://127.0.0.1:$PORT_UP/" -X POST -H "Content-Type: application/json" -d '{}' >/dev/null 2>&1; do
  sleep 0.2
done

mkdir -p "$POOLDIR/chatgpt"
cat > "$POOLDIR/chatgpt/test.json" <<JSON
{
  "email": "test@example.com",
  "account_id": "acc-gate",
  "plan_type": "pro",
  "access_token": "fake-token",
  "refresh_token": "fake-refresh",
  "expires_at": "2099-12-31T00:00:00Z",
  "created_at": "2026-04-26T00:00:00Z"
}
JSON

PROXYGATE_ADDR="127.0.0.1:$PORT_PROXY" \
PROXYGATE_ADMIN_TOKEN="$TOKEN" \
PROXYGATE_DATA_DIR="$DATADIR" \
PROXYGATE_POOL_DIR="$POOLDIR" \
PROXYGATE_CHATGPT_BASE_URL="http://127.0.0.1:$PORT_UP/responses" \
/tmp/proxy-gate-test serve > /tmp/proxy-gate-test.log 2>&1 &
PROXY_PID=$!

until curl -fsS "http://127.0.0.1:$PORT_PROXY/healthz" >/dev/null 2>&1; do sleep 0.2; done

echo "[1/4] account loaded"
curl -fsS -H "X-Admin-Token: $TOKEN" "http://127.0.0.1:$PORT_PROXY/admin/accounts" \
  | grep -q "test@example.com" || { echo "FAIL: account not loaded"; exit 1; }

echo "[2/4] streaming POST returns body"
RESP=$(curl -sS -X POST -H "Content-Type: application/json" \
  -d '{"model":"gpt-5","input":[{"type":"message","role":"user","content":"hi"}]}' \
  "http://127.0.0.1:$PORT_PROXY/v1/responses")
echo "$RESP" | grep -q "PHASE_1_PASS" || { echo "FAIL: missing PHASE_1_PASS in body"; echo "$RESP" | head -5; exit 1; }

sleep 0.5

echo "[3/4] admin/usage shows non-zero tokens (P0 BUG FIX VERIFIED)"
USAGE=$(curl -fsS -H "X-Admin-Token: $TOKEN" "http://127.0.0.1:$PORT_PROXY/admin/usage")
echo "  $USAGE"
echo "$USAGE" | grep -q '"input_tokens":100' || { echo "FAIL: input_tokens not 100"; exit 1; }
echo "$USAGE" | grep -q '"output_tokens":25' || { echo "FAIL: output_tokens not 25"; exit 1; }
echo "$USAGE" | grep -q '"total_tokens":125' || { echo "FAIL: total_tokens not 125"; exit 1; }

echo "[4/4] account stats persisted"
ACC=$(curl -fsS -H "X-Admin-Token: $TOKEN" "http://127.0.0.1:$PORT_PROXY/admin/accounts")
echo "  $ACC"
echo "$ACC" | grep -q '"total_input_tokens":100' || { echo "FAIL: account input tokens"; exit 1; }
echo "$ACC" | grep -q '"total_output_tokens":25' || { echo "FAIL: account output tokens"; exit 1; }

echo
echo "PHASE 1 GATE PASS"
