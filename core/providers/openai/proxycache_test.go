package openai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/maximhq/bifrost/core/network/proxytest"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

type proxyCacheTestLogger struct{}

func (proxyCacheTestLogger) Debug(string, ...any)                   {}
func (proxyCacheTestLogger) Info(string, ...any)                    {}
func (proxyCacheTestLogger) Warn(string, ...any)                    {}
func (proxyCacheTestLogger) Error(string, ...any)                   {}
func (proxyCacheTestLogger) Fatal(string, ...any)                   {}
func (proxyCacheTestLogger) SetLevel(schemas.LogLevel)              {}
func (proxyCacheTestLogger) SetOutputType(schemas.LoggerOutputType) {}
func (proxyCacheTestLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

// TestOpenAIProvider_ClientForBoundProxyDialthrough pins the Task 6 contract:
// a proxy picked on the attempt context must be the one the provider's request
// entries dial through. The target server records direct hits so the test fails
// loudly if the base (unproxied) client is used instead.
func TestOpenAIProvider_ClientForBoundProxyDialthrough(t *testing.T) {
	var proxyHits atomic.Int64
	proxy := proxytest.ForwardProxy(t, func(w http.ResponseWriter, _ *http.Request) {
		proxyHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"gpt-test"}]}`)
	})

	var directHits atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		directHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"direct-only"}]}`)
	}))
	defer target.Close()

	provider := NewOpenAIProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: target.URL},
	}, proxyCacheTestLogger{})
	require.NotNil(t, provider.proxied)
	require.NotNil(t, provider.client)
	require.NotNil(t, provider.streamingClient)

	// Unbound: clientFor returns the base client and the request goes direct.
	unboundCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	require.Same(t, provider.client, provider.clientFor(unboundCtx))
	require.Same(t, provider.streamingClient, provider.streamingClientFor(unboundCtx))

	// Bound: clientFor returns a distinct, proxied client.
	boundCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	boundCtx.SetValue(schemas.BifrostContextKeyProviderProxyConfig, &schemas.ProxyConfig{
		Type: schemas.HTTPProxy,
		URL:  schemas.NewSecretVar(proxy.URL),
	})
	proxied := provider.clientFor(boundCtx)
	require.NotSame(t, provider.client, proxied)
	require.NotNil(t, proxied.Dial)
	require.Same(t, proxied, provider.clientFor(boundCtx), "reuse on a second call")

	resp, bErr := provider.ListModels(boundCtx, []schemas.Key{{Models: schemas.WhiteList{"*"}}}, &schemas.BifrostListModelsRequest{})
	require.Nil(t, bErr)
	require.Len(t, resp.Data, 1)
	require.Equal(t, "openai/gpt-test", resp.Data[0].ID)
	require.Positive(t, proxyHits.Load(), "bound request must go through the proxy")
	require.Zero(t, directHits.Load(), "bound request must not reach the target directly")
}
