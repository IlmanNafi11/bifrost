package bedrock

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
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

// TestBedrockProxyCacheWiring pins that every Bedrock client flavour — the
// net/http Converse/realtime clients and the fasthttp Mantle clients — resolves
// the proxy picked on the attempt context through its cache, while an unbound
// attempt keeps each base client. The actual dial-through for both client types
// is pinned by the utils cache tests; this test guards the per-provider wiring.
func TestBedrockProxyCacheWiring(t *testing.T) {
	provider := &BedrockProvider{
		logger:                proxyCacheTestLogger{},
		client:                &http.Client{Transport: &http.Transport{}},
		streamingClient:       &http.Client{Transport: &http.Transport{}},
		realtimeClient:        &http.Client{Transport: &http.Transport{}},
		mantleClient:          &fasthttp.Client{},
		mantleStreamingClient: &fasthttp.Client{},
		proxied:               providerUtils.NewProxiedClientCache(0),
		proxiedHTTP:           providerUtils.NewProxiedHTTPClientCache(0),
	}

	unbound := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	require.Same(t, provider.client, provider.clientFor(unbound))
	require.Same(t, provider.streamingClient, provider.streamingClientFor(unbound))
	require.Same(t, provider.realtimeClient, provider.realtimeClientFor(unbound))
	require.Same(t, provider.mantleClient, provider.mantleClientFor(unbound))
	require.Same(t, provider.mantleStreamingClient, provider.mantleStreamingClientFor(unbound))

	proxyURL := "http://127.0.0.1:18099"
	bound := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bound.SetValue(schemas.BifrostContextKeyProviderProxyConfig, &schemas.ProxyConfig{
		Type: schemas.HTTPProxy,
		URL:  schemas.NewSecretVar(proxyURL),
	})

	// net/http clients get a cloned transport whose Proxy points at the picked proxy.
	for name, got := range map[string]*http.Client{
		"client":          provider.clientFor(bound),
		"streamingClient": provider.streamingClientFor(bound),
		"realtimeClient":  provider.realtimeClientFor(bound),
	} {
		require.NotSame(t, provider.client, got, name)
		transport, ok := got.Transport.(*http.Transport)
		require.True(t, ok, name+" transport must be *http.Transport")
		u, err := transport.Proxy(httptest.NewRequest(http.MethodGet, "https://example.com", nil))
		require.NoError(t, err)
		require.Equal(t, proxyURL, u.String(), name+" proxy URL")
	}

	// fasthttp Mantle clients get a distinct proxied client with a dialer.
	require.NotSame(t, provider.mantleClient, provider.mantleClientFor(bound))
	require.NotSame(t, provider.mantleStreamingClient, provider.mantleStreamingClientFor(bound))
	require.Same(t, provider.mantleClientFor(bound), provider.mantleClientFor(bound))
}
