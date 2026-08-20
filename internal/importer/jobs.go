package importer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rake-pro/go-bookshelf/internal/library"
	"github.com/rake-pro/go-bookshelf/internal/remote"
	"github.com/rake-pro/go-bookshelf/internal/store"
	"github.com/rake-pro/go-bookshelf/internal/upload"
	"github.com/rs/zerolog/log"
)

// Job statuses. There is no "cancelled": a cancel deletes the row, which is
// also how the worker learns about it, so there is exactly one place the
// answer to "is this job still wanted" lives.
const (
	StatusQueued  = "queued"
	StatusRunning = "running"
	StatusDone    = "done"
	StatusFailed  = "failed"
)

// MaxPendingPerUser bounds how many imports one account may have waiting. The
// worker runs one job at a time for the whole server, so a queue is a shared
// resource and one user must not be able to fill it.
const MaxPendingPerUser = 20

// ErrQueueFull is returned when a user already has MaxPendingPerUser waiting.
var ErrQueueFull = fmt.Errorf("importer: you already have %d imports waiting", MaxPendingPerUser)

// Job is one URL import, as the API returns it.
type Job struct {
	ID        int64  `json:"id"`
	UserID    int64  `json:"user_id"`
	LibraryID int64  `json:"library_id"`
	URL       string `json:"url"`
	Status    string `json:"status"`
	Message   string `json:"message"`
	ItemID    int64  `json:"item_id"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// Jobs is the import queue.
type Jobs struct{ db *store.DB }

// NewJobs wraps a database handle.
func NewJobs(db *store.DB) *Jobs { return &Jobs{db: db} }

const jobColumns = `id, user_id, library_id, url, status, message, coalesce(item_id, 0),
	created_at, updated_at`

func scanJob(row interface{ Scan(...any) error }) (*Job, error) {
	var j Job
	if err := row.Scan(&j.ID, &j.UserID, &j.LibraryID, &j.URL, &j.Status, &j.Message,
		&j.ItemID, &j.CreatedAt, &j.UpdatedAt); err != nil {
		return nil, err
	}
	return &j, nil
}

// Create queues an import.
func (j *Jobs) Create(ctx context.Context, userID, libraryID int64, rawURL string) (*Job, error) {
	pending, err := j.pendingFor(ctx, userID)
	if err != nil {
		return nil, err
	}
	if pending >= MaxPendingPerUser {
		return nil, ErrQueueFull
	}
	now := store.Now()
	id, err := j.db.InsertReturningID(ctx,
		`INSERT INTO import_jobs (user_id, library_id, url, status, message, created_at, updated_at)
		 VALUES (?, ?, ?, ?, '', ?, ?)`,
		userID, libraryID, strings.TrimSpace(rawURL), StatusQueued, now, now)
	if err != nil {
		return nil, err
	}
	return j.Get(ctx, id)
}

func (j *Jobs) pendingFor(ctx context.Context, userID int64) (int, error) {
	var n int
	err := j.db.QueryRowContext(ctx,
		`SELECT count(*) FROM import_jobs WHERE user_id = ? AND status IN ('queued', 'running')`,
		userID).Scan(&n)
	return n, err
}

// Get returns one job, or store.ErrNotFound.
func (j *Jobs) Get(ctx context.Context, id int64) (*Job, error) {
	job, err := scanJob(j.db.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM import_jobs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	return job, err
}

// List returns a user's jobs, newest first. A userID of 0 returns everybody's,
// which is what an administrator sees.
func (j *Jobs) List(ctx context.Context, userID int64, limit int) ([]Job, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := `SELECT ` + jobColumns + ` FROM import_jobs WHERE user_id = ? ORDER BY id DESC LIMIT ?`
	args := []any{userID, limit}
	if userID == 0 {
		query = `SELECT ` + jobColumns + ` FROM import_jobs ORDER BY id DESC LIMIT ?`
		args = []any{limit}
	}
	rows, err := j.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Job{}
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *job)
	}
	return out, rows.Err()
}

// Delete removes a job. For a queued job that is a cancel; for a running one
// the worker notices the row is gone and stops.
func (j *Jobs) Delete(ctx context.Context, id int64) error {
	res, err := j.db.ExecContext(ctx, `DELETE FROM import_jobs WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return store.ErrNotFound
	}
	return nil
}

// exists reports whether a job row is still there.
func (j *Jobs) exists(ctx context.Context, id int64) bool {
	var n int
	if err := j.db.QueryRowContext(ctx, `SELECT count(*) FROM import_jobs WHERE id = ?`, id).Scan(&n); err != nil {
		return true // a database blip is not a cancellation
	}
	return n > 0
}

// claim takes the oldest queued job and marks it running, if there is one.
func (j *Jobs) claim(ctx context.Context) (*Job, error) {
	var id int64
	err := j.db.QueryRowContext(ctx,
		`SELECT id FROM import_jobs WHERE status = ? ORDER BY id LIMIT 1`, StatusQueued).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// The status is part of the WHERE clause, so two workers racing for the
	// same row leave exactly one of them holding it.
	res, err := j.db.ExecContext(ctx,
		`UPDATE import_jobs SET status = ?, updated_at = ? WHERE id = ? AND status = ?`,
		StatusRunning, store.Now(), id, StatusQueued)
	if err != nil {
		return nil, err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return nil, nil
	}
	return j.Get(ctx, id)
}

// finish records the outcome. A job whose row has been deleted stays deleted:
// the update simply matches nothing.
func (j *Jobs) finish(ctx context.Context, id int64, status, message string, itemID int64) {
	var item any
	if itemID > 0 {
		item = itemID
	}
	if _, err := j.db.ExecContext(ctx,
		`UPDATE import_jobs SET status = ?, message = ?, item_id = ?, updated_at = ? WHERE id = ?`,
		status, truncate(message, 500), item, store.Now(), id); err != nil {
		log.Error().Err(err).Int64("job", id).Msg("recording the import outcome failed")
	}
}

// Requeue puts jobs left running by a previous process back in the queue. A
// job is idempotent as far as the library is concerned - a book that made it
// in is caught by the duplicate check on the retry - so re-running one is
// safer than leaving it stuck.
func (j *Jobs) Requeue(ctx context.Context) error {
	res, err := j.db.ExecContext(ctx,
		`UPDATE import_jobs SET status = ?, updated_at = ? WHERE status = ?`,
		StatusQueued, store.Now(), StatusRunning)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n > 0 {
		log.Info().Int64("jobs", n).Msg("requeued imports interrupted by a restart")
	}
	return nil
}

// Worker runs queued imports, one at a time.
//
// One at a time is a deliberate limit rather than an implementation shortcut:
// an import is a crawl of somebody else's site, and running several at once
// would turn a polite one-request-a-second walk into a burst from the same
// address.
type Worker struct {
	jobs    *Jobs
	cat     *library.Catalog
	up      *upload.Service
	fetcher func() *remote.Fetcher
	wake    chan struct{}
	poll    time.Duration
}

// NewWorker builds the queue worker. fetcher is called per job so that a
// change to the metadata settings takes effect on the next import rather than
// the next restart.
func NewWorker(jobs *Jobs, cat *library.Catalog, up *upload.Service, fetcher func() *remote.Fetcher) *Worker {
	return &Worker{jobs: jobs, cat: cat, up: up, fetcher: fetcher, wake: make(chan struct{}, 1), poll: 10 * time.Second}
}

// Wake asks the worker to look for work now instead of at the next poll.
func (w *Worker) Wake() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Run processes the queue until ctx is done.
func (w *Worker) Run(ctx context.Context) {
	if err := w.jobs.Requeue(ctx); err != nil {
		log.Error().Err(err).Msg("requeueing interrupted imports failed")
	}
	for {
		for {
			job, err := w.jobs.claim(ctx)
			if err != nil {
				log.Error().Err(err).Msg("claiming an import job failed")
				break
			}
			if job == nil {
				break
			}
			w.run(ctx, job)
		}
		select {
		case <-ctx.Done():
			return
		case <-w.wake:
		case <-time.After(w.poll):
		}
	}
}

// run performs one job and records its outcome.
func (w *Worker) run(ctx context.Context, job *Job) {
	log.Info().Int64("job", job.ID).Int64("library", job.LibraryID).Msg("import started")

	// A cancel deletes the row; a watcher turns that into a cancelled context
	// so a long chapter walk stops within a couple of seconds rather than at
	// the end.
	jobCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go w.watchCancel(jobCtx, job.ID, cancel)

	lib, err := w.cat.LibraryByID(jobCtx, job.LibraryID)
	if err != nil {
		w.jobs.finish(ctx, job.ID, StatusFailed, "the library no longer exists", 0)
		return
	}

	accepted, err := New(w.up, w.fetcher()).Import(jobCtx, lib, job.URL)
	switch {
	case errors.Is(err, context.Canceled) && !w.jobs.exists(ctx, job.ID):
		log.Info().Int64("job", job.ID).Msg("import cancelled")
		return
	case err != nil:
		log.Warn().Err(err).Int64("job", job.ID).Msg("import failed")
		w.jobs.finish(ctx, job.ID, StatusFailed, userMessage(err), 0)
		return
	case len(accepted) == 0:
		w.jobs.finish(ctx, job.ID, StatusFailed, "nothing was imported", 0)
		return
	}

	// The scan runs on the parent context: the book is already on disk, so
	// cancelling now would only leave the catalog behind the filesystem.
	resolved, _ := w.up.ScanAndResolve(ctx, job.LibraryID, accepted, 2*time.Minute)
	w.jobs.finish(ctx, job.ID, StatusDone, "Added "+resolved[0].Title, resolved[0].ItemID)
	log.Info().Int64("job", job.ID).Int64("item", resolved[0].ItemID).Msg("import finished")
}

// watchCancel cancels the job's context once its row disappears.
func (w *Worker) watchCancel(ctx context.Context, id int64, cancel context.CancelFunc) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !w.jobs.exists(ctx, id) {
				cancel()
				return
			}
		}
	}
}

// userMessage turns an internal error into something worth showing in a job
// list, without handing back an address the guard refused to connect to.
func userMessage(err error) string {
	switch {
	case errors.Is(err, remote.ErrBlocked):
		return "that address is not allowed"
	case errors.Is(err, remote.ErrScheme):
		return "only http and https URLs can be imported"
	case errors.Is(err, remote.ErrTooLarge):
		return "the file is larger than the limit"
	case errors.Is(err, ErrNoContent):
		return "no readable text was found on that page"
	case errors.Is(err, ErrUnsupported):
		return "that URL is neither a book file nor a readable page"
	case errors.Is(err, context.DeadlineExceeded):
		return "the import took too long and was stopped"
	}
	var dup *upload.DuplicateError
	if errors.As(err, &dup) {
		return "that book is already in the library"
	}
	for _, known := range []error{upload.ErrExtension, upload.ErrTooLarge, upload.ErrParse,
		upload.ErrEmpty, upload.ErrNoPath, upload.ErrNotWritable} {
		if errors.Is(err, known) {
			return strings.TrimPrefix(err.Error(), "upload: ")
		}
	}
	return "the import failed"
}
