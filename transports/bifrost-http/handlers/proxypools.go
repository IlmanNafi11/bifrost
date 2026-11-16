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

// ProxyPoolHandler manages the proxy pool admin routes.
type ProxyPoolHandler struct {
	store  schemas.ProxyPoolStore
	prober ProxyProber
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
	SendJSON(ctx, struct {
		Pools []schemas.ProxyPool `json:"pools"`
	}{Pools: pools})
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
