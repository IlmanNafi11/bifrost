# Implementation Plan: Proxy Pool

## Overview

Give each upstream provider API key (`schemas.Key`) its own proxy pool so requests egress through distinct clean IPs, selected by one of six mechanisms (`round_robin`, `random`, `weighted`, `least_connections`, `fallback`, `sticky_session`). Precedence: key-bound pool → provider proxy → global proxy. A bound key whose pool cannot serve fails loudly (`proxy_pool_exhausted`, 503) — never a silent IP leak.

Source of truth: `SPEC.md` (approved) + `docs/superpowers/specs/2026-11-16-proxy-pool-design.md`. The existing `docs/superpowers/plans/2026-11-16-proxy-pool.md` already contains a fine-grained 11-task breakdown; this plan adopts its architecture and task order, restructured to record dependencies, checkpoints, and vertical-slice rationale.

## Architecture Decisions

- **Key-level resolution at key-selection time** (rejected: provider-level pool — reintroduces the leak; per-request dialer rebuild — shared-mutation hazard). Publish picked proxy on the existing `BifrostContextKeyProviderProxyConfig` seam via `applyProviderProxySignal`.
- **Single source of truth for bindings**: `proxy_pool_bindings` table; per-key `proxy_pool_id`/`proxy_pool_mechanism` on the key API are a read projection, never a second store.
- **Hybrid state model**: cooldown/sticky pins in shared `KVStore` (survive restart, multi-replica); round_robin cursor + least_connections counters per-instance atomics.
- **Capped per-provider client cache** (`ProxiedClientCache`, LRU cap 128) keyed by canonical `type|host:port|creds`; unbound requests return the base client unchanged (zero allocs).
- **Six mechanisms, one cycle** — none deferred.

## Task List

### Phase 1: Foundation (types → tables → store)

- [x] Task 1: Schema types + import parser (`core/schemas/proxypool.go`)
- [x] Task 2: Configstore tables + migration (`framework/configstore/tables/proxypool.go`)
- [x] Task 3: ConfigStore methods — CRUD, import, bind, cascade, unbind

### Checkpoint: Foundation
- [x] `cd core && go test ./schemas/ -run TestParseImport -v` passes
- [x] `cd framework && go test ./configstore/... -v` passes
- [x] Pools, proxies, bindings round-trip in SQLite; cascade deletes leave no orphans
- [ ] Review with human before proceeding

### Phase 2: Core resolution + provider dialing

- [x] Task 4: Selection manager (six mechanisms, cooldown, sticky, probe)
- [x] Task 5: Wire selection into Bifrost request path (`proxyForAttempt`)
- [ ] Task 6: Per-proxy client cache + OpenAI reference wiring
- [x] Task 7: Sweep remaining providers (mechanical)

### Checkpoint: Core Features
- [ ] `cd core && go test ./proxypool/ ./providers/utils/ ./providers/openai/ -v` passes
- [x] `cd core && go test . -run 'TestProxyForAttempt|TestApplyProviderProxySignal' -v` passes
- [x] `cd core && go build ./...` passes
- [ ] Bound key egresses through pool proxy; unbound key unchanged; exhausted pool → 503
- [ ] Review with human before proceeding

### Phase 3: API + UI + harness + docs

- [x] Task 8: HTTP handler + OpenAPI + config schema
- [x] Task 9: UI — Proxy Pool menu + per-key editor dropdown
- [ ] Task 10: Provider-harness regression cases
- [ ] Task 11: Benchmark + docs

### Checkpoint: Complete
- [x] `cd transports && go test ./bifrost-http/handlers/ -run TestProxyPools -v` passes
- [ ] `cd ui && npm run i18n:check && npm run build && npx vitest run` passes
- [ ] Harness augment + filter scripts exit 0
- [ ] All acceptance criteria in SPEC.md met; ready for review

## Risks and Mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| Silent egress leak when pool exhausted | High | `ErrPoolExhausted` → terminal 503 before dialing; pinned by harness case |
| Import parser ambiguity (4 forms + dedupe + comments) | Med | Task 1 red/green tests pin deterministic parsing |
| Cascade orphans (pool/key deleted, binding resurrected) | Med | Task 3 tx-level cascade tests |
| Probe false-alive | High | Failed probe marks `dead`; mode B tolerates echo failure → dead/unknown |
| Non-fallback mechanisms picking dead/disabled | Med | Task 4 test forces all six mechanisms to skip ineligible proxies |
| Per-provider client cache unbounded | Med | LRU cap 128; eviction test |
| Provider sweep (20+ files) compile breakage | Med | `go build ./...` gate; representative dial-through test per family |
| Common-case (unbound) regression | High | Nil/empty check only; benchmark records before/after allocs |

## Open Questions

1. Mode-B echo-IP endpoint default (configurable, documented; tolerate failure). Builder's call pending — resolve in Task 4.
