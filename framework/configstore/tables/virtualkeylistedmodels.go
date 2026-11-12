package tables

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const (
	// MaxVirtualKeyListedModelsEntries caps how many entries one virtual key's listed_models block
	// can carry, models and routing rules together.
	MaxVirtualKeyListedModelsEntries = 100
)

// TableListedModelEntry is one entry of a virtual key's listed_models block. An entry names either
// a model on a specific provider (Provider + Model) or a routing rule (RoutingRuleID), never both.
// The rule is stored and emitted by id, because a rename must not silently change what a key may
// call; the name a config.json author writes is the config-only RoutingRuleName.
type TableListedModelEntry struct {
	Provider      string `json:"provider,omitempty"`
	Model         string `json:"model,omitempty"`
	RoutingRuleID string `json:"routing_rule_id,omitempty"`
	// RoutingRuleName is the name a config.json author writes. It exists only between config load
	// and name→id resolution and is never serialized, so a resolved entry cannot carry a stale name.
	RoutingRuleName string `json:"-"`
}

// UnmarshalJSON accepts a routing rule spelled as a name ("routing_rule", the config-file form)
// alongside the stored id form ("routing_rule_id"). The reverse mapping is deliberately not
// symmetric: an entry always marshals as its id.
func (e *TableListedModelEntry) UnmarshalJSON(data []byte) error {
	type alias TableListedModelEntry
	aux := struct {
		alias
		RoutingRule string `json:"routing_rule"`
	}{}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	*e = TableListedModelEntry(aux.alias)
	e.RoutingRuleName = aux.RoutingRule
	return nil
}

// IsModel reports whether the entry names a provider model rather than a routing rule.
func (e TableListedModelEntry) IsModel() bool { return e.Model != "" }

// IsRule reports whether the entry names a routing rule.
func (e TableListedModelEntry) IsRule() bool { return e.RoutingRuleID != "" || e.RoutingRuleName != "" }

// RuleRef returns the entry's routing rule as the id when resolved, else the configured name.
// Callers that need a definitive id must have resolved the name first.
func (e TableListedModelEntry) RuleRef() string {
	if e.RoutingRuleID != "" {
		return e.RoutingRuleID
	}
	return e.RoutingRuleName
}

// normalizeVirtualKeyListedModels canonicalizes a listed_models block before it is written:
// blank entries are dropped, a rule name that has already been resolved to an id is cleared (the
// stored form is the id), and entries are ordered so the config hash is stable across loads.
//
// It never changes the block's meaning: nil stays nil (feature off) and [] stays [] (deny
// everything). Collapsing one into the other would silently switch the gate on or off.
func normalizeVirtualKeyListedModels(entries *[]TableListedModelEntry) {
	if *entries == nil {
		return
	}
	kept := make([]TableListedModelEntry, 0, len(*entries))
	for _, entry := range *entries {
		entry.Provider = strings.TrimSpace(entry.Provider)
		entry.Model = strings.TrimSpace(entry.Model)
		entry.RoutingRuleID = strings.TrimSpace(entry.RoutingRuleID)
		entry.RoutingRuleName = strings.TrimSpace(entry.RoutingRuleName)
		if entry.RoutingRuleID != "" {
			entry.RoutingRuleName = ""
		}
		if entry.Provider == "" && entry.Model == "" && !entry.IsRule() {
			continue
		}
		kept = append(kept, entry)
	}
	// Models first, then rules; each group ordered by its own identity. Any total order would do,
	// this one keeps a generated listing readable.
	sort.SliceStable(kept, func(i, j int) bool {
		a, b := kept[i], kept[j]
		if a.IsModel() != b.IsModel() {
			return a.IsModel()
		}
		if a.Provider != b.Provider {
			return a.Provider < b.Provider
		}
		if a.Model != b.Model {
			return a.Model < b.Model
		}
		return a.RuleRef() < b.RuleRef()
	})
	*entries = kept
}

// ValidateVirtualKeyListedModels checks a listed_models block: at most
// MaxVirtualKeyListedModelsEntries entries, each naming exactly one of a provider model or a
// routing rule, and no wildcard (the block narrows access, so "*" has no meaning in it; "all" is
// spelled by leaving the block absent). A nil or empty block is valid. Errors name the offending
// entry so API callers can fix it.
func ValidateVirtualKeyListedModels(entries []TableListedModelEntry) error {
	if len(entries) > MaxVirtualKeyListedModelsEntries {
		return fmt.Errorf("listed_models can have at most %d entries, got %d", MaxVirtualKeyListedModelsEntries, len(entries))
	}
	for i, entry := range entries {
		hasModel := entry.Provider != "" || entry.Model != ""
		if hasModel && entry.IsRule() {
			return fmt.Errorf("listed_models entry %d cannot be both a model and a routing rule", i)
		}
		if !hasModel && !entry.IsRule() {
			return fmt.Errorf("listed_models entry %d must name a model or a routing rule", i)
		}
		if entry.IsRule() {
			continue
		}
		if entry.Provider == "" || entry.Model == "" {
			return fmt.Errorf("listed_models entry %d must name a model or a routing rule: a model entry needs both provider and model", i)
		}
		if entry.Provider == "*" || entry.Model == "*" {
			return fmt.Errorf("listed_models entry %d cannot be a wildcard: leave the block absent to allow every model", i)
		}
	}
	return nil
}
