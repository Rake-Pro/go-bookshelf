package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestSetupLoginAndMe(t *testing.T) {
	h := newHarness(t)

	status := h.do(http.MethodGet, "/api/v1/auth/status", nil)
	if status.Code != http.StatusOK {
		t.Fatalf("auth status = %d", status.Code)
	}
	var st struct {
		SetupRequired bool `json:"setup_required"`
		OIDCEnabled   bool `json:"oidc_enabled"`
	}
	decode(t, status, &st)
	if !st.SetupRequired {
		t.Error("a fresh install must report setup_required")
	}
	if st.OIDCEnabled {
		t.Error("OIDC must be off when it is not configured")
	}

	sid := h.setupAdmin()

	// Setup is single-use.
	again := h.do(http.MethodPost, "/api/v1/auth/setup", map[string]string{
		"token": "whatever", "username": "second", "password": "another-long-password",
	})
	if again.Code != http.StatusConflict {
		t.Errorf("second setup = %d, want 409", again.Code)
	}

	me := h.do(http.MethodGet, "/api/v1/auth/me", nil, withCookie(sid))
	if me.Code != http.StatusOK {
		t.Fatalf("auth/me = %d: %s", me.Code, me.Body.String())
	}
	var body map[string]any
	decode(t, me, &body)
	for _, key := range []string{"id", "username", "display_name", "role", "libraries"} {
		if _, ok := body[key]; !ok {
			t.Errorf("auth/me is missing %q: %v", key, body)
		}
	}
	if body["role"] != "admin" {
		t.Errorf("role = %v, want admin", body["role"])
	}

	logout := h.do(http.MethodPost, "/api/v1/auth/logout", nil, withCookie(sid))
	if logout.Code != http.StatusOK {
		t.Fatalf("logout = %d", logout.Code)
	}
	after := h.do(http.MethodGet, "/api/v1/auth/me", nil, withCookie(sid))
	if after.Code != http.StatusUnauthorized {
		t.Errorf("auth/me after logout = %d, want 401", after.Code)
	}
}

func TestWeakPasswordRejected(t *testing.T) {
	h := newHarness(t)
	token, err := h.auth.EnsureSetupToken(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	rec := h.do(http.MethodPost, "/api/v1/auth/setup", map[string]string{
		"token": token, "username": "admin", "password": "short",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("setup with a short password = %d, want 400", rec.Code)
	}
}

func TestItemsAndDetailShape(t *testing.T) {
	h := newHarness(t)
	sid := h.setupAdmin()
	h.seedLibrary(sid, "Everything", h.media)

	list := h.do(http.MethodGet, "/api/v1/items?sort=title", nil, withCookie(sid))
	if list.Code != http.StatusOK {
		t.Fatalf("items = %d: %s", list.Code, list.Body.String())
	}
	var listBody struct {
		Items []map[string]any `json:"items"`
		Total int              `json:"total"`
	}
	decode(t, list, &listBody)
	if listBody.Total != 2 || len(listBody.Items) != 2 {
		t.Fatalf("total = %d, items = %d, want 2 of each", listBody.Total, len(listBody.Items))
	}
	for _, key := range []string{
		"id", "library_id", "kind", "title", "sort_title", "subtitle", "authors",
		"narrators", "series", "cover_url", "duration_ms", "size_bytes", "added_at",
		"updated_at", "missing", "progress",
	} {
		if _, ok := listBody.Items[0][key]; !ok {
			t.Errorf("item summary is missing %q", key)
		}
	}

	itemID := h.firstItemID(sid, "ebook")
	detail := h.do(http.MethodGet, "/api/v1/items/"+itoa(itemID), nil, withCookie(sid))
	if detail.Code != http.StatusOK {
		t.Fatalf("item detail = %d", detail.Code)
	}
	var d map[string]any
	decode(t, detail, &d)
	for _, key := range []string{
		"description", "language", "published", "isbn", "asin", "publisher",
		"people", "tags", "files", "download_url", "read_url",
	} {
		if _, ok := d[key]; !ok {
			t.Errorf("item detail is missing %q", key)
		}
	}
	files, _ := d["files"].([]any)
	if len(files) != 1 {
		t.Fatalf("files = %v", d["files"])
	}
	file, _ := files[0].(map[string]any)
	if _, ok := file["path"]; ok {
		t.Error("the item detail must not expose an on-disk path")
	}
	if file["filename"] != "afternoon.epub" {
		t.Errorf("filename = %v", file["filename"])
	}

	// Filters and pagination.
	filtered := h.do(http.MethodGet, "/api/v1/items?kind=audiobook", nil, withCookie(sid))
	var f struct {
		Items []map[string]any `json:"items"`
		Total int              `json:"total"`
	}
	decode(t, filtered, &f)
	if f.Total != 1 || f.Items[0]["kind"] != "audiobook" {
		t.Errorf("kind filter returned %+v", f)
	}
	search := h.do(http.MethodGet, "/api/v1/items?q=Afternoon", nil, withCookie(sid))
	decode(t, search, &f)
	if f.Total != 1 {
		t.Errorf("search returned total %d, want 1", f.Total)
	}
}

func TestHomeShape(t *testing.T) {
	h := newHarness(t)
	sid := h.setupAdmin()
	h.seedLibrary(sid, "Everything", h.media)

	rec := h.do(http.MethodGet, "/api/v1/home", nil, withCookie(sid))
	if rec.Code != http.StatusOK {
		t.Fatalf("home = %d: %s", rec.Code, rec.Body.String())
	}
	var home map[string]json.RawMessage
	decode(t, rec, &home)
	for _, key := range []string{"continue", "recent", "series_in_progress"} {
		raw, ok := home[key]
		if !ok {
			t.Fatalf("home is missing %q", key)
		}
		if strings.TrimSpace(string(raw)) == "null" {
			t.Errorf("home.%s is null, want an array", key)
		}
	}

	// Reading half of a book puts it in the continue row.
	itemID := h.firstItemID(sid, "ebook")
	put := h.do(http.MethodPut, "/api/v1/me/progress/"+itoa(itemID), map[string]any{
		"percent": 0.5, "locator": "epubcfi(/6/4!/4/2)", "device": "test",
	}, withCookie(sid))
	if put.Code != http.StatusOK {
		t.Fatalf("put progress = %d: %s", put.Code, put.Body.String())
	}

	rec = h.do(http.MethodGet, "/api/v1/home", nil, withCookie(sid))
	var home2 struct {
		Continue []map[string]any `json:"continue"`
	}
	decode(t, rec, &home2)
	if len(home2.Continue) != 1 {
		t.Fatalf("continue = %v, want the started book", home2.Continue)
	}
	progress, _ := home2.Continue[0]["progress"].(map[string]any)
	if progress == nil || progress["percent"] != 0.5 {
		t.Errorf("progress = %v", home2.Continue[0]["progress"])
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	h := newHarness(t)
	sid := h.setupAdmin()

	rec := h.do(http.MethodGet, "/api/v1/me/settings", nil, withCookie(sid))
	if rec.Code != http.StatusOK {
		t.Fatalf("settings = %d", rec.Code)
	}
	var settings map[string]map[string]any
	decode(t, rec, &settings)
	for _, group := range []string{"reader", "player", "ui"} {
		if _, ok := settings[group]; !ok {
			t.Fatalf("settings is missing the %q group", group)
		}
	}
	for _, key := range []string{
		"font_scale", "font_family", "line_height", "letter_spacing", "word_spacing",
		"paragraph_spacing", "margin", "align", "theme", "custom_fg", "custom_bg",
		"layout", "columns",
	} {
		if _, ok := settings["reader"][key]; !ok {
			t.Errorf("reader settings missing %q", key)
		}
	}
	for _, key := range []string{"speed", "skip_back_s", "skip_fwd_s", "sleep_timer_min", "sleep_end_of_chapter", "volume_boost"} {
		if _, ok := settings["player"][key]; !ok {
			t.Errorf("player settings missing %q", key)
		}
	}

	put := h.do(http.MethodPut, "/api/v1/me/settings", map[string]any{
		"reader": map[string]any{"font_scale": 1.8, "theme": "hc-dark", "layout": "scrolled"},
		"player": map[string]any{"speed": 1.25},
	}, withCookie(sid))
	if put.Code != http.StatusOK {
		t.Fatalf("put settings = %d: %s", put.Code, put.Body.String())
	}
	decode(t, put, &settings)
	if settings["reader"]["font_scale"] != 1.8 || settings["reader"]["theme"] != "hc-dark" {
		t.Errorf("reader settings not stored: %v", settings["reader"])
	}
	if settings["player"]["speed"] != 1.25 {
		t.Errorf("player speed not stored: %v", settings["player"])
	}
	// Values not sent keep their stored defaults.
	if settings["player"]["skip_fwd_s"] != float64(30) {
		t.Errorf("skip_fwd_s = %v, want the default preserved", settings["player"]["skip_fwd_s"])
	}
}

func TestEPUBManifestAndResources(t *testing.T) {
	h := newHarness(t)
	sid := h.setupAdmin()
	h.seedLibrary(sid, "Everything", h.media)
	itemID := h.firstItemID(sid, "ebook")

	rec := h.do(http.MethodGet, "/api/v1/items/"+itoa(itemID)+"/epub", nil, withCookie(sid))
	if rec.Code != http.StatusOK {
		t.Fatalf("epub manifest = %d: %s", rec.Code, rec.Body.String())
	}
	var manifest struct {
		Spine []struct {
			Href string `json:"href"`
			URL  string `json:"url"`
		} `json:"spine"`
		ResourceURL string `json:"resource_url"`
	}
	decode(t, rec, &manifest)
	if len(manifest.Spine) == 0 {
		t.Fatalf("manifest has no spine: %s", rec.Body.String())
	}

	res := h.do(http.MethodGet, manifest.Spine[0].URL, nil, withCookie(sid))
	if res.Code != http.StatusOK {
		t.Fatalf("epub resource = %d", res.Code)
	}
	csp := res.Header().Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'none'", "sandbox", "frame-ancestors 'self'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("EPUB resource CSP %q is missing %q", csp, want)
		}
	}
	if res.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("EPUB resources must be served with nosniff")
	}
}

func TestAudioStreamSupportsRange(t *testing.T) {
	h := newHarness(t)
	sid := h.setupAdmin()
	h.seedLibrary(sid, "Everything", h.media)
	itemID := h.firstItemID(sid, "audiobook")

	detail := h.do(http.MethodGet, "/api/v1/items/"+itoa(itemID), nil, withCookie(sid))
	var d struct {
		Files []struct {
			StreamURL string `json:"stream_url"`
		} `json:"files"`
	}
	decode(t, detail, &d)
	if len(d.Files) == 0 {
		t.Fatal("audiobook has no files")
	}

	full := h.do(http.MethodGet, d.Files[0].StreamURL, nil, withCookie(sid))
	if full.Code != http.StatusOK {
		t.Fatalf("stream = %d", full.Code)
	}
	if full.Header().Get("Accept-Ranges") != "bytes" {
		t.Error("stream must advertise range support")
	}

	partial := h.do(http.MethodGet, d.Files[0].StreamURL, nil, withCookie(sid), func(r *http.Request) {
		r.Header.Set("Range", "bytes=0-15")
	})
	if partial.Code != http.StatusPartialContent {
		t.Fatalf("ranged stream = %d, want 206", partial.Code)
	}
	if got := partial.Body.Len(); got != 16 {
		t.Errorf("ranged body = %d bytes, want 16", got)
	}
}

func TestCoverServedFromCache(t *testing.T) {
	h := newHarness(t)
	sid := h.setupAdmin()
	h.seedLibrary(sid, "Everything", h.media)
	itemID := h.firstItemID(sid, "ebook")

	for _, size := range []string{"", "thumb", "full"} {
		path := "/api/v1/items/" + itoa(itemID) + "/cover"
		if size != "" {
			path += "?size=" + size
		}
		rec := h.do(http.MethodGet, path, nil, withCookie(sid))
		if rec.Code != http.StatusOK {
			t.Fatalf("cover %q = %d", size, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "image/jpeg" {
			t.Errorf("cover content type = %q", ct)
		}
		if rec.Body.Len() == 0 {
			t.Errorf("cover %q is empty", size)
		}
	}
}

func TestSPAFallbackAndAssets(t *testing.T) {
	h := newHarness(t)

	for _, path := range []string{"/", "/library/1", "/item/42", "/settings", "/login"} {
		rec := h.do(http.MethodGet, path, nil)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want the application shell", path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "go-bookshelf") {
			t.Errorf("GET %s did not return the shell", path)
		}
	}

	sw := h.do(http.MethodGet, "/sw.js", nil)
	if sw.Code != http.StatusOK || sw.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("sw.js = %d, cache-control %q", sw.Code, sw.Header().Get("Cache-Control"))
	}
	manifest := h.do(http.MethodGet, "/manifest.webmanifest", nil)
	if manifest.Code != http.StatusOK {
		t.Errorf("manifest = %d", manifest.Code)
	}

	// A missing file keeps its 404 instead of silently returning the shell.
	missing := h.do(http.MethodGet, "/assets/nope.js", nil)
	if missing.Code != http.StatusNotFound {
		t.Errorf("missing asset = %d, want 404", missing.Code)
	}
	// An unmatched API path answers JSON, not HTML.
	unknown := h.do(http.MethodGet, "/api/v1/nope", nil)
	if unknown.Code != http.StatusUnauthorized {
		t.Errorf("unknown API path unauthenticated = %d, want 401", unknown.Code)
	}
}

func TestSecurityHeadersAndGzip(t *testing.T) {
	h := newHarness(t)
	sid := h.setupAdmin()

	rec := h.do(http.MethodGet, "/api/v1/auth/me", nil, withCookie(sid))
	csp := rec.Header().Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'self'", "object-src 'none'", "base-uri 'none'", "frame-ancestors 'self'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("app CSP %q is missing %q", csp, want)
		}
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("nosniff header missing")
	}
	if rec.Header().Get("Referrer-Policy") == "" {
		t.Error("referrer policy missing")
	}

	gz := h.do(http.MethodGet, "/api/v1/items", nil, withCookie(sid), func(r *http.Request) {
		r.Header.Set("Accept-Encoding", "gzip")
	})
	if gz.Header().Get("Content-Encoding") != "gzip" {
		t.Errorf("JSON response was not compressed: %v", gz.Header())
	}
}

func TestScanRunsAndSystemStatus(t *testing.T) {
	h := newHarness(t)
	sid := h.setupAdmin()
	libID := h.seedLibrary(sid, "Everything", h.media)

	scans := h.do(http.MethodGet, "/api/v1/libraries/"+itoa(libID)+"/scans", nil, withCookie(sid))
	if scans.Code != http.StatusOK {
		t.Fatalf("scans = %d", scans.Code)
	}
	var body struct {
		Items []map[string]any `json:"items"`
		Total int              `json:"total"`
	}
	decode(t, scans, &body)
	if body.Total == 0 {
		t.Fatal("expected at least one recorded scan run")
	}

	status := h.do(http.MethodGet, "/api/v1/system/status", nil, withCookie(sid))
	if status.Code != http.StatusOK {
		t.Fatalf("system status = %d", status.Code)
	}
	var st map[string]any
	decode(t, status, &st)
	for _, key := range []string{"version", "db_size_bytes", "counts", "libraries", "last_scans"} {
		if _, ok := st[key]; !ok {
			t.Errorf("system status missing %q", key)
		}
	}
}

func TestOPDSFeeds(t *testing.T) {
	h := newHarness(t)
	sid := h.setupAdmin()
	libID := h.seedLibrary(sid, "Everything", h.media)

	anon := h.do(http.MethodGet, "/opds", nil)
	if anon.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated OPDS = %d, want 401", anon.Code)
	}
	if !strings.Contains(anon.Header().Get("WWW-Authenticate"), "Basic") {
		t.Error("OPDS must challenge with Basic auth")
	}

	// OPDS clients authenticate with an API token in the Basic password field.
	tokenRec := h.do(http.MethodPost, "/api/v1/me/tokens", map[string]any{
		"name": "reader app", "scopes": []string{"read"},
	}, withCookie(sid))
	if tokenRec.Code != http.StatusCreated {
		t.Fatalf("create token = %d: %s", tokenRec.Code, tokenRec.Body.String())
	}
	var tok struct {
		Secret string `json:"secret"`
	}
	decode(t, tokenRec, &tok)

	root := h.do(http.MethodGet, "/opds", nil, func(r *http.Request) {
		r.SetBasicAuth("opds", tok.Secret)
	})
	if root.Code != http.StatusOK {
		t.Fatalf("OPDS root = %d: %s", root.Code, root.Body.String())
	}
	if !strings.Contains(root.Body.String(), "<feed") {
		t.Errorf("OPDS root is not an Atom feed: %s", root.Body.String())
	}

	lib := h.do(http.MethodGet, "/opds/"+itoa(libID), nil, func(r *http.Request) {
		r.SetBasicAuth("opds", tok.Secret)
	})
	if lib.Code != http.StatusOK {
		t.Fatalf("OPDS library = %d", lib.Code)
	}
	if !strings.Contains(lib.Body.String(), "The Long Afternoon") {
		t.Errorf("OPDS library feed does not list the book: %s", lib.Body.String())
	}

	search := h.do(http.MethodGet, "/opds/search?q=Afternoon", nil, func(r *http.Request) {
		r.SetBasicAuth("opds", tok.Secret)
	})
	if search.Code != http.StatusOK || !strings.Contains(search.Body.String(), "The Long Afternoon") {
		t.Errorf("OPDS search = %d: %s", search.Code, search.Body.String())
	}
}

func TestHealthAndMetrics(t *testing.T) {
	h := newHarness(t)

	for _, path := range []string{"/healthz", "/readyz", "/api/v1/healthz", "/api/v1/readyz"} {
		rec := h.do(http.MethodGet, path, nil)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200 without a credential", path, rec.Code)
		}
	}

	metrics := h.do(http.MethodGet, "/metrics", nil, withRemoteAddr("127.0.0.1:5555"))
	if metrics.Code != http.StatusOK {
		t.Fatalf("metrics from loopback = %d", metrics.Code)
	}
	if !strings.Contains(metrics.Body.String(), "gobookshelf_items") {
		t.Errorf("metrics body = %s", metrics.Body.String())
	}

	blocked := h.do(http.MethodGet, "/metrics", nil, withRemoteAddr("203.0.113.9:5555"))
	if blocked.Code != http.StatusForbidden {
		t.Errorf("metrics from a public address = %d, want 403", blocked.Code)
	}
}
