# Implementation Plan: VK-level `listed_models`

## Overview

Menambahkan satu blok VK-level `listed_models` yang menjadi **sumber tunggal isi `/v1/models`** untuk sebuah virtual key dan **batas model yang boleh dipanggil** (di luar daftar → 403), dengan pengecualian: nama **routing rule** yang didaftarkan boleh dipanggil dan menembus ke seluruh target+fallback rule tersebut. Blok ini berlapis di atas `allowed_models` (entri model wajib lolos `allowed_models` dulu), `nil` = fitur nonaktif (perilaku hari ini), `[]` = tolak semua. Rule yang didaftarkan divalidasi terhadap definisinya (target+fallback) — bukan bypass — dan gate provider tetap berlaku.

Spec: `docs/specs/SPEC-vk-listed-models.md` (disetujui). Intent: `docs/intent/vk-listed-models.md`.

## Architecture Decisions

| # | Decision | Rationale |
|---|---|---|
| A1 | `listed_models` adalah satu kolom JSON tekstual (`gorm:"type:text;serializer:json"`) pada `TableVirtualKey`, berisi `[]TableListedModelEntry{Provider string; Model string; RoutingRuleID string}` | Precedent `Metadata map[string]string` (virtualkey.go:294) memakai `serializer:json`; GORM round-trip slice-of-struct tanpa hook. Entri rule disimpan sebagai **ID** (tahan rename, tidak ambigu antar-scope — open question #2). |
| A2 | Kolom config-only `RoutingRuleName` (`gorm:"-"`) pada entry: `config.json` menulis nama rule, server me-resolve ke `RoutingRuleID` saat load, ambigu → ditolak | Mengikuti pola `provider_key_name`→`key_id` (RoutingFallback/TableRoutingTarget) dan precedent `resolveRoutingRuleIDs` (config.go:5102). Listing menampilkan **nama** rule (dari ID), karena `id` rule di `/models` adalah nama polos (D10). |
| A3 | Nil vs kosong dibedakan di `BeforeSave`: `nil` (absen) → NULL (fitur nonaktif); `[]` → `'[]'` (tolak semua) | D7. `Metadata` menormalisasi `[]`→nil (virtualkey.go:473-537); `listed_models` **tidak** melakukan itu — `[]` punya arti deny-by-default. Normalisasi hanya: drop entry kosong, sort deterministik untuk hash. |
| A4 | Hash `GenerateVirtualKeyHash` menyertakan `listed_models` hanya saat non-empty, entry di-sort (provider, model, routing_rule_id) | D8/#8 → restart tidak me-reset config; non-empty-only menjaga hash lama VK yang predate kolom (pola `Metadata` di clientconfig.go:1452). |
| A5 | `schemas.ProviderPermit` dapat `ListedModels WhiteList` (nama model, per provider); VK-level `ListedRules` (set rule ID) dibawa di `Permit`; `schemas.Access` dapat method `ModelListed(provider, model)` | Spec Code Style mencontohkan `access.ModelListed(...)` dibaca dari objek akses yang sama dengan `AllowedModels`; `ListedModels` per provider cukup karena entry model adalah `{provider, model}` dan permit sudah provider-scoped. Rule tidak provider-scoped → hidup di level permit, bukan provider permit. |
| A6 | Gate membaca `BifrostContextKeyRequestedModel` (nama pra-rewrite), **bukan** `req.Model` post-rewrite | D12. `core/utils.go:402` (`stampRequestedRoute`) sudah men-stamp `BifrostContextKeyRequestedModel`; routing me-rewrite model di `routing/main.go:492` *sebelum* `PreLLMHook` governance membaca `req.GetRequestFields()`. |
| A7 | Governance punya cache definisi rule sendiri (`routingrulestore.go`) yang membaca `configstore.ConfigStore.GetRoutingRules` — **bukan** `plugins/routing` (governance tidak boleh import routing: dep satu-arah) | GovFunnelMap: `plugins/governance/go.mod` hanya require core+framework. Cache dibangun saat init, di-invalidate lewat callback server `ReloadRoutingRule`/`RemoveRoutingRule` (server.go:1493/1510) yang sekarang juga memanggil governance. |
| A8 | Scope-match rule di governance adalah helper lokal yang mereproduksi rantai scope routing (VK → user → team → customer → global) dari identifier yang sudah di-stamp `StampVirtualKeyScope` | governance tidak bisa pakai `rules/buildScopeChain` (engine.go:408). Rantai identifier sudah dimiliki governance di context; helper lokal menjaga fail-closed tanpa dep baru. |
| A9 | Validasi entri (D3/D6): model ⊆ `allowed_models` penyedia-nya, rule ada + aktif + scope cocok, cap 100 entry — ditegakkan di **handler** VK create/update (API langsung) DAN di UI picker | Spec Testing Strategy ("Handler tests") + Success Criteria #7/#11; server-side enforcement jadi tidak divergen dari UI. |

## Dependency Graph

```
T1 field+entry (tables) ─┬─ T6 permitForVirtualKey builds listed
T2 migration             ─┘
T3 hash + rdb            ─── config sync
T4 grant schema (core)   ─┬─ T5 grant impl
                          └─ T6 permit
T7 routingrulestore ─────── T8 evaluateAccess gate
T9 server invalidation ───── T7
T8 ─ T10 filterModelsForAccess listing
T6/T7/T8 ─ T11 handler validation
T11 ─ T12 config schema + name→ID resolution
T11/T12 ─ T13 UI types/zod ─ T14 editor ─ T15 sheet wiring ─ T16 i18n
T8/T10 ─ T17 harness case
T15 ─ T18 docs
```

Build order: T1 → T2/T3 → T4 → T5 → T6 → T7 → T8 → T9 → T10 → T11 → T12 → T13 → T14 → T15 → T16 → T17 → T18.

## Task List

### Phase 1: Persistence (framework/configstore)

- [x] **Task 1: Column `ListedModels` + entry type**
  - Description: `type TableListedModelEntry struct { Provider, Model, RoutingRuleID string }` (semua `json:"..."`; `RoutingRuleName string \`json:"routing_rule,omitempty"\`` sebagai alias config-only `gorm:"-"` untuk nama rule) di `tables/virtualkey.go`; field `ListedModels []TableListedModelEntry \`gorm:"type:text;serializer:json" json:"listed_models,omitempty"\`` di `TableVirtualKey`; normalisasi di `BeforeSave` (drop entry kosong, **jangan** collapse `[]`→nil) + validator `ValidateVirtualKeyListedModels` (cap 100, entry model XOR rule, tidak boleh `*`).
  - Acceptance: JSON round-trip `[]` vs `nil` terbedakan; entry rule + provider/model terbaca; `[]` tidak jadi nil.
  - Verify: `cd framework && GOWORK=off go test ./configstore/tables/ -run ListedModels`.
  - Deps: None. Files: `framework/configstore/tables/virtualkey.go`, `virtualkey_test.go` (existing).
  - Scope: S (2).

- [x] **Task 2: Migration `add_virtual_key_listed_models_column`**
  - Description: `migrationAddVirtualKeyListedModelsColumn` mirroring `migrationAddVirtualKeyMetadataColumn` (migrations.go:16327) — `addColumnIfNotExists(tx, logger, &tables.TableVirtualKey{}, "ListedModels")` via `setMigrationLockTimeout`; registration di akhir `configstoreMigrationSteps` (setelah baris 592, ID baru unik); Rollback non-rollbackable.
  - Acceptance: migration ter-register dengan ID baru; SQLite + Postgres column-exists idempotent; `make test-framework` migration test hijau.
  - Verify: `make test-framework TESTCASE=Migration` (atau `cd framework && GOWORK=off go test ./configstore/ -run Migration`).
  - Deps: T1. Files: `framework/configstore/migrations.go`.
  - Scope: S (1).

- [x] **Task 3: Hash + `UpdateVirtualKey` allowlist**
  - Description: `GenerateVirtualKeyHash` menulis `listed_models` hanya saat non-empty, entry di-sort + JSON-deterministik (pola `metadata` clientconfig.go:1452-1477); `UpdateVirtualKey` (rdb.go:4589) conditionally append `"listed_models"` ke `columns` bila non-nil (pola `metadata` rdb.go:4630-4634 pointer-decide).
  - Acceptance: hash berubah saat `listed_models` berubah, stabil saat di-restart; VK tanpa `listed_models` hash-nya tidak berubah; `UpdateVirtualKey` dengan `ListedModels:nil` mempertahankan nilai stored.
  - Verify: `cd framework && GOWORK=off go test ./configstore/ -run 'VirtualKeyHash|ListedModels'`.
  - Deps: T1. Files: `framework/configstore/clientconfig.go`, `framework/configstore/rdb.go`, `clientconfig_test.go`/`rdb_test.go`.
  - Scope: S (3).

### Checkpoint: Persistence
- [x] `go build ./core/... ./framework/...` dan test configstore hijau
- [ ] **Review human — migration baru (spec "Ask first")**

### Phase 2: Grant (core/schemas + framework/grant)

- [x] **Task 4: `schemas` — `ListedModels`/`ListedRules` + `Access.ModelListed`**
  - Description: `ProviderPermit.ListedModels WhiteList` (nama model; nil=nonaktif, []=deny-all) di `core/schemas/grant.go`; `Permit` dapat method `ListedRules() []string` (rule ID, VK-level); `Access` dapat `ModelListed(provider, model) bool` — true bila VK tanpa blok (nil), atau model ada di `ListedModels` provider itu.
  - Acceptance: kompilasi; kontrak interface terdokumentasi (nil vs [] semantics).
  - Verify: `cd core && GOWORK=off go build ./...` + unit kecil di `framework/grant` (T5).
  - Deps: None. Files: `core/schemas/grant.go`.
  - Scope: S (1). *Ask first: kontrak interface.*

- [x] **Task 5: `framework/grant` implement `ModelListed`**
  - Description: `NewPermit`/`NewAccess` membawa `ListedModels`/`ListedRules`; `cloneProviderPermits` deep-copy `ListedModels`; `Access.ModelListed` membaca union allowed (nil = fitur nonaktif → true; satu permit men-list narrows). Tambah `WithListedRules(...)` permit option.
  - Acceptance: `ModelListed` true untuk VK nil, untuk `[]` hanya rule (bukan model) yang lolos di level ini; deep-copy tidak alias.
  - Verify: `cd framework && GOWORK=off go test ./grant/ -run 'Listed|ModelListed'`.
  - Deps: T4. Files: `framework/grant/permit.go`, `framework/grant/access.go`, `permit_test.go`/`access_test.go`.
  - Scope: S (3).

### Phase 3: Governance gate + rule cache (plugins/governance)

- [x] **Task 6: `permitForVirtualKey` membangun listed**
  - Description: dari `TableVirtualKey.ListedModels`, bangun `ProviderPermit.ListedModels` per provider (entry model) dan `WithListedRules(ruleIDs)` di `grant.NewPermit` (store.go:1528). Entry model di luar provider permit → skip (fail closed; validasi handler sudah menolak, ini jaring kedua).
  - Acceptance: permit provider B membawa `ListedModels=["gpt-5.4"]`; permit membawa `ListedRules` rule IDs; VK tanpa `listed_models` tetap nil.
  - Verify: `cd plugins/governance && GOWORK=off go test ./... -run ListedModels`.
  - Deps: T4, T5. Files: `plugins/governance/store.go`, `permits_test.go`.
  - Scope: S (2).

- [x] **Task 7: `routingrulestore.go` — cache definisi rule**
  - Description: `RoutingRuleStore` membaca `configstore.ConfigStore.GetRoutingRules` saat init, index by ID → `{Name, Targets, Fallbacks, Scope, ScopeID, Enabled}`; method `AllowsRequest(ctx, requestedRuleName, provider, model)` yang: resolve rule by **name** (listing/entry memakai nama call, D10), cek aktif + scope cocok (rantai VK→user→team→customer→global dari stamp context), cek model final `provider`/`model` ∈ definisi rule (target+fallback through `RoutingFallback.Resolved()`, fallback ke provider/model); method `Reload(id)`/`Remove(id)` untuk invalidation; fail closed saat rule hilang.
  - Acceptance: rule aktif+scope cocok+model ∈ definisi → allowed; rule nonaktif/scope lain/hapus → false; invalidation sebelum request berikutnya (test cache).
  - Verify: `cd plugins/governance && GOWORK=off go test ./... -run RoutingRuleStore`.
  - Deps: None (hanya configstore). Files: `plugins/governance/routingrulestore.go` (baru), `routingrulestore_test.go` (baru).
  - Scope: M (2).

- [x] **Task 8: `evaluateAccess` gate requested-name vs listed**
  - Description: di `resolver.go` (dalam `evaluateAccess`, setelah model gate ba…sekaligus sebelum/di samping `IsModelAllowed` block resolver.go:166): baca `requested := GetStringFromContext(ctx, BifrostContextKeyRequestedModel)`; bila `requested != "" && !access.ModelListed(provider, requested)` && rule-set tidak allow → `DecisionModelBlocked` ("Model '%s' is not listed for this virtual key"). Model final tetap diperiksa terhadap `allowed_models`/definisi rule (D12/D4); provider gate tetap berjalan (D5). `IsProbe` tidak men-skip gate ini (probe pun harus listed).
  - Acceptance: VK nil → perilaku lama; `[]` → model non-rule ditolak; rule teregister meneruskan ke target/fallback meski target tidak di allowed/listed; requested-name (bukan post-rewrite) yang diuji.
  - Verify: `cd plugins/governance && GOWORK=off go test ./... -run 'ListedModels|ModelBlocked|EvaluateRequest'`.
  - Deps: T6, T7. Files: `plugins/governance/resolver.go`, `resolver_test.go`.
  - Scope: M (2).

- [x] **Task 9: Wiring invalidation di server**
  - Description: `ReloadRoutingRule`/`RemoveRoutingRule` (server.go:1493/1510) menambahkan panggilan ke governance store (via `getGovernancePlugin().GetGovernanceStore()` atau method baru `BaseGovernancePlugin`), memanggil `RoutingRuleStore.Reload/Remove` sebelum/bersamaan routing plugin.
  - Acceptance: edit/hapus rule memicu refresh cache governance; tanpa governance plugin → error tidak menelan routing path (warn).
  - Verify: `cd transports/bifrost-http && GOWORK=off go test ./server/ -run 'RoutingRule'` (atau test handler routing).
  - Deps: T7. Files: `transports/bifrost-http/server/server.go`, `routing_test.go`.
  - Scope: S (2).

### Phase 4: Listing

- [x] **Task 10: `filterModelsForAccess` listed-aware + entri rule**
  - Description: `filterModelsForAccess` (utils.go:134) — saat access membawa `ListedModels`, daftar berisi tepat: entry model yang ada di `ListedModels` (prefix `provider/model`, D10) + entry rule (nama polos, dari `ListedRules` yang aktif + scope cocok). Saat access nil (no listing) → perilaku lama. Wiring `PostLLMHook` (main.go:1639) tidak berubah — lewat access yang sama.
  - Acceptance: `/v1/models` VK ber-listed = persis entri + rule; rule nonaktif/scope lain hilang; dashboard `/api/models` tidak terpengaruh (D9, tidak lewat jalur ini).
  - Verify: `cd plugins/governance && GOWORK=off go test ./... -run 'FilterModelsForAccess|ListedModels'`.
  - Deps: T7, T8. Files: `plugins/governance/utils.go`, `permits_test.go`.
  - Scope: M (2).

### Checkpoint: Backend gate
- [x] `make test-governance` dan `make test-plugins` hijau
- [ ] Manual: VK ber-listed memanggil non-listed → 403; `/v1/models` listing tepat

### Phase 5: Handler + config

- [x] **Task 11: Validasi `listed_models` di handler VK create/update**
  - Description: `CreateVirtualKeyRequest`/`UpdateVirtualKeyRequest` dapat field `ListedModels []ListedModelEntryBody{Provider, Model, RoutingRule}`; validasi pra-transaksi di `createVirtualKey` (governance.go:2160) & `updateVirtualKey` (2610): model ⊆ `allowed_models` provider-nya (tidak dikenal → 400), rule ada + scope cocok (dari `GetRoutingRules`), cap 100 → 400, `*` ditolak. Map ke `TableListedModelEntry` (rule name → ID di handler, bukan config load).
  - Acceptance: create/update dengan entry valid diterima; model di luar `allowed_models`/tidak dikenal → 400; rule tidak ada/scope tidak cocok → 400; >100 → 400; `GET /api/models` (dashboard) tetap penuh.
  - Verify: `cd transports/bifrost-http && GOWORK=off go test ./handlers/ -run 'VirtualKey|ListedModels'`.
  - Deps: T8, T10. Files: `transports/bifrost-http/handlers/governance.go`, `governance_test.go`.
  - Scope: M (3).

- [x] **Task 12: `config.schema.json` + name→ID di config load**
  - Description: `virtual_keys.items.properties.listed_models` di `transports/config.schema.json` (items dengan `provider`/`model`/`routing_rule`); di `transports/bifrost-http/lib/config.go` merge VK loop (config.go:4085) resolve `RoutingRuleName`→`RoutingRuleID` via helper mirip `resolveRoutingRuleIDs` (config.go:5102), ambigu → warn + skip entry (fail closed), sebelum hash.
  - Acceptance: schema menerima `listed_models`; config.json dengan nama rule di-resolve ke ID; ambigu ditolak; hash stabil.
  - Verify: `python docs/openapi/bundle.py` (bila relevan) + `cd transports/bifrost-http && GOWORK=off go test ./lib/ -run 'ListedModels|RoutingRuleID'`.
  - Deps: T11. Files: `transports/config.schema.json`, `transports/bifrost-http/lib/config.go`, `config_test.go`.
  - Scope: M (3).

### Checkpoint: Config end-to-end
- [ ] config.json → DB → permit → listing round-trip manual

### Phase 6: UI

- [ ] **Task 13: Types + zod + helper murni**
  - Description: `ListedModelsEntry` di `ui/lib/types/governance.ts`; `VirtualKey` + create/update request dapat `listed_models`; validator zod lokal di `virtualKeySheet.tsx`; helper murni `listedModelsOptionSource(providerConfigs, rules)` untuk picker (hanya model dari allowed + rule scope cocok).
  - Acceptance: tipe sesuai shape API; helper mengembalikan opsi yang benar dari allowed+rule; Vitest murni hijau.
  - Verify: `cd ui && npx vitest run app/workspace/virtual-keys`.
  - Deps: T11 (shape). Files: `ui/lib/types/governance.ts`, `ui/app/workspace/virtual-keys/views/virtualKeySheet.tsx`, test terkait.
  - Scope: S (3).

- [ ] **Task 14: `listedModelsEditor.tsx`**
  - Description: editor daftar entry `{provider, model}` atau rule, selector di atas + baris removable (pola `relayGrantsEditor`/`agentGrantsEditor`); picker model dari `allowed_models` + rule scope-cocok; testid `vk-listed-models-add`, `vk-listed-models-remove-${i}`; cap 100.
  - Acceptance: baris add/remove; picker tidak menawarkan model di luar allowed; testid sesuai spec; Vitest komponen hijau.
  - Verify: `cd ui && npx vitest run components/ui/listedModelsEditor`; `npm run typecheck`.
  - Deps: T13. Files: `ui/components/ui/listedModelsEditor.tsx` (baru), `listedModelsEditor.test.tsx` (baru).
  - Scope: M (2).

- [ ] **Task 15: Wire ke `virtualKeySheet.tsx`**
  - Description: blok "Allowed listed models" di atas/setelah Provider Configurations (gated `!isGovernedByOwnerProfile`), map ke `form` state, serialize pada create/update request.
  - Acceptance: blok tampil; submit mengirim `listed_models`; nilai edit ter-load.
  - Verify: `cd ui && npx vitest run app/workspace/virtual-keys`; `npm run typecheck`.
  - Deps: T14. Files: `ui/app/workspace/virtual-keys/views/virtualKeySheet.tsx`.
  - Scope: S (1).

- [ ] **Task 16: i18n semua locale**
  - Description: key `listedModels.*` (title/tooltip/add/remove/empty/validation) di namespace `governance`, ditambah ke `en` + 11 locale lain dengan terjemahan nyata.
  - Acceptance: `npm run i18n:check` exit 0; tidak ada string hardcoded baru.
  - Verify: `cd ui && npm run i18n:check`.
  - Deps: T14 (nama key final). Files: `ui/locales/*/governance.json` (12).
  - Scope: M (12, mekanis).

### Checkpoint: UI
- [ ] `npm run typecheck` + `npm run lint` + `npm run i18n:check` + `npm run build` hijau

### Phase 7: Verification & Docs

- [x] **Task 17: Harness case (`listed models`)**
  - Description: case di `provider-harness.json` — `/v1/models` dengan VK ber-`listed_models` mengembalikan persis entri (D10), panggilan model non-listed → 403, `coding` → rule jalan. Insersi surgical (splice).
  - Acceptance: augment + filter lulus; case red pre-change, green post (validasi struktural).
  - Verify: `node tests/e2e/api/runners/augment-provider-harness.mjs ...` lalu `filter-collection.mjs --feature "listed models"`.
  - Deps: T8, T10. Files: `tests/e2e/api/collections/provider-harness.json`, `tmp/` (artefak).
  - Scope: S (1).

- [x] **Task 18: Docs**
  - Description: paragraf `listed_models` di `docs/deployment-guides/config-json/governance.mdx` (bentuk, nil vs [], entri model vs rule, cap 100, nama→ID di load).
  - Acceptance: contoh konsisten; tidak ada config baru yang tidak didokumentasikan.
  - Verify: review manual.
  - Deps: T15. Files: `docs/deployment-guides/config-json/governance.mdx`.
  - Scope: XS (1).

### Checkpoint: Complete
- [ ] Semua Success Criteria spec (1-12) terpenuhi
- [ ] `make test`, `make test-framework`, `make test-plugins`, `make test-governance`, UI checks hijau
- [ ] Siap review

## Risks and Mitigations

| Risk | Impact | Mitigation |
|---|---|---|
| Rule cache governance stale vs routing (rule diedit tapi hanya satu yang di-invalidate) | High | T9 menambahkan panggilan governance di method server yang sama yang sudah dipanggil routing handler; test invalidation (T7) fail-closed saat rule hilang. |
| Scope-match governance divergen dari routing (rantai scope digandakan) | Med | Helper lokal memakai identifier yang sudah di-stamp `StampVirtualKeyScope`; unit test eksplisit per level scope (VK/user/team/customer/global). |
| `[]` vs `nil` tertukar di JSON round-trip | Med | T1 test round-trip eksplisit; `BeforeSave` tidak collapse `[]`; hash non-empty-only. |
| Gate melewat `allowed_models` (model listed tapi provider config-nya sempit) | Med | D3/D12: `ModelListed` hanya lapisan kedua — `IsModelAllowed` tetap berjalan di evaluateAccess (resolver.go:166); validasi handler menolak model di luar `allowed_models`. |
| Rule di `listed_models` dihapus → `/models` masih menampilkan nama (stale) | High | T7 cache + T9 invalidation; listing T10 men-drop rule nonaktif/scope tak cocok (D6). |
| `UpdateVirtualKey` allowlist tidak menyertakan kolom → config sync drift permanen | High | T3 conditional-append seperti `metadata`; drift test (Success #8). |
| Interface `Access`/`Permit` berubah → implementasi/mocks enterprise break | Med | T4 minimal (satu method baru + field); "Ask first" dijadwalkan; kompilasi lintas modul (`go build ./...`) sebelum merge. |

## Parallelization Opportunities

- **Aman paralel:** T2/T3 setelah T1; T5 setelah T4; T12 (config) setelah T11; T16 (i18n) setelah nama key final.
- **Harus sekuensial:** T4→T5→T6→T8 (kontrak grant), T7→T8→T10 (gate→listing), T13→T14→T15 (UI).
- **Perlu koordinasi:** T8 bentuk `ModelListed` dipakai T5/T6 — bentuk itu ditetapkan di plan ini (A5), sehingga T5 bisa mulai paralel dengan T7 selama field tidak berubah.

## Open Questions

- **Q1:** `PendingResolution` — apakah `ListedRules` dikembalikan lewat method `Permit.ListedRules()` (rekomendasi A5) atau field di `ProviderPermit`. Diputuskan di T4 dengan kompilasi silang; tidak mengubah perilaku eksternal.
- **Q2:** Apakah rule-scope validation saat request memakai `routingRuleNameKey` (scope, scope_id) untuk resolve nama→rule (ambigu antar-scope)? Rekomendasi: ya — entry disimpan sebagai ID (A1), nama hanya dipakai config.json + listing, jadi ambiguitas resolve-once di config load (T12).
- **Q3:** Apakah `video_generation`/passthrough/batch bodyModels perlu di-list-gate? D8 menyebut "semua jalur inference yang membawa model name, termasuk bodyModels batch/passthrough" — gate `evaluateAccess` sudah dijalankan per body model (main.go:1229/1256), jadi T8 otomatis berlaku. Dicatat, tidak ada work ekstra di luar pastikan test menutupi satu body-model case.