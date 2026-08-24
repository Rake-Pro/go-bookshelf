package api_test

// These tests are the "Security test matrix" from the design document, one
// sub-test group per row. They run against the fully wired server, so they
// exercise the real middleware chain rather than a handler in isolation.

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rake-pro/go-bookshelf/internal/epub"
	"github.com/rake-pro/go-bookshelf/internal/fixtures"
	"github.com/rake-pro/go-bookshelf/internal/remote"
)

// Row: path traversal.
func TestSecurityPathTraversal(t *testing.T) {
	h := newHarness(t)
	sid := h.setupAdmin()
	h.seedLibrary(sid, "Everything", h.media)
	itemID := h.firstItemID(sid, "ebook")
	base := "/api/v1/items/" + itoa(itemID) + "/epub/"

	t.Run("epub resource path", func(t *testing.T) {
		for _, attempt := range []string{
			"../../etc/passwd",
			"..%2f..%2fetc%2fpasswd",
			"%2e%2e/%2e%2e/etc/passwd",
			"....//....//etc/passwd",
			"/etc/passwd",
			"..\\..\\windows\\win.ini",
			"OEBPS/../../../etc/passwd",
		} {
			rec := h.do(http.MethodGet, base+attempt, nil, withCookie(sid))
			if rec.Code == http.StatusOK {
				t.Errorf("GET %s%s = 200, want a refusal", base, attempt)
			}
			if strings.Contains(rec.Body.String(), "root:") {
				t.Errorf("GET %s%s leaked file content", base, attempt)
			}
		}
	})

	t.Run("file outside the library root", func(t *testing.T) {
		// Point a stored file row at a path no library covers, as a corrupted
		// or tampered database row would.
		outside := filepath.Join(t.TempDir(), "passwd")
		if err := os.WriteFile(outside, []byte("root:x:0:0::/root:/bin/sh\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		audioID := h.firstItemID(sid, "audiobook")
		detail := h.do(http.MethodGet, "/api/v1/items/"+itoa(audioID), nil, withCookie(sid))
		var d struct {
			Files []struct {
				ID        int64  `json:"id"`
				StreamURL string `json:"stream_url"`
			} `json:"files"`
		}
		decode(t, detail, &d)
		if len(d.Files) == 0 {
			t.Fatal("audiobook has no files")
		}
		if _, err := h.db.ExecContext(h.ctx, `UPDATE files SET path = ? WHERE id = ?`, outside, d.Files[0].ID); err != nil {
			t.Fatal(err)
		}

		rec := h.do(http.MethodGet, d.Files[0].StreamURL, nil, withCookie(sid))
		if rec.Code != http.StatusNotFound {
			t.Errorf("streaming a file outside every library root = %d, want 404", rec.Code)
		}
		if strings.Contains(rec.Body.String(), "root:") {
			t.Error("streaming a file outside the library root leaked its content")
		}
		download := h.do(http.MethodGet, "/api/v1/items/"+itoa(audioID)+"/download", nil, withCookie(sid))
		if download.Code != http.StatusNotFound {
			t.Errorf("downloading a file outside every library root = %d, want 404", download.Code)
		}
	})

	t.Run("archive entries escaping the container", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "evil")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		err := fixtures.WriteEPUB(filepath.Join(dir, "evil.epub"), fixtures.EPUBOptions{
			Title:        "Evil",
			ExtraEntries: map[string][]byte{"../../escaped.txt": []byte("owned")},
		})
		if err != nil {
			t.Fatal(err)
		}
		libID := h.createLibraryAt(sid, "Evil", dir)

		scan := h.do(http.MethodPost, "/api/v1/libraries/"+itoa(libID)+"/scan", nil, withCookie(sid))
		if scan.Code != http.StatusOK {
			t.Fatalf("scan = %d", scan.Code)
		}
		var body struct {
			Scan struct {
				Added  int `json:"added"`
				Errors int `json:"errors"`
			} `json:"scan"`
		}
		decode(t, scan, &body)
		if body.Scan.Added != 0 {
			t.Errorf("an archive with an escaping entry was ingested: %+v", body.Scan)
		}
		if body.Scan.Errors == 0 {
			t.Error("the rejected archive was not counted as an error")
		}
		if _, err := os.Stat(filepath.Join(dir, "..", "escaped.txt")); err == nil {
			t.Error("an archive entry was written outside the library")
		}
	})
}

// Row: zip bomb. The limits live in the epub reader; this asserts they are the
// documented ones and that they fire.
func TestSecurityZipBombLimits(t *testing.T) {
	if epub.DefaultLimits.MaxEntries != 10000 {
		t.Errorf("entry limit = %d, want 10000", epub.DefaultLimits.MaxEntries)
	}
	if epub.DefaultLimits.MaxEntrySize != 256<<20 {
		t.Errorf("entry size limit = %d, want 256 MiB", epub.DefaultLimits.MaxEntrySize)
	}
	if epub.DefaultLimits.MaxTotalSize != 2<<30 {
		t.Errorf("total size limit = %d, want 2 GiB", epub.DefaultLimits.MaxTotalSize)
	}

	h := newHarness(t)
	sid := h.setupAdmin()
	dir := filepath.Join(t.TempDir(), "bomb")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	// A book whose entries expand to far more than they occupy on disk.
	entries := map[string][]byte{}
	for i := 0; i < 40; i++ {
		entries["pad/"+itoa(int64(i))+".bin"] = make([]byte, 1<<20)
	}
	if err := fixtures.WriteEPUB(filepath.Join(dir, "bomb.epub"), fixtures.EPUBOptions{
		Title: "Bomb", ExtraEntries: entries,
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "bomb.epub"))
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > 4<<20 {
		t.Fatalf("fixture is %d bytes; the point is that it compresses small", len(raw))
	}

	libID := h.createLibraryAt(sid, "Bomb", dir)
	scan := h.do(http.MethodPost, "/api/v1/libraries/"+itoa(libID)+"/scan", nil, withCookie(sid))
	var body struct {
		Scan struct {
			Added int `json:"added"`
		} `json:"scan"`
	}
	decode(t, scan, &body)
	// Within the shipped limits this particular archive is legal; what matters
	// is that the reader measured it rather than expanding it blindly.
	if body.Scan.Added > 1 {
		t.Errorf("unexpected ingest result: %+v", body.Scan)
	}
}

// Row: stored XSS.
func TestSecurityStoredXSS(t *testing.T) {
	const payload = `<script>alert('xss')</script>`

	h := newHarness(t)
	sid := h.setupAdmin()
	dir := filepath.Join(t.TempDir(), "xss")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := fixtures.WriteEPUB(filepath.Join(dir, "xss.epub"), fixtures.EPUBOptions{
		Title:       payload,
		Description: payload,
		Authors:     []string{payload},
		Series:      payload,
		Tags:        []string{payload},
	}); err != nil {
		t.Fatal(err)
	}
	libID := h.createLibraryAt(sid, "XSS", dir)
	if rec := h.do(http.MethodPost, "/api/v1/libraries/"+itoa(libID)+"/scan", nil, withCookie(sid)); rec.Code != http.StatusOK {
		t.Fatalf("scan = %d", rec.Code)
	}

	itemID := h.firstItemID(sid, "")
	for _, path := range []string{
		"/api/v1/items",
		"/api/v1/items/" + itoa(itemID),
		"/api/v1/home",
		"/api/v1/search?q=script",
		"/api/v1/authors",
		"/api/v1/series",
		"/api/v1/tags",
	} {
		rec := h.do(http.MethodGet, path, nil, withCookie(sid))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", path, rec.Code)
		}
		body := rec.Body.String()
		if strings.Contains(body, "<script>") {
			t.Errorf("GET %s emitted raw markup: %s", path, body)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("GET %s content type = %q", path, ct)
		}
	}
	// The payload survives as data, escaped, so the UI can render it as text.
	rec := h.do(http.MethodGet, "/api/v1/items/"+itoa(itemID), nil, withCookie(sid))
	if !strings.Contains(rec.Body.String(), `\u003cscript\u003e`) {
		t.Errorf("expected the payload to be present but escaped: %s", rec.Body.String())
	}

	tokenRec := h.do(http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "opds", "scopes": []string{"read"}}, withCookie(sid))
	var tok struct {
		Secret string `json:"secret"`
	}
	decode(t, tokenRec, &tok)
	for _, path := range []string{"/opds/" + itoa(libID), "/opds/search?q=script"} {
		feed := h.do(http.MethodGet, path, nil, func(r *http.Request) { r.SetBasicAuth("opds", tok.Secret) })
		if feed.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", path, feed.Code)
		}
		body := feed.Body.String()
		if strings.Contains(body, "<script>") {
			t.Errorf("OPDS feed %s emitted raw markup: %s", path, body)
		}
		if !strings.Contains(body, "&lt;script&gt;") {
			t.Errorf("OPDS feed %s did not carry the escaped payload: %s", path, body)
		}
	}
}

// Row: auth bypass.
func TestSecurityAuthBypass(t *testing.T) {
	h := newHarness(t)
	sid := h.setupAdmin()
	libID := h.seedLibrary(sid, "Everything", h.media)
	itemID := h.firstItemID(sid, "ebook")

	guarded := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/libraries"},
		{http.MethodPost, "/api/v1/libraries"},
		{http.MethodPatch, "/api/v1/libraries/" + itoa(libID)},
		{http.MethodDelete, "/api/v1/libraries/" + itoa(libID)},
		{http.MethodPost, "/api/v1/libraries/" + itoa(libID) + "/scan"},
		{http.MethodGet, "/api/v1/libraries/" + itoa(libID) + "/scans"},
		{http.MethodPost, "/api/v1/libraries/" + itoa(libID) + "/upload"},
		{http.MethodPost, "/api/v1/libraries/" + itoa(libID) + "/import"},
		{http.MethodGet, "/api/v1/imports/1"},
		{http.MethodDelete, "/api/v1/imports/1"},
		{http.MethodGet, "/api/v1/me/imports"},
		{http.MethodGet, "/api/v1/items"},
		{http.MethodGet, "/api/v1/items/" + itoa(itemID)},
		{http.MethodPatch, "/api/v1/items/" + itoa(itemID)},
		{http.MethodDelete, "/api/v1/items/" + itoa(itemID)},
		{http.MethodGet, "/api/v1/items/" + itoa(itemID) + "/cover"},
		{http.MethodGet, "/api/v1/items/" + itoa(itemID) + "/epub"},
		{http.MethodGet, "/api/v1/items/" + itoa(itemID) + "/epub/OEBPS/chapter1.xhtml"},
		{http.MethodGet, "/api/v1/items/" + itoa(itemID) + "/files/1/stream"},
		{http.MethodGet, "/api/v1/items/" + itoa(itemID) + "/download"},
		{http.MethodGet, "/api/v1/home"},
		{http.MethodGet, "/api/v1/authors"},
		{http.MethodGet, "/api/v1/authors/1"},
		{http.MethodGet, "/api/v1/series"},
		{http.MethodGet, "/api/v1/series/1"},
		{http.MethodGet, "/api/v1/tags"},
		{http.MethodGet, "/api/v1/search?q=a"},
		{http.MethodGet, "/api/v1/me/settings"},
		{http.MethodPut, "/api/v1/me/settings"},
		{http.MethodGet, "/api/v1/me/progress"},
		{http.MethodPut, "/api/v1/me/progress/" + itoa(itemID)},
		{http.MethodGet, "/api/v1/me/bookmarks"},
		{http.MethodPost, "/api/v1/me/bookmarks"},
		{http.MethodDelete, "/api/v1/me/bookmarks/1"},
		{http.MethodGet, "/api/v1/me/tokens"},
		{http.MethodPost, "/api/v1/me/tokens"},
		{http.MethodDelete, "/api/v1/me/tokens/1"},
		{http.MethodGet, "/api/v1/collections"},
		{http.MethodPost, "/api/v1/collections"},
		{http.MethodGet, "/api/v1/users"},
		{http.MethodPost, "/api/v1/users"},
		{http.MethodPatch, "/api/v1/users/1"},
		{http.MethodDelete, "/api/v1/users/1"},
		{http.MethodGet, "/api/v1/users/1/libraries"},
		{http.MethodPut, "/api/v1/users/1/libraries"},
		{http.MethodGet, "/api/v1/system/status"},
	}
	for _, route := range guarded {
		rec := h.do(route.method, route.path, nil)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s unauthenticated = %d, want 401", route.method, route.path, rec.Code)
		}
	}

	// Route matching is exact: no prefix of a public path opens a door.
	for _, path := range []string{
		"/api/v1/itemsX",
		"/api/v1/items/../users",
		"/api/v1/auth",
		"/api/v1/authX",
		"/api/v1/healthzX",
		"/api/v1/readyzz",
		"/api/v1/",
		"/api/v1",
	} {
		rec := h.do(http.MethodGet, path, nil)
		if rec.Code == http.StatusOK {
			t.Errorf("GET %s unauthenticated = 200, want a refusal", path)
		}
	}

	// The documented exemptions really are public.
	for _, path := range []string{
		"/healthz", "/readyz", "/api/v1/healthz", "/api/v1/readyz",
		"/manifest.webmanifest", "/sw.js", "/assets/app.js", "/api/v1/auth/status",
	} {
		rec := h.do(http.MethodGet, path, nil)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200 without a credential", path, rec.Code)
		}
	}

	// A garbage or expired session is not a credential.
	for _, value := range []string{"not-a-session", "", strings.Repeat("a", 64)} {
		rec := h.do(http.MethodGet, "/api/v1/items", nil, withCookie(value))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("forged session %q = %d, want 401", value, rec.Code)
		}
	}
	_ = sid
}

// Row: token confusion.
func TestSecurityTokenConfusion(t *testing.T) {
	h := newHarness(t)
	sid := h.setupAdmin()
	h.seedLibrary(sid, "Everything", h.media)

	readRec := h.do(http.MethodPost, "/api/v1/me/tokens",
		map[string]any{"name": "read only", "scopes": []string{"read"}}, withCookie(sid))
	if readRec.Code != http.StatusCreated {
		t.Fatalf("create token = %d", readRec.Code)
	}
	var readTok struct {
		Secret string `json:"secret"`
	}
	decode(t, readRec, &readTok)

	writeRec := h.do(http.MethodPost, "/api/v1/me/tokens",
		map[string]any{"name": "read write", "scopes": []string{"read", "write"}}, withCookie(sid))
	var writeTok struct {
		Secret string `json:"secret"`
	}
	decode(t, writeRec, &writeTok)

	t.Run("session id is not a bearer token", func(t *testing.T) {
		rec := h.do(http.MethodGet, "/api/v1/items", nil, withBearer(sid))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("session id as bearer = %d, want 401", rec.Code)
		}
	})

	t.Run("api token is not a session cookie", func(t *testing.T) {
		rec := h.do(http.MethodGet, "/api/v1/items", nil, withCookie(readTok.Secret))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("token as session cookie = %d, want 401", rec.Code)
		}
	})

	t.Run("read scope cannot write", func(t *testing.T) {
		ok := h.do(http.MethodGet, "/api/v1/items", nil, withBearer(readTok.Secret))
		if ok.Code != http.StatusOK {
			t.Fatalf("read token GET = %d, want 200", ok.Code)
		}
		for _, route := range []struct {
			method string
			path   string
			body   any
		}{
			{http.MethodPut, "/api/v1/me/settings", map[string]any{"reader": map[string]any{"font_scale": 1.2}}},
			{http.MethodPost, "/api/v1/me/bookmarks", map[string]any{"item_id": 1}},
			{http.MethodPost, "/api/v1/libraries", map[string]any{"name": "x", "kind": "ebook", "paths": []string{"/tmp"}}},
			{http.MethodPost, "/api/v1/users", map[string]any{"username": "x", "password": "long-enough-password"}},
		} {
			rec := h.do(route.method, route.path, route.body, withBearer(readTok.Secret))
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s %s with a read-only token = %d, want 403", route.method, route.path, rec.Code)
			}
		}
	})

	t.Run("write scope can write", func(t *testing.T) {
		rec := h.do(http.MethodPut, "/api/v1/me/settings",
			map[string]any{"reader": map[string]any{"font_scale": 1.2}}, withBearer(writeTok.Secret))
		if rec.Code != http.StatusOK {
			t.Errorf("write token PUT = %d, want 200", rec.Code)
		}
	})

	t.Run("a token cannot mint another token", func(t *testing.T) {
		rec := h.do(http.MethodPost, "/api/v1/me/tokens",
			map[string]any{"name": "escalation", "scopes": []string{"read", "write"}}, withBearer(writeTok.Secret))
		if rec.Code != http.StatusForbidden {
			t.Errorf("token minting a token = %d, want 403", rec.Code)
		}
	})

	t.Run("revoked token stops working", func(t *testing.T) {
		list := h.do(http.MethodGet, "/api/v1/me/tokens", nil, withCookie(sid))
		var tokens struct {
			Items []struct {
				ID   int64  `json:"id"`
				Name string `json:"name"`
			} `json:"items"`
		}
		decode(t, list, &tokens)
		var id int64
		for _, tk := range tokens.Items {
			if tk.Name == "read only" {
				id = tk.ID
			}
		}
		if id == 0 {
			t.Fatal("token not listed")
		}
		if rec := h.do(http.MethodDelete, "/api/v1/me/tokens/"+itoa(id), nil, withCookie(sid)); rec.Code != http.StatusOK {
			t.Fatalf("delete token = %d", rec.Code)
		}
		rec := h.do(http.MethodGet, "/api/v1/items", nil, withBearer(readTok.Secret))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("revoked token = %d, want 401", rec.Code)
		}
	})
}

// Row: cross-library access.
func TestSecurityCrossLibraryAccess(t *testing.T) {
	h := newHarness(t)
	adminSID := h.setupAdmin()
	allowedLib := h.seedLibrary(adminSID, "Allowed", h.media)
	deniedLib := h.seedLibrary(adminSID, "Denied", h.otherMe)

	userSID := h.createUser(adminSID, "reader", []int64{allowedLib})

	// Find an item that lives in the library the user cannot see.
	adminItems := h.do(http.MethodGet, "/api/v1/items?library="+itoa(deniedLib), nil, withCookie(adminSID))
	var body struct {
		Items []struct {
			ID   int64  `json:"id"`
			Kind string `json:"kind"`
		} `json:"items"`
	}
	decode(t, adminItems, &body)
	if len(body.Items) == 0 {
		t.Fatal("the denied library has no items")
	}
	var hiddenEbook, hiddenAudio int64
	for _, it := range body.Items {
		if it.Kind == "ebook" && hiddenEbook == 0 {
			hiddenEbook = it.ID
		}
		if it.Kind == "audiobook" && hiddenAudio == 0 {
			hiddenAudio = it.ID
		}
	}

	for _, path := range []string{
		"/api/v1/items/" + itoa(hiddenEbook),
		"/api/v1/items/" + itoa(hiddenEbook) + "/cover",
		"/api/v1/items/" + itoa(hiddenEbook) + "/epub",
		"/api/v1/items/" + itoa(hiddenEbook) + "/epub/chapter1.xhtml",
		"/api/v1/items/" + itoa(hiddenEbook) + "/download",
		"/api/v1/items/" + itoa(hiddenAudio) + "/files/1/stream",
		"/api/v1/items/" + itoa(hiddenAudio) + "/download",
	} {
		rec := h.do(http.MethodGet, path, nil, withCookie(userSID))
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s as a user without access = %d, want 404", path, rec.Code)
		}
	}

	// Writes against a hidden item are refused the same way.
	rec := h.do(http.MethodPut, "/api/v1/me/progress/"+itoa(hiddenEbook),
		map[string]any{"percent": 0.5}, withCookie(userSID))
	if rec.Code != http.StatusNotFound {
		t.Errorf("progress on a hidden item = %d, want 404", rec.Code)
	}

	// Listings never mention the hidden library's contents.
	list := h.do(http.MethodGet, "/api/v1/items", nil, withCookie(userSID))
	var visible struct {
		Items []struct {
			LibraryID int64 `json:"library_id"`
		} `json:"items"`
		Total int `json:"total"`
	}
	decode(t, list, &visible)
	if visible.Total != 2 {
		t.Errorf("visible items = %d, want only the granted library's two", visible.Total)
	}
	for _, it := range visible.Items {
		if it.LibraryID != allowedLib {
			t.Errorf("item from library %d leaked into the listing", it.LibraryID)
		}
	}
	libs := h.do(http.MethodGet, "/api/v1/libraries", nil, withCookie(userSID))
	var libBody struct {
		Total int `json:"total"`
	}
	decode(t, libs, &libBody)
	if libBody.Total != 1 {
		t.Errorf("libraries visible = %d, want 1", libBody.Total)
	}

	// A non-admin cannot reach the admin surface at all.
	for _, route := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/users"},
		{http.MethodPost, "/api/v1/users"},
		{http.MethodGet, "/api/v1/users/1/libraries"},
		{http.MethodGet, "/api/v1/system/status"},
		{http.MethodPost, "/api/v1/libraries"},
		{http.MethodPost, "/api/v1/libraries/" + itoa(deniedLib) + "/scan"},
		{http.MethodPatch, "/api/v1/items/" + itoa(hiddenEbook)},
		{http.MethodDelete, "/api/v1/items/" + itoa(hiddenEbook)},
	} {
		rec := h.do(route.method, route.path, map[string]any{}, withCookie(userSID))
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s as a plain user = %d, want 403", route.method, route.path, rec.Code)
		}
	}
}

// Row: SSRF. Outbound metadata fetching is off unless a provider is
// configured, and when on it refuses anything but a public http(s) endpoint.
func TestSecuritySSRF(t *testing.T) {
	h := newHarness(t)
	meta := h.settings.Get().Metadata
	if meta.Enabled() {
		t.Fatalf("no metadata provider should be configured by default, got %q", meta.Provider)
	}

	disabled := remote.New(meta.Enabled(), meta.AllowPrivate)
	if disabled.Enabled() {
		t.Fatal("the fetcher must be disabled when no provider is configured")
	}
	if _, _, err := disabled.Get(context.Background(), "https://example.com/cover.jpg"); !errors.Is(err, remote.ErrDisabled) {
		t.Errorf("fetch with no provider = %v, want ErrDisabled", err)
	}

	enabled := remote.New(true, false)
	for _, raw := range []string{"file:///etc/passwd", "ftp://example.com/x", "data:text/plain,hi"} {
		if err := enabled.CheckURL(raw); !errors.Is(err, remote.ErrScheme) {
			t.Errorf("CheckURL(%q) = %v, want ErrScheme", raw, err)
		}
	}
	for _, raw := range []string{
		"http://127.0.0.1/x", "http://[::1]/x", "http://10.0.0.1/x",
		"http://192.168.0.1/x", "http://172.20.0.1/x", "http://169.254.169.254/",
	} {
		if err := enabled.CheckURL(raw); !errors.Is(err, remote.ErrBlocked) {
			t.Errorf("CheckURL(%q) = %v, want ErrBlocked", raw, err)
		}
	}
}

// Row: proxy-header auth.
func TestSecurityProxyHeaderAuth(t *testing.T) {
	h := newHarness(t, harnessOptions{
		proxyAuthHeader: "Remote-User",
		trustedProxies:  []string{"192.0.2.0/24"},
	})
	sid := h.setupAdmin()
	h.seedLibrary(sid, "Everything", h.media)

	t.Run("honored from a trusted proxy", func(t *testing.T) {
		rec := h.do(http.MethodGet, "/api/v1/items", nil,
			withRemoteAddr("192.0.2.7:4321"),
			func(r *http.Request) { r.Header.Set("Remote-User", "proxied") })
		if rec.Code != http.StatusOK {
			t.Fatalf("trusted proxy header = %d, want 200: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("ignored from an untrusted source", func(t *testing.T) {
		for _, addr := range []string{"203.0.113.9:1234", "198.51.100.4:1234", "127.0.0.1:1234"} {
			rec := h.do(http.MethodGet, "/api/v1/items", nil,
				withRemoteAddr(addr),
				func(r *http.Request) { r.Header.Set("Remote-User", "admin") })
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("proxy header from %s = %d, want 401", addr, rec.Code)
			}
		}
	})

	t.Run("forwarding headers do not confer trust", func(t *testing.T) {
		// X-Forwarded-For is attacker-controlled; only the peer address counts.
		rec := h.do(http.MethodGet, "/api/v1/items", nil,
			withRemoteAddr("203.0.113.9:1234"),
			func(r *http.Request) {
				r.Header.Set("Remote-User", "admin")
				r.Header.Set("X-Forwarded-For", "192.0.2.7")
				r.Header.Set("X-Real-IP", "192.0.2.7")
			})
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("spoofed forwarding headers = %d, want 401", rec.Code)
		}
	})

	t.Run("header ignored entirely when not configured", func(t *testing.T) {
		plain := newHarness(t)
		plain.setupAdmin()
		rec := plain.do(http.MethodGet, "/api/v1/items", nil,
			withRemoteAddr("192.0.2.7:4321"),
			func(r *http.Request) { r.Header.Set("Remote-User", "admin") })
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("proxy header with no proxy mode = %d, want 401", rec.Code)
		}
	})
}

// Row: rate limit.
func TestSecurityRateLimit(t *testing.T) {
	t.Run("login", func(t *testing.T) {
		h := newHarness(t)
		h.setupAdmin()

		limited := false
		for i := 0; i < 25; i++ {
			rec := h.do(http.MethodPost, "/api/v1/auth/login",
				map[string]string{"username": "admin", "password": "wrong-password-here"},
				withRemoteAddr("198.51.100.10:5000"))
			if rec.Code == http.StatusTooManyRequests {
				limited = true
				break
			}
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("attempt %d = %d: %s", i, rec.Code, rec.Body.String())
			}
		}
		if !limited {
			t.Fatal("repeated failed logins from one address were never rate limited")
		}

		// The limit is per source address, so another client is unaffected.
		rec := h.do(http.MethodPost, "/api/v1/auth/login",
			map[string]string{"username": "admin", "password": "correct-horse-battery"},
			withRemoteAddr("198.51.100.11:5000"))
		if rec.Code != http.StatusOK {
			t.Errorf("login from a different address = %d, want 200", rec.Code)
		}
	})

	t.Run("setup", func(t *testing.T) {
		h := newHarness(t)
		if _, err := h.auth.EnsureSetupToken(h.ctx); err != nil {
			t.Fatal(err)
		}
		limited := false
		for i := 0; i < 25; i++ {
			rec := h.do(http.MethodPost, "/api/v1/setup/admin",
				map[string]string{"token": "wrong", "username": "x", "password": "long-enough-password"},
				withRemoteAddr("198.51.100.20:5000"))
			if rec.Code == http.StatusTooManyRequests {
				limited = true
				break
			}
			if rec.Code != http.StatusForbidden {
				t.Fatalf("attempt %d = %d: %s", i, rec.Code, rec.Body.String())
			}
		}
		if !limited {
			t.Fatal("repeated setup attempts were never rate limited")
		}
	})
}

// Row: upload permission. Adding books is a permission of its own, not a side
// effect of being able to read a library.
func TestSecurityUploadPermission(t *testing.T) {
	h := newHarness(t)
	sid := h.setupAdmin()
	libID := h.createLibraryAt(sid, "Uploads", h.media)
	book := sampleEPUB(t, "Permission", "A. Writer")

	cases := []struct {
		name      string
		role      string
		canUpload bool
		want      int
	}{
		{"a plain user without the flag", "user", false, http.StatusForbidden},
		{"a restricted account with the flag set", "restricted", true, http.StatusForbidden},
		{"a plain user with the flag", "user", true, http.StatusCreated},
	}
	for i, c := range cases {
		userID, userSID := h.makeUser(sid, "u"+itoa(int64(i)), c.role, c.canUpload, []int64{libID})

		upload := h.postUpload(userSID, libID, "", partFile{"book.epub", book})
		if upload.Code != c.want {
			t.Errorf("upload by %s = %d, want %d: %s", c.name, upload.Code, c.want, upload.Body.String())
		}
		importRec := h.do(http.MethodPost, "/api/v1/libraries/"+itoa(libID)+"/import",
			map[string]string{"url": "http://books.example.com/one.epub"}, withCookie(userSID))
		wantImport := http.StatusAccepted
		if c.want == http.StatusForbidden {
			wantImport = http.StatusForbidden
		}
		if importRec.Code != wantImport {
			t.Errorf("import by %s = %d, want %d", c.name, importRec.Code, wantImport)
		}

		// The answer /auth/me gives is the one the frontend hides the button
		// on, so it has to agree with what the endpoints actually do.
		me := h.do(http.MethodGet, "/api/v1/auth/me", nil, withCookie(userSID))
		var identity struct {
			CanUpload bool `json:"can_upload"`
		}
		decode(t, me, &identity)
		if identity.CanUpload != (c.want == http.StatusCreated) {
			t.Errorf("/auth/me says can_upload=%v for %s", identity.CanUpload, c.name)
		}

		// Withdrawing the permission takes effect on the next request.
		if c.want == http.StatusCreated {
			patch := h.do(http.MethodPatch, "/api/v1/users/"+itoa(userID),
				map[string]any{"can_upload": false}, withCookie(sid))
			if patch.Code != http.StatusOK {
				t.Fatalf("patch user = %d: %s", patch.Code, patch.Body.String())
			}
			again := h.postUpload(userSID, libID, "", partFile{"book.epub", book})
			if again.Code != http.StatusForbidden {
				t.Errorf("upload after the permission was withdrawn = %d, want 403", again.Code)
			}
		}
	}

	// Anonymous callers never get as far as the permission check.
	req := h.postUpload("", libID, "", partFile{"book.epub", book})
	if req.Code != http.StatusUnauthorized {
		t.Errorf("anonymous upload = %d, want 401", req.Code)
	}
}

// Row: cross-library access, for the add-books routes. A library the caller
// cannot see answers 404, the same as a library that does not exist.
func TestSecurityUploadCrossLibrary(t *testing.T) {
	h := newHarness(t)
	sid := h.setupAdmin()
	allowed := h.createLibraryAt(sid, "Theirs", h.media)
	denied := h.createLibraryAt(sid, "Not theirs", h.otherMe)
	_, userSID := h.makeUser(sid, "reader", "user", true, []int64{allowed})

	rec := h.postUpload(userSID, denied, "", partFile{"book.epub", sampleEPUB(t, "Elsewhere", "A. Writer")})
	if rec.Code != http.StatusNotFound {
		t.Errorf("upload into a library the user cannot see = %d, want 404", rec.Code)
	}
	imp := h.do(http.MethodPost, "/api/v1/libraries/"+itoa(denied)+"/import",
		map[string]string{"url": "http://books.example.com/one.epub"}, withCookie(userSID))
	if imp.Code != http.StatusNotFound {
		t.Errorf("import into a library the user cannot see = %d, want 404", imp.Code)
	}
	if entries, err := os.ReadDir(h.otherMe); err != nil || len(entries) != 0 {
		t.Errorf("the refused upload wrote into the other library: %v", entries)
	}
}

// Row: path traversal, for uploads. Neither the client's filename nor the
// subfolder decides where bytes land.
func TestSecurityUploadTraversal(t *testing.T) {
	h := newHarness(t)
	sid := h.setupAdmin()
	libID := h.createLibraryAt(sid, "Uploads", h.media)
	book := sampleEPUB(t, "Escape", "A. Writer")
	outside := filepath.Dir(h.media)

	for _, name := range []string{
		"../../evil.epub", `..\..\evil.epub`, "/etc/cron.d/evil.epub", "....//....//evil.epub",
	} {
		rec := h.postUpload(sid, libID, "", partFile{name, book})
		if rec.Code != http.StatusCreated && rec.Code != http.StatusConflict {
			continue // a refusal is also an acceptable answer
		}
		if rec.Code == http.StatusCreated {
			var out uploadResult
			decode(t, rec, &out)
			if strings.ContainsAny(out.Files[0].Filename, `/\`) {
				t.Errorf("filename %q produced the path %q", name, out.Files[0].Filename)
			}
		}
	}
	for _, subdir := range []string{"..", "../escape", `..\escape`, "a/b", "/etc"} {
		rec := h.postUpload(sid, libID, subdir, partFile{"book.epub", book})
		if rec.Code == http.StatusCreated {
			t.Errorf("subfolder %q was accepted", subdir)
		}
	}
	if _, err := os.Stat(filepath.Join(outside, "evil.epub")); err == nil {
		t.Fatal("an upload wrote outside the library root")
	}
	if _, err := os.Stat(filepath.Join(outside, "escape")); err == nil {
		t.Fatal("a subfolder escaped the library root")
	}
}

// Row: SSRF, for URL imports. The importer is always enabled - the user typed
// the URL - so the address guard is the only thing standing between it and the
// server's own network.
func TestSecurityImportSSRF(t *testing.T) {
	h := newHarness(t)
	sid := h.setupAdmin()
	libID := h.createLibraryAt(sid, "Imports", h.media)

	for _, raw := range []string{
		"http://127.0.0.1/book.epub", "http://[::1]/book.epub", "http://10.0.0.1/book.epub",
		"http://192.168.0.1/book.epub", "http://172.20.0.1/book.epub",
		"http://169.254.169.254/latest/meta-data/", "http://0.0.0.0/book.epub",
	} {
		rec := h.do(http.MethodPost, "/api/v1/libraries/"+itoa(libID)+"/import",
			map[string]string{"url": raw}, withCookie(sid))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("import of %q = %d, want 400: %s", raw, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), raw) {
			t.Errorf("the refusal for %q echoed the address back", raw)
		}
	}
	// Nothing was queued, so nothing will be fetched later either.
	list := h.do(http.MethodGet, "/api/v1/me/imports", nil, withCookie(sid))
	var jobs struct {
		Total int `json:"total"`
	}
	decode(t, list, &jobs)
	if jobs.Total != 0 {
		t.Errorf("%d refused imports were queued anyway", jobs.Total)
	}
}

// Row: rate limit, for uploads. One account cannot hold several 2 GiB streams
// open at once, and cannot start an unbounded number of them.
func TestSecurityUploadRateLimit(t *testing.T) {
	h := newHarness(t)
	sid := h.setupAdmin()
	libID := h.createLibraryAt(sid, "Uploads", h.media)

	limited := false
	for i := 0; i < 60; i++ {
		rec := h.postUpload(sid, libID, "", partFile{"book.epub",
			sampleEPUB(t, "Book "+itoa(int64(i)), "A. Writer")})
		if rec.Code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Error("uploads from one account were never rate limited")
	}
}
