#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."
go build -o /tmp/cligate-gate4 .

PORT_UP=29557
PORT_PROXY=29558
TOKEN=phase4-gate-token
DATADIR=$(mktemp -d)
POOLDIR=$(mktemp -d)

cleanup() {
  kill "${UPSTREAM_PID:-0}" 2>/dev/null || true
  kill "${PROXY_PID:-0}" 2>/dev/null || true
  rm -rf "$DATADIR" "$POOLDIR"
}
trap cleanup EXIT

cat > /tmp/fake-upstream4.go <<'GO'
package main

import (
	"fmt"
	"net/http"
	"os"
)

func main() {
	addr := os.Args[1]
	http.HandleFunc("/v1/messages", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		fmt.Fprint(w, `{"id":"msg_1","model":"claude-3-5-sonnet","content":[{"type":"text","text":"MSG_PASS"}],"usage":{"input_tokens":12,"output_tokens":8}}`)
	})
	http.HandleFunc("/chat/completions", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		fmt.Fprint(w, `{"id":"chat_1","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"CHAT_PASS"}}],"usage":{"prompt_tokens":15,"completion_tokens":3,"total_tokens":18}}`)
	})
	if err := http.ListenAndServe(addr, nil); err != nil {
		os.Exit(1)
	}
}
GO

go run /tmp/fake-upstream4.go "127.0.0.1:$PORT_UP" >/tmp/fake-upstream4.log 2>&1 &
UPSTREAM_PID=$!
until curl -fsS -X POST "http://127.0.0.1:$PORT_UP/chat/completions" -d '{}' >/dev/null 2>&1; do sleep 0.2; done

mkdir -p "$POOLDIR/chatgpt" "$POOLDIR/apikeys"
cat > "$POOLDIR/apikeys/anthropic-1.json" <<JSON
{ "id":"anthropic-1","name":"a","type":"anthropic","api_key":"sk-ant-test" }
JSON
cat > "$POOLDIR/apikeys/openai-1.json" <<JSON
{ "id":"openai-1","name":"o","type":"openai","api_key":"sk-openai-test" }
JSON

CLIGATE_ADDR="127.0.0.1:$PORT_PROXY" \
CLIGATE_ADMIN_TOKEN="$TOKEN" \
CLIGATE_DATA_DIR="$DATADIR" \
CLIGATE_POOL_DIR="$POOLDIR" \
CLIGATE_ANTHROPIC_BASE_URL="http://127.0.0.1:$PORT_UP/v1/messages" \
CLIGATE_CHAT_BASE_URL="http://127.0.0.1:$PORT_UP/chat/completions" \
/tmp/cligate-gate4 serve > /tmp/cligate-gate4.log 2>&1 &
PROXY_PID=$!
until curl -fsS "http://127.0.0.1:$PORT_PROXY/healthz" >/dev/null 2>&1; do sleep 0.2; done

echo "[1/2] /v1/messages routes via Anthropic key"
RESP=$(curl -fsS -X POST -H "Content-Type: application/json" \
  -d '{"model":"claude-3-5-sonnet","messages":[{"role":"user","content":"hi"}],"max_tokens":1024}' \
  "http://127.0.0.1:$PORT_PROXY/v1/messages")
echo "  $RESP"
echo "$RESP" | grep -q "MSG_PASS" || { echo "FAIL: missing MSG_PASS"; exit 1; }

echo "[2/2] /v1/chat/completions routes via OpenAI key"
RESP=$(curl -fsS -X POST -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}' \
  "http://127.0.0.1:$PORT_PROXY/v1/chat/completions")
echo "  $RESP"
echo "$RESP" | grep -q "CHAT_PASS" || { echo "FAIL: missing CHAT_PASS"; exit 1; }

KEYS=$(curl -fsS -H "X-Admin-Token: $TOKEN" "http://127.0.0.1:$PORT_PROXY/admin/keys")
echo "$KEYS" | grep -q '"id":"anthropic-1"' || { echo "FAIL: anthropic key not listed"; exit 1; }
echo "$KEYS" | grep -q '"id":"openai-1"' || { echo "FAIL: openai key not listed"; exit 1; }

echo
echo "PHASE 4 GATE PASS"
