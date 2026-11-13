package handlers

import (
	"context"
	"errors"
	"fmt"
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
	Note    string `json:"note,omitempty"`
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
	// One probe spends real provider tokens, so a probe is a metered action even though it is
	// invisible to budgets: bound how many one principal can start, and how many a failed probe
	// leaves the provider rate-limiting before the verdict is refused instead of measured. Taken
	// last, so a request rejected on its own shape never costs the caller their interval.
	caller := probePrincipal(ctx)
	if !h.probeAdmission.allow(caller, payload.Provider) {
		SendError(ctx, fasthttp.StatusTooManyRequests, "too many probes; wait before probing again")
		return
	}

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
	}

	probeCtx, probeCancel := schemas.NewBifrostContextWithTimeout(bifrostCtx, modelProbeTimeout)
	defer probeCancel()

	response := h.runModelProbe(probeCtx, payload.Provider, payload.Model, mode)
	SendJSON(ctx, response)
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
// caller can report the key and the mode that were actually exercised.
func (h *ProviderHandler) runModelProbe(ctx *schemas.BifrostContext, provider schemas.ModelProvider, model string, mode string) *ModelTestResponse {
	response := &ModelTestResponse{
		Mode:     mode,
		Model:    model,
		Provider: string(provider),
	}
	start := time.Now()
	probeErr := h.dispatchModelProbe(ctx, provider, model, mode, response, start)

	response.TotalMs = time.Since(start).Milliseconds()
	response.KeyID = bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeySelectedKeyID)
	response.KeyName = bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeySelectedKeyName)

	switch {
	case isProbeTimeout(ctx, probeErr):
		response.Error = fmt.Sprintf("probe timed out after %s", modelProbeTimeout)
		response.TimedOut = true
	case probeErr != nil:
		// A provider's refusal can quote the credential it rejected; the probe verdict is shown in
		// the dashboard, so the text is passed through the same redaction the response path uses.
		response.Error = redactProbeError(ctx, probeErr.GetErrorString())
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
// verdict the operator needs rather than the bare context error a provider may echo. The check is on
// the error itself (context.DeadlineExceeded chained, or named in the message) because a client that
// cancels its own request also leaves ctx.Err() non-nil, and that is a cancelled probe, not a slow
// provider. A nil error with an expired context means the stream ended without a verdict.
func isProbeTimeout(ctx *schemas.BifrostContext, probeErr *schemas.BifrostError) bool {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) && probeErr == nil {
		return true
	}
	if probeErr == nil || probeErr.Error == nil {
		return false
	}
	if probeErr.Error.Message == "" {
		return false
	}
	if errors.Is(probeErr.Error.Error, context.DeadlineExceeded) {
		return true
	}
	return strings.Contains(strings.ToLower(probeErr.Error.Message), context.DeadlineExceeded.Error())
}

// Probe admission bounds. A probe is spend without traffic: it is hidden from logs, cost and
// budgets, so the only limit that applies is one enforced here.
const (
	// probeMinInterval is the shortest gap between two probes one principal may start. A human
	// testing a model presses the button a few times; a loop pressing it spends a key dry.
	probeMinInterval = 2 * time.Second
	// probeFailureBackoff is how long a provider is left alone after a probe failed in a way that
	// points at the provider being throttled or down. Probing a provider that just returned 429 or
	// 503 again only deepens the rate limit, so the next attempt is refused instead of measured.
	probeFailureBackoff = 30 * time.Second
	// probeMaxBackoff caps the repeated-failure backoff.
	probeMaxBackoff = 5 * time.Minute
)

// probeAdmission is the in-process guard for the probe endpoint: a per-principal minimum interval
// plus a per-provider backoff that grows while probes keep failing and resets on the first success.
// It is intentionally per-process (not shared across replicas): its job is to stop one caller or one
// broken provider from being hammered, and every replica enforcing it locally already does that.
type probeAdmission struct {
	mu           sync.Mutex
	lastStart    map[string]time.Time
	failures     map[schemas.ModelProvider]int
	blockedUntil map[schemas.ModelProvider]time.Time
}

// allow reports whether the caller may start a probe against provider now, and reserves the slot.
// A reservation is taken even when the probe later fails: the interval measures attempts, not wins.
func (a *probeAdmission) allow(caller string, provider schemas.ModelProvider) bool {
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	if until, blocked := a.blockedUntil[provider]; blocked {
		if now.Before(until) {
			return false
		}
		delete(a.blockedUntil, provider)
	}
	if last, ok := a.lastStart[caller]; ok && now.Sub(last) < probeMinInterval {
		return false
	}
	if a.lastStart == nil {
		a.lastStart = make(map[string]time.Time, 8)
	}
	a.lastStart[caller] = now
	return true
}

// record applies the verdict to the provider's backoff. A probe that reached the provider and was
// refused for a throttle or an outage pushes the next attempt out; a verdict the caller asked for
// (a bad model, a bad key) does not, since the operator is iterating on configuration and the loop
// is already bounded by the per-principal interval.
func (a *probeAdmission) record(provider schemas.ModelProvider, response *ModelTestResponse, probeErr *schemas.BifrostError) {
	throttled := isProviderThrottle(probeErr) || (response != nil && response.TimedOut)
	a.mu.Lock()
	defer a.mu.Unlock()
	if !throttled {
		if a.failures != nil {
			delete(a.failures, provider)
		}
		if a.blockedUntil != nil {
			delete(a.blockedUntil, provider)
		}
		return
	}
	if a.failures == nil {
		a.failures = make(map[schemas.ModelProvider]int, 4)
		a.blockedUntil = make(map[schemas.ModelProvider]time.Time, 4)
	}
	a.failures[provider]++
	backoff := probeFailureBackoff << (a.failures[provider] - 1)
	if backoff <= 0 || backoff > probeMaxBackoff {
		backoff = probeMaxBackoff
	}
	a.blockedUntil[provider] = time.Now().Add(backoff)
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

// probePrincipal names the caller for admission accounting: the authenticated session when there is
// one, the peer address otherwise (an auth-disabled deployment has no other identity to count).
func probePrincipal(ctx *fasthttp.RequestCtx) string {
	if token, ok := ctx.UserValue(schemas.BifrostContextKeySessionToken).(string); ok && token != "" {
		return token
	}
	return "ip:" + ctx.RemoteIP().String()
}

// redactProbeError passes a provider error through the request's redaction data, so a message that
// quotes the credential a provider rejected or any other request content leaves the endpoint the
// same way it would leave a logged response.
func redactProbeError(ctx *schemas.BifrostContext, text string) string {
	data, ok := schemas.RedactionDataFromContext(ctx)
	if !ok {
		return text
	}
	if len(data.LiteralReplacements.Input) > 0 {
		text = schemas.ApplyLiteralReplacements(text, data.LiteralReplacements.Input)
	}
	if len(data.ReversibleMappings.Output) > 0 {
		text = schemas.ApplyLiteralReplacements(text, data.ReversibleMappings.Output)
	}
	return text
}
