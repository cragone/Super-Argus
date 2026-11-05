package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPasswordHashing(t *testing.T) {
	h, err := HashPassword("s3cret")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(h, "$argon2id$"))
	require.True(t, VerifyPassword("s3cret", h))
	require.False(t, VerifyPassword("wrong", h))
	require.False(t, VerifyPassword("s3cret", "garbage"))
	h2, _ := HashPassword("s3cret")
	require.NotEqual(t, h, h2, "salts must differ")
}

func TestTokens(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m := NewTokenManager("secret")
	m.Now = func() time.Time { return now }
	acc, ref, err := m.IssuePair("u1", "admin")
	require.NoError(t, err)

	c, err := m.Parse(acc, TypeAccess)
	require.NoError(t, err)
	require.Equal(t, "u1", c.Subject)
	require.Equal(t, "admin", c.Role)

	_, err = m.Parse(ref, TypeAccess)
	require.Error(t, err, "refresh token must not work as access")
	_, err = m.Parse(acc, TypeRefresh)
	require.Error(t, err)

	now = now.Add(time.Hour)
	_, err = m.Parse(acc, TypeAccess)
	require.Error(t, err, "access token should expire")
	_, err = m.Parse(ref, TypeRefresh)
	require.NoError(t, err)

	other := NewTokenManager("other")
	other.Now = m.Now
	_, err = other.Parse(ref, TypeRefresh)
	require.Error(t, err, "wrong secret")
}

func TestAPIKey(t *testing.T) {
	k, p, h, err := GenerateAPIKey()
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(k, "argus_"))
	require.True(t, strings.HasPrefix(k, p))
	require.Equal(t, h, HashAPIKey(k))
	require.NotContains(t, h, k)
}
