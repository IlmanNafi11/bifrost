package proxypool

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// memPoolStore is a minimal in-memory ProxyPoolStore for manager tests.
type memPoolStore struct {
	mu      sync.Mutex
	pools   map[string]schemas.ProxyPool
	binding map[string]schemas.ProxyPoolBinding
}

func newMemPoolStore(pools ...schemas.ProxyPool) *memPoolStore {
	s := &memPoolStore{pools: make(map[string]schemas.ProxyPool), binding: make(map[string]schemas.ProxyPoolBinding)}
	for _, p := range pools {
		s.pools[p.ID] = p
	}
	return s
}

func (s *memPoolStore) bind(keyID, poolID string, mechanism schemas.ProxyMechanism) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.binding[keyID] = schemas.ProxyPoolBinding{PoolID: poolID, KeyID: keyID, Mechanism: mechanism}
}

func (s *memPoolStore) ListPools(context.Context) ([]schemas.ProxyPool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]schemas.ProxyPool, 0, len(s.pools))
	for _, p := range s.pools {
		out = append(out, p)
	}
	return out, nil
}

func (s *memPoolStore) GetPool(_ context.Context, id string) (*schemas.ProxyPool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pools[id]
	if !ok {
		return nil, fmt.Errorf("pool %s not found", id)
	}
	return &p, nil
}

func (s *memPoolStore) CreatePool(context.Context, *schemas.ProxyPool) error { return nil }
func (s *memPoolStore) UpdatePool(context.Context, *schemas.ProxyPool) error { return nil }
func (s *memPoolStore) DeletePool(context.Context, string) error             { return nil }
func (s *memPoolStore) ImportProxies(context.Context, string, []schemas.PoolProxy) error {
	return nil
}
func (s *memPoolStore) DeleteProxies(context.Context, string, []string) error { return nil }
func (s *memPoolStore) SetProxiesEnabled(context.Context, string, []string, bool) error {
	return nil
}

func (s *memPoolStore) SetProxyStatus(_ context.Context, id, status string, latencyMs int64, egressIP string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for poolID, p := range s.pools {
		for i := range p.Proxies {
			if p.Proxies[i].ID == id {
				p.Proxies[i].Status = status
				p.Proxies[i].LatencyMs = latencyMs
				p.Proxies[i].EgressIP = egressIP
				s.pools[poolID] = p
				return nil
			}
		}
	}
	return nil
}

func (s *memPoolStore) BindKeys(context.Context, string, []string, schemas.ProxyMechanism) error {
	return nil
}
func (s *memPoolStore) UnbindKeys(context.Context, string, []string) error { return nil }
func (s *memPoolStore) ListBindings(context.Context) ([]schemas.ProxyPoolBinding, error) {
	return nil, nil
}

func (s *memPoolStore) GetBinding(_ context.Context, keyID string) (*schemas.ProxyPoolBinding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.binding[keyID]
	if !ok {
		return nil, nil
	}
	return &b, nil
}

func (s *memPoolStore) DeleteKeyBindings(context.Context, string) error { return nil }

// ttlKV is a KV store with real expiry, so cooldown recovery can be tested by
// rewriting a key with a TTL in the past.
type ttlKV struct {
	mu   sync.Mutex
	data map[string]struct {
		value   any
		expires time.Time
	}
}

func newTTLKV() *ttlKV {
	return &ttlKV{data: make(map[string]struct {
		value   any
		expires time.Time
	})}
}

func (k *ttlKV) Get(key string) (any, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	e, ok := k.data[key]
	if !ok {
		return nil, fmt.Errorf("key not found")
	}
	if !e.expires.IsZero() && time.Now().After(e.expires) {
		delete(k.data, key)
		return nil, fmt.Errorf("key expired")
	}
	return e.value, nil
}

func (k *ttlKV) SetWithTTL(key string, value any, ttl time.Duration) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	e := struct {
		value   any
		expires time.Time
	}{value: value}
	if ttl > 0 {
		e.expires = time.Now().Add(ttl)
	}
	k.data[key] = e
	return nil
}

func (k *ttlKV) SetNXWithTTL(key string, value any, ttl time.Duration) (bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if _, ok := k.data[key]; ok {
		return false, nil
	}
	return true, k.SetWithTTL(key, value, ttl)
}

func (k *ttlKV) Delete(key string) (bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if _, ok := k.data[key]; !ok {
		return false, nil
	}
	delete(k.data, key)
	return true, nil
}

type testLogger struct{}

func (testLogger) Debug(string, ...any)                   {}
func (testLogger) Info(string, ...any)                    {}
func (testLogger) Warn(string, ...any)                    {}
func (testLogger) Error(string, ...any)                   {}
func (testLogger) Fatal(string, ...any)                   {}
func (testLogger) SetLevel(schemas.LogLevel)              {}
func (testLogger) SetOutputType(schemas.LoggerOutputType) {}
func (testLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

func testManager(store schemas.ProxyPoolStore, kv schemas.KVStore) *Manager {
	return NewManager(store, kv, testLogger{})
}

func testCtx(sessionID string) *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	if sessionID != "" {
		ctx.SetValue(schemas.BifrostContextKeySessionID, sessionID)
	}
	return ctx
}

func testProxies(ids ...string) []schemas.PoolProxy {
	out := make([]schemas.PoolProxy, 0, len(ids))
	for i, id := range ids {
		out = append(out, schemas.PoolProxy{
			ID:      id,
			PoolID:  "pool",
			Type:    schemas.HTTPProxy,
			URL:     fmt.Sprintf("http://127.0.0.1:%d", 18080+i),
			Enabled: true,
			Weight:  1,
			Status:  ProxyStatusUnknown,
		})
	}
	return out
}

func pickedURL(t *testing.T, cfg *schemas.ProxyConfig) string {
	t.Helper()
	if cfg == nil {
		t.Fatal("expected a picked proxy, got nil")
	}
	return cfg.URL.GetValue()
}

func TestPickProxy_RoundRobin(t *testing.T) {
	store := newMemPoolStore(schemas.ProxyPool{ID: "pool", Proxies: testProxies("p1", "p2", "p3")})
	store.bind("key", "pool", schemas.ProxyRoundRobin)
	m := testManager(store, newTTLKV())
	ctx := testCtx("")

	want := []string{
		"http://127.0.0.1:18080",
		"http://127.0.0.1:18081",
		"http://127.0.0.1:18082",
		"http://127.0.0.1:18080",
		"http://127.0.0.1:18081",
	}
	for i, w := range want {
		cfg, err := m.PickProxy(ctx, "key")
		if err != nil {
			t.Fatalf("pick %d: %v", i, err)
		}
		if got := pickedURL(t, cfg); got != w {
			t.Fatalf("pick %d = %s, want %s", i, got, w)
		}
		m.Release(cfgURLID(cfg))
	}
}

// cfgURLID maps a picked config back to the proxy id from its test URL.
func cfgURLID(cfg *schemas.ProxyConfig) string {
	switch cfg.URL.GetValue() {
	case "http://127.0.0.1:18080":
		return "p1"
	case "http://127.0.0.1:18081":
		return "p2"
	case "http://127.0.0.1:18082":
		return "p3"
	}
	return ""
}

func TestPickProxy_AllMechanismsSkipDisabledDeadCooldown(t *testing.T) {
	proxies := testProxies("p1", "p2", "p3", "p4")
	proxies[0].Enabled = false
	proxies[1].Status = ProxyStatusDead
	// p3 is cooled down by three failures.
	store := newMemPoolStore(schemas.ProxyPool{ID: "pool", Proxies: proxies})
	kv := newTTLKV()
	m := testManager(store, kv)
	for i := 0; i < ProxyFailureCooldownThreshold; i++ {
		m.RecordFailure(testCtx(""), "p3", false)
	}

	for _, mechanism := range []schemas.ProxyMechanism{
		schemas.ProxyRoundRobin,
		schemas.ProxyRandom,
		schemas.ProxyWeighted,
		schemas.ProxyLeastConnections,
		schemas.ProxyFallback,
		schemas.ProxyStickySession,
	} {
		t.Run(string(mechanism), func(t *testing.T) {
			store.bind("key", "pool", mechanism)
			cfg, err := m.PickProxy(testCtx("session"), "key")
			if err != nil {
				t.Fatalf("PickProxy: %v", err)
			}
			if got := pickedURL(t, cfg); got != "http://127.0.0.1:18083" {
				t.Fatalf("picked %s, want the only eligible proxy p4", got)
			}
			m.Release(cfgURLID(cfg))
		})
	}
}

func TestPickProxy_FallbackAdvancesOnFailure(t *testing.T) {
	store := newMemPoolStore(schemas.ProxyPool{ID: "pool", Proxies: testProxies("p1", "p2", "p3")})
	store.bind("key", "pool", schemas.ProxyFallback)
	m := testManager(store, newTTLKV())
	ctx := testCtx("")

	for i, want := range []string{"http://127.0.0.1:18080", "http://127.0.0.1:18081", "http://127.0.0.1:18082"} {
		cfg, err := m.PickProxy(ctx, "key")
		if err != nil {
			t.Fatalf("pick %d: %v", i, err)
		}
		if got := pickedURL(t, cfg); got != want {
			t.Fatalf("pick %d = %s, want %s", i, got, want)
		}
		m.RecordFailure(ctx, cfgURLID(cfg), false)
	}
}

func TestPickProxy_EmptyPoolReturnsErrPoolExhausted(t *testing.T) {
	allDisabled := testProxies("p1")
	allDisabled[0].Enabled = false
	allDead := testProxies("p1")
	allDead[0].Status = ProxyStatusDead

	for _, tc := range []struct {
		name    string
		proxies []schemas.PoolProxy
	}{
		{"empty", nil},
		{"all disabled", allDisabled},
		{"all dead", allDead},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemPoolStore(schemas.ProxyPool{ID: "pool", Proxies: tc.proxies})
			store.bind("key", "pool", schemas.ProxyRoundRobin)
			m := testManager(store, newTTLKV())
			cfg, err := m.PickProxy(testCtx(""), "key")
			if err != ErrPoolExhausted {
				t.Fatalf("err = %v, want ErrPoolExhausted", err)
			}
			if cfg != nil {
				t.Fatal("exhausted pool returned a proxy config")
			}
		})
	}
}

func TestPickProxy_UnboundKeyReturnsNoProxy(t *testing.T) {
	store := newMemPoolStore(schemas.ProxyPool{ID: "pool", Proxies: testProxies("p1")})
	m := testManager(store, newTTLKV())
	cfg, err := m.PickProxy(testCtx(""), "unbound")
	if err != nil || cfg != nil {
		t.Fatalf("unbound key = (%v, %v), want (nil, nil)", cfg, err)
	}
}

func TestCooldown_Threshold3AndRecovery(t *testing.T) {
	store := newMemPoolStore(schemas.ProxyPool{ID: "pool", Proxies: testProxies("p1")})
	store.bind("key", "pool", schemas.ProxyRoundRobin)
	kv := newTTLKV()
	m := testManager(store, kv)
	ctx := testCtx("")

	// Two failures are tolerated.
	m.RecordFailure(ctx, "p1", false)
	m.RecordFailure(ctx, "p1", false)
	if _, err := m.PickProxy(ctx, "key"); err != nil {
		t.Fatalf("pick after 2 failures: %v", err)
	}

	// A success resets the streak; two further failures are still tolerated.
	m.RecordSuccess(ctx, "p1")
	m.RecordFailure(ctx, "p1", false)
	m.RecordFailure(ctx, "p1", false)
	if _, err := m.PickProxy(ctx, "key"); err != nil {
		t.Fatalf("pick after reset + 2 failures: %v", err)
	}

	// The third consecutive failure cools the proxy down.
	m.RecordFailure(ctx, "p1", false)
	if _, err := m.PickProxy(ctx, "key"); err != ErrPoolExhausted {
		t.Fatalf("pick while cooling down: err = %v, want ErrPoolExhausted", err)
	}
	if _, err := kv.Get(cooldownKey("p1")); err != nil {
		t.Fatal("expected a cooldown entry in KV")
	}

	// Once the cooldown window has passed the proxy is eligible again.
	if err := kv.SetWithTTL(cooldownKey("p1"), time.Now().Add(-time.Second).UnixNano(), time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := m.PickProxy(ctx, "key"); err != nil {
		t.Fatalf("pick after cooldown elapsed: %v", err)
	}
}

func TestRecordFailure_PermanentMarksDead(t *testing.T) {
	store := newMemPoolStore(schemas.ProxyPool{ID: "pool", Proxies: testProxies("p1")})
	store.bind("key", "pool", schemas.ProxyRoundRobin)
	m := testManager(store, newTTLKV())
	ctx := testCtx("")

	m.RecordFailure(ctx, "p1", true)
	if _, err := m.PickProxy(ctx, "key"); err != ErrPoolExhausted {
		t.Fatalf("permanent failure: err = %v, want ErrPoolExhausted", err)
	}
	m.RecordSuccess(ctx, "p1")
	if _, err := m.PickProxy(ctx, "key"); err != nil {
		t.Fatalf("success after permanent failure did not revive proxy: %v", err)
	}
}

func TestSticky_PinnedAcrossPicksAndFallsBackWithoutSession(t *testing.T) {
	store := newMemPoolStore(schemas.ProxyPool{ID: "pool", Proxies: testProxies("p1", "p2", "p3")})
	store.bind("key", "pool", schemas.ProxyStickySession)
	m := testManager(store, newTTLKV())

	first := pickedURL(t, mustPick(t, m, testCtx("s1"), "key"))
	for i := 0; i < 3; i++ {
		got := pickedURL(t, mustPick(t, m, testCtx("s1"), "key"))
		if got != first {
			t.Fatalf("session s1 pin drifted to %s, want %s", got, first)
		}
	}

	// A different session gets its own pin.
	other := pickedURL(t, mustPick(t, m, testCtx("s2"), "key"))
	if other == first {
		t.Fatal("different sessions pinned to the same proxy")
	}

	// Without a session the mechanism degrades to round robin.
	seen := map[string]bool{}
	for i := 0; i < 6; i++ {
		seen[pickedURL(t, mustPick(t, m, testCtx(""), "key"))] = true
	}
	if len(seen) < 2 {
		t.Fatalf("sessionless sticky picks did not rotate: %v", seen)
	}

	// A pinned proxy that dies is re-picked.
	deadID := cfgURLIDFromURL(first)
	if err := store.SetProxyStatus(context.Background(), deadID, ProxyStatusDead, 0, ""); err != nil {
		t.Fatal(err)
	}
	got := pickedURL(t, mustPick(t, m, testCtx("s1"), "key"))
	if got == first {
		t.Fatalf("session stayed pinned to dead proxy %s", first)
	}
}

func mustPick(t *testing.T, m *Manager, ctx *schemas.BifrostContext, keyID string) *schemas.ProxyConfig {
	t.Helper()
	cfg, err := m.PickProxy(ctx, keyID)
	if err != nil {
		t.Fatalf("PickProxy(%s): %v", keyID, err)
	}
	return cfg
}

func cfgURLIDFromURL(raw string) string {
	switch raw {
	case "http://127.0.0.1:18080":
		return "p1"
	case "http://127.0.0.1:18081":
		return "p2"
	case "http://127.0.0.1:18082":
		return "p3"
	}
	return ""
}

func TestPickProxy_WeightedFavorsHigherWeight(t *testing.T) {
	proxies := testProxies("p1", "p2")
	proxies[0].Weight = 1
	proxies[1].Weight = 3
	store := newMemPoolStore(schemas.ProxyPool{ID: "pool", Proxies: proxies})
	store.bind("key", "pool", schemas.ProxyWeighted)
	m := testManager(store, newTTLKV())
	ctx := testCtx("")

	heavy := 0
	for i := 0; i < 800; i++ {
		cfg := mustPick(t, m, ctx, "key")
		if strings.HasSuffix(cfg.URL.GetValue(), ":18081") {
			heavy++
		}
		m.Release(cfgURLID(cfg))
	}
	if heavy < 450 {
		t.Fatalf("weight 3 proxy chosen only %d/800 times", heavy)
	}
}

func TestPickProxy_LeastConnections(t *testing.T) {
	store := newMemPoolStore(schemas.ProxyPool{ID: "pool", Proxies: testProxies("p1", "p2")})
	store.bind("key", "pool", schemas.ProxyLeastConnections)
	m := testManager(store, newTTLKV())
	ctx := testCtx("")

	first := mustPick(t, m, ctx, "key")
	if got := pickedURL(t, first); got != "http://127.0.0.1:18080" {
		t.Fatalf("first pick = %s, want p1", got)
	}
	mustPick(t, m, ctx, "key") // p1 and p2 now each have one in flight.
	m.Release("p1")            // p1 is now the least-connected proxy.
	third := mustPick(t, m, ctx, "key")
	if got := pickedURL(t, third); got != "http://127.0.0.1:18080" {
		t.Fatalf("third pick = %s, want p1 after releasing it", got)
	}
}
