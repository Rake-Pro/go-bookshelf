package api

// Adding books: the two ways a book gets into a library from the browser.
//
// An upload is a multipart request that is never buffered - the handler reads
// straight off the wire into the upload service, which stages, validates and
// files it. A URL import is a queued job, because fetching somebody else's
// site can take minutes and must not hold a request open for them.
//
// Both end in the same place: internal/upload is the only code in the server
// that writes into a library directory, and everything it writes it has parsed
// first.

import (
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rake-pro/go-bookshelf/internal/auth"
	"github.com/rake-pro/go-bookshelf/internal/importer"
	"github.com/rake-pro/go-bookshelf/internal/library"
	"github.com/rake-pro/go-bookshelf/internal/remote"
	"github.com/rake-pro/go-bookshelf/internal/store"
	"github.com/rake-pro/go-bookshelf/internal/upload"
	"github.com/rs/zerolog/log"
)

// uploadScanWait is how long an upload request waits for the scan that follows
// it before answering 202 and letting the scan finish on its own.
const uploadScanWait = 20 * time.Second

// codeTooLarge is the error code for a file over the size cap. It is its own
// code rather than a bad_request so a client can tell "this file is too big"
// from "this file is not a book" without reading the message.
const codeTooLarge = "too_large"

// inflight is the per-user concurrency guard on uploads. One upload at a time
// per account: the size cap is 2 GiB, and a handful of parallel requests from
// one browser tab would otherwise be a way to fill a disk at n times the rate
// the rate limiter thinks it is allowing.
type inflight struct {
	mu   sync.Mutex
	busy map[int64]bool
}

func newInflight() *inflight { return &inflight{busy: map[int64]bool{}} }

func (f *inflight) begin(userID int64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.busy[userID] {
		return false
	}
	f.busy[userID] = true
	return true
}

func (f *inflight) end(userID int64) {
	f.mu.Lock()
	delete(f.busy, userID)
	f.mu.Unlock()
}

// requireUploader returns the identity for a request that adds books.
func requireUploader(w http.ResponseWriter, r *http.Request) *auth.Identity {
	id := requireWrite(w, r)
	if id == nil {
		return nil
	}
	if !id.User.MayUpload() {
		writeError(w, http.StatusForbidden, codeForbidden, "this account may not add books")
		return nil
	}
	return id
}

// libraryForWrite resolves the library an add-books request names, answering
// 404 when the caller cannot see it - the same answer they get for a library
// that does not exist, so the API never confirms one they have no access to.
func (a *API) libraryForWrite(w http.ResponseWriter, r *http.Request, id *auth.Identity) *library.Library {
	libID, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, codeBadRequest, "library id must be a positive integer")
		return nil
	}
	allowed, err := a.auth.CanAccessLibrary(r.Context(), id.User, libID)
	if err != nil {
		fail(w, err, "library access")
		return nil
	}
	if !allowed {
		writeError(w, http.StatusNotFound, codeNotFound, "not found")
		return nil
	}
	lib, err := a.cat.LibraryByID(r.Context(), libID)
	if err != nil {
		fail(w, err, "library")
		return nil
	}
	return lib
}

// uploadFiles handles POST /libraries/{id}/upload.
func (a *API) uploadFiles(w http.ResponseWriter, r *http.Request) {
	id := requireUploader(w, r)
	if id == nil {
		return
	}
	lib := a.libraryForWrite(w, r, id)
	if lib == nil {
		return
	}
	if !a.uploadLimiter.Allow(uploadKey(id.User.ID)) {
		writeError(w, http.StatusTooManyRequests, codeRateLimited, "too many uploads; try again in a moment")
		return
	}
	if !a.uploading.begin(id.User.ID) {
		writeError(w, http.StatusTooManyRequests, codeRateLimited,
			"another upload from this account is still in progress")
		return
	}
	defer a.uploading.end(id.User.ID)

	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.HasPrefix(mediaType, "multipart/") {
		writeError(w, http.StatusBadRequest, codeBadRequest, "this endpoint takes a multipart/form-data body")
		return
	}
	reader, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, "the multipart body could not be read")
		return
	}
	src, err := newMultipartSource(reader)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, "the multipart body could not be read")
		return
	}
	// The subfolder may be given in the query string, or as a form field sent
	// before the first file - the request is streamed, so a field that arrives
	// after the files it was meant to describe is already too late.
	subdir := strings.TrimSpace(r.URL.Query().Get("subdir"))
	if subdir == "" {
		subdir = src.subdir
	}

	accepted, err := a.uploads.Accept(r.Context(), lib, subdir, src)
	if err != nil {
		writeUploadError(w, err)
		return
	}

	resolved, complete := a.uploads.ScanAndResolve(r.Context(), lib.ID, accepted, uploadScanWait)
	status, state := http.StatusCreated, "complete"
	if !complete {
		status, state = http.StatusAccepted, "scanning"
	}
	log.Info().Int64("library", lib.ID).Str("user", id.User.Username).Int("files", len(resolved)).
		Msg("books uploaded")
	writeJSON(w, status, map[string]any{"status": state, "files": resolved})
}

// writeUploadError maps a validation failure onto a status code. Every one of
// these is the user's mistake to fix, so the message is theirs to read.
func writeUploadError(w http.ResponseWriter, err error) {
	var dup *upload.DuplicateError
	switch {
	case errors.As(err, &dup):
		// The conflict body carries the existing item alongside the standard
		// envelope, so the client can offer "open the copy you already have".
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": map[string]string{
				"code":    codeConflict,
				"message": "that file is already in the library",
			},
			"item_id": dup.ItemID,
			"title":   dup.Title,
		})
	case errors.Is(err, upload.ErrTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, codeTooLarge, userFacing(err))
	case errors.Is(err, upload.ErrExtension), errors.Is(err, upload.ErrMagic),
		errors.Is(err, upload.ErrParse), errors.Is(err, upload.ErrEmpty),
		errors.Is(err, upload.ErrSubdir), errors.Is(err, upload.ErrNoFiles),
		errors.Is(err, upload.ErrTooManyFiles), errors.Is(err, upload.ErrNoPath):
		writeError(w, http.StatusBadRequest, codeBadRequest, userFacing(err))
	case errors.Is(err, upload.ErrNotWritable):
		log.Error().Err(err).Msg("a library directory is not writable")
		writeError(w, http.StatusInternalServerError, codeInternal,
			"the library's directory is not writable by the server")
	default:
		fail(w, err, "upload")
	}
}

// userFacing strips the package prefix off an upload error. The message is
// shown to whoever tried the upload, and "upload: " in front of it tells them
// nothing they do not already know.
func userFacing(err error) string {
	return strings.TrimPrefix(err.Error(), "upload: ")
}

func uploadKey(userID int64) string { return "user:" + itoa64(userID) }

// multipartSource adapts a streamed multipart body to upload.Source.
//
// The form fields that precede the files are read on construction, so the
// subfolder is known before the first byte of the first file is written; from
// then on each part is handed over as it comes and read to its end by the
// upload service before the next is requested.
type multipartSource struct {
	reader  *multipart.Reader
	pending *multipart.Part
	subdir  string
	done    bool
}

func newMultipartSource(reader *multipart.Reader) (*multipartSource, error) {
	src := &multipartSource{reader: reader}
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			src.done = true
			return src, nil
		}
		if err != nil {
			return nil, err
		}
		if part.FileName() != "" {
			src.pending = part
			return src, nil
		}
		if part.FormName() == "subdir" {
			value, err := io.ReadAll(io.LimitReader(part, 512))
			part.Close()
			if err != nil {
				return nil, err
			}
			src.subdir = strings.TrimSpace(string(value))
			continue
		}
		part.Close()
	}
}

func (m *multipartSource) Next() (upload.Incoming, bool, error) {
	if m.pending != nil {
		part := m.pending
		m.pending = nil
		return upload.Incoming{Filename: part.FileName(), Body: part}, true, nil
	}
	if m.done {
		return upload.Incoming{}, false, nil
	}
	for {
		part, err := m.reader.NextPart()
		if errors.Is(err, io.EOF) {
			m.done = true
			return upload.Incoming{}, false, nil
		}
		if err != nil {
			return upload.Incoming{}, false, err
		}
		if part.FileName() == "" {
			part.Close()
			continue
		}
		return upload.Incoming{Filename: part.FileName(), Body: part}, true, nil
	}
}

/* ---------------------------- URL imports ---------------------------- */

// importFetcher builds the guarded client an import runs through.
//
// Unlike the metadata lookups, importing is never "off": the user typed the
// URL, which is the consent the metadata provider switch stands in for. What
// the stored settings still decide is whether private and loopback addresses
// are reachable, because that is a property of the network the server is on
// and not of the request.
func (a *API) importFetcher() *remote.Fetcher {
	return remote.New(true, a.settings.Get().Metadata.AllowPrivate)
}

// createImport handles POST /libraries/{id}/import.
func (a *API) createImport(w http.ResponseWriter, r *http.Request) {
	id := requireUploader(w, r)
	if id == nil {
		return
	}
	lib := a.libraryForWrite(w, r, id)
	if lib == nil {
		return
	}
	var body struct {
		URL string `json:"url"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	raw := strings.TrimSpace(body.URL)
	if raw == "" {
		writeError(w, http.StatusBadRequest, codeBadRequest, "a url is required")
		return
	}
	if !a.uploadLimiter.Allow(uploadKey(id.User.ID)) {
		writeError(w, http.StatusTooManyRequests, codeRateLimited, "too many imports; try again in a moment")
		return
	}
	// The obvious refusals happen now, while there is somebody to tell. A
	// hostname that only resolves to a private address is caught later, at
	// dial time, by the same guard.
	if err := a.importFetcher().CheckURL(raw); err != nil {
		switch {
		case errors.Is(err, remote.ErrScheme):
			writeError(w, http.StatusBadRequest, codeBadRequest, "only http and https URLs can be imported")
		case errors.Is(err, remote.ErrBlocked):
			writeError(w, http.StatusBadRequest, codeBadRequest, "that address is not allowed")
		default:
			writeError(w, http.StatusBadRequest, codeBadRequest, "that is not a usable URL")
		}
		return
	}

	job, err := a.jobs.Create(r.Context(), id.User.ID, lib.ID, raw)
	if errors.Is(err, importer.ErrQueueFull) {
		writeError(w, http.StatusTooManyRequests, codeRateLimited, err.Error())
		return
	}
	if err != nil {
		fail(w, err, "create import")
		return
	}
	a.importWorker.Wake()
	log.Info().Int64("job", job.ID).Int64("library", lib.ID).Str("user", id.User.Username).
		Msg("import queued")
	writeJSON(w, http.StatusAccepted, job)
}

// listImports handles GET /me/imports. An administrator sees every account's
// jobs, which is what makes the queue diagnosable when somebody reports that
// their import never finished.
func (a *API) listImports(w http.ResponseWriter, r *http.Request) {
	id := requireUser(w, r)
	if id == nil {
		return
	}
	owner := id.User.ID
	if id.User.IsAdmin() {
		owner = 0
	}
	jobs, err := a.jobs.List(r.Context(), owner, queryInt(r, "limit", 50))
	if err != nil {
		fail(w, err, "imports")
		return
	}
	writeJSON(w, http.StatusOK, listBody[importer.Job]{Items: jobs, Total: len(jobs)})
}

// visibleJob loads a job the caller is allowed to see, or writes the refusal.
func (a *API) visibleJob(w http.ResponseWriter, r *http.Request) *importer.Job {
	id := requireUser(w, r)
	if id == nil {
		return nil
	}
	jobID, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, codeBadRequest, "import id must be a positive integer")
		return nil
	}
	job, err := a.jobs.Get(r.Context(), jobID)
	if err != nil {
		fail(w, err, "import")
		return nil
	}
	// Somebody else's job is answered as missing rather than forbidden: the
	// existence of a job is itself information about what they are reading.
	if job.UserID != id.User.ID && !id.User.IsAdmin() {
		writeError(w, http.StatusNotFound, codeNotFound, "not found")
		return nil
	}
	return job
}

// getImport handles GET /imports/{id}.
func (a *API) getImport(w http.ResponseWriter, r *http.Request) {
	job := a.visibleJob(w, r)
	if job == nil {
		return
	}
	writeJSON(w, http.StatusOK, job)
}

// deleteImport handles DELETE /imports/{id}: cancel a queued or running job,
// or clear a finished one off the list.
func (a *API) deleteImport(w http.ResponseWriter, r *http.Request) {
	job := a.visibleJob(w, r)
	if job == nil {
		return
	}
	if err := a.jobs.Delete(r.Context(), job.ID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, codeNotFound, "not found")
			return
		}
		fail(w, err, "cancel import")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

// itoa64 avoids importing strconv for one call in the rate-limit key.
func itoa64(v int64) string {
	if v == 0 {
		return "0"
	}
	var digits []byte
	for v > 0 {
		digits = append([]byte{byte('0' + v%10)}, digits...)
		v /= 10
	}
	return string(digits)
}
