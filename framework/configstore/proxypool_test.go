package configstore

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupProxyPoolTestStore extends the base test store with the proxy pool tables
// through the same migration a live store runs.
func setupProxyPoolTestStore(t *testing.T) *RDBConfigStore {
	t.Helper()
	store := setupRDBTestStore(t)
	require.NoError(t, migrationAddProxyPoolTables(context.Background(), store.DB(), testMigrationLogger))
	return store
}

// seedProxyPoolProvider creates a provider (idempotently by name) so keys can be
// created under it.
func seedProxyPoolProvider(t *testing.T, store *RDBConfigStore, provider string) {
	t.Helper()
	if _, err := store.GetProviderConfig(context.Background(), schemas.ModelProvider(provider)); err == nil {
		return
	}
	require.NoError(t, store.AddProvider(context.Background(), schemas.ModelProvider(provider), ProviderConfig{}))
}

// seedProxyPoolProviderKey creates one upstream key under an existing provider.
func seedProxyPoolProviderKey(t *testing.T, store *RDBConfigStore, provider, keyID string) {
	t.Helper()
	seedProxyPoolProvider(t, store, provider)
	require.NoError(t, store.CreateProviderKey(context.Background(), schemas.ModelProvider(provider), schemas.Key{
		ID:     keyID,
		Name:   keyID + "-name",
		Value:  *schemas.NewSecretVar("sk-" + keyID),
		Weight: 1,
	}))
}

func TestProxyPoolCRUDAndImport(t *testing.T) {
	store := setupProxyPoolTestStore(t)
	ctx := context.Background()

	pool := &schemas.ProxyPool{ID: "pool-1", Name: "us-egress", Description: "clean US IPs"}
	require.NoError(t, store.CreatePool(ctx, pool))

	got, err := store.GetPool(ctx, "pool-1")
	require.NoError(t, err)
	assert.Equal(t, "us-egress", got.Name)
	assert.Equal(t, "clean US IPs", got.Description)
	assert.Empty(t, got.Proxies)

	proxies := []schemas.PoolProxy{
		{ID: "proxy-1", PoolID: "pool-1", Type: schemas.HTTPProxy, URL: "10.0.0.1:8080", Enabled: true, Status: "unknown", Weight: 1},
		{ID: "proxy-2", PoolID: "pool-1", Type: schemas.Socks5Proxy, URL: "socks5://10.0.0.2:1080", Username: "u", Password: schemas.NewSecretVar("sekrit"), Enabled: true, Status: "unknown", Weight: 2},
	}
	require.NoError(t, store.ImportProxies(ctx, "pool-1", proxies))

	got, err = store.GetPool(ctx, "pool-1")
	require.NoError(t, err)
	require.Len(t, got.Proxies, 2)

	// The socks5 password round-trips internally but never serializes.
	b, err := json.Marshal(got)
	require.NoError(t, err)
	assert.NotContains(t, string(b), "\"password\"", "password must be json-hidden")
	assert.NotContains(t, string(b), "sekrit", "password value must not serialize")

	// Importing the same proxy URL again is idempotent (dedupe by URL).
	require.NoError(t, store.ImportProxies(ctx, "pool-1", []schemas.PoolProxy{
		{ID: "proxy-1-dup", PoolID: "pool-1", Type: schemas.HTTPProxy, URL: "10.0.0.1:8080", Enabled: true, Status: "unknown", Weight: 1},
	}))
	got, err = store.GetPool(ctx, "pool-1")
	require.NoError(t, err)
	assert.Len(t, got.Proxies, 2, "import dedupes by URL")

	pool.Name = "us-egress-v2"
	pool.Description = "rotated"
	require.NoError(t, store.UpdatePool(ctx, pool))
	got, err = store.GetPool(ctx, "pool-1")
	require.NoError(t, err)
	assert.Equal(t, "us-egress-v2", got.Name)
	assert.Equal(t, "rotated", got.Description)

	pools, err := store.ListPools(ctx)
	require.NoError(t, err)
	assert.Len(t, pools, 1)
	assert.Equal(t, "us-egress-v2", pools[0].Name)

	require.NoError(t, store.DeleteProxies(ctx, "pool-1", []string{"proxy-1"}))
	got, err = store.GetPool(ctx, "pool-1")
	require.NoError(t, err)
	require.Len(t, got.Proxies, 1)
	assert.Equal(t, "proxy-2", got.Proxies[0].ID)
}

func TestDeletePoolCascadesBindings(t *testing.T) {
	store := setupProxyPoolTestStore(t)
	ctx := context.Background()

	require.NoError(t, store.CreatePool(ctx, &schemas.ProxyPool{ID: "pool-1", Name: "us-egress"}))
	require.NoError(t, store.ImportProxies(ctx, "pool-1", []schemas.PoolProxy{
		{ID: "proxy-1", PoolID: "pool-1", Type: schemas.HTTPProxy, URL: "10.0.0.1:8080", Enabled: true, Status: "unknown", Weight: 1},
	}))
	seedProxyPoolProviderKey(t, store, "openai", "key-1")
	require.NoError(t, store.BindKeys(ctx, "pool-1", []string{"key-1"}, schemas.ProxyRoundRobin))

	require.NoError(t, store.DeletePool(ctx, "pool-1"))

	_, err := store.GetPool(ctx, "pool-1")
	require.ErrorIs(t, err, ErrNotFound)

	bindings, err := store.ListBindings(ctx)
	require.NoError(t, err)
	assert.Empty(t, bindings, "pool delete must cascade its bindings")
}

func TestDeleteKeyCascadesBindings(t *testing.T) {
	store := setupProxyPoolTestStore(t)
	ctx := context.Background()

	require.NoError(t, store.CreatePool(ctx, &schemas.ProxyPool{ID: "pool-1", Name: "us-egress"}))
	seedProxyPoolProviderKey(t, store, "openai", "key-1")
	require.NoError(t, store.BindKeys(ctx, "pool-1", []string{"key-1"}, schemas.ProxyRoundRobin))

	binding, err := store.GetBinding(ctx, "key-1")
	require.NoError(t, err)
	assert.Equal(t, "pool-1", binding.PoolID)

	require.NoError(t, store.DeleteProviderKey(ctx, "openai", "key-1"))

	binding, err = store.GetBinding(ctx, "key-1")
	require.NoError(t, err)
	assert.Nil(t, binding, "key delete must cascade its bindings")
}

func TestSetProxiesEnabledDisables(t *testing.T) {
	store := setupProxyPoolTestStore(t)
	ctx := context.Background()

	require.NoError(t, store.CreatePool(ctx, &schemas.ProxyPool{ID: "pool-1", Name: "us-egress"}))
	require.NoError(t, store.ImportProxies(ctx, "pool-1", []schemas.PoolProxy{
		{ID: "proxy-1", PoolID: "pool-1", Type: schemas.HTTPProxy, URL: "10.0.0.1:8080", Enabled: true, Status: "unknown", Weight: 1},
		{ID: "proxy-2", PoolID: "pool-1", Type: schemas.HTTPProxy, URL: "10.0.0.2:8080", Enabled: true, Status: "unknown", Weight: 1},
	}))

	require.NoError(t, store.SetProxiesEnabled(ctx, "pool-1", []string{"proxy-1"}, false))

	got, err := store.GetPool(ctx, "pool-1")
	require.NoError(t, err)
	byID := map[string]schemas.PoolProxy{}
	for _, p := range got.Proxies {
		byID[p.ID] = p
	}
	assert.False(t, byID["proxy-1"].Enabled, "disabled proxy must be inactive")
	assert.True(t, byID["proxy-2"].Enabled, "untouched proxy stays active")

	// Re-enable flips it back.
	require.NoError(t, store.SetProxiesEnabled(ctx, "pool-1", []string{"proxy-1"}, true))
	got, err = store.GetPool(ctx, "pool-1")
	require.NoError(t, err)
	for _, p := range got.Proxies {
		if p.ID == "proxy-1" {
			assert.True(t, p.Enabled)
		}
	}
}

func TestDeleteProviderCascadesBindings(t *testing.T) {
	store := setupProxyPoolTestStore(t)
	ctx := context.Background()

	require.NoError(t, store.CreatePool(ctx, &schemas.ProxyPool{ID: "pool-1", Name: "us-egress"}))
	seedProxyPoolProviderKey(t, store, "openai", "key-1")
	require.NoError(t, store.BindKeys(ctx, "pool-1", []string{"key-1"}, schemas.ProxyRoundRobin))

	require.NoError(t, store.DeleteProvider(ctx, "openai"))

	binding, err := store.GetBinding(ctx, "key-1")
	require.NoError(t, err)
	assert.Nil(t, binding, "provider delete must cascade its keys' bindings")
}

func TestGetBindingByKeyID(t *testing.T) {
	store := setupProxyPoolTestStore(t)
	ctx := context.Background()

	require.NoError(t, store.CreatePool(ctx, &schemas.ProxyPool{ID: "pool-1", Name: "us-egress"}))
	seedProxyPoolProviderKey(t, store, "openai", "key-1")
	seedProxyPoolProviderKey(t, store, "openai", "key-2")

	require.NoError(t, store.BindKeys(ctx, "pool-1", []string{"key-1", "key-2"}, schemas.ProxyStickySession))

	binding, err := store.GetBinding(ctx, "key-1")
	require.NoError(t, err)
	assert.Equal(t, "pool-1", binding.PoolID)
	assert.Equal(t, "key-1", binding.KeyID)
	assert.Equal(t, schemas.ProxyStickySession, binding.Mechanism)

	// Re-binding is an upsert, not a duplicate.
	require.NoError(t, store.BindKeys(ctx, "pool-1", []string{"key-1"}, schemas.ProxyRandom))
	binding, err = store.GetBinding(ctx, "key-1")
	require.NoError(t, err)
	assert.Equal(t, schemas.ProxyRandom, binding.Mechanism)

	// Unbinding one key leaves the other.
	require.NoError(t, store.UnbindKeys(ctx, "pool-1", []string{"key-1"}))
	binding, err = store.GetBinding(ctx, "key-1")
	require.NoError(t, err)
	assert.Nil(t, binding)
	binding, err = store.GetBinding(ctx, "key-2")
	require.NoError(t, err)
	assert.NotNil(t, binding)

	// DeleteKeyBindings clears the remainder.
	require.NoError(t, store.DeleteKeyBindings(ctx, "key-2"))
	binding, err = store.GetBinding(ctx, "key-2")
	require.NoError(t, err)
	assert.Nil(t, binding)

	bindings, err := store.ListBindings(ctx)
	require.NoError(t, err)
	assert.Empty(t, bindings)
}

func TestHasProxyBindingsTracksBindingMutations(t *testing.T) {
	store := setupProxyPoolTestStore(t)
	ctx := context.Background()
	checker, ok := any(store).(interface{ HasProxyBindings() bool })
	require.True(t, ok, "RDBConfigStore must expose the cheap binding-existence check")
	require.NoError(t, store.CreatePool(ctx, &schemas.ProxyPool{ID: "pool-1", Name: "p"}))

	require.False(t, checker.HasProxyBindings())
	require.NoError(t, store.BindKeys(ctx, "pool-1", []string{"key-1"}, schemas.ProxyRoundRobin))
	require.True(t, checker.HasProxyBindings(), "bind must be visible without a store reload")
	require.NoError(t, store.UnbindKeys(ctx, "pool-1", []string{"key-1"}))
	require.False(t, checker.HasProxyBindings(), "unbind must clear the flag")
}

func TestProxyPoolPasswordNeverReturned(t *testing.T) {
	store := setupProxyPoolTestStore(t)
	ctx := context.Background()

	require.NoError(t, store.CreatePool(ctx, &schemas.ProxyPool{ID: "pool-1", Name: "us-egress"}))
	require.NoError(t, store.ImportProxies(ctx, "pool-1", []schemas.PoolProxy{
		{ID: "proxy-1", PoolID: "pool-1", Type: schemas.Socks5Proxy, URL: "socks5://10.0.0.1:1080", Username: "u", Password: schemas.NewSecretVar("super-secret"), Enabled: true, Status: "unknown", Weight: 1},
	}))

	got, err := store.GetPool(ctx, "pool-1")
	require.NoError(t, err)
	b, err := json.Marshal(got)
	require.NoError(t, err)
	assert.NotContains(t, string(b), "super-secret", "password value must not appear in the API shape")
}
