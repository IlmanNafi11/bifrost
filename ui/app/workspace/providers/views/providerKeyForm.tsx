import { Button } from "@/components/ui/button";
import { ConfigSyncAlert } from "@/components/ui/configSyncAlert";
import { Form, FormDescription, FormField, FormItem, FormLabel, FormMessage, FormControl } from "@/components/ui/form";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { getErrorMessage, useBindKeysMutation, useGetProxyPoolsQuery, useUnbindKeysMutation } from "@/lib/store";
import { useCreateProviderKeyMutation, useGetProviderKeysQuery, useUpdateProviderKeyMutation } from "@/lib/store/apis/providersApi";
import { ModelProvider, ModelProviderKey } from "@/lib/types/config";
import { PROXY_MECHANISMS, type ProxyMechanism } from "@/lib/types/proxyPools";
import { modelProviderKeySchema, proxyBindingFormSchema, type ProxyBindingFormSchema } from "@/lib/types/schemas";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { zodResolver } from "@hookform/resolvers/zod";
import { Save } from "lucide-react";
import { useCallback, useEffect, useRef } from "react";
import { useForm, useWatch } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { v4 as uuid } from "uuid";
import { z } from "zod";
import { bindingChange } from "@/app/workspace/config/proxy-pools/views/proxyPools.utils";
import { ApiKeyFormFragment } from "../fragments";
import { applyAuthMethod } from "./providerKeyForm.utils";
interface Props {
	provider: ModelProvider;
	keyId: string | null;
	onCancel: () => void;
	onSave: () => void;
}

// The proxy selection is a form-only field: the binding is written through the
// pool bind/unbind API, never as part of the key payload.
const providerKeyFormSchema = z.object({
	key: modelProviderKeySchema,
	proxy: proxyBindingFormSchema,
});

type ProviderKeyFormValues = z.infer<typeof modelProviderKeySchema>;
// zod transforms the weight string back to a number, so the form works with the
// schema's input type while submit payloads use its inferred (output) type.
type FormValues = z.input<typeof providerKeyFormSchema>;

export default function ProviderKeyForm({ provider, keyId, onCancel, onSave }: Props) {
	const { t } = useTranslation("models");
	const { t: tc } = useTranslation("common");
	const { t: tp } = useTranslation("proxypool");
	const hasUpdateProviderAccess = useRbac(RbacResource.ModelProvider, RbacOperation.Update);
	const [createProviderKey, { isLoading: isCreatingProviderKey }] = useCreateProviderKeyMutation();
	const [updateProviderKey, { isLoading: isUpdatingProviderKey }] = useUpdateProviderKeyMutation();
	const [bindKeys] = useBindKeysMutation();
	const [unbindKeys] = useUnbindKeysMutation();
	const { data: keys = [] } = useGetProviderKeysQuery(provider.name);
	const { data: poolData } = useGetProxyPoolsQuery();
	const pools = poolData?.pools ?? [];
	const isEditing = keyId !== null;
	const currentKey = keyId ? keys.find((k) => k.id === keyId) : undefined;
	const initialBinding = useRef<ProxyBindingFormSchema>({ pool_id: "", mechanism: "round_robin" });

	const form = useForm<FormValues>({
		resolver: zodResolver(providerKeyFormSchema),
		mode: "onChange",
		reValidateMode: "onChange",
		defaultValues: {
			key: (currentKey as ProviderKeyFormValues) ?? {
				id: uuid(),
				name: "",
				models: ["*"],
				blacklisted_models: [],
				weight: 1.0,
				enabled: true,
			},
			proxy: initialBinding.current,
		},
	});
	const selectedPoolId = useWatch({ control: form.control, name: "proxy.pool_id" });

	// Reset form when currentKey arrives (handles late async resolution)
	// Skip reset if user has unsaved edits to avoid discarding changes during background refetches
	useEffect(() => {
		if (!isEditing || !currentKey || form.formState.isDirty) return;
		const binding = {
			pool_id: currentKey.proxy_pool_id ?? "",
			mechanism: currentKey.proxy_pool_mechanism ?? "round_robin",
		};
		initialBinding.current = binding;
		form.reset({ key: currentKey as ProviderKeyFormValues, proxy: binding });
	}, [isEditing, currentKey, form]);

	// Trigger validation on mount when editing existing data
	useEffect(() => {
		if (isEditing) {
			form.trigger();
		}
	}, [isEditing, form]);

	const getTooltipContent = useCallback(() => {
		if (!hasUpdateProviderAccess) {
			return t("providers.noPermissionModifyKeys");
		}
		if (!form.formState.isValid && form.formState.errors.root?.message) {
			return form.formState.errors.root?.message;
		}
		if (!form.formState.isDirty) {
			return t("providers.noChanges");
		}
		return null;
	}, [form?.formState.errors, form?.formState.isValid, form?.formState.isDirty, hasUpdateProviderAccess, t]);

	const onSubmit = async (value: FormValues) => {
		if (isEditing && !currentKey) return;
		// The utility strips the UI-only _auth_type discriminators and says what the chosen
		// method clears, since the update API keeps every field the payload omits.
		const payload = applyAuthMethod(value.key, isEditing) as unknown as ModelProviderKey;
		const keyId = isEditing ? currentKey!.id : payload.id;
		const mutation = isEditing
			? updateProviderKey({
					provider: provider.name,
					keyId: currentKey!.id,
					key: payload,
				})
			: createProviderKey({
					provider: provider.name,
					key: payload,
				});

		try {
			await mutation.unwrap();
		} catch (err) {
			if ((err as { status?: number })?.status === 409) {
				form.setError("key.name", { message: getErrorMessage(err) });
				return;
			}
			toast.error(isEditing ? t("providers.errorUpdatingKey") : t("providers.errorCreatingKey"), {
				description: getErrorMessage(err),
			});
			return;
		}

		// The key is saved; the binding lives in the pool bindings table, so sync it
		// separately. A failure here is reported but does not re-open the saved form.
		const before = initialBinding.current;
		const after = value.proxy;
		const change = bindingChange(before, after);
		if (change.changed) {
			try {
				if (change.unbind) {
					await unbindKeys({ id: before.pool_id, data: { key_ids: [keyId] } }).unwrap();
				}
				if (change.bind) {
					await bindKeys({ id: after.pool_id, data: { key_ids: [keyId], mechanism: after.mechanism } }).unwrap();
				}
				toast.success(tp(after.pool_id ? "toastBound" : "toastUnbound"));
			} catch (err) {
				toast.error(tp("toastError"), { description: getErrorMessage(err) });
			}
		}
		initialBinding.current = { ...after };
		onSave();
	};

	return (
		<Form {...form}>
			<form onSubmit={form.handleSubmit(onSubmit)} className="flex grow flex-col gap-6 pt-4">
				<div className="grow px-4 md:px-8">
					<ApiKeyFormFragment
						control={form.control}
						providerName={provider.name}
						baseProviderType={provider.custom_provider_config?.base_provider_type}
						form={form}
					/>
					<div className="mt-6 space-y-4">
						<FormField
							control={form.control}
							name="proxy.pool_id"
							render={({ field }) => (
								<FormItem>
									<FormLabel>{tp("keyProxyLabel")}</FormLabel>
									<FormDescription>{tp("keyProxyDescription")}</FormDescription>
									<Select
										value={field.value || "none"}
										onValueChange={(value) => field.onChange(value === "none" ? "" : value)}
										disabled={!hasUpdateProviderAccess}
									>
										<FormControl>
											<SelectTrigger className="w-64" data-testid="key-proxy-pool-select">
												<SelectValue placeholder={tp("keyProxyNone")} />
											</SelectTrigger>
										</FormControl>
										<SelectContent>
											<SelectItem value="none" data-testid="key-proxy-pool-option-none">
												{tp("keyProxyNone")}
											</SelectItem>
											{pools.map((pool) => (
												<SelectItem key={pool.id} value={pool.id} data-testid={`key-proxy-pool-option-${pool.id}`}>
													{pool.name}
												</SelectItem>
											))}
										</SelectContent>
									</Select>
									<FormMessage />
								</FormItem>
							)}
						/>
						{selectedPoolId && (
							<FormField
								control={form.control}
								name="proxy.mechanism"
								render={({ field }) => (
									<FormItem>
										<FormLabel>{tp("keyProxyMechanism")}</FormLabel>
										<Select value={field.value} onValueChange={field.onChange} disabled={!hasUpdateProviderAccess}>
											<FormControl>
												<SelectTrigger className="w-64" data-testid="key-proxy-mechanism-select">
													<SelectValue />
												</SelectTrigger>
											</FormControl>
											<SelectContent>
												{PROXY_MECHANISMS.map((mechanism: ProxyMechanism) => (
													<SelectItem key={mechanism} value={mechanism} data-testid={`key-proxy-mechanism-option-${mechanism}`}>
														{tp(`mechanisms.${mechanism}`)}
													</SelectItem>
												))}
											</SelectContent>
										</Select>
										<FormMessage />
									</FormItem>
								)}
							/>
						)}
					</div>
					{isEditing && currentKey?.config_hash && <ConfigSyncAlert className="mt-4" />}
				</div>
				<div className="bg-card sticky bottom-0 border-t px-4 py-4 md:px-8">
					<div className="flex justify-end space-x-3">
						<Button type="button" variant="outline" onClick={onCancel} data-testid="key-cancel-btn">
							{tc("cancel")}
						</Button>
						<TooltipProvider>
							<Tooltip>
								<TooltipTrigger asChild>
									<span>
										<Button
											type="submit"
											disabled={!form.formState.isDirty || !hasUpdateProviderAccess}
											isLoading={form.formState.isSubmitting || isCreatingProviderKey || isUpdatingProviderKey}
											data-testid="key-save-btn"
										>
											<Save className="h-4 w-4 shrink-0" />
											{tc("save")}
										</Button>
									</span>
								</TooltipTrigger>
								{getTooltipContent() && <TooltipContent>{getTooltipContent()}</TooltipContent>}
							</Tooltip>
						</TooltipProvider>
					</div>
				</div>
			</form>
		</Form>
	);
}