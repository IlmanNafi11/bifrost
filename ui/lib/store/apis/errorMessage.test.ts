import { describe, expect, it } from "vitest";
import { getErrorMessage } from "./baseApi";

// Every probe refusal an operator can act on ("only administrators can probe a model", a rate
// limit, a provider timeout) arrives as an RTK rejection, not as a plain Error. The probe toast
// renders this string directly, so a shape it cannot read becomes "[object Object]" on screen.
describe("getErrorMessage", () => {
	it("reads the server message out of an RTK rejection", () => {
		expect(getErrorMessage({ status: 403, data: { error: { message: "only administrators can probe a model" } } })).toBe(
			"Only administrators can probe a model",
		);
	});

	it("reads a 429 refusal the probe endpoint itself produced", () => {
		const refusal = { status: 429, data: { error: { message: "8 probes are already running; wait for one to finish" } } };

		expect(getErrorMessage(refusal)).toBe("8 probes are already running; wait for one to finish");
	});

	it("falls back to a plain Error message", () => {
		expect(getErrorMessage(new TypeError("Failed to fetch"))).toBe("Failed to fetch");
	});

	it("names an unreadable failure instead of stringifying a wrapper", () => {
		expect(getErrorMessage({ status: 502 })).toBe("An unexpected error occurred");
		expect(getErrorMessage(undefined)).toBe("An unexpected error occurred");
	});
});