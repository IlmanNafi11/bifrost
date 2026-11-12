# Todo: VK-level `listed_models`

Spec: `docs/specs/SPEC-vk-listed-models.md` · Plan: `tasks/plan.md`

## Phase 1: Persistence (framework/configstore)

- [x] **T1 — Column `ListedModels` + entry type**
  - Acceptance: JSON round-trip membedakan `[]` vs `nil`; entry rule + provider/model terbaca; `[]` tidak di-collapse jadi nil.
  - Verify: `cd framework && GOWORK=off go test ./configstore/tables/ -run ListedModels`
  - Files: `framework/configstore/tables/virtualkey.go`, `virtualkey_test.go`
  - Deps: none · Size: S

- [x] **T2 — Migration `add_virtual_key_listed_models_column`**
  - Acceptance: migration ter-register ID baru; idempotent SQLite/Postgres; migration test hijau.
  - Verify: `make test-framework` (migration)
  - Files: `framework/configstore/migrations.go`
  - Deps: T1 · Size: S

- [x] **T3 — Hash + `UpdateVirtualKey` allowlist**
  - Acceptance: hash berubah saat listed berubah; stabil restart; tanpa listed hash tak berubah; `ListedModels:nil` preserves stored.
  - Verify: `cd framework && GOWORK=off go test ./configstore/ -run 'VirtualKeyHash|ListedModels'`
  - Files: `framework/configstore/clientconfig.go`, `framework/configstore/rdb.go`, test terkait
  - Deps: T1 · Size: S

### Checkpoint: Persistence
- [x] build + test configstore hijau
- [ ] Review human — migration baru (wajib)

## Phase 2: Grant (core/schemas + framework/grant)

- [ ] **T4 — `schemas` kontrak listed**
  - Acceptance: `ProviderPermit.ListedModels`, `Permit.ListedRules()`, `Access.ModelListed()` terdokumentasi; kompilasi.
  - Verify: `cd core && GOWORK=off go build ./...` (+ test di T5)
  - Files: `core/schemas/grant.go`
  - Deps: none · Size: S
  - ⚠️ Ask first: kontrak interface.

- [ ] **T5 — `framework/grant` implement `ModelListed`**
  - Acceptance: nil → true; `[]` hanya rule (bukan model); deep-copy tidak alias.
  - Verify: `cd framework && GOWORK=off go test ./grant/ -run 'Listed|ModelListed'`
  - Files: `framework/grant/permit.go`, `framework/grant/access.go`, test terkait
  - Deps: T4 · Size: S

## Phase 3: Governance gate + rule cache

- [ ] **T6 — `permitForVirtualKey` membangun listed**
  - Acceptance: permit provider membawa `ListedModels`; permit membawa `ListedRules`; VK tanpa listed tetap nil.
  - Verify: `cd plugins/governance && GOWORK=off go test ./... -run ListedModels`
  - Files: `plugins/governance/store.go`, `permits_test.go`
  - Deps: T4, T5 · Size: S

- [ ] **T7 — `routingrulestore.go` cache definisi rule**
  - Acceptance: aktif+scope cocok+model ∈ definisi → allowed; nonaktif/scope lain/hapus → false; invalidation fail-closed.
  - Verify: `cd plugins/governance && GOWORK=off go test ./... -run RoutingRuleStore`
  - Files: `plugins/governance/routingrulestore.go` (baru), `routingrulestore_test.go` (baru)
  - Deps: none (configstore) · Size: M

- [ ] **T8 — `evaluateAccess` gate requested-name vs listed**
  - Acceptance: nil → perilaku lama; `[]` → non-rule ditolak; rule meneruskan target/fallback; requested-name yang diuji.
  - Verify: `cd plugins/governance && GOWORK=off go test ./... -run 'ListedModels|ModelBlocked|EvaluateRequest'`
  - Files: `plugins/governance/resolver.go`, `resolver_test.go`
  - Deps: T6, T7 · Size: M

- [ ] **T9 — Wiring invalidation di server**
  - Acceptance: edit/hapus rule refresh cache governance; tanpa plugin → warn, routing path tetap.
  - Verify: `cd transports/bifrost-http && GOWORK=off go test ./server/ -run RoutingRule`
  - Files: `transports/bifrost-http/server/server.go`, `routing_test.go`
  - Deps: T7 · Size: S

## Phase 4: Listing

- [ ] **T10 — `filterModelsForAccess` listed-aware + entri rule**
  - Acceptance: `/v1/models` = persis entri + rule aktif/cocok; dashboard tidak terganggu (D9).
  - Verify: `cd plugins/governance && GOWORK=off go test ./... -run 'FilterModelsForAccess|ListedModels'`
  - Files: `plugins/governance/utils.go`, `permits_test.go`
  - Deps: T7, T8 · Size: M

### Checkpoint: Backend gate
- [ ] `make test-governance` + `make test-plugins` hijau

## Phase 5: Handler + config

- [ ] **T11 — Validasi `listed_models` di handler VK create/update**
  - Acceptance: valid diterima; model di luar allowed/tidak dikenal → 400; rule tak ada/scope tak cocok → 400; >100 → 400; dashboard `/api/models` tetap penuh.
  - Verify: `cd transports/bifrost-http && GOWORK=off go test ./handlers/ -run 'VirtualKey|ListedModels'`
  - Files: `transports/bifrost-http/handlers/governance.go`, `governance_test.go`
  - Deps: T8, T10 · Size: M

- [ ] **T12 — `config.schema.json` + name→ID di config load**
  - Acceptance: schema menerima `listed_models`; nama rule di-resolve ke ID; ambigu ditolak; hash stabil.
  - Verify: `cd transports/bifrost-http && GOWORK=off go test ./lib/ -run 'ListedModels|RoutingRuleID'`
  - Files: `transports/config.schema.json`, `transports/bifrost-http/lib/config.go`, `config_test.go`
  - Deps: T11 · Size: M

### Checkpoint: Config end-to-end
- [ ] config.json → DB → permit → listing round-trip

## Phase 6: UI

- [ ] **T13 — Types + zod + helper murni**
  - Acceptance: tipe sesuai shape API; helper opsi picker benar; Vitest hijau.
  - Verify: `cd ui && npx vitest run app/workspace/virtual-keys`
  - Files: `ui/lib/types/governance.ts`, `.../virtualKeySheet.tsx`, test terkait
  - Deps: T11 · Size: S

- [ ] **T14 — `listedModelsEditor.tsx`**
  - Acceptance: add/remove; picker ⊆ allowed+rule; testid `vk-listed-models-*`; cap 100; Vitest hijau.
  - Verify: `cd ui && npx vitest run components/ui/listedModelsEditor`; `npm run typecheck`
  - Files: `ui/components/ui/listedModelsEditor.tsx` (baru) + test
  - Deps: T13 · Size: M

- [ ] **T15 — Wire ke `virtualKeySheet.tsx`**
  - Acceptance: blok tampil; submit kirim `listed_models`; nilai edit ter-load.
  - Verify: `cd ui && npx vitest run app/workspace/virtual-keys`; `npm run typecheck`
  - Files: `ui/app/workspace/virtual-keys/views/virtualKeySheet.tsx`
  - Deps: T14 · Size: S

- [ ] **T16 — i18n semua locale**
  - Acceptance: `npm run i18n:check` exit 0; tidak ada string hardcoded baru.
  - Verify: `cd ui && npm run i18n:check`
  - Files: `ui/locales/*/governance.json` (12)
  - Deps: T14 · Size: M (mekanis)

### Checkpoint: UI
- [ ] typecheck + lint + i18n:check + build hijau

## Phase 7: Verification & Docs

- [ ] **T17 — Harness case (`listed models`)**
  - Acceptance: augment + filter lulus; case red pre-change, green post.
  - Verify: `filter-collection.mjs --feature "listed models"`
  - Files: `tests/e2e/api/collections/provider-harness.json`, `tmp/`
  - Deps: T8, T10 · Size: S

- [ ] **T18 — Docs**
  - Acceptance: `listed_models` terdokumentasi (nil vs [], model vs rule, cap 100, nama→ID).
  - Verify: review manual
  - Files: `docs/deployment-guides/config-json/governance.mdx`
  - Deps: T15 · Size: XS

### Checkpoint: Complete
- [ ] Semua Success Criteria (1-12) terpenuhi
- [ ] make test / test-framework / test-plugins / test-governance + UI checks hijau
- [ ] Siap review