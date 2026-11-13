package tables

import (
	"fmt"
	"sort"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

const (
	// MaxVirtualKeyListedModelsEntries caps how many entries one virtual key's listed_models block
	// can carry, models and routing rules together.
	MaxVirtualKeyListedModelsEntries = 100
)

// TableListedModelEntry is one entry of a virtual key's listed_models block. An entry names either
// a model on a specific provider (Provider + Model) or a routing rule (RoutingRule), never both.
//
// The rule is stored by name rather than by id. A listed rule is called by its name and is shown by
// its name in /v1/models, so name is the entry's whole identity: a rename is visible to the key that
// listed it, and a rule that no longer exists simply stops matching anything. Storing an id here
// would need a name to be resolved on every read and would silently follow a rename.
type TableListedModelEntry struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	// RoutingRule is the name of a routing rule the key may call, and the model the listing shows.
	RoutingRule string `json:"routing_rule,omitempty"`
}

// IsModel reports whether the entry names a provider model rather than a routing rule.
func (e TableListedModelEntry) IsModel() bool { return e.Model != "" }

// IsRule reports whether the entry names a routing rule.
func (e TableListedModelEntry) IsRule() bool { return e.RoutingRule != "" }

// RuleRef returns the entry's routing rule name, or "" for a model entry.
func (e TableListedModelEntry) RuleRef() string { return e.RoutingRule }

// normalizeVirtualKeyListedModels canonicalizes a listed_models block before it is written: blank
// entries are dropped and entries are ordered so the config hash is stable across loads.
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
		entry.RoutingRule = strings.TrimSpace(entry.RoutingRule)
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
			if strings.Contains(entry.RoutingRule, "/") {
				return fmt.Errorf("listed_models entry %d names routing rule %q: a rule is listed by name, not as provider/model", i, entry.RoutingRule)
			}
			continue
		}
		if entry.Provider == "" || entry.Model == "" {
			return fmt.Errorf("listed_models entry %d must name a model or a routing rule: a model entry needs both provider and model", i)
		}
		if entry.Provider == "*" || entry.Model == "*" {
			return fmt.Errorf("listed_models entry %d cannot be a wildcard: leave the block absent to allow every model", i)
		}
		// The model lists match "regex:" entries as RE2 patterns, so a pattern here would be a
		// wildcard the rest of this block's validation was written to refuse - and the listing would
		// show the literal pattern string instead of the models it matches. A listed model is named.
		if schemas.IsRegexEntry(entry.Provider) || schemas.IsRegexEntry(entry.Model) {
			return fmt.Errorf("listed_models entry %d cannot be a regex entry: name the model exactly, or leave the block absent to allow every model", i)
		}
	}
	return nil
}
