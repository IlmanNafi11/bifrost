# Spec: VK-level `listed_models` — kontrol daftar `/models` + pengecualian routing rule

> Traces to: `docs/intent/vk-listed-models.md` (terkonfirmasi, explicit yes).
> Module id: `vk-listed-models`.

## Objective

Virtual key saat ini tidak punya kontrol atas isi `/models` terpisah dari akses:
`allowed_models: ["*"]` membocorkan seluruh katalog provider ke pemegang VK, dan
nama routing rule tidak pernah muncul di listing. Fitur ini menambahkan satu blok
konfigurasi VK-level bernama `listed_models` yang menjadi **satu-satunya sumber isi
`/models`** untuk VK tersebut **dan** batas model yang boleh dipanggil (di luar
daftar → 403), dengan pengecualian: nama routing rule yang terdaftar boleh dipanggil
dan menembus ke seluruh target+fallback rule-nya.

**User:** Admin VK (mengonfigurasi) dan pemegang VK (melihat permukaan yang
diizinkan, tidak boleh melihat detail provider/model di baliknya).

**Success (contoh nyata dari intent):** VK A dengan
`listed_models = [{B, gpt-5.4}, {C, GLM-5.3}, rule "coding"}]`, sementara VK A
punya `allowed_models: ["*"]` untuk provider B, C, D:
- `GET /v1/models` (dengan VK A) → `id` = `B/gpt-5.4`, `C/GLM-5.3`, `coding` saja.
- Panggil `gpt-5.4`/`GLM-5.3` → jalan; panggil model lain mana pun → 403.
- Panggil `coding` → jalan sampai target/fallback rule (`deepseek-v4-flash`,
  `glm-5.3-flash`, `kimi-k3`, dst.) meski model-model itu tidak ada di
  `allowed_models` maupun `listed_models`; provider-gate tetap berlaku.

## Keputusan dari interview (mengikat)

| # | Keputusan |
|---|---|
| D1 | Satu blok VK-level baru `listed_models`, **terpisah** dari `Provider Configurations`. |
| D2 | Entri: `{provider, model}` (untuk model dari provider ber-prefix) **atau** nama routing rule. |
| D3 | Lapisan kedua yang lebih sempit di atas `allowed_models`: entri model wajib lolos `allowed_models` dulu; picker UI hanya menawarkan model yang lolos `allowed_models` + rule yang scope chain-nya cocok dengan VK (termasuk global). Validasi yang sama ditegakkan di server (API langsung). |
| D4 | Pengecualian rule: hanya rule yang **terdaftar di `listed_models`** yang membuka akses; model final (hasil rewrite) divalidasi terhadap **definisi rule** (target + fallback), bukan bypass total. |
| D5 | Gate **provider tetap berlaku** untuk target/fallback rule (VK harus punya `provider_config` untuk provider yang dipakai rule). |
| D6 | Rule harus masih **aktif dan scope-nya cocok** saat request; kalau tidak → fail closed (403; nama hilang dari `/models`). |
| D7 | `nil`/absen = fitur tidak aktif (perilaku hari ini). `[]` eksplisit = tolak semua (deny-by-default, konsisten `allowed_models`/`key_ids`). |
| D8 | Ditegakkan di **semua jalur inference yang membawa model name** (chat, responses, embeddings, images, speech, batch, passthrough, termasuk bodyModels batch/passthrough). |
| D9 | `/api/models` & `/api/models/details` **tidak** dipersempit `listed_models` (tetap katalog penuh; hanya narrowing akses yang sudah ada hari ini). |
| D10 | Format `id` di `/models`: entri model **tetap ber-prefix** `provider/model`; entri rule **polos** (`coding`, tanpa prefix). Tidak ada display-name baru di response. |
| D11 | Budget & rate-limit **tetap ditagih penuh** untuk model di balik rule (termasuk model-scoped limit; dikumpulkan lewat provider/model final). |
| D12 | Gate diperiksa terhadap **nama yang diminta caller** (`BifrostContextKeyRequestedModel`, pra-rewrite), bukan model final; model final hanya dibandingkan dengan definisi rule / `allowed_models`. |

## Tech Stack

- Go workspace multi-module (Go 1.27.3): framework, transports, plugins/governance, plugins/routing
- `framework/configstore` + GORM (SQLite/Postgres), `tables.TableVirtualKey`
- `core/schemas.Permit` / `ProviderPermit` + implementasi `framework/grant`
- `plugins/governance` (resolver, store, PreLLMHook/PostLLMHook) — pemilik gate
- `plugins/routing` (engine, rules.Store) — pemilik definisi rule
- React + TypeScript UI (`ui/app/workspace/virtual-keys/views/virtualKeySheet.tsx`),
  react-hook-form + zod, i18next, TanStack
- `transports/config.schema.json` — source of truth config; Vitest untuk UI

## Commands

```bash
# Backend — governance (gate + store)
make test-governance                       # semua test governance
make test-governance TESTCASE=TestBudgetResolver_EvaluateRequest_ModelBlocked
make test-governance TESTCASE=TestFilterModelsForAccess
make test-framework                        # configstore (persistence, hash, migration)
make test                                  # HTTP transport handlers (VK CRUD, /models)
make test-plugins                          # semua plugin (routing, governance)
cd plugins/governance && GOWORK=off go test ./... -run ListedModels
make build                                 # build bifrost-http

# UI
cd ui && npm run typecheck
cd ui && npx vitest run
cd ui && npm run lint
cd ui && npm run format:changed:check
cd ui && npm run i18n:check

# Harness (wire-visible — wajib, lihat Testing Strategy)
node tests/e2e/api/runners/augment-provider-harness.mjs --source tests/e2e/api/collections/provider-harness.json --out tmp/harness-augmented.json
node tests/e2e/api/runners/filter-collection.mjs --source tmp/harness-augmented.json --out tmp/filtered.json --feature "listed models"
```

## Project Structure

File yang **di-modify** (indikatif; slicing di phase Tasks):

```
framework/configstore/tables/virtualkey.go   # + kolom `listed_models` (JSON), Marshal/Unmarshal
framework/configstore/store.go               # ConfigStore interface: baca/tulis ListedModels VK
framework/configstore/rdb.go                 # persistence + preload
framework/configstore/migrations.go          # migration add vk listed_models column
framework/configstore/clientconfig.go        # GenerateVirtualKeyHash: sertakan listed_models
core/schemas/grant.go                        # Permit/ProviderPermit + ListedModels (+ ListedRules)
framework/grant/permit.go, access.go         # implementasi listed + rule-def lookup
plugins/governance/store.go                  # permitForVirtualKey: bangun listed dari VK
plugins/governance/resolver.go               # evaluateAccess: gate requested-name vs listed
plugins/governance/main.go                   # PostLLMHook listing + wiring rule store
plugins/governance/utils.go                  # filterModelsForAccess: listed-aware + entri rule
plugins/governance/routingrulestore.go       # (baru) cache definisi rule dari ConfigStore
transports/bifrost-http/handlers/governance.go # VK create/update: validasi listed_models
transports/config.schema.json                # virtual_keys.listed_models di schema
tests/e2e/api/collections/provider-harness.json # case harness (wire-visible)
docs/deployment-guides/config-json/governance.mdx # dokumentasi config
docs/specs/SPEC-vk-listed-models.md          # spec ini (living)
```

File yang **di-modify (UI)**:

```
ui/lib/types/governance.ts                  # + ListedModelsEntry pada VirtualKey
ui/app/workspace/virtual-keys/views/virtualKeySheet.tsx # blok "Allowed listed models"
ui/components/ui/listedModelsEditor.tsx     # (baru) editor + picker (opsi dari allowed+rules)
ui/locales/en/governance.json + mirror      # key i18n baru (Provider label, tooltip, validation)
ui/lib/types/schemas.ts                     # zod schema VK + validator listed
```

File **baru (test)**:

```
plugins/governance/listing_test.go           # (jika test file belum ada) gate + filter listing
plugins/governance/routingrulestore_test.go  # cache definisi rule
ui/app/workspace/virtual-keys/views/virtualKeySheet.listedModels.test.ts # pure picker/validation logic
```

Aturan konvensi repo: Go filenames tanpa underscore (kecuali `_test.go`);
test dimasukkan ke file `_test.go` yang sudah ada untuk package yang sama bila ada
(hanya bikin file test baru bila belum ada file untuk source yang bersangkutan).

## Code Style

Escontoh pola yang diikuti — permit membawa `ListedModels` seperti ia membawa
`AllowedModels` (framework/grant), sehingga gate dibaca dari objek akses yang sama:

```go
// core/schemas/grant.go (ProviderPermit)
type ProviderPermit struct {
	Provider          string    // provider name, as configured
	AllowedModels     WhiteList // ["*"] allows all; empty denies all
	BlacklistedModels BlackList // wins over AllowedModels
	ListedModels      WhiteList // nil=fitur nonaktif; [] denies all; subset of AllowedModels
	KeyIDs            WhiteList
	Weight *float64
}
```

Gate: diperiksa terhadap nama yang diminta caller, bukan model final:

```go
// plugins/governance/resolver.go (di dalam evaluateAccess)
requested := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyRequestedModel)
if requested != "" && !access.ModelListed(string(provider), requested) {
	if ruleAllowed, _ := r.ruleSets.AllowsRequest(ctx, requested, provider, model); !ruleAllowed {
		return &EvaluationResult{Decision: DecisionModelBlocked,
			Reason: denialReason(fmt.Sprintf("Model '%s' is not listed for this virtual key", requested),
				access.DeniedPermitsForModel(string(provider), requested))}
	}
}
```

Naming: field `ListedModels`, entries `provider`/`model`/`routing_rule` (snake_case
di JSON); UI label `listed_models` / "Allowed listed models"; testid
`vk-listed-models-add`, `vk-listed-models-remove-${i}`. i18n key di namespace
`governance` (`listedModels.*`). Kode memakai tab indent, `sonic` untuk marshaling
fields tunggal sesuai gotcha #13, `protojson` dihindari.

Titik terpenting yang **harus** tampil di kode (dari intent D12): gate membaca
`BifrostContextKeyRequestedModel` (nama yang dikirim caller sebelum routing
rewrite) — bukan `req.Model` post-rewrite. Ini perbedaan perilaku yang membuat
`coding` bisa memanggil model di baliknya tanpa mengekspos nama-namanya ke gate.

## Testing Strategy

- **Framework Go:** `go test` via `make test-governance` / `make test-framework` /
  `make test`. UI: Vitest 4.1.12 (config `ui/vitest.config.ts`, globals).
- **Level:**
  - *Unit (gate, tanpa HTTP):* `evaluateAccess` — VK tanpa `listed_models` (nil)
    tetap perilaku lama; `[]` → tidak ada model yang bolehdipanggil selain rule yang
    terdaftar; entri model di luar `allowed_models` ditolak; requested-name vs
    model-final (rule rewrite) diverifikasi; provider-gate tetap berlaku untuk
    target/fallback rule.
  - *Unit (permit/store):* `permitForVirtualKey` membangun `ListedModels` +
    `ListedRules` dari `TableVirtualKey`; `GenerateVirtualKeyHash` berubah saat
    `listed_models` berubah (test hash stable vs drift).
  - *Unit (listing):* `filterModelsForAccess` menghasilkan tepat entri `listed_models`
    + entri rule (polos, tanpa prefix); rule nonaktif/scopetidak-cocok di-drop.
  - *Handler tests:* VK create/update menerima `listed_models` valid, menolak entri
    model yang tidak lolos `allowed_models` dan rule yang tidak ada/tidak cocok
    scope (400); `GET /api/models` (dashboard) tetap penuh (D9).
  - *Rule cache:* `routingrulestore` invalidation — rule diedit/dihapus → cache
    invalidated sebelum request berikutnya (fail closed).
  - *Harness (wajib, wire-visible):* case baru di `provider-harness.json` —
    `/v1/models` dengan VK ber-`listed_models` mengembalikan persis entri yang
    didaftarkan (D10), panggilan model non-listed → 403, `coding` → rule jalan.
    Harness case harus **red sebelum change, green setelah** — divalidasi
    struktural via `augment-provider-harness.mjs` + `filter-collection.mjs`,
    tidak perlu live paid run.
  - *UI:* pure helper picker/validation logic diuji Vitest; i18n check wajib.
- **Coverage expectation:** gate & rule-exception logic (D3–D8, D11, D12) di-cover
  unit test; jenis perubahan hanya komentar/log → nyatakan eksplisit di PR.

## Boundaries

- **Always do:**
  - Gate memakai `BifrostContextKeyRequestedModel`, bukan model post-rewrite.
  - Semua validation: absen vs `[]` dibedakan (D7); model entry wajib ⊆
    `allowed_models`; rule entry wajib ada & scope cocok (D3, D6).
  - Provider gate tetap untuk target/fallback rule (D5); budget/rate-limit penuh (D11).
  - Setiap string UI baru lewat i18n + `npm run i18n:check` exit 0.
  - Setiap file Go diformat (`gofmt`/`goimports`), UI via oxfmt `format:changed`.
  - `make test-governance`, `npm run typecheck` + `npm run lint` jalan sebelum selesai.
  - Perubahan request-path menyertakan cost statement (AGENTS.md): fitur off saat
    `ListedModels == nil` — biaya = satu map lookup, nol alloc untuk VK tanpa blok.

- **Ask first:**
  - Perubahan schema DB (migration baru) — wajib review.
  - Menambah dependensi (target: nol dependensi baru).
  - Mengubah kontrak `schemas.Permit`/`Access` interface (dipakai grant, mocks,
    enterprise).
  - Mengubah urutan plugin / hook pipeline di core.
  - Menyentuh `plugins/routing` engine atau definisi `Decision`/fallback semantics.
  - Perubahan i18n locale di luar `en` (harus mirror semua locale yang ada).

- **Never do:**
  - Mengubah arti `allowed_models`/`key_ids` (D9, keluar scope).
  - Menambahkan display-name/label baru ke response `/models` (OpenAI-compatible).
  - Memperbaiki `id` prefixing untuk entri provider (harus tetap `provider/model`).
  - Bypass total gate model untuk rule (D4) — pengecualian harus ke definisi rule.
  - Menyimpan/mengembalikan `listed_models` tanpa memasukkannya ke `GenerateVirtualKeyHash`
    (config.json sync akan jadi drift permanen).
  - Meng-cache definisi rule lebih lama dari invalidation rule (escalation path).

## Success Criteria

Testable, terverifikasi via unit + harness + UI check:

1. VK tanpa `listed_models` (nil): `/v1/models` dan semua gate berperilaku persis
   hari ini (D7) — regression test mengunci ini.
2. VK dengan `listed_models = [{B, gpt-5.4}, {C, GLM-5.3}, {rule: coding}]` &
   `allowed_models:["*"]` di B/C/D: `/v1/models` = `B/gpt-5.4`, `C/GLM-5.3`,
   `coding` (order/prefix sesuai D10); `gpt-4o` (provider B, tidak listed) → 403.
3. Panggil `coding`: request diterima; routing me-rewrite ke target/fallback;
   request sukses meski model target tidak ada di `allowed_models`/`listed_models`;
   provider yang tidak punya `provider_config` di VK A → 403 (D5).
4. Rule `coding` dinonaktifkan/di-scope ke VK lain/dihapus: (a) panggil `coding` →
   403, (b) `coding` tidak muncul di `/v1/models` (D6) — baru green setelah
   invalidation cache.
5. `listed_models: []` → `/v1/models` kosong, semua model non-rule → 403 (D7).
6. `GET /api/models` dan `/api/models/details` (dashboard, tanpa VK) tetap menampilkan
   katalog penuh — tidak dipersempit `listed_models` (D9); dengan VK tetap narrowing
   akses lama.
7. VK create/update dengan `listed_models` berisi model yang tidak lolos
   `allowed_models`/tidak dikenal, atau rule yang tidak ada/tidak cocok scope →
   400, state tidak berubah (D3).
8. Entri `listel_models` ikut `GenerateVirtualKeyHash` → restart tidak me-reset
   config (framework test drift).
9. Harness case (`FEATURE="listed models"`) green; validasi struktural lulus.
10. UI: blok "Allowed listed models" tampil di VK sheet; picker hanya menawarkan
    model dari `allowed_models` + rule dengan scope cocok; i18n check exit 0.
11. VK create/update dengan >100 entri `listed_models` → 400 (cap entri).
12. `listed_models` menyimpan ID rule; config.json menerima nama rule yang
    di-resolve ke ID saat load (ambigu → ditolak); `/models` menampilkan nama
    rule (bukan ID).

## Keputusan Open Questions (dikonfirmasi)

1. **Cache definisi rule di governance** — cache dalam governance plugin dengan
   invalidation via callback yang sudah ada: `ReloadRoutingRule` dan
   `RemoveRoutingRule` (server memanggil keduanya — governance dan routing).
   Gate rule-exception membaca dari cache ini; cache divalidasi sinkron dengan
   config (fail closed saat rule hilang/nonaktif/scope berubah).
2. **Entri rule disimpan sebagai ID rule** di `listed_models` (tahan rename,
   tidak ambigu antar-scope). `config.json` menerima **nama** rule;
   resolusi nama→ID terjadi saat load, ambigu → ditolak (fail closed). Listing
   menampilkan nama rule (dari ID) karena `id` di `/models` untuk entri rule
   adalah nama polos (D10).
3. **Cap entri**: maksimal 100 entri per VK (gabungan model + rule); melebihi →
   400 saat create/update.
4. **Enterprise parity**: access profile di luar scope repo OSS ini; VK-level
   dulu. (Dokumen di sini sebagai catatan untuk implementasi enterprise
   berikutnya.)

---

Setelah approve, phase PLAN → TASKS → IMPLEMENT berjalan per-module dengan
dependency: configstore/persistence → schemas/grant → governance gate →
listing → handler validasi → config schema → UI → harness.
