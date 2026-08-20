package importer_test

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rake-pro/go-bookshelf/internal/epub"
	"github.com/rake-pro/go-bookshelf/internal/fixtures"
	"github.com/rake-pro/go-bookshelf/internal/images"
	"github.com/rake-pro/go-bookshelf/internal/importer"
	"github.com/rake-pro/go-bookshelf/internal/library"
	"github.com/rake-pro/go-bookshelf/internal/remote"
	"github.com/rake-pro/go-bookshelf/internal/storetest"
	"github.com/rake-pro/go-bookshelf/internal/upload"
	"github.com/rs/zerolog"
)

func TestMain(m *testing.M) {
	zerolog.SetGlobalLevel(zerolog.Disabled)
	os.Exit(m.Run())
}

type env struct {
	ctx  context.Context
	up   *upload.Service
	cat  *library.Catalog
	lib  *library.Library
	root string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	db := storetest.Open(t)
	covers, err := images.NewStore(filepath.Join(dir, "covers"))
	if err != nil {
		t.Fatal(err)
	}
	cat := library.NewCatalog(db)
	root := filepath.Join(dir, "media")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	lib, err := cat.CreateLibrary(ctx, "Books", library.KindMixed, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	return &env{ctx: ctx, up: upload.New(cat, library.NewScanner(cat, covers)), cat: cat, lib: lib, root: root}
}

// loopbackFetcher is the guarded client with private addresses allowed, which
// is the same switch the admin page offers and the only way a test server on
// 127.0.0.1 is reachable at all.
func loopbackFetcher() *remote.Fetcher { return remote.New(true, true) }

/* ---------------------------- the EPUB builder --------------------------- */

// A book this package builds has to be readable by the same code that reads
// every other book in the library, so the assertion is made with that reader.
func TestBuildEPUBIsReadable(t *testing.T) {
	raw, err := importer.BuildEPUB(&importer.Book{
		Title:    "A Serial",
		Author:   "A. Writer",
		Language: "en",
		Source:   "http://example.com/story",
		Chapters: []importer.Chapter{
			{Title: "One", XHTML: "<p>First &amp; foremost.</p>"},
			{Title: "Two", XHTML: "<p>Second.<br/>Still second.</p>"},
		},
		Images: []importer.Image{
			{Name: "img0001.png", ContentType: "image/png", Data: fixtures.PNG(4, 4, blue())},
		},
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	path := filepath.Join(t.TempDir(), "built.epub")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := epub.Open(path)
	if err != nil {
		t.Fatalf("the built book does not open: %v", err)
	}
	defer r.Close()
	book, err := r.Book()
	if err != nil {
		t.Fatalf("the built book has no readable package document: %v", err)
	}
	if book.Meta.Title != "A Serial" {
		t.Errorf("title = %q", book.Meta.Title)
	}
	var author string
	for _, p := range book.Meta.People {
		if p.Role == library.RoleAuthor {
			author = p.Name
		}
	}
	if author != "A. Writer" {
		t.Errorf("author = %q, want the creator to be read as the author", author)
	}
	if len(book.Spine) != 2 {
		t.Errorf("spine has %d entries, want 2", len(book.Spine))
	}
	// The mimetype entry has to be first and stored, or a reader cannot
	// identify the file from its opening bytes: a 30-byte local header, the
	// name, then the payload itself rather than a compressed stream.
	const payload = 30 + len("mimetype")
	if got := string(raw[payload : payload+len("application/epub+zip")]); got != "application/epub+zip" {
		t.Errorf("mimetype is not the first stored entry: %q", got)
	}
	for _, name := range []string{"OEBPS/nav.xhtml", "OEBPS/images/img0001.png", "OEBPS/style.css"} {
		if _, err := r.ReadFile(name); err != nil {
			t.Errorf("%s is missing from the container: %v", name, err)
		}
	}
}

func TestBuildEPUBRefusesAnEmptyBook(t *testing.T) {
	if _, err := importer.BuildEPUB(&importer.Book{Title: "Nothing"}); err == nil {
		t.Error("a book with no chapters was built")
	}
}

/* ------------------------------ direct files ----------------------------- */

// A URL that answers with a book file is not extracted at all: it goes
// straight into the upload validation path.
func TestImportDirectEPUBURL(t *testing.T) {
	e := newEnv(t)
	body, err := fixtures.EPUBBytes(fixtures.EPUBOptions{
		Title: "The Long Afternoon", Authors: []string{"A. Writer"},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A deliberately unhelpful content type and no extension in the path:
		// the decision is made on the bytes.
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(body)
	}))
	defer srv.Close()

	accepted, err := importer.New(e.up, loopbackFetcher()).Import(e.ctx, e.lib, srv.URL+"/download")
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(accepted) != 1 || accepted[0].Filename != "A. Writer - The Long Afternoon.epub" {
		t.Fatalf("imported %+v", accepted)
	}
	if _, err := os.Stat(filepath.Join(e.root, accepted[0].Filename)); err != nil {
		t.Errorf("the imported book is not on disk: %v", err)
	}
}

func TestImportRejectsSomethingThatIsNeither(t *testing.T) {
	e := newEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("just some words, not a book and not a page"))
	}))
	defer srv.Close()

	_, err := importer.New(e.up, loopbackFetcher()).Import(e.ctx, e.lib, srv.URL)
	if !errors.Is(err, importer.ErrUnsupported) {
		t.Errorf("import = %v, want ErrUnsupported", err)
	}
}

/* ------------------------------ web stories ------------------------------ */

// storyPage is one page of the three-part serial the next test walks.
func storyPage(chapter int, next string) string {
	var nextLink string
	if next != "" {
		nextLink = fmt.Sprintf(`<a rel="next" href="%s">Next chapter</a>`, next)
	}
	return `<!doctype html>
<html lang="en">
<head>
  <title>Chapter ` + itoa(chapter) + ` - A Serial | Example Reads</title>
  <meta property="og:title" content="A Serial"/>
  <meta name="author" content="A. Writer"/>
  <script type="application/ld+json">
    {"@context":"https://schema.org","@type":"Book","name":"A Serial",
     "author":{"@type":"Person","name":"A. Writer"}}
  </script>
  <script>window.tracking = true; document.cookie = 'x';</script>
  <style>.ad { color: red }</style>
</head>
<body>
  <nav class="site-nav"><a href="/">Home</a> <a href="/about">About</a></nav>
  <div class="ad-slot advertisement">
    <p>BUY SOMETHING NOW. This advertisement is not part of the story at all and
       runs on for long enough that a scorer might otherwise be tempted by it.</p>
  </div>
  <article class="post-content entry">
    <h1>Chapter ` + itoa(chapter) + `</h1>
    <p>The story of chapter ` + itoa(chapter) + ` began, as these things do, with a
       long sentence containing several commas, a certain amount of atmosphere,
       and no advertising whatsoever.</p>
    <p>It continued for a second paragraph, which is what makes this block score
       higher than the navigation, the sidebar, or the advertisement above it.</p>
    <p>And it ended, as chapter ` + itoa(chapter) + ` always does, rather abruptly.</p>
    <img src="/art/` + itoa(chapter) + `.png" alt="An illustration"/>
    <form action="/subscribe"><input name="email"/><button>Subscribe</button></form>
  </article>
  <aside class="sidebar related-posts"><p>You might also like this other thing entirely.</p></aside>
  ` + nextLink + `
  <footer class="site-footer"><p>Copyright somebody, all rights reserved, every year.</p></footer>
</body>
</html>`
}

func TestImportWebStoryFollowsNextLinks(t *testing.T) {
	e := newEnv(t)
	png := fixtures.PNG(8, 8, blue())

	mux := http.NewServeMux()
	mux.HandleFunc("/story/1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(storyPage(1, "/story/2")))
	})
	mux.HandleFunc("/story/2", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(storyPage(2, "/story/3")))
	})
	mux.HandleFunc("/story/3", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(storyPage(3, "")))
	})
	mux.HandleFunc("/art/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(png)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	accepted, err := importer.New(e.up, loopbackFetcher()).Import(e.ctx, e.lib, srv.URL+"/story/1")
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(accepted) != 1 {
		t.Fatalf("imported %d files", len(accepted))
	}
	if accepted[0].Title != "A Serial" || accepted[0].Author != "A. Writer" {
		t.Errorf("imported book is %q by %q, want the page's own metadata",
			accepted[0].Title, accepted[0].Author)
	}

	r, err := epub.Open(filepath.Join(e.root, accepted[0].Filename))
	if err != nil {
		t.Fatalf("the imported book does not open: %v", err)
	}
	defer r.Close()
	book, err := r.Book()
	if err != nil {
		t.Fatal(err)
	}
	if len(book.Spine) != 3 {
		t.Fatalf("spine has %d chapters, want the three pages of the serial", len(book.Spine))
	}

	var whole strings.Builder
	for _, name := range r.Names() {
		if strings.HasSuffix(name, ".xhtml") {
			body, err := r.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			whole.Write(body)
		}
	}
	text := whole.String()

	for _, wanted := range []string{
		"The story of chapter 1 began", "The story of chapter 2 began", "The story of chapter 3 began",
	} {
		if !strings.Contains(text, wanted) {
			t.Errorf("the book is missing %q", wanted)
		}
	}
	for what, unwanted := range map[string]string{
		"the advertisement": "BUY SOMETHING NOW",
		"the sidebar":       "You might also like",
		"the footer":        "all rights reserved",
		"the navigation":    "About",
		"a script":          "window.tracking",
		"a stylesheet":      "color: red",
		"a form":            "<form",
		"a subscribe box":   "Subscribe",
	} {
		if strings.Contains(text, unwanted) {
			t.Errorf("%s survived into the book (%q)", what, unwanted)
		}
	}
	// The illustration is fetched through the guard and embedded, and the
	// chapter points at the local copy rather than back at the site.
	if !strings.Contains(text, `src="../images/img0001.png"`) {
		t.Errorf("the illustration was not embedded; body was:\n%s", truncateForLog(text))
	}
	if strings.Contains(text, srv.URL) {
		t.Error("the book still references the site it was imported from")
	}
	if _, err := r.ReadFile("OEBPS/images/img0001.png"); err != nil {
		t.Errorf("the embedded image is missing from the container: %v", err)
	}
}

// A page with nothing on it is a failure with a reason, not an empty book.
func TestImportRefusesAPageWithNoStory(t *testing.T) {
	e := newEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<!doctype html><html><head><title>Nothing</title></head>
			<body><nav><a href="/">Home</a></nav></body></html>`))
	}))
	defer srv.Close()

	_, err := importer.New(e.up, loopbackFetcher()).Import(e.ctx, e.lib, srv.URL)
	if !errors.Is(err, importer.ErrNoContent) {
		t.Errorf("import = %v, want ErrNoContent", err)
	}
}

// A next link that points at another host is not followed: a chapter walk must
// not turn into a crawl of the open internet.
func TestImportDoesNotLeaveTheHost(t *testing.T) {
	e := newEnv(t)
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(storyPage(99, "")))
	}))
	defer elsewhere.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(storyPage(1, elsewhere.URL+"/story/99")))
	}))
	defer srv.Close()

	accepted, err := importer.New(e.up, loopbackFetcher()).Import(e.ctx, e.lib, srv.URL+"/story/1")
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	r, err := epub.Open(filepath.Join(e.root, accepted[0].Filename))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	book, err := r.Book()
	if err != nil {
		t.Fatal(err)
	}
	if len(book.Spine) != 1 {
		t.Errorf("spine has %d chapters, want only the one on this host", len(book.Spine))
	}
}

/* ---------------------------------- SSRF --------------------------------- */

// The guard is the same one the metadata fetcher uses, and it is what stops an
// import being a way to make the server read its own network.
func TestImportRefusesPrivateAddresses(t *testing.T) {
	e := newEnv(t)
	guarded := importer.New(e.up, remote.New(true, false))

	for _, raw := range []string{
		"http://127.0.0.1/story", "http://[::1]/story", "http://10.0.0.1/story",
		"http://192.168.1.5/story", "http://169.254.169.254/latest/meta-data/",
	} {
		if _, err := guarded.Import(e.ctx, e.lib, raw); !errors.Is(err, remote.ErrBlocked) {
			t.Errorf("Import(%q) = %v, want ErrBlocked", raw, err)
		}
	}
	for _, raw := range []string{"file:///etc/passwd", "ftp://example.com/book.epub", "data:text/html,<p>x"} {
		if _, err := guarded.Import(e.ctx, e.lib, raw); !errors.Is(err, remote.ErrScheme) {
			t.Errorf("Import(%q) = %v, want ErrScheme", raw, err)
		}
	}
	// Nothing reached the library along the way.
	entries, err := os.ReadDir(e.root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("a refused import left %d entries in the library", len(entries))
	}
}

/* -------------------------------- helpers -------------------------------- */

func itoa(v int) string { return fmt.Sprintf("%d", v) }

func blue() color.Color { return color.RGBA{B: 220, A: 255} }

func truncateForLog(s string) string {
	if len(s) > 1500 {
		return s[:1500] + "..."
	}
	return s
}
