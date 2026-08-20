package settings_test

import (
	"context"
	"encoding/base64"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/rake-pro/go-bookshelf/internal/settings"
	"github.com/rake-pro/go-bookshelf/internal/store"
	"github.com/rake-pro/go-bookshelf/internal/storetest"
)

func key(fill byte) []byte {
	k := make([]byte, settings.KeySize)
	for i := range k {
		k[i] = fill
	}
	return k
}

func newDB(t *testing.T) (*store.DB, context.Context) {
	t.Helper()
	ctx := context.Background()
	return storetest.Open(t), ctx
}

func TestParseKey(t *testing.T) {
	raw := key(7)
	for _, encoded := range []string{
		base64.StdEncoding.EncodeToString(raw),
		base64.RawStdEncoding.EncodeToString(raw),
	} {
		got, err := settings.ParseKey(encoded)
		if err != nil {
			t.Fatalf("ParseKey(%q): %v", encoded, err)
		}
		if string(got) != string(raw) {
			t.Errorf("ParseKey(%q) round trip mismatch", encoded)
		}
	}
	if _, err := settings.ParseKey(""); !errors.Is(err, settings.ErrKeyMissing) {
		t.Errorf("empty key = %v, want ErrKeyMissing", err)
	}
	short := base64.StdEncoding.EncodeToString([]byte("short"))
	if _, err := settings.ParseKey(short); !errors.Is(err, settings.ErrKeyLength) {
		t.Errorf("short key = %v, want ErrKeyLength", err)
	}
	if _, err := settings.ParseKey("not base64 at all!!"); err == nil {
		t.Error("a value that is not base64 must be refused")
	}
}

func TestCryptoRoundTrip(t *testing.T) {
	k := key(3)
	sealed, err := settings.Encrypt(k, "s3cret-value")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if strings.Contains(sealed, "s3cret-value") {
		t.Fatal("the ciphertext contains the plaintext")
	}
	plain, err := settings.Decrypt(k, sealed)
	if err != nil || plain != "s3cret-value" {
		t.Fatalf("decrypt = %q, %v", plain, err)
	}

	// A second encryption of the same value must differ: the nonce is fresh
	// per write, so two rows holding the same secret do not look the same.
	again, err := settings.Encrypt(k, "s3cret-value")
	if err != nil {
		t.Fatal(err)
	}
	if again == sealed {
		t.Fatal("two encryptions of the same value are identical; the nonce is not random")
	}

	// The empty string stays empty rather than becoming a ciphertext of
	// nothing, so "no secret stored" is distinguishable.
	if got, err := settings.Encrypt(k, ""); err != nil || got != "" {
		t.Errorf("encrypt empty = %q, %v", got, err)
	}
}

func TestCryptoWrongKeyFails(t *testing.T) {
	sealed, err := settings.Encrypt(key(3), "s3cret-value")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := settings.Decrypt(key(4), sealed); err == nil {
		t.Fatal("decrypting with the wrong key must fail, not return an empty string")
	}
	if _, err := settings.Decrypt(key(3), "not-base64!!"); err == nil {
		t.Error("a corrupt stored value must fail")
	}
	if _, err := settings.Decrypt(key(3), base64.StdEncoding.EncodeToString([]byte("tiny"))); err == nil {
		t.Error("a truncated stored value must fail")
	}
}

func TestValidation(t *testing.T) {
	valid := func() settings.Settings {
		s := settings.Default()
		s.General.BaseURL = "https://books.example.com"
		return s
	}

	base := valid()
	base.Normalize()
	if err := base.Validate(); err != nil {
		t.Fatalf("the default document must be valid: %v", err)
	}

	cases := []struct {
		name  string
		mut   func(*settings.Settings)
		wants string
	}{
		{"base URL without a scheme", func(s *settings.Settings) {
			s.General.BaseURL = "books.example.com"
		}, "absolute"},
		{"base URL with an unsupported scheme", func(s *settings.Settings) {
			s.General.BaseURL = "ftp://books.example.com"
		}, "http"},
		{"session lifetime too short", func(s *settings.Settings) {
			s.General.SessionTTL = settings.Duration(time.Second)
		}, "session lifetime"},
		{"negative scan interval", func(s *settings.Settings) {
			s.General.ScanInterval = settings.Duration(-time.Hour)
		}, "scan interval"},
		{"OIDC issuer that is not a URL", func(s *settings.Settings) {
			s.OIDC = settings.OIDC{Enabled: true, Issuer: "id.example.com",
				ClientID: "bookshelf", ClientSecret: "x", LocalLoginEnabled: true}
		}, "issuer"},
		{"OIDC without a client id", func(s *settings.Settings) {
			s.OIDC = settings.OIDC{Enabled: true, Issuer: "https://id.example.com",
				ClientSecret: "x", LocalLoginEnabled: true}
		}, "client id"},
		{"OIDC without a client secret", func(s *settings.Settings) {
			s.OIDC = settings.OIDC{Enabled: true, Issuer: "https://id.example.com",
				ClientID: "bookshelf", LocalLoginEnabled: true}
		}, "client secret"},
		{"password sign-in off with OIDC off", func(s *settings.Settings) {
			s.OIDC.LocalLoginEnabled = false
		}, "password sign-in"},
		{"proxy auth without a header", func(s *settings.Settings) {
			s.ProxyAuth = settings.ProxyAuth{Enabled: true, TrustedProxies: []string{"10.0.0.0/8"}}
		}, "header name"},
		{"proxy auth without trusted proxies", func(s *settings.Settings) {
			s.ProxyAuth = settings.ProxyAuth{Enabled: true, Header: "Remote-User"}
		}, "trusted proxy"},
		{"proxy auth with an unparseable CIDR", func(s *settings.Settings) {
			s.ProxyAuth = settings.ProxyAuth{Enabled: true, Header: "Remote-User",
				TrustedProxies: []string{"not-a-cidr"}}
		}, "trusted proxies"},
		{"unknown metadata provider", func(s *settings.Settings) {
			s.Metadata.Provider = "somewhere-else"
		}, "metadata provider"},
		{"unparseable metrics CIDR", func(s *settings.Settings) {
			s.Metrics.Allow = []string{"192.0.2.0/33"}
		}, "metrics allow list"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := valid()
			tc.mut(&s)
			s.Normalize()
			err := s.Validate()
			if err == nil {
				t.Fatalf("%s was accepted", tc.name)
			}
			if !errors.Is(err, settings.ErrInvalid) {
				t.Errorf("error = %v, want it to wrap ErrInvalid", err)
			}
			if !strings.Contains(err.Error(), tc.wants) {
				t.Errorf("error = %q, want it to mention %q", err, tc.wants)
			}
		})
	}
}

// The issuer is compared byte for byte by the verifier, so a trailing slash is
// meaningful and must survive storage exactly as typed.
func TestIssuerStoredVerbatim(t *testing.T) {
	db, ctx := newDB(t)
	svc, err := settings.New(ctx, db, key(9), false)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	next := svc.Get()
	next.OIDC = settings.OIDC{
		Enabled: false, Issuer: "  https://id.example.com/application/o/books/  ",
		ClientID: "bookshelf", GroupsClaim: "groups", LocalLoginEnabled: true,
	}
	if err := svc.Save(ctx, next); err != nil {
		t.Fatalf("save: %v", err)
	}
	if got := svc.Get().OIDC.Issuer; got != "https://id.example.com/application/o/books/" {
		t.Errorf("stored issuer = %q, want the trailing slash kept and only the outer space trimmed", got)
	}
}

func TestNormalizeDefaults(t *testing.T) {
	s := settings.Settings{General: settings.General{BaseURL: "https://books.example.com/"}}
	s.Normalize()
	if s.General.BaseURL != "https://books.example.com" {
		t.Errorf("base URL = %q, want the trailing slash removed", s.General.BaseURL)
	}
	if s.General.SecureCookies != settings.CookiesAuto {
		t.Errorf("secure cookies = %q, want auto", s.General.SecureCookies)
	}
	if s.OIDC.GroupsClaim != "groups" {
		t.Errorf("groups claim = %q", s.OIDC.GroupsClaim)
	}
	if s.Metadata.Provider != settings.ProviderNone {
		t.Errorf("metadata provider = %q", s.Metadata.Provider)
	}
	if len(s.Metrics.Allow) == 0 {
		t.Error("an empty metrics allow list must fall back to the defaults")
	}
	if !s.SecureCookies() {
		t.Error("auto cookies over https must resolve to Secure")
	}
	s.General.BaseURL = "http://localhost:8080"
	if s.SecureCookies() {
		t.Error("auto cookies over http must not be Secure")
	}
	s.General.SecureCookies = settings.CookiesOn
	if !s.SecureCookies() {
		t.Error("an explicit on must win over the scheme")
	}
}

func TestSaveRoundTripAndSecretAtRest(t *testing.T) {
	db, ctx := newDB(t)
	k := key(11)
	svc, err := settings.New(ctx, db, k, false)
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	next := svc.Get()
	next.General.BaseURL = "https://books.example.com"
	next.General.SessionTTL = settings.Duration(2 * time.Hour)
	next.OIDC = settings.OIDC{
		Issuer: "https://id.example.com", ClientID: "bookshelf",
		ClientSecret: "top-secret-value", GroupsClaim: "groups", LocalLoginEnabled: true,
	}
	next.ProxyAuth = settings.ProxyAuth{Enabled: true, Header: "Remote-User",
		TrustedProxies: []string{"192.0.2.0/24"}}
	if err := svc.Save(ctx, next); err != nil {
		t.Fatalf("save: %v", err)
	}

	// The stored row must not contain the secret in clear.
	var raw string
	if err := db.QueryRowContext(ctx, `SELECT data FROM settings WHERE id = 1`).Scan(&raw); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if strings.Contains(raw, "top-secret-value") {
		t.Fatal("the client secret is stored in clear")
	}

	// A fresh service over the same database and key sees it again.
	reopened, err := settings.New(ctx, db, k, false)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got := reopened.Get()
	if got.OIDC.ClientSecret != "top-secret-value" {
		t.Errorf("client secret after reload = %q", got.OIDC.ClientSecret)
	}
	if got.General.SessionTTL.D() != 2*time.Hour {
		t.Errorf("session ttl after reload = %s", got.General.SessionTTL)
	}
	if !settings.IPInNets(net.ParseIP("192.0.2.9"), reopened.TrustedProxyNets()) {
		t.Error("the parsed trusted proxy set did not survive a reload")
	}

	// The wrong key is an error, not an empty secret.
	if _, err := settings.New(ctx, db, key(12), false); err == nil {
		t.Fatal("opening the settings with the wrong key must fail loudly")
	}

	if got.Redacted().OIDC.ClientSecret != "" {
		t.Error("Redacted must blank the client secret")
	}
	if !got.HasClientSecret() {
		t.Error("HasClientSecret must report a stored secret")
	}
}

func TestSaveRejectsInvalidWithoutPersisting(t *testing.T) {
	db, ctx := newDB(t)
	svc, err := settings.New(ctx, db, key(13), false)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	before := svc.Get()

	bad := before
	bad.General.BaseURL = "nonsense"
	if err := svc.Save(ctx, bad); !errors.Is(err, settings.ErrInvalid) {
		t.Fatalf("save invalid = %v, want ErrInvalid", err)
	}
	if svc.Get().General.BaseURL != before.General.BaseURL {
		t.Error("a rejected save changed the live configuration")
	}
}

// A preparation failure must leave both the stored document and the running
// configuration untouched: that is what makes a bad issuer a rejected save
// rather than a server that stored something it cannot use.
func TestSaveRollsBackOnApplierFailure(t *testing.T) {
	db, ctx := newDB(t)
	svc, err := settings.New(ctx, db, key(14), false)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	applied := 0
	svc.Register(applierFunc(func(context.Context, settings.Settings) (func(), error) {
		return func() { applied++ }, errors.New("discovery failed")
	}))

	next := svc.Get()
	next.General.BaseURL = "https://new.example.com"
	if err := svc.Save(ctx, next); err == nil {
		t.Fatal("a failing applier must fail the save")
	}
	if applied != 0 {
		t.Error("a failing applier's apply function was called anyway")
	}
	if svc.Get().General.BaseURL == "https://new.example.com" {
		t.Error("a rejected save was applied to the live configuration")
	}
	var raw string
	if err := db.QueryRowContext(ctx, `SELECT data FROM settings WHERE id = 1`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "new.example.com") {
		t.Error("a rejected save was persisted")
	}
}

func TestSetupCompleteAndUpgradeSeed(t *testing.T) {
	db, ctx := newDB(t)
	svc, err := settings.New(ctx, db, key(15), false)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if svc.SetupComplete() {
		t.Fatal("a fresh database must start with setup pending")
	}
	if err := svc.MarkSetupComplete(ctx); err != nil {
		t.Fatalf("mark complete: %v", err)
	}
	if !svc.SetupComplete() {
		t.Fatal("marking setup complete did not stick")
	}
	// A save must not quietly reopen the wizard.
	next := svc.Get()
	next.SetupComplete = false
	if err := svc.Save(ctx, next); err != nil {
		t.Fatalf("save: %v", err)
	}
	if !svc.SetupComplete() {
		t.Error("a settings save reopened first-run setup")
	}

	// An upgraded database, which already has accounts, starts complete.
	other, ctx2 := newDB(t)
	upgraded, err := settings.New(ctx2, other, key(15), true)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if !upgraded.SetupComplete() {
		t.Error("a database that already has accounts must not be sent back through the wizard")
	}
}

func TestSplitListAndCIDRs(t *testing.T) {
	got := settings.SplitList(" 10.0.0.0/8 , 192.0.2.5 \n\n 172.16.0.0/12 ")
	if len(got) != 3 || got[1] != "192.0.2.5" {
		t.Fatalf("SplitList = %#v", got)
	}
	nets, err := settings.ParseCIDRs(got)
	if err != nil {
		t.Fatalf("ParseCIDRs: %v", err)
	}
	if !settings.IPInNets(net.ParseIP("10.1.2.3"), nets) {
		t.Error("10.1.2.3 should be inside 10.0.0.0/8")
	}
	if !settings.IPInNets(net.ParseIP("192.0.2.5"), nets) {
		t.Error("a bare address should be treated as a single-host network")
	}
	if settings.IPInNets(net.ParseIP("192.0.2.6"), nets) {
		t.Error("192.0.2.6 must not match the single-host entry")
	}
	if _, err := settings.ParseCIDRs([]string{"nope"}); err == nil {
		t.Error("an unparseable entry must be refused")
	}
}

type applierFunc func(context.Context, settings.Settings) (func(), error)

func (f applierFunc) Prepare(ctx context.Context, s settings.Settings) (func(), error) {
	return f(ctx, s)
}
