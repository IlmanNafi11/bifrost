/**
 * Fallback rows of a routing rule: an ordered list where priority is the array order, so the rows
 * are reordered by dragging the grip handle. Dragging only changes order; every other edit goes
 * back to the caller.
 */

import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { ModelSelector } from "@/components/ui/modelSelector";
import { ProviderSelector, type ProviderSelectorOption } from "@/components/ui/providerSelector";
import type { RoutingFallbackFormData } from "@/lib/types/routingRules";
import i18n from "@/lib/i18n";
import { cn } from "@/lib/utils";
import type { FallbackRows } from "@/lib/utils/routingRules";
import { Accessibility, DragDropManager, type DragOverEvent, type DragStartEvent } from "@dnd-kit/dom";
import { DragDropProvider } from "@dnd-kit/react";
import { useSortable } from "@dnd-kit/react/sortable";
import { GripVertical, Plus, Trash2 } from "lucide-react";
import { useMemo, useRef } from "react";
import { useTranslation } from "react-i18next";
import { ProviderKeySelect } from "./routingProviderKeySelect";

interface FallbackRowProps {
	fallback: RoutingFallbackFormData;
	index: number;
	referencedProviderOptions: ProviderSelectorOption[];
	allKeys: Array<{ key_id: string; name: string; provider: string }>;
	onUpdate: (index: number, changes: Partial<RoutingFallbackFormData>) => void;
	onRemove: (index: number) => void;
}

function FallbackRow({ fallback, index, referencedProviderOptions, allKeys, onUpdate, onRemove }: FallbackRowProps) {
	const { t } = useTranslation("models");
	const provider = fallback.provider || "";

	return (
		<div className="space-y-2 rounded-lg border p-3" data-testid={`routing-fallback-${index}`}>
			<div className="flex items-center gap-2">
				<div className="flex-1">
					<ProviderSelector
						extraOptions={referencedProviderOptions}
						value={provider}
						// A key belongs to one provider, so switching providers invalidates the pin.
						onChange={(value: string) => onUpdate(index, { provider: value, model: "", key_id: "" })}
						placeholder={t("routing.selectProvider")}
						className="!h-9 !min-h-9"
						data-testid={`routing-fallback-${index}-provider-select`}
						noPortal
					/>
				</div>
				<div className="flex-1" data-testid={`routing-fallback-${index}-model-select`}>
					<ModelSelector
						provider={provider || undefined}
						value={fallback.model || ""}
						onChange={(value) => onUpdate(index, { model: value })}
						placeholder={t("routing.incomingOptional")}
						allowCustomModel
						unfiltered={true}
						disabled={!provider}
						className="!h-9 !min-h-9 w-full"
					/>
				</div>
				<Button
					type="button"
					variant="ghost"
					size="sm"
					onClick={() => onRemove(index)}
					className="h-9 px-2"
					aria-label={t("routing.removeFallback", { n: index + 1 })}
					data-testid={`routing-fallback-${index}-remove-button`}
				>
					<Trash2 className="h-4 w-4" />
				</Button>
			</div>

			<ProviderKeySelect
				idPrefix={`routing-fallback-${index}`}
				clearLabel={t("routing.clearFallbackApiKey", { n: index + 1 })}
				provider={provider}
				keyId={fallback.key_id}
				allKeys={allKeys}
				onChange={(value) => onUpdate(index, { key_id: value })}
			/>
		</div>
	);
}

interface SortableFallbackRowProps extends FallbackRowProps {
	id: string;
}

/**
 * The drag handle carries the row's identity, so only the handle starts a drag: the provider and
 * model selectors stay clickable, and a keyboard user can grab a row with Space or Enter.
 */
function SortableFallbackRow({ id, ...row }: SortableFallbackRowProps) {
	const { t } = useTranslation("models");
	const { ref, isDragging, handleRef } = useSortable({ id, index: row.index });

	return (
		<div
			ref={ref}
			className={cn("flex items-start gap-1", isDragging && "opacity-50")}
			data-testid={`routing-fallback-${row.index}-drag-row`}
		>
			<button
				type="button"
				ref={handleRef}
				className="text-muted-foreground hover:text-foreground focus-visible:ring-ring mt-3 cursor-grab rounded p-1 focus-visible:ring-2 focus-visible:outline-none active:cursor-grabbing"
				aria-label={t("routing.fallbackDragHandleAria", { n: row.index + 1 })}
				data-testid={`routing-fallback-${row.index}-handle`}
			>
				<GripVertical className="h-4 w-4 shrink-0" />
			</button>
			<div className="min-w-0 flex-1">
				<FallbackRow {...row} />
			</div>
		</div>
	);
}

interface RoutingFallbacksFieldProps {
	rows: FallbackRows<RoutingFallbackFormData>;
	referencedProviderOptions: ProviderSelectorOption[];
	allKeys: Array<{ key_id: string; name: string; provider: string }>;
	onAdd: () => void;
	onUpdate: (index: number, changes: Partial<RoutingFallbackFormData>) => void;
	onRemove: (index: number) => void;
	onReorder: (sourceId: string, targetId: string) => void;
	/** Restores the order a cancelled drag started from (Escape or pointercancel). */
	onCancel: (startOrder: readonly string[]) => void;
}

export function RoutingFallbacksField({
	rows,
	referencedProviderOptions,
	allKeys,
	onAdd,
	onUpdate,
	onRemove,
	onReorder,
	onCancel,
}: RoutingFallbacksFieldProps) {
	const { t } = useTranslation("models");

	// The row order as the current drag started. `onDragStart` fires once per drag, and a cancelled
	// drag (Escape, pointercancel) has already committed live reorders by then, so onCancel replays
	// this snapshot through the same helper that committed the reorder.
	const dragStartOrderRef = useRef<string[]>([]);

	// The manager is created once: rebuilding it on every reorder would destroy the drag in flight,
	// since a reorder commits new ids mid-drag. Announcements read the live rows and language through
	// refs instead, so they stay correct without re-creating the manager.
	const rowsRef = useRef(rows);
	rowsRef.current = rows;
	const dndManager = useMemo(() => {
		const rowNumberOf = (source: unknown): string => {
			const id = source && typeof source === "object" && "id" in source ? String(source.id) : "";
			const index = rowsRef.current.ids.indexOf(id);
			return String(index >= 0 ? index + 1 : "?");
		};
		const localized = Accessibility.configure({
			announcements: {
				dragstart: (event: Parameters<DragStartEvent>[0]) =>
					i18n.t("routing.fallbackDragAnnounce", { ns: "models", n: rowNumberOf(event.operation.source) }),
				// A separate key from dragstart: repeating "grabbed…" on every dragover tells the user nothing
				// new, so dragover states which position the row landed on.
				dragover: (event: Parameters<DragOverEvent>[0]) =>
					i18n.t("routing.fallbackDragMovedTo", { ns: "models", n: rowNumberOf(event.operation.target) }),
				dragend: () => i18n.t("routing.fallbackDragDropped", { ns: "models" }),
			},
			screenReaderInstructions: { draggable: i18n.t("routing.fallbackDragInstructions", { ns: "models" }) },
		});
		return new DragDropManager({
			plugins: (defaults) => defaults.map((plugin) => (plugin === Accessibility ? localized : plugin)),
		});
	}, []);

	return (
		<div className="space-y-3">
			<div className="flex items-center justify-between">
				<div>
					<Label>{t("routing.fallbacks")}</Label> <p className="text-muted-foreground mt-0.5 text-xs">{t("routing.fallbacksHelp")}</p>
				</div>
				<Button type="button" variant="outline" size="sm" onClick={onAdd} className="gap-2" data-testid="routing-fallback-add">
					<Plus className="h-4 w-4" />
					{t("routing.addFallback")}
				</Button>
			</div>
			<div className="space-y-2">
				{rows.fallbacks.length === 0 ? (
					<p className="text-muted-foreground text-sm">{t("routing.noFallbacks")}</p>
				) : (
					<DragDropProvider
						manager={dndManager}
						onDragStart={() => {
							dragStartOrderRef.current = [...rows.ids];
						}}
						onDragOver={(event) => {
							const { source, target } = event.operation;
							if (!source || !target || source.id === target.id) {
								return;
							}
							onReorder(String(source.id), String(target.id));
						}}
						onDragEnd={(event) => {
							if (!event.canceled) {
								return;
							}
							onCancel(dragStartOrderRef.current);
						}}
					>
						{rows.fallbacks.map((fallback, index) => {
							const rowId = rows.ids[index];
							if (import.meta.env.DEV && !rowId) {
								// The sheet guarantees ids.length === fallbacks.length, so an undefined id here
								// means that invariant broke somewhere upstream. Fail loudly instead of silently
								// keying by index (which would remount the row mid-interaction).
								throw new Error(`Routing fallback row ${index} has no stable id`);
							}
							return (
								<SortableFallbackRow
									key={rowId ?? index}
									id={rowId ?? String(index)}
									fallback={fallback}
									index={index}
									referencedProviderOptions={referencedProviderOptions}
									allKeys={allKeys}
									onUpdate={onUpdate}
									onRemove={onRemove}
								/>
							);
						})}
					</DragDropProvider>
				)}
			</div>
			<p className="text-muted-foreground text-xs">{t("routing.fallbacksOrder")}</p>
		</div>
	);
}