# Intent: Drag & Drop Reorder untuk Fallback List di Routing Rule Sheet

## Statement of Intent

- **Outcome**: Fallback list di routing rule sheet (form create/edit rule) bisa di-reorder via drag & drop, sehingga prioritas fallback diatur tanpa delete-add ulang.
- **User**: Admin/operator yang mengelola routing rules di UI governance.
- **Why now**: Urutan fallback menentukan urutan percobaan di core (`fallbacks[0]` dicoba lebih dulu), tapi UI sekarang hanya punya add/remove.
- **Success**: User menarik baris fallback ke posisi baru (live reorder saat drag), urutan ter-update di form state, dan tersimpan via tombol save yang sudah ada.
- **Constraint**: Pakai `@dnd-kit/react` yang sudah ada di `ui/package.json` (pola sortable + grip handle sudah ada di `ui/app/workspace/plugins/sheets/pluginSequenceSheet.tsx`), tanpa dependensi baru, konsisten dengan desain sheet sekarang.
- **Out of scope**: Reorder blok targets routing; tombol panah up/down; perubahan API/backend; fitur logging IP client (dibatalkan — sudah didukung di versi sekarang via kolom `client_ip` + toggle `capture_client_ip`).

## Konteks Eksplorasi

- Sheet yang dimodifikasi: `ui/app/workspace/routing-rules/views/routingRuleSheet.tsx` (komponen `FallbackRow`, state fallbacks via `setValue("fallbacks", ...)` dari react-hook-form).
- Urutan fallback bermakna bisnis: `core/bifrost.go` membaca `fallbacks` sebagai array berurutan; reorder frontend langsung mengubah prioritas percobaan.
- Library: `@dnd-kit/react` 0.3.2 + `@dnd-kit/react/sortable` sudah terpasang dan dipakai di `pluginSequenceSheet.tsx` (`DragDropProvider` + `useSortable` + grip handle `GripVertical`), sehingga tidak butuh dependensi baru.
- Fallback rows punya state tidak sempurna untuk drag-by-id: `key={index}` dan fallback kosong (provider belum dipilih) valid dengan `DEFAULT_ROUTING_FALLBACK` — stabilisasi id/identity row perlu diperhatikan saat implementasi.

## Status

Intent dikonfirmasi (explicit yes) pada sesi interview. Spec dan task planning belum dibuat.
