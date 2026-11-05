// Package auth provides password hashing, JWT sessions, and API key helpers.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/argon2"
)

const (
	argonTime    = 1
	argonMemory  = 64 * 1024
	argonThreads = 4
	argonKeyLen  = 32
)

// HashPassword returns an encoded argon2id hash.
func HashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	h := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(h)), nil
}

// VerifyPassword checks pw against an encoded argon2id hash in constant time.
func VerifyPassword(pw, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false
	}
	got := argon2.IDKey([]byte(pw), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// Token types.
const (
	TypeAccess  = "access"
	TypeRefresh = "refresh"
)

// Claims are the JWT claims used by Argus.
type Claims struct {
	Role string `json:"role"`
	Type string `json:"typ"`
	jwt.RegisteredClaims
}

// TokenManager issues and verifies session tokens. Now is injectable for tests.
type TokenManager struct {
	Secret     []byte
	AccessTTL  time.Duration
	RefreshTTL time.Duration
	Now        func() time.Time
}

// NewTokenManager returns a manager with default TTLs (15m access, 7d refresh).
func NewTokenManager(secret string) *TokenManager {
	return &TokenManager{Secret: []byte(secret), AccessTTL: 15 * time.Minute, RefreshTTL: 7 * 24 * time.Hour, Now: time.Now}
}

func (m *TokenManager) issue(userID, role, typ string, ttl time.Duration) (string, error) {
	now := m.Now()
	c := Claims{Role: role, Type: typ, RegisteredClaims: jwt.RegisteredClaims{
		Subject: userID, IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
	}}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(m.Secret)
}

// IssuePair returns an access and refresh token.
func (m *TokenManager) IssuePair(userID, role string) (access, refresh string, err error) {
	if access, err = m.issue(userID, role, TypeAccess, m.AccessTTL); err != nil {
		return
	}
	refresh, err = m.issue(userID, role, TypeRefresh, m.RefreshTTL)
	return
}

// Parse validates a token of the wanted type.
func (m *TokenManager) Parse(tok, wantType string) (*Claims, error) {
	c := &Claims{}
	_, err := jwt.ParseWithClaims(tok, c, func(*jwt.Token) (any, error) { return m.Secret, nil },
		jwt.WithValidMethods([]string{"HS256"}), jwt.WithTimeFunc(m.Now), jwt.WithExpirationRequired())
	if err != nil {
		return nil, err
	}
	if c.Type != wantType {
		return nil, errors.New("wrong token type")
	}
	return c, nil
}

const keyPrefix = "argus_"

// GenerateAPIKey returns the plaintext key (show once), its display prefix, and its hash.
func GenerateAPIKey() (key, prefix, hash string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return
	}
	key = keyPrefix + hex.EncodeToString(b)
	return key, key[:len(keyPrefix)+8], HashAPIKey(key), nil
}

// HashAPIKey hashes a key with SHA-256 (keys are high-entropy, so a fast hash is appropriate).
func HashAPIKey(key string) string {
	s := sha256.Sum256([]byte(key))
	return hex.EncodeToString(s[:])
}
