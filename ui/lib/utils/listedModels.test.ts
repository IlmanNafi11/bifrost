import type { RoutingRule } from "@/lib/types/routingRules";
import { describe, expect, it } from "vitest";
import {
	defaultListedProvider,
	isModelOffered,
	listedEntryFromModel,
	listedEntryFromRule,
	listedEntryKey,
	listedModelPickerProviders,
	listedModelsFromVirtualKey,
	listedModelsPayload,
	listedProviderOptions,
	listedRuleOptions,
	listingRuleOptions,
	type ListedModelsScope,
	MAX_LISTED_MODELS_ENTRIES,
	modelAllowedBy,
	routingRuleScopeMatches,
	validateListedModelsShape,
	validateListedModels,
} from "./listedModels";

const scope: ListedModelsScope = { virtualKeyId: "vk-1", teamId: "team-1", customerId: "cust-1" };

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

describe("modelAllowedBy", () => {
	it("treats a wildcard allow list as allowing every model", () => {
		expect(modelAllowedBy("gpt-5.4", { allowed_models: ["*"] })).toBe(true);
	});

	it("denies every model when the allow list is empty", () => {
		expect(modelAllowedBy("gpt-5.4", { allowed_models: [] })).toBe(false);
		expect(modelAllowedBy("gpt-5.4", {})).toBe(false);
	});

	it("matches exact names case-insensitively", () => {
		expect(modelAllowedBy("GPT-5.4", { allowed_models: ["gpt-5.4"] })).toBe(true);
		expect(modelAllowedBy("gpt-5.4-mini", { allowed_models: ["gpt-5.4"] })).toBe(false);
	});

	it("matches regex: entries as a full, case-insensitive match", () => {
		const config = { allowed_models: ["regex:^glm-.*"] };
		expect(modelAllowedBy("GLM-5.3", config)).toBe(true);
		expect(modelAllowedBy("glm", config)).toBe(false);
	});

	it("lets the block list win over the allow list", () => {
		expect(modelAllowedBy("gpt-5.4", { allowed_models: ["*"], blacklisted_models: ["gpt-5.4"] })).toBe(false);
		expect(modelAllowedBy("gpt-5.4", { allowed_models: ["*"], blacklisted_models: ["*"] })).toBe(false);
	});
});

describe("routingRuleScopeMatches", () => {
	it("matches a global rule for every key", () => {
		expect(routingRuleScopeMatches(rule({ scope: "global" }), scope)).toBe(true);
		expect(routingRuleScopeMatches(rule({ scope: "global" }), {})).toBe(true);
	});

	it("treats an empty scope as global", () => {
		expect(routingRuleScopeMatches(rule({ scope: "" as RoutingRule["scope"] }), scope)).toBe(true);
	});

	it("matches a key, team or customer rule only against the same id", () => {
		expect(routingRuleScopeMatches(rule({ scope: "virtual_key", scope_id: "vk-1" }), scope)).toBe(true);
		expect(routingRuleScopeMatches(rule({ scope: "virtual_key", scope_id: "vk-2" }), scope)).toBe(false);
		expect(routingRuleScopeMatches(rule({ scope: "team", scope_id: "team-1" }), scope)).toBe(true);
		expect(routingRuleScopeMatches(rule({ scope: "team", scope_id: "team-2" }), scope)).toBe(false);
		expect(routingRuleScopeMatches(rule({ scope: "customer", scope_id: "cust-1" }), scope)).toBe(true);
	});

	it("never matches a scoped rule when the key has no such owner yet", () => {
		expect(routingRuleScopeMatches(rule({ scope: "team", scope_id: "team-1" }), { virtualKeyId: "vk-new" })).toBe(false);
	});

	it("never matches a user-scoped rule, which no key request carries", () => {
		expect(routingRuleScopeMatches(rule({ scope: "user", scope_id: "user-1" }), scope)).toBe(false);
	});
});

describe("listingRuleOptions", () => {
	it("offers enabled rules in scope, by name", () => {
		const options = listingRuleOptions([rule({ name: "coding" }), rule({ name: "fast", scope: "team", scope_id: "team-1" })], scope);
		expect(options.map((option) => option.name)).toEqual(["coding", "fast"]);
	});

	it("drops a rule that is out of scope or disabled", () => {
		const options = listingRuleOptions(
			[rule({ name: "other-team", scope: "team", scope_id: "team-9" }), rule({ name: "off", enabled: false }), rule({ name: "coding" })],
			scope,
		);
		expect(options.map((option) => option.name)).toEqual(["coding"]);
	});

	it("drops a name that two in-scope rules answer to, which a listing cannot promise", () => {
		const options = listingRuleOptions(
			[rule({ id: "a", name: "coding" }), rule({ id: "b", name: "coding", scope: "virtual_key", scope_id: "vk-1" })],
			scope,
		);
		expect(options).toEqual([]);
	});

	it("keeps a name when only one of two same-name rules is in scope", () => {
		const options = listingRuleOptions(
			[rule({ id: "a", name: "coding" }), rule({ id: "b", name: "coding", scope: "team", scope_id: "team-9" })],
			scope,
		);
		expect(options.map((option) => option.id)).toEqual(["a"]);
	});
});

describe("listedModelPickerProviders", () => {
	it("offers a provider whose allow list names models", () => {
		expect(listedModelPickerProviders([{ provider: "openai", allowed_models: ["gpt-5.4"] }])).toEqual(["openai"]);
	});

	it("offers a provider whose allow list is the wildcard", () => {
		expect(listedModelPickerProviders([{ provider: "openai", allowed_models: ["*"] }])).toEqual(["openai"]);
	});

	it("skips a provider that allows no model at all", () => {
		expect(listedModelPickerProviders([{ provider: "openai", allowed_models: [] }, { provider: "anthropic" }])).toEqual([]);
	});

	it("skips a provider whose block list hides every model", () => {
		expect(
			listedModelPickerProviders([
				{ provider: "openai", allowed_models: ["*"] },
				{ provider: "anthropic", allowed_models: ["*"], blacklisted_models: ["*"] },
			]),
		).toEqual(["openai"]);
	});
});

describe("isModelOffered", () => {
	const configs = [
		{ provider: "openai", allowed_models: ["gpt-5.4", "regex:^o3-.*"] },
		{ provider: "anthropic", allowed_models: [] },
	];

	it("offers a model the provider's allow list names", () => {
		expect(isModelOffered("openai", "gpt-5.4", configs)).toBe(true);
		expect(isModelOffered("openai", "o3-mini", configs)).toBe(true);
	});

	it("refuses a model outside the allow list, on an unconfigured provider, or on a dead provider", () => {
		expect(isModelOffered("openai", "gpt-4o", configs)).toBe(false);
		expect(isModelOffered("groq", "llama-3", configs)).toBe(false);
		expect(isModelOffered("anthropic", "claude-opus", configs)).toBe(false);
	});
});

describe("entry helpers", () => {
	it("builds a model entry and a rule entry, and keys them distinctly", () => {
		expect(listedEntryFromModel("openai", "gpt-5.4")).toEqual({ provider: "openai", model: "gpt-5.4", routing_rule: "" });
		expect(listedEntryFromRule("coding")).toEqual({ provider: "", model: "", routing_rule: "coding" });
		expect(listedEntryKey(listedEntryFromModel("openai", "gpt-5.4"))).toBe("model:openai/gpt-5.4");
		expect(listedEntryKey(listedEntryFromRule("coding"))).toBe("rule:coding");
	});
});

describe("editor option helpers", () => {
	const configs = [
		{ provider: "openai", allowed_models: ["*"] },
		{ provider: "anthropic", allowed_models: [] },
	];

	it("offers the rules the listing would show, never a user-scoped one", () => {
		expect(listedRuleOptions([rule({ name: "coding" }), rule({ name: "own", scope: "user", scope_id: "u-1" })], scope)).toEqual([
			{ id: "rule-id", name: "coding" },
		]);
	});

	it("starts the model picker on the first provider that allows something", () => {
		expect(defaultListedProvider(configs, false)).toBe("openai");
		expect(defaultListedProvider([{ provider: "anthropic", allowed_models: [] }], false)).toBe("");
	});

	it("lets allow_all_providers offer a provider with no config of its own", () => {
		const noneAllowing = [{ provider: "groq", allowed_models: [] }];
		expect(defaultListedProvider(noneAllowing, false)).toBe("");
		expect(defaultListedProvider(noneAllowing, true)).toBe("groq");
	});

	it("lists offered providers plus a provider the block already names outside any config", () => {
		expect(listedProviderOptions(configs, [], false)).toEqual(["openai"]);
		expect(listedProviderOptions(configs, [listedEntryFromModel("mistral", "m-large")], false)).toEqual(["openai"]);
		expect(listedProviderOptions(configs, [listedEntryFromModel("mistral", "m-large")], true)).toEqual(["openai", "mistral"]);
		expect(listedProviderOptions(configs, [listedEntryFromModel("anthropic", "claude-opus")], false)).toEqual(["openai", "anthropic"]);
	});
});

describe("listedModelsFromVirtualKey", () => {
	it("maps a stored block to form rows, preserving the empty block", () => {
		expect(listedModelsFromVirtualKey({ listed_models: [{ provider: "openai", model: "gpt-5.4" }, { routing_rule: "coding" }] })).toEqual([
			{ provider: "openai", model: "gpt-5.4", routing_rule: "" },
			{ provider: "", model: "", routing_rule: "coding" },
		]);
	});

	it("keeps an explicit empty block distinct from an absent one", () => {
		expect(listedModelsFromVirtualKey({ listed_models: [] })).toEqual([]);
		expect(listedModelsFromVirtualKey({})).toBeUndefined();
	});

	it("never reports a block the list endpoint omitted", () => {
		expect(listedModelsFromVirtualKey({ listed_models: undefined })).toBeUndefined();
	});
});

describe("listedModelsPayload", () => {
	it("sends only the form each entry uses", () => {
		expect(
			listedModelsPayload([
				{ provider: "openai", model: "gpt-5.4", routing_rule: "" },
				{ provider: "", model: "", routing_rule: "coding" },
			]),
		).toEqual([{ provider: "openai", model: "gpt-5.4" }, { routing_rule: "coding" }]);
	});

	it("keeps an empty block as an empty array, not as an absent field", () => {
		expect(listedModelsPayload([])).toEqual([]);
	});
});

describe("validateListedModels", () => {
	const providerConfigs = [
		{ provider: "openai", allowed_models: ["gpt-5.4"] },
		{ provider: "anthropic", allowed_models: ["*"], blacklisted_models: ["claude-opus"] },
	];
	const rules = [rule({ id: "a", name: "coding" }), rule({ id: "b", name: "off", enabled: false })];
	const base = { providerConfigs, rules, scope, allowAllProviders: false };

	it("accepts a model entry inside allowed_models and a rule in scope", () => {
		expect(
			validateListedModels(
				[
					{ provider: "openai", model: "gpt-5.4", routing_rule: "" },
					{ provider: "", model: "", routing_rule: "coding" },
				],
				base,
			),
		).toBeUndefined();
	});

	it("refuses an entry that names neither a model nor a rule", () => {
		expect(validateListedModels([{ provider: "", model: "", routing_rule: "" }], base)).toBeTruthy();
	});

	it("refuses an entry that names both", () => {
		expect(validateListedModels([{ provider: "openai", model: "gpt-5.4", routing_rule: "coding" }], base)).toBeTruthy();
	});

	it("refuses a model entry that sets only one of provider and model", () => {
		expect(validateListedModels([{ provider: "openai", model: "", routing_rule: "" }], base)).toBeTruthy();
	});

	it("refuses the wildcard as a model", () => {
		expect(validateListedModels([{ provider: "anthropic", model: "*", routing_rule: "" }], base)).toBeTruthy();
	});

	it("refuses a model on a provider the key does not configure", () => {
		expect(validateListedModels([{ provider: "groq", model: "llama-3", routing_rule: "" }], base)).toBeTruthy();
	});

	it("refuses a model the provider allows but its block list hides", () => {
		expect(validateListedModels([{ provider: "anthropic", model: "claude-opus", routing_rule: "" }], base)).toBeTruthy();
	});

	it("refuses a model outside allowed_models", () => {
		expect(validateListedModels([{ provider: "openai", model: "gpt-4o", routing_rule: "" }], base)).toBeTruthy();
	});

	it("accepts a model on any provider when the key allows all providers", () => {
		expect(
			validateListedModels([{ provider: "groq", model: "llama-3", routing_rule: "" }], { ...base, allowAllProviders: true }),
		).toBeUndefined();
	});

	it("refuses a duplicate model and a duplicate rule", () => {
		const model = { provider: "openai", model: "gpt-5.4", routing_rule: "" };
		expect(validateListedModels([model, { ...model }], base)).toBeTruthy();
		const ruleEntry = { provider: "", model: "", routing_rule: "coding" };
		expect(validateListedModels([ruleEntry, { ...ruleEntry }], base)).toBeTruthy();
	});

	it("refuses an unknown, disabled, ambiguous or out-of-scope rule", () => {
		expect(validateListedModels([{ provider: "", model: "", routing_rule: "nope" }], base)).toBeTruthy();
		expect(validateListedModels([{ provider: "", model: "", routing_rule: "off" }], base)).toBeTruthy();
		expect(
			validateListedModels([{ provider: "", model: "", routing_rule: "coding" }], {
				...base,
				rules: [rule({ name: "coding" }), rule({ id: "z", name: "coding" })],
			}),
		).toBeTruthy();
		expect(
			validateListedModels([{ provider: "", model: "", routing_rule: "coding" }], {
				...base,
				rules: [rule({ name: "coding", scope: "team", scope_id: "team-9" })],
			}),
		).toBeTruthy();
	});

	it("refuses more entries than the server accepts", () => {
		const entries = Array.from({ length: MAX_LISTED_MODELS_ENTRIES + 1 }, (_, i) => ({
			provider: "openai",
			model: `m-${i}`,
			routing_rule: "",
		}));
		expect(validateListedModels(entries, { ...base, providerConfigs: [{ provider: "openai", allowed_models: ["*"] }] })).toBeTruthy();
	});

	it("accepts an empty block, which the server reads as deny-every-model", () => {
		expect(validateListedModels([], base)).toBeUndefined();
	});
});

describe("validateListedModelsShape", () => {
	it("accepts a model entry and a rule entry", () => {
		expect(validateListedModelsShape([{ provider: "openai", model: "gpt-5.4", routing_rule: "" }])).toBeUndefined();
		expect(validateListedModelsShape([{ provider: "", model: "", routing_rule: "coding" }])).toBeUndefined();
	});

	it("refuses an entry that names neither, both, half a model, or the wildcard", () => {
		expect(validateListedModelsShape([{ provider: "", model: "", routing_rule: "" }])).toBeTruthy();
		expect(validateListedModelsShape([{ provider: "openai", model: "gpt-5.4", routing_rule: "coding" }])).toBeTruthy();
		expect(validateListedModelsShape([{ provider: "openai", model: "", routing_rule: "" }])).toBeTruthy();
		expect(validateListedModelsShape([{ provider: "*", model: "gpt-5.4", routing_rule: "" }])).toBeTruthy();
	});

	it("needs no entity lookups, so it can run inside a zod schema", () => {
		expect(validateListedModelsShape([])).toBeUndefined();
	});

	it("refuses a block over the server's cap", () => {
		const entries = Array.from({ length: MAX_LISTED_MODELS_ENTRIES + 1 }, (_, i) => ({
			provider: "openai",
			model: `m-${i}`,
			routing_rule: "",
		}));
		expect(validateListedModelsShape(entries)).toBeTruthy();
	});
});