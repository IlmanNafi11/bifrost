package server

import (
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/maximhq/bifrost/plugins/routing"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGetRoutingPluginResolvesRegisteredPlugin pins the type the routing plugin is looked up
// as. Init registers a *RoutingPlugin, and FindPluginAs type-asserts against its type
// parameter, which the compiler cannot check for a generic target. Asserting against the
// struct type instead of the pointer therefore builds fine and fails only at runtime, taking
// routing rule reload, rule removal and complexity analyzer reload down with it.
func TestGetRoutingPluginResolvesRegisteredPlugin(t *testing.T) {
	config := &lib.Config{}
	// A zero-value plugin is enough: the lookup is a type assertion on the registered value,
	// so what matters is the type, not a working plugin. Constructing one would tie this test
	// to the constructor's dependencies, which change further up the stack.
	plugins := []schemas.BasePlugin{&routing.RoutingPlugin{}}
	config.BasePlugins.Store(&plugins)

	server := &BifrostHTTPServer{Config: config}

	// Deliberately does not dereference the result: calling a pointer method here would make
	// this file fail to build against the buggy value-typed signature, turning a runtime bug
	// into a compile error and hiding what actually breaks in production. The returned error
	// is the symptom that matters, since it is what the reload paths surface to callers.
	if _, err := server.getRoutingPlugin(); err != nil {
		t.Fatalf("getRoutingPlugin returned error for a registered routing plugin: %v", err)
	}
}

// TestReloadAndRemoveRoutingRuleRefreshTheGovernanceRuleCache pins the listed_models contract at the
// seam: a rule edit or delete has two readers, the routing engine and governance's own definition
// cache behind the model-listing exception. Only calling the routing plugin would leave governance
// answering from a definition that no longer exists, so a rule could keep opening a model the
// operator just narrowed, or keep a deleted rule's name in /v1/models.
func TestReloadAndRemoveRoutingRuleRefreshTheGovernanceRuleCache(t *testing.T) {
	ctx := t.Context()
	log := bifrost.NewDefaultLogger(schemas.LogLevelError)

	configStore, err := configstore.NewConfigStore(ctx, &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config:  &configstore.SQLiteConfig{Path: t.TempDir() + "/rules.db"},
	}, log)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, configStore.Close(ctx)) })

	govPlugin, err := governance.Init(ctx, nil, log, configStore, nil, nil, nil, nil)
	require.NoError(t, err)
	routingPlugin, err := routing.Init(ctx, nil, log, configStore, govPlugin)
	require.NoError(t, err)

	rule := &configstoreTables.TableRoutingRule{
		ID: "rule-1", Name: "coding", Scope: "global", CelExpression: "true",
		Enabled: bifrost.Ptr(true),
		Targets: []configstoreTables.TableRoutingTarget{{
			Provider: bifrost.Ptr("deepseek"), Model: bifrost.Ptr("deepseek-v4-flash"), Weight: 1,
		}},
	}
	require.NoError(t, configStore.CreateRoutingRule(ctx, rule))

	holders := []schemas.BasePlugin{govPlugin, routingPlugin}
	server := &BifrostHTTPServer{Config: &lib.Config{ConfigStore: configStore}, Ctx: schemas.NewBifrostContext(ctx, schemas.NoDeadline)}
	server.Config.BasePlugins.Store(&holders)

	// The server's governance path is the one the handler calls; assert it reaches the plugin.
	plugin, err := server.getGovernancePlugin()
	require.NoError(t, err)
	cache := plugin.GetGovernanceStore().RoutingRules()
	require.NotNil(t, cache, "a config-store-backed governance store carries the rule cache")

	// The cache was built before the rule existed, so it knows nothing; the reload is what teaches it.
	assert.False(t, cache.AllowsRequest(ctx, "coding", "deepseek", "deepseek-v4-flash"))
	require.NoError(t, server.ReloadRoutingRule(ctx, rule.ID))
	assert.True(t, cache.AllowsRequest(ctx, "coding", "deepseek", "deepseek-v4-flash"), "an edit must reach the governance cache")

	require.NoError(t, configStore.DeleteRoutingRule(ctx, rule.ID))
	require.NoError(t, server.RemoveRoutingRule(ctx, rule.ID))
	assert.False(t, cache.AllowsRequest(ctx, "coding", "deepseek", "deepseek-v4-flash"), "a delete must reach the governance cache")
}
