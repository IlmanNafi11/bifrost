# Spec: Proxy Pool

Date: 2026-11-16 · Owner: bansos · Status: approved (owner sign-off recorded 2026-11-17)

## Objective

Give each **upstream provider API key** (`schemas.Key`, not Bifrost Virtual Keys) its own proxy pool so requests egress through distinct clean IPs. Today Bifrost applies one proxy per provider at build time, so every key under a provider shares one egress IP and gets fingerprinted/banned upstream.

Selection mechanisms (all ship this cycle): `round_robin`, `random`, `weighted`, `least_connections`, `fallback`, `sticky_session`.

Precedence for a request: **key-bound pool → provider proxy → global proxy**. A key with no binding behaves exactly as today. A key bound to a pool that cannot serve fails loudly (`proxy_pool_exhausted`, 503) — never a silent IP leak.

## Tech Stack

Go 1.27.3 (multi-module workspace), GORM (SQLite/Postgres), fasthttp, React + Vite + TanStack Router + Tailwind v4 + RTK Query + i18next.

## Commands

```bash
# Core (schemas, proxypool manager, providers)
cd core && go test ./schemas/ ./proxypool/ ./providers/... -v
cd core && make test-core PROVIDER=openai TESTCASE=TestSimpleChat   # live key; skip if none

# Framework (configstore tables + methods)
cd framework && go test ./configstore/... -v

# Transports (HTTP handler)
cd transports && go test ./bifrost-http/handlers/ -v

# UI
cd ui && npm run i18n:check && npm run build && npx vitest run
cd ui && npm run format:changed   # touched files only

# Harness structural validation (no live paid run)
node tests/e2e/api/runners/augment-provider-harness.mjs --source tests/e2e/api/collections/provider-harness.json --out tmp/harness-augmented.json
node tests/e2e/api/runners/filter-collection.mjs --source tmp/harness-augmented.json --out tmp/filtered.json --feature "proxy pool"
```

## Project Structure

```
core/schemas/proxypool.go          # types, ParseImport, ProxyPoolStore interface
core/schemas/proxypool_test.go
core/proxypool/manager.go          # selection manager, probe, cooldown
core/proxypool/selector.go         # per-mechanism selectors
core/proxypool/manager_test.go
core/proxypool/manager_bench_test.go
core/bifrost.go, core/bifrost_test.go        # proxyForAttempt + proxy_pool_exhausted
core/schemas/bifrost.go                      # BifrostConfig.ProxyPoolStore
core/providers/utils/proxycache.go           # ProxiedClientCache
core/providers/openai/openai.go              # reference provider wiring
core/providers/*                            # sweep remaining providers
framework/configstore/tables/proxypool.go   # GORM tables + migration
framework/configstore/proxypool.go          # ConfigStore CRUD/bind/cascade
framework/configstore/store.go              # ConfigStore interface additions
framework/configstore/rdb.go                # DeleteProviderKey cascade
transports/bifrost-http/handlers/proxypools.go
transports/config.schema.json
docs/openapi/openapi.json
ui/app/workspace/config/proxy-pools/        # menu, list, detail, import, probe
ui/lib/store/apis/proxyPoolsApi.ts          # RTK Query
ui/lib/types/proxyPools.ts
ui/locales/*/proxypool.json                 # new i18n namespace, 11 locales
tests/e2e/api/collections/provider-harness.json
docs/                                        # Mintlify page
SPEC.md
```

## Code Style

- Go filenames: no underscores except `_test.go`. JSON tags `snake_case`; error strings lowercase, no trailing punctuation. Struct field comments are short trailing one-liners.
- Pooled objects: reset **all** fields before `pool.Put`. Never store stream-sized data on `BifrostContext` — manager state keyed by ID only.
- Proxy pool store types:
  ```go
  type ProxyMechanism string // "round_robin" | "random" | "weighted" | "least_connections" | "fallback" | "sticky_session"

  type PoolProxy struct {
      ID, PoolID string
      Type       schemas.ProxyType // http | socks5
      URL, Username string
      Password  *schemas.SecretVar
      Weight    float64
      Enabled   bool
      Status    string
      LatencyMs int64
      EgressIP  string
      LastTestedAt *time.Time
  }

  type ProxyPool struct { ID, Name, Description string; Proxies []PoolProxy }
  type ProxyPoolBinding struct { PoolID, KeyID string; Mechanism ProxyMechanism }

  func ParseImport(text string) ([]PoolProxy, error)
  ```
- Import format, 1 line = 1 proxy, dedupe by `host:port`, blank/`#` skipped:
  `host:port`→http · `http://host:port`→http · `socks5://user:pass@host:port`→socks5+auth · `user:pass@host:port`→http+auth.
- No `sed`/perl edits; use `edit`/`write`/`ast_edit`.

## Testing Strategy

- **Unit tests** in the existing `_test.go` per package; never create a new test file for a package that already has one. New file only for a source file with no test file yet (e.g. `core/proxypool/manager_test.go`).
- **Bug fixes: red before green** — failing test first, confirm it fails for the right reason, then implement.
- **Every wire-visible change** ships a provider-harness case in `tests/e2e/api/collections/provider-harness.json`; never skip an error status code in a harness script; assert exact status + `pm.response.text()`.
- **Provider-native features** tested on every reaching route (native SDK surface, `/v1/responses`, `/v1/chat/completions`, Go SDK, other hosts of the family).
- **Benchmark** `BenchmarkPickProxy_Bound` / `_Unbound` with `-benchmem`; record allocs/op + ns/op before/after in PR body.
- Provider-level tests via `make test-core` (never bare `go test ./core/providers/<name>/...`).

## Boundaries

- **Always do:** run the narrowest test that exercises the change; unbound keys pay zero allocations (nil/empty check only); `go mod tidy` in the specific module dir; validate config examples against `config.schema.json`; i18n all UI strings + `data-testid`; proxy passwords encrypted like other `SecretVar` paths and never returned in API responses.
- **Ask first:** any change to `transports/config.schema.json` shape beyond the pool/binding surface; echo-IP endpoint choice for mode-B probe (configurable, default documented); adding dependencies.
- **Never do:** silently leak the provider/global proxy when a bound pool cannot serve; let a deleted/disabled proxy stay bound; store `proxy_pool_id` on the key as a second source of truth (bindings table is the single source); unbounded proxy client cache (cap 128 LRU per provider); repo-wide `npm run format`.

## Success Criteria

- `core/schemas/proxypool.go` types + `ParseImport` pass (four accepted forms, dedupe, comments, garbage-line error).
- Configstore tables migrate in-memory SQLite and round-trip; `DeletePool`/`DeleteProviderKey` cascade bindings with no orphans.
- `core/proxypool.Manager`: six mechanisms all skip disabled/dead/cooldown; empty/exhausted pool → `ErrPoolExhausted`; cooldown threshold 3 → 60s, success resets; mode-A/B probe records `status`/`latency_ms`/`egress_ip`, failed probe marks `dead`.
- `core/bifrost.proxyForAttempt`: bound key → picked proxy; unbound → provider proxy; exhausted → `proxy_pool_exhausted` 503.
- `ProxiedClientCache` + OpenAI wiring: unbound returns base; bound builds/reuses; cap evicts LRU; every provider under `core/providers/` routes picked proxy through the cache.
- HTTP API: `/api/proxy-pools` CRUD + `import`/`probe`/`disable`/`delete`/`bind`/`unbind`, plus per-key read projection `proxy_pool_id`/`proxy_pool_mechanism`.
- UI: Proxy Pool menu + per-key editor dropdown; `npm run i18n:check` and `npm run build` green.
- Harness cases validate structurally; live run left to the owner.
- Benchmark recorded; unbound path not regressed.

## Open Questions

1. Mode-B echo-IP endpoint — which public endpoint to default to? (builder's call, documented; must tolerate failure → status dead/unknown, never "assume alive".)
