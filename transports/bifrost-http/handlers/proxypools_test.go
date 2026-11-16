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
}

func (p *countingProber) Probe(_ context.Context, proxy schemas.PoolProxy, mode schemas.ProxyProbeMode) (string, int64, string, error) {
	p.calls++
	p.lastIDs = append(p.lastIDs, proxy.ID)
	p.lastMode = mode
	if p.err != nil {
		return "dead", 0, "", p.err
	}
	return p.status, p.ms, p.egress, nil
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
