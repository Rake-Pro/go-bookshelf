// Package library owns the catalog: the item model, the queries the API reads
// from, the filesystem scanner that ingests books, the change watcher, and the
// janitor that retires items whose files are gone.
package library

// Kinds an item or library can have.
const (
	KindEbook     = "ebook"
	KindAudiobook = "audiobook"
	KindMixed     = "mixed"
)

// Roles a person can hold on an item.
const (
	RoleAuthor     = "author"
	RoleNarrator   = "narrator"
	RoleTranslator = "translator"
)

// Library is a configured root of media.
type Library struct {
	ID        int64    `json:"id"`
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`
	Paths     []string `json:"paths"`
	CreatedAt string   `json:"created_at"`
	ItemCount int      `json:"item_count"`
}

// SeriesRef is an item's position in a series.
type SeriesRef struct {
	ID       int64   `json:"id"`
	Name     string  `json:"name"`
	Sequence float64 `json:"sequence"`
}

// PersonRef is a contributor on an item.
type PersonRef struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	SortName string `json:"sort_name"`
	Role     string `json:"role"`
	Seq      int    `json:"seq"`
}

// TagRef is a subject tag.
type TagRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// Chapter is one chapter marker inside a file. StartMS and EndMS are relative
// to the start of that file, not to the item: a client that plays the files as
// one stream adds the durations of the preceding files (ordered by Seq).
type Chapter struct {
	FileID  int64  `json:"file_id"`
	Seq     int    `json:"seq"`
	Title   string `json:"title"`
	StartMS int64  `json:"start_ms"`
	EndMS   int64  `json:"end_ms"`
}

// FileRef is one media file belonging to an item. The on-disk path is
// deliberately not exposed: clients address files by id.
type FileRef struct {
	ID         int64     `json:"id"`
	Filename   string    `json:"filename"`
	Format     string    `json:"format"`
	SizeBytes  int64     `json:"size_bytes"`
	DurationMS int64     `json:"duration_ms"`
	Seq        int       `json:"seq"`
	StreamURL  string    `json:"stream_url"`
	Chapters   []Chapter `json:"chapters"`
}

// Progress is a user's position in an item.
type Progress struct {
	ItemID     int64   `json:"item_id"`
	Locator    string  `json:"locator"`
	PositionMS int64   `json:"position_ms"`
	Percent    float64 `json:"percent"`
	Finished   bool    `json:"finished"`
	FinishedAt string  `json:"finished_at,omitempty"`
	Device     string  `json:"device"`
	UpdatedAt  string  `json:"updated_at"`
}

// Bookmark is a user-placed marker inside an item.
type Bookmark struct {
	ID         int64  `json:"id"`
	ItemID     int64  `json:"item_id"`
	Locator    string `json:"locator"`
	PositionMS int64  `json:"position_ms"`
	Note       string `json:"note"`
	CreatedAt  string `json:"created_at"`
}

// Item is the list representation of a catalog entry.
type Item struct {
	ID         int64      `json:"id"`
	LibraryID  int64      `json:"library_id"`
	Kind       string     `json:"kind"`
	Title      string     `json:"title"`
	SortTitle  string     `json:"sort_title"`
	Subtitle   string     `json:"subtitle"`
	Authors    []string   `json:"authors"`
	Narrators  []string   `json:"narrators"`
	Series     *SeriesRef `json:"series"`
	CoverURL   string     `json:"cover_url"`
	DurationMS int64      `json:"duration_ms"`
	SizeBytes  int64      `json:"size_bytes"`
	AddedAt    string     `json:"added_at"`
	UpdatedAt  string     `json:"updated_at"`
	Missing    bool       `json:"missing"`
	Progress   *Progress  `json:"progress"`
}

// ItemDetail is the single-item representation.
type ItemDetail struct {
	Item
	Description string      `json:"description"`
	Language    string      `json:"language"`
	Published   string      `json:"published"`
	ISBN        string      `json:"isbn"`
	ASIN        string      `json:"asin"`
	Publisher   string      `json:"publisher"`
	People      []PersonRef `json:"people"`
	Tags        []TagRef    `json:"tags"`
	Files       []FileRef   `json:"files"`
	// Chapters is every chapter of every file, flattened in play order. Each
	// entry names its file, so a client can map it onto an absolute position.
	Chapters    []Chapter `json:"chapters"`
	ReadURL     string    `json:"read_url,omitempty"`
	DownloadURL string    `json:"download_url"`
}

// Author is a person with a work count, for the authors index.
type Author struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	SortName  string `json:"sort_name"`
	ItemCount int    `json:"item_count"`
}

// Series is a named series with a work count.
type Series struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	ItemCount int    `json:"item_count"`
}

// Tag is a subject tag with a work count.
type Tag struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	ItemCount int    `json:"item_count"`
}

// SeriesProgress is a series the user has started but not finished.
type SeriesProgress struct {
	Series   Series `json:"series"`
	Finished int    `json:"finished"`
	Total    int    `json:"total"`
	NextItem *Item  `json:"next_item"`
}

// Home is the discovery payload.
type Home struct {
	Continue         []Item           `json:"continue"`
	Recent           []Item           `json:"recent"`
	SeriesInProgress []SeriesProgress `json:"series_in_progress"`
}

// ScanRun records one pass of the scanner over a library.
type ScanRun struct {
	ID         int64  `json:"id"`
	LibraryID  int64  `json:"library_id"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at,omitempty"`
	Added      int    `json:"added"`
	Updated    int    `json:"updated"`
	Removed    int    `json:"removed"`
	Errors     int    `json:"errors"`
}
