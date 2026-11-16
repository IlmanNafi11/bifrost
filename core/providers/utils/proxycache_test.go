package utils

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// proxyCacheTestCtx returns a BifrostContext carrying pc as the attempt's
// picked egress proxy. Passing nil models an unbound attempt.
func proxyCacheTestCtx(pc *schemas.ProxyConfig) *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyProviderProxyConfig, pc)
	return ctx
}

func httpProxyConfig(port int, user, pass string) *schemas.ProxyConfig {
	return &schemas.ProxyConfig{
		Type:     schemas.HTTPProxy,
		URL:      schemas.NewSecretVar(fmt.Sprintf("http://127.0.0.1:%d", port)),
		Username: schemas.NewSecretVar(user),
		Password: schemas.NewSecretVar(pass),
	}
}

func TestProxiedClientCache_UnboundReturnsBase(t *testing.T) {
	base := &fasthttp.Client{}
	cache := NewProxiedClientCache(0)

	for _, tc := range []struct {
		name string
		pc   *schemas.ProxyConfig
	}{
		{name: "nil", pc: nil},
		{name: "no-proxy", pc: &schemas.ProxyConfig{Type: schemas.NoProxy}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := proxyCacheTestCtx(tc.pc)
			if got := cache.Unary(ctx, base, testLogger{}); got != base {
				t.Fatalf("Unary() = %p, want base %p", got, base)
			}
			if got := cache.Streaming(ctx, base, testLogger{}); got != base {
				t.Fatalf("Streaming() = %p, want base %p", got, base)
			}
		})
	}
}

func TestProxiedClientCache_BoundBuildsAndReuses(t *testing.T) {
	base := &fasthttp.Client{}
	cache := NewProxiedClientCache(0)
	pc := httpProxyConfig(18080, "user", "pass")

	ctx := proxyCacheTestCtx(pc)
	first := cache.Unary(ctx, base, testLogger{})
	if first == base {
		t.Fatal("Unary() returned base for a bound proxy")
	}
	if first.Dial == nil {
		t.Fatal("proxied client has no dialer")
	}
	if first.StreamResponseBody {
		// unary client must not be forced into streaming mode
		t.Fatal("unary client unexpectedly has StreamResponseBody set")
	}

	if got := cache.Unary(ctx, base, testLogger{}); got != first {
		t.Fatalf("second Unary() = %p, want cached %p", got, first)
	}
	stream := cache.Streaming(ctx, base, testLogger{})
	if stream == base || stream == first {
		t.Fatal("Streaming() must return a distinct streaming client")
	}
	if !stream.StreamResponseBody {
		t.Fatal("streaming client must have StreamResponseBody set")
	}
	if got := cache.Streaming(ctx, base, testLogger{}); got != stream {
		t.Fatalf("second Streaming() = %p, want cached %p", got, stream)
	}

	// Different credentials on the same host must not reuse the same client.
	other := cache.Unary(proxyCacheTestCtx(httpProxyConfig(18080, "user", "other")), base, testLogger{})
	if other == first {
		t.Fatal("different credentials reused the same client")
	}

	// The same proxy expressed with a scheme-less URL is the same cache entry.
	bare := &schemas.ProxyConfig{
		Type:     schemas.HTTPProxy,
		URL:      schemas.NewSecretVar("127.0.0.1:18080"),
		Username: schemas.NewSecretVar("user"),
		Password: schemas.NewSecretVar("pass"),
	}
	if got := cache.Unary(proxyCacheTestCtx(bare), base, testLogger{}); got != first {
		t.Fatalf("scheme-less URL = %p, want cached %p", got, first)
	}
}

func TestProxiedHTTPClientCache_UnboundReturnsBase(t *testing.T) {
	base := &http.Client{}
	cache := NewProxiedHTTPClientCache(0)

	for _, tc := range []struct {
		name string
		pc   *schemas.ProxyConfig
	}{
		{name: "nil", pc: nil},
		{name: "no-proxy", pc: &schemas.ProxyConfig{Type: schemas.NoProxy}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := proxyCacheTestCtx(tc.pc)
			if got := cache.Unary(ctx, base, testLogger{}); got != base {
				t.Fatalf("Unary() = %p, want base %p", got, base)
			}
			if got := cache.Streaming(ctx, base, testLogger{}); got != base {
				t.Fatalf("Streaming() = %p, want base %p", got, base)
			}
		})
	}
}

func TestProxiedHTTPClientCache_BoundDialthrough(t *testing.T) {
	var proxyHits, directHits atomic.Int64
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyHits.Add(1)
		_, _ = io.WriteString(w, "proxied")
	}))
	defer proxy.Close()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		directHits.Add(1)
		_, _ = io.WriteString(w, "direct")
	}))
	defer target.Close()

	base := &http.Client{}
	cache := NewProxiedHTTPClientCache(0)
	pc := &schemas.ProxyConfig{Type: schemas.HTTPProxy, URL: schemas.NewSecretVar(proxy.URL)}
	ctx := proxyCacheTestCtx(pc)

	client := cache.Unary(ctx, base, testLogger{})
	require.NotSame(t, base, client, "bound attempt must not use the base client")
	require.Same(t, client, cache.Unary(ctx, base, testLogger{}), "second call reuses the cached client")
	// net/http has no separate streaming transport; the streaming accessor resolves
	// to the same cached client so a pool proxy is honoured on streaming paths too.
	require.Same(t, client, cache.Streaming(ctx, base, testLogger{}))

	resp, err := client.Get(target.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "proxied", string(body))
	require.Positive(t, proxyHits.Load(), "bound request must go through the proxy")
	require.Zero(t, directHits.Load(), "bound request must not reach the target directly")

	// A different proxy must not reuse the first client.
	other := &schemas.ProxyConfig{Type: schemas.HTTPProxy, URL: schemas.NewSecretVar("http://127.0.0.1:18080")}
	require.NotSame(t, client, cache.Unary(proxyCacheTestCtx(other), base, testLogger{}))
}

func TestProxiedHTTPClientCache_CapEvictsLRU(t *testing.T) {
	base := &http.Client{}
	cache := NewProxiedHTTPClientCache(2)

	pcFor := func(port int) *schemas.ProxyConfig {
		return &schemas.ProxyConfig{Type: schemas.HTTPProxy, URL: schemas.NewSecretVar(fmt.Sprintf("http://127.0.0.1:%d", port))}
	}
	a := cache.Unary(proxyCacheTestCtx(pcFor(18091)), base, testLogger{})
	b := cache.Unary(proxyCacheTestCtx(pcFor(18092)), base, testLogger{})
	require.NotSame(t, a, b)

	// Touch a so that b becomes the least-recently-used entry.
	require.Same(t, a, cache.Unary(proxyCacheTestCtx(pcFor(18091)), base, testLogger{}))

	c := cache.Unary(proxyCacheTestCtx(pcFor(18093)), base, testLogger{})
	require.NotSame(t, a, c)
	require.Same(t, a, cache.Unary(proxyCacheTestCtx(pcFor(18091)), base, testLogger{}), "recently used client evicted")
	require.NotSame(t, b, cache.Unary(proxyCacheTestCtx(pcFor(18092)), base, testLogger{}), "LRU client not evicted")
}

func TestProxiedClientCache_CapEvictsLRU(t *testing.T) {
	base := &fasthttp.Client{}
	cache := NewProxiedClientCache(2)

	a := cache.Unary(proxyCacheTestCtx(httpProxyConfig(18081, "", "")), base, testLogger{})
	b := cache.Unary(proxyCacheTestCtx(httpProxyConfig(18082, "", "")), base, testLogger{})

	// Touch a so that b becomes the least-recently-used entry.
	if got := cache.Unary(proxyCacheTestCtx(httpProxyConfig(18081, "", "")), base, testLogger{}); got != a {
		t.Fatalf("touch of first client = %p, want %p", got, a)
	}

	c := cache.Unary(proxyCacheTestCtx(httpProxyConfig(18083, "", "")), base, testLogger{})
	if c == a {
		t.Fatal("new client collided with a live entry")
	}
	if got := cache.Unary(proxyCacheTestCtx(httpProxyConfig(18081, "", "")), base, testLogger{}); got != a {
		t.Fatalf("recently used client was evicted: got %p, want %p", got, a)
	}
	if got := cache.Unary(proxyCacheTestCtx(httpProxyConfig(18082, "", "")), base, testLogger{}); got == b {
		t.Fatal("least-recently-used client was not evicted")
	}
}
