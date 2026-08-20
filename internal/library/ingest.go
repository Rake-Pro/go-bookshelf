package library

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rake-pro/go-bookshelf/internal/audio"
	"github.com/rake-pro/go-bookshelf/internal/epub"
	"github.com/rake-pro/go-bookshelf/internal/store"
)

// itemMeta is everything the scanner learned about one candidate item.
type itemMeta struct {
	Title       string
	SortTitle   string
	Subtitle    string
	Description string
	Language    string
	Published   string
	ISBN        string
	ASIN        string
	Publisher   string
	Authors     []string
	Narrators   []string
	Translators []string
	Tags        []string
	Series      string
	SeriesIndex float64
	DurationMS  int64
	SizeBytes   int64
	Cover       []byte
	Files       []metaFile
}

type metaFile struct {
	Path       string
	Size       int64
	MTime      string
	SHA1       string
	Format     string
	DurationMS int64
	Chapters   []Chapter
}

func (s *Scanner) extract(ctx context.Context, c candidate) (itemMeta, error) {
	if c.kind == KindEbook {
		return s.extractEbook(c)
	}
	return s.extractAudiobook(ctx, c)
}

func (s *Scanner) extractEbook(c candidate) (itemMeta, error) {
	f := c.files[0]
	m := itemMeta{
		Title:     titleFromFilename(f.path),
		SizeBytes: f.size,
	}

	r, err := epub.Open(f.path)
	if err != nil {
		return m, err
	}
	defer r.Close()

	book, err := r.Book()
	if err != nil {
		return m, err
	}
	applyEPUBMetadata(&m, book.Meta)

	// A sidecar metadata.opf next to the file replaces the embedded metadata.
	sidecar := filepath.Join(c.dir, "metadata.opf")
	if raw, err := os.ReadFile(sidecar); err == nil {
		if side, err := epub.ParseOPF(raw); err == nil {
			m.Authors, m.Narrators, m.Translators, m.Tags = nil, nil, nil, nil
			m.Series, m.SeriesIndex = "", 0
			applyEPUBMetadata(&m, side.Meta)
		}
	}

	if cover, _, err := r.Cover(); err == nil {
		m.Cover = cover
	}
	if sidecarCover := findDirCover(c.dir, strings.TrimSuffix(filepath.Base(f.path), filepath.Ext(f.path))); sidecarCover != nil {
		m.Cover = sidecarCover
	}

	sha, err := hashFile(f.path)
	if err != nil {
		return m, err
	}
	m.Files = []metaFile{{
		Path: f.path, Size: f.size, MTime: store.FormatTime(f.mtime),
		SHA1: sha, Format: "epub",
	}}
	if m.SortTitle == "" {
		m.SortTitle = sortableTitle(m.Title)
	}
	return m, nil
}

func applyEPUBMetadata(m *itemMeta, meta epub.Metadata) {
	if meta.Title != "" {
		m.Title = meta.Title
	}
	if meta.SortTitle != "" {
		m.SortTitle = meta.SortTitle
	}
	if meta.Subtitle != "" {
		m.Subtitle = meta.Subtitle
	}
	if meta.Description != "" {
		m.Description = meta.Description
	}
	if meta.Language != "" {
		m.Language = meta.Language
	}
	if meta.Published != "" {
		m.Published = meta.Published
	}
	if meta.ISBN != "" {
		m.ISBN = meta.ISBN
	}
	if meta.ASIN != "" {
		m.ASIN = meta.ASIN
	}
	if meta.Publisher != "" {
		m.Publisher = meta.Publisher
	}
	if meta.Series != "" {
		m.Series = meta.Series
		m.SeriesIndex = meta.SeriesIndex
	}
	m.Tags = appendUniqueStrings(m.Tags, meta.Tags...)
	for _, p := range meta.People {
		switch p.Role {
		case RoleAuthor:
			m.Authors = appendUniqueStrings(m.Authors, p.Name)
		case RoleNarrator:
			m.Narrators = appendUniqueStrings(m.Narrators, p.Name)
		case RoleTranslator:
			m.Translators = appendUniqueStrings(m.Translators, p.Name)
		}
	}
}

func (s *Scanner) extractAudiobook(ctx context.Context, c candidate) (itemMeta, error) {
	m := itemMeta{Title: titleFromFilename(c.sourceKey)}
	if fi, err := os.Stat(c.sourceKey); err == nil && fi.IsDir() {
		m.Title = filepath.Base(c.sourceKey)
	}

	for _, f := range c.files {
		probe, err := audio.Probe(f.path)
		if err != nil {
			return m, fmt.Errorf("probe %s: %w", filepath.Base(f.path), err)
		}
		sha, err := hashFile(f.path)
		if err != nil {
			return m, err
		}

		mf := metaFile{
			Path: f.path, Size: f.size, MTime: store.FormatTime(f.mtime), SHA1: sha,
			Format: audio.FormatOf(f.path), DurationMS: probe.DurationMS,
		}
		if len(probe.Chapters) > 0 {
			for i, ch := range probe.Chapters {
				mf.Chapters = append(mf.Chapters, Chapter{
					Seq: i, Title: chapterTitle(ch.Title, i), StartMS: ch.StartMS, EndMS: ch.EndMS,
				})
			}
		} else {
			// No embedded chapter list: the file itself is one chapter.
			title := probe.Title
			if title == "" {
				title = titleFromFilename(f.path)
			}
			mf.Chapters = []Chapter{{Seq: 0, Title: title, StartMS: 0, EndMS: probe.DurationMS}}
		}
		m.Files = append(m.Files, mf)

		m.DurationMS += probe.DurationMS
		m.SizeBytes += f.size

		mergeAudioMetadata(&m, probe, len(c.files) > 1)
		if m.Cover == nil && len(probe.Cover) > 0 {
			m.Cover = probe.Cover
		}
	}

	if cover := findDirCover(c.dir, "cover"); cover != nil {
		m.Cover = cover
	}
	// A sidecar metadata.json in the audiobook directory replaces the tags.
	if side, err := readSidecarJSON(filepath.Join(c.dir, "metadata.json")); err == nil {
		applySidecar(&m, side)
	}
	if m.SortTitle == "" {
		m.SortTitle = sortableTitle(m.Title)
	}
	_ = ctx
	return m, nil
}

// mergeAudioMetadata folds one file's tags into the item. For a multi-file
// audiobook the album is the work and the track title is just a chapter, so
// only the album is promoted to the item title.
func mergeAudioMetadata(m *itemMeta, probe audio.Metadata, multiFile bool) {
	switch {
	case probe.Album != "":
		m.Title = probe.Album
	case !multiFile && probe.Title != "":
		m.Title = probe.Title
	}
	if probe.Subtitle != "" && m.Subtitle == "" {
		m.Subtitle = probe.Subtitle
	}
	if probe.Description != "" && m.Description == "" {
		m.Description = probe.Description
	}
	if probe.Publisher != "" && m.Publisher == "" {
		m.Publisher = probe.Publisher
	}
	if probe.Language != "" && m.Language == "" {
		m.Language = probe.Language
	}
	if probe.Date != "" && m.Published == "" {
		m.Published = probe.Date
	}
	if probe.ASIN != "" && m.ASIN == "" {
		m.ASIN = probe.ASIN
	}
	if probe.ISBN != "" && m.ISBN == "" {
		m.ISBN = probe.ISBN
	}
	author := probe.AlbumArtist
	if author == "" {
		author = probe.Artist
	}
	if author != "" {
		m.Authors = appendUniqueStrings(m.Authors, splitNames(author)...)
	}
	if probe.Narrator != "" {
		m.Narrators = appendUniqueStrings(m.Narrators, splitNames(probe.Narrator)...)
	}
	if probe.Composer != "" && len(m.Narrators) == 0 {
		// Audiobook tools commonly park the narrator in the composer field.
		m.Narrators = appendUniqueStrings(m.Narrators, splitNames(probe.Composer)...)
	}
	m.Tags = appendUniqueStrings(m.Tags, probe.Genres...)
}

func applySidecar(m *itemMeta, s *sidecarJSON) {
	if s.Title != "" {
		m.Title = s.Title
	}
	if s.SortTitle != "" {
		m.SortTitle = s.SortTitle
	}
	if s.Subtitle != "" {
		m.Subtitle = s.Subtitle
	}
	if s.Description != "" {
		m.Description = s.Description
	}
	if s.Publisher != "" {
		m.Publisher = s.Publisher
	}
	if s.Language != "" {
		m.Language = s.Language
	}
	if s.Published != "" {
		m.Published = s.Published
	}
	if s.ISBN != "" {
		m.ISBN = s.ISBN
	}
	if s.ASIN != "" {
		m.ASIN = s.ASIN
	}
	if s.Series != "" {
		m.Series, m.SeriesIndex = s.Series, s.SeriesIndex
	}
	if len(s.Authors) > 0 {
		m.Authors = appendUniqueStrings(nil, s.Authors...)
	}
	if len(s.Narrators) > 0 {
		m.Narrators = appendUniqueStrings(nil, s.Narrators...)
	}
	if len(s.Translators) > 0 {
		m.Translators = appendUniqueStrings(nil, s.Translators...)
	}
	if len(s.Tags) > 0 {
		m.Tags = appendUniqueStrings(nil, s.Tags...)
	}
}

// findDirCover looks for cover art sitting next to the media.
func findDirCover(dir, stem string) []byte {
	names := []string{"cover", "folder", stem}
	exts := []string{".jpg", ".jpeg", ".png"}
	for _, name := range names {
		if name == "" {
			continue
		}
		for _, ext := range exts {
			path := filepath.Join(dir, name+ext)
			info, err := os.Lstat(path)
			// Lstat, not Stat: a symlinked cover would be a way to read an
			// arbitrary file through the cover endpoint.
			if err != nil || !info.Mode().IsRegular() || info.Size() > 32<<20 {
				continue
			}
			if b, err := os.ReadFile(path); err == nil {
				return b
			}
		}
	}
	return nil
}

func chapterTitle(title string, index int) string {
	if strings.TrimSpace(title) != "" {
		return title
	}
	return fmt.Sprintf("Chapter %d", index+1)
}

func titleFromFilename(path string) string {
	base := filepath.Base(path)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	base = strings.NewReplacer("_", " ", ".", " ").Replace(base)
	return strings.TrimSpace(base)
}

// sortableTitle drops a leading article so titles file under their first
// meaningful word.
func sortableTitle(title string) string {
	lower := strings.ToLower(title)
	for _, article := range []string{"the ", "a ", "an "} {
		if strings.HasPrefix(lower, article) {
			return strings.TrimSpace(title[len(article):])
		}
	}
	return title
}

func splitNames(v string) []string {
	fields := strings.FieldsFunc(v, func(r rune) bool { return r == ';' || r == '&' })
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	if len(out) == 0 && strings.TrimSpace(v) != "" {
		return []string{strings.TrimSpace(v)}
	}
	return out
}

func appendUniqueStrings(dst []string, values ...string) []string {
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		found := false
		for _, existing := range dst {
			if strings.EqualFold(existing, v) {
				found = true
				break
			}
		}
		if !found {
			dst = append(dst, v)
		}
	}
	return dst
}
