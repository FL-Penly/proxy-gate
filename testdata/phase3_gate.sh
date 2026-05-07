#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."
go build -o /tmp/proxy-gate-test3 .

PORT_UP=29547
PORT_PROXY=29548
TOKEN=phase3-gate-token
DATADIR=$(mktemp -d)
POOLDIR=$(mktemp -d)

cleanup() {
  kill "${UPSTREAM_PID:-0}" 2>/dev/null || true
  kill "${PROXY_PID:-0}" 2>/dev/null || true
  rm -rf "$DATADIR" "$POOLDIR"
}
trap cleanup EXIT

cat > /tmp/fake-upstream3.go <<'GO'
package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

const sse = `event: response.created
data: {"type":"response.created","response":{"id":"%s","model":"gpt-5","status":"in_progress"}}

event: response.completed
data: {"type":"response.completed","response":{"id":"%s","model":"gpt-5","status":"completed","usage":{"input_tokens":30,"output_tokens":15,"total_tokens":45}}}

`

func main() {
	addr := os.Args[1]
	http.HandleFunc("/responses", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("openai-model", "gpt-5")
		w.WriteHeader(200)
		var rid string
		switch {
		case strings.HasPrefix(auth, "Bearer sk-"):
			rid = "resp_apikey"
		default:
			rid = "resp_account"
		}
		fmt.Fprintf(w, sse, rid, rid)
	})
	if err := http.ListenAndServe(addr, nil); err != nil {
		os.Exit(1)
	}
}
GO

go run /tmp/fake-upstream3.go "127.0.0.1:$PORT_UP" >/tmp/fake-upstream3.log 2>&1 &
UPSTREAM_PID=$!
until curl -fsS -X POST "http://127.0.0.1:$PORT_UP/responses" -d '{}' >/dev/null 2>&1; do sleep 0.2; done

mkdir -p "$POOLDIR/chatgpt" "$POOLDIR/apikeys"
cat > "$POOLDIR/chatgpt/account.json" <<JSON
{ "email":"acc@x.com","account_id":"a","plan_type":"pro","access_token":"tok-acc","refresh_token":"r","expires_at":"2099-12-31T00:00:00Z","created_at":"2026-04-26T00:00:00Z" }
JSON
cat > "$POOLDIR/apikeys/openai-1.json" <<JSON
{ "id":"openai-1","name":"primary","type":"openai","api_key":"sk-test-key-123" }
JSON

PROXYGATE_ADDR="127.0.0.1:$PORT_PROXY" \
PROXYGATE_ADMIN_TOKEN="$TOKEN" \
PROXYGATE_DATA_DIR="$DATADIR" \
PROXYGATE_POOL_DIR="$POOLDIR" \
PROXYGATE_CHATGPT_BASE_URL="http://127.0.0.1:$PORT_UP/responses" \
PROXYGATE_OPENAI_BASE_URL="http://127.0.0.1:$PORT_UP/responses" \
/tmp/proxy-gate-test3 serve > /tmp/proxy-gate-test3.log 2>&1 &
PROXY_PID=$!
until curl -fsS "http://127.0.0.1:$PORT_PROXY/healthz" >/dev/null 2>&1; do sleep 0.2; done

echo "[1/4] both pools loaded"
STATUS=$(curl -fsS -H "X-Admin-Token: $TOKEN" "http://127.0.0.1:$PORT_PROXY/admin/status")
echo "$STATUS" | grep -q '"accounts":1' || { echo "FAIL: $STATUS"; exit 1; }
echo "$STATUS" | grep -q '"api_keys":1' || { echo "FAIL: $STATUS"; exit 1; }

echo "[2/4] account-first priority routes through OAuth account"
RESP=$(curl -fsS -X POST -H "Content-Type: application/json" -d '{"model":"gpt-5"}' "http://127.0.0.1:$PORT_PROXY/v1/responses")
echo "$RESP" | grep -q "resp_account" || { echo "FAIL: should route to account first"; echo "$RESP" | head -3; exit 1; }
echo "  ok routed via OAuth account"

echo "[3/4] disable account → fallback to API key"
curl -fsS -X POST -H "X-Admin-Token: $TOKEN" "http://127.0.0.1:$PORT_PROXY/admin/accounts/acc@x.com/disable" >/dev/null
RESP=$(curl -fsS -X POST -H "Content-Type: application/json" -d '{"model":"gpt-5"}' "http://127.0.0.1:$PORT_PROXY/v1/responses")
echo "$RESP" | grep -q "resp_apikey" || { echo "FAIL: should fallback to API key"; echo "$RESP" | head -3; exit 1; }
echo "  ok fell back to API key"

echo "[4/4] /admin/keys shows usage stats"
KEYS=$(curl -fsS -H "X-Admin-Token: $TOKEN" "http://127.0.0.1:$PORT_PROXY/admin/keys")
echo "  $KEYS"
echo "$KEYS" | grep -q '"total_requests":1' || { echo "FAIL: API key request not recorded"; exit 1; }
echo "$KEYS" | grep -q '"total_input_tokens":30' || { echo "FAIL: API key input tokens"; exit 1; }
echo "$KEYS" | grep -q '"total_output_tokens":15' || { echo "FAIL: API key output tokens"; exit 1; }

echo
echo "PHASE 3 GATE PASS"
