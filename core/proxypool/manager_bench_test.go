package proxypool

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// benchStore returns pre-allocated values so these benchmarks measure the
// manager's own work, not the in-memory test store's struct copies. It embeds
// memPoolStore to inherit the rest of the ProxyPoolStore methods.
type benchStore struct {
	*memPoolStore
	binding *schemas.ProxyPoolBinding
	pool    *schemas.ProxyPool
}

func (s *benchStore) GetBinding(context.Context, string) (*schemas.ProxyPoolBinding, error) {
	return s.binding, nil
}

func (s *benchStore) GetPool(context.Context, string) (*schemas.ProxyPool, error) {
	return s.pool, nil
}

// BenchmarkPickProxy_Bound measures the per-attempt cost for a key bound to a
// pool: binding lookup, pool lookup, eligibility filter and mechanism pick.
// This is the path that adds allocations over the unary request.
func BenchmarkPickProxy_Bound(b *testing.B) {
	store := &benchStore{
		memPoolStore: newMemPoolStore(),
		binding:      &schemas.ProxyPoolBinding{PoolID: "pool", KeyID: "key", Mechanism: schemas.ProxyRoundRobin},
		pool:         &schemas.ProxyPool{ID: "pool", Proxies: testProxies("p1", "p2", "p3")},
	}
	m := testManager(store, nil)
	ctx := testCtx("")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cfg, err := m.PickProxy(ctx, "key")
		if err != nil || cfg == nil {
			b.Fatalf("PickProxy(bound) = (%v, %v)", cfg, err)
		}
	}
}

// BenchmarkPickProxy_Unbound measures the common case: the key has no binding,
// so PickProxy returns (nil, nil) after a single store lookup. This path must
// stay at zero allocations so unbound keys pay nothing for the feature.
func BenchmarkPickProxy_Unbound(b *testing.B) {
	store := &benchStore{memPoolStore: newMemPoolStore()}
	m := testManager(store, nil)
	ctx := testCtx("")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cfg, err := m.PickProxy(ctx, "unbound")
		if err != nil || cfg != nil {
			b.Fatalf("PickProxy(unbound) = (%v, %v)", cfg, err)
		}
	}
}
