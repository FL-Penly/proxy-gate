# Claude OAuth Pool Parity Verification Runbook

This runbook verifies that ProxyGate's Claude OAuth account pool does not reduce Claude Code / OpenCode capabilities compared with using a Claude subscription directly.

The target is semantic parity, not byte-for-byte network identity. A proxy necessarily changes `Host`, `Authorization`, `Content-Length`, transport encoding, and sometimes request path. The important invariant is that model behavior inputs are preserved: request body, model name, tools, tool choice, thinking, context management, streaming, Anthropic version, beta flags, and client/session headers.

## What Must Stay Equivalent

- Request JSON body must be forwarded unchanged after decompression for `/v1/messages`.
- Capability fields must survive unchanged: `model`, `messages`, `system`, `tools`, `tool_choice`, `thinking`, `context_management`, `metadata`, `stream`, `temperature`, `max_tokens`, and `output_config`.
- `anthropic-version` must preserve the client value when present; default to `2023-06-01` only when absent.
- `anthropic-beta` must be merged, not overwritten. OAuth upstream requests must include `oauth-2025-04-20` plus all client-provided betas.
- Claude/OpenCode SDK headers should pass through where safe: `user-agent`, `x-app`, `x-claude-code-session-id`, `x-session-affinity`, `x-request-id`, `x-client-request-id`, `x-stainless-*`, and `anthropic-dangerous-direct-browser-access`.
- Streaming responses must be relayed chunk-by-chunk; usage extraction must be passive.
- Safe upstream response headers should be returned to the client for diagnostics and SDK behavior.

## Code Review Checklist

Inspect these files before running live tests:

- `provider/anthropic.go`: `Forward` should preserve `anthropic-version`, merge `anthropic-beta`, preserve `Accept`, and pass through Anthropic/Claude SDK headers.
- `ingress/messages.go`: Claude pool requests should call `Anthropic.Forward` with the original body; `serveSuccess` should not rewrite SSE chunks; response headers should be copied safely.
- `ingress/middleware.go` and `main.go`: `/v1/messages` auth should support the intended client paths. Claude Code can use `ANTHROPIC_AUTH_TOKEN=<proxy-token>`. OpenCode with `@ex-machina/opencode-anthropic-auth` may send Claude OAuth bearer auth instead of `x-api-key`.
- `provider/claude_oauth.go` and `auth/oauth.go`: OAuth login should use Claude PKCE, save tokens under `pool/claude/*.json`, and preserve `0600` secret file permissions.

Red flags that imply possible capability loss:

- Static `anthropic-beta` replaces client betas.
- Static `anthropic-version` ignores client value.
- Claude request JSON is normalized, adapted, or filtered.
- `tools`, `tool_choice`, `thinking`, or `context_management` are absent after proxying.
- Streaming is buffered into non-SSE output.
- Upstream 4xx/5xx details are replaced with generic errors during final failure.

## Test Suite

Run:

```bash
go test ./...
```

Required assertions:

- OAuth upstream auth uses `Authorization: Bearer <pool-token>`.
- `anthropic-beta` merge preserves client betas and includes `oauth-2025-04-20`.
- `anthropic-version`, `Accept`, and Anthropic SDK/session headers are preserved.
- `/v1/messages` body is forwarded unchanged for Claude pool requests, including `tools` and `thinking`.
- Safe upstream response headers are returned.
- Middleware accepts intended proxy/client auth and rejects unknown tokens.

## Capture Method

Use a local capture server to record request shape. Redact all auth tokens.

Create `/tmp/capture_anthropic.py`:

```python
import hashlib, json, sys
from http.server import BaseHTTPRequestHandler, HTTPServer

class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        self.reply({"data": []})

    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("content-length") or "0"))
        headers = {k.lower(): v for k, v in self.headers.items()}
        for key in ("authorization", "x-api-key", "x-proxygate-token"):
            if key in headers:
                headers[key] = "<redacted>"
        parsed = json.loads(body)
        print(json.dumps({
            "path": self.path,
            "headers": headers,
            "body_len": len(body),
            "body_sha256": hashlib.sha256(body).hexdigest(),
            "body_json_keys": sorted(parsed.keys()),
            "model": parsed.get("model"),
            "stream": parsed.get("stream"),
        }, sort_keys=True), flush=True)
        self.reply({"model": parsed.get("model"), "id": "msg_capture", "type": "message", "role": "assistant", "content": [{"type": "text", "text": "pong"}], "stop_reason": "end_turn", "usage": {"input_tokens": 1, "output_tokens": 1}})

    def reply(self, data):
        out = json.dumps(data).encode()
        self.send_response(200)
        self.send_header("content-type", "application/json")
        self.send_header("content-length", str(len(out)))
        self.end_headers()
        self.wfile.write(out)

    def log_message(self, fmt, *args):
        return

server = HTTPServer(("127.0.0.1", int(sys.argv[1])), Handler)
while True:
    server.handle_request()
```

Compare captures by semantics, not hashes. Hashes often differ due dynamic session IDs, metadata, tool inventories, timestamps, and cache probes. Body key sets and critical fields must match.

## Claude Code Capture

Capture direct Claude Code request shape:

```bash
CAP=/tmp/capture-claudecode.jsonl
python3 /tmp/capture_anthropic.py 19601 > "$CAP" & SPID=$!
ANTHROPIC_AUTH_TOKEN=dummy-token \
ANTHROPIC_BASE_URL=http://127.0.0.1:19601 \
claude --bare --model claude-haiku-4-5 -p "Return exactly pong"
kill "$SPID"
cat "$CAP"
```

Expected direct Claude Code properties:

- Path: `/v1/messages?beta=true`.
- Body includes `tools`; full runs can include `thinking` and `context_management`.
- Headers include `anthropic-version`, `anthropic-beta`, `user-agent: claude-cli/...`, `x-app`, `x-claude-code-session-id`, and `x-stainless-*`.

Capture ProxyGate upstream request shape:

```bash
CAP=/tmp/capture-proxy-upstream.jsonl
python3 /tmp/capture_anthropic.py 19602 > "$CAP" & SPID=$!
PROXYGATE_ADDR=127.0.0.1:19603 \
PROXYGATE_POOL_DIR="$HOME/.proxy-gate/pool" \
PROXYGATE_DATA_DIR=/tmp/proxy-gate-capture-data \
PROXYGATE_ADMIN_TOKEN=admin \
PROXYGATE_PROXY_TOKEN=proxy-token \
PROXYGATE_ANTHROPIC_BASE_URL=http://127.0.0.1:19602/v1/messages \
./proxy-gate serve & PPID=$!
ANTHROPIC_AUTH_TOKEN=proxy-token \
ANTHROPIC_BASE_URL=http://127.0.0.1:19603 \
claude --bare --model claude-haiku-4-5 -p "Return exactly pong"
kill "$PPID" "$SPID"
cat "$CAP"
```

Pass criteria:

- Proxy upstream body contains the same capability fields as direct capture.
- Proxy upstream `anthropic-beta` contains all direct client betas plus `oauth-2025-04-20`.
- Proxy upstream preserves `anthropic-version`, `user-agent`, `x-app`, session headers, and `x-stainless-*`.
- Proxy upstream path is `/v1/messages`; direct client-to-proxy path may differ.

## OpenCode Plugin Capture

OpenCode must be tested through `@ex-machina/opencode-anthropic-auth`. A bare `@ai-sdk/anthropic` provider is not equivalent to the real subscription path.

Temporary capture config `/tmp/opencode-plugin-capture.json`:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "plugin": ["@ex-machina/opencode-anthropic-auth@1.8.0"],
  "provider": {
    "anthropic": {
      "options": {
        "baseURL": "http://127.0.0.1:19604/v1"
      }
    }
  },
  "permission": "allow"
}
```

Capture OpenCode plugin request shape:

```bash
CAP=/tmp/capture-opencode-plugin.jsonl
python3 /tmp/capture_anthropic.py 19604 > "$CAP" & SPID=$!
OPENCODE_CONFIG=/tmp/opencode-plugin-capture.json \
ANTHROPIC_BASE_URL=http://127.0.0.1:19604/v1 \
ANTHROPIC_INSECURE=1 \
opencode run -m anthropic/claude-haiku-4-5 --format json "Return exactly pong"
kill "$SPID"
cat "$CAP"
```

Expected OpenCode plugin properties:

- Path: `/v1/messages?beta=true`.
- Headers include `anthropic-beta` with `oauth-2025-04-20` and feature betas such as `interleaved-thinking-*`, `fine-grained-tool-streaming-*`, and sometimes `structured-outputs-*`.
- Headers include `x-session-affinity`.
- Body can be large because OpenCode includes agent/system/tool context. Large body size is normal and must not be simplified by ProxyGate.

## Live End-to-End Tests

Confirm Claude accounts are loaded:

```bash
ADMIN=$(cat "$HOME/.proxy-gate/.admin_token")
curl -sS -H "X-Admin-Token: $ADMIN" http://127.0.0.1:19527/admin/claude-accounts
```

Claude Code live test:

```bash
TOKEN=$(cat "$HOME/.proxy-gate/.proxy_token")
for MODEL in claude-haiku-4-5 claude-sonnet-4-6 claude-opus-4-7; do
  ANTHROPIC_AUTH_TOKEN="$TOKEN" \
  ANTHROPIC_BASE_URL=http://127.0.0.1:19527 \
  claude --bare --model "$MODEL" -p --max-budget-usd 0.10 "Return exactly pong"
done
```

OpenCode plugin live config `/tmp/opencode-plugin-proxy.json`:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "plugin": ["@ex-machina/opencode-anthropic-auth@1.8.0"],
  "provider": {
    "anthropic": {
      "options": {
        "baseURL": "http://127.0.0.1:19527/v1"
      }
    }
  },
  "permission": "allow"
}
```

OpenCode plugin live test:

```bash
TOKEN=$(cat "$HOME/.proxy-gate/.proxy_token")
for MODEL in claude-haiku-4-5 claude-sonnet-4-6 claude-opus-4-7; do
  OPENCODE_CONFIG=/tmp/opencode-plugin-proxy.json \
  PROXYGATE_PROXY_TOKEN="$TOKEN" \
  ANTHROPIC_BASE_URL=http://127.0.0.1:19527/v1 \
  ANTHROPIC_INSECURE=1 \
  opencode run -m "anthropic/$MODEL" --format json "Return exactly pong"
done
```

Pass criteria:

- Claude Code returns `pong` for all three models.
- OpenCode emits JSON events containing text `pong` for all three models.
- Admin account stats increase after tests.
- Dashboard Claude tab still shows the account as active, not dead or stuck in cooldown.

## Interpreting Failures

- `Exceeded USD budget`: Claude Code local budget blocked the request; raise `--max-budget-usd`, especially for Opus.
- `rate_limit_error`: upstream account/model tier is limited; confirm by direct upstream OAuth call before blaming ProxyGate.
- `You're out of extra usage`: upstream subscription quota is exhausted; not a proxy parity failure.
- `Unauthorized` from ProxyGate: client auth path is not accepted by middleware. For Claude Code, use `ANTHROPIC_AUTH_TOKEN=<proxy-token>`. For OpenCode plugin, verify the plugin still sends Claude OAuth bearer and ProxyGate intentionally supports that route.
- `404 /messages`: base URL is missing `/v1`. Use `ANTHROPIC_BASE_URL=http://127.0.0.1:19527/v1` for OpenCode plugin.
- Missing tools/thinking/context fields in proxy capture: parity failure; inspect `ingress/messages.go` and `provider/anthropic.go` before using the pool.

## Current Known Residual Differences

These are expected and should not affect model capability:

- Upstream `Authorization` uses the leased pool account token, not the client's original token.
- `Host`, `Content-Length`, transport compression, and connection headers differ.
- Proxy upstream path is normalized to `/v1/messages` even when client-to-proxy uses `/v1/messages?beta=true`.
- Body hashes may differ across separate captures because clients generate dynamic metadata and session IDs.

If a future change introduces body adaptation for `/v1/messages`, re-run this full runbook before trusting the pool for real Claude Code/OpenCode work.
