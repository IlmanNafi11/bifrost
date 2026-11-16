# Proxy Pool — Task List

> Tasks from `docs/superpowers/plans/2026-11-16-proxy-pool.md`, reindexed with dependencies and checkpoints. Implement with subagent-driven-development or executing-plans.

## Phase 1: Foundation

- [x] **Task 1: Schema types + import parser**
  - Acceptance: `ProxyMechanism` + six constants; `PoolProxy`/`ProxyPool`/`ProxyPoolBinding`; `ProxyPoolStore` interface; `ParseImport` parses the four accepted forms, dedupes by `host:port`, skips blank/`#` lines, errors on garbage.
  - Verify: `cd core && go test ./schemas/ -run TestParseImport -v` → PASS
  - Files: `core/schemas/proxypool.go`, `core/schemas/proxypool_test.go`
  - Depends on: None

- [x] **Task 2: Configstore tables + migration**
  - Acceptance: `TableProxyPool`/`TablePoolProxy`/`TableProxyPoolBinding` GORM tables; migration `add_proxy_pool_tables` registered; in-memory SQLite round-trips.
  - Verify: `cd framework && go test ./configstore/tables/ -run TestProxyPoolTables -v` → PASS
  - Files: `framework/configstore/tables/proxypool.go`, `framework/configstore/tables/proxypool_test.go`, `framework/configstore/migrations.go`
  - Depends on: Task 1

- [x] **Task 3: ConfigStore methods**
  - Acceptance: CRUD, import (dedupe by URL), disable/delete, bind/unbind, `GetBinding` by key, `DeleteKeyBindings`; `DeletePool` and `DeleteProviderKey` cascade with no orphans; passwords encrypted, never returned.
  - Verify: `cd framework && go test ./configstore/ -run 'TestProxyPool|TestDeletePool|TestDeleteKey|TestSetProxiesEnabledDisables|TestGetBinding' -v` → PASS (full suite: 531s, PASS)
  - Files: `framework/configstore/proxypool.go`, `framework/configstore/proxypool_test.go`, `framework/configstore/store.go`, `framework/configstore/rdb.go`
  - Depends on: Task 2

## Checkpoint: Foundation
- [x] `cd core && go test ./schemas/ -run TestParseImport -v` passes
- [x] `cd framework && go test ./configstore/... -v` passes
- [x] Cascade deletes leave no orphaned bindings

## Phase 2: Core resolution + provider dialing

- [x] **Task 4: Selection manager**
  - Acceptance: six mechanisms all skip disabled/dead/cooldown; empty/exhausted → `ErrPoolExhausted`; cooldown threshold 3 → 60s, success resets, permanent → dead; mode-A/B probe records status/latency/egress_ip, failed probe marks dead.
  - Verify: `cd core && go test ./proxypool/ -v` → PASS
  - Files: `core/proxypool/manager.go`, `core/proxypool/selector.go`, `core/proxypool/manager_test.go`
  - Depends on: Task 3

- [x] **Task 5: Wire selection into Bifrost request path**
  - Acceptance: `BifrostConfig.ProxyPoolStore`; `proxyForAttempt` returns picked proxy for bound key, provider proxy for unbound, terminal `proxy_pool_exhausted` 503 on exhaustion; both `applyProviderProxySignal` calls use it.
  - Verify: `cd core && go test . -run 'TestProxyForAttempt|TestApplyProviderProxySignal' -v` → PASS
  - Files: `core/schemas/bifrost.go`, `core/bifrost.go`, `core/bifrost_test.go`
  - Depends on: Task 4

- [x] **Task 6: Per-proxy client cache + OpenAI wiring**
  - Acceptance: `ProxiedClientCache` LRU cap 128; unbound returns base; bound builds/reuses; OpenAI `clientFor`/`streamingClientFor` route picked proxy.
  - Verify: `cd core && go test ./providers/utils/ ./providers/openai/ -run 'TestProxiedClientCache|TestOpenAIProvider_ClientFor' -v` → PASS
  - Files: `core/providers/utils/proxycache.go`, `core/providers/utils/proxycache_test.go`, `core/providers/openai/openai.go`, `core/providers/openai/proxycache_test.go`
  - Depends on: Task 5

- [x] **Task 7: Sweep remaining providers**
  - Acceptance: every provider under `core/providers/` routes picked proxy through cache; `go build ./...` passes.
  - Verify: `cd core && go build ./... && go test ./providers/... -run TestProxyCache -v` → PASS
  - Files: all `core/providers/<name>/` with `ConfigureProxy` + OpenAI-compatible delegators
  - Depends on: Task 6

## Checkpoint: Core Features
- [x] `cd core && go build ./...` passes
- [ ] Bound key egresses through pool; unbound unchanged; exhausted → 503

## Phase 3: API + UI + harness + docs

- [x] **Task 8: HTTP handler + OpenAPI + config schema**
  - Acceptance: `/api/proxy-pools` CRUD + `import`/`probe`/`disable`/`delete`/`bind`/`unbind`; per-key read projection; config schema + OpenAPI updated.
  - Verify: `cd transports && go test ./bifrost-http/handlers/ -run TestProxyPools -v` → PASS
  - Files: `transports/bifrost-http/handlers/proxypools.go`, `.../proxypools_test.go`, `.../config.go`, `transports/config.schema.json`, `docs/openapi/openapi.json`
  - Depends on: Task 3, Task 4 (probe)

- [ ] **Task 9: UI — Proxy Pool menu + per-key editor dropdown**
  - Acceptance: nav entry `data-testid="proxy-pool-nav"`; pool list/detail/import/probe action bar; per-key `proxy: none | <pool> + mechanism`; new `proxypool` i18n namespace in all 11 locales.
  - Verify: `cd ui && npm run i18n:check && npm run build && npx vitest run` → PASS
  - Files: `ui/app/workspace/config/proxy-pools/*`, `ui/lib/store/apis/proxyPoolsApi.ts`, `ui/lib/types/proxyPools.ts`, `ui/components/navigation/navItems.tsx`, `ui/app/workspace/providers/views/providerKeyForm.tsx`, `ui/lib/types/schemas.ts`, `ui/locales/*/proxypool.json`, `ui/lib/i18n/resources.ts`
  - Depends on: Task 8

- [ ] **Task 10: Provider-harness regression cases**
  - Acceptance: cases for exhausted-pool 503 (exact, no leak), `/v1/responses` + `/v1/chat/completions` with pool bound, import/probe/disable/delete write-through; surgical insert only.
  - Verify: `node tests/e2e/api/runners/augment-provider-harness.mjs ... && node tests/e2e/api/runners/filter-collection.mjs ... --feature "proxy pool"` → exit 0
  - Files: `tests/e2e/api/collections/provider-harness.json`
  - Depends on: Task 5, Task 8

- [ ] **Task 11: Benchmark + docs**
  - Acceptance: `BenchmarkPickProxy_Bound`/`_Unbound` with `-benchmem`; Mintlify page (menu, import format, six mechanisms, probe A/B, precedence, `proxy_pool_exhausted`).
  - Verify: `cd core && go test ./proxypool/ -bench . -benchmem -run '^$'`; docs build
  - Files: `core/proxypool/manager_bench_test.go`, `docs/...`
  - Depends on: Task 4

## Checkpoint: Complete
- [x] `cd transports && go test ./bifrost-http/handlers/ -run TestProxyPools -v` passes
- [ ] `cd ui && npm run i18n:check && npm run build && npx vitest run` passes
- [ ] Harness augment + filter scripts exit 0
- [ ] All SPEC.md acceptance criteria met
