// Package handlers - proxypools.go implements the admin API for per-key proxy
// pools. Pools, their proxies and the key bindings live in the config store;
// this handler is the HTTP surface over that store. The probe is delegated to a
// prober (a core/proxypool.Manager in production) so it can be faked in tests.
package handlers

import (
	"context"
	"fmt"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/proxypool"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// ProxyProber verifies one pool proxy. *proxypool.Manager satisfies it.
type ProxyProber interface {
	Probe(ctx context.Context, p schemas.PoolProxy, mode schemas.ProxyProbeMode) (status string, latencyMs int64, egressIP string, err error)
}

// ProxyPoolStatsSource exposes proxy-pool counters from the running Bifrost
// instance. *bifrost.Bifrost satisfies it; tests can supply a fake.
type ProxyPoolStatsSource interface {
	ProxyPoolStats() *proxypool.Stats
}

// ProxyPoolHandler manages the proxy pool admin routes.
type ProxyPoolHandler struct {
	store       schemas.ProxyPoolStore
	prober      ProxyProber
	statsSource ProxyPoolStatsSource
}

// NewProxyPoolHandler builds a proxy pool handler. A nil prober uses a manager
// backed by the same store with the default echo endpoint; a nil store is
// rejected at request time with a clear error rather than panicking.
func NewProxyPoolHandler(store schemas.ProxyPoolStore, prober ProxyProber) *ProxyPoolHandler {
	if prober == nil {
		prober = proxypool.NewManager(store, nil, nil)
	}
	return &ProxyPoolHandler{store: store, prober: prober}
}

// SetStatsSource attaches the live proxy-pool counters to the list response.
// It is separate from the constructor so embedders that only need CRUD keep
// building the handler with a prober alone.
func (h *ProxyPoolHandler) SetStatsSource(source ProxyPoolStatsSource) {
	h.statsSource = source
}

// RegisterRoutes registers the proxy pool CRUD and subresource routes.
func (h *ProxyPoolHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	wrap := func(handler fasthttp.RequestHandler) fasthttp.RequestHandler {
		return lib.ChainMiddlewares(handler, middlewares...)
	}
	r.GET("/api/proxy-pools", wrap(h.listPools))
	r.POST("/api/proxy-pools", wrap(h.createPool))
	r.GET("/api/proxy-pools/{id}", wrap(h.getPool))
	r.PUT("/api/proxy-pools/{id}", wrap(h.updatePool))
	r.DELETE("/api/proxy-pools/{id}", wrap(h.deletePool))
	r.POST("/api/proxy-pools/{id}/import", wrap(h.importProxies))
	r.POST("/api/proxy-pools/{id}/probe", wrap(h.probeProxies))
	r.POST("/api/proxy-pools/{id}/disable", wrap(h.setProxiesEnabled(false)))
	r.POST("/api/proxy-pools/{id}/delete", wrap(h.deleteProxies))
	r.POST("/api/proxy-pools/{id}/bind", wrap(h.bindKeys))
	r.POST("/api/proxy-pools/{id}/unbind", wrap(h.unbindKeys))
}

func (h *ProxyPoolHandler) storeAvailable(ctx *fasthttp.RequestCtx) bool {
	if h.store == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "proxy pool store is not configured")
		return false
	}
	return true
}

// poolFromCtx loads the pool named by {id} and answers 404 when it is absent.
func (h *ProxyPoolHandler) poolFromCtx(ctx *fasthttp.RequestCtx) *schemas.ProxyPool {
	id, err := getIDFromCtx(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return nil
	}
	pool, err := h.store.GetPool(ctx, id)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to get proxy pool: %v", err))
		return nil
	}
	if pool == nil {
		SendError(ctx, fasthttp.StatusNotFound, "Proxy pool not found")
		return nil
	}
	return pool
}

// proxyPoolMutation is the caller-editable part of a pool. Fields are pointers
// on update so an omitted name cannot accidentally clear the existing one.
type proxyPoolMutation struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
}

func (h *ProxyPoolHandler) listPools(ctx *fasthttp.RequestCtx) {
	if !h.storeAvailable(ctx) {
		return
	}
	pools, err := h.store.ListPools(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to list proxy pools: %v", err))
		return
	}
	response := struct {
		Pools []schemas.ProxyPool `json:"pools"`
		Stats *proxypool.Stats    `json:"stats,omitempty"`
	}{Pools: pools}
	if h.statsSource != nil {
		response.Stats = h.statsSource.ProxyPoolStats()
	}
	SendJSON(ctx, response)
}

func (h *ProxyPoolHandler) createPool(ctx *fasthttp.RequestCtx) {
	if !h.storeAvailable(ctx) {
		return
	}
	var body proxyPoolMutation
	if err := sonic.Unmarshal(ctx.PostBody(), &body); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid request payload")
		return
	}
	if body.Name == nil || strings.TrimSpace(*body.Name) == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "Pool name is required")
		return
	}
	pool := &schemas.ProxyPool{
		ID:   uuid.NewString(),
		Name: strings.TrimSpace(*body.Name),
	}
	if body.Description != nil {
		pool.Description = *body.Description
	}
	if err := h.store.CreatePool(ctx, pool); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to create proxy pool: %v", err))
		return
	}
	SendJSONWithStatus(ctx, pool, fasthttp.StatusCreated)
}

func (h *ProxyPoolHandler) getPool(ctx *fasthttp.RequestCtx) {
	if !h.storeAvailable(ctx) {
		return
	}
	pool := h.poolFromCtx(ctx)
	if pool == nil {
		return
	}
	SendJSON(ctx, pool)
}

func (h *ProxyPoolHandler) updatePool(ctx *fasthttp.RequestCtx) {
	if !h.storeAvailable(ctx) {
		return
	}
	pool := h.poolFromCtx(ctx)
	if pool == nil {
		return
	}
	var body proxyPoolMutation
	if err := sonic.Unmarshal(ctx.PostBody(), &body); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid request payload")
		return
	}
	if body.Name != nil {
		name := strings.TrimSpace(*body.Name)
		if name == "" {
			SendError(ctx, fasthttp.StatusBadRequest, "Pool name must not be empty")
			return
		}
		pool.Name = name
	}
	if body.Description != nil {
		pool.Description = *body.Description
	}
	if err := h.store.UpdatePool(ctx, pool); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to update proxy pool: %v", err))
		return
	}
	SendJSON(ctx, pool)
}

func (h *ProxyPoolHandler) deletePool(ctx *fasthttp.RequestCtx) {
	if !h.storeAvailable(ctx) {
		return
	}
	pool := h.poolFromCtx(ctx)
	if pool == nil {
		return
	}
	if err := h.store.DeletePool(ctx, pool.ID); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to delete proxy pool: %v", err))
		return
	}
	SendJSON(ctx, map[string]any{"status": "success"})
}

// proxyPoolIDsRequest is the body shared by the disable, delete and probe
// subresources. An empty or nil list means "all proxies in the pool" for probe
// and is rejected for the mutating endpoints.
type proxyPoolIDsRequest struct {
	IDs []string `json:"ids"`
}

// validProxyMechanisms mirrors the six mechanisms the selection manager knows.
// Rejecting an unknown value here keeps a typo from silently becoming a no-op.
var validProxyMechanisms = map[schemas.ProxyMechanism]bool{
	schemas.ProxyRoundRobin:       true,
	schemas.ProxyRandom:           true,
	schemas.ProxyWeighted:         true,
	schemas.ProxyLeastConnections: true,
	schemas.ProxyFallback:         true,
	schemas.ProxyStickySession:    true,
}

func (h *ProxyPoolHandler) importProxies(ctx *fasthttp.RequestCtx) {
	if !h.storeAvailable(ctx) {
		return
	}
	pool := h.poolFromCtx(ctx)
	if pool == nil {
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if err := sonic.Unmarshal(ctx.PostBody(), &body); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid request payload")
		return
	}
	proxies, err := schemas.ParseImport(body.Text)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid proxy import: %v", err))
		return
	}
	if len(proxies) == 0 {
		SendError(ctx, fasthttp.StatusBadRequest, "No proxies found in import text")
		return
	}
	if err := h.store.ImportProxies(ctx, pool.ID, proxies); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to import proxies: %v", err))
		return
	}
	SendJSON(ctx, map[string]any{"imported": len(proxies)})
}

// proxyProbeResult is one proxy's probe outcome. A failed probe is reported as
// dead with its error rather than failing the whole batch.
type proxyProbeResult struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	LatencyMs int64  `json:"latency_ms"`
	EgressIP  string `json:"egress_ip,omitempty"`
	Error     string `json:"error,omitempty"`
}

func (h *ProxyPoolHandler) probeProxies(ctx *fasthttp.RequestCtx) {
	if !h.storeAvailable(ctx) {
		return
	}
	pool := h.poolFromCtx(ctx)
	if pool == nil {
		return
	}
	var body struct {
		IDs  []string               `json:"ids"`
		Mode schemas.ProxyProbeMode `json:"mode"`
	}
	if err := sonic.Unmarshal(ctx.PostBody(), &body); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid request payload")
		return
	}
	mode := body.Mode
	if mode == "" {
		mode = schemas.ProxyProbeModeA
	}
	if mode != schemas.ProxyProbeModeA && mode != schemas.ProxyProbeModeB {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid probe mode %q", mode))
		return
	}
	targets, ok := h.selectProxies(ctx, pool, body.IDs, true)
	if !ok {
		return
	}
	results := make([]proxyProbeResult, 0, len(targets))
	for _, proxy := range targets {
		status, latencyMs, egressIP, err := h.prober.Probe(ctx, proxy, mode)
		if err != nil {
			status = proxypool.ProxyStatusDead
		}
		if err := h.store.SetProxyStatus(ctx, proxy.ID, status, latencyMs, egressIP); err != nil {
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to record probe status: %v", err))
			return
		}
		result := proxyProbeResult{ID: proxy.ID, Status: status, LatencyMs: latencyMs, EgressIP: egressIP}
		if err != nil {
			result.Error = err.Error()
		}
		results = append(results, result)
	}
	SendJSON(ctx, struct {
		Results []proxyProbeResult `json:"results"`
	}{Results: results})
}

// selectProxies resolves an optional id list against a pool. Unknown ids are a
// 400: a typo must never be silently skipped. When allowAll is false an empty
// list is also a 400 because disable/delete need an explicit target.
func (h *ProxyPoolHandler) selectProxies(ctx *fasthttp.RequestCtx, pool *schemas.ProxyPool, ids []string, allowAll bool) ([]schemas.PoolProxy, bool) {
	if len(ids) == 0 {
		if allowAll {
			return pool.Proxies, true
		}
		SendError(ctx, fasthttp.StatusBadRequest, "At least one proxy id is required")
		return nil, false
	}
	byID := make(map[string]schemas.PoolProxy, len(pool.Proxies))
	for _, proxy := range pool.Proxies {
		byID[proxy.ID] = proxy
	}
	out := make([]schemas.PoolProxy, 0, len(ids))
	for _, id := range ids {
		proxy, ok := byID[id]
		if !ok {
			SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Proxy %q is not in pool %q", id, pool.ID))
			return nil, false
		}
		out = append(out, proxy)
	}
	return out, true
}

func (h *ProxyPoolHandler) setProxiesEnabled(enabled bool) fasthttp.RequestHandler {
	return func(ctx *fasthttp.RequestCtx) {
		if !h.storeAvailable(ctx) {
			return
		}
		pool := h.poolFromCtx(ctx)
		if pool == nil {
			return
		}
		var body proxyPoolIDsRequest
		if err := sonic.Unmarshal(ctx.PostBody(), &body); err != nil {
			SendError(ctx, fasthttp.StatusBadRequest, "Invalid request payload")
			return
		}
		ids, ok := h.selectProxies(ctx, pool, body.IDs, false)
		if !ok {
			return
		}
		proxyIDs := make([]string, 0, len(ids))
		for _, proxy := range ids {
			proxyIDs = append(proxyIDs, proxy.ID)
		}
		if err := h.store.SetProxiesEnabled(ctx, pool.ID, proxyIDs, enabled); err != nil {
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to update proxies: %v", err))
			return
		}
		SendJSON(ctx, map[string]any{"updated": len(proxyIDs)})
	}
}

func (h *ProxyPoolHandler) deleteProxies(ctx *fasthttp.RequestCtx) {
	if !h.storeAvailable(ctx) {
		return
	}
	pool := h.poolFromCtx(ctx)
	if pool == nil {
		return
	}
	var body proxyPoolIDsRequest
	if err := sonic.Unmarshal(ctx.PostBody(), &body); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid request payload")
		return
	}
	ids, ok := h.selectProxies(ctx, pool, body.IDs, false)
	if !ok {
		return
	}
	proxyIDs := make([]string, 0, len(ids))
	for _, proxy := range ids {
		proxyIDs = append(proxyIDs, proxy.ID)
	}
	if err := h.store.DeleteProxies(ctx, pool.ID, proxyIDs); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to delete proxies: %v", err))
		return
	}
	SendJSON(ctx, map[string]any{"deleted": len(proxyIDs)})
}

// proxyPoolKeysRequest is the body of the bind and unbind subresources.
type proxyPoolKeysRequest struct {
	KeyIDs    []string               `json:"key_ids"`
	Mechanism schemas.ProxyMechanism `json:"mechanism"`
}

func (h *ProxyPoolHandler) readKeysRequest(ctx *fasthttp.RequestCtx) (proxyPoolKeysRequest, bool) {
	var body proxyPoolKeysRequest
	if err := sonic.Unmarshal(ctx.PostBody(), &body); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid request payload")
		return body, false
	}
	if len(body.KeyIDs) == 0 {
		SendError(ctx, fasthttp.StatusBadRequest, "At least one key id is required")
		return body, false
	}
	return body, true
}

func (h *ProxyPoolHandler) bindKeys(ctx *fasthttp.RequestCtx) {
	if !h.storeAvailable(ctx) {
		return
	}
	pool := h.poolFromCtx(ctx)
	if pool == nil {
		return
	}
	body, ok := h.readKeysRequest(ctx)
	if !ok {
		return
	}
	if !validProxyMechanisms[body.Mechanism] {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid proxy mechanism %q", body.Mechanism))
		return
	}
	if err := h.store.BindKeys(ctx, pool.ID, body.KeyIDs, body.Mechanism); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to bind keys: %v", err))
		return
	}
	SendJSON(ctx, map[string]any{"bound": len(body.KeyIDs)})
}

func (h *ProxyPoolHandler) unbindKeys(ctx *fasthttp.RequestCtx) {
	if !h.storeAvailable(ctx) {
		return
	}
	pool := h.poolFromCtx(ctx)
	if pool == nil {
		return
	}
	body, ok := h.readKeysRequest(ctx)
	if !ok {
		return
	}
	if err := h.store.UnbindKeys(ctx, pool.ID, body.KeyIDs); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to unbind keys: %v", err))
		return
	}
	SendJSON(ctx, map[string]any{"unbound": len(body.KeyIDs)})
}
