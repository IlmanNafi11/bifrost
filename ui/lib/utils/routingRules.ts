import i18n from "@/lib/i18n";
import type { RoutingTarget } from "@/lib/types/routingRules";
/**
 * Routing Rules Utility Functions
 * Helper functions for CEL validation, formatting, and rule management
 */

import type { RoutingFallbackFormData, RoutingFallbackWire } from "@/lib/types/routingRules";

/**
 * Validates if a CEL expression has basic correct syntax
 * @param expression - The CEL expression to validate
 * @returns true if expression appears syntactically valid
 */
export function isValidCELExpression(expression: string): boolean {
	if (!expression) {
		return true;
	}

	const trimmed = expression.trim();
	if (trimmed.length === 0 || trimmed === "true" || trimmed === "false") {
		return true;
	}

	// Check for basic syntax issues
	if (trimmed.includes(";;")) {
		return false;
	}

	// Check for matching brackets/parentheses
	const openBrackets = (trimmed.match(/[[{]/g) || []).length;
	const closeBrackets = (trimmed.match(/[\]}]/g) || []).length;
	const openParens = (trimmed.match(/\(/g) || []).length;
	const closeParens = (trimmed.match(/\)/g) || []).length;

	if (openBrackets !== closeBrackets || openParens !== closeParens) {
		return false;
	}

	return true;
}

/**
 * Formats a fallback string (provider/model) for display
 * @param fallback - The fallback string (e.g., "openai/gpt-4o")
 * @returns Formatted fallback string
 */
export function formatFallback(fallback: string): string {
	if (!fallback) return "";
	const parts = fallback.split("/");
	return parts.length === 2 ? `${parts[0].toUpperCase()} - ${parts[1]}` : fallback;
}

/**
 * Normalizes a wire fallback into the provider and model the sheet's two selects need. Unpinned
 * fallbacks arrive as the bare "provider/model" string.
 */
export function normalizeFallback(fallback: RoutingFallbackWire): RoutingFallbackFormData {
	if (typeof fallback !== "string") {
		return {
			provider: fallback.provider ?? "",
			model: fallback.model ?? "",
			key_id: fallback.key_id ?? "",
		};
	}
	const separator = fallback.indexOf("/");
	if (separator === -1) {
		return { provider: fallback, model: "", key_id: "" };
	}
	return { provider: fallback.slice(0, separator), model: fallback.slice(separator + 1), key_id: "" };
}

/**
 * Renders a fallback back onto the wire, as the bare string unless it pins a key. Sending the object
 * form for an unpinned fallback would change the rule's config hash on every save. The trailing
 * slash is required when the model is empty: a bare "anthropic" parses to an empty provider.
 */
export function denormalizeFallback(fallback: Partial<RoutingFallbackFormData>): RoutingFallbackWire {
	const provider = (fallback.provider ?? "").trim();
	const model = (fallback.model ?? "").trim();
	const keyId = (fallback.key_id ?? "").trim();
	if (keyId) {
		return model ? { provider, model, key_id: keyId } : { provider, key_id: keyId };
	}
	return `${provider}/${model}`;
}

/**
 * Moves the item at `from` to `to`, returning a new array. Out-of-bounds indices leave the order
 * untouched. Used by the sheet's drag reorder, which keeps a parallel id array in the same order.
 */
export function reorderFallbacks<T>(items: readonly T[], from: number, to: number): T[] {
	const result = [...items];
	if (from < 0 || from >= result.length || to < 0 || to >= result.length || from === to) {
		return result;
	}
	const [moved] = result.splice(from, 1);
	result.splice(to, 0, moved);
	return result;
}

let fallbackRowIdCounter = 0;

/** Render-stable identifier for a fallback row. Never persisted; dnd-kit keys need it across reorders. */
export function newFallbackRowId(): string {
	if (typeof crypto !== "undefined") {
		if (typeof crypto.randomUUID === "function") {
			return crypto.randomUUID();
		}
		if (typeof crypto.getRandomValues === "function") {
			// v4 UUID built from crypto-random bytes: unique across concurrent sheets/tabs,
			// unlike the counter fallback below.
			const bytes = new Uint8Array(16);
			crypto.getRandomValues(bytes);
			bytes[6] = (bytes[6] & 0x0f) | 0x40;
			bytes[8] = (bytes[8] & 0x3f) | 0x80;
			const hex = Array.from(bytes, (b) => b.toString(16).padStart(2, "0"));
			// UUID groups are byte-aligned: 8-4-4-4-12 hex digits. Groups 3 and 4 carry the
			// version/variant bits already applied to bytes 6 and 8 above.
			return [
				hex.slice(0, 4).join(""),
				hex.slice(4, 6).join(""),
				hex.slice(6, 8).join(""),
				hex.slice(8, 10).join(""),
				hex.slice(10).join(""),
			].join("-");
		}
	}
	fallbackRowIdCounter += 1;
	return `fallback-row-${fallbackRowIdCounter}`;
}

/** A fallback row list plus the render-stable ids that keep each row's DOM node pinned to it. */
export interface FallbackRows<T> {
	fallbacks: T[];
	ids: string[];
}

/** Fresh ids for a loaded rule's fallbacks; ids are per-open, never persisted. */
export function fallbackRowIds(count: number): string[] {
	return Array.from({ length: count }, () => newFallbackRowId());
}

/**
 * Moves a fallback row and its id together, so the two arrays stay index-aligned. The id array is
 * what keeps a row's DOM node pinned to its fallback while dnd-kit reorders live.
 */
export function reorderFallbackRows<T>(rows: FallbackRows<T>, from: number, to: number): FallbackRows<T> {
	return { fallbacks: reorderFallbacks(rows.fallbacks, from, to), ids: reorderFallbacks(rows.ids, from, to) };
}

export function addFallbackRow<T>(rows: FallbackRows<T>, fallback: T): FallbackRows<T> {
	return { fallbacks: [...rows.fallbacks, fallback], ids: [...rows.ids, newFallbackRowId()] };
}

export function removeFallbackRow<T>(rows: FallbackRows<T>, index: number): FallbackRows<T> {
	if (index < 0 || index >= rows.fallbacks.length) {
		return rows;
	}
	return {
		fallbacks: rows.fallbacks.filter((_, i) => i !== index),
		ids: rows.ids.filter((_, i) => i !== index),
	};
}

/** Row fields change; identity does not, so a selector edit never remounts its own row. */
export function updateFallbackRow<T>(rows: FallbackRows<T>, index: number, changes: Partial<T>): FallbackRows<T> {
	return {
		fallbacks: rows.fallbacks.map((row, i) => (i === index ? { ...row, ...changes } : row)),
		ids: rows.ids,
	};
}

/** Pads or truncates ids to `count`, keeping the ones already in place. Used after a form reset. */
export function reconcileFallbackIds(ids: readonly string[], count: number): string[] {
	if (ids.length === count) {
		return ids as string[];
	}
	if (ids.length > count) {
		return ids.slice(0, count);
	}
	return [...ids, ...fallbackRowIds(count - ids.length)];
}

/**
 * Applies a dnd-kit drag reported by row id. Unknown ids and a drop on the dragged row itself
 * return the input untouched, so a live drag never churns the form while it is over its own row.
 */
export function reorderFallbackRowsById<T>(rows: FallbackRows<T>, sourceId: string, targetId: string): FallbackRows<T> {
	const from = rows.ids.indexOf(sourceId);
	const to = rows.ids.indexOf(targetId);
	if (from === -1 || to === -1 || from === to) {
		return rows;
	}
	return reorderFallbackRows(rows, from, to);
}

/**
 * Puts the rows back into a previously captured id order. Used to undo a drag that was cancelled
 * (Escape, pointercancel): the live reorder has already been committed to the form by then, so the
 * only way back is to replay the order the drag started from. A snapshot that no longer matches the
 * current rows (a row was added or removed mid-drag) is ignored rather than half-applied.
 */
export function orderFallbackRowsByIds<T>(rows: FallbackRows<T>, ids: readonly string[]): FallbackRows<T> {
	if (ids.length !== rows.ids.length) {
		return rows;
	}
	// A duplicate id would make the Map-based replay ambiguous (two slots mapping to one row and
	// silently dropping the other). Current write paths keep ids unique, so treat it as invalid input
	// rather than half-applying a broken ordering.
	if (new Set(ids).size !== ids.length || new Set(rows.ids).size !== rows.ids.length) {
		return rows;
	}
	const byId = new Map(rows.ids.map((id, index) => [id, rows.fallbacks[index]]));
	if (ids.some((id) => !byId.has(id))) {
		return rows;
	}
	return { fallbacks: ids.map((id) => byId.get(id) as T), ids: [...ids] };
}

/**
 * Truncates CEL expression for table display
 * @param expression - The CEL expression
 * @param maxLength - Maximum length (default 60)
 * @returns Truncated expression with ellipsis if needed
 */
export function truncateCELExpression(expression: string, maxLength: number = 60): string {
	if (!expression) return "";
	if (expression.length <= maxLength) return expression;
	return expression.substring(0, maxLength) + "...";
}

/**
 * Validates a provider/model combination
 * @param provider - The provider name
 * @param model - The model name (optional)
 * @returns Error message if invalid, empty string if valid
 */
export function validateProviderModel(provider: string, _model?: string): string {
	if (!provider || provider.trim().length === 0) {
		return i18n.t("validation.providerIsRequired");
	}
	return "";
}

/**
 * Generates a CSS class for priority badge color
 * @returns CSS class name for styling
 */
export function getPriorityBadgeClass(): string {
	return "bg-primary text-primary-foreground";
}

/**
 * Gets a user-friendly CEL operator from the expression. Developer-facing: nothing renders these labels.
 * @param expression - The CEL expression
 * @returns Array of detected operators
 */
export function detectCELOperators(expression: string): string[] {
	const operators: string[] = [];
	if (!expression) return operators;

	// Common CEL operators
	const operatorPatterns = [
		{ regex: /==/, label: "Equals" }, // i18n-ignore
		{ regex: /!=/, label: "Not equals" }, // i18n-ignore
		{ regex: />=/, label: "Greater than or equal" }, // i18n-ignore
		{ regex: /<=/, label: "Less than or equal" }, // i18n-ignore
		{ regex: />/, label: "Greater than" }, // i18n-ignore
		{ regex: /</, label: "Less than" }, // i18n-ignore
		{ regex: /&&/, label: "AND" }, // i18n-ignore
		{ regex: /\|\|/, label: "OR" }, // i18n-ignore
		{ regex: /!(?!=)/, label: "NOT" }, // i18n-ignore
		{ regex: /in\s/, label: "IN" }, // i18n-ignore
		{ regex: /.matches\(/, label: "Regex" }, // i18n-ignore
		{ regex: /.startsWith\(/, label: "StartsWith" }, // i18n-ignore
		{ regex: /.contains\(/, label: "Contains" }, // i18n-ignore
		{ regex: /.endsWith\(/, label: "EndsWith" }, // i18n-ignore
	];

	operatorPatterns.forEach(({ regex, label }) => {
		if (regex.test(expression) && !operators.includes(label)) {
			operators.push(label);
		}
	});

	return operators;
}

/** Upper bound for a rule's TTFT deadline, mirroring the API's validation. */
export const MAX_TTFT_TIMEOUT_MS = 300000;

/**
 * Parses the TTFT deadline input. Empty means "off" (undefined); anything else must be a
 * whole number of milliseconds in 1..MAX_TTFT_TIMEOUT_MS, or null is returned.
 */
export function parseTTFTTimeoutInput(raw: string): number | undefined | null {
	const trimmed = (raw ?? "").trim();
	if (trimmed === "") {
		return undefined;
	}
	if (!/^\d+$/.test(trimmed)) {
		return null;
	}
	const ms = Number(trimmed);
	return ms >= 1 && ms <= MAX_TTFT_TIMEOUT_MS ? ms : null;
}

/**
 * The API stores the TTFT deadline per target while the form edits a single value. Returns
 * the shared deadline, or mixed=true when targets disagree (set vs unset counts as a disagreement).
 */
export function summarizeTargetsTTFT(targets: RoutingTarget[] | undefined): { ms: number | undefined; mixed: boolean } {
	const values = (targets ?? []).map((t) => t.ttft_timeout_ms || undefined);
	const first = values[0];
	const mixed = values.some((v) => v !== first);
	return { ms: mixed ? undefined : first, mixed };
}

/** Display text for a rule's TTFT deadline, derived from its targets. */
export function formatTargetsTTFT(targets: RoutingTarget[] | undefined): string {
	const { ms, mixed } = summarizeTargetsTTFT(targets);
	if (mixed) {
		return "Mixed";
	}
	return ms ? `${ms} ms (streaming)` : "Off";
}

/**
 * Deadline to send for one target. While the input still equals what was loaded, a target keeps
 * its own stored deadline (so mixed values survive an unrelated save) and a target added since
 * inherits the shared one. Once the user edits the input, the new value applies to every target,
 * including an empty one: `edited` separates "cleared on purpose" from "never touched".
 */
export function resolveTargetTTFTMs(input: string, loadedInput: string, targetMs?: number | null, edited = false): number {
	if (!edited && input === loadedInput) {
		return targetMs || Number(loadedInput) || 0;
	}
	return parseTTFTTimeoutInput(input) ?? 0;
}

/**
 * What the TTFT field should convey. While untouched it reflects the loaded targets, so a mixed
 * rule reads "Mixed" and still counts as having a deadline; once edited it follows the input.
 */
export function summarizeTTFTDisplay(
	targets: RoutingTarget[] | undefined,
	input: string,
	edited: boolean,
): { mixed: boolean; active: boolean } {
	if (edited) {
		return { mixed: false, active: typeof parseTTFTTimeoutInput(input) === "number" };
	}
	const { ms, mixed } = summarizeTargetsTTFT(targets);
	return { mixed, active: mixed || ms !== undefined };
}