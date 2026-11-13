// The verdict POST /api/models/test returns. A provider that refuses the call is
// still a verdict: ok is false and error carries the provider's own message.
export interface ModelTestResult {
	ok: boolean;
	mode: string;
	provider: string;
	model: string;
	// Present only for a streaming mode: time to the first chunk with output.
	ttft_ms?: number;
	total_ms: number;
	// Provider HTTP status when it refused the call.
	status?: number;
	key_id?: string;
	key_name?: string;
	note?: string;
	// Present (true) only when the probe's own deadline ended it, distinct from a provider refusal.
	timed_out?: boolean;
	error?: string;
}

export interface ModelTestRequest {
	provider: string;
	model: string;
	key_id?: string;
}
