import { beforeAll, describe, expect, it } from "vitest";

import i18n from "@/lib/i18n";
import { formatModelProbeToast, isModeProbeable } from "./modelProbe";

// The toast text is built through i18next, so the catalog has to be live before
// the format assertions can compare real strings.
beforeAll(async () => {
	await i18n.changeLanguage("en");
});

// The toast must name exactly what was measured: TTFT is a streaming figure and
// must never be shown for a unary probe, and a verdict no probe produced must
// never read as a success.
describe("isModeProbeable", () => {
	it("accepts every mode the server can dispatch", () => {
		for (const mode of ["chat", "responses", "completion", "embedding", "rerank", "video_generation"]) {
			expect(isModeProbeable(mode)).toBe(true);
		}
	});

	it("rejects modes with no valid probe payload, and a missing mode is chat", () => {
		for (const mode of ["container_create", "live", "ocr", "audio_speech", "image_generation", "nonsense"]) {
			expect(isModeProbeable(mode)).toBe(false);
		}
		expect(isModeProbeable(undefined)).toBe(true);
		expect(isModeProbeable("")).toBe(true);
	});
});

describe("formatModelProbeToast", () => {
	it("reports TTFT and total for a streaming success, with the key", () => {
		const toast = formatModelProbeToast({
			ok: true,
			mode: "chat",
			provider: "openai",
			model: "gpt-4o-mini",
			ttft_ms: 412,
			total_ms: 654,
			key_name: "prod-2",
		});
		expect(toast.variant).toBe("success");
		expect(toast.title).toContain("openai/gpt-4o-mini");
		expect(toast.description).toContain("TTFT 412 ms");
		expect(toast.description).toContain("total 654 ms");
		expect(toast.description).toContain("prod-2");
	});

	it("reports latency only for a unary success", () => {
		const toast = formatModelProbeToast({
			ok: true,
			mode: "embedding",
			provider: "openai",
			model: "text-embedding-3-small",
			total_ms: 88,
			key_id: "key-1",
			key_name: "prod-1",
		});
		expect(toast.variant).toBe("success");
		expect(toast.description).toContain("Latency 88 ms");
		expect(toast.description).not.toContain("TTFT");
		expect(toast.description).toContain("prod-1");
	});

	it("says key auto when no key was stamped", () => {
		const toast = formatModelProbeToast({ ok: true, mode: "chat", provider: "openai", model: "m", ttft_ms: 1, total_ms: 2 });
		expect(toast.description).toContain("auto");
	});

	it("falls back to the key id when the name is missing", () => {
		const toast = formatModelProbeToast({
			ok: true,
			mode: "chat",
			provider: "openai",
			model: "m",
			ttft_ms: 1,
			total_ms: 2,
			key_id: "key-abc",
		});
		expect(toast.description).toContain("key-abc");
	});

	it("never shows a TTFT for a unary verdict that happens to carry one", () => {
		// The mode decides the label: an embedding probe is unary whatever numbers come back, so a
		// first-chunk time reported beside it must not be presented as a token measurement.
		const toast = formatModelProbeToast({
			ok: true,
			mode: "embedding",
			provider: "openai",
			model: "text-embedding-3-small",
			ttft_ms: 5,
			total_ms: 88,
		});
		expect(toast.description).toContain("Latency 88 ms");
		expect(toast.description).not.toContain("TTFT");
	});

	it("uses the generic failure reason when the provider sent no message", () => {
		const toast = formatModelProbeToast({ ok: false, mode: "chat", provider: "openai", model: "m", total_ms: 12, error: "" });
		expect(toast.variant).toBe("error");
		expect(toast.description).not.toContain("undefined");
		expect(toast.description.length).toBeGreaterThan(0);
	});

	it("shows the provider's error and the key for a failed probe", () => {
		const toast = formatModelProbeToast({
			ok: false,
			mode: "chat",
			provider: "openai",
			model: "gpt-4o-mini",
			total_ms: 30,
			key_name: "prod-2",
			error: "HTTP 401: invalid api key",
		});
		expect(toast.variant).toBe("error");
		expect(toast.title).toContain("openai/gpt-4o-mini");
		expect(toast.description).toContain("HTTP 401: invalid api key");
		expect(toast.description).toContain("prod-2");
	});
});