package governance

import (
	"context"
	"sync/atomic"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The rule cache is governance's own copy of what the routing layer would route through, because
// governance may not import the routing engine. What it answers is narrow on purpose: is this rule
// one the request's scope may use, is it still enabled, and does the model the request ended up on
// belong to it (a target or a fallback). Anything it cannot answer is a refusal.

// newRuleCacheTestStore opens an sqlite configstore holding the seeded routing rules. Priorities are
// assigned in seed order, because the store refuses two rules at the same priority within a scope.
func newRuleCacheTestStore(t *testing.T, rules ...*configstoreTables.TableRoutingRule) configstore.ConfigStore {
	t.Helper()
	ctx := context.Background()
	store, err := configstore.NewConfigStore(ctx, &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config:  &configstore.SQLiteConfig{Path: t.TempDir() + "/rules.db"},
	}, NewMockLogger())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close(ctx)) })
	for i, rule := range rules {
		rule.Priority = i
		require.NoError(t, store.CreateRoutingRule(ctx, rule))
	}
	return store
}

// ruleTo builds a stored routing rule with one weighted target.
func ruleTo(id, name, scope string, scopeID *string, enabled bool, provider, model string) *configstoreTables.TableRoutingRule {
	return &configstoreTables.TableRoutingRule{
		ID: id, Name: name, Scope: scope, ScopeID: scopeID,
		Enabled: bifrost.Ptr(enabled), CelExpression: "true",
		Targets: []configstoreTables.TableRoutingTarget{{Provider: &provider, Model: &model, Weight: 1}},
	}
}

// scopedCtx stamps the identifiers a request's access resolution publishes, which is what the cache
// reads its scope chain from.
func scopedCtx(vkID, userID, teamID, customerID string) *schemas.BifrostContext {
	ctx := emptyCtx()
	if vkID != "" {
		ctx.SetValue(schemas.BifrostContextKeyGovernanceVirtualKeyID, vkID)
	}
	if userID != "" {
		ctx.SetValue(schemas.BifrostContextKeyUserID, userID)
	}
	if teamID != "" {
		ctx.SetValue(schemas.BifrostContextKeyGovernanceTeamID, teamID)
	}
	if customerID != "" {
		ctx.SetValue(schemas.BifrostContextKeyGovernanceCustomerID, customerID)
	}
	return ctx
}

func TestRoutingRuleStore_AllowsRequest(t *testing.T) {
	ctx := context.Background()
	vkID := "vk-1"
	otherVK := "vk-2"

	coding := ruleTo("rule-coding", "coding", "global", nil, true, "deepseek", "deepseek-v4-flash")
	coding.ParsedFallbacks = []configstoreTables.RoutingFallback{
		{Fallback: schemas.Fallback{Provider: "bedrock", Model: "kimi-k3"}},
	}
	disabled := ruleTo("rule-off", "off", "global", nil, false, "openai", "gpt-5.4")
	theirs := ruleTo("rule-theirs", "theirs", "virtual_key", &otherVK, true, "openai", "gpt-5.4")
	mine := ruleTo("rule-mine", "mine", "virtual_key", &vkID, true, "anthropic", "claude-sonnet-5")
	anyModel := ruleTo("rule-any", "anything", "global", nil, true, "", "")
	anyModel.Targets = nil

	store := newRuleCacheTestStore(t, coding, disabled, theirs, mine, anyModel)
	rules, err := NewRoutingRuleStore(ctx, store, NewMockLogger())
	require.NoError(t, err)
	ctx = scopedCtx(vkID, "", "team-1", "")

	t.Run("a target of an enabled, in-scope rule", func(t *testing.T) {
		assert.True(t, rules.AllowsRequest(ctx, "coding", "deepseek", "deepseek-v4-flash"))
	})

	t.Run("a fallback of the same rule", func(t *testing.T) {
		assert.True(t, rules.AllowsRequest(ctx, "coding", "bedrock", "kimi-k3"))
	})

	t.Run("a model the rule does not name", func(t *testing.T) {
		assert.False(t, rules.AllowsRequest(ctx, "coding", "openai", "gpt-4o"), "the exception reaches the rule's own targets, not any model")
	})

	t.Run("the right provider with the wrong model", func(t *testing.T) {
		assert.False(t, rules.AllowsRequest(ctx, "coding", "deepseek", "deepseek-v4-pro"))
	})

	t.Run("a disabled rule", func(t *testing.T) {
		assert.False(t, rules.AllowsRequest(ctx, "off", "openai", "gpt-5.4"))
	})

	t.Run("a rule scoped to another key", func(t *testing.T) {
		assert.False(t, rules.AllowsRequest(ctx, "theirs", "openai", "gpt-5.4"))
	})

	t.Run("a rule scoped to this key", func(t *testing.T) {
		assert.True(t, rules.AllowsRequest(ctx, "mine", "anthropic", "claude-sonnet-5"))
	})

	t.Run("an unknown rule name", func(t *testing.T) {
		assert.False(t, rules.AllowsRequest(ctx, "nope", "openai", "gpt-4o"))
	})

	t.Run("a rule with no targets at all names nothing", func(t *testing.T) {
		assert.False(t, rules.AllowsRequest(ctx, "anything", "openai", "gpt-4o"))
	})
}

// Scope matching follows the routing chain: a rule at any level the request reaches applies, and the
// most specific match wins for a name that exists at more than one level.
func TestRoutingRuleStore_ScopeChain(t *testing.T) {
	ctx := context.Background()
	teamID, customerID, userID := "team-1", "cust-1", "user-1"

	teamRule := ruleTo("rule-team", "team-only", "team", &teamID, true, "openai", "gpt-5.4")
	customerRule := ruleTo("rule-cust", "cust-only", "customer", &customerID, true, "openai", "gpt-5.4")
	userRule := ruleTo("rule-user", "user-only", "user", &userID, true, "openai", "gpt-5.4")

	store := newRuleCacheTestStore(t, teamRule, customerRule, userRule)
	rules, err := NewRoutingRuleStore(ctx, store, NewMockLogger())
	require.NoError(t, err)

	scoped := scopedCtx("vk-1", userID, teamID, customerID)
	assert.True(t, rules.AllowsRequest(scoped, "team-only", "openai", "gpt-5.4"))
	assert.True(t, rules.AllowsRequest(scoped, "cust-only", "openai", "gpt-5.4"))
	assert.True(t, rules.AllowsRequest(scoped, "user-only", "openai", "gpt-5.4"))

	bare := emptyCtx()
	assert.False(t, rules.AllowsRequest(bare, "team-only", "openai", "gpt-5.4"), "no scope, no scoped rule")
	assert.False(t, rules.AllowsRequest(bare, "cust-only", "openai", "gpt-5.4"))
	assert.False(t, rules.AllowsRequest(bare, "user-only", "openai", "gpt-5.4"))
}

// A rule name is unique per scope, not per deployment: two teams may each define a rule called the
// same thing over different models. Resolving by name alone would let one team's rule answer for the
// other's key, so the name is resolved inside the caller's own scope chain.
func TestRoutingRuleStore_SameNameInTwoScopes(t *testing.T) {
	ctx := context.Background()
	teamA, teamB := "team-a", "team-b"

	store := newRuleCacheTestStore(t,
		ruleTo("rule-a", "coding", "team", &teamA, true, "openai", "gpt-5.4"),
		ruleTo("rule-b", "coding", "team", &teamB, true, "anthropic", "claude-sonnet-5"),
	)
	rules, err := NewRoutingRuleStore(ctx, store, NewMockLogger())
	require.NoError(t, err)

	callerA := scopedCtx("vk-a", "", teamA, "")
	require.True(t, rules.AllowsRequest(callerA, "coding", "openai", "gpt-5.4"))
	assert.False(t, rules.AllowsRequest(callerA, "coding", "anthropic", "claude-sonnet-5"),
		"team B's rule of the same name must not answer for team A's key")

	callerB := scopedCtx("vk-b", "", teamB, "")
	require.True(t, rules.AllowsRequest(callerB, "coding", "anthropic", "claude-sonnet-5"))
	assert.False(t, rules.AllowsRequest(callerB, "coding", "openai", "gpt-5.4"),
		"team A's rule of the same name must not answer for team B's key")

	assert.Equal(t, []string{"coding"}, rules.ListedRuleNames(callerA, []string{"coding"}))
}

// A rule edited or deleted between requests must be read as it now stands: the cache is refreshed
// from the store on the invalidation the server already runs, and anything it has not seen is a
// refusal rather than a stale yes.
func TestRoutingRuleStore_ReloadAndRemove(t *testing.T) {
	ctx := context.Background()

	rule := ruleTo("rule-edit", "editable", "global", nil, true, "openai", "gpt-5.4")
	store := newRuleCacheTestStore(t, rule)
	rules, err := NewRoutingRuleStore(ctx, store, NewMockLogger())
	require.NoError(t, err)
	requestCtx := emptyCtx()

	require.True(t, rules.AllowsRequest(requestCtx, "editable", "openai", "gpt-5.4"))

	// The model moves and the rule is disabled in one edit.
	updated := ruleTo("rule-edit", "editable", "global", nil, false, "openai", "gpt-5.5")
	require.NoError(t, store.UpdateRoutingRule(ctx, updated))
	require.NoError(t, rules.Reload(ctx, updated.ID))
	assert.False(t, rules.AllowsRequest(requestCtx, "editable", "openai", "gpt-5.4"), "the old definition no longer applies")
	assert.False(t, rules.AllowsRequest(requestCtx, "editable", "openai", "gpt-5.5"), "a disabled rule allows nothing")

	updated.Enabled = bifrost.Ptr(true)
	require.NoError(t, store.UpdateRoutingRule(ctx, updated))
	require.NoError(t, rules.Reload(ctx, updated.ID))
	assert.True(t, rules.AllowsRequest(requestCtx, "editable", "openai", "gpt-5.5"))

	// A rule deleted from the store is removed from the cache; the delete is the invalidation.
	require.NoError(t, store.DeleteRoutingRule(ctx, updated.ID))
	rules.Remove(updated.ID)
	assert.False(t, rules.AllowsRequest(requestCtx, "editable", "openai", "gpt-5.5"))
}

// A reload for an id the store no longer has drops what the cache held under it, rather than keeping
// a definition nothing backs any more.
func TestRoutingRuleStore_ReloadOfMissingRuleDropsIt(t *testing.T) {
	ctx := context.Background()
	rule := ruleTo("rule-gone", "gone", "global", nil, true, "openai", "gpt-5.4")
	store := newRuleCacheTestStore(t, rule)
	rules, err := NewRoutingRuleStore(ctx, store, NewMockLogger())
	require.NoError(t, err)
	requestCtx := emptyCtx()

	require.True(t, rules.AllowsRequest(requestCtx, "gone", "openai", "gpt-5.4"))
	require.NoError(t, store.DeleteRoutingRule(ctx, rule.ID))
	require.NoError(t, rules.Reload(ctx, rule.ID))
	assert.False(t, rules.AllowsRequest(requestCtx, "gone", "openai", "gpt-5.4"))
}

// The listing needs the names of the rules a key may call, in the order the key listed them, and only
// while they are enabled and in scope.
func TestRoutingRuleStore_ListedRuleNames(t *testing.T) {
	ctx := context.Background()
	vkID := "vk-1"
	coding := ruleTo("rule-coding", "coding", "global", nil, true, "deepseek", "deepseek-v4-flash")
	off := ruleTo("rule-off", "retired", "global", nil, false, "openai", "gpt-5.4")
	otherVK := "vk-2"
	theirs := ruleTo("rule-theirs", "theirs", "virtual_key", &otherVK, true, "openai", "gpt-5.4")

	store := newRuleCacheTestStore(t, coding, off, theirs)
	rules, err := NewRoutingRuleStore(ctx, store, NewMockLogger())
	require.NoError(t, err)
	requestCtx := scopedCtx(vkID, "", "", "")

	names := rules.ListedRuleNames(requestCtx, []string{"coding", "retired", "theirs", "unknown"})
	assert.Equal(t, []string{"coding"}, names, "only the rule that is enabled and in scope is listed: a disabled rule, another key's rule, and a name nothing answers to are all dropped")
}

// A nil access to the store is not a permission to skip the rule check: fail closed.
func TestRoutingRuleStore_FailClosed(t *testing.T) {
	ctx := context.Background()
	rules := &RoutingRuleStore{logger: NewMockLogger()}
	assert.False(t, rules.AllowsRequest(emptyCtx(), "coding", "openai", "gpt-4o"))
	assert.Empty(t, rules.ListedRuleNames(emptyCtx(), []string{"coding"}), "nothing cached lists nothing")
	require.Error(t, rules.Reload(ctx, "rule-coding"), "a cache with no store cannot refresh, and says so")
}

// A delete and an edit race on the same rule: the reload reads the table, the delete lands, and the
// reload then publishes the row it read a moment earlier. That would resurrect a rule the operator
// just deleted, opening models for a key that listed it.
func TestRoutingRuleStore_ReloadDoesNotResurrectADeletedRule(t *testing.T) {
	ctx := context.Background()
	rule := ruleTo("rule-race", "racing", "global", nil, true, "openai", "gpt-5.4")
	inner := newRuleCacheTestStore(t, rule)

	// Pause the reload inside its table read, so the delete provably lands before it publishes.
	reading := make(chan struct{}, 1)
	release := make(chan struct{})
	racing := &pausingRoutingRuleStore{ConfigStore: inner, reading: reading, release: release}

	rules, err := NewRoutingRuleStore(ctx, racing, NewMockLogger())
	require.NoError(t, err)
	require.True(t, rules.AllowsRequest(emptyCtx(), "racing", "openai", "gpt-5.4"))

	racing.pause.Store(true)
	done := make(chan error, 1)
	go func() { done <- rules.Reload(ctx, rule.ID) }()

	<-reading
	rules.Remove(rule.ID)
	require.NoError(t, inner.DeleteRoutingRule(ctx, rule.ID))
	close(release)
	require.NoError(t, <-done)

	assert.False(t, rules.AllowsRequest(emptyCtx(), "racing", "openai", "gpt-5.4"),
		"a reload that raced a delete must not publish the deleted rule")
	rules.mu.RLock()
	_, cached := rules.byID[rule.ID]
	rules.mu.RUnlock()
	assert.False(t, cached, "the deleted rule must not be back in the cache")
}

// pausingRoutingRuleStore hands the test control of the window between the reload's table read and
// its publish, which is where a delete has to win. The pause starts off so construction, which also
// reads the table, runs normally.
type pausingRoutingRuleStore struct {
	configstore.ConfigStore
	pause   atomic.Bool
	reading chan struct{}
	release chan struct{}
}

func (p *pausingRoutingRuleStore) GetRoutingRule(ctx context.Context, id string) (*configstoreTables.TableRoutingRule, error) {
	rule, err := p.ConfigStore.GetRoutingRule(ctx, id)
	if p.pause.Load() {
		p.reading <- struct{}{}
		<-p.release
	}
	return rule, err
}
