package groq

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

// TestGroqProxyCacheDialthrough pins the Task 7 contract for the OpenAI-compatible
// delegator family: a proxy picked on the attempt context must be the one the
// provider's request entries dial through, and an unbound attempt keeps the base
// client. The target records direct hits so a fall-through to the base client
// fails loudly.
func TestGroqProxyCacheDialthrough(t *testing.T) {
	var proxyHits, directHits atomic.Int64
	proxy := proxytest.ForwardProxy(t, func(w http.ResponseWriter, _ *http.Request) {
		proxyHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"groq-test"}]}`)
	})
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		directHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"direct-only"}]}`)
	}))
	defer target.Close()

	provider, err := NewGroqProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: target.URL},
	}, proxyCacheTestLogger{})
	require.NoError(t, err)
	require.NotNil(t, provider.proxied)

	// Unbound: the base client is reused unchanged.
	unboundCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	require.Same(t, provider.client, provider.clientFor(unboundCtx))
	require.Same(t, provider.streamingClient, provider.streamingClientFor(unboundCtx))

	// Bound: a distinct, cached proxied client is used and the request reaches
	// the target through the proxy, never directly.
	boundCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	boundCtx.SetValue(schemas.BifrostContextKeyProviderProxyConfig, &schemas.ProxyConfig{
		Type: schemas.HTTPProxy,
		URL:  schemas.NewSecretVar(proxy.URL),
	})
	proxied := provider.clientFor(boundCtx)
	require.NotSame(t, provider.client, proxied)
	require.Same(t, proxied, provider.clientFor(boundCtx), "reuse on a second call")

	resp, bErr := provider.ListModels(boundCtx, []schemas.Key{{Models: schemas.WhiteList{"*"}}}, &schemas.BifrostListModelsRequest{})
	require.Nil(t, bErr)
	require.Len(t, resp.Data, 1)
	require.Equal(t, "groq/groq-test", resp.Data[0].ID)
	require.Positive(t, proxyHits.Load(), "bound request must go through the proxy")
	require.Zero(t, directHits.Load(), "bound request must not reach the target directly")
}
