# Intent: Test/Probe Model di Routing Rules dan Model Catalog

## Statement of Intent

- **Outcome**: Tombol Test per baris model di routing rules (baris Target dan baris Fallback) dan di tab Models pada model catalog, yang mengirim probe nyata ke provider dan menyajikan verdict + waktu dalam toast sonner. Verdict menjawab "model ini hidup atau tidak" dan, untuk mode streaming, "seberapa cepat token pertama datang".
- **User**: Operator yang menyusun routing rule dan yang mengelola model catalog — orang yang perlu tahu model menjawab atau tidak sebelum konfigurasinya disimpan, bukan setelah traffic nyata gagal.
- **Why now**: Routing rule dan catalog saat ini hanya menampilkan provider/model/key yang dikonfigurasi; tidak ada cara memverifikasi model benar-benar melayani request dengan kredensial yang dipilih. Kesalahan konfigurasi baru terlihat saat request pengguna gagal.
- **Success**:
  - Klik Test → tombol di baris itu disabled + spinner selama probe berjalan; klik berulang di baris yang sama tidak menghasilkan request kedua.
  - Beberapa baris bisa diprobe bersamaan (mis. gpt-5.4 lalu deepseek-v4-flash); tiap toast muncul sendiri sesuai urutan selesai — probe yang selesai lebih dulu menampilkan toast lebih dulu. Tidak ada kunci global.
  - Probe sukses menampilkan angka waktu — `TTFT` untuk mode streaming, `Latency` untuk mode unary — dan probe gagal menampilkan pesan dari provider.
  - Toast menyebut key yang akhirnya dipakai, termasuk saat `key_id` kosong dan gateway melakukan auto-select.
  - Probe tidak muncul di Logs, tidak menambah `total_requests`/`success_rate`, dan tidak dihitung biaya.
- **Constraint**:
  - Probe dijalankan di server melalui endpoint baru `POST /api/models/test`, bukan `fetch` dari browser. Body `{provider, model, key_id?}`; mode ditentukan server dari katalog.
  - Probe penuh: stream dibaca sampai `finish_reason`, tidak dibatalkan pada delta pertama. Timeout tetap 60 detik sebagai konstanta (bukan config); timeout = verdict gagal dengan pesan `timeout setelah 60s`.
  - Payload probe per mode mengikuti kontrak endpoint mode itu, dan verdict dibaca dari response yang dilihat client nyata pada endpoint tersebut.
  - `key_id` opsional; kosong = auto-select persis jalur request normal (`x-bf-api-key-id` bila diisi).
  - Tanpa penyimpanan status permanen, tanpa riwayat, tanpa tombol "Test all", tanpa resource RBAC baru.
- **Out of scope**:
  - Loop health berkala / auto-disable model (subsistem `modelhealth` di repo reference) — tidak dibangun.
  - Kolom status atau riwayat hasil test di tabel mana pun.
  - Probe di permukaan lain (tabel key provider, playground).
  - Menambah tombol `X` untuk model di baris fallback.

## Posisi Tombol

| Permukaan | Urutan kontrol |
|---|---|
| Routing target row | `ProviderSelector \| ModelSelector \| [Test] \| [X]` |
| Routing fallback row | `ProviderSelector \| ModelSelector \| [Test] \| [Trash]` |
| Model catalog, tab Models | `[Test] \| [Edit]` (per baris provider/model) |

## Bentuk Hasil (sonner)

Satu toast per klik: judul = verdict + `provider/model` (`Test passed · openai/gpt-4o-mini` / `Test failed · openai/gpt-4o-mini`), deskripsi = `TTFT 412 ms · total 1.2 s · key prod-2` atau pesan error provider + key. Tidak ada riwayat; toast hilang setelah durasi normal sonner.

## Konteks Eksplorasi

### Permukaan UI yang disentuh

- `ui/app/workspace/routing-rules/views/routingRuleSheet.tsx` — `TargetRow` (baris 739-869): `ModelSelector` lalu tombol `X` (`routing-target-{i}-model-clear`, hanya render saat `target.model` terisi). Tombol Test ditaruh di antara keduanya. State `targets` dikelola di luar react-hook-form; key yang dipakai = `target.key_id`.
- `ui/app/workspace/routing-rules/views/routingFallbacksField.tsx` — `FallbackRow`: `ProviderSelector`, `ModelSelector`, lalu satu tombol Trash per baris (tanpa tombol clear model). Tombol Test ditaruh sebelum Trash. Baris dibungkus `SortableFallbackRow` (grip handle), jadi tombol baru harus tidak memulai drag.
- `ui/app/workspace/model-catalog/views/attributesTab.tsx` — tabel `(provider, model)`; tombol Edit sudah dijaga `useRbac(RbacResource.ModelProvider, RbacOperation.Update)` (baris 59, dipakai baris 259). Tombol Test ditaruh di sel aksi yang sama, di kiri tombol Edit, dengan gerbang RBAC yang sama.

### Backend

- Belum ada endpoint test/probe model. Route provider yang ada (`transports/bifrost-http/handlers/providers.go:250-278`): CRUD provider/keys, `refresh-models`, `/api/models`, `/api/models/details`, `/api/models/datasheet/*`. Routing rules: `transports/bifrost-http/handlers/routing.go:119-123`.
- Mode model tersedia di katalog: `schemas.ModelCapabilities.Mode` (`core/schemas/modelcapabilities.go`, komentar "chat, embedding, image_generation, …"), dibaca di `listModelDetails` (`providers.go:1055+`) dengan fallback `defaultCatalogPricingMode = "chat"`. `/api/models/details` belum mengekspos `mode` ke UI — tidak perlu ditambah, karena mode ditentukan server.
- Vocab mode untuk pricing/request: `framework/modelcatalog/datasheet/types.go:515-527` (`chat`, `responses`, `completion`, `embedding`, `rerank`, `decisions`, `audio_speech`, `audio_transcription`, `image_generation`, `image_edit`, `video_generation`, `ocr`, `container_create`, `live`).
- Endpoint mode yang tersedia di inference handler (`handlers/inference.go:872-916`): `/v1/chat/completions`, `/v1/responses`, `/v1/completions`, `/v1/embeddings`, `/v1/rerank`, `/v1/decisions`, `/v1/ocr`, `/v1/audio/speech`, `/v1/audio/transcriptions`, `/v1/images/generations`, `/v1/images/edits`, `/v1/images/variations`, `/v1/videos`, `/v1/videos/edits`, `/v1/videos/{id}/remix`.
- Jalur streaming di provider interface (`core/schemas/provider.go:802-920`): `ChatCompletionStream`, `ResponsesStream`, `TextCompletionStream`, `SpeechStream`, `TranscriptionStream`, `ImageGenerationStream`, `ImageEditStream`. `Embedding`, `Rerank`, `Decision`, `OCR`, `VideoGeneration` unary saja — video hanya membuat job.
- TTFT sudah ada di infrastruktur streaming: `framework/streaming/types.go:30` (`TimeToFirstToken int64`, ms di `AccumulatedData`), di-stamp di `framework/tracing/tracer.go:982`. Non-stream tidak mengukur TTFT.
- Pin key per request: header `x-bf-api-key-id` → `schemas.BifrostContextKeyAPIKeyID` (`transports/bifrost-http/lib/ctx.go:571-573`).

### Penyembunyian probe dari log/biaya/stats

- Belum ada flag "jangan catat request ini". `x-bf-disable-content-logging` (`lib/ctx.go:760`, key `BifrostContextKeyDisableContentLogging`) hanya mematikan konten, bukan baris log-nya — tidak cukup.
- Ada jalur skip berbasis path untuk access log (`handlers/middlewares.go:39` `loggingSkipPaths`, dipakai baris 402), tapi itu access log HTTP, bukan log inference plugin.
- Karena itu probe butuh penanda konteks internal baru yang dibaca plugin logging/governance (biaya) dan jalur stats, supaya probe tercatat sebagai request biasa tetapi tidak masuk log, tidak menambah counter, dan tidak menagih budget.

### Reference yang dijadikan inspirasi (`luqman-v1/9router-go`)

- `internal/handlers/media/model_test_ping.go`: `POST /api/models/test` body `{model, kind}`; probe dijalankan di server dengan menyuntikkan request ke handler milik kind (`httptest.NewRecorder`), sehingga verdict dibaca dari response yang dilihat client nyata pada endpoint tersebut. Timeout `45s`, `max_tokens: 1024`, `"stream": false` — jadi reference mengukur latency total, bukan TTFT. Verdict `{ok, latencyMs, status, note, error, blocked, resetAt}`; error provider dipotong ~240 karakter.
- `internal/handlers/dashboard/validate_test.go` — stub untuk probe HTTP.
- `internal/modelhealth/*` — loop health berkala (enabled/interval/threshold/scope/auto-enable, disable setelah N gagal). **Di luar scope**; hanya dipakai sebagai pembanding bahwa tombol Test dan loop health adalah dua subsistem berbeda.

## Keputusan Sesi Interview

| Pertanyaan | Keputusan |
|---|---|
| Bentuk dukungan di model catalog | Tombol Test per baris provider/model, hasil ephemeral; tanpa kolom status, tanpa "Test all" |
| Penyajian hasil | Sonner toast, sama untuk routing rules dan catalog; tanpa penyimpanan permanen |
| Lokasi probe | Endpoint server `POST /api/models/test` |
| Bentuk probe | Streaming, TTFT dari delta pertama, prompt `"hi"`, `max_tokens` kecil |
| Cakupan mode | Semua mode dapat dites; mode ditentukan server dari katalog, fallback `chat` |
| Key untuk probe | `key_id` opsional; kosong = auto-select; toast menyebut key yang dipakai |
| Jejak probe | Disembunyikan dari log, biaya, dan stats |
| Mode unary | `Latency` (total), bukan `TTFT`; label mengikuti mode |
| Posisi tombol di routing | Target: di kiri `X`; Fallback: di kiri Trash |
| Probe selesai atau dibatalkan | Penuh — drain sampai selesai; timeout satu-satunya pengaman |
| Timeout | 60 detik, konstanta tetap, bukan config; timeout = verdict gagal `timeout setelah 60s` |
| RBAC | Gerbang yang sudah ada: `ModelProvider/Update` (catalog), `RoutingRules/Create\|Update` (sheet); tanpa resource baru |
| Bentuk toast | Satu toast per klik, judul = verdict + `provider/model`, deskripsi = waktu + key atau pesan error |
| Klik berulang / paralel | Baris yang sedang diprobe: tombol disabled + spinner. Baris lain tetap bisa diprobe bersamaan (multi probe), toast mengikuti urutan selesai |

## Status

Intent dikonfirmasi (explicit yes) pada sesi interview. Spec dan task planning belum dibuat.
