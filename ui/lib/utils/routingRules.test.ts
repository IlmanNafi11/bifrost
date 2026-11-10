import type { RoutingFallbackFormData } from "@/lib/types/routingRules";
import { describe, expect, it } from "vitest";
import {
	addFallbackRow,
	denormalizeFallback,
	fallbackRowIds,
	type FallbackRows,
	formatTargetsTTFT,
	MAX_TTFT_TIMEOUT_MS,
	newFallbackRowId,
	normalizeFallback,
	parseTTFTTimeoutInput,
	reconcileFallbackIds,
	removeFallbackRow,
	reorderFallbackRows,
	reorderFallbackRowsById,
	reorderFallbacks,
	resolveTargetTTFTMs,
	summarizeTTFTDisplay,
	summarizeTargetsTTFT,
	orderFallbackRowsByIds,
	updateFallbackRow,
} from "./routingRules";

describe("routing fallback wire format", () => {
	it.each([
		["openai/gpt-4o", { provider: "openai", model: "gpt-4o", key_id: "" }],
		["azure/", { provider: "azure", model: "", key_id: "" }],
		["openai/ft:model/org/v2", { provider: "openai", model: "ft:model/org/v2", key_id: "" }],
	] as const)("round-trips %s", (wire, form) => {
		expect(normalizeFallback(wire)).toEqual(form);
		expect(denormalizeFallback(form)).toEqual(wire);
	});

	it("keeps the provider delimiter when an unpinned fallback uses the incoming model", () => {
		expect(denormalizeFallback({ provider: " azure " })).toBe("azure/");
	});

	it("round-trips pinned fallback objects with and without a model", () => {
		for (const wire of [
			{ provider: "openai", model: "gpt-4o", key_id: "key-1" },
			{ provider: "azure", key_id: "key-2" },
		]) {
			expect(denormalizeFallback(normalizeFallback(wire))).toEqual(wire);
		}
	});

	it("returns to valid legacy syntax when a provider-only fallback pin is cleared", () => {
		const form = normalizeFallback({ provider: "azure", key_id: "key-1" });
		expect(denormalizeFallback({ ...form, key_id: "" })).toBe("azure/");
	});
});

describe("TTFT deadline input", () => {
	it("treats an empty input as no deadline", () => {
		expect(parseTTFTTimeoutInput("")).toBeUndefined();
		expect(parseTTFTTimeoutInput("  ")).toBeUndefined();
	});

	it.each([
		["1", 1],
		[" 1500 ", 1500],
		[String(MAX_TTFT_TIMEOUT_MS), MAX_TTFT_TIMEOUT_MS],
	] as const)("accepts %s", (raw, ms) => {
		expect(parseTTFTTimeoutInput(raw)).toBe(ms);
	});

	it.each(["0", "-5", "1.5", "abc", "1e3", String(MAX_TTFT_TIMEOUT_MS + 1)])("rejects %s", (raw) => {
		expect(parseTTFTTimeoutInput(raw)).toBeNull();
	});
});

describe("TTFT deadline on targets", () => {
	it("is off when no target has a deadline", () => {
		expect(summarizeTargetsTTFT([{ weight: 1 }, { weight: 0, ttft_timeout_ms: null }])).toEqual({ ms: undefined, mixed: false });
		expect(formatTargetsTTFT([{ weight: 1 }])).toBe("Off");
	});

	it("uses the shared deadline when every target agrees", () => {
		const targets = [
			{ weight: 0.5, ttft_timeout_ms: 1500 },
			{ weight: 0.5, ttft_timeout_ms: 1500 },
		];
		expect(summarizeTargetsTTFT(targets)).toEqual({ ms: 1500, mixed: false });
		expect(formatTargetsTTFT(targets)).toBe("1500 ms (streaming)");
	});

	it("reports mixed when targets differ, including set vs unset", () => {
		expect(
			summarizeTargetsTTFT([
				{ weight: 0.5, ttft_timeout_ms: 1500 },
				{ weight: 0.5, ttft_timeout_ms: 800 },
			]).mixed,
		).toBe(true);
		expect(summarizeTargetsTTFT([{ weight: 0.5, ttft_timeout_ms: 1500 }, { weight: 0.5 }]).mixed).toBe(true);
		expect(formatTargetsTTFT([{ weight: 0.5, ttft_timeout_ms: 1500 }, { weight: 0.5 }])).toBe("Mixed");
	});
});

describe("resolveTargetTTFTMs", () => {
	it("keeps each target's own deadline while the input is unchanged (mixed rule)", () => {
		expect(resolveTargetTTFTMs("", "", 1500)).toBe(1500);
		expect(resolveTargetTTFTMs("", "", 800)).toBe(800);
		expect(resolveTargetTTFTMs("", "", undefined)).toBe(0);
	});

	it("gives a target added to a uniform rule the shared deadline", () => {
		expect(resolveTargetTTFTMs("1500", "1500", 1500)).toBe(1500);
		expect(resolveTargetTTFTMs("1500", "1500", undefined)).toBe(1500);
	});

	it("applies an edited value to every target", () => {
		expect(resolveTargetTTFTMs("900", "", 1500)).toBe(900);
		expect(resolveTargetTTFTMs("900", "1500", undefined)).toBe(900);
	});

	it("clears every target when the loaded value is emptied", () => {
		expect(resolveTargetTTFTMs("", "1500", 1500)).toBe(0);
	});

	it("clears every target when the user empties a mixed field", () => {
		expect(resolveTargetTTFTMs("", "", 1500, true)).toBe(0);
		expect(resolveTargetTTFTMs("", "", 800, true)).toBe(0);
	});

	it("applies the shared value when an edit returns to the loaded text", () => {
		expect(resolveTargetTTFTMs("1500", "1500", 800, true)).toBe(1500);
	});
});

describe("summarizeTTFTDisplay", () => {
	const mixed = [{ weight: 0.5, ttft_timeout_ms: 1500 }, { weight: 0.5 }];
	const uniform = [
		{ weight: 0.5, ttft_timeout_ms: 1500 },
		{ weight: 0.5, ttft_timeout_ms: 1500 },
	];
	const off = [{ weight: 1 }];

	it("flags an untouched mixed rule as mixed and active", () => {
		expect(summarizeTTFTDisplay(mixed, "", false)).toEqual({ mixed: true, active: true });
	});

	it("is active but not mixed for an untouched uniform rule", () => {
		expect(summarizeTTFTDisplay(uniform, "1500", false)).toEqual({ mixed: false, active: true });
	});

	it("is inactive for an untouched rule with no deadlines, and for a new rule", () => {
		expect(summarizeTTFTDisplay(off, "", false)).toEqual({ mixed: false, active: false });
		expect(summarizeTTFTDisplay(undefined, "", false)).toEqual({ mixed: false, active: false });
	});

	it("follows the input once the user has edited it", () => {
		expect(summarizeTTFTDisplay(mixed, "", true)).toEqual({ mixed: false, active: false });
		expect(summarizeTTFTDisplay(mixed, "900", true)).toEqual({ mixed: false, active: true });
		expect(summarizeTTFTDisplay(uniform, "abc", true)).toEqual({ mixed: false, active: false });
	});
});

describe("reorderFallbacks", () => {
	const items = ["a", "b", "c", "d"];

	it("moves an item down and returns a new array", () => {
		const input = [...items];
		const result = reorderFallbacks(input, 0, 2);

		expect(result).toEqual(["b", "c", "a", "d"]);
		expect(input).toEqual(items);
		expect(result).not.toBe(input);
	});

	it("moves an item up", () => {
		const input = [...items];
		const result = reorderFallbacks(input, 3, 1);

		expect(result).toEqual(["a", "d", "b", "c"]);
		expect(input).toEqual(items);
	});

	it("is a no-op that still returns a copy when source equals target", () => {
		const input = [...items];
		const result = reorderFallbacks(input, 2, 2);

		expect(result).toEqual(items);
		expect(result).not.toBe(input);
	});

	it.each([
		[-1, 1],
		[0, 4],
		[9, 0],
		[0, -1],
	] as const)("returns an unchanged copy for out-of-bounds %s -> %s", (from, to) => {
		const input = [...items];
		const result = reorderFallbacks(input, from, to);

		expect(result).toEqual(items);
		expect(result).not.toBe(input);
	});

	it("handles empty and single-element arrays", () => {
		const empty: string[] = [];
		expect(reorderFallbacks(empty, 0, 0)).toEqual([]);
		expect(reorderFallbacks(empty, -1, 1)).toEqual([]);
		expect(reorderFallbacks(["only"], 0, 0)).toEqual(["only"]);
		expect(reorderFallbacks(["only"], 0, 3)).toEqual(["only"]);
	});
});

describe("fallback row identity", () => {
	it("generates a distinct, non-empty id per row", () => {
		const ids = new Set(Array.from({ length: 20 }, () => newFallbackRowId()));

		expect(ids.size).toBe(20);
		for (const id of ids) {
			expect(id.length).toBeGreaterThan(0);
		}
	});

	it("reorders fallbacks and their ids in lockstep", () => {
		const rows = { fallbacks: ["a", "b", "c"], ids: ["id-a", "id-b", "id-c"] };

		const result = reorderFallbackRows(rows, 0, 2);

		expect(result.fallbacks).toEqual(["b", "c", "a"]);
		expect(result.ids).toEqual(["id-b", "id-c", "id-a"]);
		expect(rows.fallbacks).toEqual(["a", "b", "c"]);
		expect(rows.ids).toEqual(["id-a", "id-b", "id-c"]);
	});

	it("keeps both arrays unchanged for an out-of-bounds move", () => {
		const result = reorderFallbackRows({ fallbacks: ["a", "b"], ids: ["id-a", "id-b"] }, 0, 5);

		expect(result.fallbacks).toEqual(["a", "b"]);
		expect(result.ids).toEqual(["id-a", "id-b"]);
	});

	it("gives an added row its own id without disturbing the existing ones", () => {
		const result = addFallbackRow({ fallbacks: ["a"], ids: ["id-a"] }, "b");

		expect(result.fallbacks).toEqual(["a", "b"]);
		expect(result.ids).toHaveLength(2);
		expect(result.ids[0]).toBe("id-a");
		expect(result.ids[1]).not.toBe("id-a");
	});

	it("drops the removed row's id and keeps the rest aligned", () => {
		const result = removeFallbackRow({ fallbacks: ["a", "b", "c"], ids: ["id-a", "id-b", "id-c"] }, 1);

		expect(result.fallbacks).toEqual(["a", "c"]);
		expect(result.ids).toEqual(["id-a", "id-c"]);
	});

	it("leaves the rows untouched when removing an out-of-range index", () => {
		const rows = { fallbacks: ["a", "b"], ids: ["id-a", "id-b"] };

		expect(removeFallbackRow(rows, -1)).toBe(rows);
		expect(removeFallbackRow(rows, 2)).toBe(rows);
	});

	it("leaves ids untouched when a row's fields change", () => {
		const rows: FallbackRows<Partial<RoutingFallbackFormData>> = {
			fallbacks: [{ provider: "openai" }, { provider: "anthropic" }],
			ids: ["id-a", "id-b"],
		};

		const result = updateFallbackRow(rows, 1, { model: "claude" });

		expect(result.fallbacks).toEqual([{ provider: "openai" }, { provider: "anthropic", model: "claude" }]);
		expect(result.ids).toEqual(["id-a", "id-b"]);
	});

	it("keeps ids and fallbacks the same length through every mutation", () => {
		const start = { fallbacks: ["a", "b", "c"], ids: ["id-a", "id-b", "id-c"] };
		const steps = [
			addFallbackRow(start, "d"),
			removeFallbackRow(start, 0),
			reorderFallbackRows(start, 2, 0),
			updateFallbackRow(start, 1, "B"),
		];

		for (const step of steps) {
			expect(step.ids).toHaveLength(step.fallbacks.length);
			expect(new Set(step.ids).size).toBe(step.ids.length);
		}
	});

	it("builds a fresh id per fallback when loading a stored rule", () => {
		const ids = fallbackRowIds(2);

		expect(ids).toHaveLength(2);
		expect(ids[0]).not.toBe(ids[1]);
		expect(fallbackRowIds(0)).toEqual([]);
	});
});

describe("reconcileFallbackIds", () => {
	it("pads a short id list and keeps the ids already in place", () => {
		const reconciled = reconcileFallbackIds(["id-a"], 3);

		expect(reconciled).toHaveLength(3);
		expect(reconciled[0]).toBe("id-a");
		expect(new Set(reconciled).size).toBe(3);
	});

	it("truncates a long id list", () => {
		expect(reconcileFallbackIds(["id-a", "id-b", "id-c"], 1)).toEqual(["id-a"]);
		expect(reconcileFallbackIds(["id-a"], 0)).toEqual([]);
	});

	it("returns the same array reference when already aligned", () => {
		const ids = ["id-a", "id-b"];

		expect(reconcileFallbackIds(ids, 2)).toBe(ids);
	});
});

describe("reorderFallbackRowsById", () => {
	const rows = { fallbacks: ["a", "b", "c"], ids: ["id-a", "id-b", "id-c"] };

	it("moves the source row onto the target's index", () => {
		const result = reorderFallbackRowsById(rows, "id-a", "id-c");

		expect(result.fallbacks).toEqual(["b", "c", "a"]);
		expect(result.ids).toEqual(["id-b", "id-c", "id-a"]);
	});

	it("ignores a drag that ends on its own row", () => {
		expect(reorderFallbackRowsById(rows, "id-b", "id-b")).toBe(rows);
	});

	it("ignores ids that are not in the list", () => {
		expect(reorderFallbackRowsById(rows, "id-x", "id-b")).toBe(rows);
		expect(reorderFallbackRowsById(rows, "id-b", "id-x")).toBe(rows);
	});
});

describe("orderFallbackRowsByIds", () => {
	const rows = { fallbacks: ["b", "c", "a"], ids: ["id-b", "id-c", "id-a"] };

	it("replays a captured id order onto the current rows", () => {
		const result = orderFallbackRowsByIds(rows, ["id-a", "id-b", "id-c"]);

		expect(result.fallbacks).toEqual(["a", "b", "c"]);
		expect(result.ids).toEqual(["id-a", "id-b", "id-c"]);
	});

	it("returns the input untouched when the snapshot is already the current order", () => {
		const result = orderFallbackRowsByIds(rows, rows.ids);

		expect(result.fallbacks).toEqual(rows.fallbacks);
		expect(result.ids).toEqual(rows.ids);
	});

	it("ignores a snapshot whose length no longer matches, so a mid-drag add is not undone", () => {
		expect(orderFallbackRowsByIds(rows, ["id-a", "id-b"])).toBe(rows);
	});

	it("ignores a snapshot naming a row that no longer exists", () => {
		expect(orderFallbackRowsByIds(rows, ["id-a", "id-b", "id-gone"])).toBe(rows);
	});
});

describe("fallback row mutation sequence", () => {
	it("keeps ids aligned and unique when mutations are applied cumulatively", () => {
		let rows: FallbackRows<string> = { fallbacks: ["a", "b"], ids: ["id-a", "id-b"] };

		rows = addFallbackRow(rows, "c");
		rows = reorderFallbackRowsById(rows, rows.ids[2], rows.ids[0]);
		rows = removeFallbackRow(rows, 1);
		rows = reorderFallbackRowsById(rows, rows.ids[1], rows.ids[0]);

		expect(rows.ids).toHaveLength(rows.fallbacks.length);
		expect(new Set(rows.ids).size).toBe(rows.ids.length);
		// The sequence above adds "c", moves it to the front, removes index 1 ("a"), then swaps the
		// remaining two: every surviving id must still name the fallback it was minted for.
		expect(rows.fallbacks).toEqual(["b", "c"]);
		const owner = new Map(rows.ids.map((id, index) => [id, rows.fallbacks[index]]));
		expect(owner.get("id-b")).toBe("b");
		expect(owner.get(rows.ids[1])).toBe("c");
	});
});