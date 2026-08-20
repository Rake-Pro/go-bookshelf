package api

import (
	"net/http"

	"github.com/rake-pro/go-bookshelf/internal/auth"
	"github.com/rake-pro/go-bookshelf/internal/library"
)

func (a *API) getSettings(w http.ResponseWriter, r *http.Request) {
	id := requireUser(w, r)
	if id == nil {
		return
	}
	settings, err := a.cat.SettingsFor(r.Context(), id.User.ID)
	if err != nil {
		fail(w, err, "settings")
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (a *API) putSettings(w http.ResponseWriter, r *http.Request) {
	id := requireWrite(w, r)
	if id == nil {
		return
	}
	// Start from what is stored so a partial PUT keeps the other groups.
	settings, err := a.cat.SettingsFor(r.Context(), id.User.ID)
	if err != nil {
		fail(w, err, "settings")
		return
	}
	if !decodeJSON(w, r, &settings) {
		return
	}
	saved, err := a.cat.SaveSettings(r.Context(), id.User.ID, settings)
	if err != nil {
		fail(w, err, "save settings")
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (a *API) getProgress(w http.ResponseWriter, r *http.Request) {
	id := requireUser(w, r)
	if id == nil {
		return
	}
	rows, err := a.cat.ProgressSince(r.Context(), id.User.ID, r.URL.Query().Get("since"))
	if err != nil {
		fail(w, err, "progress")
		return
	}
	writeJSON(w, http.StatusOK, listBody[library.Progress]{Items: rows, Total: len(rows)})
}

func (a *API) putProgress(w http.ResponseWriter, r *http.Request) {
	id := requireWrite(w, r)
	if id == nil {
		return
	}
	itemID, ok := pathID(r, "item_id")
	if !ok {
		writeError(w, http.StatusBadRequest, codeBadRequest, "item id must be a positive integer")
		return
	}
	visible, err := a.itemVisible(r, id, itemID)
	if err != nil || !visible {
		writeError(w, http.StatusNotFound, codeNotFound, "not found")
		return
	}
	var body struct {
		Locator    *string  `json:"locator"`
		PositionMS *int64   `json:"position_ms"`
		Percent    *float64 `json:"percent"`
		Finished   *bool    `json:"finished"`
		Device     *string  `json:"device"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}

	current, err := a.cat.ProgressSince(r.Context(), id.User.ID, "")
	if err != nil {
		fail(w, err, "progress")
		return
	}
	p := library.Progress{ItemID: itemID}
	for _, existing := range current {
		if existing.ItemID == itemID {
			p = existing
			break
		}
	}
	if body.Locator != nil {
		p.Locator = *body.Locator
	}
	if body.PositionMS != nil {
		p.PositionMS = *body.PositionMS
	}
	if body.Percent != nil {
		p.Percent = *body.Percent
	}
	if body.Finished != nil {
		p.Finished = *body.Finished
		if !p.Finished {
			p.FinishedAt = ""
		}
	}
	if body.Device != nil {
		p.Device = *body.Device
	}

	saved, err := a.cat.SaveProgress(r.Context(), id.User.ID, p)
	if err != nil {
		fail(w, err, "save progress")
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (a *API) listBookmarks(w http.ResponseWriter, r *http.Request) {
	id := requireUser(w, r)
	if id == nil {
		return
	}
	rows, err := a.cat.Bookmarks(r.Context(), id.User.ID, queryID(r, "item"))
	if err != nil {
		fail(w, err, "bookmarks")
		return
	}
	writeJSON(w, http.StatusOK, listBody[library.Bookmark]{Items: rows, Total: len(rows)})
}

func (a *API) createBookmark(w http.ResponseWriter, r *http.Request) {
	id := requireWrite(w, r)
	if id == nil {
		return
	}
	var body struct {
		ItemID     int64  `json:"item_id"`
		Locator    string `json:"locator"`
		PositionMS int64  `json:"position_ms"`
		Note       string `json:"note"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	visible, err := a.itemVisible(r, id, body.ItemID)
	if err != nil || !visible {
		writeError(w, http.StatusNotFound, codeNotFound, "not found")
		return
	}
	bookmark, err := a.cat.CreateBookmark(r.Context(), id.User.ID, library.Bookmark{
		ItemID: body.ItemID, Locator: body.Locator, PositionMS: body.PositionMS, Note: body.Note,
	})
	if err != nil {
		fail(w, err, "create bookmark")
		return
	}
	writeJSON(w, http.StatusCreated, bookmark)
}

func (a *API) deleteBookmark(w http.ResponseWriter, r *http.Request) {
	id := requireWrite(w, r)
	if id == nil {
		return
	}
	bookmarkID, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, codeBadRequest, "bookmark id must be a positive integer")
		return
	}
	if err := a.cat.DeleteBookmark(r.Context(), id.User.ID, bookmarkID); err != nil {
		fail(w, err, "delete bookmark")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (a *API) listTokens(w http.ResponseWriter, r *http.Request) {
	id := requireUser(w, r)
	if id == nil {
		return
	}
	tokens, err := a.auth.ListTokens(r.Context(), id.User.ID)
	if err != nil {
		fail(w, err, "tokens")
		return
	}
	writeJSON(w, http.StatusOK, listBody[auth.APIToken]{Items: tokens, Total: len(tokens)})
}

func (a *API) createToken(w http.ResponseWriter, r *http.Request) {
	id := requireWrite(w, r)
	if id == nil {
		return
	}
	// A token may never mint a token with more authority than itself.
	if id.Method == "token" {
		writeError(w, http.StatusForbidden, codeForbidden, "API tokens cannot issue further tokens")
		return
	}
	var body struct {
		Name   string   `json:"name"`
		Scopes []string `json:"scopes"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	token, secret, err := a.auth.CreateToken(r.Context(), id.User.ID, body.Name, body.Scopes)
	if err != nil {
		fail(w, err, "create token")
		return
	}
	// The secret is shown exactly once.
	writeJSON(w, http.StatusCreated, map[string]any{"token": token, "secret": secret})
}

func (a *API) deleteToken(w http.ResponseWriter, r *http.Request) {
	id := requireWrite(w, r)
	if id == nil {
		return
	}
	tokenID, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, codeBadRequest, "token id must be a positive integer")
		return
	}
	if err := a.auth.DeleteToken(r.Context(), id.User.ID, tokenID); err != nil {
		fail(w, err, "delete token")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// collection is a placeholder shape: collections are stored but have no
// management UI until a later milestone, so the list is always empty for now.
type collection struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	UserID *int64 `json:"user_id"`
	Items  int    `json:"items"`
}

func (a *API) listCollections(w http.ResponseWriter, r *http.Request) {
	id := requireUser(w, r)
	if id == nil {
		return
	}
	rows, err := a.db.QueryContext(r.Context(),
		`SELECT c.id, c.name, c.user_id, (SELECT count(*) FROM collection_items ci WHERE ci.collection_id = c.id)
		 FROM collections c WHERE c.user_id IS NULL OR c.user_id = ? ORDER BY lower(c.name)`, id.User.ID)
	if err != nil {
		fail(w, err, "collections")
		return
	}
	defer rows.Close()
	out := []collection{}
	for rows.Next() {
		var c collection
		if err := rows.Scan(&c.ID, &c.Name, &c.UserID, &c.Items); err != nil {
			fail(w, err, "collections")
			return
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		fail(w, err, "collections")
		return
	}
	writeJSON(w, http.StatusOK, listBody[collection]{Items: out, Total: len(out)})
}

func (a *API) createCollection(w http.ResponseWriter, r *http.Request) {
	id := requireWrite(w, r)
	if id == nil {
		return
	}
	var body struct {
		Name   string `json:"name"`
		Shared bool   `json:"shared"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Name == "" {
		writeError(w, http.StatusBadRequest, codeBadRequest, "name is required")
		return
	}
	if body.Shared && !id.User.IsAdmin() {
		writeError(w, http.StatusForbidden, codeForbidden, "only an administrator can create a shared collection")
		return
	}
	var owner any = id.User.ID
	if body.Shared {
		owner = nil
	}
	newID, err := a.db.InsertReturningID(r.Context(),
		`INSERT INTO collections (user_id, name) VALUES (?, ?)`, owner, body.Name)
	if err != nil {
		fail(w, err, "create collection")
		return
	}
	c := collection{ID: newID, Name: body.Name}
	if !body.Shared {
		uid := id.User.ID
		c.UserID = &uid
	}
	writeJSON(w, http.StatusCreated, c)
}
