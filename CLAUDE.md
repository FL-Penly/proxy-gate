# cligate v2

Go rewrite of the v1 Node.js `codex-pool`. Single binary that pools ChatGPT OAuth accounts + API keys and proxies OpenAI Responses, Anthropic Messages, and Chat Completions API calls. Listens on `127.0.0.1:19527` by default.

## Repo layout

| Dir | Role |
|---|---|
| `main.go` | CLI dispatcher, server wiring |
| `config.go` | TOML + env config |
| `auth/` | OAuth PKCE + JWT claim extraction |
| `provider/` | HTTP clients for chatgpt.com / api.openai.com / api.anthropic.com / wham |
| `broker/` | Account pool, API key pool, rotation, fsnotify watcher, rate-limit handling |
| `ingress/` | HTTP handlers for `/v1/responses`, `/v1/messages`, `/v1/chat/completions` |
| `relay/` | SSE relay + usage extraction (the file that fixes v1's P0 streaming-records-zero-tokens bug) |
| `control/` | Admin HTTP API, recorder, wham poller, persistence queue |
| `web/` | Embedded dashboard HTML (`embed.FS`) |
| `cmd/` | OAuth + import flows reused by CLI subcommands and HTTP add-account |
| `store/` | bbolt wrapper |

## CRITICAL: deploy workflow on macOS

**Always use `scripts/deploy.sh`** after `go build`. Skipping the codesign step makes launchd kill the binary with `OS_REASON_CODESIGNING` and v2 will not start. Symptom: `launchctl print` shows `state = spawn scheduled` and `last exit reason = OS_REASON_CODESIGNING`.

```bash
scripts/deploy.sh
```

What it does:
1. `go build -o cligate .`
2. `cp cligate ~/.cligate-v2/cligate`
3. `codesign --force --sign - ~/.cligate-v2/cligate` (ad-hoc — required)
4. `launchctl kickstart -k gui/$UID/com.codeking.cligate` (or bootstrap on first install)
5. Polls `/healthz` until ready, prints PID

If you skip the script and run `go build && cp` only, v2 will not restart cleanly under launchd. Plain `nohup ./cligate serve` from a terminal works (no launchd → no codesign enforcement) but bypasses the production setup.

## Runtime install (the user's machine)

| Path | Purpose |
|---|---|
| `~/.cligate-v2/cligate` | binary (codesigned, ad-hoc) |
| `~/.cligate-v2/serve.sh` | wrapper that exports `CLIGATE_DATA_DIR`, `CLIGATE_POOL_DIR`, `CLIGATE_ADMIN_TOKEN` then `exec`s the binary |
| `~/.cligate-v2/.admin_token` | random hex token, chmod 600, used by dashboard auth |
| `~/.cligate-v2/data/cligate.db` | bbolt usage/recorder store |
| `~/.cligate-v2/pool/chatgpt/*.json` | OAuth account files (one per email) |
| `~/.cligate-v2/pool/apikeys/*.json` | API key files |
| `~/Library/LaunchAgents/com.codeking.cligate.plist` | launchd agent (RunAtLoad + KeepAlive) |

## launchd commands

```bash
launchctl print gui/$UID/com.codeking.cligate         # status (state, runs, pid)
launchctl kickstart -k gui/$UID/com.codeking.cligate  # graceful restart (use after deploy)
launchctl bootout gui/$UID/com.codeking.cligate       # disable
launchctl bootstrap gui/$UID ~/Library/LaunchAgents/com.codeking.cligate.plist  # re-enable
tail -f /tmp/cligate.log                              # logs
```

## Tests

```bash
go test ./...
```

`ingress/parity_test.go` is load-bearing — it locks byte-level equivalence with v1 codex-pool's body adapter. Do not change `AdaptForChatGPTBackend` or `AdaptForChatGPTCompact` without updating those tests; the upstream `chatgpt.com/backend-api/codex/responses` contract is captured in `feedback_chatgpt_backend.md` (in the user's auto-memory).

## Dashboard

`http://127.0.0.1:19527/` — admin dashboard served from `web/dashboard.html` (embedded). Cookie auth, token in `~/.cligate-v2/.admin_token`. Endpoints under `/admin/*` accept either `X-Admin-Token` header or `cligate_admin` cookie.

`POST /admin/ui/oauth-start` triggers OpenAI device-code flow on a callback port (1455–1460). The HTTP handler returns the auth URL immediately and a goroutine in `main.go` handles wait + token exchange + `broker.SaveAccountFile`. The fsnotify watcher in `broker/watch.go` picks up the new file and the pool reloads without restart.

## Env-var overrides

`CLIGATE_ADDR`, `CLIGATE_DATA_DIR`, `CLIGATE_POOL_DIR`, `CLIGATE_ADMIN_TOKEN`, `CLIGATE_ROUTING_PRIORITY` (`account-first`|`apikey-first`), `CLIGATE_LOG_LEVEL`, `CLIGATE_CHATGPT_BASE_URL`, `CLIGATE_OPENAI_BASE_URL`, `CLIGATE_ANTHROPIC_BASE_URL`, `CLIGATE_CHAT_BASE_URL`, `CLIGATE_CHATGPT_USAGE_URL`, `CLIGATE_PUBLIC_BASE_URL`. See `config.go`.
