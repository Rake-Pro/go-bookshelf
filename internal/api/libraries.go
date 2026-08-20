package api

import (
	"net/http"
	"strings"

	"github.com/rake-pro/go-bookshelf/internal/library"
	"github.com/rs/zerolog/log"
)

func (a *API) listLibraries(w http.ResponseWriter, r *http.Request) {
	id := requireUser(w, r)
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
		fail(w, err, "libraries")
		return
	}
	// Only an administrator sees the on-disk paths.
	if !id.User.IsAdmin() {
		for i := range libs {
			libs[i].Paths = []string{}
		}
	}
	writeJSON(w, http.StatusOK, listBody[library.Library]{Items: libs, Total: len(libs)})
}

func (a *API) createLibrary(w http.ResponseWriter, r *http.Request) {
	if requireAdmin(w, r) == nil {
		return
	}
	var body struct {
		Name          string   `json:"name"`
		Kind          string   `json:"kind"`
		Paths         []string `json:"paths"`
		CreateMissing bool     `json:"create_missing"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.CreateMissing {
		for _, p := range body.Paths {
			if err := ensureLibraryDir(strings.TrimSpace(p)); err != nil {
				writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
				return
			}
		}
	}
	lib, err := a.cat.CreateLibrary(r.Context(), body.Name, body.Kind, body.Paths)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	log.Info().Int64("library", lib.ID).Str("kind", lib.Kind).Msg("library created")
	writeJSON(w, http.StatusCreated, lib)
}

func (a *API) patchLibrary(w http.ResponseWriter, r *http.Request) {
	if requireAdmin(w, r) == nil {
		return
	}
	libID, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, codeBadRequest, "library id must be a positive integer")
		return
	}
	var body struct {
		Name  *string  `json:"name"`
		Kind  *string  `json:"kind"`
		Paths []string `json:"paths"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	lib, err := a.cat.UpdateLibrary(r.Context(), libID, body.Name, body.Kind, body.Paths)
	if err != nil {
		fail(w, err, "patch library")
		return
	}
	writeJSON(w, http.StatusOK, lib)
}

func (a *API) deleteLibrary(w http.ResponseWriter, r *http.Request) {
	if requireAdmin(w, r) == nil {
		return
	}
	libID, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, codeBadRequest, "library id must be a positive integer")
		return
	}
	if err := a.cat.DeleteLibrary(r.Context(), libID); err != nil {
		fail(w, err, "delete library")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (a *API) scanLibrary(w http.ResponseWriter, r *http.Request) {
	if requireAdmin(w, r) == nil {
		return
	}
	libID, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, codeBadRequest, "library id must be a positive integer")
		return
	}
	if _, err := a.cat.LibraryByID(r.Context(), libID); err != nil {
		fail(w, err, "scan library")
		return
	}
	run, err := a.scanner.ScanLibrary(r.Context(), libID)
	if err != nil {
		fail(w, err, "scan library")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"scan_id": run.ID, "scan": run})
}

func (a *API) listScans(w http.ResponseWriter, r *http.Request) {
	id := requireUser(w, r)
	if id == nil {
		return
	}
	libID, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, codeBadRequest, "library id must be a positive integer")
		return
	}
	allowedLib, err := a.auth.CanAccessLibrary(r.Context(), id.User, libID)
	if err != nil {
		fail(w, err, "library access")
		return
	}
	if !allowedLib {
		writeError(w, http.StatusNotFound, codeNotFound, "not found")
		return
	}
	runs, err := a.cat.ScanRuns(r.Context(), libID, queryInt(r, "limit", 0))
	if err != nil {
		fail(w, err, "scans")
		return
	}
	writeJSON(w, http.StatusOK, listBody[library.ScanRun]{Items: runs, Total: len(runs)})
}
