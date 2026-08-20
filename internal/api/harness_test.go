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
	"github.com/rake-pro/go-bookshelf/internal/store"
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
	t       *testing.T
	ctx     context.Context
	cfg     config.Config
	db      *store.DB
	cat     *library.Catalog
	auth    *auth.Manager
	scanner *library.Scanner
	handler http.Handler
	media   string
	otherMe string
}

type harnessOptions struct {
	proxyAuthHeader string
	trustedProxies  []string
}

func newHarness(t *testing.T, opts ...harnessOptions) *harness {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()

	cfg := config.Default()
	cfg.DBPath = filepath.Join(dir, "test.db")
	cfg.DataDir = dir
	cfg.BaseURL = "http://localhost:8080"
	cfg.SecureCookies = false
	if len(opts) > 0 {
		cfg.ProxyAuthHeader = opts[0].proxyAuthHeader
		cfg.TrustedProxies = opts[0].trustedProxies
	}
	// Re-run the CIDR parsing that Load would normally do.
	cfg = mustReload(t, cfg)

	db, err := store.Open(ctx, cfg.DBPath)
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
	authMgr, err := auth.New(ctx, db, cfg)
	if err != nil {
		t.Fatalf("auth: %v", err)
	}

	media := filepath.Join(dir, "media")
	if err := os.MkdirAll(media, 0o755); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "other-media")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}

	a := api.New(cfg, db, cat, authMgr, scanner, covers, "test")
	handler := server.New(cfg, a, authMgr, os.DirFS(mustFrontendStub(t, dir)))

	return &harness{
		t: t, ctx: ctx, cfg: cfg, db: db, cat: cat, auth: authMgr,
		scanner: scanner, handler: handler, media: media, otherMe: other,
	}
}

// mustReload runs the config through Load so the parsed CIDR sets are built,
// using environment variables rather than reaching into unexported fields.
func mustReload(t *testing.T, cfg config.Config) config.Config {
	t.Helper()
	t.Setenv("GOBOOKSHELF_DB_PATH", cfg.DBPath)
	t.Setenv("GOBOOKSHELF_DATA_DIR", cfg.DataDir)
	t.Setenv("GOBOOKSHELF_BASE_URL", cfg.BaseURL)
	t.Setenv("GOBOOKSHELF_SECURE_COOKIES", "false")
	// Always set these, including to the empty string: a sub-test building a
	// second harness must not inherit the parent's proxy configuration.
	t.Setenv("GOBOOKSHELF_PROXY_AUTH_HEADER", cfg.ProxyAuthHeader)
	t.Setenv("GOBOOKSHELF_TRUSTED_PROXIES", joinCommas(cfg.TrustedProxies))
	loaded, err := config.Load("")
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	return loaded
}

func joinCommas(v []string) string {
	out := ""
	for i, s := range v {
		if i > 0 {
			out += ","
		}
		out += s
	}
	return out
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

// setupAdmin completes first-run setup and returns the admin's session id.
func (h *harness) setupAdmin() string {
	h.t.Helper()
	token, err := h.auth.EnsureSetupToken(h.ctx)
	if err != nil {
		h.t.Fatalf("setup token: %v", err)
	}
	rec := h.do(http.MethodPost, "/api/v1/auth/setup", map[string]string{
		"token": token, "username": "admin", "password": "correct-horse-battery", "display_name": "Admin",
	})
	if rec.Code != http.StatusOK {
		h.t.Fatalf("setup returned %d: %s", rec.Code, rec.Body.String())
	}
	return sessionFrom(h.t, rec)
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
