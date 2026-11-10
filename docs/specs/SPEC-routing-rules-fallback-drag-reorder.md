# Spec: Drag & Drop Reorder untuk Fallback List di Routing Rule Sheet

## Objective

Fallback list di routing rule sheet (form create/edit rule) saat ini hanya mendukung add/remove. Untuk mengubah prioritas fallback, user harus menghapus baris lalu menambahkannya ulang — padahal urutan array `fallbacks` menentukan urutan percobaan di core (`core/bifrost.go` membaca `fallbacks[0]` terlebih dahulu).

Fitur: reorder fallback rows via **drag & drop** langsung di blok Fallbacks, dengan live reorder saat drag (mirror pattern `pluginSequenceSheet.tsx`). Saat save, urutan baru tersimpan melalui `setValue("fallbacks", ...)` yang sudah ada → payload API tidak berubah sama sekali.

**User**: Admin/operator yang mengelola routing rules di menu Governance → Routing Rules.

**Success**: User dapat menarik grip handle pada baris fallback ke posisi baru (list reorder live saat drag), urutan baru tercermin di form state, dan tersimpan via tombol Save/Create yang sudah ada tanpa perubahan API.

## Tech Stack

- React + TypeScript (Vite), TanStack Router/Query, react-hook-form, i18next
- `@dnd-kit/react` 0.3.2 + `@dnd-kit/react/sortable` — **sudah terpasang** di `ui/package.json`, dipakai di `ui/app/workspace/plugins/sheets/pluginSequenceSheet.tsx` sebagai referensi pattern (DragDropProvider + useSortable + GripVertical handle)
- Styling: Tailwind + lucide-react icons (+ `cn` dari `@/lib/utils`)
- Test: Vitest 4.1.12 (config `ui/vitest.config.ts`, globals: true)

## Commands

Semua dijalankan dari direktori `ui/`:

```bash
# Jalankan semua test
npx vitest run

# Jalankan test satu file (file terkait)
npx vitest run app/workspace/routing-rules/views/routingRuleSheet.reorder.test.ts
npx vitest run app/workspace/plugins/sheets/pluginSequenceSheet.test.ts

# Watch mode saat development
npx vitest

# Type check
npm run typecheck

# Lint
npm run lint

# Format
npm run format:check

# i18n consistency check (wajib jika add/edit key locales)
npm run i18n:check

# Verify no new deps (harus tetap @dnd-kit/react 0.3.2, tidak ada penambahan)
grep -E '"@dnd-kit' package.json

# Build production (opsional, verifikasi akhir)
npm run build
```

> Catatan: `npx vitest run <file>` tanpa argumen config engine gagal jika `node_modules` belum ter-install → jalankan `npm install` di `ui/` terlebih dahulu.

## Project Structure

```
ui/
├── app/workspace/routing-rules/views/
│   ├── routingRuleSheet.tsx                    # Sheet form utama (ubah di sini)
│   └── routingRuleSheet.reorder.test.ts        # Unit test reorder logic (baru)
├── app/workspace/plugins/sheets/
│   └── pluginSequenceSheet.tsx                 # Referensi pola dnd-kit (read-only)
├── components/ui/
│   ├── modelSelector.tsx                       # Sub-komponen FallbackRow (read-only)
│   ├── providerSelector.tsx                    # Sub-komponen FallbackRow (read-only)
│   └── select.tsx                              # Sub-komponen FallbackRow (read-only)
├── lib/utils/
│   └── routingRules.ts                         # normalize/denormalize fallback (read-only)
├── lib/types/
│   └── routingRules.ts                         # Types & DEFAULT_ROUTING_FALLBACK (read-only)
└── locales/
    ├── en/models.json                          # Tambah key i18n di sini
    └── [id/...]                                # Mirror key di locale lain
```

**File yang di-modify:**
1. `ui/app/workspace/routing-rules/views/routingRuleSheet.tsx` — integrasi dnd-kit pada blok Fallbacks (`FallbackRow`), drag handle, dan reorder state.
2. `ui/locales/en/models.json` — tambah key i18n (`routing.fallbackDragHandleAria` atau setara, plus deskripsi hint reorder jika perlu).
3. Locale mirror lainnya (`id`, dll.) via `npm run i18n:check` untuk verifikasi sinkron.

**File baru:**
1. `ui/app/workspace/routing-rules/views/routingRuleSheet.reorder.test.ts` — unit test reorder.

## Code Style

Konsisten dengan `pluginSequenceSheet.tsx` — tailwind class + lucide-react icon (tanpa emoji, tanpa komentar kecuali yang menjelaskan non-obvious).

**Contoh snippet pattern yang diikuti** (dari `pluginSequenceSheet.tsx`):

```tsx
const { ref, isDragging, handleRef, targetRef } = useSortable({ id: item.id, index });

<div
	ref={ref}
	className={cn(
		"space-y-2 rounded-lg border p-3 bg-white dark:bg-zinc-900 transition-colors",
		isDragging && "opacity-50",
	)}
	data-testid={`routing-fallback-${index}`}
>
	<div className="flex items-center gap-2">
		<div ref={handleRef} className="cursor-grab active:cursor-grabbing shrink-0" aria-label={t("routing.fallbackDragHandleAria", { n: index + 1 })}>
			<GripVertical className="text-muted-foreground h-4 w-4 shrink-0" />
	 </div>
	 {/* ...provider/model selectors, remove button... (isi FallbackRow lama tetap) */}
	</div>
</div>
```

**Naming conventions:**
- Komponen: PascalCase (`FallbackRow`, tetap)
- Handlers: `handleDragOver`, `handleReorder`
- testid: kebab-case dengan index (`routing-fallback-${index}-handle`)
- i18n keys: camelCase di dalam `routing.*`

**Formatting**: Tab indentasi (like `pluginSequenceSheet.tsx`), double quotes, tailwind class order konsisten. Validasi pakai `npm run format:check`.

## Testing Strategy

- **Framework**: Vitest, config `ui/vitest.config.ts` (globals: true, alias `@` → `ui/`).
- **Level utama**: unit test pure logic — `reorderFallbacks(fallbacks, sourceIndex, targetIndex)` di-extract sebagai pure helper agar mudah dites (mirip `mergeColumnEntries` di `useColumnConfig`).
- **File test**: `ui/app/workspace/routing-rules/views/routingRuleSheet.reorder.test.ts`
- **Coverage ekspektasi**: reorder logic 100% (pure function), UI integrasi di manual/E2E (tidak ada `@testing-library/react` di project ini — hanya vitest + globals, jadi UI integration test tidak praktikal di level ini).
- **Test levels**:
  - Unit test pure helper: reorder array fallback (swap, move across index, single element, empty array).
  - Type safety: `npm run typecheck`.
  - Manual test: jalankan `npm run dev`, buat rule dengan N fallback, drag reorder, verify urutan tersimpan di payload API (DevTools network).

## Boundaries

- **Always do**:
  - Jalankan `npx vitest run <file>` sebelum selesai, dan `npm run typecheck` + `npm run lint` sebelum mendeklarasikan selesai.
  - Update i18n keys dengan `npm run i18n:check` untuk memastikan locale konsisten.
  - Jaga urutan fallback tetap sinkron dengan `fallback_index` yang sudah ada.
  - Komponen `FallbackRow` yang sudah ada (provider/model/key selectors) tetap dipertahankan fungsionalnya — hanya tambah drag handle.
  - Live reorder saat drag (onDragOver) sesuai pola `pluginSequenceSheet.tsx`.
  - Preservasi data fallback (provider/model/key_id) saat reorder.

- **Ask first**:
  - Menambahkan dependencies baru (task ini TIDAK perlu — `@dnd-kit/react` 0.3.2 sudah ada).
  - Mengubah struktur `RoutingFallbackFormData` di `lib/types/routingRules.ts`.
  - Refactor besar di luar scope (misal: reorder blok Targets, refactor `routingRuleSheet.tsx` secara struktural).

- **Never do**:
  - Jangan ubah API payload (`denormalizeFallback` / wire format) — reorder murni frontend, backend sudah mengonsumsi array berurutan.
  - Jangan migrate data / forget `DEFAULT_ROUTING_FALLBACK` — hanya tambah reorder.
  - Jangan hapus/mengubah perilaku "filter out incomplete fallbacks (empty provider)" saat save.
  - Jangan hardcode string UI tanpa i18n — semua label baru harus via `t(...)` + key di `models.json`.

