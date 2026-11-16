package tables

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProxyPoolTables round-trips a pool, its proxies and a key binding through
// in-memory SQLite: the proxy password is encrypted at rest (ciphertext in the
// raw row), decrypted on read, and never serialized in JSON; the binding's
// composite key makes it idempotent.
func TestProxyPoolTables(t *testing.T) {
	db := setupTestDB(t)

	pool := &TableProxyPool{ID: "pool-1", Name: "us-egress", Description: "clean US IPs"}
	require.NoError(t, db.Create(pool).Error)

	lastTested := time.Now().Truncate(time.Second)
	proxy := &TablePoolProxy{
		ID:           "proxy-1",
		PoolID:       "pool-1",
		Type:         schemas.Socks5Proxy,
		URL:          "socks5://10.0.0.3:1080",
		Username:     "user",
		Password:     schemas.NewSecretVar("literal-secret"),
		Weight:       2,
		Enabled:      true,
		Status:       "unknown",
		LatencyMs:    0,
		LastTestedAt: &lastTested,
	}
	require.NoError(t, db.Create(proxy).Error)

	binding := &TableProxyPoolBinding{
		PoolID:        "pool-1",
		ProviderKeyID: "key-abc",
		Mechanism:     schemas.ProxyRoundRobin,
	}
	require.NoError(t, db.Create(binding).Error)
	// Composite primary key (pool_id, provider_key_id) rejects a duplicate.
	require.Error(t, db.Create(&TableProxyPoolBinding{
		PoolID:        "pool-1",
		ProviderKeyID: "key-abc",
		Mechanism:     schemas.ProxyRandom,
	}).Error)

	// Password is ciphertext at rest when encryption is enabled.
	raw := rawRow(t, db, "proxy_pool_proxies", "proxy-1")
	stored, _ := raw["password"].(string)
	require.NotEmpty(t, stored)
	assert.NotContains(t, stored, "literal-secret", "proxy password is encrypted at rest")
	assert.Equal(t, EncryptionStatusEncrypted, raw["encryption_status"])

	var loaded TablePoolProxy
	require.NoError(t, db.First(&loaded, "id = ?", "proxy-1").Error)
	require.NotNil(t, loaded.Password)
	assert.Equal(t, "literal-secret", loaded.Password.GetValue(), "password decrypts on read")
	assert.Equal(t, schemas.Socks5Proxy, loaded.Type)
	assert.Equal(t, "user", loaded.Username)
	assert.Equal(t, 2.0, loaded.Weight)
	assert.True(t, loaded.Enabled)

	var loadedBinding TableProxyPoolBinding
	require.NoError(t, db.First(&loadedBinding, "pool_id = ? AND provider_key_id = ?", "pool-1", "key-abc").Error)
	assert.Equal(t, schemas.ProxyRoundRobin, loadedBinding.Mechanism)

	// The secret never reaches JSON output, and the -json tag on the password
	// column is the only thing standing between it and a client.
	b, err := json.Marshal(loaded)
	require.NoError(t, err)
	assert.NotContains(t, string(b), "literal-secret", "password must not serialize")
	assert.NotContains(t, string(b), "password", "password field must be json-hidden")

	var poolBack TableProxyPool
	require.NoError(t, db.First(&poolBack, "id = ?", "pool-1").Error)
	assert.Equal(t, "us-egress", poolBack.Name)
	assert.Equal(t, "clean US IPs", poolBack.Description)
}

// TestTablePoolProxy_EnvRefPasswordLeavesPlainText pins that an env/vault-backed
// password is stored as its reference (not ciphered) so the secret does not reach
// the database at all, mirroring the other SecretVar columns.
func TestTablePoolProxy_EnvRefPasswordLeavesPlainText(t *testing.T) {
	t.Setenv("PROXY_PASS", "env-value")
	db := setupTestDB(t)

	proxy := &TablePoolProxy{
		ID:       "proxy-env",
		PoolID:   "pool-1",
		Type:     schemas.HTTPProxy,
		URL:      "10.0.0.4:8080",
		Password: schemas.NewSecretVar("env.PROXY_PASS"),
	}
	require.NoError(t, db.Create(proxy).Error)

	raw := rawRow(t, db, "proxy_pool_proxies", "proxy-env")
	stored, _ := raw["password"].(string)
	assert.Equal(t, "env.PROXY_PASS", stored, "env reference stored verbatim, never resolved to the secret")
	assert.Equal(t, EncryptionStatusEncrypted, raw["encryption_status"], "status is stamped whenever a password is present, so the startup backfill skips ref-only rows")

	var loaded TablePoolProxy
	require.NoError(t, db.First(&loaded, "id = ?", "proxy-env").Error)
	require.NotNil(t, loaded.Password)
	assert.True(t, loaded.Password.IsFromSecret())
	assert.Equal(t, "env.PROXY_PASS", loaded.Password.GetRawRef())
	assert.Equal(t, "env-value", loaded.Password.GetValue(), "reference re-resolves on read")
}

// TestTableProxyPoolBindingColumnNames pins the physical provider_key_id column:
// bindings reference the upstream schemas.Key.ID string, not the numeric
// auto-increment TableKey.ID, which is storage-local and unstable across reloads.
func TestTableProxyPoolBindingColumnNames(t *testing.T) {
	db := setupTestDB(t)
	require.NoError(t, db.Create(&TableProxyPoolBinding{
		PoolID:        "pool-1",
		ProviderKeyID: "key-1",
		Mechanism:     schemas.ProxyStickySession,
	}).Error)

	var raw map[string]any
	require.NoError(t, db.Table("proxy_pool_bindings").Where("pool_id = ?", "pool-1").Take(&raw).Error)
	_, ok := raw["provider_key_id"]
	assert.True(t, ok, "provider_key_id column must exist")
	_, bad := raw["providerKeyID"]
	assert.False(t, bad, "GORM must not derive providerKeyID from the Go field name")
	assert.True(t, strings.Contains(strings.Join([]string{raw["mechanism"].(string)}, ""), "sticky_session"))
}
