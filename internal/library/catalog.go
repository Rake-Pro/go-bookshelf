package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/rake-pro/go-bookshelf/internal/store"
)

// Catalog reads and writes catalog rows.
type Catalog struct{ db *store.DB }

// NewCatalog wraps a database handle.
func NewCatalog(db *store.DB) *Catalog { return &Catalog{db: db} }

// DB exposes the underlying handle for packages that need their own queries.
func (c *Catalog) DB() *store.DB { return c.db }

// ListOptions filters and paginates an item listing.
type ListOptions struct {
	// AllowedLibraries restricts the result to these library ids. A nil slice
	// means unrestricted (admin); an empty non-nil slice means no access.
	AllowedLibraries []int64
	LibraryID        int64
	Kind             string
	AuthorID         int64
	SeriesID         int64
	TagID            int64
	Query            string
	Sort             string
	Limit            int
	Offset           int
	UserID           int64
	IncludeMissing   bool
}

const defaultLimit = 50
const maxLimit = 200

// sortClauses whitelists ORDER BY fragments; the sort parameter is never
// interpolated into SQL directly.
// Case-insensitive ordering is written as lower(x) rather than
// COLLATE NOCASE: the collation is SQLite-only, while lower() means the same
// thing to both backends.
var sortClauses = map[string]string{
	"added":  "i.added_at DESC, i.id DESC",
	"title":  "lower(i.sort_title) ASC, i.id ASC",
	"author": "lower((SELECT min(pe.sort_name) FROM item_people ip2 JOIN people pe ON pe.id = ip2.person_id WHERE ip2.item_id = i.id AND ip2.role = 'author')) ASC, lower(i.sort_title) ASC",
	"recent": "coalesce(pr.updated_at, '') DESC, i.added_at DESC",
}

func (o ListOptions) clamp() ListOptions {
	if o.Limit <= 0 {
		o.Limit = defaultLimit
	}
	if o.Limit > maxLimit {
		o.Limit = maxLimit
	}
	if o.Offset < 0 {
		o.Offset = 0
	}
	if _, ok := sortClauses[o.Sort]; !ok {
		o.Sort = "added"
	}
	return o
}

// buildFilter returns the FROM/WHERE fragment and its arguments.
func (o ListOptions) buildFilter() (string, []any, bool) {
	var (
		joins strings.Builder
		where []string
		args  []any
	)

	joins.WriteString(" FROM items i")
	joins.WriteString(" LEFT JOIN progress pr ON pr.item_id = i.id AND pr.user_id = ?")
	args = append(args, o.UserID)

	if o.AuthorID > 0 {
		joins.WriteString(" JOIN item_people ipf ON ipf.item_id = i.id AND ipf.person_id = ? AND ipf.role = 'author'")
		args = append(args, o.AuthorID)
	}
	if o.SeriesID > 0 {
		joins.WriteString(" JOIN item_series isf ON isf.item_id = i.id AND isf.series_id = ?")
		args = append(args, o.SeriesID)
	}
	if o.TagID > 0 {
		joins.WriteString(" JOIN item_tags itf ON itf.item_id = i.id AND itf.tag_id = ?")
		args = append(args, o.TagID)
	}

	if o.AllowedLibraries != nil {
		if len(o.AllowedLibraries) == 0 {
			return "", nil, false
		}
		placeholders := make([]string, len(o.AllowedLibraries))
		for i, id := range o.AllowedLibraries {
			placeholders[i] = "?"
			args = append(args, id)
		}
		where = append(where, "i.library_id IN ("+strings.Join(placeholders, ",")+")")
	}
	if o.LibraryID > 0 {
		where = append(where, "i.library_id = ?")
		args = append(args, o.LibraryID)
	}
	if o.Kind == KindEbook || o.Kind == KindAudiobook {
		where = append(where, "i.kind = ?")
		args = append(args, o.Kind)
	}
	if q := strings.TrimSpace(o.Query); q != "" {
		// LIKE is case-insensitive in SQLite and case-sensitive in Postgres,
		// so both sides are folded rather than relying on either default.
		pattern := likePattern(q)
		where = append(where, "(lower(i.title) LIKE ? ESCAPE '\\' OR lower(i.subtitle) LIKE ? ESCAPE '\\' OR lower(i.description) LIKE ? ESCAPE '\\'"+
			" OR EXISTS (SELECT 1 FROM item_people ips JOIN people ps ON ps.id = ips.person_id WHERE ips.item_id = i.id AND lower(ps.name) LIKE ? ESCAPE '\\')"+
			" OR EXISTS (SELECT 1 FROM item_series iss JOIN series se ON se.id = iss.series_id WHERE iss.item_id = i.id AND lower(se.name) LIKE ? ESCAPE '\\'))")
		args = append(args, pattern, pattern, pattern, pattern, pattern)
	}
	if !o.IncludeMissing {
		where = append(where, "i.missing_at IS NULL")
	}

	clause := joins.String()
	if len(where) > 0 {
		clause += " WHERE " + strings.Join(where, " AND ")
	}
	return clause, args, true
}

const itemColumns = `i.id, i.library_id, i.kind, i.title, i.sort_title, i.subtitle,
	i.has_cover, i.duration_ms, i.size_bytes, i.added_at, i.updated_at, coalesce(i.missing_at, '')`

// ListItems returns a page of items plus the total matching count.
func (c *Catalog) ListItems(ctx context.Context, o ListOptions) ([]Item, int, error) {
	o = o.clamp()
	filter, args, ok := o.buildFilter()
	if !ok {
		return []Item{}, 0, nil
	}

	var total int
	if err := c.db.QueryRowContext(ctx, "SELECT count(*)"+filter, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	query := "SELECT " + itemColumns + filter + " ORDER BY " + sortClauses[o.Sort] + " LIMIT ? OFFSET ?"
	rows, err := c.db.QueryContext(ctx, query, append(append([]any{}, args...), o.Limit, o.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	items, err := scanItems(rows)
	if err != nil {
		return nil, 0, err
	}
	if err := c.hydrate(ctx, items, o.UserID); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func scanItems(rows *sql.Rows) ([]Item, error) {
	items := []Item{}
	for rows.Next() {
		var (
			it        Item
			hasCover  int
			missingAt string
		)
		if err := rows.Scan(&it.ID, &it.LibraryID, &it.Kind, &it.Title, &it.SortTitle, &it.Subtitle,
			&hasCover, &it.DurationMS, &it.SizeBytes, &it.AddedAt, &it.UpdatedAt, &missingAt); err != nil {
			return nil, err
		}
		it.Missing = missingAt != ""
		if hasCover != 0 {
			it.CoverURL = "/api/v1/items/" + strconv.FormatInt(it.ID, 10) + "/cover"
		}
		it.Authors = []string{}
		it.Narrators = []string{}
		items = append(items, it)
	}
	return items, rows.Err()
}

// hydrate fills the people, series and progress of a page of items.
func (c *Catalog) hydrate(ctx context.Context, items []Item, userID int64) error {
	if len(items) == 0 {
		return nil
	}
	index := make(map[int64]*Item, len(items))
	ids := make([]any, 0, len(items))
	for i := range items {
		index[items[i].ID] = &items[i]
		ids = append(ids, items[i].ID)
	}
	in := "(" + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + ")"

	rows, err := c.db.QueryContext(ctx,
		`SELECT ip.item_id, p.name, ip.role FROM item_people ip JOIN people p ON p.id = ip.person_id
		 WHERE ip.item_id IN `+in+` ORDER BY ip.seq, p.name`, ids...)
	if err != nil {
		return err
	}
	for rows.Next() {
		var (
			itemID     int64
			name, role string
		)
		if err := rows.Scan(&itemID, &name, &role); err != nil {
			rows.Close()
			return err
		}
		if it := index[itemID]; it != nil {
			switch role {
			case RoleAuthor:
				it.Authors = append(it.Authors, name)
			case RoleNarrator:
				it.Narrators = append(it.Narrators, name)
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	rows, err = c.db.QueryContext(ctx,
		`SELECT isr.item_id, s.id, s.name, coalesce(isr.sequence, 0) FROM item_series isr
		 JOIN series s ON s.id = isr.series_id WHERE isr.item_id IN `+in, ids...)
	if err != nil {
		return err
	}
	for rows.Next() {
		var (
			itemID int64
			ref    SeriesRef
		)
		if err := rows.Scan(&itemID, &ref.ID, &ref.Name, &ref.Sequence); err != nil {
			rows.Close()
			return err
		}
		if it := index[itemID]; it != nil && it.Series == nil {
			r := ref
			it.Series = &r
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	if userID > 0 {
		rows, err = c.db.QueryContext(ctx,
			`SELECT item_id, locator, position_ms, percent, coalesce(finished_at, ''), device, updated_at
			 FROM progress WHERE user_id = ? AND item_id IN `+in, append([]any{userID}, ids...)...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var p Progress
			if err := rows.Scan(&p.ItemID, &p.Locator, &p.PositionMS, &p.Percent, &p.FinishedAt, &p.Device, &p.UpdatedAt); err != nil {
				rows.Close()
				return err
			}
			p.Finished = p.FinishedAt != ""
			if it := index[p.ItemID]; it != nil {
				q := p
				it.Progress = &q
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}
	return nil
}

// ItemLibrary returns the library an item belongs to, or store.ErrNotFound.
func (c *Catalog) ItemLibrary(ctx context.Context, itemID int64) (int64, error) {
	var libID int64
	err := c.db.QueryRowContext(ctx, `SELECT library_id FROM items WHERE id = ?`, itemID).Scan(&libID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, store.ErrNotFound
	}
	return libID, err
}

// Item returns the full detail record for one item.
func (c *Catalog) Item(ctx context.Context, itemID, userID int64) (*ItemDetail, error) {
	row := c.db.QueryRowContext(ctx, "SELECT "+itemColumns+`, i.description, i.language, i.published,
		i.isbn, i.asin, i.publisher FROM items i WHERE i.id = ?`, itemID)

	var (
		d         ItemDetail
		hasCover  int
		missingAt string
	)
	err := row.Scan(&d.ID, &d.LibraryID, &d.Kind, &d.Title, &d.SortTitle, &d.Subtitle,
		&hasCover, &d.DurationMS, &d.SizeBytes, &d.AddedAt, &d.UpdatedAt, &missingAt,
		&d.Description, &d.Language, &d.Published, &d.ISBN, &d.ASIN, &d.Publisher)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	d.Missing = missingAt != ""
	d.Authors = []string{}
	d.Narrators = []string{}
	d.People = []PersonRef{}
	d.Tags = []TagRef{}
	d.Files = []FileRef{}
	base := "/api/v1/items/" + strconv.FormatInt(d.ID, 10)
	if hasCover != 0 {
		d.CoverURL = base + "/cover"
	}
	d.DownloadURL = base + "/download"
	if d.Kind == KindEbook {
		d.ReadURL = base + "/epub"
	}

	// People, tags and files are small per item, so a query each is cheaper
	// than a wide join that multiplies rows.
	rows, err := c.db.QueryContext(ctx,
		`SELECT p.id, p.name, p.sort_name, ip.role, ip.seq FROM item_people ip
		 JOIN people p ON p.id = ip.person_id WHERE ip.item_id = ? ORDER BY ip.role, ip.seq, p.name`, itemID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p PersonRef
		if err := rows.Scan(&p.ID, &p.Name, &p.SortName, &p.Role, &p.Seq); err != nil {
			rows.Close()
			return nil, err
		}
		d.People = append(d.People, p)
		switch p.Role {
		case RoleAuthor:
			d.Authors = append(d.Authors, p.Name)
		case RoleNarrator:
			d.Narrators = append(d.Narrators, p.Name)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var ref SeriesRef
	err = c.db.QueryRowContext(ctx,
		`SELECT s.id, s.name, coalesce(isr.sequence, 0) FROM item_series isr
		 JOIN series s ON s.id = isr.series_id WHERE isr.item_id = ? LIMIT 1`, itemID).
		Scan(&ref.ID, &ref.Name, &ref.Sequence)
	if err == nil {
		d.Series = &ref
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	rows, err = c.db.QueryContext(ctx,
		`SELECT t.id, t.name FROM item_tags it JOIN tags t ON t.id = it.tag_id
		 WHERE it.item_id = ? ORDER BY t.name`, itemID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var t TagRef
		if err := rows.Scan(&t.ID, &t.Name); err != nil {
			rows.Close()
			return nil, err
		}
		d.Tags = append(d.Tags, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	files, err := c.Files(ctx, itemID)
	if err != nil {
		return nil, err
	}
	d.Files = files
	d.Chapters = []Chapter{}
	for _, f := range files {
		d.Chapters = append(d.Chapters, f.Chapters...)
	}

	if userID > 0 {
		var p Progress
		err := c.db.QueryRowContext(ctx,
			`SELECT item_id, locator, position_ms, percent, coalesce(finished_at, ''), device, updated_at
			 FROM progress WHERE user_id = ? AND item_id = ?`, userID, itemID).
			Scan(&p.ItemID, &p.Locator, &p.PositionMS, &p.Percent, &p.FinishedAt, &p.Device, &p.UpdatedAt)
		if err == nil {
			p.Finished = p.FinishedAt != ""
			d.Progress = &p
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	return &d, nil
}

// Files returns an item's media files with their chapters, in play order.
func (c *Catalog) Files(ctx context.Context, itemID int64) ([]FileRef, error) {
	rows, err := c.db.QueryContext(ctx,
		`SELECT id, path, size, format, duration_ms, seq FROM files WHERE item_id = ? ORDER BY seq, id`, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	files := []FileRef{}
	byID := map[int64]int{}
	for rows.Next() {
		var (
			f    FileRef
			path string
		)
		if err := rows.Scan(&f.ID, &path, &f.SizeBytes, &f.Format, &f.DurationMS, &f.Seq); err != nil {
			return nil, err
		}
		f.Filename = baseName(path)
		f.StreamURL = fmt.Sprintf("/api/v1/items/%d/files/%d/stream", itemID, f.ID)
		f.Chapters = []Chapter{}
		byID[f.ID] = len(files)
		files = append(files, f)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return files, nil
	}

	chapterRows, err := c.db.QueryContext(ctx,
		`SELECT ch.file_id, ch.seq, ch.title, ch.start_ms, ch.end_ms FROM chapters ch
		 JOIN files f ON f.id = ch.file_id WHERE f.item_id = ? ORDER BY f.seq, ch.seq`, itemID)
	if err != nil {
		return nil, err
	}
	defer chapterRows.Close()
	for chapterRows.Next() {
		var (
			fileID int64
			ch     Chapter
		)
		if err := chapterRows.Scan(&fileID, &ch.Seq, &ch.Title, &ch.StartMS, &ch.EndMS); err != nil {
			return nil, err
		}
		ch.FileID = fileID
		if idx, ok := byID[fileID]; ok {
			files[idx].Chapters = append(files[idx].Chapters, ch)
		}
	}
	return files, chapterRows.Err()
}

// FilePath returns the on-disk path of a file belonging to an item.
func (c *Catalog) FilePath(ctx context.Context, itemID, fileID int64) (string, error) {
	var path string
	err := c.db.QueryRowContext(ctx, `SELECT path FROM files WHERE id = ? AND item_id = ?`, fileID, itemID).Scan(&path)
	if errors.Is(err, sql.ErrNoRows) {
		return "", store.ErrNotFound
	}
	return path, err
}

// ItemFilePaths returns every file path of an item, in order.
func (c *Catalog) ItemFilePaths(ctx context.Context, itemID int64) ([]string, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT path FROM files WHERE item_id = ? ORDER BY seq, id`, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		paths = append(paths, p)
	}
	return paths, rows.Err()
}

// DeleteItem removes the catalog record for one item. Its files, chapters,
// contributor and series/tag links, cover images, progress and bookmarks all
// cascade with it - every one of those tables' foreign keys is ON DELETE
// CASCADE (docs/DESIGN.md). This only ever touches the database; removing the
// underlying files from disk, inside the library root, is the caller's job.
func (c *Catalog) DeleteItem(ctx context.Context, id int64) error {
	res, err := c.db.ExecContext(ctx, `DELETE FROM items WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return store.ErrNotFound
	}
	return nil
}

func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// likePattern builds the argument for a `lower(column) LIKE ?` comparison: the
// wildcards the user did not type are escaped, and the whole thing is folded
// to match the folded column.
func likePattern(q string) string {
	return "%" + strings.ToLower(escapeLike(q)) + "%"
}

func baseName(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}
