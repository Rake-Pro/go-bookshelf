// Package upload accepts book files from a client and files them into a
// library directory.
//
// Everything a browser sends is untrusted, and unlike the rest of the server
// this path writes to the media tree, so the order of operations is the whole
// design: the bytes are streamed into a hidden staging directory inside the
// library root, checked there against a size cap, an extension allowlist, the
// format's magic bytes and finally the real parser, and only then renamed into
// place. Nothing the caller sends is ever used as an on-disk name - the name is
// derived from the metadata the parser read out of the file itself - and a file
// that fails any check leaves the library exactly as it was.
package upload

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rake-pro/go-bookshelf/internal/audio"
	"github.com/rake-pro/go-bookshelf/internal/epub"
	"github.com/rake-pro/go-bookshelf/internal/library"
	"github.com/rs/zerolog/log"
)

// Size caps, per file. An EPUB that large is already extraordinary; the
// audiobook cap is the whole point of the streaming write, since a 2 GiB M4B
// must never be held in memory.
const (
	MaxEbookBytes = 200 << 20 // 200 MiB
	MaxAudioBytes = 2 << 30   // 2 GiB
	MaxFiles      = 50        // files in one request
)

// stagingDir is where an upload lives while it is being checked. The name
// starts with a dot, which is what keeps it invisible to the scanner: the walk
// skips dot-directories, so a half-written or rejected file is never a
// candidate for ingest.
const stagingDir = ".gbs-incoming"

// stagingTTL bounds how long an abandoned staging file survives a crash.
const stagingTTL = 24 * time.Hour

// Errors the API maps onto status codes.
var (
	ErrNoFiles      = errors.New("upload: no files in the request")
	ErrTooManyFiles = fmt.Errorf("upload: at most %d files at a time", MaxFiles)
	ErrExtension    = errors.New("upload: this library does not accept that kind of file")
	ErrTooLarge     = errors.New("upload: the file is larger than the limit")
	ErrEmpty        = errors.New("upload: the file is empty")
	ErrParse        = errors.New("upload: the file could not be read as a book")
	ErrSubdir       = errors.New("upload: the folder name is not usable")
	ErrNoPath       = errors.New("upload: this library has no directory to write to")
	ErrNotWritable  = errors.New("upload: this library's directory is not writable by the server")
)

// DuplicateError reports that the exact bytes are already in the catalog. It
// carries the existing item so the client can link to the book the user
// already has instead of making them go and look.
type DuplicateError struct {
	SHA1   string
	ItemID int64
	Title  string
}

func (e *DuplicateError) Error() string {
	return fmt.Sprintf("upload: this file is already in the library as item %d", e.ItemID)
}

// Limits are the size caps a Service enforces. They are a field rather than
// constants so a test can prove the cap with a small file instead of writing a
// real 2 GiB one.
type Limits struct {
	MaxEbookBytes int64
	MaxAudioBytes int64
	MaxFiles      int
}

// DefaultLimits are the shipped caps.
func DefaultLimits() Limits {
	return Limits{MaxEbookBytes: MaxEbookBytes, MaxAudioBytes: MaxAudioBytes, MaxFiles: MaxFiles}
}

// Service files uploads into libraries.
type Service struct {
	cat     *library.Catalog
	scanner *library.Scanner
	limits  Limits
}

// New builds a Service over the catalog and the scanner that will ingest what
// it writes.
func New(cat *library.Catalog, scanner *library.Scanner) *Service {
	return &Service{cat: cat, scanner: scanner, limits: DefaultLimits()}
}

// SetLimits replaces the size caps.
func (s *Service) SetLimits(l Limits) {
	if l.MaxEbookBytes > 0 {
		s.limits.MaxEbookBytes = l.MaxEbookBytes
	}
	if l.MaxAudioBytes > 0 {
		s.limits.MaxAudioBytes = l.MaxAudioBytes
	}
	if l.MaxFiles > 0 {
		s.limits.MaxFiles = l.MaxFiles
	}
}

// Incoming is one file as it arrives. Filename is the client's own, used only
// to read an extension off and as the last-resort label for a chapter; it
// never reaches the filesystem.
type Incoming struct {
	Filename string
	Body     io.Reader
}

// Source yields the files of one request, one at a time.
//
// It is an iterator rather than a slice because the HTTP handler reads
// straight off the multipart stream: a request carrying four chapters of an
// audiobook is never buffered, in memory or anywhere else, and each part is
// consumed completely before the next one is asked for.
type Source interface {
	Next() (Incoming, bool, error)
}

// Files is a Source over a slice, for callers that already hold the bytes.
func Files(files ...Incoming) Source { return &sliceSource{files: files} }

type sliceSource struct {
	files []Incoming
	i     int
}

func (s *sliceSource) Next() (Incoming, bool, error) {
	if s.i >= len(s.files) {
		return Incoming{}, false, nil
	}
	s.i++
	return s.files[s.i-1], true, nil
}

// Accepted is one file that passed every check and is now in the library.
type Accepted struct {
	// Filename is the derived on-disk name, relative to the library root.
	Filename string `json:"filename"`
	Kind     string `json:"kind"`
	Title    string `json:"title"`
	Author   string `json:"author"`
	Size     int64  `json:"size_bytes"`
	// ItemID is filled in once a scan has ingested the file. It is 0 while the
	// scan is still running.
	ItemID int64 `json:"item_id"`

	path      string
	sourceKey string
	sha1      string
}

// AcceptedExtensions lists what a library of the given kind takes, lowercase
// and dotted, for the client's file picker and for the error message.
func AcceptedExtensions(kind string) []string {
	switch kind {
	case library.KindEbook:
		return []string{".epub"}
	case library.KindAudiobook:
		return []string{".m4b", ".m4a", ".mp3"}
	case library.KindMixed:
		return []string{".epub", ".m4b", ".m4a", ".mp3"}
	}
	return nil
}

// formatFor maps an extension onto the format and item kind it implies, or
// reports that this library does not take it. `.mp4` is deliberately absent:
// the scanner reads it, but accepting an upload of it invites a video file.
func formatFor(libraryKind, ext string) (format, itemKind string, ok bool) {
	for _, allowed := range AcceptedExtensions(libraryKind) {
		if allowed != ext {
			continue
		}
		switch ext {
		case ".epub":
			return FormatEPUB, library.KindEbook, true
		case ".mp3":
			return FormatMP3, library.KindAudiobook, true
		case ".m4a", ".m4b":
			return FormatMP4, library.KindAudiobook, true
		}
	}
	return "", "", false
}

// staged is one upload sitting in the staging directory, already parsed.
type staged struct {
	tmp    string
	ext    string
	kind   string
	size   int64
	sha1   string
	title  string
	author string
	album  string
	track  int
	disc   int
	stem   string
}

// Accept streams every file into the library, in this order: stage, validate,
// deduplicate, name, rename into place. It returns what was filed, or an error
// and a library that has not been touched.
func (s *Service) Accept(ctx context.Context, lib *library.Library, subdir string, src Source) ([]Accepted, error) {
	folder, err := SafeSubdir(subdir)
	if err != nil {
		return nil, err
	}
	root, err := writableRoot(lib)
	if err != nil {
		return nil, err
	}
	staging := filepath.Join(root, stagingDir)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotWritable, err)
	}
	purgeStale(staging)

	var all []staged
	// Any failure below discards every file in the request: a half-accepted
	// batch of an audiobook's chapters would be worse than none of it.
	defer func() {
		for _, st := range all {
			if st.tmp != "" {
				os.Remove(st.tmp)
			}
		}
	}()

	seen := map[string]bool{}
	for {
		in, ok, err := src.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		if len(all) >= s.limits.MaxFiles {
			return nil, ErrTooManyFiles
		}
		st, err := s.stage(ctx, lib.Kind, staging, in)
		if err != nil {
			return nil, err
		}
		all = append(all, st)
		if seen[st.sha1] {
			return nil, fmt.Errorf("upload: %q was sent twice in the same request", SafeName(st.stem))
		}
		seen[st.sha1] = true
		if err := s.checkDuplicate(ctx, st.sha1); err != nil {
			return nil, err
		}
	}
	if len(all) == 0 {
		return nil, ErrNoFiles
	}

	dest := root
	if folder != "" {
		dest = filepath.Join(root, folder)
		if err := os.MkdirAll(dest, 0o755); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrNotWritable, err)
		}
	}

	out, err := s.place(dest, root, all)
	if err != nil {
		return nil, err
	}
	// The files are in the library now; the deferred cleanup must not chase
	// the temporary names they no longer have.
	for i := range all {
		all[i].tmp = ""
	}
	return out, nil
}

// writableRoot picks the directory an upload is written to. A library may have
// several paths; the first is the one that receives new books, because "where
// does a new book go" needs one answer and the operator chose the order.
func writableRoot(lib *library.Library) (string, error) {
	if lib == nil || len(lib.Paths) == 0 {
		return "", ErrNoPath
	}
	root, err := filepath.Abs(lib.Paths[0])
	if err != nil {
		return "", ErrNoPath
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("%w: %s", ErrNoPath, root)
	}
	return root, nil
}

// stage streams one file into the staging directory and validates it there.
func (s *Service) stage(ctx context.Context, libraryKind, staging string, in Incoming) (staged, error) {
	ext := strings.ToLower(filepath.Ext(in.Filename))
	format, itemKind, ok := formatFor(libraryKind, ext)
	if !ok {
		return staged{}, fmt.Errorf("%w: %s takes %s", ErrExtension,
			libraryKind, strings.Join(AcceptedExtensions(libraryKind), ", "))
	}
	max := s.limits.MaxEbookBytes
	if itemKind == library.KindAudiobook {
		max = s.limits.MaxAudioBytes
	}

	f, err := os.CreateTemp(staging, "upload-*"+ext)
	if err != nil {
		return staged{}, fmt.Errorf("%w: %v", ErrNotWritable, err)
	}
	st := staged{tmp: f.Name(), ext: ext, kind: itemKind, stem: stemOf(in.Filename)}

	// Reading one byte past the cap is what tells an exactly-at-the-limit file
	// from one that would have kept going.
	h := sha1.New()
	written, copyErr := io.Copy(io.MultiWriter(f, h), io.LimitReader(in.Body, max+1))
	syncErr := f.Sync()
	closeErr := f.Close()
	switch {
	case copyErr != nil:
		os.Remove(st.tmp)
		return staged{}, fmt.Errorf("upload: reading %q failed: %w", SafeName(st.stem), copyErr)
	case syncErr != nil || closeErr != nil:
		os.Remove(st.tmp)
		return staged{}, fmt.Errorf("%w: %v", ErrNotWritable, errors.Join(syncErr, closeErr))
	case written == 0:
		os.Remove(st.tmp)
		return staged{}, ErrEmpty
	case written > max:
		os.Remove(st.tmp)
		return staged{}, fmt.Errorf("%w: %s is over %d bytes", ErrTooLarge, SafeName(st.stem), max)
	}
	st.size = written
	st.sha1 = hex.EncodeToString(h.Sum(nil))

	head, err := readHead(st.tmp)
	if err != nil {
		os.Remove(st.tmp)
		return staged{}, err
	}
	if err := magicOK(format, head); err != nil {
		os.Remove(st.tmp)
		return staged{}, err
	}
	if err := s.parse(&st, format); err != nil {
		os.Remove(st.tmp)
		return staged{}, err
	}
	_ = ctx
	return st, nil
}

// parse runs the real reader over a staged file. This is the check that a
// crafted archive has to get past, and it is the same code path the scanner
// uses, so an upload that is accepted here is one the scanner can ingest -
// with the same entry-count, entry-size and total-size limits applied.
func (s *Service) parse(st *staged, format string) error {
	switch format {
	case FormatEPUB:
		if err := checkEPUBContainer(st.tmp, st.stem); err != nil {
			return err
		}
		r, err := epub.Open(st.tmp)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrParse, err)
		}
		defer r.Close()
		book, err := r.Book()
		if err != nil {
			return fmt.Errorf("%w: %v", ErrParse, err)
		}
		st.title = book.Meta.Title
		for _, p := range book.Meta.People {
			if p.Role == library.RoleAuthor {
				st.author = p.Name
				break
			}
		}
	case FormatMP3, FormatMP4:
		meta, err := audio.Probe(st.tmp)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrParse, err)
		}
		if meta.DurationMS <= 0 {
			return fmt.Errorf("%w: no audio in the file", ErrParse)
		}
		st.title, st.album, st.track, st.disc = meta.Title, meta.Album, meta.Track, meta.Disc
		st.author = meta.AlbumArtist
		if st.author == "" {
			st.author = meta.Artist
		}
	}
	return nil
}

// checkDuplicate refuses bytes the catalog already has. Content, not name: the
// same book saved twice under two names is one book.
func (s *Service) checkDuplicate(ctx context.Context, sum string) error {
	var (
		itemID int64
		title  string
	)
	err := s.cat.DB().QueryRowContext(ctx,
		`SELECT f.item_id, i.title FROM files f JOIN items i ON i.id = f.item_id WHERE f.sha1 = ?`, sum).
		Scan(&itemID, &title)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return err
	}
	return &DuplicateError{SHA1: sum, ItemID: itemID, Title: title}
}

// place renames the staged files into the library under derived names.
//
// Ebooks become one file each. Audio files are grouped into one directory per
// book, keyed on the album and author their tags carry, so uploading the eight
// chapters of one audiobook produces one item rather than eight - which is
// what the scanner's directory rule would otherwise decide by accident.
func (s *Service) place(dest, root string, all []staged) ([]Accepted, error) {
	var (
		out    []Accepted
		groups = map[string][]int{}
		order  []string
	)
	for i, st := range all {
		if st.kind == library.KindEbook {
			acc, err := s.placeEbook(dest, root, st)
			if err != nil {
				return nil, err
			}
			out = append(out, acc)
			continue
		}
		key := groupKey(st)
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], i)
	}

	for _, key := range order {
		idx := groups[key]
		sort.SliceStable(idx, func(a, b int) bool {
			x, y := all[idx[a]], all[idx[b]]
			if x.disc != y.disc {
				return x.disc < y.disc
			}
			if x.track != y.track {
				return x.track < y.track
			}
			return x.stem < y.stem
		})
		acc, err := s.placeAudiobook(dest, root, all, idx)
		if err != nil {
			return nil, err
		}
		out = append(out, acc)
	}
	return out, nil
}

func (s *Service) placeEbook(dest, root string, st staged) (Accepted, error) {
	base := BookBase(st.author, st.title, st.stem)
	name, err := uniqueName(dest, base, ".epub")
	if err != nil {
		return Accepted{}, err
	}
	final := filepath.Join(dest, name)
	if err := moveInto(st.tmp, final); err != nil {
		return Accepted{}, err
	}
	log.Info().Str("file", name).Int64("bytes", st.size).Msg("upload filed as an ebook")
	return Accepted{
		Filename: relativeTo(root, final), Kind: library.KindEbook,
		Title: st.title, Author: st.author, Size: st.size,
		path: final, sourceKey: final, sha1: st.sha1,
	}, nil
}

func (s *Service) placeAudiobook(dest, root string, all []staged, idx []int) (Accepted, error) {
	first := all[idx[0]]
	title := first.album
	if title == "" {
		title = first.title
	}
	base := BookBase(first.author, title, first.stem)
	dirName, err := uniqueName(dest, base, "")
	if err != nil {
		return Accepted{}, err
	}
	bookDir := filepath.Join(dest, dirName)
	if err := os.MkdirAll(bookDir, 0o755); err != nil {
		return Accepted{}, fmt.Errorf("%w: %v", ErrNotWritable, err)
	}

	var total int64
	for n, i := range idx {
		st := all[i]
		label := st.title
		if label == "" {
			label = st.stem
		}
		name := TrackName(n+1, len(idx), label, st.ext)
		if err := moveInto(st.tmp, filepath.Join(bookDir, name)); err != nil {
			return Accepted{}, err
		}
		total += st.size
	}
	log.Info().Str("directory", dirName).Int("files", len(idx)).Int64("bytes", total).
		Msg("upload filed as an audiobook")
	return Accepted{
		Filename: relativeTo(root, bookDir), Kind: library.KindAudiobook,
		Title: title, Author: first.author, Size: total,
		path: bookDir, sourceKey: bookDir, sha1: first.sha1,
	}, nil
}

// groupKey decides which uploaded audio files are chapters of one book. Files
// with no album tag at all are never grouped with each other: without a title
// to agree on, "same book" would just mean "uploaded together".
func groupKey(st staged) string {
	if st.album == "" {
		return "solo:" + st.tmp
	}
	return strings.ToLower(st.album) + "\x00" + strings.ToLower(st.author)
}

// moveInto renames a staged file into its final place and flushes the
// directory entry, so a power loss leaves either the old directory or the new
// file and never a name pointing at nothing. Both sides are in the same
// library root, so the rename is atomic even on NFS.
func moveInto(tmp, final string) error {
	if err := os.Rename(tmp, final); err != nil {
		return fmt.Errorf("%w: %v", ErrNotWritable, err)
	}
	if err := os.Chmod(final, 0o644); err != nil {
		log.Debug().Err(err).Str("path", final).Msg("could not relax the uploaded file's mode")
	}
	syncDir(filepath.Dir(final))
	return nil
}

func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	defer d.Close()
	_ = d.Sync()
}

// ScanAndResolve rescans the library the upload landed in and fills in the ids
// of the items it produced.
//
// The scan runs on a context detached from the request, so a client that gives
// up mid-scan does not leave the catalog half-updated. The caller waits only
// as long as it is willing to hold the response open; when the wait runs out
// the scan carries on and the client is told the ids are not ready yet.
func (s *Service) ScanAndResolve(ctx context.Context, libraryID int64, accepted []Accepted, wait time.Duration) ([]Accepted, bool) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := s.scanner.ScanLibrary(context.WithoutCancel(ctx), libraryID); err != nil {
			log.Error().Err(err).Int64("library", libraryID).Msg("post-upload scan failed")
		}
	}()

	select {
	case <-done:
	case <-time.After(wait):
		return accepted, false
	}

	for i := range accepted {
		var id int64
		err := s.cat.DB().QueryRowContext(ctx,
			`SELECT id FROM items WHERE library_id = ? AND source_key = ?`,
			libraryID, accepted[i].sourceKey).Scan(&id)
		if err != nil {
			log.Warn().Err(err).Str("source", accepted[i].Filename).
				Msg("an uploaded file was not ingested by the scan that followed it")
			continue
		}
		accepted[i].ItemID = id
	}
	return accepted, true
}

// readHead returns the leading bytes of a file for the magic-byte checks.
func readHead(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, sniffWindow)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, err
	}
	return buf[:n], nil
}

// stemOf is the client's filename without its extension, kept only as a label
// of last resort for a file whose tags say nothing.
func stemOf(name string) string {
	name = filepath.Base(filepath.FromSlash(strings.ReplaceAll(name, `\`, "/")))
	return strings.TrimSuffix(name, filepath.Ext(name))
}

func relativeTo(root, path string) string {
	if rel, err := filepath.Rel(root, path); err == nil {
		return filepath.ToSlash(rel)
	}
	return filepath.Base(path)
}

// purgeStale removes staging files a previous run left behind.
func purgeStale(staging string) {
	entries, err := os.ReadDir(staging)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-stagingTTL)
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(staging, e.Name())); err == nil {
			log.Info().Str("file", e.Name()).Msg("removed an abandoned upload")
		}
	}
}
