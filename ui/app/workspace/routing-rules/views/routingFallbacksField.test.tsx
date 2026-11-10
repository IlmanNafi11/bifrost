/**
 * Scope: render-only structural checks via `renderToStaticMarkup`. No React lifecycle runs here, so
 * dnd-kit interactions (onDragOver → onReorder, onDragEnd canceled → onCancel replay) are NOT
 * exercised at this level — those live paths are covered by the Playwright specs in
 * `tests/e2e/features/routing-rules/`, which drive real pointer/keyboard gestures through
 * DragDropProvider. Pure reorder/reconcile logic for these callbacks lives in
 * `lib/utils/routingRules.test.ts`. Keep additions here to markup/ordering/testids; move behavior
 * assertions to e2e or the pure-helper suite.
 */
import i18n from "@/lib/i18n";
import type { RoutingFallbackFormData } from "@/lib/types/routingRules";
import { renderToStaticMarkup } from "react-dom/server";
import { beforeAll, describe, expect, it, vi } from "vitest";
import { RoutingFallbacksField } from "./routingFallbacksField";

vi.mock("@/lib/store/apis/providersApi", () => ({
	useGetProvidersQuery: () => ({ data: [] }),
	useGetModelsQuery: () => ({ data: { models: [], pagination: {} }, isLoading: false }),
	useGetBaseModelsQuery: () => ({ data: [], isLoading: false }),
}));

beforeAll(async () => {
	await i18n.changeLanguage("en");
});

function renderField(rows: { fallbacks: RoutingFallbackFormData[]; ids: string[] }) {
	return renderToStaticMarkup(
		<RoutingFallbacksField
			rows={rows}
			referencedProviderOptions={[]}
			allKeys={[]}
			onAdd={() => {}}
			onUpdate={() => {}}
			onRemove={() => {}}
			onReorder={() => {}}
			onCancel={() => {}}
		/>,
	);
}

const twoFallbacks: RoutingFallbackFormData[] = [
	{ provider: "openai", model: "", key_id: "" },
	{ provider: "anthropic", model: "", key_id: "" },
];

describe("RoutingFallbacksField", () => {
	it("renders one draggable handle per fallback row", () => {
		const html = renderField({ fallbacks: twoFallbacks, ids: ["id-a", "id-b"] });

		expect(html).toContain('data-testid="routing-fallback-0-handle"');
		expect(html).toContain('data-testid="routing-fallback-1-handle"');
	});

	it("labels each handle with the row it moves", () => {
		const html = renderField({ fallbacks: twoFallbacks, ids: ["id-a", "id-b"] });

		expect(html).toContain('aria-label="Drag fallback 1 to reorder"');
		expect(html).toContain('aria-label="Drag fallback 2 to reorder"');
	});

	it("renders rows in the order it is given", () => {
		const html = renderField({
			fallbacks: [twoFallbacks[1], twoFallbacks[0]],
			ids: ["id-b", "id-a"],
		});

		const rowIndexes = [...html.matchAll(/data-testid="routing-fallback-(\d+)"[^>]*>/g)].map((match) => match[1]);

		expect(rowIndexes).toEqual(["0", "1"]);
		// Providers render by display name, so order is read off the labels in row order.
		expect(html.indexOf("Anthropic")).toBeLessThan(html.indexOf("OpenAI"));
	});

	it("keeps the add button and the ordering hint", () => {
		const html = renderField({ fallbacks: twoFallbacks, ids: ["id-a", "id-b"] });

		expect(html).toContain("Add Fallback");
		expect(html).toContain("Fallbacks will be used in the order they are defined");
	});

	it("shows the empty state when there are no fallbacks", () => {
		const html = renderField({ fallbacks: [], ids: [] });

		expect(html).toContain("No fallbacks configured");
		expect(html).not.toContain("-handle");
	});
});