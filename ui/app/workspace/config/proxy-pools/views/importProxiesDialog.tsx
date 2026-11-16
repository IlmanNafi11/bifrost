import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Textarea } from "@/components/ui/textarea";
import { getErrorMessage, useImportProxiesMutation } from "@/lib/store";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { previewProxyImport } from "./proxyPools.utils";

interface Props {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	poolId: string;
	onImported: () => void;
}

export default function ImportProxiesDialog({ open, onOpenChange, poolId, onImported }: Props) {
	const { t } = useTranslation("proxypool");
	const [text, setText] = useState("");
	const [importProxies, { isLoading }] = useImportProxiesMutation();
	const preview = useMemo(() => previewProxyImport(text), [text]);

	const onSubmit = () => {
		importProxies({ id: poolId, data: { text } })
			.unwrap()
			.then((result) => {
				toast.success(t("toastImported", { count: result.imported }));
				setText("");
				onImported();
				onOpenChange(false);
			})
			.catch((error) => toast.error(t("toastError"), { description: getErrorMessage(error) }));
	};

	return (
		<Dialog
			open={open}
			onOpenChange={(next) => {
				if (!next) setText("");
				onOpenChange(next);
			}}
		>
			<DialogContent data-testid="proxy-pool-import-dialog">
				<DialogHeader>
					<DialogTitle>{t("importTitle")}</DialogTitle>
					<DialogDescription>{t("importDescription")}</DialogDescription>
				</DialogHeader>
				<Textarea
					value={text}
					onChange={(event) => setText(event.target.value)}
					placeholder={t("importPlaceholder")}
					rows={8}
					data-testid="proxy-pool-import-textarea"
				/>
				<p className="text-muted-foreground text-sm" data-testid="proxy-pool-import-preview">
					{t("importPreview", { count: preview.unique })}
					{preview.invalid.length > 0 && ` · ${t("importInvalid", { count: preview.invalid.length })}`}
				</p>
				<DialogFooter>
					<Button type="button" variant="outline" onClick={() => onOpenChange(false)} data-testid="proxy-pool-import-cancel-btn">
						{t("actionCancel")}
					</Button>
					<Button
						type="button"
						onClick={onSubmit}
						disabled={preview.unique === 0 || preview.invalid.length > 0 || isLoading}
						isLoading={isLoading}
						data-testid="proxy-pool-import-submit-btn"
					>
						{t("actionImport")}
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}