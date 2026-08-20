package audio_test

import (
	"image/color"
	"path/filepath"
	"testing"

	"github.com/rake-pro/go-bookshelf/internal/audio"
	"github.com/rake-pro/go-bookshelf/internal/fixtures"
)

func TestProbeMP3Tags(t *testing.T) {
	path := filepath.Join(t.TempDir(), "track.mp3")
	cover := fixtures.PNG(4, 6, color.RGBA{R: 200, A: 255})
	err := fixtures.WriteMP3(path, fixtures.MP3Options{
		Title:       "Chapter One",
		Artist:      "A. Writer",
		AlbumArtist: "A. Writer",
		Narrator:    "C. Reader",
		Album:       "The Long Afternoon",
		Comment:     "An unabridged recording.",
		Date:        "2019",
		Publisher:   "Example Press",
		Genre:       "Audiobook",
		Track:       2,
		TrackTotal:  9,
		Disc:        1,
		DiscTotal:   2,
		Cover:       cover,
		CoverMIME:   "image/png",
		Frames:      120,
	})
	if err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	m, err := audio.Probe(path)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if m.Format != "mp3" {
		t.Errorf("format = %q", m.Format)
	}
	if m.Title != "Chapter One" {
		t.Errorf("title = %q", m.Title)
	}
	if m.Artist != "A. Writer" || m.AlbumArtist != "A. Writer" {
		t.Errorf("artist = %q / %q", m.Artist, m.AlbumArtist)
	}
	if m.Narrator != "C. Reader" {
		t.Errorf("narrator = %q", m.Narrator)
	}
	if m.Album != "The Long Afternoon" {
		t.Errorf("album = %q", m.Album)
	}
	if m.Description != "An unabridged recording." {
		t.Errorf("description = %q", m.Description)
	}
	if m.Publisher != "Example Press" {
		t.Errorf("publisher = %q", m.Publisher)
	}
	if m.Date != "2019" {
		t.Errorf("date = %q", m.Date)
	}
	if m.Track != 2 || m.TrackTotal != 9 {
		t.Errorf("track = %d/%d", m.Track, m.TrackTotal)
	}
	if m.Disc != 1 || m.DiscTotal != 2 {
		t.Errorf("disc = %d/%d", m.Disc, m.DiscTotal)
	}
	if len(m.Genres) != 1 || m.Genres[0] != "Audiobook" {
		t.Errorf("genres = %v", m.Genres)
	}
	if len(m.Cover) != len(cover) {
		t.Errorf("cover = %d bytes, want %d", len(m.Cover), len(cover))
	}
	if m.CoverType != "image/png" {
		t.Errorf("cover type = %q", m.CoverType)
	}
}

func TestProbeMP3DurationCBR(t *testing.T) {
	const frames = 383 // ~10 s at 1152 samples / 44100 Hz
	path := filepath.Join(t.TempDir(), "cbr.mp3")
	if err := fixtures.WriteMP3(path, fixtures.MP3Options{Title: "T", Frames: frames}); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	m, err := audio.Probe(path)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	want := int64(frames) * fixtures.MP3FrameSamples * 1000 / fixtures.MP3SampleRate
	if diff := m.DurationMS - want; diff > 60 || diff < -60 {
		t.Errorf("duration = %d ms, want ~%d ms", m.DurationMS, want)
	}
}

func TestProbeMP3DurationXing(t *testing.T) {
	const declared = 2000 // frames declared by the VBR header
	path := filepath.Join(t.TempDir(), "vbr.mp3")
	err := fixtures.WriteMP3(path, fixtures.MP3Options{
		Title: "T", Frames: 20, Xing: true, FrameCount: declared,
	})
	if err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	m, err := audio.Probe(path)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	want := int64(declared) * fixtures.MP3FrameSamples * 1000 / fixtures.MP3SampleRate
	if m.DurationMS != want {
		t.Errorf("duration = %d ms, want exactly %d ms from the VBR header", m.DurationMS, want)
	}
}

func TestProbeMP3Chapters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chapters.mp3")
	err := fixtures.WriteMP3(path, fixtures.MP3Options{
		Title:  "T",
		Frames: 400,
		Chapters: []fixtures.MP3Chapter{
			{Title: "Opening", StartMS: 0, EndMS: 3000},
			{Title: "Middle", StartMS: 3000, EndMS: 7000},
			{Title: "Closing", StartMS: 7000},
		},
	})
	if err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	m, err := audio.Probe(path)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if len(m.Chapters) != 3 {
		t.Fatalf("chapters = %d, want 3: %+v", len(m.Chapters), m.Chapters)
	}
	if m.Chapters[0].Title != "Opening" || m.Chapters[0].StartMS != 0 || m.Chapters[0].EndMS != 3000 {
		t.Errorf("chapter 0 = %+v", m.Chapters[0])
	}
	if m.Chapters[2].Title != "Closing" || m.Chapters[2].StartMS != 7000 {
		t.Errorf("chapter 2 = %+v", m.Chapters[2])
	}
	// An open-ended final chapter runs to the end of the file.
	if m.Chapters[2].EndMS != m.DurationMS {
		t.Errorf("final chapter end = %d, want the file duration %d", m.Chapters[2].EndMS, m.DurationMS)
	}
}

func TestProbeM4B(t *testing.T) {
	path := filepath.Join(t.TempDir(), "book.m4b")
	cover := fixtures.JPEG(8, 12, color.RGBA{G: 180, A: 255})
	err := fixtures.WriteM4B(path, fixtures.M4BOptions{
		Title:       "The Long Afternoon",
		Album:       "The Long Afternoon",
		Artist:      "A. Writer",
		AlbumArtist: "A. Writer",
		Narrator:    "C. Reader",
		Description: "An unabridged recording.",
		Date:        "2019-04-01",
		Genre:       "Audiobook",
		Publisher:   "Example Press",
		ASIN:        "B00EXAMPLE",
		Track:       1,
		TrackTotal:  3,
		Disc:        1,
		DiscTotal:   1,
		DurationMS:  3_723_000,
		Cover:       cover,
		Chapters: []fixtures.M4BChapter{
			{Title: "Opening", StartMS: 0},
			{Title: "Middle", StartMS: 1_200_000},
			{Title: "Closing", StartMS: 2_400_000},
		},
	})
	if err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	m, err := audio.Probe(path)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if m.Format != "mp4" {
		t.Errorf("format = %q", m.Format)
	}
	if m.Title != "The Long Afternoon" || m.Album != "The Long Afternoon" {
		t.Errorf("title/album = %q / %q", m.Title, m.Album)
	}
	if m.Artist != "A. Writer" || m.AlbumArtist != "A. Writer" {
		t.Errorf("artist = %q / %q", m.Artist, m.AlbumArtist)
	}
	if m.Narrator != "C. Reader" {
		t.Errorf("narrator = %q", m.Narrator)
	}
	if m.Description != "An unabridged recording." {
		t.Errorf("description = %q", m.Description)
	}
	if m.Publisher != "Example Press" {
		t.Errorf("publisher = %q", m.Publisher)
	}
	if m.ASIN != "B00EXAMPLE" {
		t.Errorf("asin = %q", m.ASIN)
	}
	if m.Track != 1 || m.TrackTotal != 3 || m.Disc != 1 {
		t.Errorf("track/disc = %d/%d %d", m.Track, m.TrackTotal, m.Disc)
	}
	if m.DurationMS != 3_723_000 {
		t.Errorf("duration = %d ms", m.DurationMS)
	}
	if m.CoverType != "image/jpeg" || len(m.Cover) != len(cover) {
		t.Errorf("cover = %d bytes %q", len(m.Cover), m.CoverType)
	}
	if len(m.Chapters) != 3 {
		t.Fatalf("chapters = %+v", m.Chapters)
	}
	if m.Chapters[0].Title != "Opening" || m.Chapters[0].EndMS != 1_200_000 {
		t.Errorf("chapter 0 = %+v", m.Chapters[0])
	}
	if m.Chapters[2].EndMS != 3_723_000 {
		t.Errorf("final chapter end = %d, want the file duration", m.Chapters[2].EndMS)
	}
}

func TestProbeRejectsGarbage(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"junk.mp3", "junk.m4b"} {
		path := filepath.Join(dir, name)
		if err := writeFile(path, []byte("not audio at all, just some bytes")); err != nil {
			t.Fatal(err)
		}
		m, err := audio.Probe(path)
		if name == "junk.m4b" && err == nil {
			t.Errorf("%s: expected an error for a container with no moov box", name)
		}
		if name == "junk.mp3" {
			// An MP3 with no locatable frame is not an error; it simply has no
			// known duration.
			if err != nil {
				t.Errorf("%s: %v", name, err)
			}
			if m.DurationMS != 0 {
				t.Errorf("%s: duration = %d, want 0", name, m.DurationMS)
			}
		}
	}
}

func TestProbeUnsupportedExtension(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cover.txt")
	if err := writeFile(path, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := audio.Probe(path); err == nil {
		t.Fatal("expected ErrUnsupported")
	}
}

func TestIsAudioFile(t *testing.T) {
	for _, name := range []string{"a.mp3", "b.M4B", "c.m4a", "d.mp4"} {
		if !audio.IsAudioFile(name) {
			t.Errorf("IsAudioFile(%q) = false", name)
		}
	}
	for _, name := range []string{"cover.jpg", "notes.txt", "book.epub"} {
		if audio.IsAudioFile(name) {
			t.Errorf("IsAudioFile(%q) = true", name)
		}
	}
}
