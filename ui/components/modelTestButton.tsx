import { Loader2, Play } from "lucide-react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { useTestModelMutation } from "@/lib/store/apis/providersApi";
import { isModeProbeable, formatModelProbeToast } from "@/lib/utils/modelProbe";

interface ModelTestButtonProps {
	provider: string;
	model: string;
	// The catalog mode of this row, as /api/models/details reports it. Undefined
	// means the chat default.
	mode?: string;
	// Explicit key to probe with, for a surface that already picked one. Omitted,
	// the server selects a key exactly as it would for a real request.
	keyId?: string;
	className?: string;
}

// ModelTestButton sends one probe and reports the verdict as a toast. The server
// hides the probe from logs, cost and stats, so this is the only place the result
// is ever shown.
export function ModelTestButton({ provider, model, mode, keyId, className }: ModelTestButtonProps) {
	const { t } = useTranslation("models");
	const [testModel, { isLoading }] = useTestModelMutation();
	const probeable = isModeProbeable(mode);
	const label = `${provider}/${model}`;

	const run = async () => {
		try {
			const result = await testModel({ provider, model, key_id: keyId }).unwrap();
			const { title, description, variant } = formatModelProbeToast(result);
			(variant === "success" ? toast.success : toast.error)(title, { description });
		} catch (error) {
			// A transport failure (network, 4xx before the probe runs) has no verdict
			// body; the toast still names the row it belongs to.
			toast.error(t("modelTest.failedTitle", { label }), { description: stringifyProbeError(error) });
		}
	};

	const button = (
		<Button
			type="button"
			variant="ghost"
			size="sm"
			className={className}
			disabled={!probeable || isLoading}
			aria-label={t("modelTest.aria", { label })}
			data-testid="model-test-button"
			onClick={() => void run()}
		>
			{isLoading ? <Loader2 className="animate-spin" /> : <Play />}
		</Button>
	);

	if (probeable) return button;
	return (
		<TooltipProvider>
			<Tooltip>
				<TooltipTrigger asChild>
					<span className="inline-flex">{button}</span>
				</TooltipTrigger>
				<TooltipContent>{t("modelTest.modeUnsupported")}</TooltipContent>
			</Tooltip>
		</TooltipProvider>
	);
}

function stringifyProbeError(error: unknown): string {
	if (typeof error === "string") return error;
	if (error && typeof error === "object") {
		const record = error as Record<string, unknown>;
		if (typeof record.error === "string") return record.error;
		if (typeof record.message === "string") return record.message;
		if (record.error && typeof record.error === "object") {
			const inner = record.error as Record<string, unknown>;
			if (typeof inner.message === "string") return inner.message;
		}
	}
	return String(error);
}
