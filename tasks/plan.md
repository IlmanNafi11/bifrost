# Implementation Plan: Test/Probe Model (`POST /api/models/test`)

## Overview

Menambahkan tombol **Test** per baris model di tiga permukaan UI (routing target, routing fallback, model catalog) yang memanggil endpoint server baru `POST /api/models/test`. Endpoint mem-probe provider sesuai **mode** model (dibaca dari katalog), mengukur **TTFT** untuk mode streaming dan **latency total** untuk mode unary, lalu mengembalikan verdict yang disajikan sebagai toast sonner. Probe disembunyikan dari log, biaya, dan stats lewat penanda konteks internal.

Spec: `docs/specs/SPEC-model-test-probe.md` (disetujui). Intent: `docs/intent/model-test-probe.md`.

## Architecture Decisions

| # | Decision | Rationale |
|---|---|---|
| A1 | Probe dijalankan **in-process** melalui `*bifrost.Bifrost` (`ChatCompletionStreamRequest`, `EmbeddingRequest`, …), bukan HTTP round-trip ke diri sendiri | `ProviderHandler` sudah memegang `client *bifrost.Bifrost`; jalur ini tetap melewati plugin pipeline, key selection, routing, dan provider converter — sama dengan request nyata, tanpa noise TCP lokal pada TTFT. Reference (`9router-go`) memakai dispatch ke handler sendiri karena tidak punya entry point core setara. |
| A2 | Mode dibaca dari katalog: `inMemoryStore.ModelCatalog.GetModelCapabilityEntryForModel(model, provider).Mode`, fallback `"chat"` | Sumber tunggal kebenaran; `/api/models/details` memakai jalur yang sama (`providers.go:1090`). Client tidak perlu mengirim mode. |
| A3 | Penanda `BifrostContextKeyIsProbe (bool)` + `BifrostContextKeySkipBudgetAndRateLimits (true)` di-set pada konteks probe | `IsProbe` dipakai plugin logging/semanticcache/governance untuk melewati penulisan & cache; `SkipBudgetAndRateLimits` mencegah admission menolak/mencatat probe. Keduanya key reserved yang sudah ada pola-nya. |
| A4 | Probe **drain penuh** (baca channel sampai tutup), TTFT = waktu ke delta pertama yang membawa konten/`finish_reason` | Error di tengah stream harus tertangkap; keputusan interview #4. |
| A5 | Timeout 60s sebagai konstanta paket, bukan config | Satu-satunya pengaman; tidak perlu variasi per instalasi. |
| A6 | Verdict gagal → HTTP 200 + `ok:false`; endpoint hanya 4xx untuk payload invalid dan 5xx bila probe tak bisa dijalankan sama sekali | "Provider menolak" adalah hasil pengukuran, bukan kegagalan transport endpoint. |
| A7 | `key_id` kosong → tidak men-set pin; terisi → `BifrostContextKeyAPIKeyID` di-set sebelum pemanggilan core | Pin dibaca `selectKeyFromProviderForModelWithPool` (`core/bifrost.go:11320`); `SelectedKeyID/Name` di-stamp balik oleh core (`core/bifrost.go:7492-7493`) sehingga endpoint bisa melaporkan key yang benar-benar dipakai. |
| A8 | Tombol = ikon-only (`FlaskConical`), disabled + tooltip bila `mode` tidak probeable; `mode` diekspos di `/api/models/details` | Client harus tahu mode **sebelum** klik. Routing rules tidak butuh field ini (server yang meresolusi). |

## Dependency Graph

```
T1 context key IsProbe (core/schemas)
 ├── T2 logging plugin skip (+benchmark)
 ├── T3 governance plugin skip (+benchmark)
 └── T4 semanticcache plugin skip
        └── T5 endpoint + mode resolution + chat probe (slice vertikal #1)
             ├── T6 streaming mode dispatch
             └── T7 unary mode dispatch
                   └── T8 mode di /api/models/details (+OpenAPI)
                        ├── T9 RTK mutation + helper murni
                        ├── T10 komponen tombol bersama
                        │    ├── T11 wiring routing (target+fallback)
                        │    └── T13 wiring catalog
                        ├── T12 i18n (semua locale)
                        └── T14 harness case
                             └── T15 E2E (routing + catalog)
T15/T11 → T16 docs (routing.mdx)
```

Build order: T1 → T2/T3/T4 (paralel) → T5 → T6/T7 (paralel) → T8 → T9/T10 (paralel) → T11/T12/T13 → T14 → T15 → T16.

## Task List

### Phase 1: Foundation

- [x] **Task 1: Context key `IsProbe`**
  - Description: tambah `BifrostContextKeyIsProbe` (`bool`) ke `core/schemas/bifrost.go` dengan komentar "set by the probe endpoint - DO NOT SET MANUALLY", daftarkan di `reservedKeys` (`core/schemas/context.go`), dan sediakan helper baca (`IsModelProbe(ctx)` di `core/utils.go` atau akses langsung `GetBoolFromContext`).
  - Acceptance: key terdaftar reserved; helper mengembalikan `false` untuk konteks tanpa flag; `true` setelah `SetValue`.
  - Verify: `cd core && GOWORK=off go test ./schemas/ -run 'TestContext|Reserved'`; `go build ./...`.
  - Deps: None. Files: `core/schemas/bifrost.go`, `core/schemas/context.go`, `core/utils.go`, `core/schemas/context_test.go` (existing).
  - Scope: S (4).

- [x] **Task 2: Logging plugin menghormati `IsProbe`**
  - Description: early return di awal `PreLLMHook` (tanpa pending entry, tanpa `notifyLogCallbacks`) dan `PostLLMHook` (tanpa write, termasuk cabang `!hasPending` yang menulis minimal error entry) saat `IsProbe`. Tambah `Benchmark*` di file benchmark yang ada.
  - Acceptance: probe tidak membuat entri log/pending; request normal tidak berubah; benchmark `-benchmem` tercatat di PR body.
  - Verify: `cd plugins/logging && go test ./... -run 'Probe|PostLLMHook'`; `go test -bench BenchmarkLLM -benchmem ./...`.
  - Deps: T1. Files: `plugins/logging/main.go`, `plugins/logging/operations_test.go` (atau file test hook yang sudah ada).
  - Scope: S (2).

- [x] **Task 3: Governance plugin menghormati `IsProbe`**
  - Description: early return di `PostLLMHook` sebelum blok koleksi limit & `tracker.UpdateUsage` (tidak ada cost/budget/rate-limit/request count). Pre-hook tetap berjalan agar akses & key selection diputuskan seperti biasa. Tambah benchmark.
  - Acceptance: probe tidak menaikkan budget/rate-limit/token; akses model tetap divalidasi; benchmark tercatat.
  - Verify: `cd plugins/governance && go test ./... -run 'Accounting|Probe'`; `go test -bench . -benchmem ./... -run '^$'`.
  - Deps: T1. Files: `plugins/governance/main.go`, `plugins/governance/accounting_test.go`.
  - Scope: S (2).

- [x] **Task 4: Semantic cache plugin menghormati `IsProbe`**
  - Description: early return di `PreLLMHook` (tanpa lookup/short-circuit) dan `PostLLMHook` (tanpa cache write) saat `IsProbe`, sehingga verdict tidak pernah berasal dari cache.
  - Acceptance: probe tidak membaca cache (stub store tidak dipanggil) dan tidak menulis cache.
  - Verify: `cd plugins/semanticcache && go test ./... -run 'Probe|Core'`.
  - Deps: T1. Files: `plugins/semanticcache/main.go`, `plugins/semanticcache/plugin_core_test.go`.
  - Scope: S (2).

### Checkpoint: Foundation
- [x] `cd core && go build ./...` dan ketiga plugin kompilasi + test hijau
- [ ] Review dengan human sebelum endpoint ditulis

### Phase 2: Endpoint (slice vertikal)

- [ ] **Task 5: `POST /api/models/test` — route, contract, chat probe**
  - Description: route baru di `ProviderHandler.RegisterRoutes`; tipe request/response; validasi (400); resolusi mode (A2); runner probe chat streaming (`max_tokens:16`, prompt `"hi"`, `stream`), TTFT dari delta pertama, drain penuh, timeout 60s, pin `key_id`, baca `SelectedKeyID/Name`; set `IsProbe` + `SkipBudgetAndRateLimits`. Response `{ok, mode, ttft_ms?, total_ms, status?, key_id?, key_name?, note?, error?}`.
  - Acceptance: chat probe sukses → `ok:true` + `ttft_ms` + `total_ms`; provider error → `ok:false` + error; timeout → `ok:false` + `timeout setelah 60s`; body tanpa model/provider → 400.
  - Verify: `make test` (transports) atau `cd transports/bifrost-http && go test ./handlers/ -run TestModelProbe`.
  - Deps: T1-T4. Files: `transports/bifrost-http/handlers/providers.go`, `transports/bifrost-http/handlers/providers_test.go`.
  - Scope: M (2).

- [ ] **Task 6: Streaming mode dispatch**
  - Description: jalur probe untuk `responses`, `completion`, `audio_speech`, `audio_transcription`, `image_generation`, `image_edit` — payload minimal per mode, TTFT + `total_ms`, drain penuh. Mode tak dikenal/tak didukung → 400 `mode not supported for probe`.
  - Acceptance: tiap mode memanggil method core yang benar (dispatcher teruji dengan stub); mode unary tidak pernah diberi `ttft_ms`.
  - Verify: `cd transports/bifrost-http && go test ./handlers/ -run TestModelProbeModeDispatch`.
  - Deps: T5. Files: `transports/bifrost-http/handlers/providers.go` (atau file probe terpisah), `providers_test.go`.
  - Scope: M (2).

- [ ] **Task 7: Unary mode dispatch**
  - Description: `embedding`, `rerank`, `decisions`, `ocr`, `video_generation` → satu panggilan core, `total_ms` saja.
  - Acceptance: tiap mode memanggil method core yang benar; response tanpa `ttft_ms`; `video_generation` melaporkan pembuatan job sebagai sukses.
  - Verify: `cd transports/bifrost-http && go test ./handlers/ -run TestModelProbeModeDispatch`.
  - Deps: T5. Files: `transports/bifrost-http/handlers/providers.go`, `providers_test.go`.
  - Scope: M (2).

### Checkpoint: Endpoint
- [ ] Semua mode terpetakan; `go test ./handlers/` hijau
- [ ] Manual: `curl -X POST localhost:<port>/api/models/test -d '{"provider":"openai","model":"gpt-4o-mini"}'` mengembalikan verdict; **tidak ada baris baru** di Logs dan `total_requests` tidak bertambah

### Phase 3: Data exposure + UI

- [ ] **Task 8: `mode` pada `/api/models/details`**
  - Description: `ModelDetailsResponse.Mode string \`json:"mode,omitempty"\`` diisi dari capabilities (fallback `chat`); update OpenAPI (`paths/management/providers.yaml` + `schemas/management/providers.yaml`), `ModelDetails.mode?` di `ui/lib/store/apis/providersApi.ts`.
  - Acceptance: respons memuat `mode`; baris tanpa datasheet → `chat`; OpenAPI spec memuat field baru; `python docs/openapi/bundle.py` tetap jalan.
  - Verify: `cd transports/bifrost-http && go test ./handlers/ -run TestListModelDetails`; `cd ui && npm run typecheck`.
  - Deps: T5. Files: `handlers/providers.go`, `handlers/providers_test.go`, `docs/openapi/paths/management/providers.yaml`, `docs/openapi/schemas/management/providers.yaml`, `ui/lib/store/apis/providersApi.ts` (5).
  - Scope: M (5).

- [ ] **Task 9: RTK mutation + helper murni**
  - Description: `testModel` mutation (`POST /models/test`, body `{provider, model, key_id?}`) + tipe `ModelTestResponse`; helper murni: `isModeProbeable(mode)`, `formatModelProbeToast(result)` → `{title, description, variant}`.
  - Acceptance: helper memetakan streaming→`TTFT … · total …`, unary→`Latency …`, gagal→pesan error; `key auto` bila `key_name` kosong; mode non-probeable terdeteksi.
  - Verify: `cd ui && npx vitest run lib/utils/modelProbe.test.ts`.
  - Deps: T5/T8 (shape). Files: `ui/lib/store/apis/providersApi.ts`, `ui/lib/types/providers.ts` (atau lokasi tipe yang sudah ada), `ui/lib/utils/modelProbe.ts`, `ui/lib/utils/modelProbe.test.ts` (4).
  - Scope: S (4).

- [ ] **Task 10: Komponen tombol Test bersama**
  - Description: komponen/hook yang merender tombol ikon-only (`FlaskConical`) dengan spinner + `disabled` saat probe berjalan, `aria-label`/tooltip, dan memanggil mutation; satu instance per baris dengan state per-bar (bukan global) sehingga multi-probe jalan.
  - Acceptance: klik → disabled+spinner baris itu; baris lain tetap aktif; toast muncul saat selesai; `data-testid` bisa dikonfigurasi lewat prop.
  - Verify: `cd ui && npx vitest run components/modelTest/modelTestButton.test.tsx`; `npm run typecheck`.
  - Deps: T9. Files: `ui/components/modelTest/modelTestButton.tsx`, `ui/components/modelTest/modelTestButton.test.tsx` (2).
  - Scope: S (2).

- [ ] **Task 11: Wiring routing rules (target + fallback)**
  - Description: tombol di `TargetRow` sebelum `X` dan di `FallbackRow` sebelum Trash; kirim `{provider, model, key_id}` dari state baris; disabled bila provider/model kosong.
  - Acceptance: urutan kontrol sesuai spec; tombol tidak memulai drag fallback; klik mengirim key yang dipilih baris itu.
  - Verify: `cd ui && npx vitest run app/workspace/routing-rules/views/routingFallbacksField.test.tsx`; `npm run typecheck`.
  - Deps: T10. Files: `ui/app/workspace/routing-rules/views/routingRuleSheet.tsx`, `ui/app/workspace/routing-rules/views/routingFallbacksField.tsx`, `ui/app/workspace/routing-rules/views/routingFallbacksField.test.tsx` (3).
  - Scope: S (3).

- [ ] **Task 12: i18n keys di semua locale**
  - Description: key `modelTest.*` (title/description/tooltip/aria/`keyAuto`/`modeUnsupported`/`timeout`) dan `routing.testModel`, ditambahkan ke `en` + 11 locale lain dengan terjemahan nyata (placeholder identik).
  - Acceptance: `npm run i18n:check` exit 0; tidak ada string hardcoded baru.
  - Verify: `cd ui && npm run i18n:check`.
  - Deps: T10 (nama key final). Files: `ui/locales/*/models.json` (12 berkas, mekanis satu konsern).
  - Scope: M (12, mekanis).

- [ ] **Task 13: Wiring model catalog + gating mode**
  - Description: tombol Test di sel aksi sebelum Edit; `disabled` + tooltip untuk mode non-probeable; gerbang RBAC `ModelProvider/Update` sama dengan Edit.
  - Acceptance: tombol tampil `test|edit`; disabled untuk mode tak didukung; user tanpa `Update` melihat disabled; probe memakai auto-select key.
  - Verify: `cd ui && npx vitest run app/workspace/model-catalog`; `npm run typecheck`; `npm run build`.
  - Deps: T8, T10. Files: `ui/app/workspace/model-catalog/views/attributesTab.tsx`, `.../attributesTab.test.tsx` (baru bila belum ada).
  - Scope: S (2).

### Checkpoint: UI
- [ ] `npm run typecheck`, `npm run lint`, `npm run i18n:check`, `npm run build` hijau
- [ ] Manual di `make dev`: tiga permukaan menampilkan tombol pada posisi yang benar; dua baris berbeda diprobe bersamaan dan toast muncul sesuai urutan selesai

### Phase 4: Verification & Docs

- [ ] **Task 14: Harness case (`model probe`)**
  - Description: case baru di `tests/e2e/api/collections/provider-harness.json` (provider live) yang memanggil `POST /api/models/test` untuk satu model chat, meng-assert status 200, `ok:true`, `ttft_ms > 0`, `mode == "chat"`, dan bahwa baris tidak muncul di log stats. Insersi surgical (splice), bukan reserialize.
  - Acceptance: augment + filter script lulus; case red sebelum perubahan, green sesudah (validasi struktural).
  - Verify: `node tests/e2e/api/runners/augment-provider-harness.mjs --source tests/e2e/api/collections/provider-harness.json --out tmp/harness-augmented.json` lalu `filter-collection.mjs --feature "model probe"`.
  - Deps: T5, T6. Files: `tests/e2e/api/collections/provider-harness.json`, `tmp/` (artefak).
  - Scope: S (1).

- [ ] **Task 15: E2E Playwright (routing + catalog)**
  - Description: spec yang memverifikasi posisi/testid tombol, disabled saat model kosong, dan toast pada klik; tambah page-object accessor di `routing-rules.page.ts`.
  - Acceptance: `make run-e2e FLOW=routing-rules` dan `FLOW=model-catalog` hijau.
  - Deps: T11, T13. Files: `tests/e2e/features/routing-rules/pages/routing-rules.page.ts`, `tests/e2e/features/routing-rules/routing-rules.spec.ts`, `tests/e2e/features/model-catalog/model-catalog.spec.ts` (3).
  - Scope: M (3).

- [ ] **Task 16: Docs**
  - Description: tambahkan paragraf tombol Test + arti TTFT/Latency di `docs/features/governance/routing.mdx`; sebutkan probe tidak tercatat di logs/biaya.
  - Acceptance: contoh konsisten dengan perilaku; tidak ada contoh config baru (tidak ada config).
  - Verify: review manual + `docs/openapi/spec_invariants_test.py` bila menyentuh OpenAPI.
  - Deps: T11, T13. Files: `docs/features/governance/routing.mdx` (1).
  - Scope: XS (1).

### Checkpoint: Complete
- [ ] Semua Success Criteria di spec terpenuhi
- [ ] `make test`, `make test-plugins`, `make test-governance`, UI checks hijau
- [ ] Siap review

## Risks and Mitigations

| Risk | Impact | Mitigation |
|---|---|---|
| Probe bocor ke log/stats biaya karena satu jalur plugin terlewat (mis. cabang `!hasPending` di logging) | Med | Unit test per plugin yang meng-assert tidak ada write; manual check di checkpoint endpoint (curl → Logs kosong). |
| Payload non-chat tidak valid untuk sebagian provider (image size, WAV, OCR document) → false negative | Med | Probe dianggap sukses bila provider menerima dan menjawab; payload diverifikasi per mode terhadap provider nyata saat implementasi (open question O4 di spec). |
| Probe mahal/lama pada model reasoning (drain penuh, `max_tokens` kecil tapi chain-of-thought panjang) | Med | `max_tokens:16` + timeout 60s; `finish_reason=length` reasoning-only dianggap sukses dengan `note` (mengikuti alasan reference issue #3010). |
| `SelectedKeyID` kosong saat governance tidak aktif → toast tanpa key | Low | Fallback teks `key auto` (sudah di spec). |
| Penanda `IsProbe` tidak terbaca karena `BlockRestrictedWrites` (key reserved) | Low | Di-set pada konteks milik handler sebelum pemanggilan core (setara `SetFallbackPinnedAPIKeyID`); key didaftarkan reserved agar konsisten. |
| Mode ganda per `(provider, model)` (chat vs responses) di katalog | Low | Baris listing unik per (provider, model); mode diambil dari entry yang sama dengan yang dipakai `listModelDetails`. |

## Parallelization Opportunities

- **Aman paralel:** T2/T3/T4 (plugin berbeda, modul `go.mod` berbeda); T6/T7 setelah T5; T9/T10 setelah shape response; T12 (i18n) setelah nama key final.
- **Harus sekuensial:** T1 (semua plugin membaca key), T5 (kontrak endpoint), T8 (field response).
- **Perlu koordinasi:** T5 menentukan bentuk `ModelTestResponse` yang dipakai T9/T10 — bentuk itu ditetapkan di plan ini, jadi T9 bisa dimulai paralel dengan T6/T7 selama field tidak berubah.

## Open Questions

- **Q1 (dari spec O4):** payload per mode non-chat per provider diverifikasi saat implementasi; bila satu mode terbukti tidak punya payload universal, mode itu ditandai tidak probeable (`isModeProbeable` di UI + 400 di server) dan dicatat di PR body. Tidak memblokir task lain.
- **Q2:** apakah `image_edit`/`image_variation` perlu disertakan sekarang? Keduanya butuh input gambar; bila payload universal tidak ada, ikuti Q1.
- **Q3:** apakah benchmark plugin (T2/T3) wajib dijalankan sebelum-sesudah pada commit terpisah agar angka `-benchmem` di PR body bisa dibandingkan? Rekomendasi: ya, jalankan sekali di commit `main` sebelum perubahan, sekali sesudah.
