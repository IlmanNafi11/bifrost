import { describe, expect, it } from "vitest";
import { bindingChange, normaliseProbeMode, parseProxyLine, previewProxyImport, proxyStatusTone } from "./proxyPools.utils";

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

describe("bindingChange", () => {
	it("does nothing when the selection is unchanged", () => {
		expect(bindingChange({ pool_id: "p1", mechanism: "random" }, { pool_id: "p1", mechanism: "random" })).toEqual({
			changed: false,
			unbind: false,
			bind: false,
		});
	});

	it("unbinds only when switching away from a pool", () => {
		expect(bindingChange({ pool_id: "p1", mechanism: "random" }, { pool_id: "", mechanism: "random" })).toEqual({
			changed: true,
			unbind: true,
			bind: false,
		});
	});

	it("rebinds when only the mechanism changes on the same pool", () => {
		expect(bindingChange({ pool_id: "p1", mechanism: "random" }, { pool_id: "p1", mechanism: "weighted" })).toEqual({
			changed: true,
			unbind: false,
			bind: true,
		});
	});

	it("switches pools with unbind then bind", () => {
		expect(bindingChange({ pool_id: "p1", mechanism: "random" }, { pool_id: "p2", mechanism: "random" })).toEqual({
			changed: true,
			unbind: true,
			bind: true,
		});
	});

	it("binds a previously unbound key", () => {
		expect(bindingChange({ pool_id: "", mechanism: "random" }, { pool_id: "p1", mechanism: "random" })).toEqual({
			changed: true,
			unbind: false,
			bind: true,
		});
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
