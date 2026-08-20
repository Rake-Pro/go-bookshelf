package api_test

// The HTTP contract for adding books: who may, what a request looks like, and
// what each refusal answers. The rules themselves are tested in
// internal/upload and internal/importer; these tests are about the endpoints.

import (
	"bytes"
	"context"
	"image/color"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rake-pro/go-bookshelf/internal/fixtures"
	"github.com/rake-pro/go-bookshelf/internal/settings"
)

// allowLoopback turns on the setting that lets the guarded fetcher reach a
// private address. Every import test needs it, because the server under test
// is on 127.0.0.1 and the guard refuses that by default - which is the point.
func (h *harness) allowLoopback() {
	h.mutateSettings(func(s *settings.Settings) { s.Metadata.AllowPrivate = true })
}

type partFile struct {
	name string
	body []byte
}

// postUpload builds a multipart request the way the browser does: the
// subfolder field first, then the files.
func (h *harness) postUpload(sid string, libID int64, subdir string, files ...partFile) *httptest.ResponseRecorder {
	h.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if subdir != "" {
		if err := mw.WriteField("subdir", subdir); err != nil {
			h.t.Fatal(err)
		}
	}
	for _, f := range files {
		part, err := mw.CreateFormFile("files", f.name)
		if err != nil {
			h.t.Fatal(err)
		}
		if _, err := part.Write(f.body); err != nil {
			h.t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		h.t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/libraries/"+itoa(libID)+"/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(&http.Cookie{Name: "gbs_session", Value: sid})
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

// makeUser creates an account with a role and an upload permission, and signs
// it in.
func (h *harness) makeUser(adminSID, username, role string, canUpload bool, libraries []int64) (int64, string) {
	h.t.Helper()
	rec := h.do(http.MethodPost, "/api/v1/users", map[string]any{
		"username": username, "password": "another-long-password", "display_name": username,
		"role": role, "libraries": libraries, "can_upload": canUpload,
	}, withCookie(adminSID))
	if rec.Code != http.StatusCreated {
		h.t.Fatalf("create user returned %d: %s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID        int64 `json:"id"`
		CanUpload bool  `json:"can_upload"`
	}
	decode(h.t, rec, &created)
	if created.CanUpload != canUpload {
		h.t.Fatalf("created user can_upload = %v, want %v", created.CanUpload, canUpload)
	}
	login := h.do(http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": username, "password": "another-long-password",
	})
	if login.Code != http.StatusOK {
		h.t.Fatalf("login returned %d: %s", login.Code, login.Body.String())
	}
	return created.ID, sessionFrom(h.t, login)
}

func sampleEPUB(t *testing.T, title, author string) []byte {
	t.Helper()
	b, err := fixtures.EPUBBytes(fixtures.EPUBOptions{
		Title: title, Authors: []string{author}, Language: "en",
		Cover: fixtures.PNG(20, 30, color.RGBA{B: 200, A: 255}),
	})
	if err != nil {
		t.Fatalf("epub fixture: %v", err)
	}
	return b
}

type uploadResult struct {
	Status string `json:"status"`
	Files  []struct {
		Filename string `json:"filename"`
		Kind     string `json:"kind"`
		Title    string `json:"title"`
		Author   string `json:"author"`
		Size     int64  `json:"size_bytes"`
		ItemID   int64  `json:"item_id"`
	} `json:"files"`
}

// The whole upload journey, as the sheet performs it.
func TestUploadHappyPath(t *testing.T) {
	h := newHarness(t)
	sid := h.setupAdmin()
	libID := h.createLibraryAt(sid, "Uploads", h.media)

	rec := h.postUpload(sid, libID, "", partFile{"anything.epub", sampleEPUB(t, "The Long Afternoon", "A. Writer")})
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload = %d: %s", rec.Code, rec.Body.String())
	}
	var out uploadResult
	decode(t, rec, &out)
	if out.Status != "complete" || len(out.Files) != 1 {
		t.Fatalf("upload body = %s", rec.Body.String())
	}
	file := out.Files[0]
	if file.Filename != "A. Writer - The Long Afternoon.epub" {
		t.Errorf("filed as %q", file.Filename)
	}
	if file.Kind != "ebook" || file.Title != "The Long Afternoon" || file.Author != "A. Writer" {
		t.Errorf("upload result = %+v", file)
	}
	if file.ItemID == 0 {
		t.Fatal("the response carries no item id, so the sheet cannot link to the book")
	}

	// The item is a normal catalog entry from here on.
	item := h.do(http.MethodGet, "/api/v1/items/"+itoa(file.ItemID), nil, withCookie(sid))
	if item.Code != http.StatusOK {
		t.Fatalf("item = %d: %s", item.Code, item.Body.String())
	}
	var detail struct {
		Title    string   `json:"title"`
		Authors  []string `json:"authors"`
		CoverURL string   `json:"cover_url"`
	}
	decode(t, item, &detail)
	if detail.Title != "The Long Afternoon" || len(detail.Authors) != 1 {
		t.Errorf("item = %+v", detail)
	}
	if detail.CoverURL == "" {
		t.Error("the uploaded book's cover was not ingested")
	}
}

// Several audio files in one request are one audiobook, in one directory.
func TestUploadGroupsAudiobook(t *testing.T) {
	h := newHarness(t)
	sid := h.setupAdmin()
	libID := h.createLibraryAt(sid, "Listening", h.media)

	var files []partFile
	for i, title := range []string{"One", "Two"} {
		files = append(files, partFile{
			name: "part.mp3",
			body: fixtures.MP3Bytes(fixtures.MP3Options{
				Title: title, Album: "The Long Night", AlbumArtist: "A. Writer",
				Track: i + 1, TrackTotal: 2, Frames: 40,
			}),
		})
	}
	rec := h.postUpload(sid, libID, "", files...)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload = %d: %s", rec.Code, rec.Body.String())
	}
	var out uploadResult
	decode(t, rec, &out)
	if len(out.Files) != 1 || out.Files[0].Kind != "audiobook" {
		t.Fatalf("upload body = %s", rec.Body.String())
	}
	if out.Files[0].Filename != "A. Writer - The Long Night" {
		t.Errorf("filed as %q", out.Files[0].Filename)
	}
	detail := h.do(http.MethodGet, "/api/v1/items/"+itoa(out.Files[0].ItemID), nil, withCookie(sid))
	var body struct {
		Files []struct {
			Filename string `json:"filename"`
		} `json:"files"`
	}
	decode(t, detail, &body)
	if len(body.Files) != 2 {
		t.Fatalf("the audiobook has %d files, want 2", len(body.Files))
	}
	if body.Files[0].Filename != "01 - One.mp3" {
		t.Errorf("first file = %q", body.Files[0].Filename)
	}
}

// The same bytes twice answers 409 and names the book the user already has.
func TestUploadDuplicate(t *testing.T) {
	h := newHarness(t)
	sid := h.setupAdmin()
	libID := h.createLibraryAt(sid, "Uploads", h.media)
	body := sampleEPUB(t, "Only Once", "A. Writer")

	first := h.postUpload(sid, libID, "", partFile{"a.epub", body})
	if first.Code != http.StatusCreated {
		t.Fatalf("first upload = %d: %s", first.Code, first.Body.String())
	}
	var out uploadResult
	decode(t, first, &out)

	second := h.postUpload(sid, libID, "", partFile{"b.epub", body})
	if second.Code != http.StatusConflict {
		t.Fatalf("second upload = %d, want 409: %s", second.Code, second.Body.String())
	}
	var conflict struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
		ItemID int64  `json:"item_id"`
		Title  string `json:"title"`
	}
	decode(t, second, &conflict)
	if conflict.Error.Code != "conflict" {
		t.Errorf("conflict code = %q", conflict.Error.Code)
	}
	if conflict.ItemID != out.Files[0].ItemID {
		t.Errorf("conflict names item %d, want the existing %d", conflict.ItemID, out.Files[0].ItemID)
	}
	if conflict.Title != "Only Once" {
		t.Errorf("conflict title = %q", conflict.Title)
	}
}

// Every refusal a bad request can earn, with the status it earns.
func TestUploadRefusals(t *testing.T) {
	h := newHarness(t)
	sid := h.setupAdmin()
	libID := h.createLibraryAt(sid, "Uploads", h.media)
	book := sampleEPUB(t, "Fine", "A. Writer")

	t.Run("wrong extension", func(t *testing.T) {
		ebookLib := h.createLibraryAt(sid, "Ebooks only", filepath.Join(h.media, "ebooks"))
		if err := os.MkdirAll(filepath.Join(h.media, "ebooks"), 0o755); err != nil {
			t.Fatal(err)
		}
		h.setLibraryKind(sid, ebookLib, "ebook")
		rec := h.postUpload(sid, ebookLib, "", partFile{"song.mp3",
			fixtures.MP3Bytes(fixtures.MP3Options{Title: "x", Frames: 20})})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("mp3 into an ebook library = %d, want 400: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("bad magic", func(t *testing.T) {
		rec := h.postUpload(sid, libID, "", partFile{"lies.epub", []byte("not a zip at all")})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("a text file called .epub = %d, want 400", rec.Code)
		}
	})

	t.Run("empty request", func(t *testing.T) {
		rec := h.postUpload(sid, libID, "")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("no files = %d, want 400", rec.Code)
		}
	})

	t.Run("not multipart", func(t *testing.T) {
		rec := h.do(http.MethodPost, "/api/v1/libraries/"+itoa(libID)+"/upload",
			map[string]string{"file": "x"}, withCookie(sid))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("a JSON body = %d, want 400", rec.Code)
		}
	})

	t.Run("a library that does not exist", func(t *testing.T) {
		rec := h.postUpload(sid, 9999, "", partFile{"a.epub", book})
		if rec.Code != http.StatusNotFound {
			t.Errorf("unknown library = %d, want 404", rec.Code)
		}
	})
}

/* ---------------------------- URL imports ---------------------------- */

// runImports starts the queue worker for the duration of a test.
func (h *harness) runImports() func() {
	ctx, cancel := context.WithCancel(h.ctx)
	go h.importWorker.Run(ctx)
	return cancel
}

// awaitImport polls a job the way the sheet does, until it settles.
func (h *harness) awaitImport(sid string, jobID int64) map[string]any {
	h.t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		rec := h.do(http.MethodGet, "/api/v1/imports/"+itoa(jobID), nil, withCookie(sid))
		if rec.Code != http.StatusOK {
			h.t.Fatalf("import status = %d: %s", rec.Code, rec.Body.String())
		}
		var job map[string]any
		decode(h.t, rec, &job)
		switch job["status"] {
		case "done", "failed":
			return job
		}
		time.Sleep(100 * time.Millisecond)
	}
	h.t.Fatalf("import %d never finished", jobID)
	return nil
}

func TestImportFromURL(t *testing.T) {
	h := newHarness(t)
	sid := h.setupAdmin()
	libID := h.createLibraryAt(sid, "Imports", h.media)

	body := sampleEPUB(t, "The Imported Book", "A. Writer")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/epub+zip")
		w.Write(body)
	}))
	defer srv.Close()

	h.allowLoopback()
	stop := h.runImports()
	defer stop()

	rec := h.do(http.MethodPost, "/api/v1/libraries/"+itoa(libID)+"/import",
		map[string]string{"url": srv.URL + "/book"}, withCookie(sid))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("import = %d, want 202: %s", rec.Code, rec.Body.String())
	}
	var job struct {
		ID     int64  `json:"id"`
		Status string `json:"status"`
		URL    string `json:"url"`
	}
	decode(t, rec, &job)
	if job.ID == 0 || job.Status != "queued" {
		t.Fatalf("queued job = %+v", job)
	}

	finished := h.awaitImport(sid, job.ID)
	if finished["status"] != "done" {
		t.Fatalf("import finished as %v: %v", finished["status"], finished["message"])
	}
	itemID, _ := finished["item_id"].(float64)
	if itemID == 0 {
		t.Fatal("a finished import carries no item id")
	}

	item := h.do(http.MethodGet, "/api/v1/items/"+itoa(int64(itemID)), nil, withCookie(sid))
	var detail struct {
		Title string `json:"title"`
	}
	decode(t, item, &detail)
	if detail.Title != "The Imported Book" {
		t.Errorf("imported item title = %q", detail.Title)
	}

	// The job shows up in the caller's own list.
	list := h.do(http.MethodGet, "/api/v1/me/imports", nil, withCookie(sid))
	var jobs struct {
		Items []struct {
			ID int64 `json:"id"`
		} `json:"items"`
		Total int `json:"total"`
	}
	decode(t, list, &jobs)
	if jobs.Total != 1 || jobs.Items[0].ID != job.ID {
		t.Errorf("import list = %s", list.Body.String())
	}

	// And can be cleared off it.
	del := h.do(http.MethodDelete, "/api/v1/imports/"+itoa(job.ID), nil, withCookie(sid))
	if del.Code != http.StatusOK {
		t.Fatalf("cancel = %d: %s", del.Code, del.Body.String())
	}
	if again := h.do(http.MethodGet, "/api/v1/imports/"+itoa(job.ID), nil, withCookie(sid)); again.Code != http.StatusNotFound {
		t.Errorf("a deleted job = %d, want 404", again.Code)
	}
}

// An HTML page becomes a book; the machinery is tested in internal/importer,
// so this is only about the job reaching "done" through the API.
func TestImportWebPage(t *testing.T) {
	h := newHarness(t)
	sid := h.setupAdmin()
	libID := h.createLibraryAt(sid, "Imports", h.media)

	page := `<!doctype html><html lang="en"><head><title>A Story</title>
		<meta property="og:title" content="A Story"/><meta name="author" content="A. Writer"/></head>
		<body><article class="post-content">
		<h1>A Story</h1>
		<p>It began, as these things do, with a long sentence containing several commas,
		   a certain amount of atmosphere, and no advertising whatsoever at all.</p>
		<p>It continued for a second paragraph, which is what makes this block score higher
		   than anything else on this deliberately sparse page.</p>
		</article></body></html>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(page))
	}))
	defer srv.Close()

	h.allowLoopback()
	stop := h.runImports()
	defer stop()

	rec := h.do(http.MethodPost, "/api/v1/libraries/"+itoa(libID)+"/import",
		map[string]string{"url": srv.URL + "/story"}, withCookie(sid))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("import = %d: %s", rec.Code, rec.Body.String())
	}
	var job struct {
		ID int64 `json:"id"`
	}
	decode(t, rec, &job)

	finished := h.awaitImport(sid, job.ID)
	if finished["status"] != "done" {
		t.Fatalf("import finished as %v: %v", finished["status"], finished["message"])
	}
	itemID, _ := finished["item_id"].(float64)
	item := h.do(http.MethodGet, "/api/v1/items/"+itoa(int64(itemID)), nil, withCookie(sid))
	var detail struct {
		Title   string   `json:"title"`
		Authors []string `json:"authors"`
	}
	decode(t, item, &detail)
	if detail.Title != "A Story" {
		t.Errorf("imported title = %q", detail.Title)
	}
	if len(detail.Authors) != 1 || detail.Authors[0] != "A. Writer" {
		t.Errorf("imported authors = %v", detail.Authors)
	}
}

func TestImportRefusesBadURLs(t *testing.T) {
	h := newHarness(t)
	sid := h.setupAdmin()
	libID := h.createLibraryAt(sid, "Imports", h.media)

	for _, raw := range []string{
		"", "not a url at all", "file:///etc/passwd", "ftp://example.com/book.epub",
		"data:text/html,<p>x", "javascript:alert(1)",
	} {
		rec := h.do(http.MethodPost, "/api/v1/libraries/"+itoa(libID)+"/import",
			map[string]string{"url": raw}, withCookie(sid))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("import of %q = %d, want 400: %s", raw, rec.Code, rec.Body.String())
		}
	}
}

// One user's imports are not another's to read or cancel.
func TestImportJobsArePerUser(t *testing.T) {
	h := newHarness(t)
	sid := h.setupAdmin()
	libID := h.createLibraryAt(sid, "Imports", h.media)
	_, mine := h.makeUser(sid, "mine", "user", true, []int64{libID})
	_, theirs := h.makeUser(sid, "theirs", "user", true, []int64{libID})

	rec := h.do(http.MethodPost, "/api/v1/libraries/"+itoa(libID)+"/import",
		map[string]string{"url": "http://books.example.com/one.epub"}, withCookie(mine))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("import = %d: %s", rec.Code, rec.Body.String())
	}
	var job struct {
		ID int64 `json:"id"`
	}
	decode(t, rec, &job)

	if other := h.do(http.MethodGet, "/api/v1/imports/"+itoa(job.ID), nil, withCookie(theirs)); other.Code != http.StatusNotFound {
		t.Errorf("another user reading the job = %d, want 404", other.Code)
	}
	if other := h.do(http.MethodDelete, "/api/v1/imports/"+itoa(job.ID), nil, withCookie(theirs)); other.Code != http.StatusNotFound {
		t.Errorf("another user cancelling the job = %d, want 404", other.Code)
	}
	list := h.do(http.MethodGet, "/api/v1/me/imports", nil, withCookie(theirs))
	var jobs struct {
		Total int `json:"total"`
	}
	decode(t, list, &jobs)
	if jobs.Total != 0 {
		t.Errorf("another user sees %d imports, want none", jobs.Total)
	}
	// The administrator sees every queue, which is what makes it diagnosable.
	adminList := h.do(http.MethodGet, "/api/v1/me/imports", nil, withCookie(sid))
	decode(t, adminList, &jobs)
	if jobs.Total != 1 {
		t.Errorf("the administrator sees %d imports, want 1", jobs.Total)
	}
}

// setLibraryKind narrows a library to one kind, so the extension allowlist can
// be exercised.
func (h *harness) setLibraryKind(sid string, libID int64, kind string) {
	h.t.Helper()
	rec := h.do(http.MethodPatch, "/api/v1/libraries/"+itoa(libID),
		map[string]any{"kind": kind}, withCookie(sid))
	if rec.Code != http.StatusOK {
		h.t.Fatalf("patch library = %d: %s", rec.Code, rec.Body.String())
	}
}

// The upload response is what the sheet renders, so its shape is pinned here
// alongside the rest of the frontend contract.
func TestUploadResponseShape(t *testing.T) {
	h := newHarness(t)
	sid := h.setupAdmin()
	libID := h.createLibraryAt(sid, "Uploads", h.media)

	rec := h.postUpload(sid, libID, "New arrivals",
		partFile{"x.epub", sampleEPUB(t, "In A Folder", "A. Writer")})
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, field := range []string{`"status"`, `"files"`, `"filename"`, `"kind"`, `"title"`, `"author"`, `"size_bytes"`, `"item_id"`} {
		if !strings.Contains(body, field) {
			t.Errorf("the upload response is missing %s: %s", field, body)
		}
	}
	var out uploadResult
	decode(t, rec, &out)
	if out.Files[0].Filename != "New arrivals/A. Writer - In A Folder.epub" {
		t.Errorf("subfolder was not honoured: %q", out.Files[0].Filename)
	}
	if _, err := os.Stat(filepath.Join(h.media, "New arrivals", "A. Writer - In A Folder.epub")); err != nil {
		t.Errorf("the book is not in the subfolder: %v", err)
	}
}
