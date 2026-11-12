package handlers

import (
	"context"
	"fmt"
	"strings"
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
	"chat":      {},
	"embedding": {},
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
	Error   string `json:"error,omitempty"`
}

// testModel handles POST /api/models/test - probe one configured model.
//
// Body: {"provider": "...", "model": "...", "key_id": "..."} (key_id optional).
// The probe mode is read from the model catalog server-side; the caller only names
// what to test. A probe that the provider refuses returns HTTP 200 with ok:false,
// so "the model is misconfigured or dead" is a verdict rather than an endpoint
// failure. Only a malformed request (400) or a probe that cannot be started at all
// (500) is an HTTP error.
func (h *ProviderHandler) testModel(ctx *fasthttp.RequestCtx) {
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

	mode := h.modelProbeMode(payload.Provider, payload.Model)
	if _, supported := modelProbeModes[mode]; !supported {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("mode not supported for probe: %s", mode))
		return
	}
	if h.client == nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "bifrost client not available")
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
		response.Error = fmt.Sprintf("timeout setelah %s", modelProbeTimeout)
	case probeErr != nil:
		response.Error = probeErr.GetErrorString()
		response.Status = probeErr.StatusCode
	default:
		response.OK = true
	}
	if !response.OK {
		response.TTFTMs = nil
	}
	return response
}

// dispatchModelProbe sends the mode's payload through the same core entry point a
// real request uses, so a probe exercises routing, key selection and the provider
// converter rather than a shortcut around them.
func (h *ProviderHandler) dispatchModelProbe(ctx *schemas.BifrostContext, provider schemas.ModelProvider, model string, mode string, response *ModelTestResponse, start time.Time) *schemas.BifrostError {
	switch mode {
	case "chat":
		return h.probeChat(ctx, provider, model, response, start)
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

// isProbeTimeout reports whether the probe stopped because its deadline expired,
// which is the verdict the operator needs to see rather than the bare
// context-deadline message a provider may echo.
func isProbeTimeout(ctx *schemas.BifrostContext, probeErr *schemas.BifrostError) bool {
	if ctx.Err() == nil {
		return false
	}
	if probeErr == nil {
		return true
	}
	if probeErr.Error != nil && strings.Contains(strings.ToLower(probeErr.Error.Message), context.DeadlineExceeded.Error()) {
		return true
	}
	return false
}
