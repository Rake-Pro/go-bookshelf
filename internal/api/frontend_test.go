package api_test

import (
	"net/http"
	"strings"
	"testing"
)

// TestSPAFallback pins the contract in docs/FRONTEND.md: every client-side
// route is answered with the application shell, and every reserved path is
// answered by its own handler or a 404 - never by the shell. Serving HTML to an
// API client would turn a routing mistake into a silent parse error.
func TestSPAFallback(t *testing.T) {
	h := newHarness(t)

	shellRoutes := []string{
		"/", "/library", "/library/12", "/item/7", "/read/7", "/listen/7",
		"/authors", "/authors/3", "/series", "/series/3", "/search?q=hello",
		"/settings", "/admin", "/admin/settings", "/admin/users", "/login?next=%2Fitem%2F7",
		"/setup", "/no/such/route",
	}
	for _, route := range shellRoutes {
		rec := h.do(http.MethodGet, route, nil)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want the application shell", route, rec.Code)
			continue
		}
		if !strings.Contains(rec.Body.String(), "<!doctype html>") {
			t.Errorf("GET %s did not return the application shell: %q", route, rec.Body.String())
		}
	}

	// Reserved paths: whatever they answer, it must not be the shell.
	reserved := []string{
		"/api", "/api/", "/api/v1/items", "/api/v1/no-such-route",
		"/opds", "/opds/1", "/opds/search?q=x",
		"/healthz", "/readyz", "/metrics",
		"/manifest.webmanifest", "/sw.js",
		"/app/main.js", "/vendor/foliate-js/view.js", "/icons/icon.svg",
		"/app/nope.js", "/vendor/nope.js", "/icons/nope.png",
	}
	for _, path := range reserved {
		rec := h.do(http.MethodGet, path, nil, withRemoteAddr("127.0.0.1:1234"))
		if strings.Contains(rec.Body.String(), "<!doctype html>") {
			t.Errorf("GET %s returned the application shell (%d)", path, rec.Code)
		}
	}

	// The three static roots must serve their own bytes, not a 404.
	for path, want := range map[string]string{
		"/app/main.js":               "export const boot",
		"/vendor/foliate-js/view.js": "export const view",
		"/icons/icon.svg":            "<svg",
		"/sw.js":                     "service worker",
		"/manifest.webmanifest":      "go-bookshelf",
	} {
		rec := h.do(http.MethodGet, path, nil)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("GET %s = %d %q, want a file containing %q", path, rec.Code, rec.Body.String(), want)
		}
	}

	// A missing file under a static root is a 404, not the shell.
	if rec := h.do(http.MethodGet, "/app/nope.js", nil); rec.Code != http.StatusNotFound {
		t.Errorf("GET /app/nope.js = %d, want 404", rec.Code)
	}
	// An unmatched API path answers the JSON error envelope, never the shell.
	// Under /api/v1 the auth middleware refuses first, which is also correct.
	rec := h.do(http.MethodGet, "/api/nope", nil)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"not_found"`) {
		t.Errorf("GET /api/nope = %d %q, want a 404 error envelope", rec.Code, rec.Body.String())
	}
	if guarded := h.do(http.MethodGet, "/api/v1/no-such-route", nil); guarded.Code != http.StatusUnauthorized {
		t.Errorf("GET /api/v1/no-such-route = %d, want 401 from the auth middleware", guarded.Code)
	}
}

// TestFrontendContract walks the whole journey the PWA performs, in order, and
// asserts the exact response shapes web/dist/app reads. Anything that drifts
// here breaks a view.
func TestFrontendContract(t *testing.T) {
	h := newHarness(t)

	/* ---- 1. auth status before setup (public, drives /setup and SSO) ---- */

	rec := h.do(http.MethodGet, "/api/v1/auth/status", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("auth status = %d: %s", rec.Code, rec.Body.String())
	}
	var status struct {
		SetupRequired bool   `json:"setup_required"`
		SetupComplete bool   `json:"setup_complete"`
		OIDCEnabled   bool   `json:"oidc_enabled"`
		OIDCStartURL  string `json:"oidc_start_url"`
		LocalLogin    bool   `json:"local_login"`
	}
	decode(t, rec, &status)
	if !status.SetupRequired || status.SetupComplete {
		t.Error("auth status before setup must report setup_required and not setup_complete")
	}
	if !status.LocalLogin {
		t.Error("the password form must be offered before anything has been configured")
	}
	if status.OIDCEnabled {
		t.Error("auth status reports OIDC without any OIDC configuration")
	}
	if status.OIDCStartURL != "/api/v1/auth/oidc/start" {
		t.Errorf("oidc_start_url = %q", status.OIDCStartURL)
	}

	// /auth/me is a pure probe: 401 with nothing but the error envelope.
	me := h.do(http.MethodGet, "/api/v1/auth/me", nil)
	if me.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous /auth/me = %d", me.Code)
	}
	var meBody map[string]any
	decode(t, me, &meBody)
	if _, ok := meBody["error"]; !ok {
		t.Errorf("401 /auth/me body = %s, want an error envelope", me.Body.String())
	}
	for _, leaked := range []string{"oidc", "setup_required", "setup_complete", "local_login"} {
		if _, ok := meBody[leaked]; ok {
			t.Errorf("/auth/me 401 body carries %q; that belongs to /auth/status", leaked)
		}
	}

	/* ---- 2. setup, then login ---- */

	sid := h.setupAdmin()

	after := h.do(http.MethodGet, "/api/v1/auth/status", nil)
	decode(t, after, &status)
	if status.SetupRequired || !status.SetupComplete {
		t.Error("auth status still reports setup as pending after the wizard finished")
	}

	login := h.do(http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "correct-horse-battery",
	})
	if login.Code != http.StatusOK {
		t.Fatalf("login = %d: %s", login.Code, login.Body.String())
	}
	var user struct {
		ID          int64   `json:"id"`
		Username    string  `json:"username"`
		DisplayName string  `json:"display_name"`
		Role        string  `json:"role"`
		Libraries   []int64 `json:"libraries"`
	}
	decode(t, login, &user)
	if user.Username != "admin" || user.Role != "admin" || user.Libraries == nil {
		t.Fatalf("login body = %s", login.Body.String())
	}
	sid = sessionFrom(t, login)

	/* ---- 3. create a library and scan it ---- */

	libID := h.seedLibrary(sid, "Everything", h.media)

	scans := h.do(http.MethodGet, "/api/v1/libraries/"+itoa(libID)+"/scans", nil, withCookie(sid))
	if scans.Code != http.StatusOK {
		t.Fatalf("scans = %d", scans.Code)
	}
	var scanList struct {
		Items []struct {
			ID         int64  `json:"id"`
			StartedAt  string `json:"started_at"`
			FinishedAt string `json:"finished_at"`
			Added      int    `json:"added"`
			Updated    int    `json:"updated"`
			Removed    int    `json:"removed"`
			Errors     int    `json:"errors"`
		} `json:"items"`
		Total int `json:"total"`
	}
	decode(t, scans, &scanList)
	if len(scanList.Items) == 0 {
		t.Fatalf("no scan runs recorded: %s", scans.Body.String())
	}
	// The admin view reads items[0] and treats a null finished_at as running.
	if scanList.Items[0].StartedAt == "" || scanList.Items[0].FinishedAt == "" {
		t.Errorf("scan run = %+v, want both timestamps on a completed run", scanList.Items[0])
	}
	if scanList.Items[0].Added != 2 {
		t.Errorf("scan added = %d, want the two seeded items", scanList.Items[0].Added)
	}

	/* ---- 4. libraries list, as the library picker reads it ---- */

	libs := h.do(http.MethodGet, "/api/v1/libraries", nil, withCookie(sid))
	var libList struct {
		Items []struct {
			ID    int64    `json:"id"`
			Name  string   `json:"name"`
			Kind  string   `json:"kind"`
			Paths []string `json:"paths"`
		} `json:"items"`
	}
	decode(t, libs, &libList)
	if len(libList.Items) != 1 || libList.Items[0].Name != "Everything" || len(libList.Items[0].Paths) != 1 {
		t.Errorf("libraries = %s", libs.Body.String())
	}

	/* ---- 5. items list, as the grid reads it ---- */

	items := h.do(http.MethodGet, "/api/v1/items?sort=title&limit=60", nil, withCookie(sid))
	var itemList struct {
		Items []struct {
			ID        int64    `json:"id"`
			Kind      string   `json:"kind"`
			Title     string   `json:"title"`
			Authors   []string `json:"authors"`
			Narrators []string `json:"narrators"`
			Series    *struct {
				ID       int64   `json:"id"`
				Name     string  `json:"name"`
				Sequence float64 `json:"sequence"`
			} `json:"series"`
			Progress *struct {
				Percent    float64 `json:"percent"`
				FinishedAt string  `json:"finished_at"`
			} `json:"progress"`
		} `json:"items"`
		Total int `json:"total"`
	}
	decode(t, items, &itemList)
	if itemList.Total != 2 || len(itemList.Items) != 2 {
		t.Fatalf("items = %s", items.Body.String())
	}
	var ebookID, audioID int64
	for _, it := range itemList.Items {
		// item-card.js falls back to `authors` when there is no `people`.
		if len(it.Authors) == 0 {
			t.Errorf("item %q carries no authors; the card would render no byline", it.Title)
		}
		switch it.Kind {
		case "ebook":
			ebookID = it.ID
			if it.Series == nil || it.Series.Name != "Afternoons" {
				t.Errorf("ebook series = %+v, want the object shape views/item.js reads", it.Series)
			}
		case "audiobook":
			audioID = it.ID
		}
	}
	if ebookID == 0 || audioID == 0 {
		t.Fatalf("did not find both kinds: %s", items.Body.String())
	}

	/* ---- 6. item detail, as views/item.js and player.js read it ---- */

	detailRec := h.do(http.MethodGet, "/api/v1/items/"+itoa(audioID), nil, withCookie(sid))
	if detailRec.Code != http.StatusOK {
		t.Fatalf("item detail = %d", detailRec.Code)
	}
	var detail struct {
		ID         int64  `json:"id"`
		Kind       string `json:"kind"`
		Title      string `json:"title"`
		DurationMS int64  `json:"duration_ms"`
		People     []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
			Role string `json:"role"`
		} `json:"people"`
		Files []struct {
			ID         int64  `json:"id"`
			Seq        int    `json:"seq"`
			Format     string `json:"format"`
			DurationMS int64  `json:"duration_ms"`
			StreamURL  string `json:"stream_url"`
		} `json:"files"`
		Chapters []struct {
			FileID  int64  `json:"file_id"`
			Seq     int    `json:"seq"`
			Title   string `json:"title"`
			StartMS int64  `json:"start_ms"`
			EndMS   int64  `json:"end_ms"`
		} `json:"chapters"`
		DownloadURL string `json:"download_url"`
	}
	decode(t, detailRec, &detail)
	if len(detail.People) == 0 {
		t.Error("item detail has no people; peopleOf() would render nothing")
	}
	if len(detail.Files) == 0 {
		t.Fatalf("item detail has no files: %s", detailRec.Body.String())
	}
	// player.js builds absolute positions from files[].duration_ms in seq
	// order, so every file must carry a duration and the order must be stable.
	for i, f := range detail.Files {
		if f.DurationMS <= 0 {
			t.Errorf("file %d has duration_ms %d; the player cannot build offsets", f.ID, f.DurationMS)
		}
		if i > 0 && detail.Files[i-1].Seq > f.Seq {
			t.Errorf("files are not ordered by seq: %+v", detail.Files)
		}
	}
	if len(detail.Chapters) == 0 {
		t.Fatalf("item detail has no top-level chapters: %s", detailRec.Body.String())
	}
	fileIDs := map[int64]bool{}
	for _, f := range detail.Files {
		fileIDs[f.ID] = true
	}
	for _, c := range detail.Chapters {
		if !fileIDs[c.FileID] {
			t.Errorf("chapter %q names file %d, which is not in files[]", c.Title, c.FileID)
		}
		// File-relative, per docs/DESIGN.md: the first chapter starts at zero.
		if c.StartMS < 0 || (c.Seq == 0 && c.StartMS != 0) {
			t.Errorf("chapter %+v is not file-relative", c)
		}
	}

	/* ---- 7. EPUB manifest, one resource, and the HEAD size probe ---- */

	manifestRec := h.do(http.MethodGet, "/api/v1/items/"+itoa(ebookID)+"/epub", nil, withCookie(sid))
	if manifestRec.Code != http.StatusOK {
		t.Fatalf("epub manifest = %d: %s", manifestRec.Code, manifestRec.Body.String())
	}
	var manifest struct {
		ResourceURL  string `json:"resource_url"`
		ContainerURL string `json:"container_url"`
		Spine        []struct {
			Href string `json:"href"`
			URL  string `json:"url"`
			Size int64  `json:"size"`
		} `json:"spine"`
	}
	decode(t, manifestRec, &manifest)
	if len(manifest.Spine) == 0 {
		t.Fatalf("manifest spine is empty: %s", manifestRec.Body.String())
	}
	for _, entry := range manifest.Spine {
		if entry.Size <= 0 {
			t.Errorf("spine entry %q has no size; the reader falls back to equal weights", entry.Href)
		}
		if entry.URL != manifest.ResourceURL+entry.Href {
			t.Errorf("spine url %q is not resource_url + href", entry.URL)
		}
	}

	// app/epub.js addresses resources relative to the CONTAINER root, because
	// that is the address space foliate-js resolves manifest hrefs into. The
	// renderer's very first request is container.xml.
	container := h.do(http.MethodGet, manifest.ContainerURL, nil, withCookie(sid))
	if container.Code != http.StatusOK || !strings.Contains(container.Body.String(), "rootfile") {
		t.Fatalf("GET %s = %d: %s", manifest.ContainerURL, container.Code, container.Body.String())
	}
	if !strings.HasPrefix(manifest.Spine[0].Href, "OEBPS/") {
		t.Errorf("spine href %q is not container-root-relative", manifest.Spine[0].Href)
	}

	res := h.do(http.MethodGet, manifest.Spine[0].URL, nil, withCookie(sid))
	if res.Code != http.StatusOK {
		t.Fatalf("GET %s = %d", manifest.Spine[0].URL, res.Code)
	}
	csp := res.Header().Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'none'", "script-src 'none'", "sandbox"} {
		if !strings.Contains(csp, want) {
			t.Errorf("EPUB resource CSP %q is missing %q", csp, want)
		}
	}

	// The reader probes spine documents with HEAD and reads Content-Length. The
	// gzip middleware must not strip it, including when the client offers gzip.
	head := h.do(http.MethodHead, manifest.Spine[0].URL, nil, withCookie(sid),
		func(r *http.Request) { r.Header.Set("Accept-Encoding", "gzip") })
	if head.Code != http.StatusOK {
		t.Fatalf("HEAD %s = %d", manifest.Spine[0].URL, head.Code)
	}
	if got := head.Header().Get("Content-Length"); got == "" || got == "0" {
		t.Errorf("HEAD %s Content-Length = %q, want the resource size", manifest.Spine[0].URL, got)
	}
	if head.Header().Get("Content-Encoding") != "" {
		t.Errorf("HEAD answered with Content-Encoding %q; that hides the size",
			head.Header().Get("Content-Encoding"))
	}
	if head.Body.Len() != 0 {
		t.Errorf("HEAD returned a %d byte body", head.Body.Len())
	}

	/* ---- 8. audio streaming with Range ---- */

	stream := detail.Files[0].StreamURL
	full := h.do(http.MethodGet, stream, nil, withCookie(sid))
	if full.Code != http.StatusOK {
		t.Fatalf("GET %s = %d", stream, full.Code)
	}
	if full.Header().Get("Accept-Ranges") != "bytes" {
		t.Error("the stream endpoint does not advertise byte ranges")
	}
	partial := h.do(http.MethodGet, stream, nil, withCookie(sid),
		func(r *http.Request) { r.Header.Set("Range", "bytes=0-99") })
	if partial.Code != http.StatusPartialContent {
		t.Fatalf("ranged GET %s = %d, want 206", stream, partial.Code)
	}
	if partial.Body.Len() != 100 {
		t.Errorf("ranged GET returned %d bytes, want 100", partial.Body.Len())
	}
	if partial.Header().Get("Content-Encoding") == "gzip" {
		t.Error("a 206 must never be gzip encoded")
	}

	/* ---- 9. progress write, then home shows the continue row ---- */

	prog := h.do(http.MethodPut, "/api/v1/me/progress/"+itoa(audioID), map[string]any{
		"position_ms": 450000, "percent": 0.25, "finished": false, "device": "web-ab12cd",
	}, withCookie(sid))
	if prog.Code != http.StatusOK {
		t.Fatalf("put progress = %d: %s", prog.Code, prog.Body.String())
	}
	var saved struct {
		ItemID     int64   `json:"item_id"`
		PositionMS int64   `json:"position_ms"`
		Percent    float64 `json:"percent"`
		Device     string  `json:"device"`
		FinishedAt string  `json:"finished_at"`
	}
	decode(t, prog, &saved)
	if saved.ItemID != audioID || saved.PositionMS != 450000 || saved.Percent != 0.25 {
		t.Errorf("saved progress = %+v", saved)
	}
	if saved.FinishedAt != "" {
		t.Error("an unfinished item must not carry finished_at; the card would show 'Done'")
	}

	homeRec := h.do(http.MethodGet, "/api/v1/home", nil, withCookie(sid))
	if homeRec.Code != http.StatusOK {
		t.Fatalf("home = %d", homeRec.Code)
	}
	var home struct {
		Continue []struct {
			ID       int64 `json:"id"`
			Progress *struct {
				Percent float64 `json:"percent"`
			} `json:"progress"`
		} `json:"continue"`
		Recent           []struct{ ID int64 } `json:"recent"`
		SeriesInProgress []struct {
			Series struct {
				ID   int64  `json:"id"`
				Name string `json:"name"`
			} `json:"series"`
			Finished int `json:"finished"`
			Total    int `json:"total"`
			NextItem *struct {
				ID    int64  `json:"id"`
				Title string `json:"title"`
			} `json:"next_item"`
		} `json:"series_in_progress"`
	}
	decode(t, homeRec, &home)
	if len(home.Continue) != 1 || home.Continue[0].ID != audioID {
		t.Errorf("home continue = %s", homeRec.Body.String())
	}
	if home.Continue[0].Progress == nil || home.Continue[0].Progress.Percent != 0.25 {
		t.Error("continue entries must carry progress so the card can draw its bar")
	}
	if len(home.Recent) != 2 {
		t.Errorf("home recent = %d items, want 2", len(home.Recent))
	}
	// views/home.js renders these as series rows, not as item cards.
	for _, row := range home.SeriesInProgress {
		if row.Series.Name == "" || row.Total == 0 {
			t.Errorf("series_in_progress row = %+v, want {series,finished,total,next_item}", row)
		}
	}

	/* ---- 10. partial settings PUT must not clear the other groups ---- */

	first := h.do(http.MethodPut, "/api/v1/me/settings",
		map[string]any{"player": map[string]any{"speed": 1.5}}, withCookie(sid))
	if first.Code != http.StatusOK {
		t.Fatalf("put settings = %d: %s", first.Code, first.Body.String())
	}
	second := h.do(http.MethodPut, "/api/v1/me/settings",
		map[string]any{"reader": map[string]any{"font_scale": 1.3}}, withCookie(sid))
	if second.Code != http.StatusOK {
		t.Fatalf("put settings = %d: %s", second.Code, second.Body.String())
	}
	// store.js writes ui.text_scale; a schema that rejects it is a 400 on
	// every appearance change, because the decoder disallows unknown fields.
	third := h.do(http.MethodPut, "/api/v1/me/settings",
		map[string]any{"ui": map[string]any{"theme": "hc-dark", "text_scale": 1.2}}, withCookie(sid))
	if third.Code != http.StatusOK {
		t.Fatalf("put ui settings = %d: %s", third.Code, third.Body.String())
	}

	getSettings := h.do(http.MethodGet, "/api/v1/me/settings", nil, withCookie(sid))
	var settings struct {
		Reader struct {
			FontScale  float64 `json:"font_scale"`
			FontFamily string  `json:"font_family"`
			Theme      string  `json:"theme"`
			Layout     string  `json:"layout"`
		} `json:"reader"`
		Player struct {
			Speed     float64 `json:"speed"`
			SkipBackS int     `json:"skip_back_s"`
			SkipFwdS  int     `json:"skip_fwd_s"`
		} `json:"player"`
		UI struct {
			Theme     string  `json:"theme"`
			TextScale float64 `json:"text_scale"`
		} `json:"ui"`
	}
	decode(t, getSettings, &settings)
	if settings.Player.Speed != 1.5 {
		t.Errorf("player.speed = %v, want 1.5 to survive the later reader write", settings.Player.Speed)
	}
	if settings.Reader.FontScale != 1.3 {
		t.Errorf("reader.font_scale = %v, want 1.3", settings.Reader.FontScale)
	}
	if settings.UI.Theme != "hc-dark" || settings.UI.TextScale != 1.2 {
		t.Errorf("ui = %+v, want the values store.js wrote", settings.UI)
	}
	// Untouched keys keep their defaults rather than zeroing out.
	if settings.Reader.FontFamily != "publisher" || settings.Reader.Layout != "paginated" {
		t.Errorf("reader defaults were cleared by a partial write: %+v", settings.Reader)
	}
	if settings.Player.SkipBackS != 15 || settings.Player.SkipFwdS != 30 {
		t.Errorf("player defaults were cleared by a partial write: %+v", settings.Player)
	}

	/* ---- 11. bookmarks ---- */

	mark := h.do(http.MethodPost, "/api/v1/me/bookmarks", map[string]any{
		"item_id": audioID, "position_ms": 450000, "note": "One",
	}, withCookie(sid))
	if mark.Code != http.StatusCreated {
		t.Fatalf("create bookmark = %d: %s", mark.Code, mark.Body.String())
	}
	var bookmark struct {
		ID         int64  `json:"id"`
		ItemID     int64  `json:"item_id"`
		PositionMS int64  `json:"position_ms"`
		Note       string `json:"note"`
		CreatedAt  string `json:"created_at"`
	}
	decode(t, mark, &bookmark)
	if bookmark.ID == 0 || bookmark.ItemID != audioID || bookmark.Note != "One" {
		t.Errorf("bookmark = %+v", bookmark)
	}
	list := h.do(http.MethodGet, "/api/v1/me/bookmarks?item="+itoa(audioID), nil, withCookie(sid))
	var bookmarks struct {
		Items []struct {
			ID int64 `json:"id"`
		} `json:"items"`
		Total int `json:"total"`
	}
	decode(t, list, &bookmarks)
	if bookmarks.Total != 1 || len(bookmarks.Items) != 1 {
		t.Errorf("bookmarks = %s", list.Body.String())
	}
	if del := h.do(http.MethodDelete, "/api/v1/me/bookmarks/"+itoa(bookmark.ID), nil, withCookie(sid)); del.Code != http.StatusOK {
		t.Errorf("delete bookmark = %d", del.Code)
	}

	/* ---- 12. search, authors and series, as their views read them ---- */

	search := h.do(http.MethodGet, "/api/v1/search?q=Long", nil, withCookie(sid))
	var searchBody struct {
		Items struct {
			Items []struct {
				ID int64 `json:"id"`
			} `json:"items"`
			Total int `json:"total"`
		} `json:"items"`
		Authors struct {
			Items []struct {
				ID   int64  `json:"id"`
				Name string `json:"name"`
			} `json:"items"`
		} `json:"authors"`
		Series struct {
			Items []struct {
				ID   int64  `json:"id"`
				Name string `json:"name"`
			} `json:"items"`
		} `json:"series"`
	}
	decode(t, search, &searchBody)
	if searchBody.Items.Total != 2 {
		t.Errorf("search items = %s", search.Body.String())
	}

	authors := h.do(http.MethodGet, "/api/v1/authors", nil, withCookie(sid))
	var authorList struct {
		Items []struct {
			ID        int64  `json:"id"`
			Name      string `json:"name"`
			ItemCount int    `json:"item_count"`
		} `json:"items"`
	}
	decode(t, authors, &authorList)
	if len(authorList.Items) == 0 {
		t.Fatalf("authors = %s", authors.Body.String())
	}
	one := h.do(http.MethodGet, "/api/v1/authors/"+itoa(authorList.Items[0].ID), nil, withCookie(sid))
	var authorOne struct {
		Author struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"author"`
		Items []struct {
			ID int64 `json:"id"`
		} `json:"items"`
		Total int `json:"total"`
	}
	decode(t, one, &authorOne)
	if authorOne.Author.Name == "" || len(authorOne.Items) == 0 {
		t.Errorf("author detail = %s", one.Body.String())
	}

	seriesRec := h.do(http.MethodGet, "/api/v1/series", nil, withCookie(sid))
	var seriesList struct {
		Items []struct {
			ID int64 `json:"id"`
		} `json:"items"`
	}
	decode(t, seriesRec, &seriesList)
	if len(seriesList.Items) == 0 {
		t.Fatalf("series = %s", seriesRec.Body.String())
	}
	seriesOne := h.do(http.MethodGet, "/api/v1/series/"+itoa(seriesList.Items[0].ID), nil, withCookie(sid))
	var seriesBody struct {
		Series struct {
			Name string `json:"name"`
		} `json:"series"`
		Items []struct {
			Series *struct {
				Sequence float64 `json:"sequence"`
			} `json:"series"`
		} `json:"items"`
	}
	decode(t, seriesOne, &seriesBody)
	if seriesBody.Series.Name == "" || len(seriesBody.Items) == 0 {
		t.Fatalf("series detail = %s", seriesOne.Body.String())
	}
	if seriesBody.Items[0].Series == nil {
		t.Error("series items must carry their own sequence for the view to order them")
	}

	/* ---- 13. system status, as the admin page reads it ---- */

	sys := h.do(http.MethodGet, "/api/v1/system/status", nil, withCookie(sid))
	var sysBody struct {
		Version     string `json:"version"`
		DBSizeBytes int64  `json:"db_size_bytes"`
		Counts      struct {
			Ebooks     int `json:"ebooks"`
			Audiobooks int `json:"audiobooks"`
		} `json:"counts"`
	}
	decode(t, sys, &sysBody)
	if sysBody.Version == "" || sysBody.DBSizeBytes <= 0 {
		t.Errorf("system status = %s", sys.Body.String())
	}
	if sysBody.Counts.Ebooks != 1 || sysBody.Counts.Audiobooks != 1 {
		t.Errorf("system status counts = %+v, want one of each", sysBody.Counts)
	}

	/* ---- 14. user library assignment ---- */

	created := h.do(http.MethodPost, "/api/v1/users", map[string]any{
		"username": "reader", "password": "another-long-password",
		"display_name": "Reader", "role": "user",
	}, withCookie(sid))
	if created.Code != http.StatusCreated {
		t.Fatalf("create user = %d: %s", created.Code, created.Body.String())
	}
	var newUser struct {
		ID int64 `json:"id"`
	}
	decode(t, created, &newUser)
	assign := h.do(http.MethodPut, "/api/v1/users/"+itoa(newUser.ID)+"/libraries",
		map[string]any{"libraries": []int64{libID}}, withCookie(sid))
	if assign.Code != http.StatusOK {
		t.Fatalf("assign libraries = %d: %s", assign.Code, assign.Body.String())
	}
	var assigned struct {
		UserID    int64   `json:"user_id"`
		Libraries []int64 `json:"libraries"`
	}
	decode(t, assign, &assigned)
	if assigned.UserID != newUser.ID || len(assigned.Libraries) != 1 || assigned.Libraries[0] != libID {
		t.Errorf("assigned libraries = %s", assign.Body.String())
	}

	/* ---- 15. OPDS with an API token ---- */

	tokenRec := h.do(http.MethodPost, "/api/v1/me/tokens",
		map[string]any{"name": "opds", "scopes": []string{"read"}}, withCookie(sid))
	if tokenRec.Code != http.StatusCreated {
		t.Fatalf("create token = %d: %s", tokenRec.Code, tokenRec.Body.String())
	}
	var tok struct {
		Secret string `json:"secret"`
	}
	decode(t, tokenRec, &tok)
	if tok.Secret == "" {
		t.Fatal("token response carried no secret")
	}
	if anon := h.do(http.MethodGet, "/opds", nil); anon.Code != http.StatusUnauthorized {
		t.Errorf("anonymous /opds = %d, want 401", anon.Code)
	}
	feed := h.do(http.MethodGet, "/opds", nil, func(r *http.Request) { r.SetBasicAuth("opds", tok.Secret) })
	if feed.Code != http.StatusOK || !strings.Contains(feed.Body.String(), "<feed") {
		t.Fatalf("/opds with a token = %d: %s", feed.Code, feed.Body.String())
	}

	/* ---- 16. logout invalidates the session ---- */

	out := h.do(http.MethodPost, "/api/v1/auth/logout", nil, withCookie(sid))
	if out.Code != http.StatusOK {
		t.Fatalf("logout = %d", out.Code)
	}
	if again := h.do(http.MethodGet, "/api/v1/items", nil, withCookie(sid)); again.Code != http.StatusUnauthorized {
		t.Errorf("GET /api/v1/items after logout = %d, want 401", again.Code)
	}
	if probe := h.do(http.MethodGet, "/api/v1/auth/me", nil, withCookie(sid)); probe.Code != http.StatusUnauthorized {
		t.Errorf("GET /auth/me after logout = %d, want 401", probe.Code)
	}
}

// The admin settings page reads and writes one document; this pins the exact
// field names it depends on, because a rename here silently empties a form.
func TestFrontendAdminSettingsContract(t *testing.T) {
	h := newHarness(t)
	sid := h.setupAdmin()

	rec := h.do(http.MethodGet, "/api/v1/admin/settings", nil, withCookie(sid))
	if rec.Code != http.StatusOK {
		t.Fatalf("admin settings = %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	decode(t, rec, &body)

	for _, section := range []string{"general", "oidc", "proxy_auth", "metadata", "metrics"} {
		if _, ok := body[section]; !ok {
			t.Errorf("admin settings is missing the %q section: %s", section, rec.Body.String())
		}
	}
	general, _ := body["general"].(map[string]any)
	for _, key := range []string{"base_url", "secure_cookies", "session_ttl", "scan_interval"} {
		if _, ok := general[key]; !ok {
			t.Errorf("general is missing %q", key)
		}
	}
	oidc, _ := body["oidc"].(map[string]any)
	for _, key := range []string{
		"enabled", "issuer", "client_id", "has_client_secret", "admin_group", "user_group",
		"groups_claim", "scopes", "auto_register", "local_login_enabled", "redirect_url", "active",
	} {
		if _, ok := oidc[key]; !ok {
			t.Errorf("oidc is missing %q", key)
		}
	}
	if _, ok := oidc["client_secret"]; ok {
		t.Error("the OIDC section carries client_secret; the secret must never be readable")
	}
	if _, ok := body["updated_at"]; !ok {
		t.Error("admin settings is missing updated_at")
	}

	// The redirect URI the operator has to register is derived from the base
	// URL, and the page shows it verbatim.
	if oidc["redirect_url"] != "http://localhost:8080/api/v1/auth/oidc/callback" {
		t.Errorf("redirect_url = %v", oidc["redirect_url"])
	}

	status := h.do(http.MethodGet, "/api/v1/system/status", nil, withCookie(sid))
	var sys map[string]any
	decode(t, status, &sys)
	for _, key := range []string{"oidc_enabled", "settings_updated_at", "local_login"} {
		if _, ok := sys[key]; !ok {
			t.Errorf("system status is missing %q: %s", key, status.Body.String())
		}
	}
}
