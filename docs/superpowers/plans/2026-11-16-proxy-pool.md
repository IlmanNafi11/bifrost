# Proxy Pool Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give each upstream provider API key its own proxy pool (import, probe, disable/delete, bind) so requests egress through distinct clean IPs selected by round_robin / random / weighted / least_connections / fallback / sticky_session.

**Architecture:** Proxy pools are a reusable configstore asset bound to `schemas.Key.ID` via a `proxy_pool_bindings` table. A `core/proxypool.Manager` resolves the binding at key-selection time inside `executeRequestWithRetries`, publishes the picked proxy as a `*schemas.ProxyConfig` on the existing `BifrostContextKeyProviderProxyConfig` seam, and providers dial it through a capped per-provider client cache. Health/cooldown/sticky state lives in the shared `KVStore` (hybrid); cursor/least-connections counters are per-instance atomics.

**Tech Stack:** Go 1.27.3 (multi-module workspace), GORM (SQLite/Postgres), fasthttp, React + vite + TanStack Router + Tailwind v4 + RTK Query + i18next, `github.com/tidwall/gjson`/`sjson` only where paths are field lookups.

**Spec:** `docs/superpowers/specs/2026-11-16-proxy-pool-design.md`

## Global Constraints

- Go filenames use no underscores except `_test.go`; struct comments are short trailing one-liners.
- JSON tags `snake_case`; error strings lowercase, no trailing punctuation.
- Pool-secret passwords encrypted like other `SecretVar` storage paths; never returned in API responses.
- Every wire-visible change ships unit tests + a provider-harness case (`tests/e2e/api/collections/provider-harness.json`); never skip error status codes in a harness script.
- UI: every user-visible string through i18n (new `proxypool` namespace in all 11 locales), `data-testid` convention, `cd ui && npm run i18n:check` and `npm run build` green; `format:changed` only on touched files.
- No `sed`/perl edits; use `edit`/`write`/`ast_edit`.
- Unbound keys (common case) add zero allocations on the request path — a nil/empty check only.
- Multi-module: `go mod tidy` in the specific module dir, never the root.

## Review Focus

The spec's silence on these inputs is not permission for them to break the program:

1. **A bound key whose pool is empty/all-disabled/all-dead/all-cooldown** must fail loudly with `proxy_pool_exhausted` (503) and never leak the provider/global proxy. → test in Task 5.
2. **`host:port` vs `http://host:port` vs `socks5://` vs `user:pass@host:port` import lines**, plus duplicates, blank lines, and `#` comments, must parse deterministically with dupes dropped by `host:port`. → test in Task 1.
3. **A proxy deleted or disabled while bound** must be auto-unbound from every key that references it (disable also keeps the row inactive). → test in Task 3.
4. **Deleting a pool or an upstream key** must cascade its bindings with no orphaned rows that a recreated key could resurrect. → test in Task 3.
5. **A mode-A probe (CONNECT) and a mode-B probe (CONNECT + echo IP)** must record `status`, `latency_ms`, and (B only) `egress_ip`, and a failed probe must mark `dead` — never silently "assume alive". → test in Task 4.
6. **Non-`fallback` mechanisms** (round_robin, random, weighted, sticky) must also skip disabled/dead/cooldown proxies, not just fallback. → test in Task 4.

---

### Task 1: Schema types + import parser

**Files:**
- Create: `core/schemas/proxypool.go`
- Test: `core/schemas/proxypool_test.go`

**Interfaces:**
- Consumes: `ProxyType` from `core/schemas/provider.go:320`.
- Produces (used by every later task):
  - `type ProxyMechanism string` + constants `ProxyRoundRobin`, `ProxyRandom`, `ProxyWeighted`, `ProxyLeastConnections`, `ProxyFallback`, `ProxyStickySession` (values `"round_robin"`, `"random"`, `"weighted"`, `"least_connections"`, `"fallback"`, `"sticky_session"`).
  - `type PoolProxy struct { ID, PoolID string; Type ProxyType; URL, Username string; Password *SecretVar; Weight float64; Enabled bool; Status string; LatencyMs int64; EgressIP string; LastTestedAt *time.Time }`
  - `type ProxyPool struct { ID, Name, Description string; Proxies []PoolProxy }`
  - `type ProxyPoolBinding struct { PoolID, KeyID string; Mechanism ProxyMechanism }`
  - `type ProxyPoolStore interface { ... }` (methods as in Task 3's Produces).
  - `func ParseImport(text string) ([]PoolProxy, error)` — 1 line = 1 proxy; accepts `host:port`, `http://host:port`, `socks5://user:pass@host:port`, `user:pass@host:port`; dedupes by `host:port`; skips blank lines and `#` comments.

- [ ] **Step 1: Write the failing test** — `TestParseImport_SupportedFormsDedupeAndComments` asserts the four accepted forms produce the right `Type`/`URL`/`Username`/`Password`, duplicates collapse, `#` and blank lines skipped; `TestParseImport_RejectsGarbageLine` asserts a line like `not a proxy line` returns an error naming the line.

- [ ] **Step 2: Run test to verify it fails** — `cd core && go test ./schemas/ -run 'TestParseImport' -v` → FAIL (undefined).

- [ ] **Step 3: Implement the types and `ParseImport` in `core/schemas/proxypool.go`** — split on `\n`, trim, strip `#`-comments, parse scheme/credentials with `net/url`, default bare `host:port` to `http`, auth to `user:pass@host:port` → http.

- [ ] **Step 4: Run test to verify it passes** — `cd core && go test ./schemas/ -run 'TestParseImport' -v` → PASS.

- [ ] **Step 5: Commit** — `git add core/schemas/proxypool.go core/schemas/proxypool_test.go && git commit -m "feat: add proxy pool schema types and import parser"`.

---

### Task 2: Configstore tables + migration

**Files:**
- Create: `framework/configstore/tables/proxypool.go`
- Modify: `framework/configstore/migrations.go` (register migration)
- Test: `framework/configstore/tables/proxypool_test.go`

**Interfaces:**
- Consumes: `schemas.ProxyType`, `schemas.ProxyMechanism` (Task 1).
- Produces (used by Task 3):
  - `type TableProxyPool struct { ID string \`gorm:"primaryKey;type:varchar(64)"\`; Name string; Description string; CreatedAt, UpdatedAt time.Time }` — `TableName() = "proxy_pools"`.
  - `type TablePoolProxy struct { ID string \`gorm:"primaryKey;type:varchar(64)"\`; PoolID string \`gorm:"index;not null"\`; Type string; URL string; Username string; PasswordEncrypted string \`gorm:"column:password_encrypted;type:text"\`; Weight float64; Enabled bool; Status string; LatencyMs int64; EgressIP string; LastTestedAt *time.Time; CreatedAt, UpdatedAt time.Time }` — `TableName() = "proxy_pool_proxies"`.
  - `type TableProxyPoolBinding struct { PoolID string \`gorm:"primaryKey;type:varchar(64)"\`; KeyID string \`gorm:"primaryKey;type:varchar(255)"\`; Mechanism string; CreatedAt time.Time }` — `TableName() = "proxy_pool_bindings"`.
  - Migration ids `add_proxy_pool_tables` with one `AutoMigrate(&TableProxyPool{}, &TablePoolProxy{}, &TableProxyPoolBinding{})`.

- [ ] **Step 1: Write the failing test** — `TestProxyPoolTables_MigrateAndRoundtrip` opens an in-memory SQLite, runs the migration, inserts a pool + proxy + binding, reads them back, asserts column values round-trip.

- [ ] **Step 2: Run test to verify it fails** — `cd framework && go test ./configstore/tables/ -run TestProxyPoolTables -v` → FAIL (undefined types).

- [ ] **Step 3: Implement the three tables and register the migration** in `tables/proxypool.go` + append `{IDs: []string{"add_proxy_pool_tables"}, run: migrationAddProxyPoolTables}` to the migration list.

- [ ] **Step 4: Run test to verify it passes** — `cd framework && go test ./configstore/tables/ -run TestProxyPoolTables -v` → PASS.

- [ ] **Step 5: Commit** — `git add framework/configstore/tables/proxypool.go framework/configstore/tables/proxypool_test.go framework/configstore/migrations.go && git commit -m "feat: add proxy pool configstore tables and migration"`.

---

### Task 3: ConfigStore methods (CRUD, import, bind, cascade, unbind)

**Files:**
- Create: `framework/configstore/proxypool.go`
- Modify: `framework/configstore/store.go` (add methods to `ConfigStore` interface)
- Test: `framework/configstore/proxypool_test.go`

**Interfaces:**
- Consumes: `TableProxyPool`, `TablePoolProxy`, `TableProxyPoolBinding` (Task 2); `schemas.*` (Task 1).
- Produces (used by Tasks 4-6 and the handler):
  - `ListPools(ctx) ([]schemas.ProxyPool, error)`
  - `GetPool(ctx, id string) (*schemas.ProxyPool, error)`
  - `CreatePool(ctx, p *schemas.ProxyPool) error`
  - `UpdatePool(ctx, p *schemas.ProxyPool) error` (name/description only)
  - `DeletePool(ctx, id string) error` — cascades proxies + bindings in one tx.
  - `ImportProxies(ctx, poolID string, proxies []schemas.PoolProxy) error` — dedupe by URL within the insert.
  - `DeleteProxies(ctx, poolID string, ids []string) error` — deletes proxy rows only (bindings are pool-scoped; a proxy is just a member).
  - `SetProxiesEnabled(ctx, poolID string, ids []string, enabled bool) error` — marks rows inactive/active; selection skips inactive (no binding delete).
  - `SetProxyStatus(ctx, id string, status string, latencyMs int64, egressIP string) error`
  - `BindKeys(ctx, poolID string, keyIDs []string, mechanism schemas.ProxyMechanism) error` — upsert by (pool_id, key_id).
  - `UnbindKeys(ctx, poolID string, keyIDs []string) error`
  - `ListBindings(ctx) ([]schemas.ProxyPoolBinding, error)`
  - `GetBinding(ctx, keyID string) (*schemas.ProxyPoolBinding, error)`
  - `DeleteKeyBindings(ctx, keyID string) error` — called from `DeleteProviderKey` cascade.
  - Password encryption/decryption via the existing `encrypt` helpers; API responses never return `PasswordEncrypted`.

- [ ] **Step 1: Write the failing tests** — `TestProxyPoolCRUDAndImport`, `TestDeletePoolCascadesBindings`, `TestDeleteKeyCascadesBindings`, `TestSetProxiesEnabledDisables`, `TestGetBindingByKeyID`.

- [ ] **Step 2: Run tests to verify they fail** — `cd framework && go test ./configstore/ -run 'TestProxyPool|TestDeletePool|TestDeleteKey|TestSetProxiesEnabledDisables|TestGetBinding' -v` → FAIL (methods absent from interface/impl).

- [ ] **Step 3: Implement the methods** on `RDBConfigStore` in `framework/configstore/proxypool.go` and add the signatures to the `ConfigStore` interface in `store.go`; wire `DeleteProviderKey` (rdb.go `DeleteProviderKey`) to call `DeleteKeyBindings` in the same tx.

- [ ] **Step 4: Run tests to verify they pass** — same command → PASS.

- [ ] **Step 5: Commit** — `git add framework/configstore/proxypool.go framework/configstore/proxypool_test.go framework/configstore/store.go framework/configstore/rdb.go && git commit -m "feat: add proxy pool store CRUD, import, binding, and cascade"`.

---

### Task 4: Selection manager (mechanisms, cooldown, sticky, probe helper)

**Files:**
- Create: `core/proxypool/manager.go`
- Create: `core/proxypool/selector.go`
- Test: `core/proxypool/manager_test.go`

**Interfaces:**
- Consumes: `schemas.ProxyPoolStore` (Task 1/3), `schemas.KVStore` (`Get`/`SetWithTTL`/`Delete`), `schemas.ProxyConfig`, `schemas.BifrostContext`, `schemas.Key`.
- Produces (used by Task 5):
  - `var ErrPoolExhausted = errors.New("proxy pool cannot serve: no enabled, live proxy")`
  - `func NewManager(store schemas.ProxyPoolStore, kv schemas.KVStore, logger schemas.Logger) *Manager`
  - `func (m *Manager) PickProxy(ctx *schemas.BifrostContext, keyID string) (*schemas.ProxyConfig, error)` — reads binding, filters `Enabled && Status != "dead" && !cooldown`, applies mechanism; converts the chosen `PoolProxy` to `*schemas.ProxyConfig` (`Type` http/socks5, `URL`/`Username`/`Password`); returns `ErrPoolExhausted` when nothing eligible.
  - `func (m *Manager) RecordSuccess(ctx *schemas.BifrostContext, proxyID string)`
  - `func (m *Manager) RecordFailure(ctx *schemas.BifrostContext, proxyID string, permanent bool)` — increments streak; at 3 marks cooldown 60s; `permanent` marks `dead`.
  - `func (m *Manager) Probe(ctx context.Context, p schemas.PoolProxy, mode schemas.ProxyProbeMode) (status string, latencyMs int64, egressIP string, err error)` — mode A: CONNECT handshake via `network.DialViaProxy`; mode B: A then fetch the configured echo-IP endpoint through the proxy.
- `schemas.ProxyProbeMode` (`"a"`, `"b"`) constant added here or in Task 1.

- [ ] **Step 1: Write the failing tests** — `TestPickProxy_RoundRobin`, `TestPickProxy_AllMechanismsSkipDisabledDeadCooldown`, `TestPickProxy_FallbackAdvancesOnFailure`, `TestPickProxy_EmptyPoolReturnsErrPoolExhausted`, `TestCooldown_Threshold3AndRecovery`, `TestSticky_PinnedAcrossPicksAndFallsBackWithoutSession`, `TestProbe_ModeAConnectAndModeBEchoIP`.

- [ ] **Step 2: Run tests to verify they fail** — `cd core && go test ./proxypool/ -v` → FAIL (package undefined).

- [ ] **Step 3: Implement** the manager and per-mechanism selectors in `manager.go`/`selector.go`. Round_robin uses a per-pool atomic counter; random uses a locked `math/rand`; weighted uses weighted random across `Weight`; least_connections uses per-proxy atomics (ties → cursor); fallback picks first eligible and `RecordFailure` on attempt failure advances the in-request choice; sticky resolves the existing `BifrostContextKeySessionID` and stores the pin under `SessionStateKey(ctx, "proxy-pool", poolID, keyID)` via KV (fall back to round_robin when no session).

- [ ] **Step 4: Run tests to verify they pass** — same command → PASS.

- [ ] **Step 5: Commit** — `git add core/proxypool/manager.go core/proxypool/selector.go core/proxypool/manager_test.go && git commit -m "feat: add proxy pool selection manager with six mechanisms, cooldown, and probe"`.

---

### Task 5: Wire selection into Bifrost request path

**Files:**
- Modify: `core/schemas/bifrost.go` (add `ProxyPoolStore` field to `BifrostConfig`)
- Modify: `core/bifrost.go` (hold manager; resolve proxy per attempt; new error)
- Modify: `core/schemas/bifrost.go` (error type constant, if separate) — else `core/proxypool`.
- Test: `core/bifrost_test.go`

**Interfaces:**
- Consumes: `proxypool.Manager` (Task 4), `proxypool.ErrPoolExhausted`.
- Produces:
  - `BifrostConfig.ProxyPoolStore schemas.ProxyPoolStore` and `Bifrost.ProxyPoolStore` wiring.
  - `func (bifrost *Bifrost) proxyForAttempt(ctx *schemas.BifrostContext, k schemas.Key, config *schemas.ProviderConfig) *schemas.ProxyConfig` — when `k.ID` has a binding, `PickProxy` and return the picked config; else `config.ProxyConfig`. On `ErrPoolExhausted`, set a terminal error so the retry loop returns a new `BifrostError{StatusCode:503, Type:"proxy_pool_exhausted", IsBifrostError:false}` (mirror the `errAllKeysDead` 502 block at `bifrost.go:7513`).
  - Replace the two `applyProviderProxySignal(req.Context, config)` calls (`bifrost.go:8555`, `8698`) with `applyProviderProxySignal(req.Context, bifrost.proxyForAttempt(req.Context, k, config))`.

- [ ] **Step 1: Write the failing tests** — `TestProxyForAttempt_BoundKeyUsesPoolProxy`, `TestProxyForAttempt_UnboundKeyUsesProviderProxy`, `TestProxyForAttempt_ExhaustedPoolReturnsProxyPoolExhausted503`.

- [ ] **Step 2: Run tests to verify they fail** — `cd core && go test . -run TestProxyForAttempt -v` → FAIL (undefined).

- [ ] **Step 3: Implement** the field, manager construction in `Init`, and the `proxyForAttempt`/error path.

- [ ] **Step 4: Run tests to verify they pass** — same command → PASS; also `cd core && go test . -run TestApplyProviderProxySignal -v` → PASS (existing behavior preserved).

- [ ] **Step 5: Commit** — `git add core/schemas/bifrost.go core/bifrost.go core/bifrost_test.go && git commit -m "feat: resolve per-key proxy pool at key-selection time with proxy_pool_exhausted"`.

---

### Task 6: Per-proxy client cache + provider wiring

**Files:**
- Create: `core/providers/utils/proxycache.go`
- Modify: `core/providers/openai/openai.go` (reference wiring)
- Test: `core/providers/utils/proxycache_test.go`, `core/providers/openai/proxycache_test.go`

**Interfaces:**
- Consumes: `schemas.ProxyConfig`, `fasthttp.Client`, `ConfigureProxy`, `BuildStreamingClient`.
- Produces (used by Task 7):
  - `type ProxiedClientCache struct { ... }` + `func NewProxiedClientCache(cap int) *ProxiedClientCache` (cap default 128, LRU).
  - `func (c *ProxiedClientCache) Unary(ctx *schemas.BifrostContext, base *fasthttp.Client, logger schemas.Logger) *fasthttp.Client` and `Streaming(...)` — read the picked proxy from `BifrostContextKeyProviderProxyConfig`; nil → return `base` unchanged; else key by canonical `type|host:port|creds`, build via `ConfigureProxy` on a cloned base + `BuildStreamingClient` for streaming, store, return.
  - `OpenAIProvider` gains `proxied *providerUtils.ProxiedClientCache`; add `func (p *OpenAIProvider) clientFor(ctx) *fasthttp.Client` / `streamingClientFor(ctx)`; replace `provider.client` / `provider.streamingClient` at each request entry with the `For(ctx)` result (mechanical: a per-method one-liner before the `HandleOpenAI*`/`MakeRequestWithContext` call).

- [ ] **Step 1: Write the failing tests** — `TestProxiedClientCache_UnboundReturnsBase`, `TestProxiedClientCache_BoundBuildsAndReuses`, `TestProxiedClientCache_CapEvictsLRU`; `TestOpenAIProvider_ClientForBoundProxyDialthrough`.

- [ ] **Step 2: Run tests to verify they fail** — `cd core && go test ./providers/utils/ ./providers/openai/ -run 'TestProxiedClientCache|TestOpenAIProvider_ClientFor' -v` → FAIL (undefined).

- [ ] **Step 3: Implement** the cache and the OpenAI wiring.

- [ ] **Step 4: Run tests to verify they pass** — same command → PASS; plus `cd core && make test-core PROVIDER=openai TESTCASE=TestSimpleChat` (uses live key if present; skip if no key) and `go test ./providers/openai/ -v` → PASS.

- [ ] **Step 5: Commit** — `git add core/providers/utils/proxycache.go core/providers/utils/proxycache_test.go core/providers/openai/openai.go core/providers/openai/proxycache_test.go && git commit -m "feat: add per-provider proxied client cache and wire OpenAI"`.

---

### Task 7: Sweep remaining providers (mechanical)

**Files:**
- Modify: every provider under `core/providers/<name>/` that calls `ConfigureProxy` (anthropic, azure, baseten, bedrock, bedrockmantle, byteplus, cerebras, cloudflare, cohere, databricks, deepseek, elevenlabs, fireworks, gemini, githubcopilot, groq, huggingface, minimax, mistral, nebius, ollama, opencode, openrouter, parasail, sgl, vertex, vllm, and any OpenAI-compatible delegators that construct their own client).

**Interfaces:**
- Consumes: `ProxiedClientCache`, `clientFor`/`streamingClientFor` pattern (Task 6).
- Produces: identical pattern in each provider — add `proxied *providerUtils.ProxiedClientCache`, add the two `*For(ctx)` methods, swap request-entry `provider.client`/`provider.streamingClient` references for the `For(ctx)` result.

- [ ] **Step 1: Write the failing test** — a compile-level sweep is sufficient; add one representative dial-through test per provider family to the existing `_test.go` (do not create new files for providers that already have one): assert that with a picked proxy on ctx the provider's request entry uses the cached proxied client (mock proxy via `proxytest.ForwardProxy`).

- [ ] **Step 2: Verify it fails** — `cd core && go build ./...` → FAIL (missing `proxied` field) until each provider is updated.

- [ ] **Step 3: Implement** the mechanical wiring per provider, parallelizing across providers that share no state.

- [ ] **Step 4: Verify it passes** — `cd core && go build ./... && go test ./providers/... -run TestProxyCache -v` → PASS.

- [ ] **Step 5: Commit** — `git add core/providers && git commit -m "feat: route per-key proxy pool through every provider's client cache"`.

---

### Task 8: HTTP handler + OpenAPI + config schema

**Files:**
- Create: `transports/bifrost-http/handlers/proxypools.go`
- Modify: `transports/bifrost-http/handlers/config.go` (register routes) or a new registration point
- Modify: `transports/config.schema.json` (pool + binding surface)
- Modify: `docs/openapi/openapi.json` (if the project generates it from handlers)
- Test: `transports/bifrost-http/handlers/proxypools_test.go`

**Interfaces:**
- Consumes: `schemas.ProxyPoolStore`, `proxypool.ParseImport`, `proxypool.Manager.Probe`.
- Produces:
  - `GET /api/proxy-pools`, `POST /api/proxy-pools`, `GET/PUT/DELETE /api/proxy-pools/{id}`.
  - `POST /api/proxy-pools/{id}/import` (body `{text}`) → `ParseImport` → `ImportProxies`.
  - `POST /api/proxy-pools/{id}/probe` (body `{ids: []|null(all), mode:"a"|"b"}`) → runs `Probe` and returns `[{id,status,latency_ms,egress_ip}]`; writes results via `SetProxyStatus`.
  - `POST /api/proxy-pools/{id}/disable` and `/delete` (body `{ids:[...]}`) → `SetProxiesEnabled(false)` / `DeleteProxies`.
  - `POST /api/proxy-pools/{id}/bind` (`{key_ids:[...], mechanism}`) and `/unbind` (`{key_ids:[...]}`).
  - Per-key read projection `proxy_pool_id`/`proxy_pool_mechanism` on the existing key list/get responses (from `GetBinding`).

- [ ] **Step 1: Write the failing tests** — one per endpoint asserting status codes and the write-through effect on the store (never skip an error status in assertions; include `pm.response.text()`-style detail in the Go test).

- [ ] **Step 2: Run tests to verify they fail** — `cd transports && go test ./bifrost-http/handlers/ -run TestProxyPools -v` → FAIL (undefined handler).

- [ ] **Step 3: Implement** the handler, route registration, config schema additions, and OpenAPI additions.

- [ ] **Step 4: Run tests to verify they pass** — same command → PASS.

- [ ] **Step 5: Commit** — `git add transports/bifrost-http/handlers/proxypools.go transports/bifrost-http/handlers/proxypools_test.go transports/bifrost-http/handlers/config.go transports/config.schema.json docs/openapi/openapi.json && git commit -m "feat: add proxy pool HTTP API and schema"`.

---

### Task 9: UI — Proxy Pool menu + per-key editor dropdown

**Files:**
- Create: `ui/app/workspace/config/proxy-pools/layout.tsx`, `page.tsx`, `views/*` (list, detail table, import dialog, probe action bar)
- Create: `ui/lib/store/apis/proxyPoolsApi.ts` (RTK Query)
- Create: `ui/lib/types/proxyPools.ts`
- Modify: `ui/components/navigation/navItems.tsx` (nav entry)
- Modify: `ui/app/workspace/providers/views/providerKeyForm.tsx` + `ui/lib/types/schemas.ts` (pool dropdown + mechanism)
- Modify: `ui/locales/en/proxypool.json` + all 10 other locales (new `proxypool` namespace), `ui/lib/i18n/resources.ts` (register namespace)
- Test: `ui/app/workspace/config/proxy-pools/views/proxyPool.test.ts` (or `*utils.test.ts` per convention)

**Interfaces:**
- Consumes: Task 8 endpoints; `ProviderSelector`/`ModelSelector` conventions; `entitySelectors` for the per-key pool dropdown is NOT needed (pool list is a plain RTK Query select, not an entity selector).
- Produces: nav item `proxy-pools` with `data-testid="proxy-pool-nav"`; pool table `data-testid="proxy-pool-*"`; per-key `proxy: none | <pool> + mechanism` control in the key form.

- [ ] **Step 1: Write the failing tests** — vitest for the import-line parser util and the pool form; assert `data-testid` presence and mechanism values.

- [ ] **Step 2: Run tests to verify they fail** — `cd ui && npx vitest run <spec>` → FAIL.

- [ ] **Step 3: Implement** the pages, RTK Query API, nav entry, key-form dropdown, and all locale catalogs.

- [ ] **Step 4: Verify** — `cd ui && npm run i18n:check && npm run build` → PASS; `npx vitest run` → PASS.

- [ ] **Step 5: Commit** — `git add ui/ && git commit -m "feat: add proxy pool UI and per-key pool dropdown"`.

---

### Task 10: Provider-harness regression cases

**Files:**
- Modify: `tests/e2e/api/collections/provider-harness.json` (surgical insert via a script, never whole-file reserialize)

**Interfaces:**
- Consumes: the wire-visible surfaces from Tasks 5 and 8.

- [ ] **Step 1: Add harness cases** for: (a) a bound key that egresses through a pool proxy (assert no leak when pool exhausted → exact `503` + `proxy_pool_exhausted`, including `pm.response.text()` in the failure message), (b) `/v1/responses` and `/v1/chat/completions` routes reaching the provider with a pool bound (no provider-specific headers on the neutral route), (c) import + probe + disable/delete write-through.

- [ ] **Step 2: Validate structurally** — `node tests/e2e/api/runners/augment-provider-harness.mjs --source tests/e2e/api/collections/provider-harness.json --out tmp/harness-augmented.json && node tests/e2e/api/runners/filter-collection.mjs --source tmp/harness-augmented.json --out tmp/filtered.json --feature "proxy pool"` → both exit 0.

- [ ] **Step 3: Commit** — `git add tests/e2e/api/collections/provider-harness.json && git commit -m "test: add proxy pool provider-harness regression cases"`.

---

### Task 11: Benchmark + docs

**Files:**
- Create: `core/proxypool/manager_bench_test.go`
- Modify: `docs/providers/` or `docs/features/` Mintlify page (per docs-writer conventions); `docs/docs.json` nav if needed.

**Interfaces:**
- Consumes: `PickProxy` bound + unbound paths.

- [ ] **Step 1: Write the benchmark** — `BenchmarkPickProxy_Bound` and `BenchmarkPickProxy_Unbound` with `-benchmem` in `manager_bench_test.go`.

- [ ] **Step 2: Run and record** — `cd core && go test ./proxypool/ -bench . -benchmem -run '^$'`; record allocs/op and ns/op in the PR body (before/after).

- [ ] **Step 3: Write docs** — a Mintlify page documenting the menu, import format, six mechanisms, probe modes A/B, precedence (key pool → provider → global), and the `proxy_pool_exhausted` contract; validate config examples against `config.schema.json`.

- [ ] **Step 4: Verify docs build** — per docs-writer conventions (`npm run` in docs dir if present).

- [ ] **Step 5: Commit** — `git add core/proxypool/manager_bench_test.go docs/ && git commit -m "docs: document proxy pools and add selection benchmark"`.
