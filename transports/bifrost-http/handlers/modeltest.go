package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// modelProbeTimeout bounds one probe. It is the only guard on a hung provider and
// is deliberately a constant rather than a config: an installation that needs a
// longer probe is better served by fixing the provider than by tuning this.
var modelProbeTimeout = 60 * time.Second

const (
	// modelProbePrompt is the smallest prompt that still forces a real completion.
	modelProbePrompt = "hi"
	// modelProbeMaxTokens keeps a probe cheap; it is enough for a first token and a
	// finish reason.
	modelProbeMaxTokens = 16
)

// modelProbeModes are the catalog modes the probe knows how to exercise. A model
// whose catalog row names any other mode (container_create, live, an unknown
// value) is refused rather than guessed at.
var modelProbeModes = map[string]struct{}{
	"chat":             {},
	"responses":        {},
	"completion":       {},
	"embedding":        {},
	"rerank":           {},
	"video_generation": {},
}

// modelTestPayload is the body of POST /api/models/test.
type modelTestPayload struct {
	Provider schemas.ModelProvider `json:"provider"`
	Model    string                `json:"model"`
	KeyID    string                `json:"key_id,omitempty"`
}

// ModelTestResponse is a probe verdict. A provider that refuses the call is a
// measurement, not a transport failure, so it comes back as ok:false with the
// error it produced.
type ModelTestResponse struct {
	OK       bool   `json:"ok"`
	Mode     string `json:"mode"`
	Model    string `json:"model"`
	Provider string `json:"provider"`
	// TTFTMs is set only for a streaming mode: the time to the first chunk that
	// carried output.
	TTFTMs *int64 `json:"ttft_ms,omitempty"`
	// TotalMs is time to the end of the probe, always set.
	TotalMs int64 `json:"total_ms"`
	// Status is the provider's HTTP status when it refused the call.
	Status  *int   `json:"status,omitempty"`
	KeyID   string `json:"key_id,omitempty"`
	KeyName string `json:"key_name,omitempty"`
	// TimedOut marks a probe its own deadline ended, distinct from a provider refusal.
	TimedOut bool   `json:"timed_out,omitempty"`
	Error    string `json:"error,omitempty"`
}

// testModel handles POST /api/models/test - probe one configured model.
//
// Body: {"provider": "...", "model": "...", "key_id": "..."} (key_id optional).
// The probe mode is read from the model catalog server-side; the caller only names
// what to test. A probe that the provider refuses returns HTTP 200 with ok:false,
// so "the model is misconfigured or dead" is a verdict rather than an endpoint
// failure. Only a malformed request (400), a probe that cannot be started at all
// (500), or a refused caller is an HTTP error.
//
// A probe spends the deployment's provider credentials without being traffic, so it
// is admin-only: refusing has no side effect, while anyone admitted here can spend.
// key_id pins one configured key, which is how an operator checks a specific key.
func (h *ProviderHandler) testModel(ctx *fasthttp.RequestCtx) {
	// A probe spends the deployment's provider credentials while counting as no traffic at all:
	// it is invisible to logs, budgets and provider stats. That is a powerful capability with no
	// audit trail behind it, so it is limited to the local admin, the same floor notifications
	// publishing uses.
	if localAdmin, _ := ctx.UserValue(schemas.IsLocalAdminContextKey).(bool); !localAdmin {
		SendError(ctx, fasthttp.StatusForbidden, "Only administrators can probe a model")
		return
	}
	// IsLocalAdminContextKey is also true for a caller that presented no credential at all, because
	// an auth-disabled deployment treats every caller as the local admin. A probe spends the
	// deployment's provider credentials while leaving no log, budget or stats row behind, so it
	// takes a credential that was actually checked - the same floor native plugin loading uses.
	if bypassed, _ := ctx.UserValue(schemas.BifrostContextKeyAuthBypassed).(bool); bypassed {
		SendError(ctx, fasthttp.StatusForbidden, "Probing a model requires an authenticated administrator")
		return
	}
	var payload modelTestPayload
	if err := sonic.Unmarshal(ctx.PostBody(), &payload); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid request payload")
		return
	}
	payload.Model = strings.TrimSpace(payload.Model)
	payload.Provider = schemas.ModelProvider(strings.TrimSpace(string(payload.Provider)))
	payload.KeyID = strings.TrimSpace(payload.KeyID)
	if payload.Provider == "" || payload.Model == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "provider and model are required")
		return
	}
	// A pinned key must exist on the provider named in the body: the ID alone would otherwise
	// silently fall back to another key of the same provider, and the verdict would report a key
	// the caller never asked for. Ownership is checked before spending anything.
	if payload.KeyID != "" {
		if _, err := h.inMemoryStore.GetProviderKeyRedacted(payload.Provider, payload.KeyID); err != nil {
			if errors.Is(err, lib.ErrNotFound) {
				SendError(ctx, fasthttp.StatusNotFound, fmt.Sprintf("provider %s has no key %s", payload.Provider, payload.KeyID))
				return
			}
			SendError(ctx, fasthttp.StatusInternalServerError, "failed to resolve the requested key")
			return
		}
	}
	mode := h.modelProbeMode(payload.Provider, payload.Model)
	if _, supported := modelProbeModes[mode]; !supported {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("mode not supported for probe: %s", mode))
		return
	}
	if h.client == nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "bifrost client not available")
		return
	}
	// A probe spends real provider tokens, so a probe is a metered action even though it is
	// invisible to budgets: bound how often one may start, and how many a failed probe leaves
	// the provider rate-limiting before the verdict is refused instead of measured. Taken
	// last, so a request rejected on its own shape never costs the caller their interval.
	if refusal := h.probeAdmission.allow(payload.Provider); refusal != "" {
		SendError(ctx, fasthttp.StatusTooManyRequests, refusal)
		return
	}
	defer h.probeAdmission.release()

	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.inMemoryStore)
	defer cancel()
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Failed to convert context")
		return
	}
	// The probe is hidden from logs, cost and stats: these two flags are read by the
	// logging, governance and semantic-cache plugins, and the key is reserved so a
	// plugin cannot clear it.
	bifrostCtx.SetValue(schemas.BifrostContextKeyIsProbe, true)
	bifrostCtx.SetValue(schemas.BifrostContextKeySkipBudgetAndRateLimits, true)
	if payload.KeyID != "" {
		bifrostCtx.SetValue(schemas.BifrostContextKeyAPIKeyID, payload.KeyID)
		// A provider that rejects the call can quote the credential it read. The request path never
		// hands core a literal the caller named, so core cannot recall it into the redaction set;
		// the pin is the caller naming one, and the verdict must not echo it back.
		if literal := h.providerKeyLiteral(payload.Provider, payload.KeyID); literal != "" {
			data, _ := schemas.RedactionDataFromContext(bifrostCtx)
			if data.LiteralReplacements.Input == nil {
				data.LiteralReplacements.Input = map[string]string{}
			}
			data.LiteralReplacements.Input[literal] = "[REDACTED]"
			schemas.SetRedactionDataOnContext(bifrostCtx, data)
		}
	}

	timeout := h.probeTimeout
	if timeout <= 0 {
		timeout = modelProbeTimeout
	}
	probeCtx, probeCancel := schemas.NewBifrostContextWithTimeout(bifrostCtx, timeout)
	defer probeCancel()
	response := h.runModelProbe(probeCtx, payload.Provider, payload.Model, mode, timeout)
	finishProbeTrace(probeCtx)
	caller := probePrincipal(ctx)
	writeProbeAuditLine(probeAuditEvent{
		Principal: caller,
		Provider:  string(payload.Provider),
		Model:     payload.Model,
		Mode:      mode,
		OK:        response.OK,
		TimedOut:  response.TimedOut,
		TotalMs:   response.TotalMs,
		KeyName:   response.KeyName,
	})
	SendJSON(ctx, response)
}

// finishProbeTrace ends the trace a probe created through core and hands it to the observability
// connectors. The probe enters by the management API, whose routes carry no tracing middleware, and
// the logging plugin that would otherwise complete a trace skips probes, so without this a probe
// leaves its trace in the store until the TTL sweep exports it as a completion that never arrived.
// The probe run is over by the time this is called, so nothing can still be adding to the trace;
// a client that was given no tracer stamps no trace ID and this does nothing.
//
// The request's literal redactions are installed first, for the same reason the verdict is scrubbed:
// a provider that rejects a call can quote the credential it read, and that message also lands in
// the span's error attribute. Connectors read those attributes, so the scrub has to reach the trace,
// not just the response body. The input map carries both the pinned key's literal and any
// credential-shaped literal the error text quoted (seeded by runModelProbe), so the trace's error
// attribute is scrubbed by the same set the verdict was.
func finishProbeTrace(ctx *schemas.BifrostContext) {
	tracer, traceID, err := bifrost.GetTracerFromContext(ctx)
	if err != nil || tracer == nil {
		return
	}
	if data, ok := schemas.RedactionDataFromContext(ctx); ok {
		tracer.SetTraceRedactionReplacements(traceID, schemas.RedactionPhaseInput, data.LiteralReplacements.Input)
	}
	tracer.CompleteAndFlushTrace(traceID)
}

// modelProbeMode resolves the mode to probe from the catalog, defaulting to chat
// for a row with no datasheet entry — the same default listModelDetails applies.
func (h *ProviderHandler) modelProbeMode(provider schemas.ModelProvider, model string) string {
	if h.inMemoryStore != nil && h.inMemoryStore.ModelCatalog != nil {
		if entry := h.inMemoryStore.ModelCatalog.GetModelCapabilityEntryForModel(model, provider); entry != nil && entry.Mode != "" {
			return entry.Mode
		}
	}
	return defaultCatalogPricingMode
}

// runModelProbe dispatches one probe and turns the outcome into a verdict. The
// measured result is filled in even when the provider refused the call, so the
// caller can report the key and the mode that were actually exercised. timeout is the
// deadline this probe ran under, reported in the timeout verdict: a handler built
// without one, or a test that shortens it, must name the deadline that applied rather
// than a zero or a stale configured value.
func (h *ProviderHandler) runModelProbe(ctx *schemas.BifrostContext, provider schemas.ModelProvider, model string, mode string, timeout time.Duration) *ModelTestResponse {
	response := &ModelTestResponse{
		Mode:     mode,
		Model:    model,
		Provider: string(provider),
	}
	start := time.Now()
	probeErr := h.dispatchModelProbe(ctx, provider, model, mode, response, start)

	response.TotalMs = time.Since(start).Milliseconds()
	// The resolved key identity is echoed back only when the caller pinned a key: they named it,
	// so they may know it was the one that served the probe. An unpinned probe reports no key,
	// so the endpoint cannot be used to enumerate which keys the deployment routes real traffic
	// through.
	if pinned := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyAPIKeyID); pinned != "" {
		response.KeyID = bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeySelectedKeyID)
		response.KeyName = bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeySelectedKeyName)
	}

	switch {
	case isProbeTimeout(ctx, probeErr):
		response.Error = fmt.Sprintf("probe timed out after %s", timeout)
		response.TimedOut = true
	case probeErr != nil:
		raw := probeErr.GetErrorString()
		// A provider's refusal can quote the credential it rejected; the probe verdict is shown in
		// the dashboard, so the text is passed through the same redaction the response path uses.
		// The credential-shaped literals the shape scrub removes are also seeded as literal
		// replacements, so the same scrub reaches the trace's error attribute.
		seedProbeCredentialLiterals(ctx, raw)
		response.Error = redactProbeError(ctx, raw)
		response.Status = probeErr.StatusCode
	default:
		response.OK = true
	}
	if !response.OK {
		response.TTFTMs = nil
	}
	// The verdict feeds the per-provider backoff: a throttled or timed-out provider is left alone
	// for a growing interval, while a verdict the caller asked for resets it.
	h.probeAdmission.record(provider, response, probeErr)
	return response
}

// dispatchModelProbe sends the mode's payload through the same core entry point a
// real request uses, so a probe exercises routing, key selection and the provider
// converter rather than a shortcut around them.
func (h *ProviderHandler) dispatchModelProbe(ctx *schemas.BifrostContext, provider schemas.ModelProvider, model string, mode string, response *ModelTestResponse, start time.Time) *schemas.BifrostError {
	switch mode {
	case "chat":
		return h.probeChat(ctx, provider, model, response, start)
	case "responses":
		return h.probeResponses(ctx, provider, model, response, start)
	case "completion":
		return h.probeCompletion(ctx, provider, model, response, start)
	case "embedding":
		return h.probeEmbedding(ctx, provider, model)
	case "rerank":
		return h.probeRerank(ctx, provider, model)
	case "video_generation":
		return h.probeVideoGeneration(ctx, provider, model)
	default:
		return &schemas.BifrostError{Error: &schemas.ErrorField{Message: fmt.Sprintf("mode not supported for probe: %s", mode)}}
	}
}

// probeChat runs the chat probe: a streamed "hi" with a token cap, drained to the
// end so an error raised mid-stream is caught, with TTFT taken at the first chunk
// that carries output.
func (h *ProviderHandler) probeChat(ctx *schemas.BifrostContext, provider schemas.ModelProvider, model string, response *ModelTestResponse, start time.Time) *schemas.BifrostError {
	prompt := modelProbePrompt
	maxTokens := modelProbeMaxTokens
	stream, bifrostErr := h.client.ChatCompletionStreamRequest(ctx, &schemas.BifrostChatRequest{
		Provider: provider,
		Model:    model,
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: &prompt},
		}},
		Params: &schemas.ChatParameters{MaxCompletionTokens: &maxTokens},
	})
	if bifrostErr != nil {
		return bifrostErr
	}

	return drainProbeStream(ctx, stream, start, func(chunk *schemas.BifrostStreamChunk) bool {
		if chunk == nil || chunk.BifrostChatResponse == nil {
			return false
		}
		for _, choice := range chunk.BifrostChatResponse.Choices {
			if choice.FinishReason != nil {
				return true
			}
			if choice.Delta != nil && choice.Delta.Content != nil && *choice.Delta.Content != "" {
				return true
			}
		}
		return false
	}, response)
}

// probeResponses runs the Responses API probe: one input message and a token cap,
// drained to the end, TTFT taken at the first event carrying output.
func (h *ProviderHandler) probeResponses(ctx *schemas.BifrostContext, provider schemas.ModelProvider, model string, response *ModelTestResponse, start time.Time) *schemas.BifrostError {
	prompt := modelProbePrompt
	maxTokens := modelProbeMaxTokens
	role := schemas.ResponsesInputMessageRoleUser
	stream, bifrostErr := h.client.ResponsesStreamRequest(ctx, &schemas.BifrostResponsesRequest{
		Provider: provider,
		Model:    model,
		Input: []schemas.ResponsesMessage{{
			Role:    &role,
			Content: &schemas.ResponsesMessageContent{ContentStr: &prompt},
		}},
		Params: &schemas.ResponsesParameters{MaxOutputTokens: &maxTokens},
	})
	if bifrostErr != nil {
		return bifrostErr
	}

	return drainProbeStream(ctx, stream, start, func(chunk *schemas.BifrostStreamChunk) bool {
		if chunk == nil || chunk.BifrostResponsesStreamResponse == nil {
			return false
		}
		event := chunk.BifrostResponsesStreamResponse
		return (event.Delta != nil && *event.Delta != "") || event.Text != nil || event.Item != nil || event.Response != nil
	}, response)
}

// probeCompletion runs the legacy text-completion probe, drained to the end with
// TTFT taken at the first choice carrying text or a finish reason.
func (h *ProviderHandler) probeCompletion(ctx *schemas.BifrostContext, provider schemas.ModelProvider, model string, response *ModelTestResponse, start time.Time) *schemas.BifrostError {
	prompt := modelProbePrompt
	maxTokens := modelProbeMaxTokens
	stream, bifrostErr := h.client.TextCompletionStreamRequest(ctx, &schemas.BifrostTextCompletionRequest{
		Provider: provider,
		Model:    model,
		Input:    &schemas.TextCompletionInput{PromptStr: &prompt},
		Params:   &schemas.TextCompletionParameters{MaxTokens: &maxTokens},
	})
	if bifrostErr != nil {
		return bifrostErr
	}

	return drainProbeStream(ctx, stream, start, func(chunk *schemas.BifrostStreamChunk) bool {
		if chunk == nil || chunk.BifrostTextCompletionResponse == nil {
			return false
		}
		for _, choice := range chunk.BifrostTextCompletionResponse.Choices {
			if choice.FinishReason != nil {
				return true
			}
			if choice.TextCompletionResponseChoice != nil && choice.Text != nil && *choice.Text != "" {
				return true
			}
		}
		return false
	}, response)
}

// probeEmbedding runs the unary embedding probe. A unary mode has no first token to
// time, so only the total latency is reported.
func (h *ProviderHandler) probeEmbedding(ctx *schemas.BifrostContext, provider schemas.ModelProvider, model string) *schemas.BifrostError {
	text := modelProbePrompt
	_, bifrostErr := h.client.EmbeddingRequest(ctx, &schemas.BifrostEmbeddingRequest{
		Provider: provider,
		Model:    model,
		Input: []schemas.EmbeddingInputItem{{
			Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &text}},
		}},
	})
	return bifrostErr
}

// probeRerank runs the unary rerank probe: one query against one document.
func (h *ProviderHandler) probeRerank(ctx *schemas.BifrostContext, provider schemas.ModelProvider, model string) *schemas.BifrostError {
	topN := 1
	_, bifrostErr := h.client.RerankRequest(ctx, &schemas.BifrostRerankRequest{
		Provider:  provider,
		Model:     model,
		Query:     modelProbePrompt,
		Documents: []schemas.RerankDocument{{Text: modelProbePrompt}},
		Params:    &schemas.RerankParameters{TopN: &topN},
	})
	return bifrostErr
}

// probeVideoGeneration runs the unary video probe. It only creates the job: what
// the mode answers is whether the provider accepted the work at all, and waiting
// for a render inside a probe would measure the render, not the model.
func (h *ProviderHandler) probeVideoGeneration(ctx *schemas.BifrostContext, provider schemas.ModelProvider, model string) *schemas.BifrostError {
	_, bifrostErr := h.client.VideoGenerationRequest(ctx, &schemas.BifrostVideoGenerationRequest{
		Provider: provider,
		Model:    model,
		Input:    &schemas.VideoGenerationInput{Prompt: modelProbePrompt},
	})
	return bifrostErr
}

// drainProbeStream reads a probe stream to its end, recording TTFT at the first
// chunk hasOutput accepts and returning the first error the stream carried. A
// cancelled probe context ends the drain with a timeout.
func drainProbeStream(ctx *schemas.BifrostContext, stream chan *schemas.BifrostStreamChunk, start time.Time, hasOutput func(*schemas.BifrostStreamChunk) bool, response *ModelTestResponse) *schemas.BifrostError {
	for {
		select {
		case <-ctx.Done():
			return &schemas.BifrostError{Error: &schemas.ErrorField{Message: ctx.Err().Error()}}
		case chunk, ok := <-stream:
			if !ok {
				return nil
			}
			if chunk == nil {
				continue
			}
			if chunk.BifrostError != nil {
				if chunk.BifrostError.IsBifrostError || chunk.BifrostError.Error != nil {
					return chunk.BifrostError
				}
				continue
			}
			if response.TTFTMs == nil && hasOutput(chunk) {
				elapsed := time.Since(start).Milliseconds()
				response.TTFTMs = &elapsed
			}
		}
	}
}

// isProbeTimeout reports whether the probe stopped because its own deadline expired, which is the
// verdict the operator needs rather than the bare context error a provider may echo.
//
// The probe's own context is authoritative. A provider that answers "context deadline exceeded" on
// its own — its upstream timed out, or a message happens to quote the phrase — is reporting a
// provider failure, not a probe that outlived its budget, so the text alone is not enough: it only
// counts when this probe's deadline has actually expired. A nil error under an expired context means
// the stream ended without a verdict, which is the same timeout seen from the drain's side.
func isProbeTimeout(ctx *schemas.BifrostContext, probeErr *schemas.BifrostError) bool {
	expired := errors.Is(ctx.Err(), context.DeadlineExceeded)
	if probeErr == nil {
		return expired
	}
	if probeErr.Error == nil {
		return false
	}
	// The drain reports its own cancellation with the context's error chained, so that arm is exact.
	if probeErr.Error.Error != nil && errors.Is(probeErr.Error.Error, context.DeadlineExceeded) {
		return true
	}
	return expired && probeErr.Error.Message == context.DeadlineExceeded.Error()
}

// Probe admission bounds. A probe is spend without traffic: it is hidden from logs, cost and
// budgets, so the only limit that applies is one enforced here.
const (
	// probeMinInterval is the shortest gap between two probe starts. A human testing a model
	// presses the button a few times; a loop pressing it spends a key dry. The gap is global —
	// a per-principal bound is keyed on whoever the middleware says is calling (a session token,
	// or the peer IP in an auth-disabled deployment), and a caller can rotate tokens or
	// distribute across addresses, which turns that bound into no bound at all while the probes
	// still spend the same deployment keys.
	probeMinInterval = 2 * time.Second
	// probeFailureBackoff is how long a provider is left alone after a probe failed in a way that
	// points at the provider being throttled or down. Probing a provider that just returned 429 or
	// 503 again only deepens the rate limit, so the next attempt is refused instead of measured.
	probeFailureBackoff = 30 * time.Second
	// probeMaxBackoff caps the repeated-failure backoff.
	probeMaxBackoff = 5 * time.Minute
	// probeMaxInFlight bounds how many probes one process runs at once. Probes share the provider
	// queues and connection pools with real traffic, so an unbounded burst (a loop, or N replicas
	// each at their own pace) contends for the same workers the request path needs. A probe refused
	// here costs nothing: it is rejected before the Bifrost context is even built.
	probeMaxInFlight = 8
)

// probeAuditEvent is the one record a probe leaves behind. Every log store, metric, budget and
// stats surface deliberately skips a probe, which makes a probe unattributable spend — the only
// such endpoint — so the verdict is written to the standard logger (never the request-log store)
// under a dedicated `bifrost.probe` marker. The operator can grep the deployment's own log sink
// for it and reconstruct who probed what and what answered, without the probe polluting the
// provider statistics it must stay out of.
type probeAuditEvent struct {
	Principal string `json:"principal"`
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	Mode      string `json:"mode"`
	OK        bool   `json:"ok"`
	TimedOut  bool   `json:"timed_out,omitempty"`
	TotalMs   int64  `json:"total_ms"`
	KeyName   string `json:"key_name,omitempty"`
}

// writeProbeAuditLine emits the audit record. It never fails the request: an audit sink that is
// slow or broken must not turn a probe into an error.
func writeProbeAuditLine(event probeAuditEvent) {
	if logger == nil {
		return
	}
	if line, err := sonic.Marshal(event); err == nil {
		logger.Info("bifrost.probe %s", string(line))
	}
}

// probeAdmission is the in-process guard for the probe endpoint: a global minimum interval between
// probe starts, a per-provider backoff that grows while probes keep failing and resets on the first
// success, and a per-process in-flight cap. It is intentionally per-process (not shared across
// replicas): its job is to stop a deployment's keys from being spent faster than an operator would
// reasonably press the button, and every replica enforcing it locally already does that.
//
// The interval is deliberately global rather than per-principal. A per-principal bound is keyed on
// whoever the middleware says is calling — a session token, or the peer IP when auth is disabled —
// and a caller can rotate the token or distribute across addresses, which turns that bound into no
// bound at all while every probe still spends the same deployment keys. A single time.Time is the
// bound that holds however many identities the caller presents.
type probeAdmission struct {
	mu sync.Mutex
	// lastStart is when the process last admitted a probe. The interval is global, so a single
	// time.Time replaces the per-principal map and none can be grown by minting principals.
	lastStart    time.Time
	failures     map[schemas.ModelProvider]int
	blockedUntil map[schemas.ModelProvider]time.Time
	// blockedFor is the backoff window that produced blockedUntil, named in the refusal so an
	// operator learns roughly when to retry without the endpoint revealing the exact remainder.
	blockedFor map[schemas.ModelProvider]time.Duration
	// inFlight counts probes currently running. allow refuses once probeMaxInFlight is reached,
	// and release is paired with every allow that admitted.
	inFlight int
}

// allow reserves a probe slot, or reports why the caller may not start one now: the empty string
// means admitted. A reservation is taken even when the probe later fails (the interval measures
// attempts, not wins), and the per-process concurrency cap is checked here too, so an unbounded
// burst is refused before it contends with real traffic on the provider queues. The refusal says
// which waitable bound it hit and for how long: a probe writes no log line, so the response is the
// only place an operator can learn that a probe is coming and roughly when to retry. It never
// names the failing provider or the exact remaining backoff — the deployment's provider health is
// not something every refused caller gets to read off the endpoint.
func (a *probeAdmission) allow(provider schemas.ModelProvider) string {
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	if until, blocked := a.blockedUntil[provider]; blocked {
		if now.Before(until) {
			window := a.blockedFor[provider]
			if window <= 0 {
				window = probeFailureBackoff
			}
			return fmt.Sprintf("a probe against this provider is backing off for about %s; wait before probing again", window)
		}
		delete(a.blockedUntil, provider)
		delete(a.blockedFor, provider)
	}
	if wait := probeMinInterval - now.Sub(a.lastStart); wait > 0 {
		return fmt.Sprintf("a probe was started less than %s ago; wait before starting another", probeMinInterval)
	}
	if a.inFlight >= probeMaxInFlight {
		return "too many probes are already running; wait for one to finish"
	}
	a.lastStart = now
	a.inFlight++
	return ""
}

// release frees the in-flight slot allow reserved. It is paired with every allow that returned
// true, via a defer in the handler, so a probe that panics or is cancelled still frees its slot.
func (a *probeAdmission) release() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.inFlight > 0 {
		a.inFlight--
	}
}

// record applies the verdict to the provider's backoff. A provider that refused the call for a
// throttle or an outage (429/5xx) pushes the next attempt out; a verdict the caller asked for (a
// bad model, a bad key) does not, since the operator is iterating on configuration and the loop is
// already bounded by the global interval. A probe timeout is deliberately not a backoff either: a
// slow provider that outlived the 60s window is re-probeable immediately, because punishing its
// latency by fencing it for 30s and growing is what turns "slow" into "dead" for the operator.
func (a *probeAdmission) record(provider schemas.ModelProvider, response *ModelTestResponse, probeErr *schemas.BifrostError) {
	throttled := isProviderThrottle(probeErr)
	a.mu.Lock()
	defer a.mu.Unlock()
	if !throttled {
		if a.failures != nil {
			delete(a.failures, provider)
		}
		if a.blockedUntil != nil {
			delete(a.blockedUntil, provider)
		}
		if a.blockedFor != nil {
			delete(a.blockedFor, provider)
		}
		return
	}
	if a.failures == nil {
		a.failures = make(map[schemas.ModelProvider]int, 4)
		a.blockedUntil = make(map[schemas.ModelProvider]time.Time, 4)
		a.blockedFor = make(map[schemas.ModelProvider]time.Duration, 4)
	}
	a.failures[provider]++
	backoff := probeFailureBackoff << (a.failures[provider] - 1)
	if backoff <= 0 || backoff > probeMaxBackoff {
		backoff = probeMaxBackoff
	}
	a.blockedUntil[provider] = time.Now().Add(backoff)
	a.blockedFor[provider] = backoff
}

// isProviderThrottle reports whether the provider asked to be left alone (429) or said it cannot
// serve right now (5xx). Those are the failures re-probing makes worse.
func isProviderThrottle(probeErr *schemas.BifrostError) bool {
	if probeErr == nil || probeErr.StatusCode == nil {
		return false
	}
	status := *probeErr.StatusCode
	return status == fasthttp.StatusTooManyRequests || status >= fasthttp.StatusInternalServerError
}

// probePrincipal names the caller for the audit record: the authenticated session when there is
// one, the peer address otherwise (an auth-disabled deployment has no other identity to record).
// The token is hashed rather than kept, so the audit line holds no credential. The admission bound
// is global, so the principal plays no part in whether a probe is admitted.
func probePrincipal(ctx *fasthttp.RequestCtx) string {
	if token, ok := ctx.UserValue(schemas.BifrostContextKeySessionToken).(string); ok && token != "" {
		sum := sha256.Sum256([]byte(token))
		return "session:" + hex.EncodeToString(sum[:16])
	}
	return "ip:" + ctx.RemoteIP().String()
}

// providerKeyLiteral reads one configured key's secret by id, for the probe to redact out of a
// provider error. Empty when the key is unknown or its value is not a readable literal (a
// credential resolved from the environment or a vault has nothing here to replace).
func (h *ProviderHandler) providerKeyLiteral(provider schemas.ModelProvider, keyID string) string {
	if h.inMemoryStore == nil {
		return ""
	}
	// Read through the store's locked accessor: provider configs and their keys are replaced under
	// Config.Mu by the provider and key CRUD paths, and an unlocked read here would race a reload.
	config, err := h.inMemoryStore.GetProviderConfigRaw(provider)
	if err != nil || config == nil {
		return ""
	}
	for i := range config.Keys {
		if config.Keys[i].ID == keyID {
			return config.Keys[i].Value.GetValue()
		}
	}
	return ""
}

// probeCredentialPattern matches provider-key-shaped literals a provider may echo back in an error
// (`sk-...` for OpenAI/Anthropic/OpenRouter, the other vendors' `ak-`/`key-`/`hf_` prefixes, `gsk_`
// for Google, and long unbroken hex/base64 blobs). It is the last line of defence: the pinned path
// seeds the request's redaction map with the exact literal, and the unpinned path relies on the
// pipeline to, but a provider that refuses before key selection completes can still echo the
// credential into a verdict with no redaction map present.
//
// The prefix arm is anchored on a word boundary so a `sk_` fragment inside a longer word (for
// example `desk_x...`, or a partial match inside `gsk_`) is not sliced out of the middle, and its
// tail accepts any length: a real key is short as often as it is long, so a seven-character tail is
// still a credential. The bare-alpha run is bounded by a digit or a mixed case, because model and
// artifact names are long too (`openai/gpt-4o-mini-2024-07-18`, base64 image ids) and redacting
// those would hide the answer the operator pressed the button to get. A secret is a key shape, not
// merely a long word.
var probeCredentialPattern = regexp.MustCompile(
	`(?i)\b(?:sk|ak|key|hf|pk|gsk)[-_][A-Za-z0-9_\-]{1,}|\b[0-9a-fA-F]{32,}\b|\b[A-Za-z0-9+/_\-]{40,}={0,2}\b`,
)

// probeMixedAlnumPattern narrows the base64 arm the same way: a long run of only letters (a
// hyphenated model name, a word chain) is not a credential, while one carrying digits or mixed case
// is.
var probeMixedAlnumPattern = regexp.MustCompile(`\d|[a-z][A-Z]|[A-Z][a-z]`)

// redactProbeError passes a provider error through the request's redaction data, so a message that
// quotes the credential a provider rejected or any other request content leaves the endpoint the
// same way it would leave a logged response. The credential-shape scrub runs after the literal
// replacements in every case: a message can carry both a seeded literal and a second credential the
// map never learned about (a key the provider failed over to), so the two are not alternatives.
func redactProbeError(ctx *schemas.BifrostContext, text string) string {
	if data, ok := schemas.RedactionDataFromContext(ctx); ok {
		if len(data.LiteralReplacements.Input) > 0 {
			text = schemas.ApplyLiteralReplacements(text, data.LiteralReplacements.Input)
		}
	}
	return probeCredentialPattern.ReplaceAllStringFunc(text, func(match string) string {
		if !probeMixedAlnumPattern.MatchString(match) {
			return match
		}
		return "[REDACTED]"
	})
}

// seedProbeCredentialLiterals folds the credential-shaped literals a provider error quotes into the
// request's input replacements, so the same literals the verdict scrubs by shape are also scrubbed
// from the trace. The verdict's shape scrub runs over the body only; the trace's error attribute is
// exported to every connector and is scrubbed by literal replacement, so without seeding here a key
// the literal map never learned about (a provider that failed over to a second key, or refused
// before selection) would leave the body clean but the trace leaking.
func seedProbeCredentialLiterals(ctx *schemas.BifrostContext, text string) {
	if ctx == nil || text == "" {
		return
	}
	seeded := map[string]string{}
	probeCredentialPattern.ReplaceAllStringFunc(text, func(match string) string {
		if probeMixedAlnumPattern.MatchString(match) {
			seeded[match] = "[REDACTED]"
		}
		return match
	})
	if len(seeded) == 0 {
		return
	}
	data, _ := schemas.RedactionDataFromContext(ctx)
	if data.LiteralReplacements.Input == nil {
		data.LiteralReplacements.Input = map[string]string{}
	}
	for literal, placeholder := range seeded {
		data.LiteralReplacements.Input[literal] = placeholder
	}
	schemas.SetRedactionDataOnContext(ctx, data)
}
