package configstore

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ListPools loads every proxy pool with its proxies, in id order, so startup
// reload and API output are stable.
func (s *RDBConfigStore) ListPools(ctx context.Context) ([]schemas.ProxyPool, error) {
	var rows []tables.TableProxyPool
	if err := s.DB().WithContext(ctx).Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	proxies, err := s.loadProxiesByPoolID(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]schemas.ProxyPool, 0, len(rows))
	for _, row := range rows {
		out = append(out, proxyPoolFromTable(row, proxies[row.ID]))
	}
	return out, nil
}

// GetPool loads one pool and its proxies, ErrNotFound when it does not exist.
func (s *RDBConfigStore) GetPool(ctx context.Context, id string) (*schemas.ProxyPool, error) {
	var row tables.TableProxyPool
	if err := s.DB().WithContext(ctx).First(&row, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	proxies, err := s.loadProxiesByPoolID(ctx, []string{id})
	if err != nil {
		return nil, err
	}
	p := proxyPoolFromTable(row, proxies[id])
	return &p, nil
}

// loadProxiesByPoolID loads all proxies whose pool_id is in poolIDs, grouped by
// pool id, ordered by id. An empty poolIDs slice reads nothing.
func (s *RDBConfigStore) loadProxiesByPoolID(ctx context.Context, poolIDs []string) (map[string][]tables.TablePoolProxy, error) {
	if len(poolIDs) == 0 {
		return map[string][]tables.TablePoolProxy{}, nil
	}
	var rows []tables.TablePoolProxy
	if err := s.DB().WithContext(ctx).Where("pool_id IN ?", poolIDs).Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string][]tables.TablePoolProxy, len(poolIDs))
	for i := range rows {
		out[rows[i].PoolID] = append(out[rows[i].PoolID], rows[i])
	}
	return out, nil
}

// CreatePool persists a new pool. The id must be supplied by the caller; it is
// the stable handle bindings and the API reference.
func (s *RDBConfigStore) CreatePool(ctx context.Context, p *schemas.ProxyPool) error {
	if p.ID == "" {
		return fmt.Errorf("pool id required")
	}
	row := tables.TableProxyPool{ID: p.ID, Name: p.Name, Description: p.Description}
	return s.DB().WithContext(ctx).Create(&row).Error
}

// UpdatePool replaces a pool's name and description. Proxies are managed by
// ImportProxies/DeleteProxies, so a caller-supplied Proxies slice is ignored.
func (s *RDBConfigStore) UpdatePool(ctx context.Context, p *schemas.ProxyPool) error {
	res := s.DB().WithContext(ctx).
		Model(&tables.TableProxyPool{}).
		Where("id = ?", p.ID).
		Updates(map[string]any{"name": p.Name, "description": p.Description})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// DeletePool deletes a pool, its proxies and its key bindings in one
// transaction. The table-level cascade would do this by itself; the explicit
// deletes keep the behavior identical under the SQLite test harness where
// foreign keys may not be enforced.
func (s *RDBConfigStore) DeletePool(ctx context.Context, id string) error {
	return s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&tables.TableProxyPool{}).Where("id = ?", id).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return ErrNotFound
		}
		if err := tx.Where("pool_id = ?", id).Delete(&tables.TableProxyPoolBinding{}).Error; err != nil {
			return err
		}
		if err := tx.Where("pool_id = ?", id).Delete(&tables.TablePoolProxy{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", id).Delete(&tables.TableProxyPool{}).Error
	})
}

// ImportProxies upserts proxies into a pool. The pool must exist. Rows are
// matched by their existing id when present, otherwise a fresh id is minted;
// a duplicate URL within a single call collapses to the first entry. Selection
// state from a prior probe is preserved on an id match. Passwords are encrypted
// on write via the table's BeforeSave hook.
func (s *RDBConfigStore) ImportProxies(ctx context.Context, poolID string, proxies []schemas.PoolProxy) error {
	return s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&tables.TableProxyPool{}).Where("id = ?", poolID).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return ErrNotFound
		}
		seen := make(map[string]struct{}, len(proxies))
		for i := range proxies {
			pp := &proxies[i]
			if pp.URL == "" {
				return fmt.Errorf("proxy url required")
			}
			if _, dup := seen[pp.URL]; dup {
				continue
			}
			seen[pp.URL] = struct{}{}
			if err := s.upsertPoolProxy(ctx, tx, poolID, pp); err != nil {
				return err
			}
		}
		return nil
	})
}

// upsertPoolProxy writes one proxy row keyed by URL. A URL match updates the
// caller-editable fields and keeps the existing id and probe/cooldown state;
// otherwise a fresh row is inserted with a minted id. Passing through a full
// *TablePoolProxy and Save (not a map Updates) fires BeforeSave, which encrypts
// a literal password.
func (s *RDBConfigStore) upsertPoolProxy(ctx context.Context, tx *gorm.DB, poolID string, pp *schemas.PoolProxy) error {
	now := time.Now().UTC()
	var existing tables.TablePoolProxy
	err := tx.Where("pool_id = ? AND url = ?", poolID, pp.URL).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		id := pp.ID
		if id == "" {
			id = uuid.NewString()
		}
		return tx.Create(&tables.TablePoolProxy{ID: id, PoolID: poolID, Type: pp.Type, URL: pp.URL, Username: pp.Username, Password: pp.Password, Weight: pp.Weight, Enabled: true, Status: "unknown", CreatedAt: now, UpdatedAt: now}).Error
	}
	if err != nil {
		return err
	}
	existing.Type = pp.Type
	existing.URL = pp.URL
	existing.Username = pp.Username
	existing.Password = pp.Password
	existing.Weight = pp.Weight
	existing.Enabled = true
	existing.UpdatedAt = now
	return tx.Save(&existing).Error
}

// DeleteProxies removes the named proxies from a pool. Deleting ids that do not
// belong to the pool is not an error, and no id is an error-free no-op.
func (s *RDBConfigStore) DeleteProxies(ctx context.Context, poolID string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	return s.DB().WithContext(ctx).
		Where("pool_id = ? AND id IN ?", poolID, ids).
		Delete(&tables.TablePoolProxy{}).Error
}

// SetProxiesEnabled flips the enabled flag of the named proxies in a pool.
// Selection mechanisms skip disabled proxies; disabling never deletes a binding.
func (s *RDBConfigStore) SetProxiesEnabled(ctx context.Context, poolID string, ids []string, enabled bool) error {
	if len(ids) == 0 {
		return nil
	}
	return s.DB().WithContext(ctx).
		Model(&tables.TablePoolProxy{}).
		Where("pool_id = ? AND id IN ?", poolID, ids).
		Update("enabled", enabled).Error
}

// SetProxyStatus records a probe outcome on one proxy: status, latency and the
// observed egress ip. last_tested_at is stamped now. Rows are matched by id
// anywhere in the table; pool id is not part of the proxy key.
func (s *RDBConfigStore) SetProxyStatus(ctx context.Context, id string, status string, latencyMs int64, egressIP string) error {
	return s.DB().WithContext(ctx).
		Model(&tables.TablePoolProxy{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"status":         status,
			"latency_ms":     latencyMs,
			"egress_ip":      egressIP,
			"last_tested_at": time.Now().UTC(),
			"updated_at":     time.Now().UTC(),
		}).Error
}

// BindKeys binds a pool with a selection mechanism to each named key, upserting
// by the (pool_id, provider_key_id) composite key. Binding a key already bound
// to another pool moves it. The pool must exist; unknown keys are ignored.
func (s *RDBConfigStore) BindKeys(ctx context.Context, poolID string, keyIDs []string, mechanism schemas.ProxyMechanism) error {
	if len(keyIDs) == 0 {
		return nil
	}
	sorted := slices.Clone(keyIDs)
	slices.Sort(sorted)
	unique := slices.Compact(sorted)
	return s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&tables.TableProxyPool{}).Where("id = ?", poolID).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return ErrNotFound
		}
		now := time.Now().UTC()
		for _, keyID := range unique {
			b := &tables.TableProxyPoolBinding{PoolID: poolID, ProviderKeyID: keyID, Mechanism: mechanism, CreatedAt: now}
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "pool_id"}, {Name: "provider_key_id"}},
				DoUpdates: clause.AssignmentColumns([]string{"mechanism"}),
			}).Create(b).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// UnbindKeys removes the given keys from a pool. Keys without a binding are
// not an error.
func (s *RDBConfigStore) UnbindKeys(ctx context.Context, poolID string, keyIDs []string) error {
	if len(keyIDs) == 0 {
		return nil
	}
	return s.DB().WithContext(ctx).
		Where("pool_id = ? AND provider_key_id IN ?", poolID, keyIDs).
		Delete(&tables.TableProxyPoolBinding{}).Error
}

// ListBindings returns every key binding, ordered by key id so reloads are stable.
func (s *RDBConfigStore) ListBindings(ctx context.Context) ([]schemas.ProxyPoolBinding, error) {
	var rows []tables.TableProxyPoolBinding
	if err := s.DB().WithContext(ctx).Order("provider_key_id ASC, pool_id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]schemas.ProxyPoolBinding, 0, len(rows))
	for _, row := range rows {
		out = append(out, schemas.ProxyPoolBinding{
			PoolID:    row.PoolID,
			KeyID:     row.ProviderKeyID,
			Mechanism: row.Mechanism,
		})
	}
	return out, nil
}

// GetBinding returns the pool binding for one upstream key, or nil when the
// key is unbound.
func (s *RDBConfigStore) GetBinding(ctx context.Context, keyID string) (*schemas.ProxyPoolBinding, error) {
	var row tables.TableProxyPoolBinding
	if err := s.DB().WithContext(ctx).First(&row, "provider_key_id = ?", keyID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &schemas.ProxyPoolBinding{PoolID: row.PoolID, KeyID: row.ProviderKeyID, Mechanism: row.Mechanism}, nil
}

// DeleteKeyBindings removes every binding on one upstream key. Called from the
// key-delete path so a deleted key never leaves a binding a recreated key could
// resurrect.
func (s *RDBConfigStore) DeleteKeyBindings(ctx context.Context, keyID string) error {
	return s.DB().WithContext(ctx).
		Where("provider_key_id = ?", keyID).
		Delete(&tables.TableProxyPoolBinding{}).Error
}

// proxyPoolFromTable rebuilds a schemas.ProxyPool from its row and proxy rows.
func proxyPoolFromTable(row tables.TableProxyPool, proxies []tables.TablePoolProxy) schemas.ProxyPool {
	out := schemas.ProxyPool{ID: row.ID, Name: row.Name, Description: row.Description}
	if len(proxies) == 0 {
		out.Proxies = []schemas.PoolProxy{}
		return out
	}
	out.Proxies = make([]schemas.PoolProxy, 0, len(proxies))
	for i := range proxies {
		out.Proxies = append(out.Proxies, poolProxyFromTable(proxies[i]))
	}
	return out
}

// poolProxyFromTable rebuilds a schemas.PoolProxy from its row. The password
// decrypts on read via after-find and stays in the in-memory value only; it is
// never serialized into API output.
func poolProxyFromTable(row tables.TablePoolProxy) schemas.PoolProxy {
	return schemas.PoolProxy{
		ID:           row.ID,
		PoolID:       row.PoolID,
		Type:         row.Type,
		URL:          row.URL,
		Username:     row.Username,
		Password:     row.Password,
		Weight:       row.Weight,
		Enabled:      row.Enabled,
		Status:       row.Status,
		LatencyMs:    row.LatencyMs,
		EgressIP:     row.EgressIP,
		LastTestedAt: row.LastTestedAt,
	}
}
