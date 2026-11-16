package tables

import (
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/encrypt"
	"gorm.io/gorm"
)

// TableProxyPool is a reusable set of proxies bound to upstream keys. It carries
// no provider or key identity: pools are assets any key can bind to via
// TableProxyPoolBinding.
type TableProxyPool struct {
	ID          string    `gorm:"primaryKey;type:varchar(255)" json:"id"`
	Name        string    `gorm:"type:varchar(255);not null" json:"name"`
	Description string    `gorm:"type:text" json:"description,omitempty"`
	CreatedAt   time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt   time.Time `gorm:"index;not null" json:"updated_at"`

	// Proxies exists to declare the cascade constraint and is never serialized
	// in API responses.
	Proxies []TablePoolProxy `gorm:"foreignKey:PoolID;constraint:OnDelete:CASCADE" json:"-"`
}

// TableName pins the physical table so schema changes never depend on GORM's
// pluralization of the Go type name.
func (TableProxyPool) TableName() string { return "proxy_pools" }

// TablePoolProxy is one proxy endpoint inside a pool. Selection state
// (status, latency_ms, egress_ip, last_tested_at, consecutive_failures,
// cooldown_until) is written by the probe and by the manager's failure
// accounting; enabled=false removes the proxy from every selection mechanism.
type TablePoolProxy struct {
	ID                  string             `gorm:"primaryKey;type:varchar(255)" json:"id"`
	PoolID              string             `gorm:"type:varchar(255);index;not null" json:"pool_id"`
	Type                schemas.ProxyType  `gorm:"type:varchar(20);not null" json:"type"`
	URL                 string             `gorm:"type:text;not null" json:"url"`
	Username            string             `gorm:"type:varchar(255)" json:"username,omitempty"`
	Password            *schemas.SecretVar `gorm:"type:text" json:"-"`
	Weight              float64            `gorm:"default:1" json:"weight"`
	Enabled             bool               `gorm:"default:true" json:"enabled"`
	Status              string             `gorm:"type:varchar(20);default:'unknown'" json:"status"`
	LatencyMs           int64              `json:"latency_ms"`
	EgressIP            string             `gorm:"type:varchar(255)" json:"egress_ip,omitempty"`
	LastTestedAt        *time.Time         `json:"last_tested_at,omitempty"`
	ConsecutiveFailures int                `gorm:"default:0" json:"consecutive_failures"`
	CooldownUntil       *time.Time         `json:"cooldown_until,omitempty"`
	EncryptionStatus    string             `gorm:"type:varchar(20);default:'plain_text'" json:"-"`

	CreatedAt time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"index;not null" json:"updated_at"`
}

// TableName pins the physical table name.
func (TablePoolProxy) TableName() string { return "proxy_pool_proxies" }

// BeforeSave encrypts a literal proxy password so it is never stored in
// plaintext when encryption is enabled. An env- or vault-backed password holds
// only a reference and is left alone, and the status column records the outcome
// so reads know how to interpret the row.
func (p *TablePoolProxy) BeforeSave(*gorm.DB) error {
	if encrypt.IsEnabled() && p.Password.IsSet() {
		if err := encryptSecretVar(p.Password); err != nil {
			return err
		}
		p.EncryptionStatus = EncryptionStatusEncrypted
	}
	return nil
}

// AfterFind decrypts a proxy password only for rows marked encrypted, so rows
// written before encryption was enabled continue to load unchanged.
func (p *TablePoolProxy) AfterFind(*gorm.DB) error {
	if p.EncryptionStatus == EncryptionStatusEncrypted && p.Password != nil && !p.Password.IsFromSecret() && p.Password.Val != "" {
		if err := decryptString(&p.Password.Val); err != nil {
			return err
		}
	}
	return nil
}

// TableProxyPoolBinding binds a pool and a selection mechanism to one upstream
// provider key. The composite primary key makes a bind naturally idempotent, and
// ProviderKeyID is the upstream schemas.Key.ID string — held across config
// reloads by the existing key-continuity logic — never the storage-local numeric
// TableKey.ID.
type TableProxyPoolBinding struct {
	PoolID        string                 `gorm:"primaryKey;type:varchar(255);not null" json:"pool_id"`
	ProviderKeyID string                 `gorm:"column:provider_key_id;primaryKey;type:varchar(255);not null;index:idx_proxy_pool_bindings_key" json:"provider_key_id"`
	Mechanism     schemas.ProxyMechanism `gorm:"type:varchar(20);not null" json:"mechanism"`
	CreatedAt     time.Time              `gorm:"not null" json:"created_at"`

	// Pool exists to declare the cascade constraint and is never serialized in
	// API responses.
	Pool *TableProxyPool `gorm:"foreignKey:PoolID;references:ID;constraint:OnDelete:CASCADE" json:"-"`
}

// TableName pins the physical table name.
func (TableProxyPoolBinding) TableName() string { return "proxy_pool_bindings" }
