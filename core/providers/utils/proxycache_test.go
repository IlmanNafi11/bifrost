package utils

import (
	"context"
	"fmt"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
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
