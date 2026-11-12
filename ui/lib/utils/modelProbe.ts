import type { ParseKeys } from "i18next";

import i18n from "@/lib/i18n";
import type { ModelTestResult } from "@/lib/types/modelTest";

// Keys are looked up with an explicit namespace and interpolations. Typed against
// the English catalog through i18next's CustomTypeOptions; the cast keeps the
// lookup readable at the one place the toast text is assembled.
function t(key: ParseKeys<"models">, values: Record<string, string | number>): string {
	return i18n.t(key, { ns: "models", ...values }) as string;
}

// The modes the server can probe. Kept beside the caller so a model whose catalog
// row names anything else shows a disabled button instead of a request the server
// answers with 400.
const PROBEABLE_MODES = new Set([
	"chat",
	"responses",
	"completion",
	"embedding",
	"rerank",
	"video_generation",
]);

// The modes whose probe measures time to the first token rather than total time.
const STREAMING_MODES = new Set(["chat", "responses", "completion"]);

// A missing mode is the chat default, matching what /api/models/details reports
// for a row with no datasheet entry.
export function isModeProbeable(mode: string | undefined): boolean {
	if (!mode) return true;
	return PROBEABLE_MODES.has(mode);
}

export function isStreamingMode(mode: string | undefined): boolean {
	return mode ? STREAMING_MODES.has(mode) : true;
}

export interface ModelProbeToast {
	title: string;
	description: string;
	variant: "success" | "error";
}

// formatModelProbeToast turns a verdict into the one toast a click produces: the
// title is the verdict with the model it belongs to, the description is what was
// measured and which key paid for it.
export function formatModelProbeToast(result: ModelTestResult): ModelProbeToast {
	const label = `${result.provider}/${result.model}`;
	const key = result.key_name || result.key_id || t("modelTest.keyAuto", {});

	if (!result.ok) {
		const reason = result.error || t("modelTest.failed", {});
		return {
			title: t("modelTest.failedTitle", { label }),
			description: t("modelTest.failedDescription", { reason, key }),
			variant: "error",
		};
	}

	const description =
		result.ttft_ms !== undefined && isStreamingMode(result.mode)
			? t("modelTest.streamingDescription", { ttft: result.ttft_ms, total: result.total_ms, key })
			: t("modelTest.unaryDescription", { total: result.total_ms, key });

	return {
		title: t("modelTest.successTitle", { label }),
		description,
		variant: "success",
	};
}
