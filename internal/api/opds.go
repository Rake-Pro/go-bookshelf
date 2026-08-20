package api

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rake-pro/go-bookshelf/internal/auth"
	"github.com/rake-pro/go-bookshelf/internal/library"
)

// OPDS media types.
const (
	opdsNavType         = "application/atom+xml;profile=opds-catalog;kind=navigation"
	opdsAcquisitionType = "application/atom+xml;profile=opds-catalog;kind=acquisition"
)

type atomLink struct {
	Rel   string `xml:"rel,attr,omitempty"`
	Href  string `xml:"href,attr"`
	Type  string `xml:"type,attr,omitempty"`
	Title string `xml:"title,attr,omitempty"`
}

type atomAuthor struct {
	Name string `xml:"name"`
}

type atomContent struct {
	Type string `xml:"type,attr"`
	Body string `xml:",chardata"`
}

type atomEntry struct {
	Title    string       `xml:"title"`
	ID       string       `xml:"id"`
	Updated  string       `xml:"updated"`
	Authors  []atomAuthor `xml:"author,omitempty"`
	Language string       `xml:"dc:language,omitempty"`
	Issued   string       `xml:"dc:issued,omitempty"`
	Content  *atomContent `xml:"content,omitempty"`
	Links    []atomLink   `xml:"link"`
}

type atomFeed struct {
	XMLName   xml.Name    `xml:"feed"`
	Xmlns     string      `xml:"xmlns,attr"`
	XmlnsDC   string      `xml:"xmlns:dc,attr"`
	XmlnsOPDS string      `xml:"xmlns:opds,attr"`
	ID        string      `xml:"id"`
	Title     string      `xml:"title"`
	Updated   string      `xml:"updated"`
	Links     []atomLink  `xml:"link"`
	Entries   []atomEntry `xml:"entry"`
}

func newFeed(id, title string) *atomFeed {
	return &atomFeed{
		Xmlns:     "http://www.w3.org/2005/Atom",
		XmlnsDC:   "http://purl.org/dc/terms/",
		XmlnsOPDS: "http://opds-spec.org/2010/catalog",
		ID:        id,
		Title:     title,
		Updated:   time.Now().UTC().Format(time.RFC3339),
	}
}

// opdsAuth authenticates an OPDS request. OPDS clients speak HTTP Basic, so
// the token goes in the password field; a session cookie is also accepted for
// a browser following the same links.
func (a *API) opdsAuth(w http.ResponseWriter, r *http.Request) *auth.Identity {
	id, err := a.auth.Authenticate(r.Context(), r)
	if err != nil || id == nil {
		w.Header().Set("WWW-Authenticate", `Basic realm="go-bookshelf", charset="UTF-8"`)
		writeError(w, http.StatusUnauthorized, codeUnauthorized, "authentication required")
		return nil
	}
	return id
}

func (a *API) writeFeed(w http.ResponseWriter, feed *atomFeed, contentType string) {
	w.Header().Set("Content-Type", contentType+"; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusOK)

	if _, err := w.Write([]byte(xml.Header)); err != nil {
		return
	}
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	// Every value passes through the encoder, so metadata taken from a book
	// is escaped rather than injected into the document.
	_ = enc.Encode(feed)
	_ = enc.Flush()
}

func (a *API) opdsRoot(w http.ResponseWriter, r *http.Request) {
	id := a.opdsAuth(w, r)
	if id == nil {
		return
	}
	allowed, err := a.allowedLibraries(r, id)
	if err != nil {
		fail(w, err, "library access")
		return
	}
	libs, err := a.cat.Libraries(r.Context(), allowed)
	if err != nil {
		fail(w, err, "opds root")
		return
	}

	feed := newFeed(a.baseURL()+"/opds", "go-bookshelf")
	feed.Links = []atomLink{
		{Rel: "self", Href: "/opds", Type: opdsNavType},
		{Rel: "start", Href: "/opds", Type: opdsNavType},
		{Rel: "search", Href: "/opds/search?q={searchTerms}", Type: opdsAcquisitionType},
	}
	for _, l := range libs {
		href := "/opds/" + strconv.FormatInt(l.ID, 10)
		feed.Entries = append(feed.Entries, atomEntry{
			Title:   l.Name,
			ID:      fmt.Sprintf("%s/opds/%d", a.baseURL(), l.ID),
			Updated: feed.Updated,
			Content: &atomContent{Type: "text", Body: fmt.Sprintf("%d items", l.ItemCount)},
			Links:   []atomLink{{Rel: "subsection", Href: href, Type: opdsAcquisitionType}},
		})
	}
	a.writeFeed(w, feed, opdsNavType)
}

func (a *API) opdsLibrary(w http.ResponseWriter, r *http.Request) {
	id := a.opdsAuth(w, r)
	if id == nil {
		return
	}
	libID, err := strconv.ParseInt(r.PathValue("library"), 10, 64)
	if err != nil || libID <= 0 {
		writeError(w, http.StatusNotFound, codeNotFound, "not found")
		return
	}
	ok, err := a.auth.CanAccessLibrary(r.Context(), id.User, libID)
	if err != nil || !ok {
		writeError(w, http.StatusNotFound, codeNotFound, "not found")
		return
	}
	lib, err := a.cat.LibraryByID(r.Context(), libID)
	if err != nil {
		fail(w, err, "opds library")
		return
	}
	allowed, err := a.allowedLibraries(r, id)
	if err != nil {
		fail(w, err, "library access")
		return
	}
	items, _, err := a.cat.ListItems(r.Context(), library.ListOptions{
		AllowedLibraries: allowed, LibraryID: libID, UserID: id.User.ID,
		Sort: "title", Limit: queryInt(r, "limit", 100), Offset: queryInt(r, "offset", 0),
	})
	if err != nil {
		fail(w, err, "opds library")
		return
	}

	feed := newFeed(fmt.Sprintf("%s/opds/%d", a.baseURL(), libID), lib.Name)
	feed.Links = []atomLink{
		{Rel: "self", Href: "/opds/" + strconv.FormatInt(libID, 10), Type: opdsAcquisitionType},
		{Rel: "start", Href: "/opds", Type: opdsNavType},
	}
	a.appendItemEntries(r, feed, items)
	a.writeFeed(w, feed, opdsAcquisitionType)
}

func (a *API) opdsSearch(w http.ResponseWriter, r *http.Request) {
	id := a.opdsAuth(w, r)
	if id == nil {
		return
	}
	allowed, err := a.allowedLibraries(r, id)
	if err != nil {
		fail(w, err, "library access")
		return
	}
	query := r.URL.Query().Get("q")
	items, _, err := a.cat.ListItems(r.Context(), library.ListOptions{
		AllowedLibraries: allowed, Query: query, UserID: id.User.ID,
		Sort: "title", Limit: queryInt(r, "limit", 100),
	})
	if err != nil {
		fail(w, err, "opds search")
		return
	}
	feed := newFeed(a.baseURL()+"/opds/search", "Search: "+query)
	feed.Links = []atomLink{
		{Rel: "self", Href: "/opds/search?q=" + urlQueryEscape(query), Type: opdsAcquisitionType},
		{Rel: "start", Href: "/opds", Type: opdsNavType},
	}
	a.appendItemEntries(r, feed, items)
	a.writeFeed(w, feed, opdsAcquisitionType)
}

func (a *API) appendItemEntries(r *http.Request, feed *atomFeed, items []library.Item) {
	for _, it := range items {
		base := fmt.Sprintf("/api/v1/items/%d", it.ID)
		entry := atomEntry{
			Title:    it.Title,
			ID:       fmt.Sprintf("%s/items/%d", a.baseURL(), it.ID),
			Updated:  it.UpdatedAt,
			Language: "",
			Links: []atomLink{
				{Rel: "http://opds-spec.org/acquisition", Href: base + "/download", Type: acquisitionType(it.Kind)},
			},
		}
		for _, name := range it.Authors {
			entry.Authors = append(entry.Authors, atomAuthor{Name: name})
		}
		if it.Subtitle != "" {
			entry.Content = &atomContent{Type: "text", Body: it.Subtitle}
		}
		if it.CoverURL != "" {
			entry.Links = append(entry.Links,
				atomLink{Rel: "http://opds-spec.org/image", Href: base + "/cover", Type: "image/jpeg"},
				atomLink{Rel: "http://opds-spec.org/image/thumbnail", Href: base + "/cover?size=thumb", Type: "image/jpeg"})
		}
		feed.Entries = append(feed.Entries, entry)
	}
}

func acquisitionType(kind string) string {
	if kind == library.KindAudiobook {
		return "audio/mpeg"
	}
	return "application/epub+zip"
}

func urlQueryEscape(v string) string {
	var b strings.Builder
	for _, c := range []byte(v) {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}
