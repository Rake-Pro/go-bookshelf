package library

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rake-pro/go-bookshelf/internal/audio"
	"github.com/rake-pro/go-bookshelf/internal/images"
	"github.com/rake-pro/go-bookshelf/internal/store"
	"github.com/rs/zerolog/log"
)

// maxWalkEntries bounds a single library walk so a pathological tree cannot
// spin the scanner forever.
const maxWalkEntries = 500_000

// Scanner ingests library paths into the catalog.
type Scanner struct {
	cat    *Catalog
	covers *images.Store
}

// NewScanner builds a scanner writing covers into the given cache.
func NewScanner(cat *Catalog, covers *images.Store) *Scanner {
	return &Scanner{cat: cat, covers: covers}
}

// candidate is one prospective item found on disk.
type candidate struct {
	kind      string
	sourceKey string // .epub path, or the audiobook directory
	dir       string
	files     []scannedFile
}

type scannedFile struct {
	path  string
	size  int64
	mtime time.Time
}

// ScanAll rescans every library.
func (s *Scanner) ScanAll(ctx context.Context) error {
	libs, err := s.cat.Libraries(ctx, nil)
	if err != nil {
		return err
	}
	for _, l := range libs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if _, err := s.ScanLibrary(ctx, l.ID); err != nil {
			log.Error().Err(err).Int64("library", l.ID).Msg("library scan failed")
		}
	}
	return nil
}

// ScanLibrary walks a library's paths and reconciles the catalog with what it
// finds. It is incremental: files whose size and modification time are
// unchanged are not re-parsed.
func (s *Scanner) ScanLibrary(ctx context.Context, libraryID int64) (ScanRun, error) {
	lib, err := s.cat.LibraryByID(ctx, libraryID)
	if err != nil {
		return ScanRun{}, err
	}

	run := ScanRun{LibraryID: libraryID, StartedAt: store.Now()}
	res, err := s.cat.db.ExecContext(ctx,
		`INSERT INTO scan_runs (library_id, started_at) VALUES (?, ?)`, libraryID, run.StartedAt)
	if err != nil {
		return run, err
	}
	if run.ID, err = res.LastInsertId(); err != nil {
		return run, err
	}

	candidates, walkErrors := s.discover(ctx, lib)
	run.Errors = walkErrors

	seen := make(map[string]bool, len(candidates))
	for _, c := range candidates {
		if ctx.Err() != nil {
			break
		}
		seen[c.sourceKey] = true
		status, err := s.ingest(ctx, lib, c)
		switch {
		case err != nil:
			run.Errors++
			log.Warn().Err(err).Str("source", c.sourceKey).Msg("ingest failed")
		case status == statusAdded:
			run.Added++
		case status == statusUpdated:
			run.Updated++
		}
	}

	removed, err := s.markMissing(ctx, libraryID, seen)
	if err != nil {
		return run, err
	}
	run.Removed = removed
	run.FinishedAt = store.Now()

	if _, err := s.cat.db.ExecContext(ctx,
		`UPDATE scan_runs SET finished_at = ?, added = ?, updated = ?, removed = ?, errors = ? WHERE id = ?`,
		run.FinishedAt, run.Added, run.Updated, run.Removed, run.Errors, run.ID); err != nil {
		return run, err
	}
	log.Info().Int64("library", libraryID).Int("added", run.Added).Int("updated", run.Updated).
		Int("removed", run.Removed).Int("errors", run.Errors).Msg("library scan finished")
	return run, nil
}

// discover walks the library's roots and groups files into candidate items.
func (s *Scanner) discover(ctx context.Context, lib *Library) ([]candidate, int) {
	var (
		out       []candidate
		errCount  int
		audioDirs = map[string][]scannedFile{}
		entries   int
	)

	wantEbooks := lib.Kind == KindEbook || lib.Kind == KindMixed
	wantAudio := lib.Kind == KindAudiobook || lib.Kind == KindMixed

	for _, root := range lib.Paths {
		absRoot, err := filepath.Abs(root)
		if err != nil {
			errCount++
			continue
		}
		info, err := os.Stat(absRoot)
		if err != nil || !info.IsDir() {
			log.Warn().Str("path", root).Msg("library path is not a readable directory")
			errCount++
			continue
		}

		err = filepath.WalkDir(absRoot, func(path string, d fs.DirEntry, err error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				errCount++
				return nil
			}
			if entries++; entries > maxWalkEntries {
				return fmt.Errorf("library: walk of %s exceeded %d entries", absRoot, maxWalkEntries)
			}
			name := d.Name()
			// Symlinks are never followed: a link inside the library could
			// otherwise pull in arbitrary parts of the filesystem.
			if d.Type()&fs.ModeSymlink != 0 {
				return nil
			}
			if d.IsDir() {
				if path != absRoot && strings.HasPrefix(name, ".") {
					return fs.SkipDir
				}
				return nil
			}
			if strings.HasPrefix(name, ".") {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				errCount++
				return nil
			}
			sf := scannedFile{path: path, size: info.Size(), mtime: info.ModTime()}

			switch {
			case wantEbooks && strings.EqualFold(filepath.Ext(name), ".epub"):
				out = append(out, candidate{
					kind: KindEbook, sourceKey: path, dir: filepath.Dir(path), files: []scannedFile{sf},
				})
			case wantAudio && audio.IsAudioFile(name):
				dir := filepath.Dir(path)
				// A loose audio file directly in a library root is its own
				// item; anything in a subdirectory joins that directory.
				if dir == absRoot {
					out = append(out, candidate{
						kind: KindAudiobook, sourceKey: path, dir: dir, files: []scannedFile{sf},
					})
					return nil
				}
				audioDirs[dir] = append(audioDirs[dir], sf)
			}
			return nil
		})
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Warn().Err(err).Str("path", absRoot).Msg("library walk stopped early")
			errCount++
		}
	}

	for dir, files := range audioDirs {
		sortAudioFiles(files)
		out = append(out, candidate{kind: KindAudiobook, sourceKey: dir, dir: dir, files: files})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].sourceKey < out[j].sourceKey })
	return out, errCount
}

type ingestStatus int

const (
	statusUnchanged ingestStatus = iota
	statusAdded
	statusUpdated
)

// ingest creates or refreshes the item for one candidate.
func (s *Scanner) ingest(ctx context.Context, lib *Library, c candidate) (ingestStatus, error) {
	var (
		itemID  int64
		existed bool
	)
	err := s.cat.db.QueryRowContext(ctx,
		`SELECT id FROM items WHERE library_id = ? AND source_key = ?`, lib.ID, c.sourceKey).Scan(&itemID)
	switch {
	case err == nil:
		existed = true
	case errors.Is(err, sql.ErrNoRows):
	default:
		return statusUnchanged, err
	}

	if existed {
		unchanged, err := s.filesUnchanged(ctx, itemID, c.files)
		if err != nil {
			return statusUnchanged, err
		}
		if unchanged {
			if _, err := s.cat.db.ExecContext(ctx,
				`UPDATE items SET missing_at = NULL WHERE id = ? AND missing_at IS NOT NULL`, itemID); err != nil {
				return statusUnchanged, err
			}
			return statusUnchanged, nil
		}
	}

	meta, err := s.extract(ctx, c)
	if err != nil {
		return statusUnchanged, err
	}

	tx, err := s.cat.db.BeginTx(ctx, nil)
	if err != nil {
		return statusUnchanged, err
	}
	defer tx.Rollback()

	now := store.Now()
	if existed {
		_, err = tx.ExecContext(ctx,
			`UPDATE items SET kind = ?, title = ?, sort_title = ?, subtitle = ?, description = ?,
				language = ?, published = ?, isbn = ?, asin = ?, publisher = ?, duration_ms = ?,
				size_bytes = ?, updated_at = ?, missing_at = NULL WHERE id = ?`,
			c.kind, meta.Title, meta.SortTitle, meta.Subtitle, meta.Description, meta.Language,
			meta.Published, meta.ISBN, meta.ASIN, meta.Publisher, meta.DurationMS, meta.SizeBytes, now, itemID)
		if err != nil {
			return statusUnchanged, err
		}
		for _, table := range []string{"item_people", "item_series", "item_tags"} {
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE item_id = ?`, itemID); err != nil {
				return statusUnchanged, err
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM files WHERE item_id = ?`, itemID); err != nil {
			return statusUnchanged, err
		}
	} else {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO items (library_id, kind, title, sort_title, subtitle, description, language,
				published, isbn, asin, publisher, duration_ms, size_bytes, source_key, added_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			lib.ID, c.kind, meta.Title, meta.SortTitle, meta.Subtitle, meta.Description, meta.Language,
			meta.Published, meta.ISBN, meta.ASIN, meta.Publisher, meta.DurationMS, meta.SizeBytes,
			c.sourceKey, now, now)
		if err != nil {
			return statusUnchanged, err
		}
		if itemID, err = res.LastInsertId(); err != nil {
			return statusUnchanged, err
		}
	}

	for i, f := range meta.Files {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO files (item_id, path, size, mtime, sha1, format, duration_ms, seq)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			itemID, f.Path, f.Size, f.MTime, f.SHA1, f.Format, f.DurationMS, i)
		if err != nil {
			return statusUnchanged, err
		}
		fileID, err := res.LastInsertId()
		if err != nil {
			return statusUnchanged, err
		}
		for seq, ch := range f.Chapters {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO chapters (file_id, seq, title, start_ms, end_ms) VALUES (?, ?, ?, ?, ?)`,
				fileID, seq, ch.Title, ch.StartMS, ch.EndMS); err != nil {
				return statusUnchanged, err
			}
		}
	}

	for role, names := range map[string][]string{
		RoleAuthor:     meta.Authors,
		RoleNarrator:   meta.Narrators,
		RoleTranslator: meta.Translators,
	} {
		for seq, name := range names {
			personID, err := upsertNamed(ctx, tx, "people", name, sortName(name))
			if err != nil {
				return statusUnchanged, err
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT OR IGNORE INTO item_people (item_id, person_id, role, seq) VALUES (?, ?, ?, ?)`,
				itemID, personID, role, seq); err != nil {
				return statusUnchanged, err
			}
		}
	}
	if meta.Series != "" {
		seriesID, err := upsertNamed(ctx, tx, "series", meta.Series, "")
		if err != nil {
			return statusUnchanged, err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO item_series (item_id, series_id, sequence) VALUES (?, ?, ?)`,
			itemID, seriesID, meta.SeriesIndex); err != nil {
			return statusUnchanged, err
		}
	}
	for _, tag := range meta.Tags {
		tagID, err := upsertNamed(ctx, tx, "tags", tag, "")
		if err != nil {
			return statusUnchanged, err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO item_tags (item_id, tag_id) VALUES (?, ?)`, itemID, tagID); err != nil {
			return statusUnchanged, err
		}
	}

	if err := tx.Commit(); err != nil {
		return statusUnchanged, err
	}

	if len(meta.Cover) > 0 && s.covers != nil {
		if err := s.saveCover(ctx, itemID, meta.Cover); err != nil {
			log.Warn().Err(err).Int64("item", itemID).Msg("cover extraction failed")
		}
	}

	if existed {
		return statusUpdated, nil
	}
	if err := s.restoreProgress(ctx, itemID, lib.ID, c.sourceKey); err != nil {
		log.Warn().Err(err).Int64("item", itemID).Msg("restoring archived progress failed")
	}
	return statusAdded, nil
}

func (s *Scanner) saveCover(ctx context.Context, itemID int64, raw []byte) error {
	full, err := s.covers.Save(ctx, itemID, "full", raw)
	if err != nil {
		return err
	}
	if _, err := s.covers.Save(ctx, itemID, "thumb", raw); err != nil {
		return err
	}
	_, err = s.cat.db.ExecContext(ctx, `UPDATE items SET cover_path = ? WHERE id = ?`, full, itemID)
	return err
}

// filesUnchanged reports whether the stored file rows match what is on disk.
func (s *Scanner) filesUnchanged(ctx context.Context, itemID int64, found []scannedFile) (bool, error) {
	rows, err := s.cat.db.QueryContext(ctx, `SELECT path, size, mtime FROM files WHERE item_id = ?`, itemID)
	if err != nil {
		return false, err
	}
	defer rows.Close()

	stored := map[string]string{}
	for rows.Next() {
		var (
			path  string
			size  int64
			mtime string
		)
		if err := rows.Scan(&path, &size, &mtime); err != nil {
			return false, err
		}
		stored[path] = fmt.Sprintf("%d|%s", size, mtime)
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	if len(stored) != len(found) {
		return false, nil
	}
	for _, f := range found {
		want, ok := stored[f.path]
		if !ok || want != fmt.Sprintf("%d|%s", f.size, store.FormatTime(f.mtime)) {
			return false, nil
		}
	}
	return true, nil
}

// markMissing flags items whose source is no longer on disk and clears the flag
// for items that came back.
func (s *Scanner) markMissing(ctx context.Context, libraryID int64, seen map[string]bool) (int, error) {
	rows, err := s.cat.db.QueryContext(ctx,
		`SELECT id, source_key, coalesce(missing_at, '') FROM items WHERE library_id = ?`, libraryID)
	if err != nil {
		return 0, err
	}
	type row struct {
		id      int64
		key     string
		missing string
	}
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.key, &r.missing); err != nil {
			rows.Close()
			return 0, err
		}
		all = append(all, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	removed := 0
	now := store.Now()
	for _, r := range all {
		switch {
		case seen[r.key] && r.missing != "":
			if _, err := s.cat.db.ExecContext(ctx, `UPDATE items SET missing_at = NULL WHERE id = ?`, r.id); err != nil {
				return removed, err
			}
		case !seen[r.key] && r.missing == "":
			if _, err := s.cat.db.ExecContext(ctx, `UPDATE items SET missing_at = ? WHERE id = ?`, now, r.id); err != nil {
				return removed, err
			}
			removed++
		}
	}
	return removed, nil
}

func upsertNamed(ctx context.Context, tx *sql.Tx, table, name, sortValue string) (int64, error) {
	// Only the three fixed catalog vocabularies reach this helper.
	switch table {
	case "people", "series", "tags":
	default:
		return 0, fmt.Errorf("library: unknown vocabulary table %q", table)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, errors.New("library: empty name")
	}
	if table == "people" {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO people (name, sort_name) VALUES (?, ?) ON CONFLICT(name) DO NOTHING`, name, sortValue); err != nil {
			return 0, err
		}
	} else {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO `+table+` (name) VALUES (?) ON CONFLICT(name) DO NOTHING`, name); err != nil {
			return 0, err
		}
	}
	var id int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM `+table+` WHERE name = ?`, name).Scan(&id); err != nil {
		return 0, err
	}
	return id, nil
}

// sortName turns "First Middle Last" into "Last, First Middle" so people file
// alphabetically under their family name.
func sortName(name string) string {
	fields := strings.Fields(name)
	if len(fields) < 2 {
		return name
	}
	last := fields[len(fields)-1]
	return last + ", " + strings.Join(fields[:len(fields)-1], " ")
}

// sortAudioFiles orders an audiobook's files by disc, then track, then a
// natural filename comparison so "chapter-2" precedes "chapter-10".
func sortAudioFiles(files []scannedFile) {
	type keyed struct {
		f     scannedFile
		disc  int
		track int
	}
	keys := make([]keyed, len(files))
	for i, f := range files {
		k := keyed{f: f}
		if m, err := audio.Probe(f.path); err == nil {
			k.disc, k.track = m.Disc, m.Track
		}
		keys[i] = k
	}
	sort.SliceStable(keys, func(i, j int) bool {
		if keys[i].disc != keys[j].disc {
			return keys[i].disc < keys[j].disc
		}
		if keys[i].track != keys[j].track {
			return keys[i].track < keys[j].track
		}
		return naturalLess(filepath.Base(keys[i].f.path), filepath.Base(keys[j].f.path))
	})
	for i, k := range keys {
		files[i] = k.f
	}
}

// naturalLess compares strings so embedded digit runs sort numerically.
func naturalLess(a, b string) bool {
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if isDigit(a[i]) && isDigit(b[j]) {
			si, sj := i, j
			for i < len(a) && isDigit(a[i]) {
				i++
			}
			for j < len(b) && isDigit(b[j]) {
				j++
			}
			na := strings.TrimLeft(a[si:i], "0")
			nb := strings.TrimLeft(b[sj:j], "0")
			if len(na) != len(nb) {
				return len(na) < len(nb)
			}
			if na != nb {
				return na < nb
			}
			continue
		}
		ca, cb := lowerByte(a[i]), lowerByte(b[j])
		if ca != cb {
			return ca < cb
		}
		i++
		j++
	}
	return len(a)-i < len(b)-j
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func lowerByte(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 32
	}
	return c
}

// hashFile returns the SHA-1 of a file, used to detect content changes when
// size and mtime alone are inconclusive.
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha1.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// WithinRoots reports whether path resolves inside one of roots. Both sides are
// resolved through symlinks first, so a link planted inside a library cannot
// be used to read files outside it.
func WithinRoots(path string, roots []string) bool {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		resolved, err = filepath.Abs(path)
		if err != nil {
			return false
		}
	}
	for _, root := range roots {
		absRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			if absRoot, err = filepath.Abs(root); err != nil {
				continue
			}
		}
		rel, err := filepath.Rel(absRoot, resolved)
		if err != nil {
			continue
		}
		if rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..") {
			return true
		}
	}
	return false
}

// LibraryRoots returns every configured path across all libraries, used to
// bound what the server will ever open from disk.
func (c *Catalog) LibraryRoots(ctx context.Context) ([]string, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT path FROM library_paths`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// sidecarJSON is the shape of an audiobook's optional metadata.json.
type sidecarJSON struct {
	Title       string   `json:"title"`
	SortTitle   string   `json:"sort_title"`
	Subtitle    string   `json:"subtitle"`
	Description string   `json:"description"`
	Authors     []string `json:"authors"`
	Narrators   []string `json:"narrators"`
	Translators []string `json:"translators"`
	Series      string   `json:"series"`
	SeriesIndex float64  `json:"series_index"`
	Publisher   string   `json:"publisher"`
	Language    string   `json:"language"`
	Published   string   `json:"published"`
	ISBN        string   `json:"isbn"`
	ASIN        string   `json:"asin"`
	Tags        []string `json:"tags"`
}

func readSidecarJSON(path string) (*sidecarJSON, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(b) > 1<<20 {
		return nil, errors.New("library: metadata.json is too large")
	}
	var s sidecarJSON
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	return &s, nil
}
