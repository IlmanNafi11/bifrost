/**
 * Virtual-key `listed_models`: the block that decides what a key sees in `/v1/models` and may call
 * beyond `allowed_models`, with routing-rule entries standing for the models behind the rule.
 *
 * The helpers here mirror the server's rules (`validateListedModelsBody` in
 * `transports/bifrost-http/handlers/governance.go`) so the form can refuse an entry before a round
 * trip, and the picker only offers what the server would accept.
 *
 * Wire shape: `{ provider, model }` for a model entry, `{ routing_rule }` for a rule entry, with
 * the unused keys empty. Absent (`undefined`) leaves the feature off; an empty array denies every
 * model — the same nil-vs-[] split the server draws.
 */
import type { RoutingRule } from "@/lib/types/routingRules";
import { REGEX_ENTRY_PREFIX } from "@/components/modelAccess/utils";

/** The server's cap on entries (`configstoreTables.MaxVirtualKeyListedModelsEntries`). */
export const MAX_LISTED_MODELS_ENTRIES = 100;

const MODEL_WILDCARD = "*";

/** One entry as the API carries it, and as the form holds it. */
export interface ListedModelsEntry {
	provider: string;
	model: string;
	routing_rule: string;
}

/** The part of a provider config the picker and the validator read. */
export interface ListedProviderConfig {
	provider: string;
	allowed_models?: string[] | null;
	blacklisted_models?: string[] | null;
}

/** The owner chain a rule entry has to fall inside, as the key currently stands. */
export interface ListedModelsScope {
	virtualKeyId?: string;
	teamId?: string;
	customerId?: string;
}

/** One selectable routing rule, by name (the listing's id) and id. */
export interface ListingRuleOption {
	id: string;
	name: string;
}

/** Matches one access list entry against a model: exact (case-insensitive) or a regex: full match. */
function entryMatches(entry: string, model: string): boolean {
	if (!entry.startsWith(REGEX_ENTRY_PREFIX)) {
		return entry.toLowerCase() === model.toLowerCase();
	}
	const pattern = entry.slice(REGEX_ENTRY_PREFIX.length);
	if (pattern === "" || pattern === MODEL_WILDCARD) return false;
	try {
		return new RegExp(`^(?:${pattern})$`, "i").test(model);
	} catch {
		// A pattern that does not compile never matches, as on the server.
		return false;
	}
}

/**
 * Whether a provider config's `allowed_models` / `blacklisted_models` permit model: the allow list
 * must name it (or be the wildcard) and the block list must not. An empty or absent allow list
 * allows nothing, as on the server.
 */
export function modelAllowedBy(model: string, config: Pick<ListedProviderConfig, "allowed_models" | "blacklisted_models">): boolean {
	const allowed = config.allowed_models ?? [];
	const blocked = config.blacklisted_models ?? [];
	if (blocked.includes(MODEL_WILDCARD) || blocked.some((entry) => entryMatches(entry, model))) return false;
	return allowed.includes(MODEL_WILDCARD) || allowed.some((entry) => entryMatches(entry, model));
}

/** Whether rule is enabled and scoped somewhere this key's requests reach. */
export function routingRuleScopeMatches(rule: RoutingRule, scope: ListedModelsScope): boolean {
	if (!rule.enabled) return false;
	const ruleScope = rule.scope || "global";
	switch (ruleScope) {
		case "global":
			return true;
		case "virtual_key":
			return !!scope.virtualKeyId && rule.scope_id === scope.virtualKeyId;
		case "team":
			return !!scope.teamId && rule.scope_id === scope.teamId;
		case "customer":
			return !!scope.customerId && rule.scope_id === scope.customerId;
		default:
			// A user-scoped rule belongs to a user a key does not carry, so no request here reaches it.
			return false;
	}
}

/**
 * The rules a listing may name: enabled, in scope, and unambiguous. Two in-scope rules answering to
 * one name cannot both be promised by a listing, so the name is dropped — the server rejects it too.
 */
export function listingRuleOptions(rules: RoutingRule[], scope: ListedModelsScope): ListingRuleOption[] {
	const byName = new Map<string, RoutingRule[]>();
	for (const rule of rules) {
		if (!routingRuleScopeMatches(rule, scope)) continue;
		const candidates = byName.get(rule.name);
		if (candidates) candidates.push(rule);
		else byName.set(rule.name, [rule]);
	}
	const options: ListingRuleOption[] = [];
	for (const [name, candidates] of byName) {
		if (candidates.length === 1) options.push({ id: candidates[0].id, name });
	}
	return options;
}

/**
 * The providers the model picker offers anything from: those whose allow list names a model (or is
 * the wildcard) and whose block list does not hide everything.
 */
export function listedModelPickerProviders(configs: ListedProviderConfig[]): string[] {
	return configs
		.filter((config) => (config.allowed_models?.length ?? 0) > 0 && !(config.blacklisted_models ?? []).includes(MODEL_WILDCARD))
		.map((config) => config.provider);
}

/** Whether the picker may offer provider/model: the provider must be configured and allow the model. */
export function isModelOffered(provider: string, model: string, configs: ListedProviderConfig[]): boolean {
	const config = configs.find((candidate) => candidate.provider === provider);
	if (!config) return false;
	if (!listedModelPickerProviders([config]).includes(provider)) return false;
	return modelAllowedBy(model, config);
}

export function listedEntryFromModel(provider: string, model: string): ListedModelsEntry {
	return { provider, model, routing_rule: "" };
}

export function listedEntryFromRule(routingRule: string): ListedModelsEntry {
	return { provider: "", model: "", routing_rule: routingRule };
}

/** A stable identity for an entry, for de-duplication and as a row key. */
export function listedEntryKey(entry: ListedModelsEntry): string {
	return entry.routing_rule !== "" ? `rule:${entry.routing_rule}` : `model:${entry.provider}/${entry.model}`;
}

/** One entry as a request body and a response carry it: only the form the entry uses. */
export interface ListedModelsWireEntry {
	provider?: string;
	model?: string;
	routing_rule?: string;
}

/**
 * The form's rows for a key's stored block. `undefined` means the key has no block (feature off), and
 * `[]` means it has an empty one (deny every model): the two are kept apart.
 */
export function listedModelsFromVirtualKey(vk: {
	listed_models?: ListedModelsWireEntry[] | null | undefined;
}): ListedModelsEntry[] | undefined {
	if (!vk.listed_models) return undefined;
	return vk.listed_models.map((entry) => ({
		provider: entry.provider ?? "",
		model: entry.model ?? "",
		routing_rule: entry.routing_rule ?? "",
	}));
}

/** The request body's block: only the form each entry uses, empty entries dropped. */
export function listedModelsPayload(entries: ListedModelsEntry[]): ListedModelsWireEntry[] {
	return entries
		.filter((entry) => entry.routing_rule !== "" || entry.provider !== "" || entry.model !== "")
		.map((entry) =>
			entry.routing_rule !== "" ? { routing_rule: entry.routing_rule } : { provider: entry.provider.trim(), model: entry.model.trim() },
		);
}

/** The routing rules a block may name, as the listing shows them: enabled, in scope, unambiguous. */
export function listedRuleOptions(rules: RoutingRule[], scope: ListedModelsScope): ListingRuleOption[] {
	return listingRuleOptions(rules, scope);
}

/**
 * The provider the add-model picker starts on: the first one the block could name at all, so the
 * model search is scoped without an extra pick for the common single-provider key. Empty when no
 * provider offers a model, which leaves the picker disabled.
 */
export function defaultListedProvider(configs: ListedProviderConfig[], allowAllProviders: boolean): string {
	return listedModelPickerProviders(configs)[0] ?? (allowAllProviders ? (configs[0]?.provider ?? "") : "");
}

/** The model picker's provider list: the configured providers that allow something, plus any outside provider the block already names. */
export function listedProviderOptions(configs: ListedProviderConfig[], entries: ListedModelsEntry[], allowAllProviders: boolean): string[] {
	const offered = listedModelPickerProviders(configs);
	const known = new Set(configs.map((config) => config.provider));
	const named = entries
		.map((entry) => entry.provider.trim())
		.filter((provider) => provider !== "" && (known.has(provider) || allowAllProviders));
	const values: string[] = [];
	for (const provider of [...offered, ...named]) {
		if (!values.includes(provider)) values.push(provider);
	}
	return values;
}

export interface ValidateListedModelsInput {
	providerConfigs: ListedProviderConfig[];
	rules: RoutingRule[];
	scope: ListedModelsScope;
	/** The form's allow_all_providers: when true, a model entry may name an unconfigured provider. */
	allowAllProviders: boolean;
}

/** The structural part of the server's validation: cap, one form per entry, no wildcard. */
export function validateListedModelsShape(entries: ListedModelsEntry[]): string | undefined {
	if (entries.length > MAX_LISTED_MODELS_ENTRIES) {
		return `listed_models can have at most ${MAX_LISTED_MODELS_ENTRIES} entries, got ${entries.length}`;
	}
	for (const [i, entry] of entries.entries()) {
		const provider = entry.provider.trim();
		const model = entry.model.trim();
		const ruleName = entry.routing_rule.trim();
		if ((provider !== "" || model !== "") && ruleName !== "") {
			return `listed_models entry ${i} cannot be both a model and a routing rule`;
		}
		if (ruleName !== "") continue;
		if (provider === "" || model === "") {
			return `listed_models entry ${i} must name a model or a routing rule: a model entry needs both provider and model`;
		}
		if (provider === MODEL_WILDCARD || model === MODEL_WILDCARD) {
			return `listed_models entry ${i} cannot be a wildcard: leave the block absent to allow every model`;
		}
	}
	return undefined;
}

/**
 * Validates a block the way the server will, returning an error message or undefined. The messages
 * are the server's, so the same problem reads the same whichever side catches it.
 */
export function validateListedModels(entries: ListedModelsEntry[], input: ValidateListedModelsInput): string | undefined {
	const shapeError = validateListedModelsShape(entries);
	if (shapeError) return shapeError;
	const configs = new Map(input.providerConfigs.map((config) => [config.provider, config]));
	const seen = new Set<string>();
	for (const [i, entry] of entries.entries()) {
		const provider = entry.provider.trim();
		const model = entry.model.trim();
		const ruleName = entry.routing_rule.trim();
		if (ruleName === "") {
			const config = configs.get(provider);
			if (!config) {
				if (!input.allowAllProviders) {
					return `listed_models entry ${i} names provider "${provider}", which this virtual key does not configure`;
				}
			} else if (!modelAllowedBy(model, config)) {
				const blocked =
					(config.blacklisted_models ?? []).some((guess) => entryMatches(guess, model)) ||
					(config.blacklisted_models ?? []).includes(MODEL_WILDCARD);
				return blocked
					? `listed_models entry ${i} names "${model}", which blacklisted_models of provider ${provider} blocks`
					: `listed_models entry ${i} names "${model}", which allowed_models of provider ${provider} does not allow`;
			}
			const key = `model:${provider}/${model}`;
			if (seen.has(key)) return `listed_models entry ${i} repeats ${provider}/${model}`;
			seen.add(key);
			continue;
		}

		const candidates = input.rules.filter((rule) => rule.name === ruleName && routingRuleScopeMatches(rule, input.scope));
		if (candidates.length === 0) {
			if (!input.rules.some((rule) => rule.name === ruleName)) {
				return `listed_models entry ${i} names routing rule "${ruleName}", which does not exist`;
			}
			if (!input.rules.some((rule) => rule.name === ruleName && routingRuleScopeMatches(rule, input.scope))) {
				return `listed_models entry ${i} names routing rule "${ruleName}", which is not scoped to this virtual key, its team or its customer`;
			}
			return `listed_models entry ${i} names routing rule "${ruleName}", which is disabled`;
		}
		if (candidates.length > 1) {
			return `listed_models entry ${i} names routing rule "${ruleName}", which is ambiguous: ${candidates.length} rules of that name are in scope`;
		}
		const key = `rule:${ruleName}`;
		if (seen.has(key)) return `listed_models entry ${i} repeats routing rule "${ruleName}"`;
		seen.add(key);
	}
	return undefined;
}