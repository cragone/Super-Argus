package notify

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
)

// Sealer encrypts channel secrets at rest with AES-256-GCM.
type Sealer struct{ aead cipher.AEAD }

// NewSealer derives an encryption key from the server secret.
func NewSealer(secret string) *Sealer {
	key := sha256.Sum256([]byte("argus-channel-secrets|" + secret))
	block, _ := aes.NewCipher(key[:]) // key length is fixed, cannot fail
	aead, _ := cipher.NewGCM(block)
	return &Sealer{aead}
}

// Seal returns base64(nonce||ciphertext).
func (s *Sealer) Seal(plain string) (string, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(s.aead.Seal(nonce, nonce, []byte(plain), nil)), nil
}

// Open reverses Seal.
func (s *Sealer) Open(sealed string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return "", err
	}
	n := s.aead.NonceSize()
	if len(raw) < n {
		return "", errors.New("sealed value too short")
	}
	out, err := s.aead.Open(nil, raw[:n], raw[n:], nil)
	if err != nil {
		return "", err
	}
	return string(out), nil
}
