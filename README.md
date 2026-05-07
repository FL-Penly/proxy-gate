# ProxyGate

A single Go binary that pools multiple ChatGPT OAuth accounts and API keys, then proxies requests to OpenAI, Anthropic, and Chat Completions APIs with smart rotation, usage tracking, and an admin dashboard.

## Features

- **Multi-provider proxy** — OpenAI Responses API, Anthropic Messages API, Chat Completions API
- **Account pooling** — rotate across multiple ChatGPT OAuth accounts with scoring-based selection
- **API key pooling** — rotate across multiple OpenAI/Anthropic API keys
- **Smart routing** — drain-aware scoring, inflight tracking, conversation pinning
- **OAuth login** — built-in OpenAI PKCE device-code flow (CLI + dashboard)
- **Usage tracking** — wham polling, token counting, cost estimation (via litellm pricing)
- **SSE relay** — streaming pass-through with real-time usage extraction
- **Admin dashboard** — embedded web UI for pool management, stats, and account operations
- **Hot reload** — fsnotify watches pool directories; add/remove account files without restart
- **macOS launchd** — deploy script with ad-hoc codesigning for launchd integration

## Prerequisites

- Go 1.22+ (build from source)

## Quick Start

```bash
# Clone
git clone https://github.com/FL-Penly/proxy-gate.git
cd proxy-gate

# Build
go build -o proxy-gate .

# Or install directly:
# go install github.com/FL-Penly/proxy-gate@latest

# Copy example config
cp config.toml.example config.toml
# Edit config.toml — set admin_token, adjust paths if needed

# Create pool directories
mkdir -p pool/chatgpt pool/apikeys

# Run
./proxy-gate serve
```

The server starts on `127.0.0.1:19527` by default. Open http://127.0.0.1:19527/ for the admin dashboard.

> **Note:** You need at least one ChatGPT OAuth account or API key in the pool before the proxy can route requests. See [Adding Accounts](#adding-accounts) below.

## Adding Accounts

### Via CLI (OAuth)
```bash
./proxy-gate add-account              # opens browser for OpenAI login
./proxy-gate add-account --no-browser # prints URL instead
./proxy-gate add-account --code=URL   # paste code from another device
```

### Via Dashboard
Click "Add Account" in the web dashboard — triggers the same OAuth flow.

### Via API Keys
Place JSON files in `pool/apikeys/`:
```json
{
  "id": "my-openai-key",
  "provider": "openai",
  "api_key": "sk-..."
}
```
For Anthropic:
```json
{
  "id": "my-anthropic-key",
  "provider": "anthropic",
  "api_key": "sk-ant-..."
}
```

### Import from v1
```bash
./proxy-gate import --from=accounts.json
./proxy-gate import-keys --from=api-keys.json
```

## API Endpoints

| Endpoint | Upstream |
|---|---|
| `POST /v1/responses` | OpenAI Responses API (via OAuth account or API key) |
| `POST /v1/responses/compact` | Same, compact SSE format |
| `POST /v1/messages` | Anthropic Messages API (via API key) |
| `POST /v1/chat/completions` | OpenAI Chat Completions API (via API key) |
| `GET /healthz` | Health check |
| `GET /` | Admin dashboard |

Set `PROXYGATE_PROXY_TOKEN` to require a bearer token on `/v1/*` endpoints.

## Configuration

See [`config.toml.example`](config.toml.example) for all options. Environment variables override TOML:

| Env Var | Description |
|---|---|
| `PROXYGATE_ADDR` | Bind address (default `127.0.0.1:19527`) |
| `PROXYGATE_ADMIN_TOKEN` | Admin API/dashboard auth token |
| `PROXYGATE_PROXY_TOKEN` | Optional bearer token for `/v1/*` endpoints |
| `PROXYGATE_DATA_DIR` | Data directory for bbolt DB |
| `PROXYGATE_POOL_DIR` | Pool directory with account/key JSON files |
| `PROXYGATE_ROUTING_PRIORITY` | `account-first` or `apikey-first` |
| `PROXYGATE_LOG_LEVEL` | `debug`, `info`, `warn`, `error` |

## CLI Commands

```
proxy-gate serve              Run the HTTP proxy
proxy-gate add-account        Add a ChatGPT account via OAuth
proxy-gate list               List accounts in pool
proxy-gate status             Show pool status and counts
proxy-gate disable <email>    Disable an account or key
proxy-gate enable <email>     Re-enable an account or key
proxy-gate import             Import v1 accounts
proxy-gate import-keys        Import v1 API keys
proxy-gate version            Print version
```

## macOS Deployment (launchd)

```bash
scripts/deploy.sh
```

This builds, codesigns (ad-hoc, required for launchd), installs to `~/.proxy-gate/`, and starts/restarts via launchd.

## Architecture

```
                    ┌─────────────────────────────────┐
                    │         proxy-gate               │
                    │                                  │
  Client ──────►   │  ingress/     ──►  broker/       │
  (Cursor/CLI)     │  (handlers)       (pool+rotate)  │
                    │       │               │          │
                    │       ▼               ▼          │
                    │  relay/        provider/         │
                    │  (SSE relay)   (HTTP clients)    │
                    │       │               │          │
                    └───────┼───────────────┼──────────┘
                            │               │
                    ┌───────▼───┐   ┌───────▼──────────┐
                    │ chatgpt.com│   │ api.openai.com   │
                    │ (OAuth)    │   │ api.anthropic.com│
                    └───────────┘   └──────────────────┘
```

## Testing

```bash
go test ./...
```

Integration gate scripts in `testdata/` exercise end-to-end flows with mock upstreams.

## License

[MIT](LICENSE)
