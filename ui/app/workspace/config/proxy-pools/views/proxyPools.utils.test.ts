import { describe, expect, it } from "vitest";
import { normaliseProbeMode, parseProxyLine, previewProxyImport, proxyStatusTone } from "./proxyPools.utils";

describe("parseProxyLine", () => {
	it("accepts the four import forms", () => {
		expect(parseProxyLine("proxy.example.com:8080")).toBe("proxy.example.com:8080");
		expect(parseProxyLine("http://proxy.example.com:8080")).toBe("proxy.example.com:8080");
		expect(parseProxyLine("socks5://user:pass@proxy.example.com:1080")).toBe("proxy.example.com:1080");
		expect(parseProxyLine("user:pass@proxy.example.com:8080")).toBe("proxy.example.com:8080");
	});

	it("rejects unsupported schemes, missing ports, paths and malformed credentials", () => {
		expect(parseProxyLine("ftp://proxy.example.com:8080")).toBeNull();
		expect(parseProxyLine("proxy.example.com")).toBeNull();
		expect(parseProxyLine("proxy.example.com:0")).toBeNull();
		expect(parseProxyLine("proxy.example.com:70000")).toBeNull();
		expect(parseProxyLine("proxy.example.com:8080/path")).toBeNull();
		expect(parseProxyLine("user@proxy.example.com:8080")).toBeNull();
	});
});

describe("previewProxyImport", () => {
	it("skips blanks and comments and dedupes by host:port", () => {
		const preview = previewProxyImport(
			["", "# a comment", "proxy.example.com:8080", "http://proxy.example.com:8080", "other.example.com:1080"].join("\n"),
		);
		expect(preview.unique).toBe(2);
		expect(preview.invalid).toEqual([]);
	});

	it("reports garbage lines without dropping valid ones", () => {
		const preview = previewProxyImport("garbage\nproxy.example.com:8080\nproxy.example.com:8080/path");
		expect(preview.unique).toBe(1);
		expect(preview.invalid).toEqual(["garbage", "proxy.example.com:8080/path"]);
	});
});

describe("proxy display helpers", () => {
	it("maps status to a badge tone", () => {
		expect(proxyStatusTone("alive")).toBe("success");
		expect(proxyStatusTone("dead")).toBe("destructive");
		expect(proxyStatusTone("unknown")).toBe("secondary");
	});

	it("defaults probe mode to A and keeps B", () => {
		expect(normaliseProbeMode(undefined)).toBe("a");
		expect(normaliseProbeMode("b")).toBe("b");
	});
});
