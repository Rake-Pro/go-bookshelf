package api

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/rake-pro/go-bookshelf/internal/auth"
	"github.com/rake-pro/go-bookshelf/internal/epub"
	"github.com/rake-pro/go-bookshelf/internal/images"
	"github.com/rake-pro/go-bookshelf/internal/library"
	"github.com/rake-pro/go-bookshelf/internal/store"
	"github.com/rs/zerolog/log"
)

// epubCSP isolates book content: no scripts, no outbound requests, and the
// sandbox directive strips the resource of an origin of its own.
const epubCSP = "default-src 'none'; script-src 'none'; img-src 'self' data:; " +
	"style-src 'self' 'unsafe-inline'; font-src 'self' data:; media-src 'self'; " +
	"object-src 'none'; base-uri 'none'; frame-ancestors 'self'; sandbox"

func (a *API) listItems(w http.ResponseWriter, r *http.Request) {
	id := requireUser(w, r)
	if id == nil {
		return
	}
	allowed, err := a.allowedLibraries(r, id)
	if err != nil {
		fail(w, err, "library access")
		return
	}
	opts := library.ListOptions{
		AllowedLibraries: allowed,
		LibraryID:        queryID(r, "library"),
		Kind:             r.URL.Query().Get("kind"),
		AuthorID:         queryID(r, "author"),
		SeriesID:         queryID(r, "series"),
		TagID:            queryID(r, "tag"),
		Query:            r.URL.Query().Get("q"),
		Sort:             r.URL.Query().Get("sort"),
		Limit:            queryInt(r, "limit", 0),
		Offset:           queryInt(r, "offset", 0),
		UserID:           id.User.ID,
	}
	items, total, err := a.cat.ListItems(r.Context(), opts)
	if err != nil {
		fail(w, err, "list items")
		return
	}
	writeJSON(w, http.StatusOK, listBody[library.Item]{Items: items, Total: total})
}

func (a *API) getItem(w http.ResponseWriter, r *http.Request) {
	id := requireUser(w, r)
	if id == nil {
		return
	}
	itemID, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, codeBadRequest, "item id must be a positive integer")
		return
	}
	visible, err := a.itemVisible(r, id, itemID)
	if err != nil || !visible {
		writeError(w, http.StatusNotFound, codeNotFound, "not found")
		return
	}
	detail, err := a.cat.Item(r.Context(), itemID, id.User.ID)
	if err != nil {
		fail(w, err, "get item")
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (a *API) patchItem(w http.ResponseWriter, r *http.Request) {
	id := requireAdmin(w, r)
	if id == nil {
		return
	}
	itemID, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, codeBadRequest, "item id must be a positive integer")
		return
	}
	var body struct {
		Title       *string `json:"title"`
		SortTitle   *string `json:"sort_title"`
		Subtitle    *string `json:"subtitle"`
		Description *string `json:"description"`
		Language    *string `json:"language"`
		Published   *string `json:"published"`
		ISBN        *string `json:"isbn"`
		ASIN        *string `json:"asin"`
		Publisher   *string `json:"publisher"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	updates := map[string]*string{
		"title": body.Title, "sort_title": body.SortTitle, "subtitle": body.Subtitle,
		"description": body.Description, "language": body.Language, "published": body.Published,
		"isbn": body.ISBN, "asin": body.ASIN, "publisher": body.Publisher,
	}
	// Column names come from this fixed map, never from the request.
	for column, value := range updates {
		if value == nil {
			continue
		}
		if _, err := a.db.ExecContext(r.Context(),
			`UPDATE items SET `+column+` = ?, updated_at = ? WHERE id = ?`, *value, store.Now(), itemID); err != nil {
			fail(w, err, "patch item")
			return
		}
	}
	detail, err := a.cat.Item(r.Context(), itemID, id.User.ID)
	if err != nil {
		fail(w, err, "patch item")
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (a *API) itemCover(w http.ResponseWriter, r *http.Request) {
	id := requireUser(w, r)
	if id == nil {
		return
	}
	itemID, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, codeBadRequest, "item id must be a positive integer")
		return
	}
	visible, err := a.itemVisible(r, id, itemID)
	if err != nil || !visible {
		writeError(w, http.StatusNotFound, codeNotFound, "not found")
		return
	}
	variant := images.Variant(r.URL.Query().Get("size"))

	// The cache, when there is one, answers first. It holds exactly the bytes
	// the database holds, written at the same moment, so a hit needs no
	// round-trip.
	if data, mod, ok := a.covers.Read(itemID, variant); ok {
		serveCover(w, r, itemID, variant, "image/jpeg", data, mod)
		return
	}

	cover, err := a.cat.Cover(r.Context(), itemID, variant)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, codeNotFound, "no cover for this item")
		return
	}
	if err != nil {
		fail(w, err, "cover")
		return
	}
	// Write-through: the next request for this cover is served from disk.
	if err := a.covers.Put(itemID, variant, cover.Bytes); err != nil {
		log.Warn().Err(err).Int64("item", itemID).Msg("caching the cover on disk failed")
	}
	serveCover(w, r, itemID, variant, cover.ContentType, cover.Bytes, cover.UpdatedAt)
}

// serveCover writes one cover with the caching headers a browser needs to stop
// asking for it. The validator is derived from the bytes rather than from a
// file's metadata, so it is the same whether the response came from the cache
// or from the database.
func serveCover(w http.ResponseWriter, r *http.Request, itemID int64, variant, contentType string,
	data []byte, modTime time.Time) {
	if contentType == "" {
		contentType = "image/jpeg"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "private, max-age=604800")
	w.Header().Set("ETag", fmt.Sprintf(`"%d-%d-%s"`, itemID, len(data), variant))
	http.ServeContent(w, r, strconv.FormatInt(itemID, 10)+"-"+variant+".jpg", modTime, bytes.NewReader(data))
}

// epubManifest describes an EPUB well enough for a client to open it without
// downloading the container: where the resources live, the spine in reading
// order with each document's byte size, and the caller's saved position.
//
// The bundled reader uses `spine[].size` to weight reading progress and then
// parses `META-INF/container.xml`, the OPF and the navigation document itself
// through the resource route, so no table of contents is duplicated here.
type epubManifest struct {
	ItemID       int64             `json:"item_id"`
	Title        string            `json:"title"`
	Language     string            `json:"language"`
	ResourceURL  string            `json:"resource_url"`
	ContainerURL string            `json:"container_url"`
	CoverURL     string            `json:"cover_url,omitempty"`
	Spine        []spineEntry      `json:"spine"`
	Progress     *library.Progress `json:"progress"`
}

type spineEntry struct {
	Href string `json:"href"`
	URL  string `json:"url"`
	Size int64  `json:"size"`
}

func (a *API) itemEPUBManifest(w http.ResponseWriter, r *http.Request) {
	id := requireUser(w, r)
	if id == nil {
		return
	}
	itemID, path, ok := a.epubFileFor(w, r, id)
	if !ok {
		return
	}
	reader, err := epub.Open(path)
	if err != nil {
		fail(w, err, "open epub")
		return
	}
	defer reader.Close()
	book, err := reader.Book()
	if err != nil {
		fail(w, err, "parse epub")
		return
	}

	base := fmt.Sprintf("/api/v1/items/%d/epub/", itemID)
	manifest := epubManifest{
		ItemID:       itemID,
		Title:        book.Meta.Title,
		Language:     book.Meta.Language,
		ResourceURL:  base,
		ContainerURL: base + "META-INF/container.xml",
		Spine:        []spineEntry{},
	}
	// Spine hrefs in the package document are relative to the OPF; the
	// manifest publishes them in the container-root address space the resource
	// route serves, so a client can concatenate resource_url and href.
	for _, href := range book.Spine {
		name, err := reader.Resolve(href)
		if err != nil {
			continue
		}
		entry := spineEntry{Href: name, URL: base + name}
		if size, ok := reader.Size(name); ok {
			entry.Size = size
		}
		manifest.Spine = append(manifest.Spine, entry)
	}
	if book.CoverHref != "" {
		if name, err := reader.Resolve(book.CoverHref); err == nil {
			manifest.CoverURL = base + name
		}
	}
	detail, err := a.cat.Item(r.Context(), itemID, id.User.ID)
	if err == nil {
		manifest.Progress = detail.Progress
	}
	writeJSON(w, http.StatusOK, manifest)
}

func (a *API) itemEPUBResource(w http.ResponseWriter, r *http.Request) {
	id := requireUser(w, r)
	if id == nil {
		return
	}
	itemID, path, ok := a.epubFileFor(w, r, id)
	if !ok {
		return
	}
	_ = itemID

	reader, err := epub.Open(path)
	if err != nil {
		fail(w, err, "open epub")
		return
	}
	defer reader.Close()
	if _, err := reader.Book(); err != nil {
		fail(w, err, "parse epub")
		return
	}

	// The requested path is resolved inside the archive, never on disk, and is
	// relative to the container root - the same address space the EPUB's own
	// container.xml, OPF and navigation document live in.
	name, err := reader.ResolveRoot(r.PathValue("path"))
	switch {
	case errors.Is(err, epub.ErrUnsafePath):
		writeError(w, http.StatusBadRequest, codeBadRequest, "invalid resource path")
		return
	case errors.Is(err, epub.ErrNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, "not found")
		return
	case err != nil:
		fail(w, err, "resolve resource")
		return
	}
	body, err := reader.ReadFile(name)
	if err != nil {
		fail(w, err, "read resource")
		return
	}

	w.Header().Set("Content-Security-Policy", epubCSP)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", epub.ContentType(name))
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeContent(w, r, filepath.Base(name), time.Time{}, strings.NewReader(string(body)))
}

// epubFileFor resolves the item's EPUB file, enforcing visibility and the
// library-root boundary.
func (a *API) epubFileFor(w http.ResponseWriter, r *http.Request, id *auth.Identity) (int64, string, bool) {
	itemID, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, codeBadRequest, "item id must be a positive integer")
		return 0, "", false
	}
	visible, err := a.itemVisible(r, id, itemID)
	if err != nil || !visible {
		writeError(w, http.StatusNotFound, codeNotFound, "not found")
		return 0, "", false
	}
	paths, err := a.cat.ItemFilePaths(r.Context(), itemID)
	if err != nil {
		fail(w, err, "item files")
		return 0, "", false
	}
	for _, p := range paths {
		if strings.EqualFold(filepath.Ext(p), ".epub") {
			if !a.pathAllowed(r, p) {
				writeError(w, http.StatusNotFound, codeNotFound, "not found")
				return 0, "", false
			}
			return itemID, p, true
		}
	}
	writeError(w, http.StatusNotFound, codeNotFound, "this item has no EPUB file")
	return 0, "", false
}

// pathAllowed re-checks that a stored path still lies inside a configured
// library root before the server opens it.
func (a *API) pathAllowed(r *http.Request, path string) bool {
	roots, err := a.cat.LibraryRoots(r.Context())
	if err != nil {
		log.Error().Err(err).Msg("reading library roots failed")
		return false
	}
	if library.WithinRoots(path, roots) {
		return true
	}
	log.Warn().Str("path", path).Msg("refusing to serve a file outside every library root")
	return false
}

func (a *API) itemStream(w http.ResponseWriter, r *http.Request) {
	id := requireUser(w, r)
	if id == nil {
		return
	}
	itemID, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, codeBadRequest, "item id must be a positive integer")
		return
	}
	fileID, ok := pathID(r, "file_id")
	if !ok {
		writeError(w, http.StatusBadRequest, codeBadRequest, "file id must be a positive integer")
		return
	}
	visible, err := a.itemVisible(r, id, itemID)
	if err != nil || !visible {
		writeError(w, http.StatusNotFound, codeNotFound, "not found")
		return
	}
	path, err := a.cat.FilePath(r.Context(), itemID, fileID)
	if err != nil {
		writeError(w, http.StatusNotFound, codeNotFound, "not found")
		return
	}
	if !a.pathAllowed(r, path) {
		writeError(w, http.StatusNotFound, codeNotFound, "not found")
		return
	}
	f, err := os.Open(path)
	if err != nil {
		writeError(w, http.StatusNotFound, codeNotFound, "not found")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		fail(w, err, "stream")
		return
	}
	w.Header().Set("Content-Type", mediaContentType(path))
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	// ServeContent implements Range, If-Range and conditional requests, which
	// is what the player relies on for seeking.
	http.ServeContent(w, r, filepath.Base(path), info.ModTime(), f)
}

func (a *API) itemDownload(w http.ResponseWriter, r *http.Request) {
	id := requireUser(w, r)
	if id == nil {
		return
	}
	if id.User.Role == auth.RoleRestricted {
		writeError(w, http.StatusForbidden, codeForbidden, "downloads are not available to this account")
		return
	}
	itemID, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, codeBadRequest, "item id must be a positive integer")
		return
	}
	visible, err := a.itemVisible(r, id, itemID)
	if err != nil || !visible {
		writeError(w, http.StatusNotFound, codeNotFound, "not found")
		return
	}
	paths, err := a.cat.ItemFilePaths(r.Context(), itemID)
	if err != nil || len(paths) == 0 {
		writeError(w, http.StatusNotFound, codeNotFound, "not found")
		return
	}
	for _, p := range paths {
		if !a.pathAllowed(r, p) {
			writeError(w, http.StatusNotFound, codeNotFound, "not found")
			return
		}
	}

	if len(paths) == 1 {
		f, err := os.Open(paths[0])
		if err != nil {
			writeError(w, http.StatusNotFound, codeNotFound, "not found")
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			fail(w, err, "download")
			return
		}
		w.Header().Set("Content-Type", mediaContentType(paths[0]))
		w.Header().Set("Content-Disposition", contentDisposition(filepath.Base(paths[0])))
		http.ServeContent(w, r, filepath.Base(paths[0]), info.ModTime(), f)
		return
	}

	detail, err := a.cat.Item(r.Context(), itemID, 0)
	if err != nil {
		fail(w, err, "download")
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", contentDisposition(safeFilename(detail.Title)+".zip"))
	w.WriteHeader(http.StatusOK)

	zw := zip.NewWriter(w)
	defer zw.Close()
	for _, p := range paths {
		entry, err := zw.Create(filepath.Base(p))
		if err != nil {
			log.Warn().Err(err).Msg("building download archive failed")
			return
		}
		f, err := os.Open(p)
		if err != nil {
			log.Warn().Err(err).Str("path", filepath.Base(p)).Msg("skipping unreadable file in download")
			continue
		}
		_, copyErr := io.Copy(entry, f)
		f.Close()
		if copyErr != nil {
			return
		}
	}
}

func mediaContentType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp3":
		return "audio/mpeg"
	case ".m4b", ".m4a":
		return "audio/mp4"
	case ".mp4":
		return "video/mp4"
	case ".epub":
		return "application/epub+zip"
	}
	return "application/octet-stream"
}

// contentDisposition builds an attachment header with a filename that cannot
// break out of the quoted string.
func contentDisposition(name string) string {
	return `attachment; filename="` + safeFilename(name) + `"`
}

// safeFilename strips anything that could confuse a Content-Disposition
// header or a client's save dialog.
func safeFilename(name string) string {
	name = strings.Map(func(r rune) rune {
		switch {
		case r < 0x20, r == '"', r == '\\', r == '/', r == 0x7f:
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if name == "" {
		name = "download"
	}
	if len(name) > 120 {
		name = name[:120]
	}
	return name
}
