package auth

import (
	"errors"
	"log/slog"

	"github.com/cragone/Super-Argus/internal/models"
	"gorm.io/gorm"
)

// SeedAdmin creates the first admin user when no users exist and credentials are provided.
func SeedAdmin(db *gorm.DB, email, password string, log *slog.Logger) error {
	var n int64
	if err := db.Model(&models.User{}).Count(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	if email == "" || password == "" {
		log.Warn("no users exist and ARGUS_ADMIN_EMAIL / ARGUS_ADMIN_PASSWORD are not set")
		return nil
	}
	h, err := HashPassword(password)
	if err != nil {
		return err
	}
	u := models.User{Email: email, PasswordHash: h, Role: models.RoleAdmin}
	if err := db.Create(&u).Error; err != nil {
		return err
	}
	log.Info("seeded admin user", "email", email)
	return nil
}

// ErrInvalidKey is returned when an API key is unknown or revoked.
var ErrInvalidKey = errors.New("invalid api key")

// LookupAPIKey resolves a plaintext key to its record, rejecting revoked keys.
func LookupAPIKey(db *gorm.DB, key string) (*models.APIKey, error) {
	var k models.APIKey
	err := db.Where("key_hash = ? AND revoked_at IS NULL", HashAPIKey(key)).First(&k).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrInvalidKey
	}
	if err != nil {
		return nil, err
	}
	return &k, nil
}
