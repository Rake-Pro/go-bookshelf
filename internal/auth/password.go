package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// ArgonParams are the argon2id cost parameters used for new hashes. Stored
// hashes carry their own parameters, so these can change without invalidating
// existing passwords.
type ArgonParams struct {
	Time    uint32
	Memory  uint32 // KiB
	Threads uint8
	KeyLen  uint32
	SaltLen int
}

// DefaultArgonParams is the cost applied to newly hashed passwords.
var DefaultArgonParams = ArgonParams{Time: 3, Memory: 64 * 1024, Threads: 4, KeyLen: 32, SaltLen: 16}

// ErrBadHash is returned when a stored hash cannot be parsed.
var ErrBadHash = errors.New("auth: malformed password hash")

// MinPasswordLength is the shortest password accepted for a local account.
const MinPasswordLength = 10

// HashPassword returns a PHC-formatted argon2id hash of password.
func HashPassword(password string, p ArgonParams) (string, error) {
	salt := make([]byte, p.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, p.KeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.Memory, p.Time, p.Threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword reports whether password matches the stored PHC hash. It is
// constant-time with respect to the derived key.
func VerifyPassword(password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, ErrBadHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, ErrBadHash
	}
	var p ArgonParams
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Time, &p.Threads); err != nil {
		return false, ErrBadHash
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, ErrBadHash
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, ErrBadHash
	}
	got := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// dummyHash is verified against when a login names an account that does not
// exist, so a missing user and a wrong password take comparable time.
var dummyHash string

func init() {
	h, err := HashPassword("go-bookshelf-timing-equaliser", ArgonParams{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32, SaltLen: 16})
	if err != nil {
		panic(err)
	}
	dummyHash = h
}
