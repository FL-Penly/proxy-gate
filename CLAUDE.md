# proxy-gate

Single Go binary that pools ChatGPT OAuth accounts + API keys and proxies OpenAI Responses, Anthropic Messages, and Chat Completions API calls. Listens on `127.0.0.1:19527` by default.

## First-time setup (help the user run this)

```bash
# 1. Build
go build -o proxy-gate .

# 2. Copy and edit config
cp config.toml.example config.toml
# User MUST set admin_token: openssl rand -hex 32

# 3. Create pool directories
mkdir -p pool/chatgpt pool/apikeys

# 4. Add at least one account or API key (proxy won't route without one)
./proxy-gate add-account              # interactive OAuth login
# OR place a JSON file in pool/apikeys/:
# { "id": "my-key", "provider": "openai", "api_key": "sk-..." }

# 5. Run
./proxy-gate serve
```

Server listens on `127.0.0.1:19527`. Dashboard at http://127.0.0.1:19527/.

## Repo layout

| Dir | Role |
|---|---|
| `main.go` | CLI dispatcher, server wiring |
| `config.go` | TOML + env config, env-var overrides |
| `auth/` | OAuth PKCE + JWT claim extraction |
| `provider/` | HTTP clients for chatgpt.com / api.openai.com / api.anthropic.com / wham |
| `broker/` | Account pool, API key pool, rotation, fsnotify watcher, rate-limit handling |
| `ingress/` | HTTP handlers for `/v1/responses`, `/v1/messages`, `/v1/chat/completions` |
| `relay/` | SSE relay + usage extraction |
| `control/` | Admin HTTP API, recorder, wham poller, persistence queue |
| `pricing/` | Model pricing (embedded snapshot + remote litellm fetch) |
| `web/` | Embedded dashboard HTML (`embed.FS`) |
| `cmd/` | OAuth + import flows reused by CLI subcommands and HTTP add-account |
| `store/` | bbolt KV wrapper |

## API endpoints

| Method + Path | What it proxies |
|---|---|
| `POST /v1/responses` | OpenAI Responses API (OAuth account or API key) |
| `POST /v1/responses/compact` | Same, compact SSE format |
| `POST /v1/messages` | Anthropic Messages API (API key only) |
| `POST /v1/chat/completions` | OpenAI Chat Completions API (API key only) |
| `GET /healthz` | Returns `{"ok":true}` |
| `GET /` | Admin dashboard |
| `/admin/*` | Admin API (requires `X-Admin-Token` header or `proxygate_admin` cookie) |

## CLI commands

```
proxy-gate serve              Start HTTP proxy server
proxy-gate add-account        Add ChatGPT account via OAuth PKCE flow
proxy-gate add-account --no-browser   Print auth URL instead of opening browser
proxy-gate add-account --code=URL     Add account from copy-pasted auth code
proxy-gate list               List accounts in pool/chatgpt/
proxy-gate status             Show account/key counts and routing config
proxy-gate disable <email|id> Disable an account or API key
proxy-gate enable <email|id>  Re-enable an account or API key
proxy-gate import --from=PATH Import v1 accounts.json
proxy-gate import-keys --from=PATH Import v1 api-keys.json
proxy-gate version            Print version
```

## Adding API keys

Place JSON files in `pool/apikeys/`. File name doesn't matter, must be `.json`.

OpenAI key:
```json
{ "id": "my-openai", "provider": "openai", "api_key": "sk-..." }
```

Anthropic key:
```json
{ "id": "my-anthropic", "provider": "anthropic", "api_key": "sk-ant-..." }
```

Keys are hot-reloaded — drop a file in and the pool picks it up automatically.

## Env-var overrides

All override `config.toml`. See `config.go` for implementation.

| Variable | Default | Description |
|---|---|---|
| `PROXYGATE_ADDR` | `127.0.0.1:19527` | Bind address |
| `PROXYGATE_ADMIN_TOKEN` | (from config) | Admin dashboard/API auth token |
| `PROXYGATE_PROXY_TOKEN` | (none) | If set, `/v1/*` endpoints require this as Bearer token |
| `PROXYGATE_DATA_DIR` | `./data` | Directory for bbolt database |
| `PROXYGATE_POOL_DIR` | `./pool` | Directory containing `chatgpt/` and `apikeys/` subdirs |
| `PROXYGATE_ROUTING_PRIORITY` | `account-first` | `account-first` or `apikey-first` |
| `PROXYGATE_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `PROXYGATE_CHATGPT_BASE_URL` | chatgpt.com backend | Override ChatGPT upstream URL |
| `PROXYGATE_OPENAI_BASE_URL` | api.openai.com | Override OpenAI API upstream URL |
| `PROXYGATE_ANTHROPIC_BASE_URL` | api.anthropic.com | Override Anthropic API upstream URL |
| `PROXYGATE_CHAT_BASE_URL` | api.openai.com | Override Chat Completions upstream URL |
| `PROXYGATE_PUBLIC_BASE_URL` | (none) | Public-facing base URL for clients |

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

### Runtime paths (launchd install)

| Path | Purpose |
|---|---|
| `~/.proxy-gate/proxy-gate` | binary (codesigned) |
| `~/.proxy-gate/serve.sh` | wrapper that sets env vars then `exec`s the binary |
| `~/.proxy-gate/.admin_token` | admin auth token (chmod 600) |
| `~/.proxy-gate/data/proxygate.db` | bbolt usage/recorder store |
| `~/.proxy-gate/pool/chatgpt/*.json` | OAuth account files |
| `~/.proxy-gate/pool/apikeys/*.json` | API key files |
| `~/Library/LaunchAgents/com.proxygate.server.plist` | launchd agent |

### launchd commands

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
