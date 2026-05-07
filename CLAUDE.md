# proxy-gate

Single Go binary that pools ChatGPT OAuth accounts + API keys and proxies OpenAI Responses, Anthropic Messages, and Chat Completions API calls. Listens on `127.0.0.1:19527` by default.

## Repo layout

| Dir | Role |
|---|---|
| `main.go` | CLI dispatcher, server wiring |
| `config.go` | TOML + env config |
| `auth/` | OAuth PKCE + JWT claim extraction |
| `provider/` | HTTP clients for chatgpt.com / api.openai.com / api.anthropic.com / wham |
| `broker/` | Account pool, API key pool, rotation, fsnotify watcher, rate-limit handling |
| `ingress/` | HTTP handlers for `/v1/responses`, `/v1/messages`, `/v1/chat/completions` |
| `relay/` | SSE relay + usage extraction |
| `control/` | Admin HTTP API, recorder, wham poller, persistence queue |
| `web/` | Embedded dashboard HTML (`embed.FS`) |
| `cmd/` | OAuth + import flows reused by CLI subcommands and HTTP add-account |
| `store/` | bbolt wrapper |

## macOS deploy (launchd)

**Always use `scripts/deploy.sh`** after `go build`. Skipping the codesign step makes launchd kill the binary with `OS_REASON_CODESIGNING`.

```bash
scripts/deploy.sh
```

What it does:
1. `go build -o proxy-gate .`
2. `cp proxy-gate ~/.proxy-gate/proxy-gate`
3. `codesign --force --sign - ~/.proxy-gate/proxy-gate` (ad-hoc — required)
4. `launchctl kickstart -k gui/$UID/com.proxygate.server` (or bootstrap on first install)
5. Polls `/healthz` until ready, prints PID

Running `nohup ./proxy-gate serve` from a terminal also works (no launchd = no codesign enforcement).

## Runtime paths

| Path | Purpose |
|---|---|
| `~/.proxy-gate/proxy-gate` | binary (codesigned, ad-hoc) |
| `~/.proxy-gate/serve.sh` | wrapper that exports `PROXYGATE_DATA_DIR`, `PROXYGATE_POOL_DIR`, `PROXYGATE_ADMIN_TOKEN` then `exec`s the binary |
| `~/.proxy-gate/.admin_token` | random hex token, chmod 600, used by dashboard auth |
| `~/.proxy-gate/data/proxygate.db` | bbolt usage/recorder store |
| `~/.proxy-gate/pool/chatgpt/*.json` | OAuth account files (one per email) |
| `~/.proxy-gate/pool/apikeys/*.json` | API key files |
| `~/Library/LaunchAgents/com.proxygate.server.plist` | launchd agent (RunAtLoad + KeepAlive) |

## launchd commands

```bash
launchctl print gui/$UID/com.proxygate.server         # status
launchctl kickstart -k gui/$UID/com.proxygate.server  # graceful restart
launchctl bootout gui/$UID/com.proxygate.server       # disable
launchctl bootstrap gui/$UID ~/Library/LaunchAgents/com.proxygate.server.plist  # re-enable
tail -f /tmp/proxy-gate.log                            # logs
```

## Tests

```bash
go test ./...
```

`ingress/parity_test.go` is load-bearing — it locks byte-level equivalence of the body adapter. Do not change `AdaptForChatGPTBackend` or `AdaptForChatGPTCompact` without updating those tests.

## Dashboard

`http://127.0.0.1:19527/` — admin dashboard served from `web/dashboard.html` (embedded). Cookie auth, token in `~/.proxy-gate/.admin_token`. Endpoints under `/admin/*` accept either `X-Admin-Token` header or `proxygate_admin` cookie.

`POST /admin/ui/oauth-start` triggers OpenAI device-code flow on a callback port (1455–1460). The HTTP handler returns the auth URL immediately and a goroutine in `main.go` handles wait + token exchange + `broker.SaveAccountFile`. The fsnotify watcher in `broker/watch.go` picks up the new file and the pool reloads without restart.

## Env-var overrides

`PROXYGATE_ADDR`, `PROXYGATE_DATA_DIR`, `PROXYGATE_POOL_DIR`, `PROXYGATE_ADMIN_TOKEN`, `PROXYGATE_ROUTING_PRIORITY` (`account-first`|`apikey-first`), `PROXYGATE_LOG_LEVEL`, `PROXYGATE_CHATGPT_BASE_URL`, `PROXYGATE_OPENAI_BASE_URL`, `PROXYGATE_ANTHROPIC_BASE_URL`, `PROXYGATE_CHAT_BASE_URL`, `PROXYGATE_CHATGPT_USAGE_URL`, `PROXYGATE_PUBLIC_BASE_URL`. See `config.go`.
