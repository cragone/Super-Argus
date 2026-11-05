package auth

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/cragone/Super-Argus/internal/db"
	"github.com/cragone/Super-Argus/internal/models"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	g, err := db.Open("sqlite:" + filepath.Join(t.TempDir(), "t.db"))
	require.NoError(t, err)
	sqlDB, _ := g.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.Migrate(g))
	return g
}

func TestSeedAdmin(t *testing.T) {
	g := testDB(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	require.NoError(t, SeedAdmin(g, "", "", log))
	var n int64
	g.Model(&models.User{}).Count(&n)
	require.Zero(t, n)

	require.NoError(t, SeedAdmin(g, "a@x.io", "pw", log))
	require.NoError(t, SeedAdmin(g, "b@x.io", "pw", log)) // no-op, users exist
	var us []models.User
	g.Find(&us)
	require.Len(t, us, 1)
	require.Equal(t, models.RoleAdmin, us[0].Role)
	require.True(t, VerifyPassword("pw", us[0].PasswordHash))
}

func TestLookupAPIKey(t *testing.T) {
	g := testDB(t)
	key, prefix, hash, _ := GenerateAPIKey()
	k := models.APIKey{Name: "n", KeyHash: hash, KeyPrefix: prefix, AllowedLabels: "{}"}
	require.NoError(t, g.Create(&k).Error)

	got, err := LookupAPIKey(g, key)
	require.NoError(t, err)
	require.Equal(t, k.ID, got.ID)

	_, err = LookupAPIKey(g, "argus_nope")
	require.ErrorIs(t, err, ErrInvalidKey)

	require.NoError(t, g.Model(&k).Update("revoked_at", gorm.Expr("CURRENT_TIMESTAMP")).Error)
	_, err = LookupAPIKey(g, key)
	require.ErrorIs(t, err, ErrInvalidKey)
}
