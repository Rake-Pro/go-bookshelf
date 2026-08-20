// Package oidctest provides the smallest identity provider that a real OpenID
// Connect client will talk to: a discovery document, a JWKS, and a token
// endpoint that mints an RS256 id_token carrying whatever claims a test asked
// for.
//
// It exists so sign-in can be exercised through the real path - discovery, code
// exchange, signature and nonce verification, claim reading - rather than
// against internal helpers that could drift from it. Nothing outside tests
// imports this package, so it never reaches a built binary.
package oidctest

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// Identity is what the next id_token will claim.
type Identity struct {
	Subject     string
	Username    string
	DisplayName string
	Groups      []string

	// Nonce must match the one the client put in its state cookie, or
	// verification fails - which is exactly what a test of that check wants.
	Nonce string
}

// Provider is a running fake identity provider.
type Provider struct {
	server   *httptest.Server
	key      *rsa.PrivateKey
	clientID string

	mu       sync.Mutex
	identity Identity
}

// New starts a provider for clientID and stops it when the test ends.
func New(t testing.TB, clientID string) *Provider {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("oidctest: rsa key: %v", err)
	}
	p := &Provider{key: key, clientID: clientID}

	mux := http.NewServeMux()
	p.server = httptest.NewServer(mux)
	t.Cleanup(p.server.Close)

	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"issuer":                                p.server.URL,
			"authorization_endpoint":                p.server.URL + "/authorize",
			"token_endpoint":                        p.server.URL + "/token",
			"jwks_uri":                              p.server.URL + "/jwks",
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, r *http.Request) {
		pub := key.Public().(*rsa.PublicKey)
		writeJSON(w, map[string]any{"keys": []map[string]any{{
			"kty": "RSA", "kid": "test", "alg": "RS256", "use": "sig",
			"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString([]byte{1, 0, 1}),
		}}})
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		token, err := p.idToken()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{
			"access_token": "access-token",
			"token_type":   "Bearer",
			"expires_in":   3600,
			"id_token":     token,
		})
	})
	return p
}

// URL is the issuer, and the value to store as the OIDC issuer setting.
func (p *Provider) URL() string { return p.server.URL }

// Issue sets the identity the next token endpoint call will vouch for.
func (p *Provider) Issue(id Identity) {
	p.mu.Lock()
	p.identity = id
	p.mu.Unlock()
}

// idToken mints a signed token for the current identity.
func (p *Provider) idToken() (string, error) {
	p.mu.Lock()
	id := p.identity
	p.mu.Unlock()

	claims := map[string]any{
		"iss":                p.server.URL,
		"aud":                p.clientID,
		"sub":                id.Subject,
		"exp":                time.Now().Add(time.Hour).Unix(),
		"iat":                time.Now().Unix(),
		"nonce":              id.Nonce,
		"preferred_username": id.Username,
		"name":               id.DisplayName,
	}
	// Absent rather than empty: a provider that sends no groups at all is what
	// a membership check has to cope with.
	if id.Groups != nil {
		claims["groups"] = id.Groups
	}

	enc := func(v any) (string, error) {
		raw, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		return base64.RawURLEncoding.EncodeToString(raw), nil
	}
	header, err := enc(map[string]any{"alg": "RS256", "typ": "JWT", "kid": "test"})
	if err != nil {
		return "", err
	}
	payload, err := enc(claims)
	if err != nil {
		return "", err
	}
	signing := header + "." + payload
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, p.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}
