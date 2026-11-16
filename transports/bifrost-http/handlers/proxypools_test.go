package handlers

import (
	"context"
	"fmt"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// proxyPoolMemStore is an in-memory ProxyPoolStore used to assert the handler's
// write-through behaviour and the status code of every failure path.
type proxyPoolMemStore struct {
	pools    map[string]*schemas.ProxyPool
	bindings map[string]*schemas.ProxyPoolBinding
	statuses map[string]proxyPoolProbeResult
	nextID   int
	err      error
}

type proxyPoolProbeResult struct {
	status string
	ms     int64
	egress string
}

func newProxyPoolMemStore() *proxyPoolMemStore {
	return &proxyPoolMemStore{
		pools:    make(map[string]*schemas.ProxyPool),
		bindings: make(map[string]*schemas.ProxyPoolBinding),
		statuses: make(map[string]proxyPoolProbeResult),
	}
}

func (s *proxyPoolMemStore) id(prefix string) string {
	s.nextID++
	return fmt.Sprintf("%s-%d", prefix, s.nextID)
}

func clonePool(p *schemas.ProxyPool) *schemas.ProxyPool {
	if p == nil {
		return nil
	}
	out := *p
	out.Proxies = append([]schemas.PoolProxy(nil), p.Proxies...)
	return &out
}

func (s *proxyPoolMemStore) ListPools(context.Context) ([]schemas.ProxyPool, error) {
	if s.err != nil {
		return nil, s.err
	}
	out := make([]schemas.ProxyPool, 0, len(s.pools))
	for _, p := range s.pools {
		out = append(out, *clonePool(p))
	}
	return out, nil
}

func (s *proxyPoolMemStore) GetPool(_ context.Context, id string) (*schemas.ProxyPool, error) {
	if s.err != nil {
		return nil, s.err
	}
	return clonePool(s.pools[id]), nil
}

func (s *proxyPoolMemStore) CreatePool(_ context.Context, p *schemas.ProxyPool) error {
	if s.err != nil {
		return s.err
	}
	if p.ID == "" {
		p.ID = s.id("pool")
	}
	s.pools[p.ID] = clonePool(p)
	return nil
}

func (s *proxyPoolMemStore) UpdatePool(_ context.Context, p *schemas.ProxyPool) error {
	if s.err != nil {
		return s.err
	}
	if _, ok := s.pools[p.ID]; !ok {
		return fmt.Errorf("pool %q not found", p.ID)
	}
	s.pools[p.ID] = clonePool(p)
	return nil
}

func (s *proxyPoolMemStore) DeletePool(_ context.Context, id string) error {
	if s.err != nil {
		return s.err
	}
	delete(s.pools, id)
	for keyID, b := range s.bindings {
		if b.PoolID == id {
			delete(s.bindings, keyID)
		}
	}
	return nil
}

func (s *proxyPoolMemStore) ImportProxies(_ context.Context, poolID string, proxies []schemas.PoolProxy) error {
	if s.err != nil {
		return s.err
	}
	pool := s.pools[poolID]
	if pool == nil {
		return fmt.Errorf("pool %q not found", poolID)
	}
	for _, p := range proxies {
		p.ID = s.id("proxy")
		p.PoolID = poolID
		pool.Proxies = append(pool.Proxies, p)
	}
	return nil
}

func (s *proxyPoolMemStore) DeleteProxies(_ context.Context, poolID string, ids []string) error {
	if s.err != nil {
		return s.err
	}
	pool := s.pools[poolID]
	if pool == nil {
		return fmt.Errorf("pool %q not found", poolID)
	}
	remove := make(map[string]bool, len(ids))
	for _, id := range ids {
		remove[id] = true
	}
	kept := pool.Proxies[:0]
	for _, p := range pool.Proxies {
		if !remove[p.ID] {
			kept = append(kept, p)
		}
	}
	pool.Proxies = kept
	return nil
}

func (s *proxyPoolMemStore) SetProxiesEnabled(_ context.Context, poolID string, ids []string, enabled bool) error {
	if s.err != nil {
		return s.err
	}
	pool := s.pools[poolID]
	if pool == nil {
		return fmt.Errorf("pool %q not found", poolID)
	}
	target := make(map[string]bool, len(ids))
	for _, id := range ids {
		target[id] = true
	}
	for i := range pool.Proxies {
		if target[pool.Proxies[i].ID] {
			pool.Proxies[i].Enabled = enabled
		}
	}
	return nil
}

func (s *proxyPoolMemStore) SetProxyStatus(_ context.Context, id string, status string, ms int64, egress string) error {
	if s.err != nil {
		return s.err
	}
	s.statuses[id] = proxyPoolProbeResult{status: status, ms: ms, egress: egress}
	return nil
}

func (s *proxyPoolMemStore) BindKeys(_ context.Context, poolID string, keyIDs []string, mechanism schemas.ProxyMechanism) error {
	if s.err != nil {
		return s.err
	}
	for _, keyID := range keyIDs {
		s.bindings[keyID] = &schemas.ProxyPoolBinding{PoolID: poolID, KeyID: keyID, Mechanism: mechanism}
	}
	return nil
}

func (s *proxyPoolMemStore) UnbindKeys(_ context.Context, poolID string, keyIDs []string) error {
	if s.err != nil {
		return s.err
	}
	for _, keyID := range keyIDs {
		if b := s.bindings[keyID]; b != nil && b.PoolID == poolID {
			delete(s.bindings, keyID)
		}
	}
	return nil
}

func (s *proxyPoolMemStore) ListBindings(context.Context) ([]schemas.ProxyPoolBinding, error) {
	out := make([]schemas.ProxyPoolBinding, 0, len(s.bindings))
	for _, b := range s.bindings {
		out = append(out, *b)
	}
	return out, nil
}

func (s *proxyPoolMemStore) GetBinding(_ context.Context, keyID string) (*schemas.ProxyPoolBinding, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.bindings[keyID], nil
}

func (s *proxyPoolMemStore) DeleteKeyBindings(_ context.Context, keyID string) error {
	delete(s.bindings, keyID)
	return nil
}

// countingProber records probe calls without doing network I/O.
type countingProber struct {
	calls    int
	lastIDs  []string
	lastMode schemas.ProxyProbeMode
	status   string
	ms       int64
	egress   string
	err      error
	results  map[string]proxyPoolProbeResult
	errs     map[string]error
}

func (p *countingProber) Probe(_ context.Context, proxy schemas.PoolProxy, mode schemas.ProxyProbeMode) (string, int64, string, error) {
	p.calls++
	p.lastIDs = append(p.lastIDs, proxy.ID)
	p.lastMode = mode
	if err := p.errs[proxy.ID]; err != nil {
		return "dead", 0, "", err
	}
	if r, ok := p.results[proxy.ID]; ok {
		return r.status, r.ms, r.egress, nil
	}
	if p.err != nil {
		return "dead", 0, "", p.err
	}
	return p.status, p.ms, p.egress, nil
}

func seedProxyPool(t *testing.T, store *proxyPoolMemStore, proxies ...schemas.PoolProxy) string {
	t.Helper()
	pool := &schemas.ProxyPool{ID: "pool-1", Name: "seeded", Proxies: append([]schemas.PoolProxy(nil), proxies...)}
	require.NoError(t, store.CreatePool(context.Background(), pool))
	return pool.ID
}

func testPoolProxy(id, url string) schemas.PoolProxy {
	return schemas.PoolProxy{ID: id, Type: schemas.HTTPProxy, URL: url, Enabled: true, Weight: 1, Status: "unknown"}
}

func newProxyPoolTestRouter(store schemas.ProxyPoolStore, prober ProxyProber) *router.Router {
	r := router.New()
	NewProxyPoolHandler(store, prober).RegisterRoutes(r)
	return r
}

func proxyPoolRequest(r *router.Router, method, uri, body string) *fasthttp.RequestCtx {
	ctx := &fasthttp.RequestCtx{}
	ctx.Init(&fasthttp.Request{}, nil, nil)
	ctx.Request.Header.SetMethod(method)
	ctx.Request.SetRequestURI(uri)
	if body != "" {
		ctx.Request.Header.SetContentType("application/json")
		ctx.Request.SetBodyString(body)
	}
	r.Handler(ctx)
	return ctx
}

func requireStatus(t *testing.T, ctx *fasthttp.RequestCtx, want int) {
	t.Helper()
	require.Equal(t, want, ctx.Response.StatusCode(), "body: %s", ctx.Response.Body())
}

func TestProxyPools_CRUD(t *testing.T) {
	store := newProxyPoolMemStore()
	r := newProxyPoolTestRouter(store, nil)

	ctx := proxyPoolRequest(r, fasthttp.MethodPost, "/api/proxy-pools", `{"name":"clean-ips","description":"egress pool"}`)
	requireStatus(t, ctx, fasthttp.StatusCreated)
	created := struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
	}{}
	require.NoError(t, sonic.Unmarshal(ctx.Response.Body(), &created))
	require.NotEmpty(t, created.ID)
	require.Equal(t, "clean-ips", created.Name)
	require.NotNil(t, store.pools[created.ID], "create must write through to the store")

	ctx = proxyPoolRequest(r, fasthttp.MethodGet, "/api/proxy-pools", "")
	requireStatus(t, ctx, fasthttp.StatusOK)
	var listed struct {
		Pools []schemas.ProxyPool `json:"pools"`
	}
	require.NoError(t, sonic.Unmarshal(ctx.Response.Body(), &listed))
	require.Len(t, listed.Pools, 1)
	require.Equal(t, created.ID, listed.Pools[0].ID)

	ctx = proxyPoolRequest(r, fasthttp.MethodGet, "/api/proxy-pools/"+created.ID, "")
	requireStatus(t, ctx, fasthttp.StatusOK)
	require.Contains(t, string(ctx.Response.Body()), `"clean-ips"`)

	ctx = proxyPoolRequest(r, fasthttp.MethodPut, "/api/proxy-pools/"+created.ID, `{"name":"renamed","description":"updated"}`)
	requireStatus(t, ctx, fasthttp.StatusOK)
	require.Equal(t, "renamed", store.pools[created.ID].Name)

	ctx = proxyPoolRequest(r, fasthttp.MethodDelete, "/api/proxy-pools/"+created.ID, "")
	requireStatus(t, ctx, fasthttp.StatusOK)
	require.Nil(t, store.pools[created.ID], "delete must remove the pool from the store")

	ctx = proxyPoolRequest(r, fasthttp.MethodGet, "/api/proxy-pools/"+created.ID, "")
	requireStatus(t, ctx, fasthttp.StatusNotFound)
}

func TestProxyPools_RejectsBadRequests(t *testing.T) {
	store := newProxyPoolMemStore()
	r := newProxyPoolTestRouter(store, nil)

	requireStatus(t, proxyPoolRequest(r, fasthttp.MethodPost, "/api/proxy-pools", `{not-json`), fasthttp.StatusBadRequest)
	requireStatus(t, proxyPoolRequest(r, fasthttp.MethodPost, "/api/proxy-pools", `{"description":"no name"}`), fasthttp.StatusBadRequest)
	requireStatus(t, proxyPoolRequest(r, fasthttp.MethodGet, "/api/proxy-pools/missing", ""), fasthttp.StatusNotFound)
	requireStatus(t, proxyPoolRequest(r, fasthttp.MethodPut, "/api/proxy-pools/missing", `{"name":"x"}`), fasthttp.StatusNotFound)
	requireStatus(t, proxyPoolRequest(r, fasthttp.MethodDelete, "/api/proxy-pools/missing", ""), fasthttp.StatusNotFound)
	require.Empty(t, store.pools, "invalid requests must not write through")
}

func TestProxyPools_Import(t *testing.T) {
	store := newProxyPoolMemStore()
	id := seedProxyPool(t, store)
	r := newProxyPoolTestRouter(store, nil)

	text := "1.2.3.4:8080\nhttp://5.6.7.8:8080\nsocks5://u:p@9.10.11.12:1080\nu2:p2@13.14.15.16:8080\n1.2.3.4:8080\n# comment\n\n"
	body, err := sonic.Marshal(map[string]string{"text": text})
	require.NoError(t, err)
	ctx := proxyPoolRequest(r, fasthttp.MethodPost, "/api/proxy-pools/"+id+"/import", string(body))
	requireStatus(t, ctx, fasthttp.StatusOK)
	require.Contains(t, string(ctx.Response.Body()), `"imported":4`, "duplicates and comments must be skipped")
	require.Len(t, store.pools[id].Proxies, 4, "import must write through to the store")
	require.Equal(t, schemas.Socks5Proxy, store.pools[id].Proxies[2].Type)
	require.Equal(t, "u", store.pools[id].Proxies[2].Username)
	require.NotNil(t, store.pools[id].Proxies[2].Password)
	require.Equal(t, "p", store.pools[id].Proxies[2].Password.GetValue())
	require.NotContains(t, string(ctx.Response.Body()), "password", "proxy passwords must never be returned")

	bad, err := sonic.Marshal(map[string]string{"text": "not a proxy line"})
	require.NoError(t, err)
	requireStatus(t, proxyPoolRequest(r, fasthttp.MethodPost, "/api/proxy-pools/"+id+"/import", string(bad)), fasthttp.StatusBadRequest)
	requireStatus(t, proxyPoolRequest(r, fasthttp.MethodPost, "/api/proxy-pools/missing/import", `{"text":"1.2.3.4:80"}`), fasthttp.StatusNotFound)
	require.Len(t, store.pools[id].Proxies, 4, "failed imports must not write through")
}

func TestProxyPools_Probe(t *testing.T) {
	store := newProxyPoolMemStore()
	id := seedProxyPool(t, store, testPoolProxy("px-1", "1.1.1.1:8080"), testPoolProxy("px-2", "2.2.2.2:8080"))
	prober := &countingProber{
		results: map[string]proxyPoolProbeResult{
			"px-1": {status: "alive", ms: 12, egress: "9.9.9.9"},
		},
		errs: map[string]error{
			"px-2": fmt.Errorf("connect refused"),
		},
	}
	r := newProxyPoolTestRouter(store, prober)

	ctx := proxyPoolRequest(r, fasthttp.MethodPost, "/api/proxy-pools/"+id+"/probe", `{"ids":["px-1","px-2"],"mode":"b"}`)
	requireStatus(t, ctx, fasthttp.StatusOK)
	var probeResp struct {
		Results []struct {
			ID        string `json:"id"`
			Status    string `json:"status"`
			LatencyMs int64  `json:"latency_ms"`
			EgressIP  string `json:"egress_ip"`
			Error     string `json:"error,omitempty"`
		} `json:"results"`
	}
	require.NoError(t, sonic.Unmarshal(ctx.Response.Body(), &probeResp))
	require.Len(t, probeResp.Results, 2)
	require.Equal(t, "px-1", probeResp.Results[0].ID)
	require.Equal(t, "alive", probeResp.Results[0].Status)
	require.Equal(t, int64(12), probeResp.Results[0].LatencyMs)
	require.Equal(t, "9.9.9.9", probeResp.Results[0].EgressIP)
	require.Equal(t, "dead", probeResp.Results[1].Status)
	require.Contains(t, probeResp.Results[1].Error, "connect refused")
	require.Equal(t, 2, prober.calls)
	require.Equal(t, schemas.ProxyProbeModeB, prober.lastMode)
	require.Equal(t, "alive", store.statuses["px-1"].status)
	require.Equal(t, "dead", store.statuses["px-2"].status)

	// A missing ids array means "all"; a missing mode defaults to mode A.
	prober.calls = 0
	ctx = proxyPoolRequest(r, fasthttp.MethodPost, "/api/proxy-pools/"+id+"/probe", `{}`)
	requireStatus(t, ctx, fasthttp.StatusOK)
	require.Equal(t, 2, prober.calls)
	require.Equal(t, schemas.ProxyProbeModeA, prober.lastMode)

	requireStatus(t, proxyPoolRequest(r, fasthttp.MethodPost, "/api/proxy-pools/"+id+"/probe", `{"mode":"c"}`), fasthttp.StatusBadRequest)
	requireStatus(t, proxyPoolRequest(r, fasthttp.MethodPost, "/api/proxy-pools/"+id+"/probe", `{"ids":["nope"]}`), fasthttp.StatusBadRequest)
	requireStatus(t, proxyPoolRequest(r, fasthttp.MethodPost, "/api/proxy-pools/missing/probe", `{}`), fasthttp.StatusNotFound)
}

func TestProxyPools_DisableAndDeleteProxies(t *testing.T) {
	store := newProxyPoolMemStore()
	id := seedProxyPool(t, store, testPoolProxy("px-1", "1.1.1.1:8080"), testPoolProxy("px-2", "2.2.2.2:8080"))
	r := newProxyPoolTestRouter(store, nil)

	ctx := proxyPoolRequest(r, fasthttp.MethodPost, "/api/proxy-pools/"+id+"/disable", `{"ids":["px-1"]}`)
	requireStatus(t, ctx, fasthttp.StatusOK)
	require.False(t, store.pools[id].Proxies[0].Enabled)
	require.True(t, store.pools[id].Proxies[1].Enabled)

	ctx = proxyPoolRequest(r, fasthttp.MethodPost, "/api/proxy-pools/"+id+"/delete", `{"ids":["px-2"]}`)
	requireStatus(t, ctx, fasthttp.StatusOK)
	require.Len(t, store.pools[id].Proxies, 1)
	require.Equal(t, "px-1", store.pools[id].Proxies[0].ID)

	requireStatus(t, proxyPoolRequest(r, fasthttp.MethodPost, "/api/proxy-pools/"+id+"/disable", `{}`), fasthttp.StatusBadRequest)
	requireStatus(t, proxyPoolRequest(r, fasthttp.MethodPost, "/api/proxy-pools/"+id+"/delete", `{"ids":["nope"]}`), fasthttp.StatusBadRequest)
	requireStatus(t, proxyPoolRequest(r, fasthttp.MethodPost, "/api/proxy-pools/missing/disable", `{"ids":["px-1"]}`), fasthttp.StatusNotFound)
}

func TestProxyPools_BindUnbind(t *testing.T) {
	store := newProxyPoolMemStore()
	id := seedProxyPool(t, store)
	r := newProxyPoolTestRouter(store, nil)

	ctx := proxyPoolRequest(r, fasthttp.MethodPost, "/api/proxy-pools/"+id+"/bind", `{"key_ids":["k1","k2"],"mechanism":"weighted"}`)
	requireStatus(t, ctx, fasthttp.StatusOK)
	require.Equal(t, &schemas.ProxyPoolBinding{PoolID: id, KeyID: "k1", Mechanism: schemas.ProxyWeighted}, store.bindings["k1"])
	require.Equal(t, &schemas.ProxyPoolBinding{PoolID: id, KeyID: "k2", Mechanism: schemas.ProxyWeighted}, store.bindings["k2"])

	ctx = proxyPoolRequest(r, fasthttp.MethodPost, "/api/proxy-pools/"+id+"/unbind", `{"key_ids":["k1"]}`)
	requireStatus(t, ctx, fasthttp.StatusOK)
	require.Nil(t, store.bindings["k1"])
	require.NotNil(t, store.bindings["k2"])

	requireStatus(t, proxyPoolRequest(r, fasthttp.MethodPost, "/api/proxy-pools/"+id+"/bind", `{"key_ids":["k3"],"mechanism":"bogus"}`), fasthttp.StatusBadRequest)
	requireStatus(t, proxyPoolRequest(r, fasthttp.MethodPost, "/api/proxy-pools/"+id+"/bind", `{"key_ids":[]}`), fasthttp.StatusBadRequest)
	requireStatus(t, proxyPoolRequest(r, fasthttp.MethodPost, "/api/proxy-pools/missing/bind", `{"key_ids":["k1"],"mechanism":"random"}`), fasthttp.StatusNotFound)
	require.Nil(t, store.bindings["k3"])
}
