package api

import (
	"net/http"

	"github.com/rake-pro/go-bookshelf/internal/library"
)

func (a *API) home(w http.ResponseWriter, r *http.Request) {
	id := requireUser(w, r)
	if id == nil {
		return
	}
	allowed, err := a.allowedLibraries(r, id)
	if err != nil {
		fail(w, err, "library access")
		return
	}
	home, err := a.cat.HomeFor(r.Context(), id.User.ID, allowed)
	if err != nil {
		fail(w, err, "home")
		return
	}
	writeJSON(w, http.StatusOK, home)
}

func (a *API) listAuthors(w http.ResponseWriter, r *http.Request) {
	id := requireUser(w, r)
	if id == nil {
		return
	}
	allowed, err := a.allowedLibraries(r, id)
	if err != nil {
		fail(w, err, "library access")
		return
	}
	authors, total, err := a.cat.Authors(r.Context(), allowed,
		r.URL.Query().Get("q"), queryInt(r, "limit", 0), queryInt(r, "offset", 0))
	if err != nil {
		fail(w, err, "authors")
		return
	}
	writeJSON(w, http.StatusOK, listBody[library.Author]{Items: authors, Total: total})
}

func (a *API) getAuthor(w http.ResponseWriter, r *http.Request) {
	id := requireUser(w, r)
	if id == nil {
		return
	}
	personID, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, codeBadRequest, "author id must be a positive integer")
		return
	}
	person, err := a.cat.PersonByID(r.Context(), personID)
	if err != nil {
		fail(w, err, "author")
		return
	}
	allowed, err := a.allowedLibraries(r, id)
	if err != nil {
		fail(w, err, "library access")
		return
	}
	items, total, err := a.cat.ListItems(r.Context(), library.ListOptions{
		AllowedLibraries: allowed, AuthorID: personID, UserID: id.User.ID,
		Sort: r.URL.Query().Get("sort"), Limit: queryInt(r, "limit", 0), Offset: queryInt(r, "offset", 0),
	})
	if err != nil {
		fail(w, err, "author items")
		return
	}
	person.ItemCount = total
	writeJSON(w, http.StatusOK, map[string]any{
		"author": person,
		"items":  items,
		"total":  total,
	})
}

func (a *API) listSeries(w http.ResponseWriter, r *http.Request) {
	id := requireUser(w, r)
	if id == nil {
		return
	}
	allowed, err := a.allowedLibraries(r, id)
	if err != nil {
		fail(w, err, "library access")
		return
	}
	series, total, err := a.cat.SeriesList(r.Context(), allowed,
		r.URL.Query().Get("q"), queryInt(r, "limit", 0), queryInt(r, "offset", 0))
	if err != nil {
		fail(w, err, "series")
		return
	}
	writeJSON(w, http.StatusOK, listBody[library.Series]{Items: series, Total: total})
}

func (a *API) getSeries(w http.ResponseWriter, r *http.Request) {
	id := requireUser(w, r)
	if id == nil {
		return
	}
	seriesID, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, codeBadRequest, "series id must be a positive integer")
		return
	}
	series, err := a.cat.SeriesByID(r.Context(), seriesID)
	if err != nil {
		fail(w, err, "series")
		return
	}
	allowed, err := a.allowedLibraries(r, id)
	if err != nil {
		fail(w, err, "library access")
		return
	}
	items, total, err := a.cat.ListItems(r.Context(), library.ListOptions{
		AllowedLibraries: allowed, SeriesID: seriesID, UserID: id.User.ID,
		Sort: "title", Limit: queryInt(r, "limit", 0), Offset: queryInt(r, "offset", 0),
	})
	if err != nil {
		fail(w, err, "series items")
		return
	}
	series.ItemCount = total
	writeJSON(w, http.StatusOK, map[string]any{
		"series": series,
		"items":  items,
		"total":  total,
	})
}

func (a *API) listTags(w http.ResponseWriter, r *http.Request) {
	id := requireUser(w, r)
	if id == nil {
		return
	}
	allowed, err := a.allowedLibraries(r, id)
	if err != nil {
		fail(w, err, "library access")
		return
	}
	tags, total, err := a.cat.Tags(r.Context(), allowed, queryInt(r, "limit", 0), queryInt(r, "offset", 0))
	if err != nil {
		fail(w, err, "tags")
		return
	}
	writeJSON(w, http.StatusOK, listBody[library.Tag]{Items: tags, Total: total})
}

// search returns matches grouped by kind, which is what the search page shows.
func (a *API) search(w http.ResponseWriter, r *http.Request) {
	id := requireUser(w, r)
	if id == nil {
		return
	}
	query := r.URL.Query().Get("q")
	allowed, err := a.allowedLibraries(r, id)
	if err != nil {
		fail(w, err, "library access")
		return
	}
	limit := queryInt(r, "limit", 20)

	items, itemTotal, err := a.cat.ListItems(r.Context(), library.ListOptions{
		AllowedLibraries: allowed, Query: query, UserID: id.User.ID, Limit: limit, Sort: "title",
	})
	if err != nil {
		fail(w, err, "search items")
		return
	}
	authors, authorTotal, err := a.cat.Authors(r.Context(), allowed, query, limit, 0)
	if err != nil {
		fail(w, err, "search authors")
		return
	}
	series, seriesTotal, err := a.cat.SeriesList(r.Context(), allowed, query, limit, 0)
	if err != nil {
		fail(w, err, "search series")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"query":   query,
		"items":   listBody[library.Item]{Items: items, Total: itemTotal},
		"authors": listBody[library.Author]{Items: authors, Total: authorTotal},
		"series":  listBody[library.Series]{Items: series, Total: seriesTotal},
	})
}
