import { expect, test } from "../../core/fixtures/base.fixture";

test.describe("Model Catalog - Price Precision", () => {
	test("displays sub-cent token prices accurately in table and attribute drawer", async ({ page }) => {
		const mockModel = {
			name: "gpt-4o-mini-precision-test",
			provider: "openai",
			context_length: 128000,
			max_input_tokens: 128000,
			max_output_tokens: 16384,
			// $0.075 / 1M (was previously rounded to $0.08)
			input_cost_per_token: 0.000000075,
			// $1.875 / 1M (was previously rounded to $1.88)
			output_cost_per_token: 0.000001875,
			// $0.035 / 1M (was previously rounded to $0.04)
			cache_creation_input_token_cost: 0.000000035,
			// $0.00875 / 1M (was previously rounded to $0.01)
			cache_read_input_token_cost: 0.00000000875,
			additional_attributes: {
				description: "Model for testing price precision display in E2E",
			},
		};

		// Intercept the model details API to supply the precision test model
		await page.route("**/api/models/details*", async (route) => {
			if (route.request().method() === "GET") {
				await route.fulfill({
					status: 200,
					contentType: "application/json",
					body: JSON.stringify({
						models: [mockModel],
						total: 1,
						pricing_overrides: {},
					}),
				});
			} else {
				await route.continue();
			}
		});

		// Navigate directly to the Models attribute tab
		await page.goto("/workspace/model-catalog?tab=attributes");

		// Locate the row in the catalog table
		const row = page.getByTestId("model-catalog-row-gpt-4o-mini-precision-test-openai");
		await expect(row).toBeVisible();

		// Verify per-1M sub-cent prices in the table retain accurate decimal precision
		await expect(row).toContainText("$0.075");
		await expect(row).toContainText("$1.875");
		await expect(row).toContainText("$0.035");
		await expect(row).toContainText("$0.00875");

		// Open the Attribute Drawer for this model
		await page.getByTestId("model-catalog-edit-gpt-4o-mini-precision-test-openai").click();

		// Verify the Attribute Drawer opens
		const sheet = page.getByTestId("model-catalog-attribute-sheet");
		await expect(sheet).toBeVisible();

		// Verify full price labels in the attribute drawer preserve sub-cent rates with / 1M tokens
		await expect(page.getByTestId("model-catalog-input-cost")).toHaveText("$0.075 / 1M tokens");
		await expect(page.getByTestId("model-catalog-output-cost")).toHaveText("$1.875 / 1M tokens");
		await expect(page.getByTestId("model-catalog-cache-write-cost")).toHaveText("$0.035 / 1M tokens");
		await expect(page.getByTestId("model-catalog-cache-read-cost")).toHaveText("$0.00875 / 1M tokens");
	});
});

test.describe("Model Catalog - Model Probe", () => {
	test("probes a model in its catalog mode and disables Test for a mode it cannot exercise", async ({ page }) => {
		const chatModel = {
			name: "gpt-4o-mini-probe-test",
			provider: "openai",
			mode: "chat",
			input_cost_per_token: 0.00000015,
			output_cost_per_token: 0.0000006,
			additional_attributes: { description: "Chat model used by the probe spec" },
		};
		const imageModel = {
			name: "dall-e-3-probe-test",
			provider: "openai",
			// The server has no universal image-generation payload, so this row's Test is disabled
			// rather than sent and refused.
			mode: "image_generation",
			input_cost_per_token: 0.00000004,
			output_cost_per_token: 0,
			additional_attributes: { description: "Image model used by the probe spec" },
		};

		await page.route("**/api/models/details*", async (route) => {
			if (route.request().method() === "GET") {
				await route.fulfill({
					status: 200,
					contentType: "application/json",
					body: JSON.stringify({
						models: [chatModel, imageModel],
						total: 2,
						pricing_overrides: {},
					}),
				});
				return;
			}
			await route.continue();
		});

		// The probe is one real provider call; its verdict and the request the UI builds are what
		// this spec pins, so the answer is stubbed.
		let probedBody: { provider: string; model: string } | undefined;
		// The answer is held open so the in-flight state can be observed: an unanswered probe must
		// disable the button, or a second click would spend a second real provider call. Executor
		// form, not Promise.withResolvers: this project's lib target predates es2024.
		let releaseProbe: () => void = () => {};
		const probeHeld = new Promise<void>((resolve) => {
			releaseProbe = resolve;
		});
		await page.route("**/api/models/test", async (route) => {
			probedBody = route.request().postDataJSON() as {
				provider: string;
				model: string;
			};
			await probeHeld;
			await route.fulfill({
				status: 200,
				contentType: "application/json",
				body: JSON.stringify({
					ok: true,
					mode: "chat",
					provider: "openai",
					model: "gpt-4o-mini-probe-test",
					ttft_ms: 90,
					total_ms: 250,
					key_name: "e2e key",
				}),
			});
		});

		await page.goto("/workspace/model-catalog?tab=attributes");

		const chatRow = page.getByTestId("model-catalog-row-gpt-4o-mini-probe-test-openai");
		const imageRow = page.getByTestId("model-catalog-row-dall-e-3-probe-test-openai");
		await expect(chatRow).toBeVisible();
		await expect(imageRow).toBeVisible();

		const chatTest = chatRow.getByTestId("model-catalog-test-gpt-4o-mini-probe-test-openai");
		await expect(chatTest).toBeEnabled();
		await expect(imageRow.getByTestId("model-catalog-test-dall-e-3-probe-test-openai")).toBeDisabled();

		await chatTest.click();

		// While the probe is unanswered the button is disabled and its spinner is the only
		// affordance left: a click that still fired would bill the provider a second time.
		await expect(chatTest).toBeDisabled();
		await expect(chatTest.locator(".animate-spin")).toBeVisible();

		releaseProbe();
		const toast = page.locator('[data-sonner-toast][data-type="success"]').first();
		await expect(toast).toBeVisible();
		await expect(toast).toContainText("TTFT 90 ms");
		await expect(toast).toContainText("total 250 ms");
		expect(probedBody).toEqual({ provider: "openai", model: "gpt-4o-mini-probe-test" });
	});
});
