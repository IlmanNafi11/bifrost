package utils

import (
	"container/list"
	"context"
	"net/http"
	"sync"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// proxiedHTTPClientEntry holds one net/http client for a canonical proxy.
type proxiedHTTPClientEntry struct {
	key    string
	client *http.Client
}

// ProxiedHTTPClientCache is the net/http counterpart of ProxiedClientCache. It
// builds and reuses *http.Client instances whose transport dials the egress
// proxy published on the attempt's BifrostContext. It is used by providers whose
// inference client is net/http (Bedrock's Converse/realtime paths) rather than
// fasthttp. Unbound attempts get the base client back unchanged; bound attempts
// pay one canonical-key hash per request and, on first use of a proxy, one
// transport clone. The cap is an LRU so a provider bound to many pool proxies
// does not grow transports without limit.
//
// Pool proxies are http:// or socks5:// URLs, which net/http dispatches natively
// via Transport.Proxy, so no proxy-hop TLS setup is needed here. The base
// transport's timeouts, HTTP/2 settings, connection limits and target TLS config
// are preserved by Clone; only Proxy is swapped for the picked proxy.
type ProxiedHTTPClientCache struct {
	cap   int
	mu    sync.Mutex
	index map[string]*list.Element
	lru   *list.List
}

// NewProxiedHTTPClientCache returns a cache holding at most cap distinct proxies.
// A non-positive cap selects DefaultProxiedClientCacheCap.
func NewProxiedHTTPClientCache(cap int) *ProxiedHTTPClientCache {
	if cap <= 0 {
		cap = DefaultProxiedClientCacheCap
	}
	return &ProxiedHTTPClientCache{
		cap:   cap,
		index: make(map[string]*list.Element),
		lru:   list.New(),
	}
}

// Unary returns the client for the proxy picked on ctx, or base when unbound.
func (c *ProxiedHTTPClientCache) Unary(ctx context.Context, base *http.Client, logger schemas.Logger) *http.Client {
	return c.clientFor(ctx, base, logger)
}

// Streaming is Unary for streaming requests. The streaming base already carries
// no overall Timeout, and Clone preserves that, so the same construction applies.
func (c *ProxiedHTTPClientCache) Streaming(ctx context.Context, base *http.Client, logger schemas.Logger) *http.Client {
	return c.clientFor(ctx, base, logger)
}

func (c *ProxiedHTTPClientCache) clientFor(ctx context.Context, base *http.Client, logger schemas.Logger) *http.Client {
	if ctx == nil || base == nil {
		return base
	}
	pc, _ := ctx.Value(schemas.BifrostContextKeyProviderProxyConfig).(*schemas.ProxyConfig)
	if pc == nil || pc.Type == schemas.NoProxy {
		return base
	}
	return c.getOrBuild(proxyCacheKey(pc), pc, base, logger).client
}

func (c *ProxiedHTTPClientCache) getOrBuild(key string, pc *schemas.ProxyConfig, base *http.Client, logger schemas.Logger) *proxiedHTTPClientEntry {
	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.index[key]; ok {
		c.lru.MoveToFront(el)
		return el.Value.(*proxiedHTTPClientEntry)
	}

	built := cloneHTTPClientForProxy(base, pc, logger)
	entry := &proxiedHTTPClientEntry{key: key, client: built}
	el := c.lru.PushFront(entry)
	c.index[key] = el
	if c.lru.Len() > c.cap {
		oldest := c.lru.Back()
		c.lru.Remove(oldest)
		delete(c.index, oldest.Value.(*proxiedHTTPClientEntry).key)
	}
	return entry
}

// cloneHTTPClientForProxy clones base's transport and points Proxy at the picked
// proxy. When the base transport is not an *http.Transport it cannot be safely
// reconfigured, so the base client is returned (the caller's proxy_config path);
// every provider that uses this cache (Bedrock) builds an *http.Transport.
func cloneHTTPClientForProxy(base *http.Client, pc *schemas.ProxyConfig, logger schemas.Logger) *http.Client {
	baseTransport, ok := base.Transport.(*http.Transport)
	if !ok {
		if base.Transport != nil {
			// A custom RoundTripper cannot be safely reconfigured; fall back to
			// the base client's own (proxy_config) path.
			return base
		}
		// No transport set: net/http would use DefaultTransport, so clone that.
		baseTransport = http.DefaultTransport.(*http.Transport)
	}
	proxyFn, _, err := NetHTTPProxy(pc)
	if err != nil || proxyFn == nil {
		if err != nil {
			logger.Warn("invalid pool proxy for net/http client: " + err.Error())
		}
		return base
	}
	transport := baseTransport.Clone()
	transport.Proxy = proxyFn
	clone := *base
	clone.Transport = transport
	return &clone
}
