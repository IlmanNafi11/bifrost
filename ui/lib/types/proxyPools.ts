// Proxy pool resources. Mirrors the shapes served by /api/proxy-pools (Task 8).
// The binding is the single source of truth for a key's pool; the proxy_pool_*
// fields on a key are a read-only projection of it.

export const PROXY_MECHANISMS = ["round_robin", "random", "weighted", "least_connections", "fallback", "sticky_session"] as const;

export type ProxyMechanism = (typeof PROXY_MECHANISMS)[number];

export const PROXY_PROBE_MODES = ["a", "b"] as const;
export type ProxyProbeMode = (typeof PROXY_PROBE_MODES)[number];

export type ProxyType = "http" | "socks5";

export interface PoolProxy {
	id: string;
	pool_id: string;
	type: ProxyType;
	url: string;
	username: string;
	weight: number;
	enabled: boolean;
	status: string;
	latency_ms: number;
	egress_ip: string;
	last_tested_at?: string;
}

export interface ProxyPool {
	id: string;
	name: string;
	description: string;
	proxies: PoolProxy[];
}

// Runtime counters on the list response; omitted when no pool store is configured.
export interface ProxyPoolStats {
	selections?: Partial<Record<ProxyMechanism, number>>;
	pool_exhausted?: number;
	marked_dead?: number;
	cooled_down?: number;
	probes_alive?: number;
	probes_dead?: number;
	last_probe_latency_ms?: number;
}

export interface ProxyPoolMutation {
	name?: string;
	description?: string;
}

export interface ProxyPoolImportRequest {
	text: string;
}

export interface ProxyPoolProbeRequest {
	ids?: string[];
	mode?: ProxyProbeMode;
}

export interface ProxyProbeResult {
	id: string;
	status: string;
	latency_ms: number;
	egress_ip?: string;
	error?: string;
}

export interface ProxyPoolIDsRequest {
	ids: string[];
}

export interface ProxyPoolKeysRequest {
	key_ids: string[];
	mechanism: ProxyMechanism;
}

// The per-key read projection on a provider key (never sent back on key save).
export interface ProxyKeyBindingProjection {
	proxy_pool_id?: string;
	proxy_pool_mechanism?: ProxyMechanism;
}