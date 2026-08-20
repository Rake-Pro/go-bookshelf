package epub_test

import (
	"archive/zip"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rake-pro/go-bookshelf/internal/epub"
	"github.com/rake-pro/go-bookshelf/internal/fixtures"
)

func writeBook(t *testing.T, o fixtures.EPUBOptions) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "book.epub")
	if err := fixtures.WriteEPUB(path, o); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func TestParseMetadata(t *testing.T) {
	path := writeBook(t, fixtures.EPUBOptions{
		Title:       "The Long Afternoon",
		Subtitle:    "A Study in Idleness",
		Authors:     []string{"A. Writer", "B. Coauthor"},
		Narrator:    "C. Reader",
		Language:    "en-GB",
		Description: "A short description.",
		Publisher:   "Example Press",
		Date:        "2019-04-01",
		ISBN:        "9781234567897",
		Series:      "Afternoons",
		SeriesIndex: 2,
		Tags:        []string{"Fiction", "Slow"},
		Chapters: map[string]string{
			"c1.xhtml": "<p>One.</p>",
			"c2.xhtml": "<p>Two.</p>",
		},
		ChapterOrder: []string{"c1.xhtml", "c2.xhtml"},
		Cover:        fixtures.PNG(6, 9, colorBlue()),
	})

	r, err := epub.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r.Close()

	book, err := r.Book()
	if err != nil {
		t.Fatalf("book: %v", err)
	}
	m := book.Meta

	if m.Title != "The Long Afternoon" {
		t.Errorf("title = %q", m.Title)
	}
	if m.Subtitle != "A Study in Idleness" {
		t.Errorf("subtitle = %q", m.Subtitle)
	}
	if m.Language != "en-GB" {
		t.Errorf("language = %q", m.Language)
	}
	if m.Publisher != "Example Press" {
		t.Errorf("publisher = %q", m.Publisher)
	}
	if m.Published != "2019-04-01" {
		t.Errorf("published = %q", m.Published)
	}
	if m.ISBN != "9781234567897" {
		t.Errorf("isbn = %q", m.ISBN)
	}
	if m.Series != "Afternoons" || m.SeriesIndex != 2 {
		t.Errorf("series = %q %v", m.Series, m.SeriesIndex)
	}
	if len(m.Tags) != 2 {
		t.Errorf("tags = %v", m.Tags)
	}

	var authors, narrators []string
	for _, p := range m.People {
		switch p.Role {
		case "author":
			authors = append(authors, p.Name)
		case "narrator":
			narrators = append(narrators, p.Name)
		}
	}
	if len(authors) != 2 || authors[0] != "A. Writer" {
		t.Errorf("authors = %v", authors)
	}
	if len(narrators) != 1 || narrators[0] != "C. Reader" {
		t.Errorf("narrators = %v", narrators)
	}

	if len(book.Spine) != 2 || book.Spine[0] != "c1.xhtml" {
		t.Errorf("spine = %v", book.Spine)
	}
	if book.OPFPath != "OEBPS/content.opf" {
		t.Errorf("opf path = %q", book.OPFPath)
	}
	if book.CoverHref != "cover.png" {
		t.Errorf("cover href = %q", book.CoverHref)
	}

	cover, ct, err := r.Cover()
	if err != nil {
		t.Fatalf("cover: %v", err)
	}
	if len(cover) == 0 || !strings.HasPrefix(ct, "image/png") {
		t.Errorf("cover = %d bytes, %q", len(cover), ct)
	}
}

// Metadata is untrusted: markup in a title survives parsing as literal text
// and is never interpreted here.
func TestMetadataMarkupStaysText(t *testing.T) {
	const evil = `<script>alert(1)</script>`
	path := writeBook(t, fixtures.EPUBOptions{Title: evil, Description: evil, Authors: []string{evil}})

	r, err := epub.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r.Close()
	book, err := r.Book()
	if err != nil {
		t.Fatalf("book: %v", err)
	}
	if book.Meta.Title != evil {
		t.Errorf("title = %q, want the literal markup", book.Meta.Title)
	}
	if book.Meta.Description != evil {
		t.Errorf("description = %q", book.Meta.Description)
	}
}

func TestResolveRejectsTraversal(t *testing.T) {
	path := writeBook(t, fixtures.EPUBOptions{Title: "T"})
	r, err := epub.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r.Close()
	if _, err := r.Book(); err != nil {
		t.Fatalf("book: %v", err)
	}

	for _, bad := range []string{
		"../../etc/passwd",
		"../META-INF/container.xml",
		"/etc/passwd",
		"..\\..\\windows\\win.ini",
		"chapter1.xhtml/../../../etc/passwd",
	} {
		if name, err := r.Resolve(bad); err == nil {
			t.Errorf("Resolve(%q) resolved to %q, want an error", bad, name)
		} else if !errors.Is(err, epub.ErrUnsafePath) && !errors.Is(err, epub.ErrNotFound) {
			t.Errorf("Resolve(%q) = %v, want ErrUnsafePath or ErrNotFound", bad, err)
		}
	}

	if _, err := r.Resolve("chapter1.xhtml"); err != nil {
		t.Errorf("Resolve of a real entry failed: %v", err)
	}
}

func TestOpenRejectsUnsafeEntryNames(t *testing.T) {
	cases := map[string]string{
		"parent traversal": "../evil.txt",
		"absolute path":    "/etc/passwd",
		"nested traversal": "OEBPS/../../evil.txt",
		"windows drive":    "c:/evil.txt",
		"backslash escape": "..\\evil.txt",
	}
	for name, entry := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := fixtures.ZipWithEntries(map[string][]byte{entry: []byte("x")})
			if err != nil {
				t.Fatalf("build zip: %v", err)
			}
			zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
			if err != nil {
				t.Fatalf("zip reader: %v", err)
			}
			if _, err := epub.NewReader(zr, epub.DefaultLimits); !errors.Is(err, epub.ErrUnsafePath) {
				t.Fatalf("NewReader() = %v, want ErrUnsafePath", err)
			}
		})
	}
}

func TestArchiveLimits(t *testing.T) {
	t.Run("entry count", func(t *testing.T) {
		entries := map[string][]byte{}
		for i := 0; i < 12; i++ {
			entries["f"+string(rune('a'+i))+".txt"] = []byte("x")
		}
		zr := zipReader(t, entries)
		limits := epub.Limits{MaxEntries: 10, MaxEntrySize: 1 << 20, MaxTotalSize: 1 << 20}
		if _, err := epub.NewReader(zr, limits); !errors.Is(err, epub.ErrTooManyEntries) {
			t.Fatalf("err = %v, want ErrTooManyEntries", err)
		}
	})

	t.Run("entry size", func(t *testing.T) {
		zr := zipReader(t, map[string][]byte{"big.txt": bytes.Repeat([]byte("a"), 4096)})
		limits := epub.Limits{MaxEntries: 10, MaxEntrySize: 1024, MaxTotalSize: 1 << 20}
		if _, err := epub.NewReader(zr, limits); !errors.Is(err, epub.ErrEntryTooLarge) {
			t.Fatalf("err = %v, want ErrEntryTooLarge", err)
		}
	})

	t.Run("total size", func(t *testing.T) {
		entries := map[string][]byte{}
		for i := 0; i < 8; i++ {
			entries["f"+string(rune('a'+i))+".txt"] = bytes.Repeat([]byte("a"), 1000)
		}
		zr := zipReader(t, entries)
		limits := epub.Limits{MaxEntries: 100, MaxEntrySize: 4096, MaxTotalSize: 4000}
		if _, err := epub.NewReader(zr, limits); !errors.Is(err, epub.ErrArchiveTooBig) {
			t.Fatalf("err = %v, want ErrArchiveTooBig", err)
		}
	})

	t.Run("compression ratio bomb", func(t *testing.T) {
		// Highly compressible payload: small on disk, enormous once expanded.
		zr := zipReader(t, map[string][]byte{"bomb.txt": bytes.Repeat([]byte{0}, 4<<20)})
		limits := epub.Limits{MaxEntries: 10, MaxEntrySize: 1 << 20, MaxTotalSize: 8 << 20}
		if _, err := epub.NewReader(zr, limits); !errors.Is(err, epub.ErrEntryTooLarge) {
			t.Fatalf("err = %v, want ErrEntryTooLarge", err)
		}
	})
}

func TestMissingContainer(t *testing.T) {
	path := writeBook(t, fixtures.EPUBOptions{Title: "T", OmitContainer: true})
	r, err := epub.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r.Close()
	if _, err := r.Book(); err == nil {
		t.Fatal("expected an error for a container without META-INF/container.xml")
	}
}

func TestOpenNonZip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not.epub")
	if err := os.WriteFile(path, []byte("this is not a zip file"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := epub.Open(path); err == nil {
		t.Fatal("expected an error opening a non-zip file")
	}
}

func zipReader(t *testing.T, entries map[string][]byte) *zip.Reader {
	t.Helper()
	raw, err := fixtures.ZipWithEntries(entries)
	if err != nil {
		t.Fatalf("build zip: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("zip reader: %v", err)
	}
	return zr
}
