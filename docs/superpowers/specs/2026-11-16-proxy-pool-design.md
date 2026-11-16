# Proxy Pool — Design Doc

Date: 2026-11-16
Author: integration with `bansos` (owner)

## Intent and Why

Bifrost supports only **one** proxy per provider, applied once when the provider
is built (`providerUtils.ConfigureProxy` seeds `client.Dial`). Every upstream
provider API key under that provider therefore egresses from the same IP. A user
with 10+ upstream keys for one provider is fingerprinted by the upstream
provider: all requests arrive on one identity/IP, the IP gets flagged, and keys
get banned.

This feature gives each **upstream provider API key** its own **proxy pool**:
requests rotating across clean IPs, so the upstream sees each key coming from a
fresh, distinct egress. It also provides proxy lifecycle management (import,
probe, disable/delete) from a new UI menu.

The two reference projects solve the same problem:
- `9router-go` — proxies bound per **connection** (= per provider API key), with
  proxy pools storing multiple URLs and a capped transport cache.
- `Cartethyia` — network pools with a rotation strategy (`round_robin`,
  `weighted_least_loaded`, `random`), pool health, and egress IP capture.

## Confirmed decisions

All confirmed in conversation with the owner (`bansos`):

- **"API key" = upstream provider API key** (`schemas.Key`, credits Bifrost uses
  to call OpenAI/Anthropic/…), **not** Bifrost Virtual Keys (`sk-bf-…`). VK stays
  untouched.
- **Precedence for a request**: key-bound pool → provider proxy → global proxy.
  A key without a binding behaves exactly as today (provider proxy, else global
  when enabled for inference). A pool is never silently dropped: a key bound to
  a pool that cannot serve fails loudly rather than leaking the real IP.
- **All selection mechanisms ship in one cycle** (none deferred):
  `round_robin`, `random`, `weighted`, `least_connections`, `fallback`
  (ordered), `sticky_session`.
- **Probe is two-moded**: mode A = fast CONNECT handshake pre-check; mode B =
  CONNECT + fetch a public echo-IP endpoint and record the visible egress IP.
  Both modes run; B is the full verification (the "clean IP" proof), A is the
  cheap pre-check.
- **Import format**, 1 line = 1 proxy, deduped by `host:port`; blank lines and
  `#` comments ignored:
  ```
  host:port                      → http
  http://host:port               → http
  socks5://user:pass@host:port   → socks5 + auth
  user:pass@host:port            → http + auth
  ```
- **Lifecycle semantics**: disable → mark the proxy inactive so every selection
  mechanism skips it (a disabled proxy is never picked; no key unbind is needed
  because bindings are pool-scoped, and a pool with no live proxy fails loudly);
  delete → remove the proxy row. Both single and batch (checkbox). A proxy that
  is disabled or deleted is removed from its pool's candidate set immediately.
- **Auto-cooldown** ships in the same cycle: a proxy failing N consecutive
  attempts is skipped for a cooldown window, then retried. Re-verified on
  `least_connections`/`fallback` health.
- `sticky_session` keys on the **existing** Bifrost session affinity
  (`BifrostContextKeySelectedKeyID`-style) — without a session it falls back to
  `round_robin`.
- **Deployment model (hybrid, owner's pick)**: health/cooldown state and
  sticky-session pins live in the shared `KVStore` (survive restart and
  multi-replica); the round_robin cursor and least_connections in-flight
  counters stay per-instance (best-effort distribution, documented). KV cost is
  paid only on bound-request paths; unbound stays zero-allocation.
- **Single source of truth for a binding**: the `proxy_pool_bindings` table.
  The per-key `proxy_pool_id`/`proxy_pool_mechanism` fields on the key API are a
  read projection of that table (and the write path for it), never a second
  storage location, so no two stores can diverge.
- **Pool selection discipline for ALL mechanisms** (round_robin, random,
  weighted, sticky included, not just fallback): candidates are always filtered
  to `enabled=true` and not `status=dead` and not in cooldown; a pick after
  filtering that finds nothing counts as "pool cannot serve" and fails loudly
  (see below). Fallback additionally advances within a request's attempts when
  the current proxy fails; other mechanisms re-pick per attempt.
- **"Pool cannot serve" is hard**: a bound key whose pool is empty, all-
  disabled, all-dead, or all-in-cooldown aborts with a new error type
  `proxy_pool_exhausted` (HTTP 503) before dialing. It never falls back to the
  provider/global proxy (a silent IP leak) unless the pool itself is empty and
  the owner explicitly opted the binding into provider-proxy fallback at bind
  time (off by default).
- **Binding lifecycle**: deleting a pool auto-unbinds every key bound to it;
  deleting an upstream key cascades its bindings. Bindings reference the string
  `schemas.Key.ID` (`config_keys.key_id`), held across config reloads by the
  existing name-fallback key-continuity logic in the store (numeric
  auto-increment `TableKey.ID` is storage-local and not stable across reloads).
- **Cooldown specifics**: a transport failure through a proxy (dial error,
  timeout, connection reset) increments its consecutive-failure counter; a
  permanent per-proxy error (407 proxy auth, forbidden) marks the proxy
  `dead` instead of cooling down; threshold 3 consecutive failures → cooldown
  60s; any success resets the counter. A mode-B probe result also writes the
  proxy's `status` (alive/dead) and refresh its egress IP.
- **Wire-visible failure contract**: new Bifrost error type
  `proxy_pool_exhausted` (503), unit-tested and pinned in a provider-harness
  case on every reaching route (per AGENTS.md Testing); streaming path surfaces
  it as stream setup error, non-streaming as the request error.

Decisions marked "(builder's call)" were made by the author after the
independent builder check, within its "settle it yourself" remit.

## Goals, Non-Goals, Anti-Goals

### Goals
- Store many proxies per pool; bind a pool + selection mechanism to any upstream
  API key; requests from that key egress through the pool.
- Selection: round_robin, random, weighted, least_connections, fallback, sticky.
- Probe active/dead single and batch (checkbox), with latency + egress IP.
- Import batch (textarea), delete & disable single + batch, auto-unbind.
- New Proxy Pool menu in the UI; per-key enable/dropdown in the API key editor.
- Common case must not regress: a key with no binding pays zero new
  allocations on the request path.

### Non-Goals
- Proxy pools for Bifrost Virtual Keys.
- Routing that also rotates VK→key mapping; unrelated to this feature.
- Changing the existing global-proxy and provider-proxy features.

### Anti-Goals
- Silent egress leak: a bound key must never fall back to a raw IP silently.
- Unbounded proxy clients (proxy connection cache must be capped).
- Breaking byte-stable JSON marshaling of config (unsorted map reorder).

## Constraints

- Multi-module Go workspace (Go 1.27.3); `go mod tidy` per module.
- `config.schema.json` is the source of truth for config fields; docs follow.
- Every wire-visible change ships unit tests + provider-harness case
  (AGENTS.md Testing). Provider-native features tested on every reaching route.
- UI: i18n on every user-visible string; `data-testid` convention; run
  `ui npm run i18n:check`.
- No `sed`/perl edits; `edit` only.
- Per-credential/per-stream state lives in a manager keyed by ID, never on ctx
  with stream-sized data. Pool/egress state is manager state.

## Approaches Considered

1. **Provider-level proxy pool** (proxy pool on `ProviderConfig`, all keys share
   it). Rejected: reintroduces the same leak when a provider owns multiple
   upstream keys — exactly what is being fixed.
2. **Key-level proxy pool, resolved in core at key-selection time** (chosen).
   Key selection already happens per-attempt in `executeRequestWithRetries`
   (`core/bifrost.go:7337`) via `keyProvider`. Resolve the bound pool + pick a
   proxy there, publish it on ctx via the existing `applyProviderProxySignal`
   seam; providers dial the per-proxy cached client. Keys without a binding take
   the existing path unchanged (zero extra allocs).
3. **Per-request rebuild of the provider dialer**. Rejected: rebuilding
   `client.Dial` per request is a shared-mutation hazard and defeats connection
   pooling.

Builder's design follows approach 2.

## The How (decided)

### Data model (framework/configstore)

New tables (GORM, alongside existing config store):

- `proxy_pools` — id (uuid), name, description, created_at, updated_at.
  Bound to no provider; purely a reusable asset.
- `proxy_pool_proxies` — id, pool_id (FK), type (http|socks5), url (line from
  import), username, password, weight (default 1), enabled (bool), status
  (unknown|alive|dead), latency_ms, egress_ip, last_tested_at, consecutive
  failures, cooldown_until, created_at, updated_at. Secrets (password) encrypted
  like other SecretVar storage paths.
- `proxy_pool_bindings` — pool_id, provider_key_id (upstream `schemas.Key` id),
  mechanism (round_robin|random|weighted|least_connections|fallback|sticky),
  unique(pool_id, provider_key_id).

Manager interface `ProxyPoolStore` on `ConfigStore` (mirrors existing
`GetProxyConfig`/`UpdateProxyConfig`): list/get/create/update/delete pools,
import proxies, CRUD proxies, set status (probe results), bind/list/unbind,
list pools for key.

### Core resolution (core/)

- `ProxyPoolManager` (`core/pool/` or `core/proxypool/`): loads bindings,
  caches per-pool proxy slices; **selection per mechanism**:
  - round_robin: atomic cursor per pool (per-instance).
  - random: `math/rand` (locked) per pick.
  - weighted: weighted random across `weight`.
  - least_connections: per-proxy in-flight counter (atomic, per-instance), pick
    min; ties broken by round_robin cursor.
  - fallback: ordered; pick first non-cooldown, non-dead proxy; try next within
    a request's attempts when the current one fails.
  - sticky: session ID from ctx (existing affinity) → pinned proxy held in the
    shared `KVStore` (via the existing `SessionStateKey` machinery); fallback to
    round_robin without a session.
- Cooldown/health state: consecutive-failure counters and `cooldown_until`
  stamps live in the shared `KVStore` (hybrid model), so cooldown and sticky
  pins survive restart and are consistent across replicas. Threshold 3
  consecutive failures → cooldown 60s; a success resets. 407/forbidden marks
  `dead` (no cooldown).
- Resolve at the point key is selected (`keyProvider` closure in the worker,
  `core/bifrost.go:8413-8477`); when the selected key has a binding, compute the
  pool's picked proxy and publish on ctx:
  - extend/reuse `BifrostContextKeyProviderProxyConfig` (per-attempt via the
    existing `applyProviderProxySignal` call at `8555`/`8698` — for a bound key
    this now carries the picked proxy instead of (fallback to) the provider
    proxy). A pool that cannot serve returns `proxy_pool_exhausted` (503).
- Request-path cost for unbound keys: a nil/empty check only; bound keys pay one
  KV read for cooldown/sticky state, one map load, and one selection.

### Provider dialing with per-proxy cache (core/providers)

- Providers keep their base `fasthttp.Client` as today. Add a per-provider
  `proxiedClientCache` keyed by canonical proxy string (host:port+type+creds),
  capped (e.g. 128 entries, mirroring 9router `maxProxyClients`; least-recently
  used eviction). A provider method `clientFor(ctx)`:
  - ctx carries picked proxy (from binding) → cache hit/miss → `ConfigureProxy`
    on a cloned base client, store, return; streaming client likewise.
  - no picked proxy → existing base client (unchanged path).
- The `applyProviderProxySignal` seam already routes side fetches
  (`FetchAndEncodeURL`, token exchange) through the same proxy via
  `BifrostContextKeyProviderProxyConfig` — bound pools inherit this for free.
- Every provider that calls `ConfigureProxy` at build (20+) is touched only to
  route the picked proxy through the cache; the cache constructor is shared in
  `core/providers/utils`.

### API (transports/bifrost-http/handlers + openapi)

`/api/proxy-pools` CRUD; subresources:
- `POST /api/proxy-pools/{id}/import` — textarea batch import (format above).
- `POST /api/proxy-pools/{id}/probe` — body `{ids: [...] | all, mode: a|b}`;
  returns per-proxy `{id, status, latency_ms, egress_ip}`.
- `POST /api/proxy-pools/{id}/delete`, `/disable` — single + batch
  (`{ids: [...]}`); both unbind affected proxies from all keys (disable also
  marks inactive; delete removes rows).
- `POST /api/proxy-pools/{id}/bind` / `/unbind` — `{key_ids: [...]}`,
  mechanism.
Provider config / key APIs expose per-key `proxy_pool_id` + `proxy_pool_mechanism`
as a read projection of `proxy_pool_bindings` (the key editor's write path goes
through `/bind`/`/unbind`; the field is not a second store).
`transports/config.schema.json` updated (source of truth) for the new pool and
binding surface before docs.

### UI (ui/)

- New menu **Proxy Pool** (`workspace/config/proxy-pools`), nav item alongside
  `settings.proxy`; `data-testid` = `proxy-pool-*`.
- Pool list → pool detail: proxies table (status badge, latency, egress IP,
  enabled, weight), checkbox row select + select-all, action bar
  (probe / disable / delete / import textarea dialog), mechanism selector.
- Per-key editor (`providerConfigSheet`/key form): dropdown
  `proxy: none | <pool> + mechanism`.
- i18n: new `proxypool` namespace in every locale; `ui npm run i18n:check` green.

### Docs
`docs/` Mintlify page under config/providers documenting the menu, import
format, mechanisms, probe modes, precedence; examples validated against
`config.schema.json`.

## What's Left to the Builder

- Exact GORM table DDL + migration ordering (follow existing configstore
  migration patterns).
- `math/rand` source details and the least_connections tiebreak counter
  placement (build as manager state, not on ctx).
- Echo-IP endpoint choice + config for mode B (configurable, default a
  documented public endpoint; must tolerate failure → status dead/unknown, never
  silently assume).
- Proxy client cache eviction policy details (LRU cap 128, per-provider).
- Harness case naming/routes for each wire-visible surface (per AGENTS.md
  Testing; a `/v1/responses` case must not carry provider-specific headers).
- UI component structure within existing proxy/config views conventions.

## Costs (per AGENTS.md "every change states its cost")

- **Unbound keys (common case)**: zero allocations — one nil/empty check when
  publishing the proxy signal and when the provider picks a client; bindings own
  a small map load.
- **Bound keys**: one map load (binding→pool) + one selection (atomic
  cursor/rand) + one cached-client lookup at attempt time. No per-request
  rebuild of dialers (client cache is a manager, keyed by canonical proxy
  string, capped at 128 per provider; entries freed by LRU eviction).
- **Memory retained**: the per-provider client cache (≤128 clients × connection
  pools) and per-pool proxy slices in the manager, keyed by pool id, rebuilt on
  pool mutation; the round_robin cursor and least_connections in-flight counts
  are small per-instance atomics. Cooldown counters and sticky pins are KV
  entries (bounded, keyed by `SessionStateKey`/proxy id) — not heap-retained.
- **Benchmarks**: a `Benchmark*` on the bound-key select path with `-benchmem`
  in the new manager test file (before/after allocs and ns recorded in the PR
  body); unbound path not regressed (nil-check only) — stated in PR.
