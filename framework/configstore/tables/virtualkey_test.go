package tables

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TestVirtualKeyProviderConfigKeyIDs pins KeyIDs' actual empty-Keys semantics, which the Keys
// field's own comment previously contradicted: AllowAllKeys=false with no Keys rows means no
// keys allowed, not all keys allowed (matching AllowAllKeys' own comment, and the grant layer's
// deny-by-default reading of an empty KeyIDs list).
func TestVirtualKeyProviderConfigKeyIDs(t *testing.T) {
	t.Run("AllowAllKeys true returns the wildcard regardless of Keys", func(t *testing.T) {
		pc := &TableVirtualKeyProviderConfig{AllowAllKeys: true}
		got := pc.KeyIDs()
		if len(got) != 1 || got[0] != "*" {
			t.Fatalf("KeyIDs() = %v, want [\"*\"]", got)
		}
	})

	t.Run("AllowAllKeys false with no Keys means no keys allowed, not all keys", func(t *testing.T) {
		pc := &TableVirtualKeyProviderConfig{AllowAllKeys: false}
		got := pc.KeyIDs()
		if len(got) != 0 {
			t.Fatalf("KeyIDs() = %v, want an empty list (deny-by-default), not the all-keys wildcard", got)
		}
	})

	t.Run("AllowAllKeys false with specific Keys returns exactly those IDs", func(t *testing.T) {
		pc := &TableVirtualKeyProviderConfig{
			AllowAllKeys: false,
			Keys:         []TableKey{{KeyID: "key-1"}, {KeyID: "key-2"}},
		}
		got := pc.KeyIDs()
		if len(got) != 2 || got[0] != "key-1" || got[1] != "key-2" {
			t.Fatalf("KeyIDs() = %v, want [key-1 key-2]", got)
		}
	})
}

// TestVirtualKeyAssignedUserSerialization pins the tri-state assigned_user contract that the
// field's own comment, the TS VirtualKey type and useVirtualKeyUsage all depend on: a resolved
// key carries assigned_user (object or null), and a key whose assignee could not be resolved
// omits the field entirely so the UI can tell "unassigned" from "unknown" and refetch.
func TestVirtualKeyAssignedUserSerialization(t *testing.T) {
	marshalToMap := func(t *testing.T, vk TableVirtualKey) map[string]json.RawMessage {
		t.Helper()
		b, err := json.Marshal(vk)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return m
	}

	t.Run("resolved with an assignee emits the user", func(t *testing.T) {
		m := marshalToMap(t, TableVirtualKey{
			ID:               "vk-1",
			AssigneeResolved: true,
			AssignedUser:     &AssignedUser{ID: "user-1", Name: "Ada", Email: "ada@example.com"},
		})
		raw, ok := m["assigned_user"]
		if !ok {
			t.Fatal("expected assigned_user to be present for a resolved key")
		}
		var got AssignedUser
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("unmarshal assigned_user: %v", err)
		}
		if got.Email != "ada@example.com" {
			t.Fatalf("assigned_user = %#v, want ada@example.com", got)
		}
	})

	t.Run("resolved with no assignee emits null, not an absent key", func(t *testing.T) {
		m := marshalToMap(t, TableVirtualKey{ID: "vk-1", AssigneeResolved: true})
		raw, ok := m["assigned_user"]
		if !ok {
			t.Fatal("expected assigned_user to be present (null) for a resolved, unassigned key")
		}
		if string(raw) != "null" {
			t.Fatalf("assigned_user = %s, want null", raw)
		}
	})

	t.Run("unresolved omits the key so callers can tell unknown from unassigned", func(t *testing.T) {
		m := marshalToMap(t, TableVirtualKey{ID: "vk-1"})
		if raw, ok := m["assigned_user"]; ok {
			t.Fatalf("expected assigned_user to be absent when unresolved, got %s", raw)
		}
		// The rest of the key must still serialize normally.
		if _, ok := m["id"]; !ok {
			t.Fatal("expected the remaining fields to survive the omission")
		}
	})
}

// TestVirtualKeyOwnerMutualExclusion pins that a key belongs to at most one owner. The owner is
// what decides whose money a request spends and whose access profile the key answers to, so a key
// claiming two of them has no answer to either question.
//
// Every pair is exercised rather than just the team/customer one the check originally covered:
// business_unit_id joined team_id and customer_id as an owner, and a pairwise check is exactly the
// kind that leaves a new pair unguarded.
func TestVirtualKeyOwnerMutualExclusion(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&TableVirtualKey{}))

	ptr := func(s string) *string { return &s }
	newKey := func(name string) TableVirtualKey {
		return TableVirtualKey{
			ID:    name,
			Name:  name,
			Value: *schemas.NewSecretVar("sk-bf-" + name),
		}
	}

	t.Run("one owner is allowed", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			apply func(*TableVirtualKey)
		}{
			{"team", func(vk *TableVirtualKey) { vk.TeamID = ptr("team-1") }},
			{"customer", func(vk *TableVirtualKey) { vk.CustomerID = ptr("cust-1") }},
			{"business unit", func(vk *TableVirtualKey) { vk.BusinessUnitID = ptr("bu-1") }},
			{"none", func(vk *TableVirtualKey) {}},
		} {
			vk := newKey("solo-" + tc.name)
			tc.apply(&vk)
			require.NoError(t, db.Create(&vk).Error, "a key owned by %s alone must save", tc.name)
		}
	})

	t.Run("a blank owner id is no owner", func(t *testing.T) {
		// JSON decoding turns "team_id": "" into a pointer to an empty string. Counted as an owner it
		// would store an owner nothing resolves, and would make a body naming one real owner beside a
		// blank one read as two.
		vk := newKey("blank-owner")
		vk.TeamID, vk.CustomerID, vk.BusinessUnitID = ptr(""), ptr("   "), ptr("bu-1")
		require.NoError(t, db.Create(&vk).Error, "blank owner ids must not count as owners")

		var stored TableVirtualKey
		require.NoError(t, db.First(&stored, "id = ?", vk.ID).Error)
		assert.Nil(t, stored.TeamID, "a blank team id is stored as no team")
		assert.Nil(t, stored.CustomerID, "a whitespace customer id is stored as no customer")
		require.NotNil(t, stored.BusinessUnitID)
		assert.Equal(t, "bu-1", *stored.BusinessUnitID, "the one real owner survives")
	})

	t.Run("two owners are rejected", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			apply func(*TableVirtualKey)
		}{
			{"team and customer", func(vk *TableVirtualKey) {
				vk.TeamID, vk.CustomerID = ptr("team-1"), ptr("cust-1")
			}},
			{"team and business unit", func(vk *TableVirtualKey) {
				vk.TeamID, vk.BusinessUnitID = ptr("team-1"), ptr("bu-1")
			}},
			{"customer and business unit", func(vk *TableVirtualKey) {
				vk.CustomerID, vk.BusinessUnitID = ptr("cust-1"), ptr("bu-1")
			}},
			{"all three", func(vk *TableVirtualKey) {
				vk.TeamID, vk.CustomerID, vk.BusinessUnitID = ptr("team-1"), ptr("cust-1"), ptr("bu-1")
			}},
		} {
			vk := newKey("dual-" + tc.name)
			tc.apply(&vk)
			err := db.Create(&vk).Error
			require.Error(t, err, "a key owned by %s must be rejected", tc.name)
			assert.Contains(t, err.Error(), "more than one of team, customer or business unit")
		}
	})
}

// TestValidateVirtualKeyMetadata covers the entry, key and value limits and the reserved keys,
// including the load balancer's "bifrost_alb_" prefix spelled out literally so a drift from
// core's schemas.LoadBalancerMetadataPrefix value shows up here.
func TestValidateVirtualKeyMetadata(t *testing.T) {
	tooMany := make(map[string]string, MaxVirtualKeyMetadataEntries+1)
	for i := range MaxVirtualKeyMetadataEntries + 1 {
		tooMany[fmt.Sprintf("k%d", i)] = "v"
	}
	atLimit := make(map[string]string, MaxVirtualKeyMetadataEntries)
	for i := range MaxVirtualKeyMetadataEntries {
		atLimit[fmt.Sprintf("k%d", i)] = "v"
	}
	tests := []struct {
		name     string
		metadata map[string]string
		wantErr  string
	}{
		{name: "nil", metadata: nil},
		{name: "empty", metadata: map[string]string{}},
		{name: "valid keys", metadata: map[string]string{"cost_center": "cc-42", "owner.email": "a@example.com", "env-1": ""}},
		{name: "entries at limit", metadata: atLimit},
		{name: "key at max length", metadata: map[string]string{strings.Repeat("k", MaxVirtualKeyMetadataKeyLength): "v"}},
		{name: "value at max length", metadata: map[string]string{"k": strings.Repeat("é", MaxVirtualKeyMetadataValueLength)}},
		{name: "too many entries", metadata: tooMany, wantErr: "at most 50 entries"},
		{name: "empty key", metadata: map[string]string{"": "v"}, wantErr: "invalid metadata key"},
		{name: "key with space", metadata: map[string]string{"cost center": "v"}, wantErr: `invalid metadata key "cost center"`},
		{name: "key with quote", metadata: map[string]string{`a"b`: "v"}, wantErr: "invalid metadata key"},
		{name: "key too long", metadata: map[string]string{strings.Repeat("k", MaxVirtualKeyMetadataKeyLength+1): "v"}, wantErr: "invalid metadata key"},
		{name: "reserved key", metadata: map[string]string{"isAsyncRequest": "true"}, wantErr: "reserved"},
		{name: "load balancer prefix", metadata: map[string]string{"bifrost_alb_provider": "x"}, wantErr: "reserved"},
		{name: "value too long", metadata: map[string]string{"k": strings.Repeat("v", MaxVirtualKeyMetadataValueLength+1)}, wantErr: "at most 512 characters"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateVirtualKeyMetadata(tt.metadata)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

// TestVirtualKeyListedModelsRoundTrip pins the D7 distinction the whole feature rests on: an absent
// block (nil, feature off) and an explicitly empty block ([], deny everything) must survive the trip
// through the database without collapsing into each other. GORM's json serializer writes nil as NULL
// and [] as '[]', so the only way this breaks is if a hook normalizes one into the other.
func TestVirtualKeyListedModelsRoundTrip(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&TableVirtualKey{}))

	modelEntry := TableListedModelEntry{Provider: "openai", Model: "gpt-5.4"}
	ruleEntry := TableListedModelEntry{RoutingRule: "coding"}

	t.Run("absent block stays nil", func(t *testing.T) {
		vk := TableVirtualKey{ID: "lm-nil", Name: "lm-nil", Value: *schemas.NewSecretVar("sk-bf-lm-nil")}
		require.NoError(t, db.Create(&vk).Error)

		var stored TableVirtualKey
		require.NoError(t, db.First(&stored, "id = ?", vk.ID).Error)
		assert.Nil(t, stored.ListedModels)

		raw, err := json.Marshal(stored)
		require.NoError(t, err)
		assert.NotContains(t, string(raw), "listed_models", "an absent block must not be emitted as an empty one")
	})

	t.Run("empty block stays empty and denies", func(t *testing.T) {
		vk := TableVirtualKey{
			ID: "lm-empty", Name: "lm-empty", Value: *schemas.NewSecretVar("sk-bf-lm-empty"),
			ListedModels: []TableListedModelEntry{},
		}
		require.NoError(t, db.Create(&vk).Error)

		var stored TableVirtualKey
		require.NoError(t, db.First(&stored, "id = ?", vk.ID).Error)
		require.NotNil(t, stored.ListedModels, "[] must not come back as nil, which would silently re-enable the feature")
		assert.Empty(t, stored.ListedModels)

		raw, err := json.Marshal(stored)
		require.NoError(t, err)
		assert.Contains(t, string(raw), `"listed_models":[]`)
	})

	t.Run("provider/model and rule entries both survive", func(t *testing.T) {
		vk := TableVirtualKey{
			ID: "lm-entries", Name: "lm-entries", Value: *schemas.NewSecretVar("sk-bf-lm-entries"),
			ListedModels: []TableListedModelEntry{modelEntry, ruleEntry},
		}
		require.NoError(t, db.Create(&vk).Error)

		var stored TableVirtualKey
		require.NoError(t, db.First(&stored, "id = ?", vk.ID).Error)
		require.Len(t, stored.ListedModels, 2)
		assert.ElementsMatch(t, []TableListedModelEntry{modelEntry, ruleEntry}, stored.ListedModels)
	})
}

// TestTableListedModelEntryJSON pins the wire shape of one entry: a model entry is
// {"provider","model"} and a rule entry is {"routing_rule":"coding"}. The name is the rule's whole
// identity - what the caller asks for and what /v1/models shows - so it is what is stored and
// emitted, with no id form to keep in step with it.
func TestTableListedModelEntryJSON(t *testing.T) {
	t.Run("a rule entry reads back by name", func(t *testing.T) {
		var entry TableListedModelEntry
		require.NoError(t, json.Unmarshal([]byte(`{"routing_rule":"coding"}`), &entry))
		assert.Equal(t, "coding", entry.RoutingRule)
		assert.True(t, entry.IsRule())
		assert.False(t, entry.IsModel())
	})

	t.Run("a rule entry is emitted by name", func(t *testing.T) {
		entry := TableListedModelEntry{RoutingRule: "coding"}
		raw, err := json.Marshal(entry)
		require.NoError(t, err)
		assert.JSONEq(t, `{"routing_rule":"coding"}`, string(raw))
	})

	t.Run("model entry", func(t *testing.T) {
		raw, err := json.Marshal(TableListedModelEntry{Provider: "openai", Model: "gpt-5.4"})
		require.NoError(t, err)
		assert.JSONEq(t, `{"provider":"openai","model":"gpt-5.4"}`, string(raw))
	})
}

// TestValidateVirtualKeyListedModels covers the entry rule (exactly one of a model or a route, never
// a wildcard), the 100-entry cap, and the normalization BeforeSave applies: blank entries dropped,
// [] kept as [], nil kept as nil.
func TestValidateVirtualKeyListedModels(t *testing.T) {
	model := func(provider, name string) TableListedModelEntry {
		return TableListedModelEntry{Provider: provider, Model: name}
	}
	tooMany := make([]TableListedModelEntry, 0, MaxVirtualKeyListedModelsEntries+1)
	for i := range MaxVirtualKeyListedModelsEntries + 1 {
		tooMany = append(tooMany, model("openai", fmt.Sprintf("m%d", i)))
	}
	atLimit := make([]TableListedModelEntry, 0, MaxVirtualKeyListedModelsEntries)
	for i := range MaxVirtualKeyListedModelsEntries {
		atLimit = append(atLimit, model("openai", fmt.Sprintf("m%d", i)))
	}
	tests := []struct {
		name    string
		entries []TableListedModelEntry
		wantErr string
	}{
		{name: "nil", entries: nil},
		{name: "empty", entries: []TableListedModelEntry{}},
		{name: "model entry", entries: []TableListedModelEntry{model("openai", "gpt-5.4")}},
		{name: "rule entry", entries: []TableListedModelEntry{{RoutingRule: "coding"}}},
		{name: "rule spelled with a provider prefix", entries: []TableListedModelEntry{{RoutingRule: "openai/coding"}}, wantErr: "listed by name"},
		{name: "mixed", entries: []TableListedModelEntry{model("openai", "gpt-5.4"), {RoutingRule: "coding"}}},
		{name: "entries at limit", entries: atLimit},
		{name: "too many entries", entries: tooMany, wantErr: "at most 100 entries"},
		{name: "neither model nor rule", entries: []TableListedModelEntry{{}}, wantErr: "must name a model or a routing rule"},
		{name: "provider without model", entries: []TableListedModelEntry{{Provider: "openai"}}, wantErr: "must name a model or a routing rule"},
		{name: "model without provider", entries: []TableListedModelEntry{{Model: "gpt-5.4"}}, wantErr: "must name a model or a routing rule"},
		{name: "both model and rule", entries: []TableListedModelEntry{{Provider: "openai", Model: "gpt-5.4", RoutingRule: "coding"}}, wantErr: "cannot be both"},
		{name: "wildcard model", entries: []TableListedModelEntry{model("openai", "*")}, wantErr: `cannot be a wildcard`},
		{name: "wildcard provider", entries: []TableListedModelEntry{model("*", "gpt-5.4")}, wantErr: `cannot be a wildcard`},
		// A regex entry would grant every model the pattern matches, which is the whole catalog the
		// block exists to narrow; the provider lists accept patterns, this one must not.
		{name: "regex model", entries: []TableListedModelEntry{model("openai", "regex:.*")}, wantErr: "cannot be a regex entry"},
		{name: "regex provider", entries: []TableListedModelEntry{model("regex:^open", "gpt-5.4")}, wantErr: "cannot be a regex entry"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateVirtualKeyListedModels(tt.entries)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

// TestNormalizeVirtualKeyListedModels pins what BeforeSave does to a block before writing it: blank
// entries (a config-file template with empty fields) are dropped, the rest are ordered
// deterministically so the config hash is stable, and neither nil nor [] changes meaning.
func TestNormalizeVirtualKeyListedModels(t *testing.T) {
	t.Run("nil stays nil", func(t *testing.T) {
		var entries []TableListedModelEntry
		normalizeVirtualKeyListedModels(&entries)
		assert.Nil(t, entries)
	})

	t.Run("empty stays empty, not nil", func(t *testing.T) {
		entries := []TableListedModelEntry{}
		normalizeVirtualKeyListedModels(&entries)
		require.NotNil(t, entries)
		assert.Empty(t, entries)
	})

	t.Run("blank entries are dropped", func(t *testing.T) {
		entries := []TableListedModelEntry{
			{},
			{Provider: "openai", Model: "gpt-5.4"},
			{Provider: " ", Model: " "},
		}
		normalizeVirtualKeyListedModels(&entries)
		assert.Equal(t, []TableListedModelEntry{{Provider: "openai", Model: "gpt-5.4"}}, entries)
	})

	t.Run("order is deterministic", func(t *testing.T) {
		entries := []TableListedModelEntry{
			{RoutingRule: "b-coding"},
			{Provider: "openai", Model: "gpt-5.4"},
			{RoutingRule: "a-coding"},
			{Provider: "anthropic", Model: "claude-sonnet-5"},
		}
		normalizeVirtualKeyListedModels(&entries)
		assert.Equal(t, []TableListedModelEntry{
			{Provider: "anthropic", Model: "claude-sonnet-5"},
			{Provider: "openai", Model: "gpt-5.4"},
			{RoutingRule: "a-coding"},
			{RoutingRule: "b-coding"},
		}, entries)
	})

	t.Run("a rule entry is stored by name unchanged", func(t *testing.T) {
		entries := []TableListedModelEntry{{RoutingRule: " coding "}}
		normalizeVirtualKeyListedModels(&entries)
		require.Len(t, entries, 1)
		assert.Equal(t, "coding", entries[0].RoutingRule)
	})
}
