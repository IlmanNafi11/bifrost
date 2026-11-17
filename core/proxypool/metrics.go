package proxypool

import (
	"sync/atomic"

	"github.com/maximhq/bifrost/core/schemas"
)

// Stats is a point-in-time snapshot of proxy-pool activity. It is intended for
// diagnostics and admin surfaces; building it allocates a small map, so callers
// must not use it on the request path. The counters themselves are atomic and
// add no allocations to selection.
type Stats struct {
	Selections         map[schemas.ProxyMechanism]uint64 `json:"selections"`
	PoolExhausted      uint64                            `json:"pool_exhausted"`
	MarkedDead         uint64                            `json:"marked_dead"`
	CooledDown         uint64                            `json:"cooled_down"`
	ProbesAlive        uint64                            `json:"probes_alive"`
	ProbesDead         uint64                            `json:"probes_dead"`
	LastProbeLatencyMs int64                             `json:"last_probe_latency_ms"`
}

// metrics holds the hot-path counters. A fixed-size array keeps selection
// allocation-free; mechanismIndex maps every stored value (including unknown
// ones, which selectProxy treats as round robin) to a slot.
type metrics struct {
	selections         [6]atomic.Uint64
	poolExhausted      atomic.Uint64
	markedDead         atomic.Uint64
	cooledDown         atomic.Uint64
	probesAlive        atomic.Uint64
	probesDead         atomic.Uint64
	lastProbeLatencyMs atomic.Int64
}

var mechanisms = [...]schemas.ProxyMechanism{
	schemas.ProxyRoundRobin,
	schemas.ProxyRandom,
	schemas.ProxyWeighted,
	schemas.ProxyLeastConnections,
	schemas.ProxyFallback,
	schemas.ProxyStickySession,
}

func mechanismIndex(mechanism schemas.ProxyMechanism) int {
	for i, candidate := range mechanisms {
		if mechanism == candidate {
			return i
		}
	}
	return 0 // unknown mechanisms fall back to round robin in selectProxy
}

func (m *metrics) recordSelection(mechanism schemas.ProxyMechanism) {
	m.selections[mechanismIndex(mechanism)].Add(1)
}

// snapshot returns a copy safe for JSON serialization and concurrent readers.
func (m *metrics) snapshot() Stats {
	out := Stats{
		Selections:         make(map[schemas.ProxyMechanism]uint64, len(mechanisms)),
		PoolExhausted:      m.poolExhausted.Load(),
		MarkedDead:         m.markedDead.Load(),
		CooledDown:         m.cooledDown.Load(),
		ProbesAlive:        m.probesAlive.Load(),
		ProbesDead:         m.probesDead.Load(),
		LastProbeLatencyMs: m.lastProbeLatencyMs.Load(),
	}
	for i, mechanism := range mechanisms {
		if count := m.selections[i].Load(); count > 0 {
			out.Selections[mechanism] = count
		}
	}
	return out
}
