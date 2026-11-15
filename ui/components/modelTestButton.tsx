import { Loader2, Play } from "lucide-react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { useTestModelMutation } from "@/lib/store/apis/providersApi";
import { getErrorMessage } from "@/lib/store/apis/baseApi";
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
	// data-testid for the button. Each surface passes its own, so a row can be addressed from outside
	// (routing-target-{i}-model-test, model-catalog-test-{key}), per the repo's testid convention.
	testId?: string;
	// A caller that knows the user lacks the access the endpoint requires passes this, so the control
	// is visibly unavailable instead of 403ing after a click. The server still enforces it.
	disabled?: boolean;
}

// ModelTestButton sends one probe and reports the verdict as a toast. The server
// hides the probe from logs, cost and stats, so this is the only place the result
// is ever shown.
export function ModelTestButton({
	provider,
	model,
	mode,
	keyId,
	className,
	testId = "model-test-button",
	disabled = false,
}: ModelTestButtonProps) {
	const { t } = useTranslation("models");
	const [testModel, { isLoading }] = useTestModelMutation();
	const probeable = isModeProbeable(mode) && !disabled;
	const label = `${provider}/${model}`;

	const run = async () => {
		try {
			const result = await testModel({ provider, model, key_id: keyId }).unwrap();
			const { title, description, variant } = formatModelProbeToast(result);
			(variant === "success" ? toast.success : toast.error)(title, { description });
		} catch (error) {
			// A transport failure (network, 4xx before the probe runs) has no verdict body. Its RTK
			// shape nests the server message under data.error, so the shared extractor reads it;
			// stringifying the wrapper would show "[object Object]" instead of the refusal.
			toast.error(t("modelTest.failedTitle", { label }), { description: getErrorMessage(error) });
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
			data-testid={testId}
			onClick={() => void run()}
		>
			{isLoading ? <Loader2 className="animate-spin" /> : <Play />}
		</Button>
	);

	if (probeable) return button;
	// The tooltip explains why a click does nothing; a caller-driven disable has no reason to give,
	// so the button is rendered plainly rather than with a mode explanation that would be wrong.
	if (disabled) return button;
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