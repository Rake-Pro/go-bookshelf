package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"strings"

	"github.com/rake-pro/go-bookshelf/internal/store"
)

// ------------------------------------------------------------ libraries ----

// Libraries returns every library, optionally restricted to a set of ids.
func (c *Catalog) Libraries(ctx context.Context, allowed []int64) ([]Library, error) {
	query := `SELECT l.id, l.name, l.kind, l.created_at,
		(SELECT count(*) FROM items i WHERE i.library_id = l.id AND i.missing_at IS NULL)
		FROM libraries l ORDER BY l.name COLLATE NOCASE`
	var args []any
	if allowed != nil {
		if len(allowed) == 0 {
			return []Library{}, nil
		}
		placeholders := make([]string, len(allowed))
		for i, id := range allowed {
			placeholders[i] = "?"
			args = append(args, id)
		}
		query = `SELECT l.id, l.name, l.kind, l.created_at,
			(SELECT count(*) FROM items i WHERE i.library_id = l.id AND i.missing_at IS NULL)
			FROM libraries l WHERE l.id IN (` + strings.Join(placeholders, ",") + `) ORDER BY l.name COLLATE NOCASE`
	}
	rows, err := c.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	libs := []Library{}
	byID := map[int64]int{}
	var ids []any
	for rows.Next() {
		var l Library
		if err := rows.Scan(&l.ID, &l.Name, &l.Kind, &l.CreatedAt, &l.ItemCount); err != nil {
			return nil, err
		}
		l.Paths = []string{}
		byID[l.ID] = len(libs)
		ids = append(ids, l.ID)
		libs = append(libs, l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(libs) == 0 {
		return libs, nil
	}

	in := "(" + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + ")"
	pathRows, err := c.db.QueryContext(ctx,
		`SELECT library_id, path FROM library_paths WHERE library_id IN `+in+` ORDER BY path`, ids...)
	if err != nil {
		return nil, err
	}
	defer pathRows.Close()
	for pathRows.Next() {
		var (
			id   int64
			path string
		)
		if err := pathRows.Scan(&id, &path); err != nil {
			return nil, err
		}
		if idx, ok := byID[id]; ok {
			libs[idx].Paths = append(libs[idx].Paths, path)
		}
	}
	return libs, pathRows.Err()
}

// LibraryByID returns one library.
func (c *Catalog) LibraryByID(ctx context.Context, id int64) (*Library, error) {
	libs, err := c.Libraries(ctx, []int64{id})
	if err != nil {
		return nil, err
	}
	if len(libs) == 0 {
		return nil, store.ErrNotFound
	}
	return &libs[0], nil
}

// CreateLibrary inserts a library and its paths.
func (c *Catalog) CreateLibrary(ctx context.Context, name, kind string, paths []string) (*Library, error) {
	switch kind {
	case KindEbook, KindAudiobook, KindMixed:
	default:
		return nil, errors.New("library: kind must be ebook, audiobook or mixed")
	}
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("library: name is required")
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
		`INSERT INTO libraries (name, kind, created_at) VALUES (?, ?, ?)`, strings.TrimSpace(name), kind, store.Now())
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	for _, p := range cleanPaths(paths) {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO library_paths (library_id, path) VALUES (?, ?)`, id, p); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return c.LibraryByID(ctx, id)
}

// UpdateLibrary changes a library's name, kind and/or paths. Nil arguments are
// left unchanged.
func (c *Catalog) UpdateLibrary(ctx context.Context, id int64, name, kind *string, paths []string) (*Library, error) {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if name != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE libraries SET name = ? WHERE id = ?`, strings.TrimSpace(*name), id); err != nil {
			return nil, err
		}
	}
	if kind != nil {
		switch *kind {
		case KindEbook, KindAudiobook, KindMixed:
		default:
			return nil, errors.New("library: kind must be ebook, audiobook or mixed")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE libraries SET kind = ? WHERE id = ?`, *kind, id); err != nil {
			return nil, err
		}
	}
	if paths != nil {
		if _, err := tx.ExecContext(ctx, `DELETE FROM library_paths WHERE library_id = ?`, id); err != nil {
			return nil, err
		}
		for _, p := range cleanPaths(paths) {
			if _, err := tx.ExecContext(ctx,
				`INSERT OR IGNORE INTO library_paths (library_id, path) VALUES (?, ?)`, id, p); err != nil {
				return nil, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return c.LibraryByID(ctx, id)
}

// DeleteLibrary removes a library and everything ingested from it.
func (c *Catalog) DeleteLibrary(ctx context.Context, id int64) error {
	res, err := c.db.ExecContext(ctx, `DELETE FROM libraries WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return store.ErrNotFound
	}
	return nil
}

// ------------------------------------------------------------ discovery ----

// HomeFor builds the discovery payload for a user.
func (c *Catalog) HomeFor(ctx context.Context, userID int64, allowed []int64) (Home, error) {
	home := Home{Continue: []Item{}, Recent: []Item{}, SeriesInProgress: []SeriesProgress{}}

	base := ListOptions{AllowedLibraries: allowed, UserID: userID, Limit: 12}

	recent, _, err := c.ListItems(ctx, withSort(base, "added"))
	if err != nil {
		return home, err
	}
	home.Recent = recent

	inProgress, _, err := c.ListItems(ctx, withSort(base, "recent"))
	if err != nil {
		return home, err
	}
	for _, it := range inProgress {
		if it.Progress != nil && !it.Progress.Finished && it.Progress.Percent > 0 {
			home.Continue = append(home.Continue, it)
		}
	}

	series, err := c.seriesInProgress(ctx, userID, allowed)
	if err != nil {
		return home, err
	}
	home.SeriesInProgress = series
	return home, nil
}

func withSort(o ListOptions, sort string) ListOptions {
	o.Sort = sort
	return o
}

// seriesInProgress lists series where the user has finished at least one entry
// and at least one entry remains unstarted.
func (c *Catalog) seriesInProgress(ctx context.Context, userID int64, allowed []int64) ([]SeriesProgress, error) {
	out := []SeriesProgress{}
	if userID <= 0 {
		return out, nil
	}
	libFilter, args := libraryFilter("i.library_id", allowed)
	if libFilter == "" && allowed != nil && len(allowed) == 0 {
		return out, nil
	}

	query := `SELECT s.id, s.name,
			count(DISTINCT i.id),
			count(DISTINCT CASE WHEN pr.finished_at IS NOT NULL THEN i.id END)
		FROM series s
		JOIN item_series isr ON isr.series_id = s.id
		JOIN items i ON i.id = isr.item_id AND i.missing_at IS NULL
		LEFT JOIN progress pr ON pr.item_id = i.id AND pr.user_id = ?
		` + libFilter + `
		GROUP BY s.id, s.name
		HAVING count(DISTINCT CASE WHEN pr.finished_at IS NOT NULL THEN i.id END) > 0
		   AND count(DISTINCT CASE WHEN pr.item_id IS NULL THEN i.id END) > 0
		ORDER BY s.name COLLATE NOCASE
		LIMIT 12`

	rows, err := c.db.QueryContext(ctx, query, append([]any{userID}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var sp SeriesProgress
		if err := rows.Scan(&sp.Series.ID, &sp.Series.Name, &sp.Total, &sp.Finished); err != nil {
			return nil, err
		}
		sp.Series.ItemCount = sp.Total
		out = append(out, sp)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i := range out {
		next, err := c.nextUnstartedInSeries(ctx, out[i].Series.ID, userID, allowed)
		if err != nil {
			return nil, err
		}
		out[i].NextItem = next
	}
	return out, nil
}

func (c *Catalog) nextUnstartedInSeries(ctx context.Context, seriesID, userID int64, allowed []int64) (*Item, error) {
	libFilter, args := libraryFilter("i.library_id", allowed)
	query := "SELECT " + itemColumns + ` FROM items i
		JOIN item_series isr ON isr.item_id = i.id AND isr.series_id = ?
		LEFT JOIN progress pr ON pr.item_id = i.id AND pr.user_id = ?
		WHERE i.missing_at IS NULL AND pr.item_id IS NULL ` + strings.Replace(libFilter, "WHERE", "AND", 1) + `
		ORDER BY coalesce(isr.sequence, 0), i.sort_title COLLATE NOCASE LIMIT 1`
	rows, err := c.db.QueryContext(ctx, query, append([]any{seriesID, userID}, args...)...)
	if err != nil {
		return nil, err
	}
	items, err := scanItems(rows)
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}
	if err := c.hydrate(ctx, items, userID); err != nil {
		return nil, err
	}
	return &items[0], nil
}

func libraryFilter(column string, allowed []int64) (string, []any) {
	if allowed == nil {
		return "", nil
	}
	if len(allowed) == 0 {
		return "WHERE 1 = 0", nil
	}
	args := make([]any, len(allowed))
	placeholders := make([]string, len(allowed))
	for i, id := range allowed {
		placeholders[i] = "?"
		args[i] = id
	}
	return "WHERE " + column + " IN (" + strings.Join(placeholders, ",") + ")", args
}

// Authors lists contributors with the author role and their work counts.
func (c *Catalog) Authors(ctx context.Context, allowed []int64, query string, limit, offset int) ([]Author, int, error) {
	return c.people(ctx, allowed, RoleAuthor, query, limit, offset)
}

// Narrators lists contributors with the narrator role.
func (c *Catalog) Narrators(ctx context.Context, allowed []int64, query string, limit, offset int) ([]Author, int, error) {
	return c.people(ctx, allowed, RoleNarrator, query, limit, offset)
}

func (c *Catalog) people(ctx context.Context, allowed []int64, role, search string, limit, offset int) ([]Author, int, error) {
	limit, offset = clampPage(limit, offset)
	where := []string{"ip.role = ?", "i.missing_at IS NULL"}
	args := []any{role}
	if allowed != nil {
		if len(allowed) == 0 {
			return []Author{}, 0, nil
		}
		placeholders := make([]string, len(allowed))
		for i, id := range allowed {
			placeholders[i] = "?"
			args = append(args, id)
		}
		where = append(where, "i.library_id IN ("+strings.Join(placeholders, ",")+")")
	}
	if s := strings.TrimSpace(search); s != "" {
		where = append(where, `p.name LIKE ? ESCAPE '\'`)
		args = append(args, "%"+escapeLike(s)+"%")
	}
	from := ` FROM people p JOIN item_people ip ON ip.person_id = p.id
		JOIN items i ON i.id = ip.item_id WHERE ` + strings.Join(where, " AND ")

	var total int
	if err := c.db.QueryRowContext(ctx, `SELECT count(DISTINCT p.id)`+from, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := c.db.QueryContext(ctx,
		`SELECT p.id, p.name, p.sort_name, count(DISTINCT i.id)`+from+
			` GROUP BY p.id, p.name, p.sort_name ORDER BY coalesce(nullif(p.sort_name, ''), p.name) COLLATE NOCASE LIMIT ? OFFSET ?`,
		append(append([]any{}, args...), limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Author{}
	for rows.Next() {
		var a Author
		if err := rows.Scan(&a.ID, &a.Name, &a.SortName, &a.ItemCount); err != nil {
			return nil, 0, err
		}
		out = append(out, a)
	}
	return out, total, rows.Err()
}

// SeriesList lists series and their work counts.
func (c *Catalog) SeriesList(ctx context.Context, allowed []int64, search string, limit, offset int) ([]Series, int, error) {
	limit, offset = clampPage(limit, offset)
	where := []string{"i.missing_at IS NULL"}
	var args []any
	if allowed != nil {
		if len(allowed) == 0 {
			return []Series{}, 0, nil
		}
		placeholders := make([]string, len(allowed))
		for i, id := range allowed {
			placeholders[i] = "?"
			args = append(args, id)
		}
		where = append(where, "i.library_id IN ("+strings.Join(placeholders, ",")+")")
	}
	if s := strings.TrimSpace(search); s != "" {
		where = append(where, `s.name LIKE ? ESCAPE '\'`)
		args = append(args, "%"+escapeLike(s)+"%")
	}
	from := ` FROM series s JOIN item_series isr ON isr.series_id = s.id
		JOIN items i ON i.id = isr.item_id WHERE ` + strings.Join(where, " AND ")

	var total int
	if err := c.db.QueryRowContext(ctx, `SELECT count(DISTINCT s.id)`+from, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := c.db.QueryContext(ctx,
		`SELECT s.id, s.name, count(DISTINCT i.id)`+from+
			` GROUP BY s.id, s.name ORDER BY s.name COLLATE NOCASE LIMIT ? OFFSET ?`,
		append(append([]any{}, args...), limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Series{}
	for rows.Next() {
		var s Series
		if err := rows.Scan(&s.ID, &s.Name, &s.ItemCount); err != nil {
			return nil, 0, err
		}
		out = append(out, s)
	}
	return out, total, rows.Err()
}

// Tags lists subject tags and their work counts.
func (c *Catalog) Tags(ctx context.Context, allowed []int64, limit, offset int) ([]Tag, int, error) {
	limit, offset = clampPage(limit, offset)
	where := []string{"i.missing_at IS NULL"}
	var args []any
	if allowed != nil {
		if len(allowed) == 0 {
			return []Tag{}, 0, nil
		}
		placeholders := make([]string, len(allowed))
		for i, id := range allowed {
			placeholders[i] = "?"
			args = append(args, id)
		}
		where = append(where, "i.library_id IN ("+strings.Join(placeholders, ",")+")")
	}
	from := ` FROM tags t JOIN item_tags it ON it.tag_id = t.id
		JOIN items i ON i.id = it.item_id WHERE ` + strings.Join(where, " AND ")

	var total int
	if err := c.db.QueryRowContext(ctx, `SELECT count(DISTINCT t.id)`+from, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := c.db.QueryContext(ctx,
		`SELECT t.id, t.name, count(DISTINCT i.id)`+from+
			` GROUP BY t.id, t.name ORDER BY t.name COLLATE NOCASE LIMIT ? OFFSET ?`,
		append(append([]any{}, args...), limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Tag{}
	for rows.Next() {
		var t Tag
		if err := rows.Scan(&t.ID, &t.Name, &t.ItemCount); err != nil {
			return nil, 0, err
		}
		out = append(out, t)
	}
	return out, total, rows.Err()
}

// PersonByID returns one contributor.
func (c *Catalog) PersonByID(ctx context.Context, id int64) (*Author, error) {
	var a Author
	err := c.db.QueryRowContext(ctx, `SELECT id, name, sort_name FROM people WHERE id = ?`, id).
		Scan(&a.ID, &a.Name, &a.SortName)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	return &a, err
}

// SeriesByID returns one series.
func (c *Catalog) SeriesByID(ctx context.Context, id int64) (*Series, error) {
	var s Series
	err := c.db.QueryRowContext(ctx, `SELECT id, name FROM series WHERE id = ?`, id).Scan(&s.ID, &s.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	return &s, err
}

func clampPage(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

// ----------------------------------------------------------- user state ----

// ReaderSettings are the per-user reading preferences.
type ReaderSettings struct {
	FontScale        float64 `json:"font_scale"`
	FontFamily       string  `json:"font_family"`
	LineHeight       float64 `json:"line_height"`
	LetterSpacing    float64 `json:"letter_spacing"`
	WordSpacing      float64 `json:"word_spacing"`
	ParagraphSpacing float64 `json:"paragraph_spacing"`
	Margin           string  `json:"margin"`
	Align            string  `json:"align"`
	Theme            string  `json:"theme"`
	CustomFG         string  `json:"custom_fg"`
	CustomBG         string  `json:"custom_bg"`
	Layout           string  `json:"layout"`
	Columns          string  `json:"columns"`
}

// PlayerSettings are the per-user playback preferences.
type PlayerSettings struct {
	Speed             float64 `json:"speed"`
	SkipBackS         int     `json:"skip_back_s"`
	SkipFwdS          int     `json:"skip_fwd_s"`
	SleepTimerMin     *int    `json:"sleep_timer_min"`
	SleepEndOfChapter bool    `json:"sleep_end_of_chapter"`
	VolumeBoost       bool    `json:"volume_boost"`
}

// UISettings are the per-user application chrome preferences.
//
// TextScale multiplies the browser's own font size and is applied by the
// frontend as a percentage, never as a pixel value, so OS text scaling still
// compounds on top of it.
type UISettings struct {
	Theme     string  `json:"theme"`
	TextScale float64 `json:"text_scale"`
}

// Settings bundles every per-user preference group.
type Settings struct {
	Reader ReaderSettings `json:"reader"`
	Player PlayerSettings `json:"player"`
	UI     UISettings     `json:"ui"`
}

// DefaultSettings returns the settings a new user starts with.
func DefaultSettings() Settings {
	return Settings{
		Reader: ReaderSettings{
			FontScale: 1.0, FontFamily: "publisher", LineHeight: 1.5,
			LetterSpacing: 0, WordSpacing: 0, ParagraphSpacing: 0,
			Margin: "normal", Align: "publisher", Theme: "light",
			CustomFG: "#1f1d1a", CustomBG: "#faf8f4",
			Layout: "paginated", Columns: "auto",
		},
		Player: PlayerSettings{Speed: 1.0, SkipBackS: 15, SkipFwdS: 30},
		UI:     UISettings{Theme: "auto", TextScale: 1.0},
	}
}

// Normalize clamps every value to its documented range and replaces unknown
// enum values with the default, so a hand-written PUT cannot store nonsense.
func (s *Settings) Normalize() {
	d := DefaultSettings()

	s.Reader.FontScale = quantize(s.Reader.FontScale, 0.7, 2.5, 0.1, d.Reader.FontScale)
	s.Reader.LineHeight = clampFloat(s.Reader.LineHeight, 1.0, 3.0, d.Reader.LineHeight)
	s.Reader.LetterSpacing = clampFloat(s.Reader.LetterSpacing, -0.1, 1.0, 0)
	s.Reader.WordSpacing = clampFloat(s.Reader.WordSpacing, -0.1, 2.0, 0)
	s.Reader.ParagraphSpacing = clampFloat(s.Reader.ParagraphSpacing, 0, 4.0, 0)
	s.Reader.FontFamily = oneOf(s.Reader.FontFamily, d.Reader.FontFamily, "publisher", "system", "serif", "sans", "dyslexic")
	s.Reader.Margin = oneOf(s.Reader.Margin, d.Reader.Margin, "narrow", "normal", "wide")
	s.Reader.Align = oneOf(s.Reader.Align, d.Reader.Align, "publisher", "left", "justify")
	s.Reader.Theme = oneOf(s.Reader.Theme, d.Reader.Theme, "light", "dark", "sepia", "hc-dark", "hc-light", "custom")
	s.Reader.Layout = oneOf(s.Reader.Layout, d.Reader.Layout, "paginated", "scrolled")
	s.Reader.Columns = oneOf(s.Reader.Columns, d.Reader.Columns, "auto", "1", "2")
	s.Reader.CustomFG = hexColor(s.Reader.CustomFG, d.Reader.CustomFG)
	s.Reader.CustomBG = hexColor(s.Reader.CustomBG, d.Reader.CustomBG)

	s.Player.Speed = quantize(s.Player.Speed, 0.5, 3.0, 0.05, d.Player.Speed)
	s.Player.SkipBackS = clampInt(s.Player.SkipBackS, 5, 120, d.Player.SkipBackS)
	s.Player.SkipFwdS = clampInt(s.Player.SkipFwdS, 5, 120, d.Player.SkipFwdS)
	if s.Player.SleepTimerMin != nil {
		v := clampInt(*s.Player.SleepTimerMin, 1, 240, 15)
		s.Player.SleepTimerMin = &v
	}

	s.UI.Theme = oneOf(s.UI.Theme, d.UI.Theme, "auto", "light", "dark", "hc-dark", "hc-light")
	s.UI.TextScale = quantize(s.UI.TextScale, 1.0, 1.6, 0.05, d.UI.TextScale)
}

// SettingsFor returns a user's settings, filling in defaults for anything not
// stored yet.
func (c *Catalog) SettingsFor(ctx context.Context, userID int64) (Settings, error) {
	s := DefaultSettings()
	var readerJSON, playerJSON, uiJSON string
	err := c.db.QueryRowContext(ctx,
		`SELECT reader_json, player_json, ui_json FROM user_settings WHERE user_id = ?`, userID).
		Scan(&readerJSON, &playerJSON, &uiJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	_ = json.Unmarshal([]byte(readerJSON), &s.Reader)
	_ = json.Unmarshal([]byte(playerJSON), &s.Player)
	_ = json.Unmarshal([]byte(uiJSON), &s.UI)
	s.Normalize()
	return s, nil
}

// SaveSettings stores a user's settings after normalising them.
func (c *Catalog) SaveSettings(ctx context.Context, userID int64, s Settings) (Settings, error) {
	s.Normalize()
	reader, err := json.Marshal(s.Reader)
	if err != nil {
		return s, err
	}
	player, err := json.Marshal(s.Player)
	if err != nil {
		return s, err
	}
	ui, err := json.Marshal(s.UI)
	if err != nil {
		return s, err
	}
	_, err = c.db.ExecContext(ctx,
		`INSERT INTO user_settings (user_id, reader_json, player_json, ui_json, updated_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(user_id) DO UPDATE SET reader_json = excluded.reader_json,
			player_json = excluded.player_json, ui_json = excluded.ui_json, updated_at = excluded.updated_at`,
		userID, string(reader), string(player), string(ui), store.Now())
	return s, err
}

// ProgressSince returns a user's progress rows updated at or after since.
func (c *Catalog) ProgressSince(ctx context.Context, userID int64, since string) ([]Progress, error) {
	query := `SELECT item_id, locator, position_ms, percent, coalesce(finished_at, ''), device, updated_at
		FROM progress WHERE user_id = ?`
	args := []any{userID}
	if since != "" {
		query += ` AND updated_at >= ?`
		args = append(args, since)
	}
	query += ` ORDER BY updated_at DESC`

	rows, err := c.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Progress{}
	for rows.Next() {
		var p Progress
		if err := rows.Scan(&p.ItemID, &p.Locator, &p.PositionMS, &p.Percent, &p.FinishedAt, &p.Device, &p.UpdatedAt); err != nil {
			return nil, err
		}
		p.Finished = p.FinishedAt != ""
		out = append(out, p)
	}
	return out, rows.Err()
}

// SaveProgress upserts a user's position in an item.
func (c *Catalog) SaveProgress(ctx context.Context, userID int64, p Progress) (Progress, error) {
	p.Percent = math.Max(0, math.Min(1, p.Percent))
	if p.PositionMS < 0 {
		p.PositionMS = 0
	}
	if len(p.Locator) > 1024 {
		p.Locator = p.Locator[:1024]
	}
	if len(p.Device) > 128 {
		p.Device = p.Device[:128]
	}
	var finishedAt any
	if p.Finished {
		if p.FinishedAt == "" {
			p.FinishedAt = store.Now()
		}
		finishedAt = p.FinishedAt
	} else {
		p.FinishedAt = ""
	}
	p.UpdatedAt = store.Now()

	_, err := c.db.ExecContext(ctx,
		`INSERT INTO progress (user_id, item_id, locator, position_ms, percent, finished_at, device, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(user_id, item_id) DO UPDATE SET locator = excluded.locator,
			position_ms = excluded.position_ms, percent = excluded.percent,
			finished_at = excluded.finished_at, device = excluded.device, updated_at = excluded.updated_at`,
		userID, p.ItemID, p.Locator, p.PositionMS, p.Percent, finishedAt, p.Device, p.UpdatedAt)
	return p, err
}

// Bookmarks lists a user's bookmarks, optionally for one item.
func (c *Catalog) Bookmarks(ctx context.Context, userID, itemID int64) ([]Bookmark, error) {
	query := `SELECT id, item_id, locator, position_ms, note, created_at FROM bookmarks WHERE user_id = ?`
	args := []any{userID}
	if itemID > 0 {
		query += ` AND item_id = ?`
		args = append(args, itemID)
	}
	query += ` ORDER BY created_at DESC, id DESC`

	rows, err := c.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Bookmark{}
	for rows.Next() {
		var b Bookmark
		if err := rows.Scan(&b.ID, &b.ItemID, &b.Locator, &b.PositionMS, &b.Note, &b.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// CreateBookmark stores a new bookmark.
func (c *Catalog) CreateBookmark(ctx context.Context, userID int64, b Bookmark) (Bookmark, error) {
	if len(b.Note) > 2048 {
		b.Note = b.Note[:2048]
	}
	if len(b.Locator) > 1024 {
		b.Locator = b.Locator[:1024]
	}
	b.CreatedAt = store.Now()
	res, err := c.db.ExecContext(ctx,
		`INSERT INTO bookmarks (user_id, item_id, locator, position_ms, note, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		userID, b.ItemID, b.Locator, b.PositionMS, b.Note, b.CreatedAt)
	if err != nil {
		return b, err
	}
	b.ID, err = res.LastInsertId()
	return b, err
}

// DeleteBookmark removes one of a user's bookmarks.
func (c *Catalog) DeleteBookmark(ctx context.Context, userID, id int64) error {
	res, err := c.db.ExecContext(ctx, `DELETE FROM bookmarks WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return store.ErrNotFound
	}
	return nil
}

// ScanRuns returns the most recent scan runs for a library.
func (c *Catalog) ScanRuns(ctx context.Context, libraryID int64, limit int) ([]ScanRun, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := c.db.QueryContext(ctx,
		`SELECT id, library_id, started_at, coalesce(finished_at, ''), added, updated, removed, errors
		 FROM scan_runs WHERE library_id = ? ORDER BY id DESC LIMIT ?`, libraryID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ScanRun{}
	for rows.Next() {
		var r ScanRun
		if err := rows.Scan(&r.ID, &r.LibraryID, &r.StartedAt, &r.FinishedAt, &r.Added, &r.Updated, &r.Removed, &r.Errors); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ItemCounts returns the number of non-missing items per kind.
func (c *Catalog) ItemCounts(ctx context.Context) (map[string]int, error) {
	rows, err := c.db.QueryContext(ctx,
		`SELECT kind, count(*) FROM items WHERE missing_at IS NULL GROUP BY kind`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{KindEbook: 0, KindAudiobook: 0}
	for rows.Next() {
		var (
			kind string
			n    int
		)
		if err := rows.Scan(&kind, &n); err != nil {
			return nil, err
		}
		out[kind] = n
	}
	return out, rows.Err()
}

func cleanPaths(paths []string) []string {
	out := make([]string, 0, len(paths))
	seen := map[string]bool{}
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

func clampFloat(v, lo, hi, fallback float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) || v == 0 && fallback != 0 && v < lo {
		return fallback
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func quantize(v, lo, hi, step, fallback float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 {
		return fallback
	}
	if v < lo {
		v = lo
	}
	if v > hi {
		v = hi
	}
	// Snapping to a step reintroduces binary float noise (1.2 becomes
	// 1.2000000000000002), which would be echoed back to the client and shown
	// on a slider, so round the result to a sane number of decimals.
	return math.Round(math.Round(v/step)*step*1e6) / 1e6
}

func clampInt(v, lo, hi, fallback int) int {
	if v == 0 {
		return fallback
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func oneOf(v, fallback string, allowed ...string) string {
	for _, a := range allowed {
		if v == a {
			return v
		}
	}
	return fallback
}

func hexColor(v, fallback string) string {
	if len(v) != 7 && len(v) != 4 || len(v) > 0 && v[0] != '#' {
		return fallback
	}
	for _, r := range v[1:] {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return fallback
		}
	}
	return v
}
