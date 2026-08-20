// Command gen-samples writes a small synthetic library - one EPUB, one M4B and
// one multi-file MP3 audiobook - into a directory, so the smoke test has
// something to ingest without shipping real books in the repository.
//
// With -inbox it also writes one book that is deliberately not part of any
// library, which is what the smoke test uploads and imports; with -serve it
// then stays running as a plain file server over that directory, so the URL
// import has somewhere on the local machine to fetch from.
package main

import (
	"flag"
	"fmt"
	"image/color"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/rake-pro/go-bookshelf/internal/fixtures"
)

func main() {
	dir := flag.String("dir", "", "directory to write the sample library into")
	inbox := flag.String("inbox", "", "also write one book here, outside any library, for the upload and import steps")
	serve := flag.String("serve", "", "after writing, serve -inbox over HTTP on this address and block")
	flag.Parse()

	if *dir == "" && *inbox == "" {
		fmt.Fprintln(os.Stderr, "gen-samples: -dir or -inbox is required")
		os.Exit(2)
	}
	if *dir != "" {
		if err := generate(*dir); err != nil {
			fmt.Fprintln(os.Stderr, "gen-samples:", err)
			os.Exit(1)
		}
		fmt.Println("sample library written to", *dir)
	}
	if *inbox != "" {
		if err := generateInbox(*inbox); err != nil {
			fmt.Fprintln(os.Stderr, "gen-samples:", err)
			os.Exit(1)
		}
		fmt.Println("inbox written to", *inbox)
	}
	if *serve != "" {
		if *inbox == "" {
			fmt.Fprintln(os.Stderr, "gen-samples: -serve needs -inbox")
			os.Exit(2)
		}
		srv := &http.Server{
			Addr:              *serve,
			Handler:           http.FileServer(http.Dir(*inbox)),
			ReadHeaderTimeout: 5 * time.Second,
		}
		fmt.Println("serving", *inbox, "on", *serve)
		if err := srv.ListenAndServe(); err != nil {
			fmt.Fprintln(os.Stderr, "gen-samples:", err)
			os.Exit(1)
		}
	}
}

// generateInbox writes one book that no library holds. It has to be a
// different book from anything in the sample library, or the upload would be
// refused as a duplicate - which is correct behaviour, and not what the upload
// step is trying to prove.
func generateInbox(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// Three distinct books, because the duplicate check is server-wide: the
	// smoke test needs one to upload, one to import and one to try to write
	// outside the library with, and no two of them can be the same bytes.
	books := []struct {
		file, title, chapter string
	}{
		{"morning.epub", "The Long Morning", "The morning began, as mornings do."},
		{"noon.epub", "The Long Noon", "By noon it was warm, and nothing much had happened."},
		{"dusk.epub", "The Long Dusk", "At dusk the light went, slowly, and then all at once."},
	}
	for _, b := range books {
		if err := fixtures.WriteEPUB(filepath.Join(dir, b.file), fixtures.EPUBOptions{
			Title:       b.title,
			Authors:     []string{"A. Writer"},
			Language:    "en",
			Description: "A sample book generated for the upload and import smoke steps.",
			Publisher:   "Example Press",
			Date:        "2022",
			Chapters: map[string]string{
				"c1.xhtml": "<h1>One</h1><p>" + b.chapter + "</p>",
			},
			ChapterOrder: []string{"c1.xhtml"},
			Cover:        fixtures.PNG(400, 600, color.RGBA{R: 200, G: 140, B: 40, A: 255}),
		}); err != nil {
			return fmt.Errorf("write inbox epub: %w", err)
		}
	}
	// A file whose name claims to be a book and whose bytes are not, so the
	// smoke test can watch the magic-byte check refuse it.
	return os.WriteFile(filepath.Join(dir, "not-a-book.epub"),
		[]byte("This is a text file with an EPUB extension.\n"), 0o644)
}

func generate(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	cover := fixtures.PNG(400, 600, color.RGBA{R: 30, G: 60, B: 120, A: 255})
	if err := fixtures.WriteEPUB(filepath.Join(dir, "the-long-afternoon.epub"), fixtures.EPUBOptions{
		Title:       "The Long Afternoon",
		Subtitle:    "A Study in Idleness",
		Authors:     []string{"A. Writer"},
		Language:    "en",
		Description: "A sample book generated for the smoke test.",
		Publisher:   "Example Press",
		Date:        "2019-04-01",
		ISBN:        "9781234567897",
		Series:      "Afternoons",
		SeriesIndex: 1,
		Tags:        []string{"Fiction", "Sample"},
		Chapters: map[string]string{
			"c1.xhtml": "<h1>One</h1><p>The afternoon began slowly.</p>",
			"c2.xhtml": "<h1>Two</h1><p>And ended slower still.</p>",
		},
		ChapterOrder: []string{"c1.xhtml", "c2.xhtml"},
		Cover:        cover,
	}); err != nil {
		return fmt.Errorf("write epub: %w", err)
	}

	if err := fixtures.WriteM4B(filepath.Join(dir, "the-long-evening.m4b"), fixtures.M4BOptions{
		Title:       "The Long Evening",
		Album:       "The Long Evening",
		Artist:      "A. Writer",
		AlbumArtist: "A. Writer",
		Narrator:    "C. Reader",
		Description: "A sample audiobook generated for the smoke test.",
		Date:        "2020",
		Publisher:   "Example Press",
		DurationMS:  3_600_000,
		Cover:       fixtures.JPEG(400, 400, color.RGBA{R: 120, G: 30, B: 60, A: 255}),
		Chapters: []fixtures.M4BChapter{
			{Title: "Dusk", StartMS: 0},
			{Title: "Midnight", StartMS: 1_800_000},
		},
	}); err != nil {
		return fmt.Errorf("write m4b: %w", err)
	}

	multi := filepath.Join(dir, "The Long Night")
	for i, name := range []string{"part-1.mp3", "part-2.mp3", "part-3.mp3"} {
		if err := fixtures.WriteMP3(filepath.Join(multi, name), fixtures.MP3Options{
			Title:       fmt.Sprintf("Part %d", i+1),
			Album:       "The Long Night",
			Artist:      "A. Writer",
			AlbumArtist: "A. Writer",
			Narrator:    "C. Reader",
			Publisher:   "Example Press",
			Date:        "2021",
			Track:       i + 1,
			TrackTotal:  3,
			Frames:      400,
		}); err != nil {
			return fmt.Errorf("write mp3: %w", err)
		}
	}
	if err := os.WriteFile(filepath.Join(multi, "cover.png"),
		fixtures.PNG(300, 300, color.RGBA{R: 20, G: 90, B: 40, A: 255}), 0o644); err != nil {
		return fmt.Errorf("write cover: %w", err)
	}
	return nil
}
