package schemas

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// ProxyMechanism selects which proxy a bound pool serves for a request.
type ProxyMechanism string

// ProxyProbeMode selects how a pool proxy is verified. Mode A is a CONNECT
// handshake pre-check; mode B repeats it and fetches the visible egress IP.
type ProxyProbeMode string

const (
	ProxyProbeModeA ProxyProbeMode = "a"
	ProxyProbeModeB ProxyProbeMode = "b"
)

const (
	ProxyRoundRobin       ProxyMechanism = "round_robin"       // rotate in order per pool
	ProxyRandom           ProxyMechanism = "random"            // uniform random pick
	ProxyWeighted         ProxyMechanism = "weighted"          // weighted random by proxy weight
	ProxyLeastConnections ProxyMechanism = "least_connections" // fewest in-flight, ties to cursor
	ProxyFallback         ProxyMechanism = "fallback"          // ordered, advance on failure
	ProxyStickySession    ProxyMechanism = "sticky_session"    // pinned by session, else round_robin
)

// PoolProxy is one proxy endpoint inside a pool.
type PoolProxy struct {
	ID           string     `json:"id"`
	PoolID       string     `json:"pool_id"`
	Type         ProxyType  `json:"type"`
	URL          string     `json:"url"`
	Username     string     `json:"username"`
	Password     *SecretVar `json:"-"`
	Weight       float64    `json:"weight"`
	Enabled      bool       `json:"enabled"`
	Status       string     `json:"status"`
	LatencyMs    int64      `json:"latency_ms"`
	EgressIP     string     `json:"egress_ip"`
	LastTestedAt *time.Time `json:"last_tested_at,omitempty"`
}

// ProxyPool is a reusable set of proxies bound to upstream keys.
type ProxyPool struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Proxies     []PoolProxy `json:"proxies"`
}

// ProxyPoolBinding binds a pool and a selection mechanism to one upstream key.
type ProxyPoolBinding struct {
	PoolID    string         `json:"pool_id"`
	KeyID     string         `json:"key_id"`
	Mechanism ProxyMechanism `json:"mechanism"`
}

// ProxyPoolStore is the storage contract for proxy pools, proxies, and key bindings.
type ProxyPoolStore interface {
	ListPools(ctx context.Context) ([]ProxyPool, error)
	GetPool(ctx context.Context, id string) (*ProxyPool, error)
	CreatePool(ctx context.Context, p *ProxyPool) error
	UpdatePool(ctx context.Context, p *ProxyPool) error
	DeletePool(ctx context.Context, id string) error
	ImportProxies(ctx context.Context, poolID string, proxies []PoolProxy) error
	DeleteProxies(ctx context.Context, poolID string, ids []string) error
	SetProxiesEnabled(ctx context.Context, poolID string, ids []string, enabled bool) error
	SetProxyStatus(ctx context.Context, id string, status string, latencyMs int64, egressIP string) error
	BindKeys(ctx context.Context, poolID string, keyIDs []string, mechanism ProxyMechanism) error
	UnbindKeys(ctx context.Context, poolID string, keyIDs []string) error
	ListBindings(ctx context.Context) ([]ProxyPoolBinding, error)
	GetBinding(ctx context.Context, keyID string) (*ProxyPoolBinding, error)
	DeleteKeyBindings(ctx context.Context, keyID string) error
}

// ParseImport parses a newline-separated proxy list. Each non-empty, non-comment
// line is one proxy; duplicates collapse by host:port with the first form winning.
func ParseImport(text string) ([]PoolProxy, error) {
	seen := make(map[string]struct{})
	var out []PoolProxy
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		p, key, err := parseImportLine(line)
		if err != nil {
			return nil, fmt.Errorf("invalid proxy line %q: %w", line, err)
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, p)
	}
	return out, nil
}

// parseImportLine parses one accepted import form into a PoolProxy plus its
// canonical host:port dedupe key. Bare host:port and http(s):// forms are HTTP
// proxies; socks5:// is a SOCKS5 proxy. The stored URL carries the scheme only when
// the line explicitly provided one, so `proxyDialFunc` can default the dialer scheme
// by proxy type without `url.Parse` misreading a bare "host:port" as scheme "host".
func parseImportLine(line string) (PoolProxy, string, error) {
	hasScheme := false
	scheme := "http"
	if idx := strings.Index(line, "://"); idx >= 0 {
		hasScheme = true
		scheme = line[:idx]
		line = line[idx+3:]
		if scheme != "http" && scheme != "https" && scheme != "socks5" {
			return PoolProxy{}, "", fmt.Errorf("unsupported scheme %q", scheme)
		}
	}

	var username, password string
	if at := strings.LastIndex(line, "@"); at >= 0 {
		creds := line[:at]
		line = line[at+1:]
		user, pass, ok := strings.Cut(creds, ":")
		if !ok {
			return PoolProxy{}, "", fmt.Errorf("malformed credentials")
		}
		username = user
		password = pass
	}

	if strings.ContainsAny(line, "/?#") {
		return PoolProxy{}, "", fmt.Errorf("unexpected path in host %q", line)
	}
	hostPort := line
	host, port, err := netSplitHostPort(hostPort)
	if err != nil {
		return PoolProxy{}, "", fmt.Errorf("invalid host:port %q", hostPort)
	}

	typ := HTTPProxy
	if scheme == "socks5" {
		typ = Socks5Proxy
	}

	urlStr := hostPort
	if hasScheme {
		urlStr = scheme + "://" + hostPort
	}

	p := PoolProxy{
		Type:    typ,
		URL:     urlStr,
		Enabled: true,
		Weight:  1,
		Status:  "unknown",
	}
	if username != "" || password != "" {
		p.Username = username
		p.Password = NewSecretVar(password)
	}
	return p, host + ":" + port, nil
}

func netSplitHostPort(hostPort string) (string, string, error) {
	hostPort = strings.TrimPrefix(hostPort, "http://")
	hostPort = strings.TrimPrefix(hostPort, "https://")
	u, err := url.Parse("//" + hostPort)
	if err != nil {
		return "", "", err
	}
	host := u.Hostname()
	if host == "" {
		return "", "", fmt.Errorf("missing host")
	}
	port := u.Port()
	if port == "" {
		return "", "", fmt.Errorf("missing port")
	}
	return host, port, nil
}
