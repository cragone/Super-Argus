// Package models holds GORM models. Schema is managed by goose migrations only.
package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Roles in ascending privilege order.
const (
	RoleViewer = "viewer"
	RoleEditor = "editor"
	RoleAdmin  = "admin"
)

// ValidRole reports whether r is a known role.
func ValidRole(r string) bool { return RoleRank(r) > 0 }

// RoleRank returns a comparable privilege level (0 for unknown).
func RoleRank(r string) int {
	switch r {
	case RoleViewer:
		return 1
	case RoleEditor:
		return 2
	case RoleAdmin:
		return 3
	}
	return 0
}

// User is a UI account.
type User struct {
	ID           string `gorm:"primaryKey"`
	Email        string
	PasswordHash string
	Role         string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// BeforeCreate assigns a UUID if none is set.
func (u *User) BeforeCreate(*gorm.DB) error {
	if u.ID == "" {
		u.ID = uuid.NewString()
	}
	return nil
}

// APIKey authenticates ingest clients. Only the hash is stored.
type APIKey struct {
	ID            string `gorm:"primaryKey"`
	Name          string
	KeyHash       string
	KeyPrefix     string
	Label         string
	AllowedLabels string
	RateLimit     int
	LastUsedAt    *time.Time
	RevokedAt     *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// BeforeCreate assigns a UUID if none is set.
func (k *APIKey) BeforeCreate(*gorm.DB) error {
	if k.ID == "" {
		k.ID = uuid.NewString()
	}
	return nil
}

// Setting is a key/value runtime setting.
type Setting struct {
	Key       string `gorm:"primaryKey"`
	Value     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Stream is a unique label set. ID is a 63-bit hash of LabelsText.
type Stream struct {
	ID         uint64 `gorm:"primaryKey"`
	Labels     string
	LabelsText string
	APIKeyID   string `gorm:"column:api_key_id"`
	FirstSeen  time.Time
	LastSeen   time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// StreamLabel is one key/value pair of a stream, indexed for selector lookup.
type StreamLabel struct {
	StreamID  uint64 `gorm:"primaryKey;column:stream_id"`
	Key       string `gorm:"primaryKey"`
	Value     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Chunk is the index row for one chunk file. Timestamps are unix nanoseconds.
type Chunk struct {
	ID              string `gorm:"primaryKey"`
	StreamID        uint64 `gorm:"column:stream_id"`
	Path            string
	MinTS           int64 `gorm:"column:min_ts"`
	MaxTS           int64 `gorm:"column:max_ts"`
	LineCount       int
	BytesCompressed int64
	BytesRaw        int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
}
