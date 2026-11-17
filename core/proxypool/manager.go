// Package proxypool resolves the proxy bound to an upstream key into a concrete
// ProxyConfig for a request attempt. Health state (cooldown, permanent-dead and
// sticky pins) lives in the shared KVStore so it survives restarts and is shared
// across replicas; selection counters are per-instance best effort.
package proxypool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/network"
	"github.com/maximhq/bifrost/core/schemas"
)

// DefaultProbeEchoEndpoint is the mode-B echo-IP endpoint. It is a builder
// choice, overridable per manager with SetEchoIPEndpoint; a probe that cannot
// reach it or gets a non-IP answer is recorded as dead, never "assume alive".
const DefaultProbeEchoEndpoint = "https://api.ipify.org"

// Proxy status values stored on a PoolProxy by the probe lifecycle.
const (
	ProxyStatusUnknown = "unknown"
	ProxyStatusAlive   = "alive"
	ProxyStatusDead    = "dead"
)

// Cooldown policy: three consecutive transport failures park a proxy for a
// minute. A success resets the streak; a permanent failure marks it dead.
const (
	ProxyFailureCooldownThreshold = 3
	ProxyFailureCooldown          = 60 * time.Second
)

// ErrPoolExhausted is returned when a key is bound to a pool with no eligible
// proxy. Callers must surface it as a terminal 503 rather than dial through a
// provider or global proxy, which would leak the caller's real egress IP.
var ErrPoolExhausted = errors.New("proxy pool cannot serve: no enabled, live proxy")

// Manager resolves key bindings to picked proxies. The zero value is not
// usable; construct one with NewManager.
type Manager struct {
	store  schemas.ProxyPoolStore
	kv     schemas.KVStore
	logger schemas.Logger
	state  *stateStore

	echoIPURL string // mode-B echo endpoint; immutable after construction

	mu       sync.Mutex
	rng      *rand.Rand
	cursors  map[string]uint64 // pool id -> round-robin cursor
	inFlight map[string]int64  // proxy id -> attempts in flight

	stats metrics
}

// Stats returns a snapshot of selection, exhaustion, failure and probe counters.
// It is a diagnostics read, not a request-path call: snapshot allocates a small
// map. The counters it reads are atomic and add nothing to unbound requests.
func (m *Manager) Stats() Stats {
	return m.stats.snapshot()
}

// NewManager builds a selection manager. A nil KVStore keeps health state in
// process memory; a nil logger discards messages.
func NewManager(store schemas.ProxyPoolStore, kv schemas.KVStore, logger schemas.Logger) *Manager {
	if logger == nil {
		logger = discardLogger{}
	}
	return &Manager{
		store:     store,
		kv:        kv,
		logger:    logger,
		state:     newStateStore(kv),
		echoIPURL: DefaultProbeEchoEndpoint,
		rng:       rand.New(rand.NewSource(time.Now().UnixNano())),
		cursors:   make(map[string]uint64),
		inFlight:  make(map[string]int64),
	}
}

// PickProxy returns the proxy a bound key should use for this attempt. It
// returns (nil, nil) when the key has no binding, leaving the caller's provider
// or global proxy path untouched. Other errors come from the store; an empty
// or fully ineligible pool returns ErrPoolExhausted.
func (m *Manager) PickProxy(ctx *schemas.BifrostContext, keyID string) (*schemas.ProxyConfig, error) {
	ctx = ensureCtx(ctx)
	binding, err := m.store.GetBinding(ctx, keyID)
	if err != nil {
		return nil, err
	}
	if binding == nil {
		return nil, nil
	}
	pool, err := m.store.GetPool(ctx, binding.PoolID)
	if err != nil {
		return nil, err
	}
	if pool == nil {
		m.stats.poolExhausted.Add(1)
		return nil, ErrPoolExhausted
	}

	candidates := m.eligible(pool)
	if len(candidates) == 0 {
		m.stats.poolExhausted.Add(1)
		return nil, ErrPoolExhausted
	}

	picked := m.selectProxy(ctx, binding.Mechanism, binding.PoolID, keyID, candidates)
	m.stats.recordSelection(binding.Mechanism)
	proxy := proxyConfigFrom(picked)
	return proxy, nil
}

// RecordSuccess clears a proxy's failure streak and cooldown and releases the
// in-flight slot taken by its selection.
func (m *Manager) RecordSuccess(ctx *schemas.BifrostContext, proxyID string) {
	m.state.del(failureKey(proxyID))
	m.state.del(cooldownKey(proxyID))
	m.state.del(deadKey(proxyID))
	m.Release(proxyID)
}

// RecordFailure records an attempt failure on a proxy. A permanent failure
// (407 proxy auth, forbidden) marks it dead immediately; transport failures
// escalate to a 60s cooldown after ProxyFailureCooldownThreshold in a row. It
// also advances the per-request fallback pointer.
func (m *Manager) RecordFailure(ctx *schemas.BifrostContext, proxyID string, permanent bool) {
	ctx = ensureCtx(ctx)
	count := m.failureCount(proxyID)
	if permanent {
		m.state.set(deadKey(proxyID), "1", 0)
		m.state.del(failureKey(proxyID))
		m.state.del(cooldownKey(proxyID))
		m.stats.markedDead.Add(1)
		m.logger.Warn("proxy pool: proxy marked dead", "proxy_id", proxyID)
	} else if count+1 >= ProxyFailureCooldownThreshold {
		m.state.set(cooldownKey(proxyID), time.Now().Add(ProxyFailureCooldown).UnixNano(), ProxyFailureCooldown)
		m.state.del(failureKey(proxyID))
		m.stats.cooledDown.Add(1)
		m.logger.Warn("proxy pool: proxy cooled down", "proxy_id", proxyID, "duration", ProxyFailureCooldown)
	} else {
		m.state.set(failureKey(proxyID), count+1, 0)
	}
	idx, _ := ctx.Value(fallbackIndexKey).(int)
	ctx.SetValue(fallbackIndexKey, idx+1)
}

// Release returns an in-flight slot taken by a least-connections pick. Callers
// that finish an attempt through a picked proxy should call this once.
func (m *Manager) Release(proxyID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.inFlight[proxyID] > 0 {
		m.inFlight[proxyID]--
	}
}

// eligible filters a pool to proxies every selection mechanism may consider:
// enabled, not dead (in the store or the runtime overlay), and not cooling down.
func (m *Manager) eligible(pool *schemas.ProxyPool) []schemas.PoolProxy {
	out := make([]schemas.PoolProxy, 0, len(pool.Proxies))
	now := time.Now()
	for _, p := range pool.Proxies {
		if !p.Enabled || p.Status == ProxyStatusDead {
			continue
		}
		if _, dead := m.state.get(deadKey(p.ID)); dead {
			continue
		}
		if now.Before(m.cooldownUntil(p.ID)) {
			continue
		}
		out = append(out, p)
	}
	return out
}

func (m *Manager) cooldownUntil(proxyID string) time.Time {
	raw, ok := m.state.get(cooldownKey(proxyID))
	if !ok {
		return time.Time{}
	}
	return time.Unix(0, parseIntState(raw))
}

func (m *Manager) failureCount(proxyID string) int {
	raw, ok := m.state.get(failureKey(proxyID))
	if !ok {
		return 0
	}
	return int(parseIntState(raw))
}

func (m *Manager) nextCursor(poolID string) uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.cursors[poolID]
	m.cursors[poolID] = c + 1
	return c
}

// SetEchoIPEndpoint overrides the endpoint used by mode-B probes to read the
// visible egress IP. It must be called before the manager serves requests.
func (m *Manager) SetEchoIPEndpoint(rawURL string) {
	if strings.TrimSpace(rawURL) != "" {
		m.echoIPURL = strings.TrimSpace(rawURL)
	}
}

// Probe verifies one proxy. Mode A completes a CONNECT handshake through it;
// mode B repeats the handshake and fetches the echo endpoint to read the egress
// IP. Both return "dead" on any failure — a probe never silently assumes a
// proxy is alive. The caller persists the returned status, latency and IP with
// SetProxyStatus.
func (m *Manager) Probe(ctx context.Context, p schemas.PoolProxy, mode schemas.ProxyProbeMode) (status string, latencyMs int64, egressIP string, err error) {
	// Probe runs on the admin path, so a defer here is free on the request path.
	// Every exit records an outcome and, on success, the observed latency.
	defer func() {
		if status == ProxyStatusAlive {
			m.stats.probesAlive.Add(1)
			m.stats.lastProbeLatencyMs.Store(latencyMs)
		} else {
			m.stats.probesDead.Add(1)
		}
	}()
	proxyURL, err := proxyURLFor(p)
	if err != nil {
		return ProxyStatusDead, 0, "", err
	}
	target, err := probeTargetAddr(m.echoIPURL)
	if err != nil {
		return ProxyStatusDead, 0, "", err
	}

	start := time.Now()
	dialCtx, cancel := context.WithTimeout(ctx, network.ProxyHandshakeTimeout)
	defer cancel()
	conn, err := network.DialViaProxy(dialCtx, proxyURL, target)
	if err != nil {
		return ProxyStatusDead, 0, "", fmt.Errorf("proxy connect: %w", err)
	}
	_ = conn.Close()

	if mode == schemas.ProxyProbeModeA {
		return ProxyStatusAlive, time.Since(start).Milliseconds(), "", nil
	}

	ip, err := m.fetchEgressIP(ctx, proxyURL)
	if err != nil {
		return ProxyStatusDead, time.Since(start).Milliseconds(), "", err
	}
	return ProxyStatusAlive, time.Since(start).Milliseconds(), ip, nil
}

// proxyURLFor turns a stored proxy into a URL with the scheme implied by its
// type and credentials attached, ready for network.DialViaProxy.
func proxyURLFor(p schemas.PoolProxy) (*url.URL, error) {
	scheme := "http"
	switch p.Type {
	case schemas.Socks5Proxy:
		scheme = "socks5"
	case schemas.HTTPProxy:
		scheme = "http"
	}
	raw := p.URL
	if !strings.Contains(raw, "://") {
		raw = scheme + "://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse proxy url: %w", err)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("proxy url has no host")
	}
	if p.Username != "" {
		u.User = url.UserPassword(p.Username, p.Password.GetValue())
	}
	return u, nil
}

// probeTargetAddr is the host:port the CONNECT handshake aims at: the echo
// endpoint's own authority, defaulting the port by scheme.
func probeTargetAddr(echoURL string) (string, error) {
	u, err := url.Parse(echoURL)
	if err != nil || u.Hostname() == "" {
		return "", fmt.Errorf("invalid echo endpoint %q", echoURL)
	}
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	return net.JoinHostPort(u.Hostname(), port), nil
}

// fetchEgressIP fetches the echo endpoint through the proxy and reads the IP
// from its body. A non-IP answer is an error so the probe is recorded dead.
func (m *Manager) fetchEgressIP(ctx context.Context, proxyURL *url.URL) (string, error) {
	tr := &http.Transport{
		DialContext: func(dialCtx context.Context, _, addr string) (net.Conn, error) {
			return network.DialViaProxy(dialCtx, proxyURL, addr)
		},
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: network.ProxyHandshakeTimeout}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.echoIPURL, nil)
	if err != nil {
		return "", fmt.Errorf("build echo request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch echo endpoint: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("echo endpoint status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil {
		return "", fmt.Errorf("read echo response: %w", err)
	}
	ip := strings.TrimSpace(string(body))
	if net.ParseIP(ip) == nil {
		return "", fmt.Errorf("echo endpoint returned a non-IP: %q", ip)
	}
	return ip, nil
}

// parseIntState reads a counter written by a local store or returned as JSON
// bytes by a replicated one.
func parseIntState(raw any) int64 {
	switch v := raw.(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	case string:
		n, _ := strconv.ParseInt(strings.Trim(v, "\""), 10, 64)
		return n
	case []byte:
		n, _ := strconv.ParseInt(strings.Trim(string(v), "\""), 10, 64)
		return n
	}
	return 0
}

func stateString(raw any) string {
	switch v := raw.(type) {
	case string:
		return v
	case []byte:
		return strings.Trim(string(v), "\"")
	}
	return ""
}

func proxyConfigFrom(p schemas.PoolProxy) *schemas.ProxyConfig {
	return &schemas.ProxyConfig{
		Type:     p.Type,
		URL:      schemas.NewSecretVar(p.URL),
		Username: schemas.NewSecretVar(p.Username),
		Password: p.Password,
	}
}

func ensureCtx(ctx *schemas.BifrostContext) *schemas.BifrostContext {
	if ctx == nil {
		return schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	}
	return ctx
}

const stateKeyPrefix = "proxy-pool:v1:"

func cooldownKey(proxyID string) string { return stateKeyPrefix + "cooldown:" + proxyID }
func failureKey(proxyID string) string  { return stateKeyPrefix + "failures:" + proxyID }
func deadKey(proxyID string) string     { return stateKeyPrefix + "dead:" + proxyID }

// stickyKey scopes a session's pin the same way core.SessionStateKey scopes
// session state: by session id and the caller identity from the grant. It is
// local to avoid an import cycle once core wires the manager into Bifrost.
func stickyKey(ctx *schemas.BifrostContext, poolID, keyID string) string {
	virtualKeyID, userID := "", ""
	if grant := ctx.Grant(); grant != nil {
		if identity := grant.Identity(); identity != nil {
			if key := identity.VirtualKey(); key != nil {
				virtualKeyID = key.ID
			}
			if user := identity.User(); user != nil {
				userID = user.ID
			}
		}
	}
	sessionID, _ := ctx.Value(schemas.BifrostContextKeySessionID).(string)
	h := sha256.New()
	for _, part := range []string{virtualKeyID, userID, sessionID, poolID, keyID} {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	return stateKeyPrefix + "sticky:" + hex.EncodeToString(h.Sum(nil))
}

// proxyPoolCtxKey is a private context key type so proxy state never collides
// with a reserved BifrostContextKey. Values are small handles (an int pointer).
type proxyPoolCtxKey string

const fallbackIndexKey proxyPoolCtxKey = "proxy-pool-fallback-index"

// stateStore keeps health state in the injected KV store when one is present,
// and in a bounded in-process map otherwise.
type stateStore struct {
	kv  schemas.KVStore
	mu  sync.Mutex
	mem map[string]memEntry
}

type memEntry struct {
	value   any
	expires time.Time
}

func newStateStore(kv schemas.KVStore) *stateStore {
	return &stateStore{kv: kv, mem: make(map[string]memEntry)}
}

func (s *stateStore) get(key string) (any, bool) {
	if s.kv != nil {
		value, err := s.kv.Get(key)
		return value, err == nil && value != nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.mem[key]
	if !ok {
		return nil, false
	}
	if !e.expires.IsZero() && time.Now().After(e.expires) {
		delete(s.mem, key)
		return nil, false
	}
	return e.value, true
}

func (s *stateStore) set(key string, value any, ttl time.Duration) {
	if s.kv != nil {
		_ = s.kv.SetWithTTL(key, value, ttl)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e := memEntry{value: value}
	if ttl > 0 {
		e.expires = time.Now().Add(ttl)
	}
	s.mem[key] = e
}

func (s *stateStore) del(key string) {
	if s.kv != nil {
		_, _ = s.kv.Delete(key)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.mem, key)
}

// discardLogger lets the manager be used without logging setup.
type discardLogger struct{}

func (discardLogger) Debug(string, ...any)                   {}
func (discardLogger) Info(string, ...any)                    {}
func (discardLogger) Warn(string, ...any)                    {}
func (discardLogger) Error(string, ...any)                   {}
func (discardLogger) Fatal(string, ...any)                   {}
func (discardLogger) SetLevel(schemas.LogLevel)              {}
func (discardLogger) SetOutputType(schemas.LoggerOutputType) {}
func (discardLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}
