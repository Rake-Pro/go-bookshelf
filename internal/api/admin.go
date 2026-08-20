package api

import (
	"errors"
	"net/http"
	"os"
	"time"

	"github.com/rake-pro/go-bookshelf/internal/auth"
	"github.com/rake-pro/go-bookshelf/internal/library"
	"github.com/rake-pro/go-bookshelf/internal/store"
	"github.com/rs/zerolog/log"
)

func (a *API) listUsers(w http.ResponseWriter, r *http.Request) {
	if requireAdmin(w, r) == nil {
		return
	}
	users, err := a.auth.ListUsers(r.Context())
	if err != nil {
		fail(w, err, "users")
		return
	}
	out := make([]*auth.User, 0, len(users))
	out = append(out, users...)
	writeJSON(w, http.StatusOK, listBody[*auth.User]{Items: out, Total: len(out)})
}

func (a *API) createUser(w http.ResponseWriter, r *http.Request) {
	if requireAdmin(w, r) == nil {
		return
	}
	var body struct {
		Username    string  `json:"username"`
		Password    string  `json:"password"`
		DisplayName string  `json:"display_name"`
		Role        string  `json:"role"`
		Libraries   []int64 `json:"libraries"`
		CanUpload   bool    `json:"can_upload"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Role == "" {
		body.Role = auth.RoleUser
	}
	user, err := a.auth.CreateUser(r.Context(), body.Username, body.Password, body.DisplayName, body.Role)
	switch {
	case errors.Is(err, auth.ErrWeakPassword):
		writeError(w, http.StatusBadRequest, codeBadRequest, auth.ErrWeakPassword.Error())
		return
	case err != nil:
		writeError(w, http.StatusBadRequest, codeBadRequest, "could not create the account")
		return
	}
	if len(body.Libraries) > 0 {
		if err := a.auth.SetLibraryAccess(r.Context(), user.ID, body.Libraries); err != nil {
			fail(w, err, "library access")
			return
		}
	}
	if body.CanUpload {
		if err := a.auth.SetCanUpload(r.Context(), user.ID, true); err != nil {
			fail(w, err, "upload permission")
			return
		}
		user, err = a.auth.UserByID(r.Context(), user.ID)
		if err != nil {
			fail(w, err, "upload permission")
			return
		}
	}
	log.Info().Str("username", user.Username).Str("role", user.Role).Msg("account created")
	writeJSON(w, http.StatusCreated, user)
}

func (a *API) patchUser(w http.ResponseWriter, r *http.Request) {
	admin := requireAdmin(w, r)
	if admin == nil {
		return
	}
	userID, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, codeBadRequest, "user id must be a positive integer")
		return
	}
	var body struct {
		DisplayName *string `json:"display_name"`
		Role        *string `json:"role"`
		Password    *string `json:"password"`
		Disabled    *bool   `json:"disabled"`
		CanUpload   *bool   `json:"can_upload"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if _, err := a.auth.UserByID(r.Context(), userID); err != nil {
		fail(w, err, "patch user")
		return
	}
	if body.Role != nil {
		// An administrator must not be able to lock every admin out.
		if *body.Role != auth.RoleAdmin && userID == admin.User.ID {
			writeError(w, http.StatusBadRequest, codeBadRequest, "an administrator cannot demote their own account")
			return
		}
		if err := a.auth.SetRole(r.Context(), userID, *body.Role); err != nil {
			writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
			return
		}
	}
	if body.Disabled != nil {
		if *body.Disabled && userID == admin.User.ID {
			writeError(w, http.StatusBadRequest, codeBadRequest, "an administrator cannot disable their own account")
			return
		}
		if err := a.auth.SetDisabled(r.Context(), userID, *body.Disabled); err != nil {
			fail(w, err, "patch user")
			return
		}
	}
	if body.Password != nil {
		if err := a.auth.SetPassword(r.Context(), userID, *body.Password); err != nil {
			writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
			return
		}
	}
	if body.CanUpload != nil {
		if err := a.auth.SetCanUpload(r.Context(), userID, *body.CanUpload); err != nil {
			fail(w, err, "upload permission")
			return
		}
	}
	if body.DisplayName != nil {
		if _, err := a.db.ExecContext(r.Context(),
			`UPDATE users SET display_name = ? WHERE id = ?`, *body.DisplayName, userID); err != nil {
			fail(w, err, "patch user")
			return
		}
	}
	user, err := a.auth.UserByID(r.Context(), userID)
	if err != nil {
		fail(w, err, "patch user")
		return
	}
	writeJSON(w, http.StatusOK, user)
}

func (a *API) deleteUser(w http.ResponseWriter, r *http.Request) {
	admin := requireAdmin(w, r)
	if admin == nil {
		return
	}
	userID, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, codeBadRequest, "user id must be a positive integer")
		return
	}
	if userID == admin.User.ID {
		writeError(w, http.StatusBadRequest, codeBadRequest, "an administrator cannot delete their own account")
		return
	}
	if err := a.auth.DeleteUser(r.Context(), userID); err != nil {
		fail(w, err, "delete user")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (a *API) putUserLibraries(w http.ResponseWriter, r *http.Request) {
	if requireAdmin(w, r) == nil {
		return
	}
	userID, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, codeBadRequest, "user id must be a positive integer")
		return
	}
	var body struct {
		Libraries []int64 `json:"libraries"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := a.auth.SetLibraryAccess(r.Context(), userID, body.Libraries); err != nil {
		fail(w, err, "library access")
		return
	}
	user, err := a.auth.UserByID(r.Context(), userID)
	if err != nil {
		fail(w, err, "library access")
		return
	}
	ids, err := a.auth.LibraryIDs(r.Context(), user)
	if err != nil {
		fail(w, err, "library access")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user_id": userID, "libraries": ids})
}

func (a *API) systemStatus(w http.ResponseWriter, r *http.Request) {
	if requireAdmin(w, r) == nil {
		return
	}
	counts, err := a.cat.ItemCounts(r.Context())
	if err != nil {
		fail(w, err, "system status")
		return
	}
	libs, err := a.cat.Libraries(r.Context(), nil)
	if err != nil {
		fail(w, err, "system status")
		return
	}
	lastScans := map[int64]*library.ScanRun{}
	for _, l := range libs {
		runs, err := a.cat.ScanRuns(r.Context(), l.ID, 1)
		if err != nil {
			fail(w, err, "system status")
			return
		}
		if len(runs) > 0 {
			lastScans[l.ID] = &runs[0]
		}
	}
	// SQLite: size of the database file. Postgres: pg_database_size of the
	// connected database (includes indexes and cover bytes).
	var dbSize int64
	switch a.db.Dialect().Name() {
	case store.DriverPostgres:
		if err := a.db.QueryRowContext(r.Context(),
			`SELECT pg_database_size(current_database())`).Scan(&dbSize); err != nil {
			dbSize = 0
		}
	default:
		if a.cfg.DBPath != "" {
			if info, err := os.Stat(a.cfg.DBPath); err == nil {
				dbSize = info.Size()
			}
		}
	}
	users, err := a.auth.UserCount(r.Context())
	if err != nil {
		fail(w, err, "system status")
		return
	}
	set := a.settings.Get()
	writeJSON(w, http.StatusOK, map[string]any{
		"version":       a.version,
		"go_version":    goVersion(),
		"db_driver":     a.cfg.DBDriver,
		"db_path":       a.cfg.DBPath,
		"db_dsn":        a.cfg.SafeDSN(),
		"db_size_bytes": dbSize,
		"data_dir":      a.cfg.DataDir,
		"counts": map[string]int{
			"ebooks":     counts[library.KindEbook],
			"audiobooks": counts[library.KindAudiobook],
		},
		"libraries":           libs,
		"users":               users,
		"last_scans":          lastScans,
		"oidc_enabled":        a.auth.OIDCEnabled(),
		"local_login":         a.auth.LocalLoginEnabled(),
		"settings_updated_at": set.UpdatedAt,
		"base_url":            set.General.BaseURL,
		"time":                time.Now().UTC().Format(time.RFC3339),
	})
}

func goVersion() string { return runtimeVersion }
