/**
 * Scope: render-only structural checks via `renderToStaticMarkup`. No React lifecycle runs here, so
 * add/remove clicks are NOT exercised at this level — those go through the Playwright spec in
 * `tests/e2e/features/virtual-keys/virtual-keys-listed-models.spec.ts`. The picker and the rules the
 * editor leans on are pure
 * and covered by `lib/utils/listedModels.test.ts`. Keep additions here to markup, ordering and testids.
 */
import type { RoutingRule } from "@/lib/types/routingRules";
import type { ListedModelsEntry, ListedProviderConfig, ListedModelsScope } from "@/lib/utils/listedModels";
import { MAX_LISTED_MODELS_ENTRIES } from "@/lib/utils/listedModels";
import i18n from "@/lib/i18n";
import { renderToStaticMarkup } from "react-dom/server";
import { beforeAll, describe, expect, it, vi } from "vitest";
import { ListedModelsEditor } from "./listedModelsEditor";

vi.mock("@/lib/store/apis/providersApi", () => ({
	useGetProvidersQuery: () => ({ data: [], isLoading: false, isError: false }),
	useGetModelsQuery: () => ({ data: { models: [], total: 0 }, isLoading: false, isFetching: false, isError: false }),
	useGetBaseModelsQuery: () => ({ data: { models: [], total: 0 }, isLoading: false, isFetching: false, isError: false }),
}));

beforeAll(async () => {
	await i18n.changeLanguage("en");
});

function rule(overrides: Partial<RoutingRule>): RoutingRule {
	return {
		id: "rule-id",
		name: "coding",
		description: "",
		cel_expression: "",
		targets: [],
		scope: "global",
		priority: 0,
		enabled: true,
		chain_rule: false,
		created_at: "",
		updated_at: "",
		...overrides,
	};
}

const scope: ListedModelsScope = { virtualKeyId: "vk-1" };
const providerConfigs: ListedProviderConfig[] = [{ provider: "openai", allowed_models: ["*"] }];

function renderEditor(value: ListedModelsEntry[] | undefined, overrides: Partial<Parameters<typeof ListedModelsEditor>[0]> = {}) {
	return renderToStaticMarkup(
		<ListedModelsEditor
			value={value}
			onChange={() => {}}
			providerConfigs={providerConfigs}
			rules={[rule({})]}
			scope={scope}
			allowAllProviders={false}
			{...overrides}
		/>,
	);
}

describe("ListedModelsEditor", () => {
	it("renders one removable row per entry, in order", () => {
		const html = renderEditor([
			{ provider: "openai", model: "gpt-5.4", routing_rule: "" },
			{ provider: "", model: "", routing_rule: "coding" },
		]);

		expect(html).toContain('data-testid="vk-listed-models-row-0"');
		expect(html).toContain('data-testid="vk-listed-models-row-1"');
		expect(html).toContain('data-testid="vk-listed-models-remove-0"');
		expect(html).toContain('data-testid="vk-listed-models-remove-1"');
		expect(html).toContain("openai/gpt-5.4");
		expect(html).toContain("coding");
		expect(html.indexOf("openai/gpt-5.4")).toBeLessThan(html.indexOf('data-testid="vk-listed-models-row-1"'));
	});

	it("renders an empty block as deny-every-model, with the switch on", () => {
		const html = renderEditor([]);

		expect(html).toContain('data-testid="vk-listed-models-empty"');
		expect(html).not.toContain('data-testid="vk-listed-models-row-0"');
		expect(html).toContain('data-testid="vk-listed-models-switch"');
		expect(html).toMatch(/aria-checked="true"[^>]*data-testid="vk-listed-models-switch"/);
		expect(html).toContain('data-testid="vk-listed-models-add-model"');
		expect(html).toContain('data-testid="vk-listed-models-add-rule"');
	});

	it("treats an absent block as the feature off, with no add controls", () => {
		const html = renderEditor(undefined);

		expect(html).toContain('data-testid="vk-listed-models-off-hint"');
		expect(html).not.toContain('data-testid="vk-listed-models-add-model"');
		expect(html).toMatch(/aria-checked="false"[^>]*data-testid="vk-listed-models-switch"/);
	});

	it("renders the server's refusals", () => {
		const html = renderEditor([], { errors: ['listed_models entry 0 names routing rule "coding", which does not exist'] });

		expect(html).toContain('data-testid="vk-listed-models-errors"');
		expect(html).toContain("which does not exist");
	});

	it("stops offering new entries at the cap", () => {
		const entries: ListedModelsEntry[] = Array.from({ length: MAX_LISTED_MODELS_ENTRIES }, (_, i) => ({
			provider: "openai",
			model: `model-${i}`,
			routing_rule: "",
		}));
		const html = renderEditor(entries);

		expect(html).toMatch(/data-testid="vk-listed-models-add-model"[^>]*disabled/);
		expect(html).toContain('data-testid="vk-listed-models-cap"');
	});
});