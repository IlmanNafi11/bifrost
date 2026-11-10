# TODO: Drag & Drop Reorder untuk Fallback List di Routing Rule Sheet

## Task 1: Tambah `reorderFallbacks` pure helper + unit test

**Description:** Extract reorder array sebagai pure function `reorderFallbacks` di `ui/lib/utils/routingRules.ts` (dekat `normalizeFallback`/`denormalizeFallback`). Helper menerima list + source index + target index, mengembalikan array baru (splice-move, immutable), dan no-op/gracefully mempertahankan array asli bila index out-of-bounds. Test diletakkan di file yang sudah ada `ui/lib/utils/routingRules.test.ts`.

**Acceptance criteria:**
- [x] `reorderFallbacks(items, from, to)` memindahkan item dari `from` ke `to` dan mengembalikan array baru (tidak memutasi input).
- [x] Move down (from < to), move up (from > to), dan from === to (no-op) menghasilkan urutan benar.
- [x] Out-of-bounds index mengembalikan salinan array tanpa perubahan/error.
- [x] Array kosong / satu elemen di-handle tanpa error.

**Verification:**
- [x] Tests pass: `cd ui && npx vitest run lib/utils/routingRules.test.ts`

**Dependencies:** None

**Files likely touched:**
- `ui/lib/utils/routingRules.ts`
- `ui/lib/utils/routingRules.test.ts`

**Estimated scope:** Small (1-2 files)

---

## Task 2: Integrasi dnd-kit sortable pada blok Fallbacks di `routingRuleSheet.tsx`

**Description:** Tambah identity stabil `fallbackIds: string[]` (paralel dengan `fallbacks`), di-update lockstep pada load/edit/add/remove/reorder. Bungkus blok Fallbacks dengan `DragDropProvider` + `onDragOver` yang mereorder `fallbacks` dan `fallbackIds`. `FallbackRow` memakai `useSortable` dengan `id` stabil + `GripVertical` handle (drag hanya lewat handle). Provider/model/key selectors dan tombol remove tetap berfungsi. Reorder tersimpan via `setValue("fallbacks", ...)` yang sudah ada — payload API tidak berubah.

**Acceptance criteria:**
- [x] User bisa menarik grip handle baris fallback ke posisi baru; urutan list ter-update live saat drag.
- [x] `fallbackIds` selalu sinkron panjang/urutan dengan `fallbacks` setelah add/remove/reorder.
- [x] Provider/model/key selectors dan tombol remove tetap berfungsi setelah ada drag handle.
- [x] Save mempertahankan urutan baru tanpa mengubah wire format (`denormalizeFallback`).

**Verification:**
- [x] Type check: `cd ui && npm run typecheck`
- [x] Manual: `cd ui && npm run dev`, buat rule dengan 3+ fallback, drag reorder, cek urutan di payload API (DevTools Network) saat save

**Dependencies:** Task 1

**Files likely touched:**
- `ui/app/workspace/routing-rules/views/routingRuleSheet.tsx`

**Estimated scope:** Medium (1 file, logika nontrivial)

---

## Task 3: Tambah i18n key drag handle + mirror ke semua locale

**Description:** Tambah key `routing.fallbackDragHandleAria` (aria-label grip handle, e.g. "Drag fallback {n} to reorder") di `ui/locales/en/models.json`, lalu mirror dengan terjemahan asli ke semua locale lain (de, es, fr, it, ja, ko, pt, ru, zh-CN, zh-TW). Gunakan `{{n}}` placeholder yang konsisten. Edit katalog surgically, jangan re-serialize seluruh file.

**Acceptance criteria:**
- [x] `routing.fallbackDragHandleAria` hadir di en dan 10 locale lain dengan terjemahan asli (bukan copy English).
- [x] Placeholder `{{n}}` konsisten di semua locale.
- [x] `FallbackRow` memakai `t("routing.fallbackDragHandleAria", { n: index + 1 })` sebagai `aria-label`.

**Verification:**
- [x] i18n check: `cd ui && npm run i18n:check`
- [x] Type check: `cd ui && npm run typecheck`

**Dependencies:** Task 2

**Files likely touched:**
- `ui/locales/en/models.json` + 10 locale mirror (`de`, `es`, `fr`, `it`, `ja`, `ko`, `pt`, `ru`, `zh-CN`, `zh-TW`)

**Estimated scope:** Medium (11 file, perubahan mechanical)

---

## Checkpoint: Complete

- [x] `npx vitest run lib/utils/routingRules.test.ts` lulus
- [x] `npm run typecheck` lulus
- [x] `npm run lint` lulus
- [x] `npm run i18n:check` lulus
- [x] Manual dev check drag reorder end-to-end
- [x] Siap review