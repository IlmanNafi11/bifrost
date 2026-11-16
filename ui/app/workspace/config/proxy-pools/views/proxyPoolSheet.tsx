import { Button } from "@/components/ui/button";
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Textarea } from "@/components/ui/textarea";
import { msg } from "@/lib/i18n/validationMessage";
import { getErrorMessage, useCreateProxyPoolMutation, useUpdateProxyPoolMutation } from "@/lib/store";
import type { ProxyPool } from "@/lib/types/proxyPools";
import { zodResolver } from "@hookform/resolvers/zod";
import { useEffect } from "react";
import { useForm } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { z } from "zod";

const poolFormSchema = z.object({
	name: z.string().trim().min(1, msg("validation.nameIsRequired")),
	description: z.string(),
});

type PoolFormValues = z.infer<typeof poolFormSchema>;

interface Props {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	pool?: ProxyPool;
	onSaved: () => void;
}

export default function ProxyPoolSheet({ open, onOpenChange, pool, onSaved }: Props) {
	const { t } = useTranslation("proxypool");
	const [createPool, { isLoading: isCreating }] = useCreateProxyPoolMutation();
	const [updatePool, { isLoading: isUpdating }] = useUpdateProxyPoolMutation();
	const isEditing = Boolean(pool);

	const form = useForm<PoolFormValues>({
		resolver: zodResolver(poolFormSchema),
		mode: "onChange",
		defaultValues: { name: pool?.name ?? "", description: pool?.description ?? "" },
	});

	useEffect(() => {
		if (open) form.reset({ name: pool?.name ?? "", description: pool?.description ?? "" });
	}, [open, pool, form]);

	const onSubmit = (values: PoolFormValues) => {
		const mutation = pool
			? updatePool({ id: pool.id, data: { name: values.name, description: values.description } })
			: createPool({ name: values.name, description: values.description });
		mutation
			.unwrap()
			.then(() => {
				toast.success(isEditing ? t("toastUpdated") : t("toastCreated"));
				onSaved();
				onOpenChange(false);
			})
			.catch((error) => toast.error(t("toastError"), { description: getErrorMessage(error) }));
	};

	return (
		<Sheet open={open} onOpenChange={onOpenChange}>
			<SheetContent className="p-0">
				<SheetHeader className="flex flex-col items-start px-4 pt-6 md:px-6">
					<SheetTitle>{isEditing ? t("editPool") : t("addPool")}</SheetTitle>
					<SheetDescription>{t("description")}</SheetDescription>
				</SheetHeader>
				<Form {...form}>
					<form onSubmit={form.handleSubmit(onSubmit)} className="flex grow flex-col">
						<div className="flex-1 space-y-4 px-4 py-4 md:px-6">
							<FormField
								control={form.control}
								name="name"
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("poolName")}</FormLabel>
										<FormControl>
											<Input {...field} data-testid="proxy-pool-name-input" placeholder={t("poolNamePlaceholder")} />
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={form.control}
								name="description"
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("poolDescription")}</FormLabel>
										<FormControl>
											<Textarea {...field} data-testid="proxy-pool-description-input" placeholder={t("poolDescriptionPlaceholder")} />
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
						</div>
						<SheetFooter className="flex-row justify-end border-t px-4 py-4 md:px-6">
							<Button type="button" variant="outline" onClick={() => onOpenChange(false)} data-testid="proxy-pool-cancel-btn">
								{t("actionCancel")}
							</Button>
							<Button
								type="submit"
								disabled={!form.formState.isValid || isCreating || isUpdating}
								isLoading={isCreating || isUpdating}
								data-testid="proxy-pool-submit-btn"
							>
								{t("actionSave")}
							</Button>
						</SheetFooter>
					</form>
				</Form>
			</SheetContent>
		</Sheet>
	);
}