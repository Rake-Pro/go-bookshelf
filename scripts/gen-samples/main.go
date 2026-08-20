// Command gen-samples writes a small synthetic library - one EPUB, one M4B and
// one multi-file MP3 audiobook - into a directory, so the smoke test has
// something to ingest without shipping real books in the repository.
package main

import (
	"flag"
	"fmt"
	"image/color"
	"os"
	"path/filepath"

	"github.com/rake-pro/go-bookshelf/internal/fixtures"
)

func main() {
	dir := flag.String("dir", "", "directory to write the sample library into")
	flag.Parse()

	if *dir == "" {
		fmt.Fprintln(os.Stderr, "gen-samples: -dir is required")
		os.Exit(2)
	}
	if err := generate(*dir); err != nil {
		fmt.Fprintln(os.Stderr, "gen-samples:", err)
		os.Exit(1)
	}
	fmt.Println("sample library written to", *dir)
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
