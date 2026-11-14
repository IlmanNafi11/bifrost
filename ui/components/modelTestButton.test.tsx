import i18n from "@/lib/i18n";
import { renderToStaticMarkup } from "react-dom/server";
import { beforeAll, describe, expect, it, vi } from "vitest";
import { ModelTestButton } from "./modelTestButton";

// The probe's in-flight state is the only thing the mock varies: hoisted so both the factory and a
// test can reach it, since vi.mock is lifted above the imports.
const probeState = vi.hoisted(() => ({ loading: false }));

vi.mock("@/lib/store/apis/providersApi", () => ({
	useTestModelMutation: () => [vi.fn(), { isLoading: probeState.loading }],
}));

beforeAll(async () => {
	await i18n.changeLanguage("en");
});

// The button is the only surface a probe result reaches, and a disabled button
// that still fires would bill a provider for nothing, so the gate is markup.
describe("ModelTestButton", () => {
	it("is enabled for a probeable mode and names the row it tests", () => {
		const html = renderToStaticMarkup(<ModelTestButton provider="openai" model="gpt-4o-mini" mode="chat" />);

		expect(html).toContain('aria-label="Test openai/gpt-4o-mini"');
		expect(html).toContain('data-testid="model-test-button"');
		expect(html).not.toContain('disabled=""');
	});

	it("is disabled when the mode has no probe payload", () => {
		const html = renderToStaticMarkup(<ModelTestButton provider="openai" model="dall-e-3" mode="image_generation" />);

		expect(html).toContain('disabled=""');
	});

	it("treats a row without a mode as the chat default", () => {
		const html = renderToStaticMarkup(<ModelTestButton provider="openai" model="gpt-4o-mini" />);

		expect(html).not.toContain('disabled=""');
	});
});

// A probe in flight must be disabled: the button is the only entry point, and a second click while
// the first is unanswered would spend a second probe.
it("is disabled while its probe is unanswered", () => {
	probeState.loading = true;
	const html = renderToStaticMarkup(<ModelTestButton provider="openai" model="gpt-4o-mini" mode="chat" />);
	probeState.loading = false;

	expect(html).toContain('disabled=""');
});
