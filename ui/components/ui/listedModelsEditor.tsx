import { Button } from "@/components/ui/button";
import { ComboboxSelect } from "@/components/ui/combobox";
import { Label } from "@/components/ui/label";
import { ModelSelector } from "@/components/ui/modelSelector";
import { ProviderSelector } from "@/components/ui/providerSelector";
import { Switch } from "@/components/ui/switch";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { RenderProviderIcon, resolveProviderIconKey } from "@/lib/constants/icons";
import { getProviderLabel } from "@/lib/constants/logs";
import type { RoutingRule } from "@/lib/types/routingRules";
import {
	defaultListedProvider,
	isModelOffered,
	listedEntryFromModel,
	listedEntryFromRule,
	listedEntryKey,
	type ListedModelsEntry,
	type ListedModelsScope,
	listedProviderOptions,
	type ListedProviderConfig,
	listedRuleOptions,
	MAX_LISTED_MODELS_ENTRIES,
} from "@/lib/utils/listedModels";
import { Info, Trash2 } from "lucide-react";
import { type ReactNode, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

export interface ListedModelsEditorProps {
	/** The block as the form holds it. `undefined` is the feature off; `[]` denies every model. */
	value: ListedModelsEntry[] | undefined;
	onChange: (next: ListedModelsEntry[] | undefined) => void;
	/** The key's provider configurations: the layer a listed model still has to pass. */
	providerConfigs: ListedProviderConfig[];
	/** The routing rules the block may name, from the same list the server validates against. */
	rules: RoutingRule[];
	/** The key's owner ids, which decide whether a rule is in scope. */
	scope: ListedModelsScope;
	/** The form's allow_all_providers: when set, a model entry may name an unconfigured provider. */
	allowAllProviders: boolean;
	/** Server-side validation messages, rendered under the list. */
	errors?: string[];
	/** Overridable so a holder other than a virtual key (an access profile, a project) can say what the block means. */
	label?: string;
	tooltip?: ReactNode;
	testIdPrefix?: string;
	disabled?: boolean;
}

/**
 * The virtual key's `listed_models` block: the single source of what the key sees in `/v1/models`
 * and may call beyond `allowed_models`, with routing-rule entries standing for the models behind a
 * rule. Off means the block is absent and the key behaves as it always has; switched on but empty
 * denies every model, so the two states are kept apart rather than collapsed.
 *
 * Rows are removable; models are added a provider at a time, from that provider's allow list, and
 * rules from those enabled and in scope. The server re-checks all of it on save, so the editor
 * only refuses what it can see, and its `errors` show what it refused.
 */
export function ListedModelsEditor({
	value,
	onChange,
	providerConfigs,
	rules,
	scope,
	allowAllProviders,
	errors,
	label,
	tooltip,
	testIdPrefix = "vk-listed-models",
	disabled = false,
}: ListedModelsEditorProps) {
	const { t } = useTranslation("governance");
	const enabled = value !== undefined;
	const entries = useMemo(() => value ?? [], [value]);
	// Staged picks: a provider and the models from it to add. Switching provider drops them, since
	// a model belongs to exactly one provider.
	const [stagedProvider, setStagedProvider] = useState("");
	const [stagedModels, setStagedModels] = useState<string[]>([]);

	const providerOptions = useMemo(
		() => listedProviderOptions(providerConfigs, entries, allowAllProviders),
		[providerConfigs, entries, allowAllProviders],
	);
	const provider = providerOptions.includes(stagedProvider)
		? stagedProvider
		: defaultListedProvider(providerConfigs, allowAllProviders) || providerOptions[0] || "";
	const providerConfig = providerConfigs.find((config) => config.provider === provider);

	const ruleOptions = useMemo(() => listedRuleOptions(rules, scope), [rules, scope]);
	const usedRuleNames = useMemo(() => new Set(entries.map((entry) => entry.routing_rule).filter(Boolean)), [entries]);
	const addableRules = useMemo(
		() => ruleOptions.filter((option) => !usedRuleNames.has(option.name)).map((option) => ({ value: option.name, label: option.name })),
		[ruleOptions, usedRuleNames],
	);

	const atCap = entries.length >= MAX_LISTED_MODELS_ENTRIES;
	const addableModels = useMemo(() => {
		const used = new Set(entries.map(listedEntryKey));
		return stagedModels.filter((model) => !used.has(listedEntryKey(listedEntryFromModel(provider, model))));
	}, [entries, stagedModels, provider]);

	const addModels = () => {
		if (addableModels.length === 0) return;
		onChange([...entries, ...addableModels.map((model) => listedEntryFromModel(provider, model))]);
		setStagedModels([]);
	};

	const addRule = (name: string | null) => {
		if (!name || usedRuleNames.has(name)) return;
		onChange([...entries, listedEntryFromRule(name)]);
	};

	const resolvedLabel = label ?? t("listedModels.label");

	return (
		<div className="mt-6 space-y-3" data-testid={`${testIdPrefix}-editor`}>
			<div className="flex items-center justify-between gap-2">
				<div className="flex items-center gap-2">
					<Label className="text-sm font-medium">{resolvedLabel}</Label>
					<TooltipProvider>
						<Tooltip>
							<TooltipTrigger asChild>
								<button type="button" aria-label={t("listedModels.moreInfo")} className="inline-flex">
									<Info className="text-muted-foreground h-3 w-3" />
								</button>
							</TooltipTrigger>
							<TooltipContent className="max-w-xs">{tooltip ?? <p>{t("listedModels.tooltip")}</p>}</TooltipContent>
						</Tooltip>
					</TooltipProvider>
				</div>
				<Switch
					checked={enabled}
					disabled={disabled}
					aria-label={resolvedLabel}
					data-testid={`${testIdPrefix}-switch`}
					onCheckedChange={(checked) => onChange(checked ? [] : undefined)}
				/>
			</div>

			{!enabled && (
				<p className="text-muted-foreground text-xs" data-testid={`${testIdPrefix}-off-hint`}>
					{t("listedModels.offHint")}
				</p>
			)}

			{enabled && (
				<>
					{entries.length > 0 ? (
						<div className="divide-y overflow-hidden rounded-md border">
							{entries.map((entry, index) => {
								const isRule = entry.routing_rule !== "";
								const iconKey = isRule ? undefined : resolveProviderIconKey(entry.provider);
								return (
									<div
										key={listedEntryKey(entry)}
										className="flex items-center justify-between gap-2 px-3 py-2"
										data-testid={`${testIdPrefix}-row-${index}`}
									>
										<div className="flex min-w-0 items-center gap-2">
											{iconKey && <RenderProviderIcon provider={iconKey} className="h-4 w-4" />}
											<div className="flex min-w-0 flex-col">
												<span className="truncate text-sm font-medium">
													{isRule ? entry.routing_rule : `${entry.provider}/${entry.model}`}
												</span>
												<span className="text-muted-foreground truncate text-xs">
													{isRule ? t("listedModels.routingRule") : getProviderLabel(entry.provider)}
												</span>
											</div>
										</div>
										<Button
											type="button"
											variant="ghost"
											size="sm"
											disabled={disabled}
											aria-label={t("listedModels.removeAria", {
												entry: isRule ? entry.routing_rule : `${entry.provider}/${entry.model}`,
											})}
											data-testid={`${testIdPrefix}-remove-${index}`}
											onClick={() => onChange(entries.filter((_, i) => i !== index))}
										>
											<Trash2 className="h-4 w-4" />
										</Button>
									</div>
								);
							})}
						</div>
					) : (
						<p className="text-muted-foreground text-xs" data-testid={`${testIdPrefix}-empty`}>
							{t("listedModels.empty")}
						</p>
					)}

					<div className="flex flex-col gap-2 sm:flex-row">
						<div className="sm:w-56">
							<ProviderSelector
								source="values"
								values={providerOptions}
								value={provider}
								onChange={(next: string) => {
									setStagedProvider(next);
									setStagedModels([]);
								}}
								disabled={disabled || providerOptions.length === 0}
								placeholder={t("listedModels.selectProvider")}
								className="!h-9 !min-h-9"
								data-testid={`${testIdPrefix}-provider-select`}
							/>
						</div>
						<div className="flex-1">
							<ModelSelector
								multiple
								provider={provider || undefined}
								value={stagedModels}
								onChange={(next) =>
									setStagedModels(
										next.filter((model) => isModelOffered(provider, model, providerConfig ? [providerConfig] : providerConfigs)),
									)
								}
								disabled={disabled || atCap || !provider}
								placeholder={atCap ? t("listedModels.capReached", { max: MAX_LISTED_MODELS_ENTRIES }) : t("listedModels.selectModels")}
								className="!h-9 !min-h-9 w-full"
								data-testid={`${testIdPrefix}-model-select`}
							/>
						</div>
						<Button
							type="button"
							variant="outline"
							size="sm"
							className="h-9"
							disabled={disabled || atCap || addableModels.length === 0}
							data-testid={`${testIdPrefix}-add-model`}
							onClick={addModels}
						>
							{t("listedModels.add")}
						</Button>
					</div>

					<div className="flex flex-col gap-2 sm:flex-row">
						<div className="flex-1">
							<ComboboxSelect
								options={addableRules}
								value={null}
								onValueChange={addRule}
								disabled={disabled || atCap || addableRules.length === 0}
								placeholder={atCap ? t("listedModels.capReached", { max: MAX_LISTED_MODELS_ENTRIES }) : t("listedModels.selectRule")}
								emptyMessage={t("listedModels.noRules")}
								className="!h-9 !min-h-9"
								data-testid={`${testIdPrefix}-add-rule`}
							/>
						</div>
					</div>

					{atCap && (
						<p className="text-muted-foreground text-xs" data-testid={`${testIdPrefix}-cap`}>
							{t("listedModels.capReached", { max: MAX_LISTED_MODELS_ENTRIES })}
						</p>
					)}
				</>
			)}

			{errors && errors.length > 0 && (
				<ul className="text-destructive space-y-1 text-xs" data-testid={`${testIdPrefix}-errors`}>
					{errors.map((error) => (
						<li key={error}>{error}</li>
					))}
				</ul>
			)}
		</div>
	);
}