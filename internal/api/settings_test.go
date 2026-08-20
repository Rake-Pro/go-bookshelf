package api_test

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rake-pro/go-bookshelf/internal/oidctest"
	"github.com/rake-pro/go-bookshelf/internal/settings"
)

// The wizard's own path, step by step, including the two steps that may be
// skipped and the library step that insists the path exists.
func TestSetupWizard(t *testing.T) {
	h := newHarness(t)

	token, err := h.auth.EnsureSetupToken(h.ctx)
	if err != nil {
		t.Fatalf("setup token: %v", err)
	}

	t.Run("the token step refuses the wrong token", func(t *testing.T) {
		rec := h.do(http.MethodPost, "/api/v1/setup/token", map[string]string{"token": "nonsense"})
		if rec.Code != http.StatusForbidden {
			t.Fatalf("wrong token = %d, want 403", rec.Code)
		}
		// And it must not have spent the real one.
		ok := h.do(http.MethodPost, "/api/v1/setup/token", map[string]string{"token": token})
		if ok.Code != http.StatusOK {
			t.Fatalf("the correct token was rejected after a wrong attempt: %d", ok.Code)
		}
	})

	t.Run("the token step suggests a base URL from the request", func(t *testing.T) {
		rec := h.do(http.MethodPost, "/api/v1/setup/token", map[string]string{"token": token},
			func(r *http.Request) {
				r.Host = "internal:8080"
				r.Header.Set("X-Forwarded-Proto", "https")
				r.Header.Set("X-Forwarded-Host", "books.example.com")
			})
		var body struct {
			OK               bool   `json:"ok"`
			SuggestedBaseURL string `json:"suggested_base_url"`
		}
		decode(t, rec, &body)
		if !body.OK || body.SuggestedBaseURL != "https://books.example.com" {
			t.Errorf("token step = %s, want the forwarded host prefilled", rec.Body.String())
		}
	})

	t.Run("the later steps refuse an anonymous caller", func(t *testing.T) {
		for _, step := range []string{"base-url", "oidc", "library", "complete"} {
			rec := h.do(http.MethodPost, "/api/v1/setup/"+step, map[string]any{"skip": true})
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("anonymous setup/%s = %d, want 401", step, rec.Code)
			}
		}
	})

	// Until the wizard finishes, the ordinary API is closed.
	before := h.do(http.MethodGet, "/api/v1/items", nil)
	if before.Code != http.StatusUnauthorized {
		t.Errorf("anonymous items during setup = %d, want 401", before.Code)
	}

	admin := h.do(http.MethodPost, "/api/v1/setup/admin", map[string]string{
		"token": token, "username": "admin", "password": "correct-horse-battery", "display_name": "Admin",
	})
	if admin.Code != http.StatusOK {
		t.Fatalf("setup/admin = %d: %s", admin.Code, admin.Body.String())
	}
	sid := sessionFrom(t, admin)

	t.Run("an authenticated admin is still refused the ordinary API", func(t *testing.T) {
		rec := h.do(http.MethodGet, "/api/v1/items", nil, withCookie(sid))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("items before setup completes = %d, want 403", rec.Code)
		}
		var body map[string]map[string]string
		decode(t, rec, &body)
		if body["error"]["code"] != "setup_required" {
			t.Errorf("error code = %q, want setup_required", body["error"]["code"])
		}
	})

	t.Run("base URL is validated", func(t *testing.T) {
		bad := h.do(http.MethodPost, "/api/v1/setup/base-url",
			map[string]string{"base_url": "not-a-url"}, withCookie(sid))
		if bad.Code != http.StatusBadRequest {
			t.Fatalf("bad base URL = %d, want 400", bad.Code)
		}
		rec := h.do(http.MethodPost, "/api/v1/setup/base-url",
			map[string]string{"base_url": "https://books.example.com/"}, withCookie(sid))
		if rec.Code != http.StatusOK {
			t.Fatalf("base URL step = %d: %s", rec.Code, rec.Body.String())
		}
		var body struct {
			BaseURL     string `json:"base_url"`
			RedirectURL string `json:"redirect_url"`
		}
		decode(t, rec, &body)
		if body.BaseURL != "https://books.example.com" {
			t.Errorf("stored base URL = %q, want the trailing slash removed", body.BaseURL)
		}
		if body.RedirectURL != "https://books.example.com/api/v1/auth/oidc/callback" {
			t.Errorf("redirect URL = %q", body.RedirectURL)
		}
	})

	t.Run("OIDC can be skipped", func(t *testing.T) {
		rec := h.do(http.MethodPost, "/api/v1/setup/oidc", map[string]any{"skip": true}, withCookie(sid))
		if rec.Code != http.StatusOK {
			t.Fatalf("skipping OIDC = %d: %s", rec.Code, rec.Body.String())
		}
		if h.settings.Get().OIDC.Enabled {
			t.Error("skipping the OIDC step turned OIDC on")
		}
	})

	t.Run("the library path must exist", func(t *testing.T) {
		missing := filepath.Join(h.media, "definitely-not-here")
		rec := h.do(http.MethodPost, "/api/v1/setup/library", map[string]any{
			"name": "Books", "kind": "mixed", "path": missing,
		}, withCookie(sid))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("library on a missing path = %d, want 400", rec.Code)
		}

		// A file is not a directory either.
		file := filepath.Join(h.media, "a-file")
		if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if rec := h.do(http.MethodPost, "/api/v1/setup/library", map[string]any{
			"name": "Books", "kind": "mixed", "path": file,
		}, withCookie(sid)); rec.Code != http.StatusBadRequest {
			t.Errorf("library on a plain file = %d, want 400", rec.Code)
		}
	})

	t.Run("the library step creates a library", func(t *testing.T) {
		rec := h.do(http.MethodPost, "/api/v1/setup/library", map[string]any{
			"name": "Everything", "kind": "mixed", "path": h.media,
		}, withCookie(sid))
		if rec.Code != http.StatusCreated {
			t.Fatalf("library step = %d: %s", rec.Code, rec.Body.String())
		}
	})

	done := h.do(http.MethodPost, "/api/v1/setup/complete", map[string]any{}, withCookie(sid))
	if done.Code != http.StatusOK {
		t.Fatalf("complete = %d: %s", done.Code, done.Body.String())
	}

	// The gate is open and the library the wizard made is there.
	libs := h.do(http.MethodGet, "/api/v1/libraries", nil, withCookie(sid))
	if libs.Code != http.StatusOK {
		t.Fatalf("libraries after setup = %d: %s", libs.Code, libs.Body.String())
	}
	var list struct {
		Total int `json:"total"`
	}
	decode(t, libs, &list)
	if list.Total != 1 {
		t.Errorf("libraries = %d, want the one the wizard created", list.Total)
	}
}

// The whole wizard can be walked with nothing filled in but the account.
func TestSetupWizardSkipEverything(t *testing.T) {
	h := newHarness(t)
	sid := h.setupAdmin() // skips OIDC and the library

	if !h.settings.SetupComplete() {
		t.Fatal("the wizard did not complete")
	}
	rec := h.do(http.MethodGet, "/api/v1/libraries", nil, withCookie(sid))
	var list struct {
		Total int `json:"total"`
	}
	decode(t, rec, &list)
	if list.Total != 0 {
		t.Errorf("libraries = %d, want none after skipping the library step", list.Total)
	}
}

func TestAdminSettingsRequiresAdmin(t *testing.T) {
	h := newHarness(t)
	adminSID := h.setupAdmin()
	userSID := h.createUser(adminSID, "reader", nil)

	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/admin/settings"},
		{http.MethodPut, "/api/v1/admin/settings"},
		{http.MethodPost, "/api/v1/admin/settings/oidc/test"},
	} {
		rec := h.do(route.method, route.path, map[string]any{}, withCookie(userSID))
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s as a plain user = %d, want 403", route.method, route.path, rec.Code)
		}
		anon := h.do(route.method, route.path, map[string]any{})
		if anon.Code != http.StatusUnauthorized {
			t.Errorf("%s %s anonymously = %d, want 401", route.method, route.path, anon.Code)
		}
	}
}

// A saved setting takes effect on the running server, without a restart: the
// session lifetime is the one that is easiest to observe from the outside.
func TestAdminSettingsApplyLive(t *testing.T) {
	h := newHarness(t)
	sid := h.setupAdmin()

	if got := h.auth.SessionTTL(); got != settings.Default().General.SessionTTL.D() {
		t.Fatalf("session TTL before the edit = %s", got)
	}

	rec := h.do(http.MethodPut, "/api/v1/admin/settings", map[string]any{
		"general": map[string]any{"session_ttl": "3h", "scan_interval": "45m"},
	}, withCookie(sid))
	if rec.Code != http.StatusOK {
		t.Fatalf("put settings = %d: %s", rec.Code, rec.Body.String())
	}
	if got := h.auth.SessionTTL(); got != 3*time.Hour {
		t.Errorf("session TTL after the edit = %s, want 3h without a restart", got)
	}

	// And the next cookie carries it.
	login := h.do(http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "correct-horse-battery",
	})
	for _, c := range login.Result().Cookies() {
		if c.Name == "gbs_session" && c.MaxAge != int(3*time.Hour/time.Second) {
			t.Errorf("session cookie MaxAge = %d, want %d", c.MaxAge, int(3*time.Hour/time.Second))
		}
	}

	// The scan interval is stored in the same document and reported back.
	var body struct {
		General struct {
			ScanInterval string `json:"scan_interval"`
		} `json:"general"`
	}
	decode(t, rec, &body)
	if body.General.ScanInterval != "45m0s" {
		t.Errorf("scan_interval = %q", body.General.ScanInterval)
	}
}

// The client secret is write-only: it never comes back, an empty value keeps
// what is stored, and the stored row does not hold it in clear.
func TestAdminSettingsSecretHandling(t *testing.T) {
	h := newHarness(t)
	sid := h.setupAdmin()

	// Stored while OIDC is off, so no discovery is attempted.
	h.mutateSettings(func(s *settings.Settings) {
		s.OIDC.Issuer = "https://id.example.com"
		s.OIDC.ClientID = "bookshelf"
		s.OIDC.ClientSecret = "the-client-secret"
	})

	rec := h.do(http.MethodGet, "/api/v1/admin/settings", nil, withCookie(sid))
	if body := rec.Body.String(); strings.Contains(body, "the-client-secret") {
		t.Fatalf("GET /admin/settings leaked the client secret: %s", body)
	}
	var got struct {
		OIDC struct {
			HasClientSecret bool `json:"has_client_secret"`
		} `json:"oidc"`
	}
	decode(t, rec, &got)
	if !got.OIDC.HasClientSecret {
		t.Error("has_client_secret should be true once a secret is stored")
	}

	// Saving without a secret keeps the stored one.
	put := h.do(http.MethodPut, "/api/v1/admin/settings", map[string]any{
		"oidc": map[string]any{
			"enabled": false, "issuer": "https://id.example.com", "client_id": "bookshelf",
			"groups_claim": "groups", "client_secret": "",
		},
	}, withCookie(sid))
	if put.Code != http.StatusOK {
		t.Fatalf("put = %d: %s", put.Code, put.Body.String())
	}
	if h.settings.Get().OIDC.ClientSecret != "the-client-secret" {
		t.Error("an empty client_secret wiped the stored one instead of keeping it")
	}

	// The row on disk is ciphertext.
	var raw string
	if err := h.db.QueryRowContext(h.ctx, `SELECT data FROM settings WHERE id = 1`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "the-client-secret") {
		t.Error("the client secret is stored in clear")
	}
}

func TestAdminSettingsValidation(t *testing.T) {
	h := newHarness(t)
	sid := h.setupAdmin()

	cases := []struct {
		name string
		body map[string]any
	}{
		{"base URL that is not a URL", map[string]any{
			"general": map[string]any{"base_url": "books"},
		}},
		{"session lifetime that is not a duration", map[string]any{
			"general": map[string]any{"session_ttl": "a while"},
		}},
		{"proxy auth without trusted proxies", map[string]any{
			"proxy_auth": map[string]any{"enabled": true, "header": "Remote-User", "trusted_proxies": []string{}},
		}},
		{"unparseable trusted proxy", map[string]any{
			"proxy_auth": map[string]any{"enabled": true, "header": "Remote-User", "trusted_proxies": []string{"nope"}},
		}},
		{"unknown metadata provider", map[string]any{
			"metadata": map[string]any{"provider": "elsewhere"},
		}},
		{"unparseable metrics CIDR", map[string]any{
			"metrics": map[string]any{"allow": []string{"192.0.2.0/33"}},
		}},
		{"OIDC on without an issuer", map[string]any{
			"oidc": map[string]any{"enabled": true, "client_id": "bookshelf", "client_secret": "x"},
		}},
		{"password sign-in off while OIDC is off", map[string]any{
			"oidc": map[string]any{"enabled": false, "local_login_enabled": false},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := h.do(http.MethodPut, "/api/v1/admin/settings", tc.body, withCookie(sid))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s = %d, want 400: %s", tc.name, rec.Code, rec.Body.String())
			}
		})
	}

	// None of it landed.
	if h.settings.Get().General.BaseURL != "http://localhost:8080" {
		t.Errorf("a rejected save changed the base URL: %q", h.settings.Get().General.BaseURL)
	}
}

// An issuer that does not answer discovery fails the save, and nothing is
// stored: a server must not end up holding a configuration it cannot use.
func TestAdminSettingsRejectsUnreachableIssuer(t *testing.T) {
	h := newHarness(t)
	sid := h.setupAdmin()

	rec := h.do(http.MethodPut, "/api/v1/admin/settings", map[string]any{
		"oidc": map[string]any{
			"enabled": true, "issuer": "http://127.0.0.1:1/nowhere",
			"client_id": "bookshelf", "client_secret": "x", "groups_claim": "groups",
		},
	}, withCookie(sid))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unreachable issuer = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if h.settings.Get().OIDC.Enabled {
		t.Error("a rejected OIDC save was stored anyway")
	}

	// The test button reports the same failure without changing anything.
	probe := h.do(http.MethodPost, "/api/v1/admin/settings/oidc/test", map[string]any{
		"issuer": "http://127.0.0.1:1/nowhere", "client_id": "bookshelf",
		"client_secret": "x", "groups_claim": "groups",
	}, withCookie(sid))
	if probe.Code != http.StatusOK {
		t.Fatalf("oidc test = %d, want 200 carrying the verdict", probe.Code)
	}
	var verdict map[string]any
	decode(t, probe, &verdict)
	if verdict["ok"] != false || verdict["error"] == nil {
		t.Errorf("oidc test = %s, want ok:false with an error", probe.Body.String())
	}
}

// The password form can be turned off only when somebody could still get in
// through the identity provider.
func TestLocalLoginGuard(t *testing.T) {
	h := newHarness(t)
	sid := h.setupAdmin()

	// With OIDC off it is refused by validation.
	rec := h.do(http.MethodPut, "/api/v1/admin/settings", map[string]any{
		"oidc": map[string]any{"enabled": false, "local_login_enabled": false},
	}, withCookie(sid))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("turning the password form off with OIDC off = %d, want 400", rec.Code)
	}
	if !h.auth.LocalLoginEnabled() {
		t.Fatal("a rejected save turned the password form off anyway")
	}

	// Turning it off with an admin group configured is allowed: the group is
	// what would grant somebody the role on their first sign-in.
	idp := oidctest.New(t, "bookshelf")
	off := false
	oidc := map[string]any{
		"enabled": true, "issuer": idp.URL(), "client_id": "bookshelf",
		"client_secret": "client-secret", "groups_claim": "groups",
		"admin_group": "bookshelf-admins", "user_group": "bookshelf-users",
		"local_login_enabled": off,
	}
	if rec := h.do(http.MethodPut, "/api/v1/admin/settings",
		map[string]any{"oidc": oidc}, withCookie(sid)); rec.Code != http.StatusOK {
		t.Fatalf("turning the password form off with an admin group = %d: %s", rec.Code, rec.Body.String())
	}
	if h.auth.LocalLoginEnabled() {
		t.Fatal("the password form is still on after being turned off in the settings")
	}

	login := h.do(http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "correct-horse-battery",
	})
	if login.Code != http.StatusForbidden {
		t.Errorf("password login with the form off = %d, want 403", login.Code)
	}
	var status map[string]any
	decode(t, h.do(http.MethodGet, "/api/v1/auth/status", nil), &status)
	if status["local_login"] != false {
		t.Errorf("auth status local_login = %v, want false", status["local_login"])
	}
}

// GOBOOKSHELF_ADMIN_RECOVERY is the way back in: it forces the password form
// on regardless of what the stored settings say, and it cannot be set from
// inside the application.
func TestAdminRecoveryForcesLocalLogin(t *testing.T) {
	h := newHarness(t, harnessOptions{adminRecovery: true})
	sid := h.setupAdmin()

	idp := oidctest.New(t, "bookshelf")
	if rec := h.do(http.MethodPut, "/api/v1/admin/settings", map[string]any{
		"oidc": map[string]any{
			"enabled": true, "issuer": idp.URL(), "client_id": "bookshelf",
			"client_secret": "client-secret", "groups_claim": "groups",
			"admin_group": "bookshelf-admins", "local_login_enabled": false,
		},
	}, withCookie(sid)); rec.Code != http.StatusOK {
		t.Fatalf("turning the password form off = %d: %s", rec.Code, rec.Body.String())
	}

	if !h.auth.LocalLoginEnabled() {
		t.Fatal("recovery must force the password form back on")
	}
	login := h.do(http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "correct-horse-battery",
	})
	if login.Code != http.StatusOK {
		t.Fatalf("password login under recovery = %d, want 200: %s", login.Code, login.Body.String())
	}
	var status map[string]any
	decode(t, h.do(http.MethodGet, "/api/v1/auth/status", nil), &status)
	if status["local_login"] != true {
		t.Errorf("auth status local_login = %v, want true under recovery", status["local_login"])
	}

	// The page says why the form is still there.
	var body map[string]any
	decode(t, h.do(http.MethodGet, "/api/v1/admin/settings", nil, withCookie(sid)), &body)
	if body["admin_recovery"] != true {
		t.Error("admin settings does not report that recovery is on")
	}
}
