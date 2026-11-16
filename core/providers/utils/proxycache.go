package utils

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strings"
	"sync"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// DefaultProxiedClientCacheCap bounds the number of distinct picked proxies
// whose fasthttp clients a provider keeps alive.
const DefaultProxiedClientCacheCap = 128

// proxiedClientEntry holds both client flavours for one canonical proxy. They
// are built together so a unary and a streaming request for the same proxy
// share one cache slot.
type proxiedClientEntry struct {
	key       string
	unary     *fasthttp.Client
	streaming *fasthttp.Client
}

// ProxiedClientCache builds and reuses fasthttp clients configured with the
// egress proxy published on the attempt's BifrostContext. Unbound attempts get
// the provider's base client back unchanged, so the common path adds only a
// context lookup. Bound attempts pay one canonical-key hash per request and,
// on the first use of a proxy, one configure/clone. The cap is an LRU so a
// provider bound to many pool proxies does not grow clients without limit.
//
// Every cached client clones the provider's base (keeping its timeouts, pool
// settings, retry policy and TLS config) and swaps in a dialer for the picked
// proxy; the streaming client additionally forces StreamResponseBody.
type ProxiedClientCache struct {
	cap   int
	mu    sync.Mutex
	index map[string]*list.Element
	lru   *list.List
}

// NewProxiedClientCache returns a cache holding at most cap distinct proxies.
// A non-positive cap selects DefaultProxiedClientCacheCap.
func NewProxiedClientCache(cap int) *ProxiedClientCache {
	if cap <= 0 {
		cap = DefaultProxiedClientCacheCap
	}
	return &ProxiedClientCache{
		cap:   cap,
		index: make(map[string]*list.Element),
		lru:   list.New(),
	}
}

// Unary returns the unary client for the proxy picked on ctx, or base when the
// attempt is unbound.
func (c *ProxiedClientCache) Unary(ctx *schemas.BifrostContext, base *fasthttp.Client, logger schemas.Logger) *fasthttp.Client {
	entry := c.entryFor(ctx, base, logger)
	if entry == nil {
		return base
	}
	return entry.unary
}

// Streaming returns the streaming client for the proxy picked on ctx, or base
// when the attempt is unbound.
func (c *ProxiedClientCache) Streaming(ctx *schemas.BifrostContext, base *fasthttp.Client, logger schemas.Logger) *fasthttp.Client {
	entry := c.entryFor(ctx, base, logger)
	if entry == nil {
		return base
	}
	return entry.streaming
}

// entryFor resolves the attempt's picked proxy and returns its cache entry,
// building it on first use. The base client is returned as a nil entry for
// unbound attempts and for nil/no-proxy configurations.
func (c *ProxiedClientCache) entryFor(ctx *schemas.BifrostContext, base *fasthttp.Client, logger schemas.Logger) *proxiedClientEntry {
	if ctx == nil || base == nil {
		return nil
	}
	pc, _ := ctx.Value(schemas.BifrostContextKeyProviderProxyConfig).(*schemas.ProxyConfig)
	if pc == nil || pc.Type == schemas.NoProxy {
		return nil
	}
	return c.getOrBuild(proxyCacheKey(pc), pc, base, logger)
}

// getOrBuild returns the cached entry for key, promoting it to most-recently-
// used, or builds and inserts a new one, evicting the least-recently-used entry
// when the cache is full.
func (c *ProxiedClientCache) getOrBuild(key string, pc *schemas.ProxyConfig, base *fasthttp.Client, logger schemas.Logger) *proxiedClientEntry {
	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.index[key]; ok {
		c.lru.MoveToFront(el)
		return el.Value.(*proxiedClientEntry)
	}

	unary := ConfigureProxy(CloneFastHTTPClientConfig(base), pc, logger)
	entry := &proxiedClientEntry{
		key:       key,
		unary:     unary,
		streaming: BuildStreamingClient(unary),
	}
	el := c.lru.PushFront(entry)
	c.index[key] = el
	if c.lru.Len() > c.cap {
		oldest := c.lru.Back()
		c.lru.Remove(oldest)
		delete(c.index, oldest.Value.(*proxiedClientEntry).key)
	}
	return entry
}

// proxyCacheKey canonicalises a proxy into a fixed-width identity. host:port is
// parsed with a default scheme so a scheme-less "host:port" and its explicit
// "http://host:port" form share an entry. The full resolved config is included
// so two proxies that dial differently never collide; the digest keeps raw
// passwords out of the map key.
func proxyCacheKey(pc *schemas.ProxyConfig) string {
	var b strings.Builder
	b.WriteString(string(pc.Type))
	b.WriteByte('|')
	b.WriteString(proxyHostPort(pc.URL.GetValue()))
	b.WriteByte('|')
	b.WriteString(pc.Username.GetValue())
	b.WriteByte('|')
	b.WriteString(pc.Password.GetValue())
	b.WriteByte('|')
	b.WriteString(pc.CACertPEM.GetValue())
	b.WriteByte('|')
	b.WriteString(pc.NoProxy)
	b.WriteByte('|')
	if pc.SkipTLSVerify {
		b.WriteByte('1')
	} else {
		b.WriteByte('0')
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// proxyHostPort returns the host:port of a proxy URL, adding an http:// scheme
// when none is present so url.Parse can extract Host. Malformed URLs fall back
// to the raw value rather than collapsing distinct proxies into one key.
func proxyHostPort(raw string) string {
	if raw == "" {
		return ""
	}
	candidate := raw
	if !strings.Contains(candidate, "://") {
		candidate = "http://" + candidate
	}
	parsed, err := url.Parse(candidate)
	if err != nil || parsed.Host == "" {
		return raw
	}
	return parsed.Host
}
