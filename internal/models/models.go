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
