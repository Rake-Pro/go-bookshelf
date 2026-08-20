package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"image/color"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/rake-pro/go-bookshelf/internal/api"
	"github.com/rake-pro/go-bookshelf/internal/auth"
	"github.com/rake-pro/go-bookshelf/internal/config"
	"github.com/rake-pro/go-bookshelf/internal/fixtures"
	"github.com/rake-pro/go-bookshelf/internal/images"
	"github.com/rake-pro/go-bookshelf/internal/library"
	"github.com/rake-pro/go-bookshelf/internal/server"
	"github.com/rake-pro/go-bookshelf/internal/settings"
	"github.com/rake-pro/go-bookshelf/internal/store"
	"github.com/rake-pro/go-bookshelf/internal/storetest"
	"github.com/rs/zerolog"
)

func TestMain(m *testing.M) {
	// Keep the suite quiet, and hash passwords with a cost suited to tests
	// rather than to production.
	zerolog.SetGlobalLevel(zerolog.Disabled)
	auth.DefaultArgonParams = auth.ArgonParams{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32, SaltLen: 16}
	os.Exit(m.Run())
}

type harness struct {
	t        *testing.T
	ctx      context.Context
	cfg      config.Config
	settings *settings.Service
	db       *store.DB
	cat      *library.Catalog
	auth     *auth.Manager
	scanner  *library.Scanner
	handler  http.Handler
	media    string
	otherMe  string
}

type harnessOptions struct {
	proxyAuthHeader string
	trustedProxies  []string
	adminRecovery   bool
	// noDataDir runs the server with no local data directory, so every read
	// that used to touch a file has to come out of the database.
	noDataDir bool
}

func newHarness(t *testing.T, opts ...harnessOptions) *harness {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()

	// The whole API contract runs against whichever backend storetest hands
	// back: SQLite by default, Postgres when GOBOOKSHELF_TEST_POSTGRES_DSN is
	// set. Nothing below this line knows which one it got.
	driver, target := storetest.Target(t)
	cfg := config.Default()
	cfg.DBDriver = driver
	cfg.DBPath, cfg.DBDSN = "", ""
	if driver == store.DriverPostgres {
		cfg.DBDSN = target
	} else {
		cfg.DBPath = target
	}
	cfg.DataDir = dir
	cfg.SecretsKey = config.DevInsecureKey()
	if len(opts) > 0 {
		cfg.AdminRecovery = opts[0].adminRecovery
		if opts[0].noDataDir {
			// No local disk at all: covers must still be served, from the
			// database, which is the shape a multi-node deployment runs in.
			cfg.DataDir = ""
		}
	}

	db, err := store.Open(ctx, cfg.DBDriver, cfg.DSN())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	covers, err := images.NewStore(cfg.CoversDir())
	if err != nil {
		t.Fatalf("cover store: %v", err)
	}
	cat := library.NewCatalog(db)
	scanner := library.NewScanner(cat, covers)
	authMgr := auth.New(db, cfg)

	set, err := settings.New(ctx, db, cfg.SecretsKey, false)
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	set.Register(authMgr)
	if len(opts) > 0 && opts[0].proxyAuthHeader != "" {
		next := set.Get()
		next.ProxyAuth = settings.ProxyAuth{
			Enabled: true, Header: opts[0].proxyAuthHeader, TrustedProxies: opts[0].trustedProxies,
		}
		if err := set.Save(ctx, next); err != nil {
			t.Fatalf("proxy settings: %v", err)
		}
	}
	if err := set.Apply(ctx); err != nil {
		t.Fatalf("apply settings: %v", err)
	}

	media := filepath.Join(dir, "media")
	if err := os.MkdirAll(media, 0o755); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "other-media")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}

	a := api.New(cfg, set, db, cat, authMgr, scanner, covers, "test")
	handler := server.New(a, authMgr, set, os.DirFS(mustFrontendStub(t, dir)))

	return &harness{
		t: t, ctx: ctx, cfg: cfg, settings: set, db: db, cat: cat, auth: authMgr,
		scanner: scanner, handler: handler, media: media, otherMe: other,
	}
}

// mutateSettings edits the stored settings the way the admin page would, and
// fails the test if the edit is rejected.
func (h *harness) mutateSettings(mut func(*settings.Settings)) {
	h.t.Helper()
	next := h.settings.Get()
	mut(&next)
	if err := h.settings.Save(h.ctx, next); err != nil {
		h.t.Fatalf("save settings: %v", err)
	}
}

// mustFrontendStub creates a minimal static tree so the SPA fallback has
// something to serve; the real frontend is embedded in the binary.
func mustFrontendStub(t *testing.T, dir string) string {
	t.Helper()
	root := filepath.Join(dir, "dist")
	if err := os.MkdirAll(filepath.Join(root, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<!doctype html><title>go-bookshelf</title>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sw.js"), []byte("/* service worker */"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "manifest.webmanifest"), []byte(`{"name":"go-bookshelf"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "assets", "app.js"), []byte("export default 1;"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The three directories the SPA fallback must never shadow.
	for _, f := range []struct{ path, body string }{
		{"app/main.js", "export const boot = 1;"},
		{"vendor/foliate-js/view.js", "export const view = 1;"},
		{"icons/icon.svg", "<svg xmlns=\"http://www.w3.org/2000/svg\"/>"},
	} {
		full := filepath.Join(root, filepath.FromSlash(f.path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(f.body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// do performs a request against the wired-up server.
func (h *harness) do(method, path string, body any, mutate ...func(*http.Request)) *httptest.ResponseRecorder {
	h.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			h.t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, m := range mutate {
		m(req)
	}
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

func withCookie(sid string) func(*http.Request) {
	return func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: sid})
	}
}

func withBearer(token string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }
}

func withRemoteAddr(addr string) func(*http.Request) {
	return func(r *http.Request) { r.RemoteAddr = addr }
}

// setupAdmin walks the whole first-run wizard - token, administrator, base URL,
// no identity provider, no library - and returns the admin's session id. Every
// test that touches the API goes through it, so the wizard is exercised by the
// entire suite rather than by one test of its own.
func (h *harness) setupAdmin() string {
	h.t.Helper()
	token, err := h.auth.EnsureSetupToken(h.ctx)
	if err != nil {
		h.t.Fatalf("setup token: %v", err)
	}

	check := h.do(http.MethodPost, "/api/v1/setup/token", map[string]string{"token": token})
	if check.Code != http.StatusOK {
		h.t.Fatalf("setup/token returned %d: %s", check.Code, check.Body.String())
	}

	rec := h.do(http.MethodPost, "/api/v1/setup/admin", map[string]string{
		"token": token, "username": "admin", "password": "correct-horse-battery", "display_name": "Admin",
	})
	if rec.Code != http.StatusOK {
		h.t.Fatalf("setup/admin returned %d: %s", rec.Code, rec.Body.String())
	}
	sid := sessionFrom(h.t, rec)

	h.mustSetupStep(sid, "base-url", map[string]any{"base_url": "http://localhost:8080"})
	h.mustSetupStep(sid, "oidc", map[string]any{"skip": true})
	h.mustSetupStep(sid, "library", map[string]any{"skip": true})
	h.mustSetupStep(sid, "complete", map[string]any{})
	return sid
}

// mustSetupStep posts one wizard step and fails on anything but success.
func (h *harness) mustSetupStep(sid, step string, body any) {
	h.t.Helper()
	rec := h.do(http.MethodPost, "/api/v1/setup/"+step, body, withCookie(sid))
	if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		h.t.Fatalf("setup/%s returned %d: %s", step, rec.Code, rec.Body.String())
	}
}

// createUser adds a non-admin account and signs it in.
func (h *harness) createUser(adminSID, username string, libraries []int64) string {
	h.t.Helper()
	rec := h.do(http.MethodPost, "/api/v1/users", map[string]any{
		"username": username, "password": "another-long-password", "display_name": username,
		"role": "user", "libraries": libraries,
	}, withCookie(adminSID))
	if rec.Code != http.StatusCreated {
		h.t.Fatalf("create user returned %d: %s", rec.Code, rec.Body.String())
	}
	login := h.do(http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": username, "password": "another-long-password",
	})
	if login.Code != http.StatusOK {
		h.t.Fatalf("login returned %d: %s", login.Code, login.Body.String())
	}
	return sessionFrom(h.t, login)
}

func sessionFrom(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.SessionCookie && c.Value != "" {
			return c.Value
		}
	}
	t.Fatalf("no session cookie in response: %v", rec.Result().Cookies())
	return ""
}

// seedLibrary generates one ebook and one audiobook in dir and scans it.
func (h *harness) seedLibrary(sid, name, dir string) int64 {
	h.t.Helper()
	if err := fixtures.WriteEPUB(filepath.Join(dir, "afternoon.epub"), fixtures.EPUBOptions{
		Title:       "The Long Afternoon",
		Authors:     []string{"A. Writer"},
		Description: "A quiet book.",
		Language:    "en",
		Publisher:   "Example Press",
		ISBN:        "9781234567897",
		Series:      "Afternoons",
		SeriesIndex: 1,
		Tags:        []string{"Fiction"},
		Cover:       fixtures.PNG(40, 60, color.RGBA{B: 220, A: 255}),
	}); err != nil {
		h.t.Fatalf("epub fixture: %v", err)
	}
	if err := fixtures.WriteM4B(filepath.Join(dir, "evening.m4b"), fixtures.M4BOptions{
		Title: "The Long Evening", Album: "The Long Evening", AlbumArtist: "A. Writer",
		Narrator: "C. Reader", DurationMS: 1_800_000,
		Chapters: []fixtures.M4BChapter{{Title: "One", StartMS: 0}, {Title: "Two", StartMS: 900_000}},
	}); err != nil {
		h.t.Fatalf("m4b fixture: %v", err)
	}

	rec := h.do(http.MethodPost, "/api/v1/libraries", map[string]any{
		"name": name, "kind": "mixed", "paths": []string{dir},
	}, withCookie(sid))
	if rec.Code != http.StatusCreated {
		h.t.Fatalf("create library returned %d: %s", rec.Code, rec.Body.String())
	}
	var lib struct {
		ID int64 `json:"id"`
	}
	decode(h.t, rec, &lib)

	scan := h.do(http.MethodPost, "/api/v1/libraries/"+itoa(lib.ID)+"/scan", nil, withCookie(sid))
	if scan.Code != http.StatusOK {
		h.t.Fatalf("scan returned %d: %s", scan.Code, scan.Body.String())
	}
	return lib.ID
}

func decode(t *testing.T, rec *httptest.ResponseRecorder, dst any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), dst); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var digits []byte
	for v > 0 {
		digits = append([]byte{byte('0' + v%10)}, digits...)
		v /= 10
	}
	if neg {
		return "-" + string(digits)
	}
	return string(digits)
}

// firstItemID returns the id of the first item visible to a session.
func (h *harness) firstItemID(sid string, kind string) int64 {
	h.t.Helper()
	path := "/api/v1/items?sort=title"
	if kind != "" {
		path += "&kind=" + kind
	}
	rec := h.do(http.MethodGet, path, nil, withCookie(sid))
	if rec.Code != http.StatusOK {
		h.t.Fatalf("list items returned %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Items []struct {
			ID int64 `json:"id"`
		} `json:"items"`
	}
	decode(h.t, rec, &body)
	if len(body.Items) == 0 {
		h.t.Fatalf("no items in library")
	}
	return body.Items[0].ID
}

// createLibraryAt registers a library rooted at dir without seeding files.
func (h *harness) createLibraryAt(sid, name, dir string) int64 {
	h.t.Helper()
	rec := h.do(http.MethodPost, "/api/v1/libraries", map[string]any{
		"name": name, "kind": "mixed", "paths": []string{dir},
	}, withCookie(sid))
	if rec.Code != http.StatusCreated {
		h.t.Fatalf("create library returned %d: %s", rec.Code, rec.Body.String())
	}
	var lib struct {
		ID int64 `json:"id"`
	}
	decode(h.t, rec, &lib)
	return lib.ID
}
