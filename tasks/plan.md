# Implementation Plan: Drag & Drop Reorder untuk Fallback List di Routing Rule Sheet

## Overview

Menambahkan reorder drag & drop pada blok Fallbacks di `routingRuleSheet.tsx`. Pola dnd-kit (`DragDropProvider` + `useSortable` + `GripVertical`) diambil dari `pluginSequenceSheet.tsx`, dengan helper reorder murni yang dites terpisah. Payload API tidak berubah — reorder murni state frontend yang tersimpan lewat `setValue("fallbacks", ...)`.

## Architecture Decisions

- **Reorder state tetap di react-hook-form.** Sheet memakai `watch("fallbacks")` + `setValue("fallbacks", ...)` sebagai source of truth untuk nilai tersimpan. Drag hanya mengubah urutan array yang sama; tidak ada state paralel untuk data fallback.
- **Identity stabil untuk dnd-kit dipegang oleh array paralel `fallbackIds: string[]`.** `@dnd-kit/react/sortable` butuh `id` unik yang stabil selama drag. `RoutingFallbackFormData` tidak punya id, dan mengganti key/index sebagai id akan memutus identity saat array displice. Solusi: `fallbackIds` (generated via `crypto.randomUUID()` atau counter monotonik) dibuat saat load/edit dan add, di-reorder lockstep dengan `fallbacks` saat drag, dihapus saat remove. Tidak menyentuh struktur `RoutingFallbackFormData` / payload API.
- **Helper reorder diletakkan di `lib/utils/routingRules.ts`, tes di file yang sudah ada.** `reorderFallbacks()` di-extract sebagai pure function demi testability (`npx vitest run lib/utils/routingRules.test.ts`). Menggantikan catatan spec sebelumnya (file test baru) demi konvensi AGENTS.md: "add tests to existing test files, never new ones".
- **Drag hanya lewat grip handle** (bukan seluruh row), mempertahankan klik pada provider/model selector dan tombol remove tetap berfungsi normal.
- **Live reorder saat drag (`onDragOver`)** — persis pola plugin sheet; tidak menunggu drop.
- **Tidak ada tombol panah up/down** dan **tidak menyentuh blok Targets** (sesuai intent/scope).

## Task List

### Phase 1: Foundation
- [x] Task 1: Tambah `reorderFallbacks` pure helper + unit test

### Checkpoint: Foundation
- [x] `npx vitest run lib/utils/routingRules.test.ts` lulus semua

### Phase 2: Core Feature
- [ ] Task 2: Integrasi dnd-kit sortable pada blok Fallbacks di `routingRuleSheet.tsx`

### Checkpoint: Core Feature
- [ ] `npm run typecheck` lulus (type-safe)
- [ ] Manual: drag reorder baris fallback di rule sheet, urutan live ter-update, save menghasilkan payload urutan baru

### Phase 3: Polish
- [ ] Task 3: Tambah i18n key drag handle + mirror ke semua locale

### Checkpoint: Complete
- [ ] `npm run i18n:check` lulus
- [ ] `npm run lint` lulus
- [ ] Semua acceptance criteria terpenuhi
- [ ] Siap review

## Risks and Mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| Drift antara `fallbacks` (RHF) dan `fallbackIds` (paralel) saat add/remove/reorder | High | Semua mutasi (add/remove/drag) melewati satu helper/action yang memperbarui keduanya sekaligus; unit test helper reorder memverifikasi panjang array tetap sinkron |
| `id` dnd-kit berubah saat `key={index}` dipertahankan → drag source loss | High | Ganti ke identity stabil `fallbackIds`; jangan pakai index sebagai id |
| Regresi behavior add/remove/update selectors | Med | `FallbackRow` hanya ditambah handle; struktur provider/model/remove tidak diubah. Manual check pada dev |
| i18n mirror tidak lengkap (10 locale lain) | Low | `npm run i18n:check` memverifikasi parity; gunakan terjemahan asli, bukan copy English |

## Open Questions

- Tidak ada. Semua asumsi sudah dikunci di intent + spec dan mengikuti konvensi repo.