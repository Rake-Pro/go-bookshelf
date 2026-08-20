package library_test

import (
	"context"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rake-pro/go-bookshelf/internal/fixtures"
	"github.com/rake-pro/go-bookshelf/internal/images"
	"github.com/rake-pro/go-bookshelf/internal/library"
	"github.com/rake-pro/go-bookshelf/internal/store"
)

type env struct {
	ctx     context.Context
	db      *store.DB
	cat     *library.Catalog
	scanner *library.Scanner
	covers  *images.Store
	root    string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	covers, err := images.NewStore(filepath.Join(dir, "covers"))
	if err != nil {
		t.Fatalf("cover store: %v", err)
	}
	cat := library.NewCatalog(db)
	root := filepath.Join(dir, "media")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	return &env{ctx: ctx, db: db, cat: cat, scanner: library.NewScanner(cat, covers), covers: covers, root: root}
}

func TestScanIngestsEbook(t *testing.T) {
	e := newEnv(t)
	err := fixtures.WriteEPUB(filepath.Join(e.root, "afternoon.epub"), fixtures.EPUBOptions{
		Title:       "The Long Afternoon",
		Authors:     []string{"A. Writer"},
		Language:    "en",
		Publisher:   "Example Press",
		Date:        "2019",
		ISBN:        "9781234567897",
		Series:      "Afternoons",
		SeriesIndex: 2,
		Tags:        []string{"Fiction"},
		Cover:       fixtures.PNG(40, 60, color.RGBA{B: 200, A: 255}),
	})
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}

	lib, err := e.cat.CreateLibrary(e.ctx, "Books", library.KindEbook, []string{e.root})
	if err != nil {
		t.Fatalf("create library: %v", err)
	}
	run, err := e.scanner.ScanLibrary(e.ctx, lib.ID)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if run.Added != 1 || run.Errors != 0 {
		t.Fatalf("scan run = %+v", run)
	}

	items, total, err := e.cat.ListItems(e.ctx, library.ListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("total = %d, items = %d", total, len(items))
	}
	it := items[0]
	if it.Title != "The Long Afternoon" {
		t.Errorf("title = %q", it.Title)
	}
	// "The " is dropped so the book files under L.
	if it.SortTitle != "Long Afternoon" {
		t.Errorf("sort title = %q", it.SortTitle)
	}
	if len(it.Authors) != 1 || it.Authors[0] != "A. Writer" {
		t.Errorf("authors = %v", it.Authors)
	}
	if it.Series == nil || it.Series.Name != "Afternoons" || it.Series.Sequence != 2 {
		t.Errorf("series = %+v", it.Series)
	}
	if it.CoverURL == "" {
		t.Error("expected a cover URL")
	}
	if it.Kind != library.KindEbook {
		t.Errorf("kind = %q", it.Kind)
	}

	detail, err := e.cat.Item(e.ctx, it.ID, 0)
	if err != nil {
		t.Fatalf("item: %v", err)
	}
	if detail.ISBN != "9781234567897" || detail.Publisher != "Example Press" {
		t.Errorf("detail = %+v", detail)
	}
	if len(detail.Files) != 1 || detail.Files[0].Format != "epub" {
		t.Errorf("files = %+v", detail.Files)
	}
	if len(detail.Tags) != 1 || detail.Tags[0].Name != "Fiction" {
		t.Errorf("tags = %+v", detail.Tags)
	}
	// The detail record exposes a filename, never the on-disk path.
	if detail.Files[0].Filename != "afternoon.epub" {
		t.Errorf("filename = %q", detail.Files[0].Filename)
	}

	// A rescan with nothing changed must not churn the catalog.
	run2, err := e.scanner.ScanLibrary(e.ctx, lib.ID)
	if err != nil {
		t.Fatalf("rescan: %v", err)
	}
	if run2.Added != 0 || run2.Updated != 0 || run2.Removed != 0 {
		t.Errorf("incremental rescan = %+v, want no changes", run2)
	}
}

func TestScanIngestsAudiobookDirectory(t *testing.T) {
	e := newEnv(t)
	dir := filepath.Join(e.root, "The Long Afternoon")
	for i, name := range []string{"part-1.mp3", "part-2.mp3", "part-10.mp3"} {
		err := fixtures.WriteMP3(filepath.Join(dir, name), fixtures.MP3Options{
			Title:       "Part",
			Album:       "The Long Afternoon",
			AlbumArtist: "A. Writer",
			Narrator:    "C. Reader",
			Track:       i + 1,
			TrackTotal:  3,
			Frames:      40,
		})
		if err != nil {
			t.Fatalf("fixture: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "cover.png"), fixtures.PNG(30, 30, color.RGBA{R: 250, A: 255}), 0o644); err != nil {
		t.Fatal(err)
	}

	lib, err := e.cat.CreateLibrary(e.ctx, "Listening", library.KindAudiobook, []string{e.root})
	if err != nil {
		t.Fatalf("create library: %v", err)
	}
	run, err := e.scanner.ScanLibrary(e.ctx, lib.ID)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if run.Added != 1 {
		t.Fatalf("scan run = %+v, want one audiobook", run)
	}

	items, _, err := e.cat.ListItems(e.ctx, library.ListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want the directory ingested as one item", len(items))
	}
	it := items[0]
	if it.Kind != library.KindAudiobook {
		t.Errorf("kind = %q", it.Kind)
	}
	if it.Title != "The Long Afternoon" {
		t.Errorf("title = %q", it.Title)
	}
	if len(it.Narrators) != 1 || it.Narrators[0] != "C. Reader" {
		t.Errorf("narrators = %v", it.Narrators)
	}
	if it.DurationMS <= 0 {
		t.Errorf("duration = %d", it.DurationMS)
	}

	detail, err := e.cat.Item(e.ctx, it.ID, 0)
	if err != nil {
		t.Fatalf("item: %v", err)
	}
	if len(detail.Files) != 3 {
		t.Fatalf("files = %d", len(detail.Files))
	}
	// Track tags decide the order, so part-10 tagged as track 3 comes last.
	want := []string{"part-1.mp3", "part-2.mp3", "part-10.mp3"}
	for i, f := range detail.Files {
		if f.Filename != want[i] {
			t.Errorf("file %d = %q, want %q", i, f.Filename, want[i])
		}
		if len(f.Chapters) != 1 {
			t.Errorf("file %d chapters = %+v, want one per file", i, f.Chapters)
		}
	}
}

func TestScanSingleM4BAtRootIsOneItem(t *testing.T) {
	e := newEnv(t)
	err := fixtures.WriteM4B(filepath.Join(e.root, "afternoon.m4b"), fixtures.M4BOptions{
		Title:       "The Long Afternoon",
		Album:       "The Long Afternoon",
		AlbumArtist: "A. Writer",
		Narrator:    "C. Reader",
		DurationMS:  3_600_000,
		Chapters: []fixtures.M4BChapter{
			{Title: "One", StartMS: 0},
			{Title: "Two", StartMS: 1_800_000},
		},
	})
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}

	lib, _ := e.cat.CreateLibrary(e.ctx, "Listening", library.KindAudiobook, []string{e.root})
	if _, err := e.scanner.ScanLibrary(e.ctx, lib.ID); err != nil {
		t.Fatalf("scan: %v", err)
	}
	items, _, err := e.cat.ListItems(e.ctx, library.ListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d", len(items))
	}
	detail, err := e.cat.Item(e.ctx, items[0].ID, 0)
	if err != nil {
		t.Fatalf("item: %v", err)
	}
	if len(detail.Files) != 1 || len(detail.Files[0].Chapters) != 2 {
		t.Fatalf("files = %+v", detail.Files)
	}
	if detail.Files[0].Chapters[1].Title != "Two" {
		t.Errorf("chapters = %+v", detail.Files[0].Chapters)
	}
	if detail.DurationMS != 3_600_000 {
		t.Errorf("duration = %d", detail.DurationMS)
	}
}

func TestMixedLibraryAndMissingLifecycle(t *testing.T) {
	e := newEnv(t)
	epubPath := filepath.Join(e.root, "one.epub")
	if err := fixtures.WriteEPUB(epubPath, fixtures.EPUBOptions{Title: "One", Authors: []string{"A. Writer"}}); err != nil {
		t.Fatal(err)
	}
	if err := fixtures.WriteM4B(filepath.Join(e.root, "two.m4b"), fixtures.M4BOptions{Title: "Two", Album: "Two"}); err != nil {
		t.Fatal(err)
	}

	lib, _ := e.cat.CreateLibrary(e.ctx, "Everything", library.KindMixed, []string{e.root})
	run, err := e.scanner.ScanLibrary(e.ctx, lib.ID)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if run.Added != 2 {
		t.Fatalf("added = %d, want both kinds", run.Added)
	}

	if err := os.Remove(epubPath); err != nil {
		t.Fatal(err)
	}
	run2, err := e.scanner.ScanLibrary(e.ctx, lib.ID)
	if err != nil {
		t.Fatalf("rescan: %v", err)
	}
	if run2.Removed != 1 {
		t.Fatalf("removed = %d, want the deleted ebook flagged", run2.Removed)
	}

	// A missing item drops out of the default listing but is not deleted yet.
	items, total, err := e.cat.ListItems(e.ctx, library.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(items) != 1 {
		t.Errorf("visible items = %d, want only the remaining audiobook", total)
	}
	all, _, err := e.cat.ListItems(e.ctx, library.ListOptions{IncludeMissing: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Errorf("items including missing = %d, want 2", len(all))
	}

	// Putting the file back clears the flag.
	if err := fixtures.WriteEPUB(epubPath, fixtures.EPUBOptions{Title: "One", Authors: []string{"A. Writer"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.scanner.ScanLibrary(e.ctx, lib.ID); err != nil {
		t.Fatalf("rescan: %v", err)
	}
	_, total, err = e.cat.ListItems(e.ctx, library.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Errorf("total after restore = %d, want 2", total)
	}
}

func TestJanitorDeletesLongMissingItemsAndKeepsProgress(t *testing.T) {
	e := newEnv(t)
	epubPath := filepath.Join(e.root, "gone.epub")
	if err := fixtures.WriteEPUB(epubPath, fixtures.EPUBOptions{Title: "Gone"}); err != nil {
		t.Fatal(err)
	}
	lib, _ := e.cat.CreateLibrary(e.ctx, "Books", library.KindEbook, []string{e.root})
	if _, err := e.scanner.ScanLibrary(e.ctx, lib.ID); err != nil {
		t.Fatal(err)
	}
	items, _, _ := e.cat.ListItems(e.ctx, library.ListOptions{})
	itemID := items[0].ID

	if _, err := e.db.ExecContext(e.ctx,
		`INSERT INTO users (username, role, created_at) VALUES ('reader', 'user', ?)`, store.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.cat.SaveProgress(e.ctx, 1, library.Progress{ItemID: itemID, Percent: 0.42, PositionMS: 1000}); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(epubPath); err != nil {
		t.Fatal(err)
	}
	if _, err := e.scanner.ScanLibrary(e.ctx, lib.ID); err != nil {
		t.Fatal(err)
	}

	janitor := library.NewJanitor(e.cat, e.covers)
	deleted, err := janitor.Run(e.ctx)
	if err != nil {
		t.Fatalf("janitor: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("janitor deleted %d items inside the grace period", deleted)
	}

	// Age the missing marker past the grace period.
	old := store.FormatTime(time.Now().Add(-library.MissingGrace - time.Hour))
	if _, err := e.db.ExecContext(e.ctx, `UPDATE items SET missing_at = ? WHERE id = ?`, old, itemID); err != nil {
		t.Fatal(err)
	}
	deleted, err = janitor.Run(e.ctx)
	if err != nil {
		t.Fatalf("janitor: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("janitor deleted %d items, want 1", deleted)
	}

	var archived int
	if err := e.db.QueryRowContext(e.ctx, `SELECT count(*) FROM progress_archive`).Scan(&archived); err != nil {
		t.Fatal(err)
	}
	if archived != 1 {
		t.Fatalf("archived progress rows = %d, want the position retained", archived)
	}

	// The book comes back at the same path: the position is restored.
	if err := fixtures.WriteEPUB(epubPath, fixtures.EPUBOptions{Title: "Gone"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.scanner.ScanLibrary(e.ctx, lib.ID); err != nil {
		t.Fatal(err)
	}
	restored, err := e.cat.ProgressSince(e.ctx, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(restored) != 1 || restored[0].Percent != 0.42 {
		t.Fatalf("restored progress = %+v", restored)
	}
}

func TestWithinRoots(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "sub", "book.epub")
	if err := os.MkdirAll(filepath.Dir(inside), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "secret.epub")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if !library.WithinRoots(inside, []string{root}) {
		t.Error("a file inside the library root should be allowed")
	}
	if library.WithinRoots(outside, []string{root}) {
		t.Error("a file outside every library root must be refused")
	}
	if library.WithinRoots(filepath.Join(root, "..", "etc", "passwd"), []string{root}) {
		t.Error("a traversal out of the root must be refused")
	}
	if library.WithinRoots("/etc/passwd", []string{root}) {
		t.Error("an absolute path outside the root must be refused")
	}

	// A symlink planted inside the library pointing out of it is refused,
	// because both sides are resolved before comparison.
	link := filepath.Join(root, "escape.epub")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if library.WithinRoots(link, []string{root}) {
		t.Error("a symlink escaping the library root must be refused")
	}
}

func TestSettingsNormalizeClampsRanges(t *testing.T) {
	s := library.DefaultSettings()
	s.Reader.FontScale = 9
	s.Reader.Theme = "neon"
	s.Reader.Layout = "spiral"
	s.Reader.CustomBG = "javascript:alert(1)"
	s.Player.Speed = 42
	s.Player.SkipBackS = 9999
	s.Normalize()

	if s.Reader.FontScale != 2.5 {
		t.Errorf("font_scale = %v, want the 2.5 ceiling", s.Reader.FontScale)
	}
	if s.Reader.Theme != "light" || s.Reader.Layout != "paginated" {
		t.Errorf("unknown enum values were not replaced: %+v", s.Reader)
	}
	if s.Reader.CustomBG != "#faf8f4" {
		t.Errorf("custom_bg = %q, want a rejected value replaced by the default", s.Reader.CustomBG)
	}
	if s.Player.Speed != 3.0 {
		t.Errorf("speed = %v, want the 3.0 ceiling", s.Player.Speed)
	}
	if s.Player.SkipBackS != 120 {
		t.Errorf("skip_back_s = %v", s.Player.SkipBackS)
	}

	// Snapping to a step must not leak binary float noise into the response.
	s.UI.TextScale = 1.2
	s.Reader.FontScale = 1.3
	s.Player.Speed = 1.35
	s.Normalize()
	for name, got := range map[string]float64{
		"ui.text_scale":     s.UI.TextScale,
		"reader.font_scale": s.Reader.FontScale,
		"player.speed":      s.Player.Speed,
	} {
		if got != math.Round(got*1e6)/1e6 {
			t.Errorf("%s = %v, want a value free of float noise", name, got)
		}
	}
	if s.UI.TextScale != 1.2 || s.Reader.FontScale != 1.3 || s.Player.Speed != 1.35 {
		t.Errorf("in-range step values were altered: ui=%v reader=%v player=%v",
			s.UI.TextScale, s.Reader.FontScale, s.Player.Speed)
	}
}
