package proxypool

import (
	"math"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// selectProxy applies the binding's mechanism to the eligible candidate set.
// Unknown mechanisms default to round robin so a bad stored value cannot stall
// a bound key.
func (m *Manager) selectProxy(ctx *schemas.BifrostContext, mechanism schemas.ProxyMechanism, poolID, keyID string, candidates []schemas.PoolProxy) schemas.PoolProxy {
	switch mechanism {
	case schemas.ProxyRandom:
		return m.selectRandom(candidates)
	case schemas.ProxyWeighted:
		return m.selectWeighted(candidates)
	case schemas.ProxyLeastConnections:
		return m.selectLeastConnections(poolID, candidates)
	case schemas.ProxyFallback:
		return m.selectFallback(ctx, candidates)
	case schemas.ProxyStickySession:
		return m.selectSticky(ctx, poolID, keyID, candidates)
	default:
		return m.selectRoundRobin(poolID, candidates)
	}
}

func (m *Manager) selectRoundRobin(poolID string, candidates []schemas.PoolProxy) schemas.PoolProxy {
	return candidates[int(m.nextCursor(poolID)%uint64(len(candidates)))]
}

func (m *Manager) selectRandom(candidates []schemas.PoolProxy) schemas.PoolProxy {
	m.mu.Lock()
	idx := m.rng.Intn(len(candidates))
	m.mu.Unlock()
	return candidates[idx]
}

func (m *Manager) selectWeighted(candidates []schemas.PoolProxy) schemas.PoolProxy {
	m.mu.Lock()
	defer m.mu.Unlock()

	total := 0.0
	for _, p := range candidates {
		if p.Weight > 0 {
			total += p.Weight
		}
	}
	if total <= 0 {
		return candidates[m.rng.Intn(len(candidates))]
	}
	r := m.rng.Float64() * total
	cumulative := 0.0
	for _, p := range candidates {
		cumulative += math.Max(0, p.Weight)
		if r < cumulative {
			return p
		}
	}
	return candidates[len(candidates)-1]
}

// selectLeastConnections picks the proxy with the fewest attempts in flight.
// Ties go to the first candidate in round-robin order, so the choice is stable
// and does not favour a fixed proxy when all counters are equal.
func (m *Manager) selectLeastConnections(poolID string, candidates []schemas.PoolProxy) schemas.PoolProxy {
	offset := int(m.nextCursor(poolID) % uint64(len(candidates)))
	best := candidates[offset]
	bestCount := m.inFlight[best.ID]
	for i := 1; i < len(candidates); i++ {
		p := candidates[(offset+i)%len(candidates)]
		if n := m.inFlight[p.ID]; n < bestCount {
			best, bestCount = p, n
		}
	}
	m.mu.Lock()
	m.inFlight[best.ID]++
	m.mu.Unlock()
	return best
}

// selectFallback walks candidates in order across a request's attempts. The
// pointer lives on the per-request context and is advanced by RecordFailure.
func (m *Manager) selectFallback(ctx *schemas.BifrostContext, candidates []schemas.PoolProxy) schemas.PoolProxy {
	idx, _ := ctx.Value(fallbackIndexKey).(int)
	if idx < 0 {
		idx = 0
	}
	return candidates[idx%len(candidates)]
}

// selectSticky pins a session to one proxy via KV. Without a session id it
// degrades to round robin; a pin that is no longer eligible is replaced.
func (m *Manager) selectSticky(ctx *schemas.BifrostContext, poolID, keyID string, candidates []schemas.PoolProxy) schemas.PoolProxy {
	sessionID, _ := ctx.Value(schemas.BifrostContextKeySessionID).(string)
	if sessionID == "" {
		return m.selectRoundRobin(poolID, candidates)
	}

	pinKey := stickyKey(ctx, poolID, keyID)
	if raw, ok := m.state.get(pinKey); ok {
		pinned := stateString(raw)
		for _, p := range candidates {
			if p.ID == pinned {
				return p
			}
		}
	}

	chosen := m.selectRoundRobin(poolID, candidates)
	ttl := schemas.DefaultSessionStickyTTL
	if configured, ok := ctx.Value(schemas.BifrostContextKeySessionTTL).(time.Duration); ok && configured > 0 {
		ttl = configured
	}
	m.state.set(pinKey, chosen.ID, ttl)
	return chosen
}
