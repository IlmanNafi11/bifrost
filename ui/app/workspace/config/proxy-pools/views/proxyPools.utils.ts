import type { ProxyProbeMode } from "@/lib/types/proxyPools";

export interface ImportPreview {
	unique: number;
	invalid: string[];
}

// Returns the canonical host:port dedupe key for one import line, or null when
// the line is not one of the four accepted forms. This is a UI-only preview:
// the backend ParseImport is authoritative and rejects anything this accepts.
export function parseProxyLine(line: string): string | null {
	let rest = line.trim();

	if (rest.includes("://")) {
		const separator = rest.indexOf("://");
		const scheme = rest.slice(0, separator);
		if (scheme !== "http" && scheme !== "https" && scheme !== "socks5") return null;
		rest = rest.slice(separator + 3);
	}

	const at = rest.lastIndexOf("@");
	if (at >= 0) {
		const credentials = rest.slice(0, at);
		if (!credentials.includes(":")) return null;
		rest = rest.slice(at + 1);
	}

	if (!rest || /[/?#]/.test(rest)) return null;

	const colon = rest.lastIndexOf(":");
	if (colon <= 0) return null;
	const host = rest.slice(0, colon);
	const port = rest.slice(colon + 1);
	if (!host || !/^\d+$/.test(port)) return null;
	const portNumber = Number(port);
	if (portNumber < 1 || portNumber > 65535) return null;

	return `${host.toLowerCase()}:${portNumber}`;
}

export function previewProxyImport(text: string): ImportPreview {
	const seen = new Set<string>();
	const invalid: string[] = [];
	for (const raw of text.split(/\r?\n/)) {
		const line = raw.trim();
		if (!line || line.startsWith("#")) continue;
		const key = parseProxyLine(line);
		if (!key) {
			invalid.push(line);
			continue;
		}
		seen.add(key);
	}
	return { unique: seen.size, invalid };
}

export type ProxyStatusTone = "success" | "destructive" | "secondary";

export function proxyStatusTone(status: string): ProxyStatusTone {
	if (status === "alive") return "success";
	if (status === "dead") return "destructive";
	return "secondary";
}

// Mode B is the only mode that fetches the egress IP; the choice must be visible
// in the request so a user never assumes an IP from a connect-only probe.
export function normaliseProbeMode(mode: ProxyProbeMode | undefined): ProxyProbeMode {
	return mode === "b" ? "b" : "a";
}
