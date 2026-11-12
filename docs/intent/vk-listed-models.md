# Intent: Konfigurasi `listed_models` per Virtual Key

## Statement of Intent

- **Outcome**: Blok konfigurasi baru tingkat VK, `listed_models` (label UI "Allowed listed models"), terpisah dari `Provider Configurations`, berisi entri `{provider, model}` **dan** nama routing rule. Isinya menjadi (a) tepat apa yang dikembalikan `/models` untuk VK itu dan (b) tepat nama model yang boleh dipanggil lewat jalur inference mana pun yang membawa model name; di luar daftar → 403.
- **User**: Pemegang virtual key (hanya melihat permukaan yang diizinkan admin) dan admin VK yang mengonfigurasinya.
- **Why now**: `allowed_models: ["*"]` membocorkan seluruh katalog provider ke pemegang VK, dan routing rule tidak pernah muncul di `/models` — belum ada knob "tampil dan boleh dipanggil" yang terpisah dari "punya izin".
- **Success**: VK A dengan `listed_models = [{B, gpt-5.4}, {C, GLM-5.3}, rule "coding"}]` melihat `/v1/models` = `B/gpt-5.4`, `C/GLM-5.3`, `coding` saja; memanggil model lain → 403; memanggil `coding` tetap berjalan sampai seluruh target+fallback rule itu meski model di baliknya tidak ada di `allowed_models`/`listed_models`.
- **Constraint**: Berlapis di atas `allowed_models` (harus lolos dulu); picker hanya menawarkan opsi dari `allowed_models` + rule yang scope chain-nya cocok (termasuk global), dan aturan yang sama ditegakkan di server karena API bisa dipanggil langsung. Pengecualian rule divalidasi terhadap **definisi rule** (target+fallback), bukan bypass, dan gate **provider** tetap berlaku. Rule harus masih aktif dan scope-nya masih cocok saat runtime — fail closed. `nil`/absen = fitur nonaktif, `[]` = tolak semua. Budget & rate-limit ditagih penuh.
- **Out of scope**: Mengubah/mempensiunkan `allowed_models`; display name baru di response `/models`; mempersempit `/api/models` dan `/api/models/details`; mengubah format `id` (entri provider tetap berprefix, entri rule polos); gate budget/provider/MCP tidak disentuh.

## Keputusan Interview

| Topik | Keputusan |
|---|---|
| Bentuk konfigurasi | Satu blok VK-level baru, terpisah dari `Provider Configurations` |
| Hubungan dengan `allowed_models` | Lapisan kedua yang lebih sempit; harus lolos `allowed_models` dulu |
| Opsi di picker | Hanya model dari `allowed_models` + routing rule yang scope chain-nya cocok (termasuk global) |
| Penegakan | Sama di server (API langsung tidak boleh lebih longgar daripada UI) |
| Pengecualian rule | Divalidasi terhadap definisi rule (target + fallback), bukan bypass total |
| Gate provider | Tetap berlaku untuk target/fallback rule |
| Scope rule saat runtime | Harus masih aktif & masih cocok; kalau tidak → fail closed (403, hilang dari `/models`) |
| Default | `nil`/absen = fitur nonaktif (perilaku hari ini); `[]` = tolak semua |
| Cakupan gate | Semua jalur inference yang membawa model name (chat, responses, embeddings, images, speech, batch, passthrough) |
| `/api/models`, `/api/models/details` | Tetap katalog penuh (tidak dipersempit `listed_models`) |
| Format `id` di `/models` | Entri provider tetap berprefix (`provider/model`); entri rule polos (`coding`) |
| Display name / label | Tidak ada field baru; "label" hanya istilah UI ("Allowed listed models") |
| Budget & rate-limit | Tetap ditagih penuh, termasuk model-scoped limit untuk model di balik rule |

## Titik Implementasi yang Mengikat

- Gate model saat ini dievaluasi di `PreLLMHook` **setelah** routing me-rewrite model: `plugins/governance/main.go:1186-1191` membaca `req.GetRequestFields()` dan `plugins/governance/resolver.go:166-171` memeriksa `access.IsModelAllowed(provider, model)`. Routing menulis ulang model di `plugins/routing/main.go:541-546` (PreRequestHook, berjalan lebih dulu).
- Karena itu pengecualian rule harus membandingkan **nama yang diminta caller** (`BifrostContextKeyRequestedModel`, sudah distempel core) dengan entri rule di `listed_models`, lalu memvalidasi model final hanya terhadap definisi rule. Ini titik paling rawan dan perlu diuji eksplisit.
- Picker UI dapat diturunkan dari `Access.IsModelAllowed` yang sudah ada; penegakan server wajib memakai aturan yang sama agar tidak divergen.
- Listing VK saat ini difilter di `GovernancePlugin.PostLLMHook` (`plugins/governance/main.go:1629-1632` → `plugins/governance/utils.go:134-147`) dan fan-out dipersempit oleh `NarrowListModelsProviders` (`transports/bifrost-http/server/server.go:1879-1893`). Lapisan `listed_models` masuk di jalur yang sama.
- Model-scoped budget/rate-limit dikumpulkan lewat `resolveLimits` dengan provider/model final (`plugins/governance/main.go:768`), sehingga implementasi harus memastikan limit model di balik rule tetap terkumpul meski model itu tidak ada di `allowed_models`.

## Status

Intent dikonfirmasi (explicit yes) pada sesi interview. Spec dan task planning belum dibuat.
