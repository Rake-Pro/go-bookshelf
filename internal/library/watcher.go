package library

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/rs/zerolog/log"
)

// DefaultDebounce is how long the watcher waits for a burst of filesystem
// events to settle before rescanning. Copying a large audiobook in produces
// hundreds of events; one scan afterwards is enough.
const DefaultDebounce = 5 * time.Second

// Watcher rescans libraries when their files change.
type Watcher struct {
	cat      *Catalog
	scanner  *Scanner
	debounce time.Duration

	mu      sync.Mutex
	dirty   map[int64]bool
	watched map[string]int64
	fsw     *fsnotify.Watcher
}

// NewWatcher builds a watcher; call Start to begin observing.
func NewWatcher(cat *Catalog, scanner *Scanner, debounce time.Duration) *Watcher {
	if debounce <= 0 {
		debounce = DefaultDebounce
	}
	return &Watcher{
		cat: cat, scanner: scanner, debounce: debounce,
		dirty: map[int64]bool{}, watched: map[string]int64{},
	}
}

// Start begins watching every library path and returns once the watcher is
// running. It stops when ctx is cancelled.
func (w *Watcher) Start(ctx context.Context) error {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	w.fsw = fsw

	if err := w.Refresh(ctx); err != nil {
		fsw.Close()
		return err
	}

	go w.loop(ctx)
	return nil
}

// Refresh re-reads the library paths and adjusts the watch set.
func (w *Watcher) Refresh(ctx context.Context) error {
	libs, err := w.cat.Libraries(ctx, nil)
	if err != nil {
		return err
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	want := map[string]int64{}
	for _, lib := range libs {
		for _, root := range lib.Paths {
			abs, err := filepath.Abs(root)
			if err != nil {
				continue
			}
			// Watch every directory: fsnotify does not recurse on its own.
			_ = filepath.WalkDir(abs, func(path string, d fs.DirEntry, err error) error {
				if err != nil || !d.IsDir() {
					return nil
				}
				if d.Type()&fs.ModeSymlink != 0 {
					return fs.SkipDir
				}
				if path != abs && strings.HasPrefix(d.Name(), ".") {
					return fs.SkipDir
				}
				want[path] = lib.ID
				return nil
			})
		}
	}

	for path := range w.watched {
		if _, ok := want[path]; !ok {
			_ = w.fsw.Remove(path)
			delete(w.watched, path)
		}
	}
	for path, libID := range want {
		if _, ok := w.watched[path]; ok {
			continue
		}
		if err := w.fsw.Add(path); err != nil {
			log.Warn().Err(err).Str("path", path).Msg("cannot watch directory")
			continue
		}
		w.watched[path] = libID
	}
	log.Info().Int("directories", len(w.watched)).Msg("library watcher active")
	return nil
}

func (w *Watcher) loop(ctx context.Context) {
	defer w.fsw.Close()

	timer := time.NewTimer(w.debounce)
	if !timer.Stop() {
		<-timer.C
	}
	pending := false

	for {
		select {
		case <-ctx.Done():
			return

		case event, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			libID := w.libraryFor(event.Name)
			if libID == 0 {
				continue
			}
			// A new directory has to be added to the watch set itself.
			if event.Op&fsnotify.Create != 0 {
				if info, err := os.Lstat(event.Name); err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
					if err := w.fsw.Add(event.Name); err == nil {
						w.mu.Lock()
						w.watched[event.Name] = libID
						w.mu.Unlock()
					}
				}
			}
			w.mu.Lock()
			w.dirty[libID] = true
			w.mu.Unlock()

			if pending && !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(w.debounce)
			pending = true

		case err, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
			log.Warn().Err(err).Msg("filesystem watcher error")

		case <-timer.C:
			pending = false
			w.mu.Lock()
			ids := make([]int64, 0, len(w.dirty))
			for id := range w.dirty {
				ids = append(ids, id)
			}
			w.dirty = map[int64]bool{}
			w.mu.Unlock()

			for _, id := range ids {
				if ctx.Err() != nil {
					return
				}
				if _, err := w.scanner.ScanLibrary(ctx, id); err != nil {
					log.Error().Err(err).Int64("library", id).Msg("watch-triggered scan failed")
				}
			}
			if err := w.Refresh(ctx); err != nil && ctx.Err() == nil {
				log.Warn().Err(err).Msg("refreshing watch set failed")
			}
		}
	}
}

// libraryFor finds the library owning the closest watched ancestor of path.
func (w *Watcher) libraryFor(path string) int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	dir := path
	for i := 0; i < 64; i++ {
		if id, ok := w.watched[dir]; ok {
			return id
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return 0
		}
		dir = parent
	}
	return 0
}
