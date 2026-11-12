# Spec: Test/Probe Model (`POST /api/models/test`)

## Objective

Tombol **Test** per baris model di tiga permukaan UI memanggil endpoint server baru yang mengirim probe nyata ke provider, lalu menyajikan verdict + pengukuran waktu dalam toast (sonner). Verdict menjawab "model ini hidup atau tidak" dan, untuk mode streaming, "berapa lama sampai token pertama".

- **Routing rule sheet — baris Target**: `ProviderSelector | ModelSelector | [Test] | [X]`
- **Routing rule sheet — baris Fallback**: `ProviderSelector | ModelSelector | [Test] | [Trash]`
- **Model catalog — tab Models**: `[Test] | [Edit]` per baris `(provider, model)`

Probe **selalu dijalankan di server** (`POST /api/models/test`), bukan `fetch` dari browser. Mode probe ditentukan server dari katalog model; client hanya mengirim `{provider, model, key_id?}`.

**User**: Admin/operator yang menyusun routing rule dan mengelola model catalog — perlu tahu model benar-benar menjawab dengan kredensial terpilih *sebelum* konfigurasinya disimpan.

**Reference (inspirasi)**: `luqman-v1/9router-go` `internal/handlers/media/model_test_ping.go` — endpoint `POST /api/models/test`, probe server-side, verdict `{ok, latencyMs, note, error, ...}`, timeout 45s. Perbedaan penting: reference memakai `"stream": false` + `max_tokens: 1024` sehingga mengukur **latency total, bukan TTFT**; kita memakai streaming agar TTFT bisa diukur.

## Keputusan dari interview (mengikat)

1. Probe di server lewat endpoint `POST /api/models/test`. Body `{provider, model, key_id?}`.
2. Mode probe ditentukan server dari katalog (`ModelCapabilities.Mode` / datasheet `Entry.Mode`), fallback `"chat"`. Sistem memilih jalur per mode yang valid & akurat.
3. Streaming, TTFT dari delta pertama, prompt `"hi"`, `max_tokens` kecil (16).
4. **Probe penuh**: stream dibaca sampai `finish_reason` / channel selesai, tidak dibatalkan di delta pertama — error di tengah stream tertangkap.
5. Timeout tetap **60 detik** (konstanta, bukan config). Timeout = verdict gagal `timeout setelah 60s`.
6. `key_id` opsional; kosong = auto-select persis jalur request normal (`x-bf-api-key-id` bila diisi). Toast menyebut key yang akhirnya dipakai (nama/ID).
7. Probe **disembunyikan dari log, biaya, dan stats** (penanda konteks internal; tidak ada flag yang ada cukup).
8. Mode unary menampilkan `Latency` (total), mode streaming menampilkan `TTFT`.
9. Posisi tombol: Target `model | test | X`, Fallback `provider | model | test | trash`, katalog `test | edit`.
10. Toast: satu per klik, judul = verdict + `provider/model`, deskripsi = `TTFT`/`Latency` + key atau pesan error. Tanpa riwayat, tanpa penyimpanan permanen, tanpa "Test all".
11. Klik berulang pada baris yang sama: tombol disabled + spinner. Baris lain tetap bisa diprobe bersamaan (multi-probe, tanpa kunci global); toast muncul sesuai urutan selesai.
12. RBAC: gerbang yang sudah ada — `ModelProvider/Update` (catalog, gerbang tombol Edit), `RoutingRules/Create|Update` (sheet, gerbang Save). Tanpa resource RBAC baru.
13. Endpoint didaftarkan di handler yang sama dengan manajemen model (`ProviderHandler`, file `providers.go`).

## Tech Stack

- Backend: Go 1.27.3 workspace; `github.com/maximhq/bifrost/core` (`bifrost.Bifrost`), `core/schemas`, `framework/modelcatalog`, `transports/v2/bifrost-http/handlers`, `github.com/bytedance/sonic`, `github.com/valyala/fasthttp`.
- Frontend: React + TypeScript (Vite), RTK Query (`baseApi`), react-hook-form, i18next, lucide-react, sonner (`toast`).

## Commands

```bash
# Backend unit tests (target yang meng-cd ke transports/bifrost-http)
make test

# Provider harness (live, milik user) setelah build
make dev PORT=<port> APP_DIR=$(pwd)/tests/integrations/python
make run-provider-harness-test PROVIDER=<provider> FEATURE="model probe"

# UI
cd ui && npx vitest run                     # unit test
cd ui && npm run typecheck
cd ui && npm run lint
cd ui && npm run i18n:check                 # wajib jika menambah key locales
cd ui && npm run format:changed
```

## Project Structure

```
core/schemas/bifrost.go        → tambah BifrostContextKeyIsProbe (bool) — DO NOT SET MANUALLY
core/schemas/ (search)         → tempat pengecekan "adalah probe" dipakai — reserved key
plugins/logging/main.go        → PreLLMHook+PostLLMHook awal: jika isProbe → return tanpa pending/write
plugins/governance/main.go     → PostLLMHook: jika isProbe → return tanpa UpdateUsage/cost/rate-limit
plugins/semanticcache/main.go  → PreLLMHook awal: jika isProbe → skip lookup & write (kalau jalur satu paket)
transports/bifrost-http/handlers/providers.go → + route POST /api/models/test + testModel + probe runner
transports/bifrost-http/handlers/providers_test.go → unit test endpoint & dispatcher (update existing)
ui/lib/store/apis/providersApi.ts → + testModel mutation (payload {provider, model, key_id})
ui/lib/types/…                 → + ModelTestResponse (di file types yang sudah ada)
ui/components/…                → komponen / hook probe per baris yang dipakai tiga permukaan
ui/app/workspace/routing-rules/views/routingRuleSheet.tsx → tombol Test di TargetRow
ui/app/workspace/routing-rules/views/routingFallbacksField.tsx → tombol Test di FallbackRow
ui/app/workspace/model-catalog/views/attributesTab.tsx → tombol Test sebelum Edit
ui/locales/en/<ns>.json        → key i18n baru + mirror seluruh locale
tests/e2e/api/collections/provider-harness.json → case harness (regression, wire-visible)
docs/specs/SPEC-model-test-probe.md → dokumen ini (update bila keputusan berubah)
```

## API Contract — `POST /api/models/test`

### Request

```json
{
  "provider": "openai",
  "model": "gpt-4o-mini",
  "key_id": "uuid-opsional"
}
```

- `provider`, `model`: wajib, non-empty.
- `key_id`: opsional. Jika diisi → di-_set_ ke `BifrostContextKeyAPIKeyID` (dipakai pemilihan key, `lib/ctx.go:571` sudah memetakan header, tapi di sini kita set langsung lewat konteks internal). Kosong → auto-select seperti request biasa.
- Kendala: seluruh path key selection **kewajiban plugin yang berjalan**: `x-bf-api-key-id` hanya ditangkap dari header dalam jalur HTTP; pada pemanggilan langsung core/Go SDK, set `BifrostContextKeyAPIKeyID` langsung di `BifrostContext` agar plugin memilih key via jalur yang sama.

### Response

```json
{
  "ok": true,
  "ttft_ms": 412,
  "total_ms": 654,
  "key_id": "uuid",
  "key_name": "prod-2",
  "status": 200,
  "model": "gpt-4o-mini",
  "provider": "openai",
  "note": "reasoning-only response (length-limited)",   // optional, sukses
  "error": "HTTP 401: invalid api key",                 // optional, gagal
  "mode": "chat"
}
```

- Sukses: `ok: true` + setidaknya satu angka waktu (`ttft_ms` bila mode streaming, `total_ms` selalu).
- Gagal: `ok: false` + `error` (pesan dari provider / status). `ttft_ms` tidak diisi saat gagal.
- Kunci yang dipakai selalu dikembalikan: `key_id` + `key_name` dibaca dari konteks pasca-pemilihan (`BifrostContextKeySelectedKeyID` / `BifrostContextKeySelectedKeyName`, `core/schemas/bifrost.go:289-290`). Bila plugin governance tidak aktif dan tidak ada yang men-stamp key, field-nya kosong dan toast menampilkan `key auto`.
- `mode` dikembalikan agar UI tahu label yang tepat (TTFT vs Latency) tanpa duplikasi logika.

### HTTP status & error

- 400 untuk body tidak valid / provider atau model kosong / mode tidak dikenal.
- Probe gagal (provider error, timeout) → **HTTP 200** dengan `ok:false` + `error`. Ini menyamakan kasus "request ditolak karena konfigurasi" dengan "provider mengembalikan error nyata" sebagai verdict probe, bukan kesalahan transport endpoint. Endpoint sendiri hanya gagal saat tidak bisa menjalankan probe sama sekali (500).

### Dispatcher mode (dibaca dari katalog; fallback `"chat"`)

| mode | jalur probe | verdict |
|---|---|---|
| `chat` | `ChatCompletionStreamRequest` + `max_tokens:16` | `ttft_ms` (delta ke-1), drain penuh |
| `responses` | `ResponsesStreamRequest` | `ttft_ms`, drain penuh |
| `completion` | `TextCompletionStreamRequest` | `ttft_ms`, drain penuh |
| `audio_speech` | `SpeechStreamRequest`, input `"hi"` | `ttft_ms`, drain penuh |
| `audio_transcription` | `TranscriptionStreamRequest`, upload WAV senyap (250ms) | `ttft_ms`, drain penuh |
| `image_generation` | `ImageGenerationStreamRequest`, prompt `"a red box"`, ukuran kecil | `ttft_ms`, drain penuh |
| `image_edit` | `ImageEditStreamRequest` | `ttft_ms`, drain penuh (baru bila mode row muncul) |
| `embedding` | `EmbeddingRequest` `input:["test"]` | `total_ms` saja (unary) |
| `rerank` | `RerankRequest` `query:"test"`, 1 dokumen | `total_ms` |
| `decisions` | `DecisionRequest` (probe decision sederhana) | `total_ms` |
| `ocr` | `OCRRequest` (dokumen tes, URL/tipe sesuai provider) | `total_ms` |
| `video_generation` | `VideoGenerationRequest` `prompt:"test"` (hanya membuat job) | `total_ms` |

Semua mode di atas dapat diprobe; payload per mode diverifikasi terhadap provider yang benar-benar memakai mode itu saat implementasi (O4). Mode yang tidak punya jalur valid (`container_create`, `live`, mode tak dikenal) ditolak dengan 400 `mode not supported for probe`, dan tombolnya disabled di tempat mode itu diketahui client (model catalog).

### Prasyarat UI: `mode` pada `/api/models/details`

Tombol di model catalog harus tahu mode **sebelum** diklik (untuk disabled + tooltip). Karena itu:

- `ModelDetailsResponse` (`transports/bifrost-http/handlers/providers.go:890`) mendapat `Mode string \`json:"mode,omitempty"\``, diisi dari `capabilities.Mode` dengan fallback `defaultCatalogPricingMode` (`chat`).
- `ModelDetails` di `ui/lib/store/apis/providersApi.ts` mendapat `mode?: string`.
- Routing rules tidak butuh field ini: mode baris target/fallback diresolusi server-side saat probe.

### Penyembunyian dari log/biaya/stats

- Key konteks baru: `BifrostContextKeyIsProbe (bool)` di `core/schemas/bifrost.go` (reserved: set oleh endpoint probe).
- Top of `LoggerPlugin.PreLLMHook` & `PostLLMHook`: jika `isProbe` → return tanpa menyimpan pending, tanpa write, tanpa notifikasi callback.
- Top of `GovernancePlugin.PostLLMHook`: jika `isProbe` → return tanpa `DeleteUsage`/cost/rate-limit/budget.
- `SemanticCachePlugin.PreLLMHook`: jika `isProbe` → return tanpa lookup & tanpa menulis cache (hindari verdict memakai cache).
- `request_id` tidak diregister ke log callback path, dan probe tidak memakai virtual key default sehingga budget-gate tidak terpicu.
- Catatan: `BifrostContextKeyDisableContentLogging` (`x-bf-disable-content-logging`) TIDAK cukup — hanya mematikan konten, bukan baris log (diverifikasi di `lib/ctx.go:760`).

### Perilaku paruh waktu & timeout

- `context.WithTimeout(ctx, 60*time.Second)` per probe; konstanta `testModelTimeoutSec = 60` (bukan config).
- Timeout → `ok:false` + `error: "timeout setelah 60s"`.
- Stream dibaca sampai channel menutup (drain penuh). Chunk pertama ≈ sentinel `StreamCommitMarker` bila ada; TTFT diukur sejak pengiriman request. Delta pertama diperlakukan sebagai token pertama (untuk mode chat: chunk dengan `choices[0].delta.content`/`finish_reason`).

## Code Style

- Go: file dalam satu package `handlers` — endpoint di `providers.go` mengikuti `listModelDetails` dsb. Nama `testModel(ctx)`/`probeModel(...)`/`runProbe(...)` mengikuti konvensi reference. Konverter probe per mode sebagai fungsi murni (bukan HTTP/logging). Error lowercase tanpa trailing punctuation. `sonic` untuk marshal/unmarshal di hot path.
- TS: komponen baris memakai `useState` lokal `Set<string>` key `provider|model` untuk baris yang sedang probe; RTK `builder.mutation` di `providersApi.ts`; `data-testid` kebab-case (`routing-target-{i}-model-test`, `routing-fallback-{i}-model-test`, `model-catalog-test-{testKey}`); semua string lewat `t(...)` + key locales.

## Testing Strategy

- **Backend unit (primary)**: extapkan ke dalam `providers_test.go` yang sudah ada — item:
  1. Route `POST /api/models/test` terdaftar; validasi: body tanpa model → 400.
  2. Dispatch mode benar dari katalog: `chat` → `ChatCompletionStreamRequest`, `embedding` → `EmbeddingRequest` (pake stub `ModelsManager`/catalog mode mapping).
  3. Verdict sukses: `ok:true` + `ttft_ms` dan `total_ms` terisi (stub stream yang kirim 1 chunk lalu selesai).
  4. Verdict gagal: provider error → `ok:false` + pesan; **timeout 60s** → `ok:false` + `timeout setelah 60s` (test gunakan timeout lebih pendek via var).
  5. `key_id` kosong → konteks pemilihan key tidak di-set; `key_id` terisi → `BifrostContextKeyAPIKeyID` ter-set (stub plugin mengamati).
  6. Probe `ok`, dan penanda isProbe di set sebelum request masuk (`LoggerPlugin`/`GovernancePlugin` no-op diobservasi lewat stub plugin yang memeriksa key).
  7. Mode unary tidak menghitung `ttft_ms` (hanya `total_ms`).
  8. `GET /api/models/details` mengembalikan `mode` per baris, termasuk fallback `chat` untuk baris tanpa datasheet.
- **Harness case**: `tests/e2e/api/collections/provider-harness.json` — case `model probe` (routing rule/catalog surface) karena fitur wire-visible (lihat aturan repo: setiap perubahan wire-visible dapat case). Provider **live saja** (openai, anthropic, dsb.); mapping mode diuji di stub unit, bukan di harness.
- **UI unit (vitest)**: helper toast formatting (deskripsi) — format `TTFT {n} ms · key {name}` vs `Latency {n} ms · key {name}`; helper mengabungkan verdict + mode.
- **E2E (Playwright)** opsional bila mudah, `tests/e2e/features/` — verify align tombol (Target/Fallback/catalog), disabled+spinner saat probe, multi-probe muncul sesuai selesai.
- **UI unit (lanjutan)**: fungsi penentu status tombol — `disabled` bila `mode` tidak ada di daftar mode probeable, plus pemilihan tooltip.

## Boundaries

- **Always do**:
  - Jalankan `make test` (atau `go test ./handlers/` di `transports/bifrost-http`) sebelum menyatakan selesai; `cd ui && npx vitest run`, `npm run typecheck`, `npm run i18n:check`.
  - Probe penuh; jangan batalkan di token pertama.
  - `key_id` opsional; kosong = auto-select; toast menyebut key terpakai.
  - Penanda isProbe di-reset setelah request; probe tidak tercatat di log/stats/biaya.
  - Multi-probe paralel (satu per baris, tanpa kunci global).
  - Semua tombol baru beri `data-testid` (cek referensi di `tests/e2e/`).
  - `make test-core` untuk provider yang terpengaruh setelah dispatch probe (chat minimal).

- **Ask first**:
  - Menambahkan dependensi baru (backend/UI) — seharusnya tidak perlu.
  - Menambah mode probe yang belum terverifikasi payload-nya (transcription/ocr/video/decisions) untuk provider tertentu — solution space lebih besar.
  - Mengubah `config.schema.json` (seharusnya tidak — timeout konstanta).
  - Key baru di `BifrostContextKey*` yang lain selain `IsProbe`.

- **Never do**:
  - Jangan hanya pakai `x-bf-disable-content-logging` sebagai satu-satunya penyembunyian (tidak menghilangkan baris log).
  - Jangan menambah resource RBAC baru.
  - Jangan hardcode string UI tanpa i18n.
  - Jangan menyimpan status/riwayat probe.
  - Jangan menambah tombol bulk/Test-all.

## Success Criteria

Testable, terverifikasi via unit + harness + UI check:

1. `POST /api/models/test` (terdaftar) menerima `{provider, model, key_id?}`; tanpa model/provider → 400.
2. Mode `chat` → probe `ChatCompletionStreamRequest` dengan `max_tokens:16`, `prompt "hi"`, return `ok:true` + `ttft_ms` + `total_ms` saat stub stream mengirim 1 chunk lalu selesai; `ok:false` + pesan saat stub mengembalikan error provider; `ok:false` + `timeout setelah 60s` saat stub menggantung > timeout (test memakai timeout diperpendek).
3. Mode `embedding` → probe `EmbeddingRequest`, return `ok` + `total_ms` tanpa `ttft_ms`.
4. `key_id` kosong → pemilihan key auto; `key_id` terisi → konteks memakai key tsb (`BifrostContextKeyAPIKeyID`); `ok` + `key_id`/`key_name` response = key terpilih (stub plugin mengamati).
5. Setiap probe menandai konteks `BifrostContextKeyIsProbe`; stub `LoggerPlugin`/`GovernancePlugin`/`SemanticCachePlugin` tidak menulis log, tidak update cost/rate-limit/budget, tidak lookup/write cache (unit).
6. UI Target/Fallback/catalog memposisikan tombol Test sesuai urutan keputusan #9, disable+spinner per baris saat probe berjalan, dan mengizinkan probe baris lain bersamaan (multi).
7. Toast format: judul verdict + `provider/model`; deskripsi memakai `TTFT {ttft_ms} ms · total {total_ms} ms · key {key_name}` (streaming) atau `Latency {total_ms} ms · key {key_name}` (unary); gagal → pesan error + key. i18n keys ada di semua locale, `npm run i18n:check` exit 0.
8. Harness case (`FEATURE="model probe"`) green; validasi struktural lulus (`augment-provider-harness.mjs` / `filter-collection.mjs`).
9. Probe tidak muncul di Logs UI, tidak menambah `total_requests`/`success_rate` provider, dan tidak mengubah spend/budget (E2E/unit pada stub store).
10. `make test` (transports handler) hijau tanpa regression.
11. `GET /api/models/details` mengembalikan `mode`; tombol Test di katalog disabled + tooltip untuk mode yang tidak dapat diprobe; baris tanpa datasheet ber-`mode` `chat` dan tombolnya aktif.

## Keputusan Open Questions (dikonfirmasi)

1. **Tombol untuk mode yang tidak dapat diprobe** (katalog): **disabled + tooltip** yang menyebut mode-nya, bukan tombol aktif yang server tolak. Konsekuensinya `mode` diekspos di `/api/models/details` (lihat "Prasyarat UI").
2. **Label & ikon tombol**: **ikon-only** (`FlaskConical` dari lucide) dengan `aria-label` + tooltip, konsisten dengan tombol `X` dan `Edit`; tanpa label teks.
3. **Klik ulang setelah selesai**: **boleh**, tanpa cooldown — yang dilarang hanya klik kedua saat probe baris itu masih berjalan.
4. **Payload mode non-chat**: **ya**, diverifikasi per provider saat implementasi mode itu; daftar mode di tabel dispatcher adalah komitmen, detail payload adalah pekerjaan implementasi.
5. **`mode` tidak ada di katalog** → fallback `chat` (keputusan interview #2), dipakai untuk baris custom/tanpa datasheet. Dikonfirmasi.
6. **Harness**: **provider live saja**; mapping `mode` → jalur probe diuji di stub unit, tidak di harness.

---

Setelah spec ini disetujui, phase PLAN → TASKS → IMPLEMENT berjalan dengan dependency:
schemas/context key → plugin skip (logging/governance/semanticcache) → endpoint probe + dispatcher mode →
`mode` di `/api/models/details` → RTK mutation + komponen tombol → tiga permukaan UI + i18n →
unit test → harness case → E2E.
