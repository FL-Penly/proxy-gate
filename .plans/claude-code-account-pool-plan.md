# Claude Code Account Pool Plan

Status: Implemented
Created: 2026-05-09
Request: 我希望再给我们这个项目加个claudecodex的号池。给个符合项目规范优雅的实现方案吧。我希望实现这个方案之后，我能直接在opencode里用，以及直接在claudecode里用。你可以看看我们之前的chatgpt号池是怎么配的，可以看~/.config/opencode/opencode.json，我记得是配了个baseurl。就是整个方案除了实现还必须有严谨的测试。opencode和claudecode都支持非交互式直接发起对话来测试的。有什么需要我提供的信息，可以一开始就问清楚。补充：参考 CliGate 和其他优秀号池开源项目。

## 1. Goal
- Add a clean Claude Code OAuth account pool so Anthropic Messages clients can use Claude subscription credentials through this proxy, with rotation/failover behavior consistent with the existing ChatGPT OAuth and API key pools.
- Make it usable from:
  - Claude Code via `ANTHROPIC_BASE_URL=http://127.0.0.1:19527/v1` + any local auth/proxy token.
  - OpenCode via an Anthropic-compatible provider `baseURL` pointing at `http://127.0.0.1:19527/v1`.
- Keep the current ChatGPT/Codex Responses pool intact. Treat “Codex pool” as already covered by the existing ChatGPT OAuth account pool unless we explicitly decide to rename/expose it as a separate concept.
- Provide a rigorous test plan: unit tests, handler integration tests, persistence/admin tests, mock upstream tests, and optional real CLI smoke tests for `opencode run` and `claude -p`.

## 2. Non-goals
- Do not rewrite the proxy into a CliGate/CLIProxyAPI-style all-provider gateway.
- Do not add Gemini/Antigravity/Qwen/iFlow pools in this plan.
- Do not add model mapping, free-model routing, app-assigned routing, or dashboard chat unless needed by the Claude pool.
- Do not store or commit real OAuth tokens, OpenCode tokens, or Claude Code credentials.
- Do not rely on live upstream tests as required CI gates; live Claude/OpenAI tests should be opt-in smoke checks.

## 3. Current-state findings
- `README.md:3-16` — The project is intentionally a single Go binary for ChatGPT OAuth accounts + API keys, with smart rotation, usage tracking, SSE relay, and dashboard.
- `README.md:86-97` — Current public proxy endpoints are `/v1/responses`, `/v1/responses/compact`, `/v1/messages`, `/v1/chat/completions`, `/healthz`, and dashboard.
- `config.toml.example:15-23` — Runtime data is organized under `data_dir` and `pool_dir`; routing only supports `account-first` / `apikey-first` today.
- `go.mod:1-14` — Dependency set is small; adding Claude support should avoid heavy dependencies and stay Go-native.
- `main.go:204-219` — `/v1/responses` already routes through `ResponsesHandler`, which can use ChatGPT OAuth accounts or OpenAI API keys.
- `main.go:221-231` — `/v1/messages` currently uses `MessagesHandler` with **Anthropic API keys only**.
- `main.go:246-264` — `/v1/models` and ChatGPT backend passthrough are backed by the existing ChatGPT account pool.
- `main.go:371-409` — `add-account` is hardcoded to the ChatGPT/OpenAI OAuth flow and saves into `pool/chatgpt`.
- `main.go:412-427` — `status` only counts `pool/chatgpt` and `pool/apikeys`.
- `main.go:430-481` — `enable` / `disable` only target ChatGPT accounts and API keys.
- `broker/account.go:23-34` — `Account` is OpenAI/ChatGPT-shaped: `Email`, `AccountID`, `PlanType`, access/refresh/id tokens, expiry.
- `broker/account.go:36-57` — Account state includes ChatGPT WHAM-specific fields (`PrimaryUsedPct`, `SecondaryUsedPct`, WHAM error state), so reusing this type directly for Claude would leak ChatGPT concepts.
- `broker/account.go:153-170` — Availability checks combine disabled/dead/cooldown with WHAM percentage thresholds.
- `broker/account.go:240-256` — account JSON loading requires `email` and `access_token` and stores source path.
- `broker/account.go:267-293` — account files are written under an account-specific directory with sanitized email filename and `0600` permissions.
- `broker/apikey.go:17-28` — API key pool already models provider type; `KeyTypeAnthropic` exists for `/v1/messages`.
- `broker/apikey.go:277-327` — API key lease selection is simple token-load balancing and is a better structural template for a provider-specific Claude pool than the WHAM-heavy ChatGPT `Account` state.
- `ingress/messages.go:19-24` — `MessagesHandler` only has `KeyPool`, `Anthropic`, `Recorder`, `Logger`; no OAuth account pool field.
- `ingress/messages.go:34-37` — currently rejects `/v1/messages` when there are no Anthropic API keys, even if future Claude OAuth accounts exist.
- `ingress/messages.go:48-130` — Anthropic API key routing already implements retry/failover semantics for auth errors, 429 cooldown, 5xx backoff, non-2xx passthrough, usage record, and success accounting.
- `provider/anthropic.go:37-70` — Anthropic forwarding is hardcoded to `x-api-key`; OAuth token forwarding needs either `Authorization: Bearer` or a token-kind-aware auth header path.
- `relay/anthropic.go:89-137` — Anthropic SSE usage extraction already exists and can be reused for Claude OAuth routes.
- `auth/oauth.go:21-31` and `auth/oauth.go:85-99` — existing OAuth PKCE flow is OpenAI/Codex-specific, but the PKCE/callback primitives are reusable if generalized.
- `provider/refresh.go:53-78` — code exchange/refresh is OpenAI-specific; Claude refresh needs a separate endpoint/client ID and tests.
- `store/store.go:11-19` — persistent buckets are currently `accounts`, `apikeys`, `usage`, `settings`, `pins`; Claude account stats should either get a new bucket or a namespaced key strategy.
- `ingress/middleware.go:140-156` — `/v1/*` auth accepts `Authorization: Bearer <PROXYGATE_PROXY_TOKEN>` or `X-ProxyGate-Token`; this works for OpenCode/Claude Code client-side access control.
- `~/.config/opencode/opencode.json:191-203` — local OpenCode already uses the built-in `openai` provider with `baseURL: http://127.0.0.1:19527/v1`; the configured API key value should be treated as a secret and not copied into repo docs/tests.
- Existing tests to preserve/reuse:
  - `ingress/parity_test.go` — load-bearing byte-level Responses adaptation parity.
  - `ingress/responses_integration_test.go` — streaming, compact, refresh, and compression integration patterns.
  - `ingress/rotation_integration_test.go` — 429 rotation, auth failure, and 4xx no-retry behavior.
  - `broker/rotation_test.go` and `broker/apikey_test.go` — pool selection and type-filtering patterns.
  - `control/admin_toggle_test.go` and `control/e2e_cost_test.go` — admin/persistence/cost e2e patterns.

### External project references
- CliGate (`codeking-ai/cligate`) exposes the target shape: ChatGPT, Claude, and Antigravity account pools; `/v1/messages` for Claude Code; `/v1/responses` and `/backend-api/codex/responses` for Codex; app-level routing and dashboard operations. Reference: `https://github.com/codeking-ai/cligate` and docs `docs/API.md`, `docs/ACCOUNTS.md`, `docs/APP_ROUTING.md`.
- CliGate keeps ChatGPT and Claude accounts as separate account types and exposes separate Claude account management APIs (`/claude-accounts/*`). This is a good boundary for this project too, but we should keep the API surface smaller.
- CliGate’s app-assigned routing is useful inspiration, but overkill for this first implementation; route selection can remain endpoint-driven: `/v1/messages` uses Claude OAuth accounts then Anthropic API keys according to routing priority.
- CLIProxyAPI uses a richer config-driven provider/channel model with round-robin/fill-first routing, model aliases, excluded models, proxy URLs, custom headers, and OAuth model aliases. Useful ideas: provider-specific credential entries, routing strategy, custom headers. Not recommended wholesale because it would make this project much larger.
- Public Claude Code OAuth evidence indicates credentials live in macOS Keychain or `~/.claude/.credentials.json`, with a `claudeAiOauth` object containing access token, refresh token, expiry, scopes, and subscription metadata. Token endpoints/client IDs are reverse-engineered and may change.

## 4. Open questions
- [ ] Does “claudecodex 号池” mean **Claude Code OAuth pool only**, or do you also want the existing ChatGPT/Codex pool renamed/split into a first-class “Codex pool” with separate commands/UI labels?
- [ ] Should the first implementation support **import/manual JSON only** for Claude accounts, or must it include full `add-claude-account` OAuth login from day one?
- [ ] Can you provide one non-sensitive Claude Code credential shape sample with tokens redacted, or confirm your platform’s storage path? On macOS, Claude Code uses Keychain, so import automation is different from Linux/Windows file import.
- [ ] For real smoke tests, can we use one disposable/low-risk Claude account and one test prompt/model name? Live tests should be opt-in and skipped by default.
- [ ] Should `/v1/messages` priority be `claude-account-first` by default, or should it follow existing `routing.priority` (`account-first` means OAuth account first, `apikey-first` means API key first)?

## 5. Assumptions
- The minimal useful feature is a **Claude Code OAuth account pool for `/v1/messages`**. Existing ChatGPT OAuth accounts already cover Codex/OpenAI Responses usage from OpenCode and Codex-style clients.
- Claude OAuth access tokens should be modeled as a separate provider/account type, not forced into the WHAM-heavy ChatGPT `Account` type.
- OAuth token forwarding will support `Authorization: Bearer <access_token>` first, with a feature-tested fallback/config knob for `x-api-key` if real validation shows Claude Code OAuth tokens require it.
- Claude refresh behavior is reverse-engineered and potentially unstable; implementation should isolate refresh details in `provider/claude_refresh.go` / `auth/claude_oauth.go` and test it with mock token servers.
- We should keep this project’s style: small packages, simple JSON files in `pool/*`, `0600` secrets, bbolt for stats/persistence, no large framework dependencies.

## 6. Options considered

### Option A — Reuse existing `broker.Account` for Claude
- Pros:
  - Fastest implementation.
  - Reuses pool loading, lease, enable/disable, stats persistence.
- Cons:
  - Pollutes Claude accounts with ChatGPT-specific WHAM fields and thresholds.
  - Hard to express Claude-specific subscription/rate-limit metadata cleanly.
  - Long-term maintenance risk when adding more OAuth providers.
- Fit: Poor. It violates the project’s elegance goal by coupling unrelated provider semantics.

### Option B — Add a separate `ClaudeAccount` pool using existing patterns
- Pros:
  - Clean provider boundary, inspired by CliGate’s separate `/claude-accounts` concept.
  - Can reuse the simple lease/failover shape of `APIKeyPool` and the state/update patterns of `Account`.
  - Keeps `/v1/messages` changes localized and avoids destabilizing `/v1/responses`.
  - Good fit for rigorous mock tests.
- Cons:
  - Requires new broker files, admin wiring, store bucket/namespacing, and CLI commands.
  - Needs careful auth header and refresh validation against real Claude Code behavior.
- Fit: Best.

### Option C — Generalize all credentials into one provider-agnostic credential pool
- Pros:
  - Elegant in theory for future Gemini/Qwen/etc.
  - Similar to CLIProxyAPI’s provider/channel model.
- Cons:
  - Large refactor touching current ChatGPT and API key paths.
  - High regression risk for existing `/v1/responses` and cost tracking.
  - Over-engineered for the current requirement.
- Fit: Not recommended for this project now.

## 7. Recommended approach
- Implement **Option B**: a separate Claude OAuth account pool (`pool/claude/`) with provider-specific auth/refresh/forwarding, integrated into `/v1/messages` before/after Anthropic API keys according to routing priority.
- Keep existing ChatGPT/Codex pool unchanged; add docs/config examples showing OpenCode can use either:
  - OpenAI provider → `/v1/responses` backed by ChatGPT/Codex pool.
  - Anthropic provider → `/v1/messages` backed by Claude Code pool.
- Start with a robust import/manual-file path plus isolated OAuth primitives. Full browser OAuth can be Slice 4 if we confirm endpoint/header behavior; otherwise keep it behind an explicit command and tests with mock servers.

## 8. Execution slices

### Slice 1 — Claude account domain and pool
- Files:
  - New: `broker/claude_account.go`
  - New: `broker/claude_pool.go`
  - New tests: `broker/claude_pool_test.go`
  - Possibly: `store/store.go`
- Steps:
  - Define `ClaudeAccount` with fields: `email`, `account_id,omitempty`, `subscription_type,omitempty`, `rate_limit_tier,omitempty`, `access_token`, `refresh_token`, `expires_at`, `created_at`.
  - Define `ClaudeAccountStats` with generic stats only: total requests/tokens/cost, last used, disabled, dead/dead reason, cooldown until, last refresh error metadata.
  - Implement `LoadClaudeAccountFile`, `SaveClaudeAccountFile`, `ClaudePool.LoadDir`, `Add`, `Remove`, `Get`, `List`, `Len`, `Lease`, `NearestCooldown`.
  - Use token-load or request-load ordering initially; avoid WHAM thresholds.
  - Add `pool/claude` as the default subdir.
  - Add a store bucket such as `claude_accounts`, or namespaced keys under `accounts` if we want fewer buckets. Prefer a new bucket for clarity.
- Validation:
  - Pool loads only `.json`, rejects missing token/email, saves `0600`, deterministic lease order, disabled/dead/cooldown filtering, exhaustion errors, release decrements inflight.

### Slice 2 — Claude OAuth token refresh and forwarding
- Files:
  - New: `provider/claude_refresh.go`
  - Update: `provider/anthropic.go`
  - New tests: `provider/claude_refresh_test.go`, `provider/anthropic_oauth_test.go`
- Steps:
  - Add `ClaudeToken` exchange/refresh helpers with injectable token URL for tests.
  - Add `AuthMode` or `CredentialKind` to `AnthropicForwardRequest` so API-key and OAuth-token forwarding are explicit.
  - For OAuth mode, send `Authorization: Bearer <access_token>` and preserve `anthropic-version`, `Content-Type`, `Accept`, `anthropic-beta`, and passthrough headers.
  - Consider adding Claude-Code-compatible default beta header only if real testing proves required; otherwise forward client headers unchanged to avoid unnecessary coupling.
  - Implement `ClaudeTokenRefresher` that updates account tokens, saves JSON file, and persists stats/refresh errors.
- Validation:
  - Mock token endpoint tests for refresh success, refresh-token carry-forward, parse failures, non-2xx errors.
  - Header tests verifying API-key mode still uses `x-api-key`, OAuth mode uses bearer, streaming sets `Accept: text/event-stream`, and beta passthrough remains intact.

### Slice 3 — `/v1/messages` routing through Claude pool + Anthropic API keys
- Files:
  - Update: `ingress/messages.go`
  - Update: `main.go`
  - New/update tests: `ingress/messages_integration_test.go`, maybe `ingress/messages_rotation_test.go`
- Steps:
  - Extend `MessagesHandler` with `ClaudePool`, `ClaudeRefresher`, `Priority`, and optional `Pricer` if we want cost for Claude OAuth too.
  - Add source ordering analogous to `ResponsesHandler`: OAuth account vs API key, following `routing.priority`.
  - Implement `serveViaClaudePool` using `ClaudePool.Lease`, `AnthropicClient.Forward(... OAuth mode ...)`, refresh-on-401 once, mark dead on repeated auth failure, cooldown on 429, backoff on 5xx, passthrough on non-retryable 4xx.
  - Reuse existing Anthropic SSE relay and usage extraction.
  - Record provider as `claude-pool`; keep API key route as `anthropic-key`.
  - Adjust empty-pool errors so `/v1/messages` reports “No Claude accounts or Anthropic API keys configured.” when both are absent.
- Validation:
  - Mock Anthropic upstream tests for streaming and non-streaming success via Claude pool.
  - 401 refresh then retry succeeds.
  - 401 refresh failure marks account dead and tries next account/key.
  - 429 marks cooldown and rotates.
  - `routing.priority=apikey-first` uses Anthropic key before Claude account.
  - Existing Anthropic API key behavior remains unchanged.

### Slice 4 — CLI/import/admin management
- Files:
  - Update: `main.go`
  - Update/new: `cmd/claude.go`
  - Update: `control/admin.go`
  - Update: `control/persist.go` if bucket enum handling needs changes
  - New tests: `cmd/claude_test.go`, `control/admin_claude_toggle_test.go`
- Steps:
  - Add minimal commands:
    - `add-claude-account --from=PATH` to import a `.credentials.json`-style Claude Code credential or a proxy-gate Claude JSON.
    - `list-claude` or `list --type=claude`.
    - `enable/disable <email|id>` should search Claude accounts too.
    - `status` should show `claude_accounts` count.
  - If aligned, add `add-claude-account` interactive OAuth using reusable PKCE/callback helpers; otherwise defer to a later plan and keep import/manual-file first.
  - Add admin API endpoints under `/admin/claude-accounts/*` for list, refresh, delete, enable/disable. Keep names explicit rather than overloading current `/admin/accounts`.
  - Add startup rehydration for Claude account stats.
  - Add fsnotify watcher for `pool/claude` or generalize watcher to multiple pool dirs.
- Validation:
  - Import redacted fixture with `claudeAiOauth` object.
  - Import proxy-gate-native JSON.
  - Toggle persists and survives reload.
  - Status/list outputs include Claude accounts without breaking existing commands.

### Slice 5 — Client docs and local smoke-test scripts
- Files:
  - Update: `README.md`
  - Update: `config.toml.example`
  - New optional scripts under `testdata/` if the project wants scripted manual gates.
- Steps:
  - Document `pool/claude/*.json` format.
  - Document Claude Code config:
    - `ANTHROPIC_BASE_URL=http://127.0.0.1:19527/v1`
    - `ANTHROPIC_API_KEY=<PROXYGATE_PROXY_TOKEN or dummy if unset>`
    - `claude --bare -p "ping"`
  - Document OpenCode Anthropic provider config without embedding real secrets.
  - Document current OpenCode OpenAI provider remains backed by `/v1/responses` and ChatGPT/Codex pool.
  - Add optional smoke commands gated by env vars, e.g. `PROXYGATE_SMOKE=1`, `OPENCODE_MODEL=...`, `CLAUDE_MODEL=...`.
- Validation:
  - `go test ./...`.
  - Optional local-only: run proxy with mock upstream and `curl` `/v1/messages`.
  - Optional live: `opencode run --model <anthropic-provider/model> "say pong"` and `claude --bare -p "say pong"` against the local proxy.

### Slice 6 — Dashboard polish, if needed
- Files:
  - `web/dashboard.html`
  - `control/admin.go`
  - UI tests/manual checks only if dashboard behavior changes.
- Steps:
  - Add Claude account count/status table next to ChatGPT accounts and API keys.
  - Add refresh/disable/delete actions.
  - Avoid adding chat UI or app-assigned routing in this slice.
- Validation:
  - Manual dashboard login and Claude account status render.
  - Admin token/cookie auth remains enforced.

## 9. Validation plan
- Required automated checks:
  - `go test ./...`
  - New `broker` tests for Claude pool loading, leasing, cooldown, disable/dead, persistence-safe file permissions.
  - New `provider` tests for Claude OAuth refresh and Anthropic OAuth headers.
  - New `ingress` integration tests for `/v1/messages` Claude pool success, streaming usage extraction, 401 refresh, 429 rotation, 5xx backoff, API-key fallback, and priority inversion.
  - New `control` tests for admin list/toggle/refresh/delete and stats rehydration.
- Regression checks:
  - Existing `ingress/parity_test.go` must remain unchanged and passing.
  - Existing `/v1/responses` account/API key behavior unchanged.
  - Existing `/v1/chat/completions` behavior unchanged.
  - Existing `/v1/messages` Anthropic API key behavior unchanged when no Claude pool is configured.
- Manual/mock checks:
  - Start proxy with temp `PROXYGATE_POOL_DIR` containing one fake Claude account and mock Anthropic upstream; verify curl to `/v1/messages` uses OAuth headers.
  - Verify `PROXYGATE_PROXY_TOKEN` works with both `Authorization: Bearer` and `X-ProxyGate-Token` from clients where configurable.
- Optional live smoke checks, skipped by default:
  - Claude Code: `ANTHROPIC_BASE_URL=http://127.0.0.1:19527/v1 ANTHROPIC_API_KEY=<token-or-dummy> claude --bare -p "Return exactly pong"`.
  - OpenCode: configure Anthropic-compatible local provider and run `opencode run --model <provider>/<model> "Return exactly pong"`.

## 10. Risks and rollback
- Risk: Claude Code OAuth token/header behavior is unofficial and can change.
  - Mitigation: isolate Claude OAuth in provider-specific files, test via mock server, document live smoke tests, keep Anthropic API key path unchanged.
  - Rollback: disable/remove `pool/claude` files or route `/v1/messages` with `apikey-first`; existing API key path remains.
- Risk: macOS Claude Code credentials live in Keychain, making import harder than Linux/Windows file import.
  - Mitigation: support proxy-gate-native manual JSON and `~/.claude/.credentials.json` import first; treat Keychain import/full OAuth as alignment-dependent.
  - Rollback: use manual JSON export/import only.
- Risk: Adding another account pool complicates admin/status commands.
  - Mitigation: keep naming explicit (`claude_accounts`, `pool/claude`, `claude-pool`) and add persistence/admin tests.
  - Rollback: leave CLI/admin hidden but keep core `/v1/messages` pool path if needed.
- Risk: Client auth header conflict: Claude Code may use `x-api-key` for client-to-proxy while upstream OAuth needs `Authorization`.
  - Mitigation: `RequireProxyToken` already accepts `X-ProxyGate-Token`; docs should recommend that for tools supporting custom headers. If Claude Code only supports `ANTHROPIC_API_KEY`, proxy token can be disabled for localhost or matched via `ANTHROPIC_API_KEY` as current middleware accepts bearer only plus `X-ProxyGate-Token`.
  - Rollback: keep localhost-only proxy without `PROXYGATE_PROXY_TOKEN` for Claude Code smoke use, or add a narrowly scoped `x-api-key` proxy-token acceptance after security review.
- Risk: Pricing for Claude OAuth requests may be missing/incorrect.
  - Mitigation: record token usage even if unpriced; add pricing misses like Responses path if `Pricer` is wired.
  - Rollback: usage-only recording for `claude-pool`.

## 11. Handoff checklist
- [x] Requirement aligned
- [x] Approach selected
- [x] Tests identified
- [x] Rollout/rollback considered
- [x] Ready for `/execute`
