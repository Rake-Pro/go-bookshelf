package upload_test

// These tests are the acceptance rules from docs/DESIGN.md, one test per rule.
// The upload path is the only code in the server that writes into a library,
// so every way of getting something past it that should not get past it is
// worth a test of its own.

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/rake-pro/go-bookshelf/internal/fixtures"
	"github.com/rake-pro/go-bookshelf/internal/images"
	"github.com/rake-pro/go-bookshelf/internal/library"
	"github.com/rake-pro/go-bookshelf/internal/storetest"
	"github.com/rake-pro/go-bookshelf/internal/upload"
	"github.com/rs/zerolog"
)

func TestMain(m *testing.M) {
	zerolog.SetGlobalLevel(zerolog.Disabled)
	os.Exit(m.Run())
}

type env struct {
	ctx     context.Context
	svc     *upload.Service
	cat     *library.Catalog
	scanner *library.Scanner
	lib     *library.Library
	root    string
}

func newEnv(t *testing.T, kind string) *env {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	db := storetest.Open(t)

	covers, err := images.NewStore(filepath.Join(dir, "covers"))
	if err != nil {
		t.Fatalf("cover store: %v", err)
	}
	cat := library.NewCatalog(db)
	scanner := library.NewScanner(cat, covers)
	root := filepath.Join(dir, "media")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	lib, err := cat.CreateLibrary(ctx, "Books", kind, []string{root})
	if err != nil {
		t.Fatalf("create library: %v", err)
	}
	return &env{ctx: ctx, svc: upload.New(cat, scanner), cat: cat, scanner: scanner, lib: lib, root: root}
}

func (e *env) accept(t *testing.T, subdir string, files ...upload.Incoming) ([]upload.Accepted, error) {
	t.Helper()
	return e.svc.Accept(e.ctx, e.lib, subdir, upload.Files(files...))
}

func file(name string, body []byte) upload.Incoming {
	return upload.Incoming{Filename: name, Body: bytes.NewReader(body)}
}

func epubBytes(t *testing.T, o fixtures.EPUBOptions) []byte {
	t.Helper()
	b, err := fixtures.EPUBBytes(o)
	if err != nil {
		t.Fatalf("epub fixture: %v", err)
	}
	return b
}

// The happy path, end to end: an EPUB arrives under whatever name the browser
// chose, is filed under a name derived from its own metadata, and the scan
// that follows turns it into an item.
func TestAcceptEbook(t *testing.T) {
	e := newEnv(t, library.KindEbook)
	body := epubBytes(t, fixtures.EPUBOptions{
		Title:   "The Long Afternoon",
		Authors: []string{"A. Writer"},
	})

	accepted, err := e.accept(t, "", file("whatever the browser called it.epub", body))
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if len(accepted) != 1 {
		t.Fatalf("accepted %d files, want 1", len(accepted))
	}
	if got, want := accepted[0].Filename, "A. Writer - The Long Afternoon.epub"; got != want {
		t.Errorf("on-disk name = %q, want %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(e.root, accepted[0].Filename)); err != nil {
		t.Errorf("the accepted file is not on disk: %v", err)
	}

	resolved, complete := e.svc.ScanAndResolve(e.ctx, e.lib.ID, accepted, 30*time.Second)
	if !complete {
		t.Fatal("the scan did not finish inside the wait")
	}
	if resolved[0].ItemID == 0 {
		t.Fatal("the scan did not produce an item for the uploaded file")
	}
	item, err := e.cat.Item(e.ctx, resolved[0].ItemID, 0)
	if err != nil {
		t.Fatalf("item: %v", err)
	}
	if item.Title != "The Long Afternoon" {
		t.Errorf("item title = %q", item.Title)
	}
}

// A second book by the same author with the same title does not overwrite the
// first: the name is suffixed instead.
func TestAcceptUniquifiesNames(t *testing.T) {
	e := newEnv(t, library.KindEbook)
	first := epubBytes(t, fixtures.EPUBOptions{Title: "Twice", Authors: []string{"A. Writer"}})
	second := epubBytes(t, fixtures.EPUBOptions{
		Title: "Twice", Authors: []string{"A. Writer"}, Description: "A different edition.",
	})

	a, err := e.accept(t, "", file("a.epub", first))
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	b, err := e.accept(t, "", file("b.epub", second))
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if a[0].Filename != "A. Writer - Twice.epub" {
		t.Errorf("first name = %q", a[0].Filename)
	}
	if b[0].Filename != "A. Writer - Twice (2).epub" {
		t.Errorf("second name = %q, want a suffixed copy", b[0].Filename)
	}
}

// Several audio files uploaded together whose tags name one album become one
// directory, numbered in track order, which is what makes them one item.
func TestAcceptGroupsAudiobook(t *testing.T) {
	e := newEnv(t, library.KindAudiobook)
	var files []upload.Incoming
	for i, title := range []string{"Chapter One", "Chapter Two", "Chapter Three"} {
		files = append(files, file(fmt.Sprintf("track%d.mp3", 3-i), fixtures.MP3Bytes(fixtures.MP3Options{
			Title: title, Album: "The Long Night", AlbumArtist: "A. Writer",
			Track: i + 1, TrackTotal: 3, Frames: 40,
		})))
	}
	// Deliberately out of order on the wire: the tags decide, not the request.
	files[0], files[2] = files[2], files[0]

	accepted, err := e.accept(t, "", files...)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if len(accepted) != 1 {
		t.Fatalf("accepted %d items, want the three files grouped into one", len(accepted))
	}
	if accepted[0].Filename != "A. Writer - The Long Night" {
		t.Errorf("directory = %q", accepted[0].Filename)
	}
	entries, err := os.ReadDir(filepath.Join(e.root, accepted[0].Filename))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	want := []string{"01 - Chapter One.mp3", "02 - Chapter Two.mp3", "03 - Chapter Three.mp3"}
	if strings.Join(names, "|") != strings.Join(want, "|") {
		t.Errorf("files = %v, want %v", names, want)
	}

	resolved, complete := e.svc.ScanAndResolve(e.ctx, e.lib.ID, accepted, 30*time.Second)
	if !complete || resolved[0].ItemID == 0 {
		t.Fatalf("the scan did not ingest the audiobook (complete=%v)", complete)
	}
	item, err := e.cat.Item(e.ctx, resolved[0].ItemID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(item.Files) != 3 {
		t.Errorf("item has %d files, want 3", len(item.Files))
	}
}

// The extension allowlist follows the library's kind.
func TestRejectsWrongExtension(t *testing.T) {
	book := epubBytes(t, fixtures.EPUBOptions{Title: "Fine"})
	audio := fixtures.MP3Bytes(fixtures.MP3Options{Title: "Fine", Frames: 20})

	cases := []struct {
		kind, name string
		body       []byte
	}{
		{library.KindEbook, "book.mp3", audio},
		{library.KindEbook, "book.txt", book},
		{library.KindEbook, "book.epub.exe", book},
		{library.KindEbook, "noextension", book},
		{library.KindAudiobook, "book.epub", book},
		{library.KindAudiobook, "book.mp4", audio},
	}
	for _, c := range cases {
		e := newEnv(t, c.kind)
		if _, err := e.accept(t, "", file(c.name, c.body)); err == nil {
			t.Errorf("%s library accepted %q", c.kind, c.name)
		} else if !strings.Contains(err.Error(), "does not accept") {
			t.Errorf("%s/%s = %v, want an extension refusal", c.kind, c.name, err)
		}
	}
}

// An extension is a claim, not evidence. The magic bytes have to agree.
func TestRejectsBadMagic(t *testing.T) {
	e := newEnv(t, library.KindMixed)
	for _, c := range []struct {
		name string
		body []byte
	}{
		{"not-really.epub", []byte("this is a text file pretending to be a book")},
		{"not-really.mp3", []byte("PK\x03\x04 a zip called an mp3")},
		{"not-really.m4b", append([]byte("PK\x03\x04"), bytes.Repeat([]byte{0}, 64)...)},
		{"html.epub", []byte("<!doctype html><title>nope</title>")},
	} {
		if _, err := e.accept(t, "", file(c.name, c.body)); err == nil {
			t.Errorf("%q was accepted", c.name)
		}
	}
}

// A zip whose extension and first bytes are right but whose contents are not
// an EPUB is refused by the container check, not by the parser.
func TestRejectsZipThatIsNotAnEPUB(t *testing.T) {
	e := newEnv(t, library.KindEbook)
	raw, err := fixtures.ZipWithEntries(map[string][]byte{"readme.txt": []byte("hello")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.accept(t, "", file("plain.epub", raw)); err == nil {
		t.Fatal("a plain zip was accepted as an EPUB")
	} else if !strings.Contains(err.Error(), "mimetype") {
		t.Errorf("error = %v, want a mimetype complaint", err)
	}
}

// An EPUB whose mimetype entry is neither first nor stored is accepted, which
// is what real books in circulation need, and logged.
func TestAcceptsLenientEPUBMimetype(t *testing.T) {
	e := newEnv(t, library.KindEbook)
	strict := epubBytes(t, fixtures.EPUBOptions{Title: "Lenient", Authors: []string{"A. Writer"}})

	// Rebuild the same archive with the mimetype entry moved to the end and
	// deflated, which is exactly what the specification forbids.
	zr, err := zip.NewReader(bytes.NewReader(strict), int64(len(strict)))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	copyEntry := func(f *zip.File) {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		defer rc.Close()
		w, err := zw.CreateHeader(&zip.FileHeader{Name: f.Name, Method: zip.Deflate})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(w, rc); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range zr.File {
		if f.Name != "mimetype" {
			copyEntry(f)
		}
	}
	for _, f := range zr.File {
		if f.Name == "mimetype" {
			copyEntry(f)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := e.accept(t, "", file("lenient.epub", buf.Bytes())); err != nil {
		t.Fatalf("a valid EPUB with a misplaced mimetype was refused: %v", err)
	}
}

// The archive limits in internal/epub are what stop a decompression bomb, and
// the upload path applies them by parsing with the same reader the scanner
// uses rather than a laxer one of its own.
func TestRejectsZipBomb(t *testing.T) {
	e := newEnv(t, library.KindEbook)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	mimetype, err := zw.CreateHeader(&zip.FileHeader{Name: "mimetype", Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mimetype.Write([]byte("application/epub+zip")); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10_100; i++ {
		w, err := zw.Create(fmt.Sprintf("OEBPS/pad%05d.xhtml", i))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte("<p/>")); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := e.accept(t, "", file("bomb.epub", buf.Bytes())); err == nil {
		t.Fatal("an archive over the entry limit was accepted")
	} else if !strings.Contains(err.Error(), "entries") {
		t.Errorf("error = %v, want the entry-count limit", err)
	}
}

// The size cap is proved with a small cap rather than a real 200 MiB file.
func TestRejectsOversized(t *testing.T) {
	e := newEnv(t, library.KindMixed)
	e.svc.SetLimits(upload.Limits{MaxEbookBytes: 1024, MaxAudioBytes: 1024})

	body := epubBytes(t, fixtures.EPUBOptions{Title: "Too big", Authors: []string{"A. Writer"}})
	if len(body) <= 1024 {
		t.Fatalf("the fixture is only %d bytes; the cap would not be reached", len(body))
	}
	_, err := e.accept(t, "", file("big.epub", body))
	if err == nil {
		t.Fatal("a file over the cap was accepted")
	}
	if !strings.Contains(err.Error(), "larger than the limit") {
		t.Errorf("error = %v, want the size cap", err)
	}
	// Nothing was left behind in the library, staging directory included.
	if entries, err := os.ReadDir(e.root); err == nil {
		for _, entry := range entries {
			if entry.Name() == ".gbs-incoming" {
				staged, _ := os.ReadDir(filepath.Join(e.root, entry.Name()))
				if len(staged) != 0 {
					t.Errorf("the rejected upload was left in staging: %d files", len(staged))
				}
				continue
			}
			t.Errorf("a rejected upload left %q in the library", entry.Name())
		}
	}
}

func TestRejectsEmptyFile(t *testing.T) {
	e := newEnv(t, library.KindEbook)
	if _, err := e.accept(t, "", file("empty.epub", nil)); err == nil {
		t.Fatal("an empty file was accepted")
	}
}

// Neither the subfolder nor the client's filename can escape the library root.
func TestRejectsTraversal(t *testing.T) {
	body := epubBytes(t, fixtures.EPUBOptions{Title: "Escape", Authors: []string{"A. Writer"}})

	t.Run("subfolder", func(t *testing.T) {
		for _, subdir := range []string{"../outside", "..", "../../etc", `..\..\windows`, "a/b", "/etc", "."} {
			e := newEnv(t, library.KindEbook)
			if _, err := e.accept(t, subdir, file("book.epub", body)); err == nil {
				t.Errorf("subfolder %q was accepted", subdir)
			}
			if _, err := os.Stat(filepath.Join(e.root, "..", "outside")); err == nil {
				t.Errorf("subfolder %q created a directory outside the library", subdir)
			}
		}
	})

	t.Run("filename", func(t *testing.T) {
		// The client's name is only ever read for its extension, so a traversal
		// in it is not so much refused as ignored: the file lands under the
		// name the book's own metadata produced.
		e := newEnv(t, library.KindEbook)
		for _, name := range []string{
			"../../evil.epub", `..\..\evil.epub`, "/etc/cron.d/evil.epub", "....//....//evil.epub",
		} {
			accepted, err := e.accept(t, "", upload.Incoming{Filename: name, Body: bytes.NewReader(body)})
			if err != nil {
				t.Fatalf("%q: %v", name, err)
			}
			if strings.ContainsAny(accepted[0].Filename, `/\`) {
				t.Errorf("%q produced the on-disk name %q", name, accepted[0].Filename)
			}
			if _, err := os.Stat(filepath.Join(e.root, "..", "evil.epub")); err == nil {
				t.Fatalf("%q wrote outside the library", name)
			}
		}
	})

	t.Run("a valid subfolder is used", func(t *testing.T) {
		e := newEnv(t, library.KindEbook)
		accepted, err := e.accept(t, "New arrivals", file("book.epub", body))
		if err != nil {
			t.Fatalf("accept: %v", err)
		}
		if accepted[0].Filename != "New arrivals/A. Writer - Escape.epub" {
			t.Errorf("filed as %q", accepted[0].Filename)
		}
	})
}

// The same bytes twice is one book, whatever it is called the second time.
func TestRejectsDuplicate(t *testing.T) {
	e := newEnv(t, library.KindEbook)
	body := epubBytes(t, fixtures.EPUBOptions{Title: "Only once", Authors: []string{"A. Writer"}})

	accepted, err := e.accept(t, "", file("first.epub", body))
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	resolved, _ := e.svc.ScanAndResolve(e.ctx, e.lib.ID, accepted, 30*time.Second)

	_, err = e.accept(t, "", file("a-different-name.epub", body))
	var dup *upload.DuplicateError
	if !errors.As(err, &dup) {
		t.Fatalf("second upload = %v, want a DuplicateError", err)
	}
	if dup.ItemID != resolved[0].ItemID {
		t.Errorf("duplicate names item %d, want %d", dup.ItemID, resolved[0].ItemID)
	}

	// The same bytes sent twice inside one request are caught too, before
	// anything is renamed into the library.
	e2 := newEnv(t, library.KindEbook)
	if _, err := e2.accept(t, "", file("a.epub", body), file("b.epub", body)); err == nil {
		t.Error("the same file twice in one request was accepted")
	}
}

func TestSafeName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"The Long Afternoon", "The Long Afternoon"},
		{"Café Society", "Cafe Society"},
		{"a/b\\c:d*e?f\"g<h>i|j", "abcdefghij"},
		{"  padded  name  ", "padded name"},
		{"...leading dots", "leading dots"},
		{"trailing dots...", "trailing dots"},
		{"tab\tand\nnewline", "tab and newline"},
		{"", ""},
		{"..", ""},
		{strings.Repeat("x", 200), strings.Repeat("x", 80)},
	}
	for _, c := range cases {
		if got := upload.SafeName(c.in); got != c.want {
			t.Errorf("SafeName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestBookBase(t *testing.T) {
	cases := []struct{ author, title, fallback, want string }{
		{"A. Writer", "The Long Afternoon", "x", "A. Writer - The Long Afternoon"},
		{"", "The Long Afternoon", "x", "The Long Afternoon"},
		{"A. Writer", "", "x", "A. Writer"},
		{"", "", "from-the-filename", "from-the-filename"},
		{"", "", "", "Untitled"},
		{"/../", "/../", "", "Untitled"},
	}
	for _, c := range cases {
		if got := upload.BookBase(c.author, c.title, c.fallback); got != c.want {
			t.Errorf("BookBase(%q, %q, %q) = %q, want %q", c.author, c.title, c.fallback, got, c.want)
		}
	}
}

func TestSniff(t *testing.T) {
	epub := epubBytes(t, fixtures.EPUBOptions{Title: "Sniff"})
	cases := []struct {
		name string
		body []byte
		want string
	}{
		{"epub", epub, "epub"},
		{"mp3", fixtures.MP3Bytes(fixtures.MP3Options{Title: "x", Frames: 20}), "mp3"},
		{"m4b", fixtures.M4BBytes(fixtures.M4BOptions{Title: "x", DurationMS: 1000}), "mp4"},
		{"html", []byte("<!doctype html><title>x</title>"), ""},
		{"text", []byte("just words"), ""},
		{"empty", nil, ""},
	}
	for _, c := range cases {
		if got := upload.Sniff(c.body); got != c.want {
			t.Errorf("Sniff(%s) = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestAcceptedExtensions(t *testing.T) {
	if got := upload.AcceptedExtensions(library.KindEbook); strings.Join(got, ",") != ".epub" {
		t.Errorf("ebook extensions = %v", got)
	}
	if got := upload.AcceptedExtensions(library.KindMixed); len(got) != 4 {
		t.Errorf("mixed extensions = %v, want all four", got)
	}
	if got := upload.AcceptedExtensions("nonsense"); got != nil {
		t.Errorf("an unknown kind accepts %v", got)
	}
}
