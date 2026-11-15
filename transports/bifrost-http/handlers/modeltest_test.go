package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

// throttleErr builds the provider refusal shape a throttled probe carries: a status code alone is
// what isProviderThrottle keys on.
func throttleErr(status int) *schemas.BifrostError {
	return &schemas.BifrostError{StatusCode: &status}
}

// probeTraceRecorder counts the traces a probe opens and completes, which is what makes the probe's
// invisibility safe: a trace nobody completes sits in the store until the TTL sweep warns about it.
type probeTraceRecorder struct {
	*schemas.NoOpTracer
	mu         sync.Mutex
	created    int
	completed  []string
	attrs      map[string]any
	redactions map[string]map[string]string
}

func (r *probeTraceRecorder) CreateTrace(_ string, _ ...string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.created++
	return "probe-trace-" + fmt.Sprint(r.created)
}

func (r *probeTraceRecorder) CompleteAndFlushTrace(traceID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.completed = append(r.completed, traceID)
}

func (r *probeTraceRecorder) SetTraceAttribute(traceID string, key string, value any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.attrs == nil {
		r.attrs = map[string]any{}
	}
	r.attrs[traceID+"|"+key] = value
}

// SetTraceRedactionReplacements records the input replacements the probe hands the trace, so a test
// can pin that the redactions reach the trace rather than only the verdict body.
func (r *probeTraceRecorder) SetTraceRedactionReplacements(traceID string, phase schemas.RedactionPhase, replacements map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.redactions == nil {
		r.redactions = map[string]map[string]string{}
	}
	r.redactions[traceID] = replacements
}

// completedCount and createdCount are read under the lock so a stream that ends on its own
// goroutine cannot race the assertion.
func (r *probeTraceRecorder) completedCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.completed)
}

// newProbeHandler wires a provider handler over a real Bifrost client whose OpenAI
// provider points at upstream, plus an observer plugin that records the probe
// markers every hook saw.
func newProbeHandler(tb testing.TB, upstreamURL string, catalog *modelcatalog.ModelCatalog) (*ProviderHandler, *probeObserved) {
	tb.Helper()
	h, observed, _ := newProbeHandlerTracing(tb, schemas.OpenAI, upstreamURL, catalog, nil)
	return h, observed
}

// newProbeHandlerWithProvider is newProbeHandler with the provider name and custom
// provider config under test, for modes a built-in provider only serves through a
// custom (OpenAI-compatible) configuration.
func newProbeHandlerWithProvider(tb testing.TB, providerName schemas.ModelProvider, upstreamURL string, catalog *modelcatalog.ModelCatalog, custom *schemas.CustomProviderConfig) (*ProviderHandler, *probeObserved) {
	tb.Helper()
	h, observed, _ := newProbeHandlerTracing(tb, providerName, upstreamURL, catalog, custom)
	return h, observed
}

// newProbeHandlerTracing is the fixture with the tracer it handed the client, so a test can assert
// the probe completed the trace it opened.
func newProbeHandlerTracing(tb testing.TB, providerName schemas.ModelProvider, upstreamURL string, catalog *modelcatalog.ModelCatalog, custom *schemas.CustomProviderConfig) (*ProviderHandler, *probeObserved, *probeTraceRecorder) {
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
	tracer := &probeTraceRecorder{}
	client, err := bifrost.Init(tb.Context(), schemas.BifrostConfig{
		Account:      lib.NewBaseAccount(config),
		Logger:       log,
		ModelCatalog: catalog,
		LLMPlugins:   []schemas.LLMPlugin{observer},
		Tracer:       tracer,
	})
	require.NoError(tb, err)
	tb.Cleanup(client.Shutdown)
	config.SetBifrostClient(client)
	return NewProviderHandler(nil, config, client), observer, tracer
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
	// The probe endpoint is admin-only; these tests drive it as the local admin the auth
	// middleware would mark. The gate itself is covered by its own test.
	ctx.SetUserValue(schemas.IsLocalAdminContextKey, true)
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
	r := router.New()
	h.RegisterRoutes(r)

	// Dispatch through the registered route rather than calling the handler body, so the success
	// path also pins that POST /api/models/test reaches it.
	ctx := probeRequest(t, modelTestPayload{Provider: schemas.OpenAI, Model: "gpt-4o-mini"})
	r.Handler(ctx)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))

	response := decodeProbe(t, ctx)
	assert.True(t, response.OK)
	assert.Equal(t, "chat", response.Mode)
	assert.Equal(t, "openai", response.Provider)
	assert.Equal(t, "gpt-4o-mini", response.Model)
	require.NotNil(t, response.TTFTMs, "a streaming probe reports TTFT")
	assert.GreaterOrEqual(t, *response.TTFTMs, int64(0))
	assert.Empty(t, response.Error)
	assert.Empty(t, response.KeyID, "an unpinned probe must not reveal which key the deployment routed to")
	assert.Empty(t, response.KeyName, "an unpinned probe must not reveal the key's name")

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
	h.probeTimeout = 300 * time.Millisecond

	ctx := probeRequest(t, modelTestPayload{Provider: schemas.OpenAI, Model: "gpt-4o-mini"})
	h.testModel(ctx)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))

	response := decodeProbe(t, ctx)
	assert.False(t, response.OK)
	assert.Regexp(t, `^probe timed out after 300`, response.Error, "a deadline is reported as a timeout verdict, not the raw context error")
	assert.True(t, response.TimedOut, "the verdict names the timeout structurally, not only in prose")
	assert.Nil(t, response.TTFTMs, "a timeout reports no first-token time")
}

// The deadline an operator reads is the one that applied: a handler built with a shortened timeout
// must name that value, not the handler's zero field. (The default-timeout path resolves to the same
// field before the verdict, so this covers both.)
func TestModelProbe_TimeoutVerdictNamesTheDeadlineThatApplied(t *testing.T) {
	makeHandler := func(t *testing.T, configured time.Duration) *ProviderHandler {
		t.Helper()
		upstream := chatUpstream(t, 0, nil)
		upstream.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			<-r.Context().Done()
		})
		t.Cleanup(upstream.Close)
		h, _ := newProbeHandler(t, upstream.URL, probeCatalog(t, map[string]string{"openai/gpt-4o-mini": "chat"}))
		h.probeTimeout = configured
		return h
	}

	shortened := makeHandler(t, 300*time.Millisecond)
	ctx := probeRequest(t, modelTestPayload{Provider: schemas.OpenAI, Model: "gpt-4o-mini"})
	shortened.testModel(ctx)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	response := decodeProbe(t, ctx)
	require.True(t, response.TimedOut)
	assert.Contains(t, response.Error, "probe timed out after 300ms",
		"the verdict names the deadline the probe ran under, not the handler's zero value")
	assert.NotContains(t, response.Error, "after 0s")
}

// The timeout verdict distinguishes the probe's own deadline from a provider failure that happens to
// mention one. Getting this wrong reports a provider outage as "your probe timed out" and a timeout as
// a provider error, so every arm is pinned here.
func TestModelProbe_TimeoutDetectionSeparatesOurDeadlineFromProviderText(t *testing.T) {
	live := func() *schemas.BifrostContext {
		return schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	}
	expired := func(t *testing.T) *schemas.BifrostContext {
		t.Helper()
		ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), time.Millisecond)
		defer cancel()
		time.Sleep(5 * time.Millisecond)
		return ctx
	}

	t.Run("a nil error under an expired context is a timeout", func(t *testing.T) {
		assert.True(t, isProbeTimeout(expired(t), nil))
	})
	t.Run("a nil error with time to spare is not", func(t *testing.T) {
		assert.False(t, isProbeTimeout(live(), nil))
	})
	t.Run("the drain's cancellation chains the deadline", func(t *testing.T) {
		assert.True(t, isProbeTimeout(expired(t), &schemas.BifrostError{Error: &schemas.ErrorField{
			Message: context.DeadlineExceeded.Error(), Error: context.DeadlineExceeded,
		}}))
	})
	t.Run("a provider that echoes the phrase while our deadline is intact is a provider failure", func(t *testing.T) {
		assert.False(t, isProbeTimeout(live(), &schemas.BifrostError{Error: &schemas.ErrorField{
			Message: "upstream said: context deadline exceeded",
		}}), "the provider's own timeout is not ours to report as a probe timeout")
	})
	t.Run("an ordinary provider error is not a timeout", func(t *testing.T) {
		assert.False(t, isProbeTimeout(live(), &schemas.BifrostError{Error: &schemas.ErrorField{Message: "invalid api key"}}))
		assert.False(t, isProbeTimeout(expired(t), &schemas.BifrostError{Error: &schemas.ErrorField{Message: "invalid api key"}}),
			"an expired context does not turn a provider refusal into a timeout")
	})
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
	invalid.SetUserValue(schemas.IsLocalAdminContextKey, true)
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

// TestListModelDetails_ReportsProbeMode pins that the catalog listing names each
// row's mode, which is what lets the model catalog disable a Test button for a
// mode the probe cannot exercise before the operator clicks it. A row with no
// datasheet entry reports the chat default, matching how it is probed.
func TestListModelDetails_ReportsProbeMode(t *testing.T) {
	SetLogger(&mockLogger{})

	h := providerHandlerForTest(
		schemas.OpenAI,
		[]schemas.Key{{ID: "key-a"}},
		[]string{"gpt-4o-mini", "text-embedding-3-small", "brand-new-model"},
		[]string{"gpt-4o-mini", "text-embedding-3-small", "brand-new-model"},
	)
	h.inMemoryStore.ModelCatalog = probeCatalog(t, map[string]string{
		"openai/gpt-4o-mini":            "chat",
		"openai/text-embedding-3-small": "embedding",
	})

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/api/models/details?provider=openai&limit=10")
	h.listModelDetails(ctx)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))

	var resp ListModelDetailsResponse
	require.NoError(t, sonic.Unmarshal(ctx.Response.Body(), &resp))
	modes := make(map[string]string, len(resp.Models))
	for _, model := range resp.Models {
		modes[model.Name] = model.Mode
	}
	assert.Equal(t, "chat", modes["gpt-4o-mini"])
	assert.Equal(t, "embedding", modes["text-embedding-3-small"])
	assert.Equal(t, "chat", modes["brand-new-model"], "a row with no datasheet entry is probed as chat")
}

// A probe spends the deployment's credentials while counting as no traffic at all, so the endpoint
// is admin-only. A non-admin caller must be refused before anything is spent.
func TestModelProbe_NonAdminIsRefusedBeforeSpending(t *testing.T) {
	catalog := probeCatalog(t, map[string]string{"openai/gpt-4o-mini": "chat"})
	h, observer := newProbeHandler(t, chatUpstream(t, 0, nil).URL, catalog)

	ctx := probeRequest(t, modelTestPayload{Provider: schemas.OpenAI, Model: "gpt-4o-mini"})
	ctx.RemoveUserValue(schemas.IsLocalAdminContextKey)
	h.testModel(ctx)

	assert.Equal(t, fasthttp.StatusForbidden, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	observer.mu.Lock()
	defer observer.mu.Unlock()
	assert.Empty(t, observer.isProbe, "a refused caller must not reach the provider at all")
}

// IsLocalAdminContextKey is also set for a caller that presented no credential, because an
// auth-disabled deployment treats every caller as the local admin. A probe leaves no trace, so it
// takes a credential that was actually checked and refuses the credential-less caller.
func TestModelProbe_AuthBypassedCallerIsRefused(t *testing.T) {
	catalog := probeCatalog(t, map[string]string{"openai/gpt-4o-mini": "chat"})
	h, observer := newProbeHandler(t, chatUpstream(t, 0, nil).URL, catalog)

	ctx := probeRequest(t, modelTestPayload{Provider: schemas.OpenAI, Model: "gpt-4o-mini"})
	ctx.SetUserValue(schemas.BifrostContextKeyAuthBypassed, true)
	h.testModel(ctx)

	assert.Equal(t, fasthttp.StatusForbidden, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	assert.Contains(t, string(ctx.Response.Body()), "authenticated administrator")
	observer.mu.Lock()
	defer observer.mu.Unlock()
	assert.Empty(t, observer.isProbe, "an unauthenticated caller must not reach the provider")
}

// A pinned key that belongs to another provider must be refused, not silently swapped for a key of
// the named provider: the verdict names the key that served it, and it must be the one asked for.
func TestModelProbe_PinnedKeyMustBelongToTheNamedProvider(t *testing.T) {
	catalog := probeCatalog(t, map[string]string{"openai/gpt-4o-mini": "chat"})
	h, _ := newProbeHandler(t, chatUpstream(t, 0, nil).URL, catalog)
	h.inMemoryStore.Providers[schemas.Anthropic] = configstore.ProviderConfig{
		Keys:          []schemas.Key{{ID: "key-anthropic", Name: "claude", Value: *schemas.NewSecretVar("sk-ant"), Weight: 1, Models: schemas.WhiteList{"*"}}},
		NetworkConfig: &schemas.NetworkConfig{AllowPrivateNetwork: true},
	}

	ctx := probeRequest(t, modelTestPayload{Provider: schemas.OpenAI, Model: "gpt-4o-mini", KeyID: "key-anthropic"})
	h.testModel(ctx)
	assert.Equal(t, fasthttp.StatusNotFound, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	assert.Contains(t, string(ctx.Response.Body()), "has no key key-anthropic")
}

// The probe endpoint is the one place a deployment can be made to spend without a trace, so one
// principal cannot loop it: the second probe inside the interval is refused, and the interval is
// measured from attempts, not successes.
func TestModelProbe_AdmissionBoundsOnePrincipal(t *testing.T) {
	catalog := probeCatalog(t, map[string]string{"openai/gpt-4o-mini": "chat"})
	h, observer := newProbeHandler(t, chatUpstream(t, 0, nil).URL, catalog)

	first := probeRequest(t, modelTestPayload{Provider: schemas.OpenAI, Model: "gpt-4o-mini"})
	h.testModel(first)
	require.Equal(t, fasthttp.StatusOK, first.Response.StatusCode(), string(first.Response.Body()))

	second := probeRequest(t, modelTestPayload{Provider: schemas.OpenAI, Model: "gpt-4o-mini"})
	h.testModel(second)
	assert.Equal(t, fasthttp.StatusTooManyRequests, second.Response.StatusCode(), string(second.Response.Body()))

	observer.mu.Lock()
	defer observer.mu.Unlock()
	assert.Len(t, observer.isProbe, 1, "the refused probe must not reach the provider")
}

// A provider that asked to be left alone is not re-probed immediately: the backoff grows while the
// failures continue and resets on the first probe that reaches the provider.
func TestModelProbe_AdmissionBacksOffAThrottledProvider(t *testing.T) {
	var a probeAdmission
	throttled := throttleErr(fasthttp.StatusTooManyRequests)
	a.record(schemas.OpenAI, &ModelTestResponse{}, throttled)
	now := time.Now()
	a.mu.Lock()
	until := a.blockedUntil[schemas.OpenAI]
	a.mu.Unlock()
	assert.True(t, until.After(now.Add(probeFailureBackoff-time.Second)), "a failed probe pushes the next attempt out")

	a.record(schemas.OpenAI, &ModelTestResponse{OK: true}, nil)
	a.mu.Lock()
	_, stillBlocked := a.blockedUntil[schemas.OpenAI]
	a.mu.Unlock()
	assert.False(t, stillBlocked, "a probe that reached the provider resets the backoff")
}

// Each further failure doubles the window, so a provider that keeps refusing is probed less and
// less often, and the window stops growing at the cap instead of outliving the process.
func TestModelProbe_AdmissionBackoffDoublesAndCaps(t *testing.T) {
	var a probeAdmission
	window := func() time.Duration {
		a.mu.Lock()
		defer a.mu.Unlock()
		return time.Until(a.blockedUntil[schemas.OpenAI])
	}
	throttled := throttleErr(fasthttp.StatusTooManyRequests)
	failures := 0
	previous := time.Duration(0)
	for range 6 {
		a.record(schemas.OpenAI, &ModelTestResponse{}, throttled)
		failures++
		got := window()
		if failures > 1 && previous < probeMaxBackoff-5*time.Second {
			assert.Greater(t, got, previous, "failure %d must widen the window", failures)
		}
		assert.LessOrEqual(t, got, probeMaxBackoff+time.Second, "failure %d must stay under the cap", failures)
		previous = got
	}
	assert.Less(t, window(), probeMaxBackoff+time.Second, "the cap bounds the window however many failures accumulate")
}

// A probe timeout is a latency measurement, not a provider outage: the operator's next click on a
// slow-but-working model must be admitted, not fenced for a growing window. Backoff is reserved for
// a provider that answered 429/5xx and asked to be left alone.
func TestModelProbe_TimeoutDoesNotBackOffTheProvider(t *testing.T) {
	var a probeAdmission
	a.record(schemas.OpenAI, &ModelTestResponse{TimedOut: true}, nil)

	assert.Empty(t, a.allow(schemas.OpenAI), "a timed-out probe leaves the provider immediately re-probeable")
	assert.Empty(t, a.failures, "a timeout records no failure to widen")
}

// A provider error message can quote the credential it rejected. The verdict is shown in the
// dashboard, so it is passed through the request's redaction data before it leaves.
func TestRedactProbeError_AppliesTheRequestRedactions(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	schemas.SetRedactionDataOnContext(ctx, schemas.RedactionData{
		LiteralReplacements: schemas.RedactionMapsByPhase{
			Input: map[string]string{"sk-secret-value": "[REDACTED]"},
		},
	})

	got := redactProbeError(ctx, "Incorrect API key provided: sk-secret-value")
	assert.Equal(t, "Incorrect API key provided: [REDACTED]", got)
	assert.Equal(t, "unrelated", redactProbeError(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), "unrelated"),
		"a request with no redaction data keeps the provider text as-is")
}

// Reversible mappings are token -> original; applying them to a provider error would leak the
// original value if the token appears, so outbound probe error scrubbing must leave them untouched.
func TestRedactProbeError_DoesNotExpandReversibleMappings(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	schemas.SetRedactionDataOnContext(ctx, schemas.RedactionData{
		LiteralReplacements: schemas.RedactionMapsByPhase{
			Input: map[string]string{"sk-secret-value": "[REDACTED]"},
		},
		ReversibleMappings: schemas.RedactionMapsByPhase{
			Output: map[string]string{"[EMAIL_2]": "sam@example.com"},
		},
	})

	got := redactProbeError(ctx, "refusal for sk-secret-value mentioned [EMAIL_2]")
	assert.Equal(t, "refusal for [REDACTED] mentioned [EMAIL_2]", got,
		"literal secrets are scrubbed but reversible tokens must not be expanded to originals")
}

// A provider can quote the credential it rejected, and a pinned probe is the one place the caller
// names which literal to use. The verdict must not hand that literal back in the dashboard.
func TestModelProbe_PinnedKeySecretIsRedactedFromTheVerdict(t *testing.T) {
	const secret = "sk-probe-secret-value"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintf(w, `{"error":{"message":"Incorrect API key provided: %s","type":"invalid_request_error"}}`, secret)
	}))
	t.Cleanup(upstream.Close)

	catalog := probeCatalog(t, map[string]string{"openai/gpt-4o-mini": "chat"})
	h, _ := newProbeHandler(t, upstream.URL, catalog)
	h.inMemoryStore.Providers[schemas.OpenAI] = configstore.ProviderConfig{
		Keys: []schemas.Key{{
			ID: "key-pinned", Name: "pinned", Value: *schemas.NewSecretVar(secret), Weight: 1, Models: schemas.WhiteList{"*"},
		}},
		NetworkConfig:            &schemas.NetworkConfig{BaseURL: upstream.URL, AllowPrivateNetwork: true},
		ConcurrencyAndBufferSize: &schemas.ConcurrencyAndBufferSize{Concurrency: 2, BufferSize: 10},
	}

	ctx := probeRequest(t, modelTestPayload{Provider: schemas.OpenAI, Model: "gpt-4o-mini", KeyID: "key-pinned"})
	h.testModel(ctx)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))

	body := string(ctx.Response.Body())
	assert.NotContains(t, body, secret, "the provider's echo of the pinned key must not reach the caller")
}

// The admission guard also caps how many probes one process runs at once: probes share the
// provider queues with real traffic, so an unbounded burst is refused before it contends for the
// same workers and connection pool. The interval is global, so every caller after the first inside
// the window is refused even though each has its own principal.
func TestModelProbe_AdmissionCapsConcurrency(t *testing.T) {
	var a probeAdmission
	for range probeMaxInFlight {
		assert.Empty(t, a.allow(schemas.OpenAI), "the first %d probes fit under the cap", probeMaxInFlight)
		a.lastStart = time.Time{} // reset the interval so the cap is the only bound being tested
	}
	assert.Contains(t, a.allow(schemas.OpenAI), "too many probes",
		"the %d-th concurrent probe is refused for the global cap", probeMaxInFlight+1)

	a.release()
	a.lastStart = time.Time{}
	assert.Empty(t, a.allow(schemas.OpenAI), "releasing one slot admits the next probe")
}

// The interval is global, so minting a fresh principal does not reset it: two different callers
// inside one window share the same bound, which is what makes the spend guard hold however many
// identities a caller presents.
func TestModelProbe_AdmissionIntervalIsGlobalNotPerPrincipal(t *testing.T) {
	var a probeAdmission
	require.Empty(t, a.allow(schemas.OpenAI))
	a.release()

	// A different principal inside the same window is refused the same way the original would be.
	assert.Contains(t, a.allow(schemas.OpenAI), "less than",
		"a fresh principal does not reset the global interval")
}

// The three bounds are distinguishable: the interval is a wait the whole process shares, the cap
// means the process is already probing, and a provider backoff is a wait against one provider only.
func TestModelProbe_AdmissionNamesTheBoundItHit(t *testing.T) {
	var a probeAdmission
	require.Empty(t, a.allow(schemas.OpenAI))
	a.release()

	interval := a.allow(schemas.OpenAI)
	assert.Contains(t, interval, "less than", "the second probe in one window is a wait, not a busy process")

	var capped probeAdmission
	for range probeMaxInFlight {
		require.Empty(t, capped.allow(schemas.OpenAI))
		capped.lastStart = time.Time{} // the cap is the bound under test, not the interval
	}
	assert.Contains(t, capped.allow(schemas.OpenAI), "too many probes")

	var backedOff probeAdmission
	backedOff.record(schemas.OpenAI, &ModelTestResponse{}, throttleErr(fasthttp.StatusTooManyRequests))
	refusal := backedOff.allow(schemas.OpenAI)
	assert.Contains(t, refusal, "backing off", "the backoff refusal is a provider wait, not a busy process")
	assert.Contains(t, refusal, "30s", "the refusal names the window, so the operator knows roughly when to retry")
}

// The backoff is keyed by provider, so one provider that is throttled must not fence the others:
// the same process probing a healthy provider is admitted, and the refusal names only the broken
// provider's bound.
func TestModelProbe_AdmissionBackoffIsPerProvider(t *testing.T) {
	var a probeAdmission
	a.record(schemas.OpenAI, &ModelTestResponse{}, throttleErr(fasthttp.StatusTooManyRequests))

	assert.Contains(t, a.allow(schemas.OpenAI), "backing off", "the throttled provider is refused")
	assert.Empty(t, a.allow(schemas.Anthropic), "a different, healthy provider is still probeable")
}

// A backoff that has already elapsed admits the probe: now.Before(until) is the only refusal, so
// the instant the window ends (and anything after it) is a fresh start.
func TestModelProbe_AdmissionBackoffExpiresAtTheBoundary(t *testing.T) {
	var a probeAdmission
	a.blockedUntil = map[schemas.ModelProvider]time.Time{schemas.OpenAI: time.Now().Add(-time.Nanosecond)}

	assert.Empty(t, a.allow(schemas.OpenAI), "a backoff that has elapsed admits the probe")
}

// The mutex in allow is load-bearing, and only concurrency makes that falsifiable: with it removed
// the in-flight count loses updates, so a burst admits more probes than the cap. Run under -race
// this also pins the guard on every path. The global interval means only the first caller inside
// the window is admitted even under concurrency, so the burst is bound by both the cap and the
// interval together.
func TestModelProbe_AdmissionIsSafeUnderConcurrentCallers(t *testing.T) {
	var a probeAdmission
	const callers = 64
	results := make([]string, callers)
	var wg sync.WaitGroup
	wg.Add(callers)
	for i := range callers {
		go func(i int) {
			defer wg.Done()
			results[i] = a.allow(schemas.OpenAI)
		}(i)
	}
	wg.Wait()

	admitted := 0
	for _, refusal := range results {
		if refusal == "" {
			admitted++
		}
	}
	assert.Equal(t, 1, admitted, "a concurrent burst inside one global window admits exactly one probe")

	a.mu.Lock()
	inFlight := a.inFlight
	a.mu.Unlock()
	assert.Equal(t, 1, inFlight, "the reserved slot matches the admission")

	for range admitted {
		a.release()
	}
	a.mu.Lock()
	inFlight = a.inFlight
	a.mu.Unlock()
	assert.Zero(t, inFlight, "every admitted slot is returned")
}

// The interval is a process-wide lastStart stamp, not a map of principals, so there is nothing to
// prune and nothing a caller can grow by minting identities. A stale lastStart in the past admits;
// one inside the window refuses; the boundary at exactly probeMinInterval admits (strict < in the
// wait comparison).
func TestModelProbe_AdmissionIntervalBoundary(t *testing.T) {
	var a probeAdmission
	now := time.Now()
	a.lastStart = now.Add(-probeMinInterval)
	require.Empty(t, a.allow(schemas.OpenAI), "an interval that has exactly elapsed admits the next probe")
	a.release()

	a.lastStart = now.Add(-probeMinInterval + time.Second)
	assert.Contains(t, a.allow(schemas.OpenAI), "less than", "an interval still running refuses")
}

// A provider that refuses before key selection can still echo a credential into a verdict with no
// redaction data present. The shape scrub is the last line of defence so the dashboard never sees it.
//
// The long-run arm is narrowed by shape, and both sides are pinned: an unbroken run of one letter is
// a word, a hyphenated name or a model id, and redacting those would mangle a provider's message
// without hiding anything. A run carrying digits or mixed case is the credential shape.
func TestRedactProbeError_ScrubsCredentialShapeWithoutRedactionData(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	assert.Equal(t, "Incorrect API key provided: [REDACTED]",
		redactProbeError(ctx, "Incorrect API key provided: sk-1234567890abcdef"))
	// A short tail is still a credential: a real key is short as often as it is long, and a
	// provider that echoes one verbatim hands it to every reader of the verdict.
	assert.Equal(t, "key invalid: [REDACTED]",
		redactProbeError(ctx, "key invalid: gsk_x4R0mP9"), "a gsk_ key with a seven-character tail is a credential")
	assert.Equal(t, "key invalid: [REDACTED]",
		redactProbeError(ctx, "key invalid: sk_aBc9"), "a short prefix tail with mixed case and a digit is a credential")
	assert.Equal(t, "key invalid: sk_abc",
		redactProbeError(ctx, "key invalid: sk_abc"), "a short all-lowercase tail is a word fragment, not a credential")
	assert.Equal(t, "bad key [REDACTED]",
		redactProbeError(ctx, "bad key "+strings.Repeat("a", 40)+"9"), "a base64-shaped run is a credential")
	assert.Equal(t, "bad key "+strings.Repeat("a", 40),
		redactProbeError(ctx, "bad key "+strings.Repeat("a", 40)),
		"an unbroken run of one letter is a word, not a credential")
	assert.Equal(t, "plain provider refusal", redactProbeError(ctx, "plain provider refusal"),
		"non-credential text passes through unchanged")
}

// The prefix arm must not slice a key fragment out of a longer word: the word boundary keeps
// `desk_...` intact, and a `gsk_` key is matched whole rather than starting at its `sk_`.
func TestRedactProbeError_PrefixArmIsWordBounded(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	assert.Equal(t, "the desk_xxxxxxxx is yours",
		redactProbeError(ctx, "the desk_xxxxxxxx is yours"), "an sk-shaped fragment inside a word is not a credential")
	assert.Equal(t, "[REDACTED] is redacted whole",
		redactProbeError(ctx, "gsk_1234567890abcdef is redacted whole"),
		"a gsk_ key matches from its first character, not its inner sk_")
}

// The shape scrub runs after the literal replacements: a message can carry a seeded literal that is
// also credential-shaped, and it must be replaced exactly once by the map rather than mangled by
// the regex afterwards.
func TestRedactProbeError_LiteralWinsOverShapeScrub(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	schemas.SetRedactionDataOnContext(ctx, schemas.RedactionData{
		LiteralReplacements: schemas.RedactionMapsByPhase{
			Input: map[string]string{"sk-1234567890abcdef": "[PINNED]"},
		},
	})
	assert.Equal(t, "Incorrect API key provided: [PINNED]",
		redactProbeError(ctx, "Incorrect API key provided: sk-1234567890abcdef"),
		"the seeded literal replaces the credential and the shape scrub leaves the placeholder alone")
}

// redactProbeError is called on every provider-error verdict, including ones where the handler
// built no redaction data or only output literals (a probe only ever seeds input literals). A nil
// context and an output-only map must both pass through without a panic or an output-only rewrite.
func TestRedactProbeError_NilContextAndOutputOnlyLiterals(t *testing.T) {
	assert.Equal(t, "unchanged", redactProbeError(nil, "unchanged"),
		"a nil context keeps the text as-is rather than panicking")

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	schemas.SetRedactionDataOnContext(ctx, schemas.RedactionData{
		LiteralReplacements: schemas.RedactionMapsByPhase{
			Output: map[string]string{"rivera@example.com": "[EMAIL]"},
		},
	})
	assert.Equal(t, "rivera@example.com unchanged", redactProbeError(ctx, "rivera@example.com unchanged"),
		"an output-only literal is not applied to an outbound provider error")
}

// When the caller omits key_id the pipeline auto-selects a key; a provider error that echoes that
// key's secret must still be scrubbed from the verdict even though the caller never named the
// literal, so the pinned-key redaction path could not have seeded it.
func TestModelProbe_AutoSelectSecretIsScrubbedFromTheVerdict(t *testing.T) {
	const secret = "sk-autoselect-secret-abcdef123456"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintf(w, `{"error":{"message":"Incorrect API key provided: %s","type":"invalid_request_error"}}`, secret)
	}))
	t.Cleanup(upstream.Close)

	catalog := probeCatalog(t, map[string]string{"openai/gpt-4o-mini": "chat"})
	h, _ := newProbeHandler(t, upstream.URL, catalog)
	h.inMemoryStore.Providers[schemas.OpenAI] = configstore.ProviderConfig{
		Keys: []schemas.Key{{
			ID: "key-openai", Name: "prod-2", Value: *schemas.NewSecretVar(secret), Weight: 1, Models: schemas.WhiteList{"*"},
		}},
		NetworkConfig:            &schemas.NetworkConfig{BaseURL: upstream.URL, AllowPrivateNetwork: true},
		ConcurrencyAndBufferSize: &schemas.ConcurrencyAndBufferSize{Concurrency: 2, BufferSize: 10},
	}

	// No key_id: the pipeline auto-selects the only key, whose secret the upstream echoes back.
	ctx := probeRequest(t, modelTestPayload{Provider: schemas.OpenAI, Model: "gpt-4o-mini"})
	h.testModel(ctx)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))

	assert.NotContains(t, string(ctx.Response.Body()), secret, "the auto-selected key's echo must not reach the caller")
}

// A probe runs through core like any other request, so core opens a trace for it. The management
// API carries no tracing middleware and the logging plugin skips probes, so if the handler does not
// finish that trace it sits in the store until the TTL sweep exports it as a completion that never
// arrived. Completing it is also what hands the probe marker to the observability connectors, which
// is how they keep the probe out of provider statistics.
func TestModelProbe_CompletesTheTraceItOpens(t *testing.T) {
	catalog := probeCatalog(t, map[string]string{"openai/gpt-4o-mini": "chat"})
	h, _, tracer := newProbeHandlerTracing(t, schemas.OpenAI, chatUpstream(t, 0, nil).URL, catalog, nil)

	ctx := probeRequest(t, modelTestPayload{Provider: schemas.OpenAI, Model: "gpt-4o-mini"})
	h.testModel(ctx)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	require.True(t, decodeProbe(t, ctx).OK)

	tracer.mu.Lock()
	defer tracer.mu.Unlock()
	require.Equal(t, 1, tracer.created, "the probe opens exactly one trace")
	require.Equal(t, []string{"probe-trace-1"}, tracer.completed,
		"the probe must complete the trace it opened, or it expires as a completion that never arrived")
	assert.Equal(t, true, tracer.attrs["probe-trace-1|"+schemas.TraceAttrIsModelProbe],
		"the completed trace carries the probe marker the connectors read")
}

// The trace's error attribute is exported to every connector, so a provider error that quotes the
// credential it rejected must be scrubbed there, not only from the verdict body. This pins that the
// probe seeds the credential-shaped literal into the trace's redaction replacements before flushing.
func TestModelProbe_TraceRedactionReachesTheErrorAttribute(t *testing.T) {
	const secret = "sk-trace-secret-abcdef123456"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintf(w, `{"error":{"message":"Incorrect API key provided: %s","type":"invalid_request_error"}}`, secret)
	}))
	t.Cleanup(upstream.Close)

	catalog := probeCatalog(t, map[string]string{"openai/gpt-4o-mini": "chat"})
	h, _, tracer := newProbeHandlerTracing(t, schemas.OpenAI, upstream.URL, catalog, nil)
	h.inMemoryStore.Providers[schemas.OpenAI] = configstore.ProviderConfig{
		Keys: []schemas.Key{{
			ID: "key-openai", Name: "prod-2", Value: *schemas.NewSecretVar(secret), Weight: 1, Models: schemas.WhiteList{"*"},
		}},
		NetworkConfig:            &schemas.NetworkConfig{BaseURL: upstream.URL, AllowPrivateNetwork: true},
		ConcurrencyAndBufferSize: &schemas.ConcurrencyAndBufferSize{Concurrency: 2, BufferSize: 10},
	}

	// Pinned, so the literal map is seeded with the exact key before the probe runs.
	ctx := probeRequest(t, modelTestPayload{Provider: schemas.OpenAI, Model: "gpt-4o-mini", KeyID: "key-openai"})
	h.testModel(ctx)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))

	tracer.mu.Lock()
	defer tracer.mu.Unlock()
	require.Contains(t, tracer.redactions, "probe-trace-1",
		"the probe must install redaction replacements on the trace it flushes")
	replacements := tracer.redactions["probe-trace-1"]
	assert.Contains(t, replacements, secret, "the trace replacements carry the pinned key's literal")
	assert.Equal(t, "[REDACTED]", replacements[secret],
		"the trace replacements map the secret to the same placeholder the verdict uses")
}

// auditLineLogger records the lines the probe audit sink emits. The probe writes exactly one audit
// record and nothing else - no log row, no metric, no cost - so this line is what an operator's
// log pipeline sees of a probe at all, and it must carry the verdict and the principal.
type auditLineLogger struct {
	mockLogger
	lines []string
}

func (l *auditLineLogger) Info(format string, args ...any) {
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func TestModelProbe_WritesOneAuditLineWithTheVerdict(t *testing.T) {
	catalog := probeCatalog(t, map[string]string{"openai/gpt-4o-mini": "chat"})
	h, _, _ := newProbeHandlerTracing(t, schemas.OpenAI, chatUpstream(t, 0, nil).URL, catalog, nil)
	recorder := &auditLineLogger{}
	SetLogger(recorder)
	t.Cleanup(func() { SetLogger(bifrost.NewNoOpLogger()) })

	ctx := probeRequest(t, modelTestPayload{Provider: schemas.OpenAI, Model: "gpt-4o-mini"})
	h.testModel(ctx)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))

	var probeLines []string
	for _, line := range recorder.lines {
		if strings.HasPrefix(line, "bifrost.probe ") {
			probeLines = append(probeLines, line)
		}
	}
	require.Len(t, probeLines, 1, "a probe leaves exactly one audit record, and no other log line")

	var event probeAuditEvent
	require.NoError(t, sonic.Unmarshal([]byte(strings.TrimPrefix(probeLines[0], "bifrost.probe ")), &event))
	assert.Equal(t, "openai", event.Provider)
	assert.Equal(t, "gpt-4o-mini", event.Model)
	assert.Equal(t, "chat", event.Mode)
	assert.True(t, event.OK, "the record carries the verdict the caller was shown")
	assert.NotEmpty(t, event.Principal, "the record names who spent the probe")
}

// A provider that sends output and then fails mid-stream is a dead model. The verdict has to come
// from draining the stream to its end: stopping at the first chunk (which is all the TTFT needs)
// would report ok:true for a model that answered once and then broke, and clear the TTFT of a probe
// that produced no usable answer.
func TestModelProbe_ErrorAfterFirstChunkIsNotASuccess(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-4o-mini\","+
			"\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\n")
		flusher.Flush()
		fmt.Fprint(w, "data: {\"error\":{\"message\":\"upstream fell over\",\"type\":\"server_error\"}}\n\n")
		flusher.Flush()
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	t.Cleanup(upstream.Close)

	catalog := probeCatalog(t, map[string]string{"openai/gpt-4o-mini": "chat"})
	h, _ := newProbeHandler(t, upstream.URL, catalog)

	ctx := probeRequest(t, modelTestPayload{Provider: schemas.OpenAI, Model: "gpt-4o-mini"})
	h.testModel(ctx)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))

	response := decodeProbe(t, ctx)
	assert.False(t, response.OK, "a stream that failed after its first chunk is not a live model")
	assert.Nil(t, response.TTFTMs, "a failed probe reports no time to first token")
	assert.NotEmpty(t, response.Error, "the provider's failure is the verdict's explanation")
	assert.GreaterOrEqual(t, response.TotalMs, int64(0), "the measurement is still reported")
}
