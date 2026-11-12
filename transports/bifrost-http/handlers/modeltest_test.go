package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// probeObserved is what a stub LLM plugin saw of one probe request.
type probeObserved struct {
	mu        sync.Mutex
	isProbe   []bool
	skipLim   []bool
	keyID     string
	requestID string
	calls     int32
}

func (o *probeObserved) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	isProbe, _ := ctx.Value(schemas.BifrostContextKeyIsProbe).(bool)
	skip, _ := ctx.Value(schemas.BifrostContextKeySkipBudgetAndRateLimits).(bool)
	keyID, _ := ctx.Value(schemas.BifrostContextKeyAPIKeyID).(string)
	o.isProbe = append(o.isProbe, isProbe)
	o.skipLim = append(o.skipLim, skip)
	o.keyID = keyID
	o.requestID, _ = ctx.Value(schemas.BifrostContextKeyRequestID).(string)
	return req, nil, nil
}

func (o *probeObserved) PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	return resp, err, nil
}

func (o *probeObserved) GetName() string { return "probe-observer" }

func (o *probeObserved) Cleanup() error { return nil }

func (o *probeObserved) PreRequestHook(_ *schemas.BifrostContext, _ *schemas.BifrostRequest) error {
	return nil
}

// probeCatalog returns a catalog whose rows name the mode of each (provider, model)
// pair, so the handler resolves the probe mode the way production does.
func probeCatalog(tb testing.TB, rows map[string]string) *modelcatalog.ModelCatalog {
	tb.Helper()
	entries := make(map[string]string, len(rows))
	for key, mode := range rows {
		// key is "provider/model" in the fixture; the datasheet wants them split.
		var provider, model string
		for i := len(key) - 1; i >= 0; i-- {
			if key[i] == '/' {
				provider, model = key[:i], key[i+1:]
				break
			}
		}
		entries[key] = fmt.Sprintf(`{"provider":%q,"mode":%q,"base_model":%q}`, provider, mode, model)
	}
	body := "{"
	first := true
	for key, value := range entries {
		if !first {
			body += ","
		}
		first = false
		body += fmt.Sprintf("%q:%s", key, value)
	}
	body += "}"
	path := filepath.Join(tb.TempDir(), "pricing.json")
	require.NoError(tb, os.WriteFile(path, []byte(body), 0o600))
	ds := datasheet.New(nil, nil, datasheet.Config{URL: "file://" + path})
	require.NoError(tb, ds.LoadFromURLIntoMemory(context.Background()))
	return modelcatalog.NewTestCatalogWithDatasheet(ds)
}

// newProbeHandler wires a provider handler over a real Bifrost client whose OpenAI
// provider points at upstream, plus an observer plugin that records the probe
// markers every hook saw.
func newProbeHandler(tb testing.TB, upstreamURL string, catalog *modelcatalog.ModelCatalog) (*ProviderHandler, *probeObserved) {
	tb.Helper()
	return newProbeHandlerWithProvider(tb, schemas.OpenAI, upstreamURL, catalog, nil)
}

// newProbeHandlerWithProvider is newProbeHandler with the provider name and custom
// provider config under test, for modes a built-in provider only serves through a
// custom (OpenAI-compatible) configuration.
func newProbeHandlerWithProvider(tb testing.TB, providerName schemas.ModelProvider, upstreamURL string, catalog *modelcatalog.ModelCatalog, custom *schemas.CustomProviderConfig) (*ProviderHandler, *probeObserved) {
	tb.Helper()
	log := bifrost.NewNoOpLogger()
	SetLogger(log)
	config := &lib.Config{
		ClientConfig: &configstore.ClientConfig{},
		ModelCatalog: catalog,
		Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
			providerName: {
				Keys:                     []schemas.Key{{ID: "key-openai", Name: "prod-2", Value: *schemas.NewSecretVar("sk-openai"), Weight: 1, Models: schemas.WhiteList{"*"}}},
				NetworkConfig:            &schemas.NetworkConfig{BaseURL: upstreamURL, AllowPrivateNetwork: true},
				ConcurrencyAndBufferSize: &schemas.ConcurrencyAndBufferSize{Concurrency: 2, BufferSize: 10},
				CustomProviderConfig:     custom,
			},
		},
	}
	observer := &probeObserved{}
	client, err := bifrost.Init(tb.Context(), schemas.BifrostConfig{
		Account:      lib.NewBaseAccount(config),
		Logger:       log,
		ModelCatalog: catalog,
		LLMPlugins:   []schemas.LLMPlugin{observer},
	})
	require.NoError(tb, err)
	tb.Cleanup(client.Shutdown)
	config.SetBifrostClient(client)
	return NewProviderHandler(nil, config, client), observer
}

// chatUpstream streams one content chunk and a terminal frame.
func chatUpstream(tb testing.TB, delay time.Duration, calls *atomic.Int32) *httptest.Server {
	tb.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			tb.Errorf("unexpected upstream path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if calls != nil {
			calls.Add(1)
		}
		var body map[string]any
		_ = sonic.Unmarshal(readAll(tb, r), &body)
		if stream, _ := body["stream"].(bool); !stream {
			tb.Errorf("probe must stream, got stream=%v", body["stream"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		time.Sleep(delay)
		fmt.Fprint(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-4o-mini\","+
			"\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		fmt.Fprint(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-4o-mini\","+
			"\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	tb.Cleanup(server.Close)
	return server
}

func readAll(tb testing.TB, r *http.Request) []byte {
	tb.Helper()
	data := make([]byte, 0, 512)
	buf := make([]byte, 512)
	for {
		n, err := r.Body.Read(buf)
		data = append(data, buf[:n]...)
		if err != nil {
			return data
		}
	}
}

func probeRequest(t *testing.T, payload any) *fasthttp.RequestCtx {
	t.Helper()
	body, err := sonic.Marshal(payload)
	require.NoError(t, err)
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod(fasthttp.MethodPost)
	ctx.Request.SetRequestURI("/api/models/test")
	ctx.Request.Header.SetContentType("application/json")
	ctx.Request.SetBody(body)
	return ctx
}

func decodeProbe(t *testing.T, ctx *fasthttp.RequestCtx) ModelTestResponse {
	t.Helper()
	var response ModelTestResponse
	require.NoError(t, sonic.Unmarshal(ctx.Response.Body(), &response), string(ctx.Response.Body()))
	return response
}

func TestModelProbe_ChatSuccessReportsTTFTAndTotal(t *testing.T) {
	catalog := probeCatalog(t, map[string]string{"openai/gpt-4o-mini": "chat"})
	h, observer := newProbeHandler(t, chatUpstream(t, 0, nil).URL, catalog)

	ctx := probeRequest(t, modelTestPayload{Provider: schemas.OpenAI, Model: "gpt-4o-mini"})
	h.testModel(ctx)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))

	response := decodeProbe(t, ctx)
	assert.True(t, response.OK)
	assert.Equal(t, "chat", response.Mode)
	assert.Equal(t, "openai", response.Provider)
	assert.Equal(t, "gpt-4o-mini", response.Model)
	require.NotNil(t, response.TTFTMs, "a streaming probe reports TTFT")
	assert.GreaterOrEqual(t, *response.TTFTMs, int64(0))
	assert.GreaterOrEqual(t, response.TotalMs, *response.TTFTMs)
	assert.Empty(t, response.Error)
	assert.Equal(t, "key-openai", response.KeyID, "the response names the key that actually served the probe")
	assert.Equal(t, "prod-2", response.KeyName)

	observer.mu.Lock()
	defer observer.mu.Unlock()
	require.Len(t, observer.isProbe, 1)
	assert.True(t, observer.isProbe[0], "the probe marker must be visible to every plugin")
	assert.True(t, observer.skipLim[0], "a probe must not consume budget or rate limits")
}

func TestModelProbe_ProviderErrorIsAVerdictNotAnHTTPError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"invalid api key","type":"invalid_request_error"}}`)
	}))
	t.Cleanup(upstream.Close)
	catalog := probeCatalog(t, map[string]string{"openai/gpt-4o-mini": "chat"})
	h, _ := newProbeHandler(t, upstream.URL, catalog)

	ctx := probeRequest(t, modelTestPayload{Provider: schemas.OpenAI, Model: "gpt-4o-mini"})
	h.testModel(ctx)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))

	response := decodeProbe(t, ctx)
	assert.False(t, response.OK)
	assert.Contains(t, response.Error, "invalid api key")
	require.NotNil(t, response.Status)
	assert.Equal(t, http.StatusUnauthorized, *response.Status)
	assert.Nil(t, response.TTFTMs, "a failed probe carries no timing that would read as success")
}

func TestModelProbe_TimeoutIsReportedAsAVerdict(t *testing.T) {
	upstream := chatUpstream(t, 0, nil)
	// Hold the connection past the (shortened) probe deadline. The handler is released
	// by the client aborting at the deadline, so the test must not wait for it at
	// Cleanup: upstream.Close is deferred before the body returns.
	upstream.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	})
	defer upstream.Close()
	catalog := probeCatalog(t, map[string]string{"openai/gpt-4o-mini": "chat"})
	h, _ := newProbeHandler(t, upstream.URL, catalog)

	previous := modelProbeTimeout
	modelProbeTimeout = 300 * time.Millisecond
	t.Cleanup(func() { modelProbeTimeout = previous })

	ctx := probeRequest(t, modelTestPayload{Provider: schemas.OpenAI, Model: "gpt-4o-mini"})
	h.testModel(ctx)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))

	response := decodeProbe(t, ctx)
	assert.False(t, response.OK)
	assert.Regexp(t, `^timeout setelah 300`, response.Error, "a deadline is reported as a timeout verdict, not the raw context error")
	assert.Nil(t, response.TTFTMs, "a timeout reports no first-token time")
}

func TestModelProbe_RejectsIncompletePayloadAndUnknownMode(t *testing.T) {
	catalog := probeCatalog(t, map[string]string{
		"openai/gpt-4o-mini":    "chat",
		"openai/container-only": "container_create",
	})
	h, _ := newProbeHandler(t, chatUpstream(t, 0, nil).URL, catalog)

	for _, tc := range []struct {
		name    string
		payload modelTestPayload
		want    string
	}{
		{"missing model", modelTestPayload{Provider: schemas.OpenAI}, "provider and model are required"},
		{"missing provider", modelTestPayload{Model: "gpt-4o-mini"}, "provider and model are required"},
		{"unprobeable mode", modelTestPayload{Provider: schemas.OpenAI, Model: "container-only"}, "mode not supported for probe"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := probeRequest(t, tc.payload)
			h.testModel(ctx)
			assert.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode(), string(ctx.Response.Body()))
			assert.Contains(t, string(ctx.Response.Body()), tc.want)
		})
	}

	invalid := &fasthttp.RequestCtx{}
	invalid.Request.Header.SetMethod(fasthttp.MethodPost)
	invalid.Request.SetRequestURI("/api/models/test")
	invalid.Request.SetBody([]byte("{not json"))
	h.testModel(invalid)
	assert.Equal(t, fasthttp.StatusBadRequest, invalid.Response.StatusCode())
}

func TestModelProbe_KeyIDIsPinnedAndReported(t *testing.T) {
	catalog := probeCatalog(t, map[string]string{"openai/gpt-4o-mini": "chat"})
	h, observer := newProbeHandler(t, chatUpstream(t, 0, nil).URL, catalog)
	// A second key must not be chosen when one is pinned by ID.
	h.inMemoryStore.Providers[schemas.OpenAI] = func() configstore.ProviderConfig {
		cfg := h.inMemoryStore.Providers[schemas.OpenAI]
		cfg.Keys = append(cfg.Keys, schemas.Key{ID: "key-other", Name: "other", Value: *schemas.NewSecretVar("sk-other"), Weight: 100, Models: schemas.WhiteList{"*"}})
		return cfg
	}()
	require.NoError(t, h.client.UpdateProvider(schemas.OpenAI))

	ctx := probeRequest(t, modelTestPayload{Provider: schemas.OpenAI, Model: "gpt-4o-mini", KeyID: "key-openai"})
	h.testModel(ctx)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))

	response := decodeProbe(t, ctx)
	require.True(t, response.OK, response.Error)
	assert.Equal(t, "key-openai", response.KeyID, "the pinned key must be the one used")

	observer.mu.Lock()
	defer observer.mu.Unlock()
	require.NotEmpty(t, observer.isProbe)
	assert.Equal(t, "key-openai", observer.keyID)
}

func TestModelProbe_RouteIsRegistered(t *testing.T) {
	catalog := probeCatalog(t, map[string]string{"openai/gpt-4o-mini": "chat"})
	h, _ := newProbeHandler(t, chatUpstream(t, 0, nil).URL, catalog)
	r := router.New()
	h.RegisterRoutes(r)

	ctx := probeRequest(t, modelTestPayload{Provider: schemas.OpenAI, Model: "gpt-4o-mini"})
	r.Handler(ctx)
	assert.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), "POST /api/models/test must be routed to the handler: %s", ctx.Response.Body())
	assert.True(t, decodeProbe(t, ctx).OK)
}

// responsesUpstream streams one output-text delta and a terminal event.
func responsesUpstream(tb testing.TB) *httptest.Server {
	tb.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			tb.Errorf("unexpected upstream path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		fmt.Fprint(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":1,\"delta\":\"hi\"}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":2,\"response\":{\"id\":\"r1\",\"model\":\"gpt-4o-mini\",\"status\":\"completed\"}}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	tb.Cleanup(server.Close)
	return server
}

// completionUpstream streams one text delta and a terminal frame.
func completionUpstream(tb testing.TB) *httptest.Server {
	tb.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/completions" {
			tb.Errorf("unexpected upstream path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"id\":\"t1\",\"object\":\"text_completion\",\"created\":1,\"model\":\"gpt-3.5-turbo-instruct\","+
			"\"choices\":[{\"index\":0,\"text\":\"hi\",\"finish_reason\":null}]}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		fmt.Fprint(w, "data: {\"id\":\"t1\",\"object\":\"text_completion\",\"created\":1,\"model\":\"gpt-3.5-turbo-instruct\","+
			"\"choices\":[{\"index\":0,\"text\":\"\",\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	tb.Cleanup(server.Close)
	return server
}

func TestModelProbeModeDispatch_StreamingModesReportTTFT(t *testing.T) {
	cases := []struct {
		mode     string
		model    string
		upstream func(testing.TB) *httptest.Server
	}{
		{"chat", "gpt-4o-mini", func(tb testing.TB) *httptest.Server { return chatUpstream(tb, 0, nil) }},
		{"responses", "gpt-4o-mini", responsesUpstream},
		{"completion", "gpt-3.5-turbo-instruct", completionUpstream},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			upstream := tc.upstream(t)
			catalog := probeCatalog(t, map[string]string{"openai/" + tc.model: tc.mode})
			h, _ := newProbeHandler(t, upstream.URL, catalog)

			ctx := probeRequest(t, modelTestPayload{Provider: schemas.OpenAI, Model: tc.model})
			h.testModel(ctx)
			require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))

			response := decodeProbe(t, ctx)
			require.True(t, response.OK, "%s: %s", tc.mode, response.Error)
			assert.Equal(t, tc.mode, response.Mode)
			require.NotNil(t, response.TTFTMs, "%s is a streaming mode and must report TTFT", tc.mode)
			assert.GreaterOrEqual(t, response.TotalMs, *response.TTFTMs)
		})
	}
}

func TestModelProbeModeDispatch_UnrecognizedModeIsRejected(t *testing.T) {
	catalog := probeCatalog(t, map[string]string{"openai/live-model": "live"})
	h, _ := newProbeHandler(t, chatUpstream(t, 0, nil).URL, catalog)

	ctx := probeRequest(t, modelTestPayload{Provider: schemas.OpenAI, Model: "live-model"})
	h.testModel(ctx)
	require.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	assert.Contains(t, string(ctx.Response.Body()), "mode not supported for probe: live")
}

// unaryUpstreams serves the unary endpoints each probe mode exercises and records
// which one was called.
func unaryUpstreams(tb testing.TB) (*httptest.Server, *atomic.Int32, *atomic.Int32) {
	tb.Helper()
	embeddings, reranks := &atomic.Int32{}, &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/embeddings":
			embeddings.Add(1)
			fmt.Fprint(w, `{"object":"list","model":"text-embedding-3-small","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"usage":{"prompt_tokens":1,"total_tokens":1}}`)
		case "/v1/rerank":
			reranks.Add(1)
			fmt.Fprint(w, `{"id":"rr1","model":"rerank-1","results":[{"index":0,"relevance_score":0.9}]}`)
		default:
			tb.Errorf("unexpected upstream path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	tb.Cleanup(server.Close)
	return server, embeddings, reranks
}

func TestModelProbeModeDispatch_UnaryModesReportLatencyOnly(t *testing.T) {
	upstream, embeddings, reranks := unaryUpstreams(t)
	cases := []struct {
		mode  string
		model string
		hit   *atomic.Int32
	}{
		{"embedding", "text-embedding-3-small", embeddings},
		{"rerank", "rerank-1", reranks},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			catalog := probeCatalog(t, map[string]string{"openai/" + tc.model: tc.mode})
			// Embedding and rerank on an OpenAI-backed upstream exist only as a
			// custom (OpenAI-compatible) configuration, exactly as an operator adds one.
			h, _ := newProbeHandlerWithProvider(t, schemas.OpenAI, upstream.URL, catalog,
				&schemas.CustomProviderConfig{BaseProviderType: schemas.OpenAI, AllowedRequests: &schemas.AllowedRequests{Embedding: true, Rerank: true}})

			ctx := probeRequest(t, modelTestPayload{Provider: schemas.OpenAI, Model: tc.model})
			h.testModel(ctx)
			require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))

			response := decodeProbe(t, ctx)
			require.True(t, response.OK, "%s: %s", tc.mode, response.Error)
			assert.Equal(t, tc.mode, response.Mode)
			assert.Nil(t, response.TTFTMs, "%s is a unary mode and must never report TTFT", tc.mode)
			assert.GreaterOrEqual(t, response.TotalMs, int64(0))
			assert.Equal(t, int32(1), tc.hit.Load(), "the probe must reach the %s endpoint exactly once", tc.mode)
		})
	}
}

// videoUpstreams serves the video job-creation endpoint.
func videoUpstreams(tb testing.TB) (*httptest.Server, *atomic.Int32) {
	tb.Helper()
	jobs := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/videos" {
			jobs.Add(1)
			fmt.Fprint(w, `{"id":"video_1","object":"video","status":"queued","model":"sora-2","prompt":"hi"}`)
			return
		}
		tb.Errorf("unexpected upstream path %q", r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	tb.Cleanup(server.Close)
	return server, jobs
}

// The remaining unary modes create a job or answer in one call; the probe reports
// that the provider accepted the work, which is what "the model is reachable"
// means for them.
func TestModelProbeModeDispatch_JobCreatingUnaryModeReportsLatencyOnly(t *testing.T) {
	upstream, jobs := videoUpstreams(t)
	catalog := probeCatalog(t, map[string]string{"openai/sora-2": "video_generation"})
	h, _ := newProbeHandlerWithProvider(t, schemas.OpenAI, upstream.URL, catalog,
		&schemas.CustomProviderConfig{BaseProviderType: schemas.OpenAI, AllowedRequests: &schemas.AllowedRequests{VideoGeneration: true}})

	ctx := probeRequest(t, modelTestPayload{Provider: schemas.OpenAI, Model: "sora-2"})
	h.testModel(ctx)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))

	response := decodeProbe(t, ctx)
	require.True(t, response.OK, response.Error)
	assert.Equal(t, "video_generation", response.Mode)
	assert.Nil(t, response.TTFTMs, "video_generation is unary and must not report TTFT")
	assert.Equal(t, int32(1), jobs.Load(), "the probe must create exactly one job")
}
