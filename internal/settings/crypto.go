package settings

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
)

// KeySize is the required GOBOOKSHELF_SECRETS_KEY length in bytes (AES-256).
const KeySize = 32

// ErrKeyLength is returned by ParseKey when the decoded key is not KeySize
// bytes. Startup treats it as fatal: a wrong-length key would make every
// stored secret undecryptable while looking like a working configuration.
var ErrKeyLength = fmt.Errorf("secrets key must decode to exactly %d bytes", KeySize)

// ErrKeyMissing is returned by ParseKey for an empty value.
var ErrKeyMissing = errors.New("secrets key is not set")

// ParseKey decodes a base64 secrets key into raw AES key bytes. Both standard
// and raw (unpadded) base64 are accepted, so a key generated with either
// `openssl rand -base64 32` or `head -c32 /dev/urandom | base64` works.
func ParseKey(s string) ([]byte, error) {
	if s == "" {
		return nil, ErrKeyMissing
	}
	key, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		key, err = base64.RawStdEncoding.DecodeString(s)
	}
	if err != nil {
		return nil, fmt.Errorf("decode secrets key: %w", err)
	}
	if len(key) != KeySize {
		return nil, fmt.Errorf("%w (got %d)", ErrKeyLength, len(key))
	}
	return key, nil
}

// Encrypt seals plaintext with AES-GCM under key and returns base64 of
// nonce||ciphertext. The fresh nonce per write is what makes storing the same
// secret twice produce different ciphertext.
func Encrypt(key []byte, plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("secrets nonce: %w", err)
	}
	return base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, []byte(plaintext), nil)), nil
}

// Decrypt reverses Encrypt. A value that fails to decode or authenticate is an
// error rather than a silent empty string: it means the key changed, and
// pretending the secret is simply unset would quietly disable sign-in instead
// of saying why.
func Decrypt(key []byte, stored string) (string, error) {
	if stored == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(stored)
	if err != nil {
		return "", fmt.Errorf("decode secret: %w", err)
	}
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("stored secret is truncated")
	}
	plaintext, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
	if err != nil {
		return "", fmt.Errorf("decrypt secret (wrong GOBOOKSHELF_SECRETS_KEY?): %w", err)
	}
	return string(plaintext), nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secrets cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secrets gcm: %w", err)
	}
	return gcm, nil
}
