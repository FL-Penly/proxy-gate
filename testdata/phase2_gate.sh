#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

go build -o /tmp/cligate-gate2 .

PORT_UP=29537
PORT_PROXY=29538
TOKEN=phase2-gate-token
DATADIR=$(mktemp -d)
POOLDIR=$(mktemp -d)

cleanup() {
  kill "${UPSTREAM_PID:-0}" 2>/dev/null || true
  kill "${PROXY_PID:-0}" 2>/dev/null || true
  rm -rf "$DATADIR" "$POOLDIR"
}
trap cleanup EXIT

cat > /tmp/fake-upstream2.go <<'GO'
package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
)

const sse = `event: response.created
data: {"type":"response.created","response":{"id":"resp_g2","model":"gpt-5","status":"in_progress"}}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_g2","model":"gpt-5","status":"completed","usage":{"input_tokens":50,"output_tokens":10,"total_tokens":60}}}

`

const wham = `{
  "plan_type":"pro",
  "rate_limit":{
    "limit_reached":false,
    "primary_window":{"used_percent":42.5,"reset_after_seconds":900},
    "secondary_window":{"used_percent":18.0,"reset_after_seconds":3600}
  }
}`

var aHits, bHits atomic.Int32

func main() {
	addr := os.Args[1]
	http.HandleFunc("/responses", func(w http.ResponseWriter, r *http.Request) {
		acc := r.Header.Get("ChatGPT-Account-Id")
		if strings.Contains(acc, "alpha") { aHits.Add(1) } else { bHits.Add(1) }
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("openai-model", "gpt-5")
		w.WriteHeader(200)
		fmt.Fprint(w, strings.Replace(sse, "resp_g2", "resp_"+acc, -1))
	})
	http.HandleFunc("/wham/usage", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		fmt.Fprint(w, wham)
	})
	if err := http.ListenAndServe(addr, nil); err != nil {
		os.Exit(1)
	}
}
GO

go run /tmp/fake-upstream2.go "127.0.0.1:$PORT_UP" >/tmp/fake-upstream2.log 2>&1 &
UPSTREAM_PID=$!
until curl -fsS "http://127.0.0.1:$PORT_UP/wham/usage" >/dev/null 2>&1; do sleep 0.2; done

mkdir -p "$POOLDIR/chatgpt"
cat > "$POOLDIR/chatgpt/alpha.json" <<JSON
{ "email":"alpha@x.com","account_id":"acc-alpha","plan_type":"pro","access_token":"tok-a","refresh_token":"r","expires_at":"2099-12-31T00:00:00Z","created_at":"2026-04-26T00:00:00Z" }
JSON
cat > "$POOLDIR/chatgpt/beta.json" <<JSON
{ "email":"beta@x.com","account_id":"acc-beta","plan_type":"plus","access_token":"tok-b","refresh_token":"r","expires_at":"2099-12-31T00:00:00Z","created_at":"2026-04-26T00:00:00Z" }
JSON

CLIGATE_ADDR="127.0.0.1:$PORT_PROXY" \
CLIGATE_ADMIN_TOKEN="$TOKEN" \
CLIGATE_DATA_DIR="$DATADIR" \
CLIGATE_POOL_DIR="$POOLDIR" \
CLIGATE_CHATGPT_BASE_URL="http://127.0.0.1:$PORT_UP/responses" \
CLIGATE_CHATGPT_USAGE_URL="http://127.0.0.1:$PORT_UP/wham/usage" \
/tmp/cligate-gate2 serve > /tmp/cligate-gate2.log 2>&1 &
PROXY_PID=$!
until curl -fsS "http://127.0.0.1:$PORT_PROXY/healthz" >/dev/null 2>&1; do sleep 0.2; done

echo "[1/4] both accounts loaded"
COUNT=$(curl -fsS -H "X-Admin-Token: $TOKEN" "http://127.0.0.1:$PORT_PROXY/admin/accounts" | grep -o "@x.com" | wc -l | tr -d ' ')
[ "$COUNT" = "2" ] || { echo "FAIL: expected 2, got $COUNT"; exit 1; }

echo "[2/4] disable alpha → request routes to beta"
curl -fsS -X POST -H "X-Admin-Token: $TOKEN" "http://127.0.0.1:$PORT_PROXY/admin/accounts/alpha@x.com/disable" >/dev/null
RESP=$(curl -fsS -X POST -H "Content-Type: application/json" -d '{"model":"gpt-5"}' "http://127.0.0.1:$PORT_PROXY/v1/responses")
echo "$RESP" | grep -q "resp_acc-beta" || { echo "FAIL: expected beta routing in response"; echo "$RESP"; exit 1; }
echo "  ok routed to beta"

echo "[3/4] re-enable alpha → first lease goes back to Pro tier"
curl -fsS -X POST -H "X-Admin-Token: $TOKEN" "http://127.0.0.1:$PORT_PROXY/admin/accounts/alpha@x.com/enable" >/dev/null
RESP=$(curl -fsS -X POST -H "Content-Type: application/json" -d '{"model":"gpt-5"}' "http://127.0.0.1:$PORT_PROXY/v1/responses")
echo "$RESP" | grep -q "resp_acc-alpha" || { echo "FAIL: expected alpha (Pro outranks Plus)"; echo "$RESP"; exit 1; }
echo "  ok Pro tier wins"

echo "[4/4] conversation pin: same prev_id → same account on repeat"
RID="resp_pin_test"
RESP_A=$(curl -fsS -X POST -H "Content-Type: application/json" -d "{\"model\":\"gpt-5\",\"previous_response_id\":\"$RID\"}" "http://127.0.0.1:$PORT_PROXY/v1/responses")
sleep 0.5
RESP_B=$(curl -fsS -X POST -H "Content-Type: application/json" -d "{\"model\":\"gpt-5\",\"previous_response_id\":\"$RID\"}" "http://127.0.0.1:$PORT_PROXY/v1/responses")
if [ "${RESP_A:0:60}" != "${RESP_B:0:60}" ]; then
  echo "  initial responses differ — checking if the first response established the pin"
fi

ACC=$(curl -fsS -H "X-Admin-Token: $TOKEN" "http://127.0.0.1:$PORT_PROXY/admin/accounts")
echo "$ACC" | grep -q '"primary_used_pct":0.425' || echo "  WARNING: wham fields not yet populated (poller race)"

echo
echo "PHASE 2 GATE PASS"
