# Todo: Test/Probe Model (`POST /api/models/test`)

Spec: `docs/specs/SPEC-model-test-probe.md` · Plan: `tasks/plan.md`

## Phase 1: Foundation

- [x] **T1 — Context key `IsProbe`**
  - Acceptance: `BifrostContextKeyIsProbe` ada di `core/schemas/bifrost.go`, terdaftar di `reservedKeys`, helper baca mengembalikan false tanpa set / true sesudahnya.
  - Verify: `cd core && GOWORK=off go test ./schemas/ -run 'TestContext'`; `go build ./...`
  - Files: `core/schemas/bifrost.go`, `core/schemas/context.go`, `core/utils.go`, `core/schemas/context_test.go`
  - Deps: none · Size: S

- [x] **T2 — Logging plugin skip probe**
  - Acceptance: probe tidak membuat pending entry, tidak menulis log, termasuk cabang `!hasPending`; request normal tidak berubah; benchmark `-benchmem` tercatat.
  - Verify: `cd plugins/logging && go test ./... -run 'Probe|PostLLMHook'`; benchmark.
  - Files: `plugins/logging/main.go`, `plugins/logging/operations_test.go`
  - Deps: T1 · Size: S

- [x] **T3 — Governance plugin skip probe**
  - Acceptance: tidak ada cost/budget/rate-limit/request-count untuk probe; akses & key selection tetap berjalan; benchmark tercatat.
  - Verify: `cd plugins/governance && go test ./... -run 'Accounting|Probe'`; benchmark.
  - Files: `plugins/governance/main.go`, `plugins/governance/accounting_test.go`
  - Deps: T1 · Size: S

- [x] **T4 — Semantic cache skip probe**
  - Acceptance: probe tidak lookup cache (tidak short-circuit) dan tidak menulis cache.
  - Verify: `cd plugins/semanticcache && go test ./... -run 'Probe|Core'`
  - Files: `plugins/semanticcache/main.go`, `plugins/semanticcache/plugin_core_test.go`
  - Deps: T1 · Size: S

### Checkpoint: Foundation
- [x] Build core + 3 modul plugin hijau
- [x] Review human — endpoint ditulis, harness case pin hasil + log tersembunyi

## Phase 2: Endpoint

- [x] **T5 — `POST /api/models/test`: route + chat probe**
  - Acceptance: sukses → `ok:true` + `ttft_ms` + `total_ms`; error provider → `ok:false` + pesan; timeout → `ok:false` `timeout setelah 60s`; tanpa model/provider → 400; `IsProbe` + `SkipBudgetAndRateLimits` di-set.
  - Verify: `cd transports/bifrost-http && go test ./handlers/ -run TestModelProbe`
  - Files: `transports/bifrost-http/handlers/providers.go`, `transports/bifrost-http/handlers/providers_test.go`
  - Deps: T1–T4 · Size: M

- [x] **T6 — Streaming mode dispatch**
  - Acceptance: responses/completion/audio_speech/audio_transcription/image_generation/image_edit memanggil method core benar; mode tak didukung → 400; unary tidak pernah dapat `ttft_ms`.
  - Verify: `go test ./handlers/ -run TestModelProbeModeDispatch`
  - Files: `handlers/providers.go` (+ file probe bila dipisah), `handlers/providers_test.go`
  - Deps: T5 · Size: M

- [x] **T7 — Unary mode dispatch**
  - Acceptance: embedding/rerank/decisions/ocr/video_generation → satu panggilan, `total_ms` saja.
  - Verify: `go test ./handlers/ -run TestModelProbeModeDispatch`
  - Files: `handlers/providers.go`, `handlers/providers_test.go`
  - Deps: T5 · Size: M

### Checkpoint: Endpoint
- [x] `go test ./handlers/` hijau
- [ ] Manual: curl probe → verdict tampil, **tidak ada baris Logs baru**, `total_requests` tidak bertambah

## Phase 3: Data exposure + UI

- [x] **T8 — `mode` di `/api/models/details`**
  - Acceptance: field `mode` terisi (fallback `chat`); OpenAPI paths+schemas diperbarui; bundle script tetap jalan.
  - Verify: `go test ./handlers/ -run TestListModelDetails`; `cd ui && npm run typecheck`
  - Files: `handlers/providers.go`, `handlers/providers_test.go`, `docs/openapi/paths/management/providers.yaml`, `docs/openapi/schemas/management/providers.yaml`, `ui/lib/store/apis/providersApi.ts`
  - Deps: T5 · Size: M

- [x] **T9 — RTK mutation + helper murni**
  - Acceptance: `testModel` mutation; `isModeProbeable`; `formatModelProbeToast` (TTFT vs Latency, `key auto`, error).
  - Verify: `cd ui && npx vitest run lib/utils/modelProbe.test.ts`
  - Files: `ui/lib/store/apis/providersApi.ts`, `ui/lib/types/providers.ts`, `ui/lib/utils/modelProbe.ts`, `ui/lib/utils/modelProbe.test.ts`
  - Deps: T5, T8 · Size: S

- [x] **T10 — Komponen tombol Test bersama**
  - Acceptance: tombol ikon-only, spinner + disabled per baris, multi-probe antar baris; `data-testid` via prop.
  - Verify: `npx vitest run components/modelTest/modelTestButton.test.tsx`; `npm run typecheck`
  - Files: `ui/components/modelTest/modelTestButton.tsx`, `.../modelTestButton.test.tsx`
  - Deps: T9 · Size: S

- [x] **T11 — Wiring routing rules (target + fallback)**
  - Acceptance: urutan `model|test|X` dan `provider|model|test|trash`; tombol tidak memicu drag; key baris terkirim.
  - Verify: `npx vitest run app/workspace/routing-rules/views/routingFallbacksField.test.tsx`; `npm run typecheck`
  - Files: `ui/app/workspace/routing-rules/views/routingRuleSheet.tsx`, `.../routingFallbacksField.tsx`, `.../routingFallbacksField.test.tsx`
  - Deps: T10 · Size: S

- [x] **T12 — i18n semua locale**
  - Acceptance: `npm run i18n:check` exit 0; tidak ada string hardcoded baru.
  - Verify: `cd ui && npm run i18n:check`
  - Files: `ui/locales/*/models.json` (12)
  - Deps: T10 · Size: M (mekanis)

- [x] **T13 — Wiring model catalog + gating mode**
  - Acceptance: tombol `test|edit`; disabled + tooltip untuk mode non-probeable; gerbang `ModelProvider/Update`.
  - Verify: `npx vitest run app/workspace/model-catalog`; `npm run typecheck`; `npm run build`
  - Files: `ui/app/workspace/model-catalog/views/attributesTab.tsx`, test terkait
  - Deps: T8, T10 · Size: S

### Checkpoint: UI
- [x] `typecheck` + `lint` + `i18n:check` + `build` hijau
- [ ] Manual `make dev`: tiga permukaan benar; dua baris diprobe bersamaan, toast sesuai urutan selesai

## Phase 4: Verification & Docs

- [x] **T14 — Harness case `model probe`**
  - Acceptance: augment + filter lulus; case meng-assert 200/`ok:true`/`ttft_ms>0`/`mode=="chat"`; insersi surgical.
  - Verify: `node tests/e2e/api/runners/augment-provider-harness.mjs ...` lalu `filter-collection.mjs --feature "model probe"`
  - Files: `tests/e2e/api/collections/provider-harness.json`
  - Deps: T5, T6 · Size: S

- [x] **T15 — E2E Playwright**
  - Acceptance: `make run-e2e FLOW=routing-rules` dan `FLOW=model-catalog` hijau.
  - Catatan: satu test drag-cancel (`should restore the original order when a drag is cancelled with Escape`) sudah merah sebelum pekerjaan ini (dibuktikan di worktree 95fc09cd3); crash 500-nya diperbaiki di commit 0ab9528c1, sisa perilaku Escape-menutup-sheet adalah bug lama.
  - Verify: kedua target di atas.
  - Files: `tests/e2e/features/routing-rules/pages/routing-rules.page.ts`, `.../routing-rules.spec.ts`, `tests/e2e/features/model-catalog/model-catalog.spec.ts`
  - Deps: T11, T13 · Size: M

- [x] **T16 — Docs**
  - Acceptance: routing.mdx menjelaskan tombol Test, TTFT vs Latency, dan bahwa probe tidak tercatat di logs/biaya.
  - Verify: review manual; `docs/openapi/spec_invariants_test.py` bila OpenAPI tersentuh.
  - Files: `docs/features/governance/routing.mdx`
  - Deps: T11, T13 · Size: XS

### Checkpoint: Complete
- [ ] Semua Success Criteria spec terpenuhi
- [ ] `make test`, `make test-plugins`, `make test-governance`, UI checks hijau
- [ ] Siap review
