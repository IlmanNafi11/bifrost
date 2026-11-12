package governance

import (
	"context"
	"fmt"
	"sync"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

// routingRuleDefinition is the part of a stored routing rule the listing gate needs: who may call
// it, and which final models it reaches. A rule's scope says who the caller has to be for the rule
// to apply, and its targets and fallbacks say where it can land.
//
// The snapshot is immutable once published, so readers never synchronise with a reload: a read sees
// either the old definition or the new one, never a half-reloaded rule.
type routingRuleDefinition struct {
	id      string
	name    string
	enabled bool
	scope   string
	scopeID string
	// targets and fallbacks are the final provider/model pairs the rule reaches. Entries that name
	// neither are dropped at snapshot time: they inherit the request's own values and so name nothing.
	targets  []modelRef
	fallback []modelRef
}

// modelRef is one final provider/model a rule can land on.
type modelRef struct {
	provider string
	model    string
}

// RoutingRuleStore is governance's own cache of routing-rule definitions, fed from the config
// store's GetRoutingRules. Governance may not import the routing engine, so the rule exception for
// a key's listed_models block is decided here against the rule's stored definition: enabled, in
// scope, and naming the final provider/model the request sits on.
//
// The cache is built at store construction and refreshed by Reload/Remove, which the server calls
// from the same edit/delete paths that already refresh the routing plugin. Anything the cache has no
// definition for - a rule that never existed, was deleted, or a reload has not seen - is a refusal.
type RoutingRuleStore struct {
	mu sync.RWMutex

	byID   map[string]*routingRuleDefinition
	byName map[string]*routingRuleDefinition

	configStore configstore.ConfigStore
	logger      schemas.Logger
}

// NewRoutingRuleStore loads every stored routing rule into the cache. A store failure is returned
// to the caller: a cache that cannot see the rules would refuse every rule a key lists, and that is
// a startup problem, not a silent one. A nil config store builds an empty cache, which refuses
// every rule.
func NewRoutingRuleStore(ctx context.Context, configStore configstore.ConfigStore, logger schemas.Logger) (*RoutingRuleStore, error) {
	store := newRoutingRuleStore(logger)
	store.configStore = configStore
	if configStore == nil {
		return store, nil
	}
	rules, err := configStore.GetRoutingRules(ctx)
	if err != nil {
		return nil, fmt.Errorf("load routing rules for listing cache: %w", err)
	}
	store.seed(rules)
	return store, nil
}

func newRoutingRuleStore(logger schemas.Logger) *RoutingRuleStore {
	return &RoutingRuleStore{
		logger: logger,
		byID:   map[string]*routingRuleDefinition{},
		byName: map[string]*routingRuleDefinition{},
	}
}

// seed installs the definitions the store was built from.
func (s *RoutingRuleStore) seed(rules []configstoreTables.TableRoutingRule) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range rules {
		s.storeLocked(&rules[i])
	}
}

// storeLocked publishes one rule, replacing whatever was cached under its id and name. The previous
// definition's name is cleared so a rename does not leave the old name resolving to the new rule.
func (s *RoutingRuleStore) storeLocked(rule *configstoreTables.TableRoutingRule) *routingRuleDefinition {
	if previous := s.byID[rule.ID]; previous != nil {
		delete(s.byName, previous.name)
	}
	def := snapshotRoutingRule(rule)
	s.byID[def.id] = def
	s.byName[def.name] = def
	return def
}

// Reload refreshes the cached definition of one rule after an edit. The rule is read from the store
// again, so the refresh reflects exactly what the edit persisted; a rule the store no longer has is
// dropped, which is how a delete also ends a listed rule's exception.
func (s *RoutingRuleStore) Reload(ctx context.Context, id string) error {
	if s.configStore == nil {
		return fmt.Errorf("routing rule cache has no config store to reload from")
	}
	rules, err := s.configStore.GetRoutingRules(ctx)
	if err != nil {
		return fmt.Errorf("reload routing rule %s: %w", id, err)
	}
	var loaded *configstoreTables.TableRoutingRule
	for i := range rules {
		if rules[i].ID == id {
			loaded = &rules[i]
			break
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if loaded == nil {
		if previous := s.byID[id]; previous != nil {
			delete(s.byName, previous.name)
		}
		delete(s.byID, id)
		return nil
	}
	s.storeLocked(loaded)
	return nil
}

// Remove drops one rule after a delete. It is the caller's notification that the rule is gone; a
// later Reload for the same id is also safe, but the delete path already knows.
func (s *RoutingRuleStore) Remove(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if previous := s.byID[id]; previous != nil {
		delete(s.byName, previous.name)
	}
	delete(s.byID, id)
}

func snapshotRoutingRule(rule *configstoreTables.TableRoutingRule) *routingRuleDefinition {
	def := &routingRuleDefinition{
		id:      rule.ID,
		name:    rule.Name,
		enabled: rule.EnabledValue(),
		scope:   rule.Scope,
	}
	if rule.ScopeID != nil {
		def.scopeID = *rule.ScopeID
	}
	for i := range rule.Targets {
		target := &rule.Targets[i]
		provider, model := derefString(target.Provider), derefString(target.Model)
		// A target that names neither inherits the request's own provider and model, so it pins no
		// final pair and cannot be what a listed rule was called for.
		if provider == "" && model == "" {
			continue
		}
		def.targets = append(def.targets, modelRef{provider: provider, model: model})
	}
	for _, fb := range rule.ParsedFallbacks {
		resolved := fb.Resolved()
		if resolved.Provider == "" && resolved.Model == "" {
			continue
		}
		def.fallback = append(def.fallback, modelRef{provider: string(resolved.Provider), model: resolved.Model})
	}
	return def
}

// derefString lives in providerjobs.go; routing rules read their nullable columns through it too.
// scopeLevel is one step of the scope chain the routing engine matches against.
type scopeLevel struct {
	scope string
	id    string
}

// scopeChain returns the identifiers a request is governed as, from most to least specific,
// mirroring the routing engine's chain built from the same stamped identifiers.
func scopeChain(ctx context.Context) []scopeLevel {
	return []scopeLevel{
		{scope: "virtual_key", id: bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceVirtualKeyID)},
		{scope: "user", id: bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyUserID)},
		{scope: "team", id: bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceTeamID)},
		{scope: "customer", id: bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceCustomerID)},
		{scope: "global"},
	}
}

// scopeMatches reports whether the rule's scope is one the request's scope chain reaches. A level
// the request has no identifier for is skipped, so a request with a key reaches its own rules, its
// user's, its team's, its customer's, and every global rule - the chain the routing engine matches.
func scopeMatches(def *routingRuleDefinition, chain []scopeLevel) bool {
	if def.scope == "global" {
		return true
	}
	for _, level := range chain {
		if level.scope == def.scope && level.id != "" && level.id == def.scopeID {
			return true
		}
	}
	return false
}

// allowsModel reports whether the final provider/model the request sits on is one the rule reaches.
// A reference that omits provider or model inherits the incoming value, as routing itself does, so
// only the side it names has to match.
func (def *routingRuleDefinition) allowsModel(provider, model string) bool {
	if !def.enabled {
		return false
	}
	matches := func(refs []modelRef) bool {
		for _, ref := range refs {
			if ref.provider != "" && ref.provider != provider {
				continue
			}
			if ref.model != "" && ref.model != model {
				continue
			}
			return true
		}
		return false
	}
	return matches(def.targets) || matches(def.fallback)
}

// AllowsRequest answers the listing-exception question: may a caller asking for requestedRuleName
// sit on provider/model, because that final pair belongs to the rule's own targets or fallbacks and
// the rule is enabled and in scope for the caller? Resolution is by name, which is what the caller
// asks and what the model listing shows.
func (s *RoutingRuleStore) AllowsRequest(ctx context.Context, requestedRuleName string, provider, model string) bool {
	if s == nil || requestedRuleName == "" {
		return false
	}
	s.mu.RLock()
	def := s.byName[requestedRuleName]
	s.mu.RUnlock()
	if def == nil || !def.enabled {
		return false
	}
	if !scopeMatches(def, scopeChain(ctx)) {
		return false
	}
	return def.allowsModel(provider, model)
}

// ListedRuleNames returns the rules a key may call, in the order the key listed them, keeping only
// entries an enabled, in-scope rule answers to by that name. A rule that since stopped existing,
// was renamed, or fell out of scope is dropped, which is what makes it disappear from /v1/models on
// the request that follows the invalidation.
func (s *RoutingRuleStore) ListedRuleNames(ctx context.Context, ruleNames []string) []string {
	if s == nil || len(ruleNames) == 0 {
		return nil
	}
	chain := scopeChain(ctx)
	s.mu.RLock()
	defer s.mu.RUnlock()
	names := make([]string, 0, len(ruleNames))
	for _, name := range ruleNames {
		def := s.byName[name]
		if def == nil || !def.enabled || !scopeMatches(def, chain) {
			continue
		}
		names = append(names, def.name)
	}
	return names
}
