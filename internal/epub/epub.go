// Package epub reads EPUB containers: metadata from the OPF package document,
// the spine, and the cover image, plus safe access to the resources inside the
// archive.
//
// Everything an EPUB supplies is untrusted input. The reader enforces hard
// limits on entry count and uncompressed size, refuses absolute paths, parent
// traversal and symlinks, and resolves every resource request against the
// archive's own name table rather than the filesystem.
package epub

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"mime"
	"path"
	"strings"
)

// Limits bounds what the reader will accept from an archive.
type Limits struct {
	MaxEntries   int   // number of zip entries
	MaxEntrySize int64 // uncompressed size of a single entry
	MaxTotalSize int64 // uncompressed size of the whole archive
}

// DefaultLimits are the limits applied when none are given.
var DefaultLimits = Limits{
	MaxEntries:   10000,
	MaxEntrySize: 256 << 20, // 256 MiB
	MaxTotalSize: 2 << 30,   // 2 GiB
}

// Errors returned for archives that violate the safety rules.
var (
	ErrTooManyEntries = errors.New("epub: too many archive entries")
	ErrEntryTooLarge  = errors.New("epub: archive entry exceeds size limit")
	ErrArchiveTooBig  = errors.New("epub: archive exceeds total size limit")
	ErrUnsafePath     = errors.New("epub: unsafe entry path")
	ErrNotFound       = errors.New("epub: resource not found")
	ErrNoRootfile     = errors.New("epub: no rootfile in META-INF/container.xml")
)

const containerPath = "META-INF/container.xml"

// Reader is an open EPUB container.
type Reader struct {
	rc      *zip.ReadCloser
	r       *zip.Reader
	files   map[string]*zip.File
	limits  Limits
	opfPath string
	book    *Book
}

// Open opens the EPUB at path with the default limits.
func Open(path string) (*Reader, error) { return OpenLimits(path, DefaultLimits) }

// OpenLimits opens the EPUB at path with explicit limits.
func OpenLimits(name string, limits Limits) (*Reader, error) {
	rc, err := zip.OpenReader(name)
	if err != nil {
		return nil, fmt.Errorf("epub: open %s: %w", name, err)
	}
	r, err := newReader(&rc.Reader, limits)
	if err != nil {
		rc.Close()
		return nil, err
	}
	r.rc = rc
	return r, nil
}

// NewReader wraps an already-open zip reader, for callers holding the archive
// in memory.
func NewReader(zr *zip.Reader, limits Limits) (*Reader, error) { return newReader(zr, limits) }

func newReader(zr *zip.Reader, limits Limits) (*Reader, error) {
	if limits.MaxEntries <= 0 {
		limits = DefaultLimits
	}
	if len(zr.File) > limits.MaxEntries {
		return nil, fmt.Errorf("%w: %d > %d", ErrTooManyEntries, len(zr.File), limits.MaxEntries)
	}

	files := make(map[string]*zip.File, len(zr.File))
	var total int64
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		// Symlinks would let an entry point outside the archive once written
		// to disk, and have no meaning for an EPUB resource.
		if f.Mode()&0o120000 == 0o120000 {
			return nil, fmt.Errorf("%w: symlink %q", ErrUnsafePath, f.Name)
		}
		clean, err := safeEntryName(f.Name)
		if err != nil {
			return nil, err
		}
		size := int64(f.UncompressedSize64)
		if size > limits.MaxEntrySize {
			return nil, fmt.Errorf("%w: %q is %d bytes", ErrEntryTooLarge, f.Name, size)
		}
		total += size
		if total > limits.MaxTotalSize {
			return nil, fmt.Errorf("%w: %d bytes", ErrArchiveTooBig, total)
		}
		files[clean] = f
	}

	return &Reader{r: zr, files: files, limits: limits}, nil
}

// safeEntryName normalises a zip entry name and rejects anything that could
// escape the archive root.
func safeEntryName(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("%w: empty name", ErrUnsafePath)
	}
	// Windows-style separators and drive letters are normalised away before
	// the traversal check so "..\\.." cannot slip through.
	n := strings.ReplaceAll(name, "\\", "/")
	if strings.HasPrefix(n, "/") {
		return "", fmt.Errorf("%w: absolute path %q", ErrUnsafePath, name)
	}
	if len(n) >= 2 && n[1] == ':' {
		return "", fmt.Errorf("%w: drive-qualified path %q", ErrUnsafePath, name)
	}
	if strings.Contains(n, "\x00") {
		return "", fmt.Errorf("%w: NUL in path %q", ErrUnsafePath, name)
	}
	clean := path.Clean(n)
	if clean == ".." || strings.HasPrefix(clean, "../") || clean == "." || strings.HasPrefix(clean, "/") {
		return "", fmt.Errorf("%w: %q escapes the archive root", ErrUnsafePath, name)
	}
	return clean, nil
}

// Close releases the underlying file, if the reader owns one.
func (r *Reader) Close() error {
	if r.rc != nil {
		return r.rc.Close()
	}
	return nil
}

// Names returns every regular entry path in the archive.
func (r *Reader) Names() []string {
	out := make([]string, 0, len(r.files))
	for n := range r.files {
		out = append(out, n)
	}
	return out
}

// ReadFile returns the contents of an archive entry. name is resolved against
// the archive root and may not escape it.
func (r *Reader) ReadFile(name string) ([]byte, error) {
	clean, err := safeEntryName(name)
	if err != nil {
		return nil, err
	}
	f, ok := r.files[clean]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, name)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	// LimitReader guards against a header that understates the real size.
	limit := r.limits.MaxEntrySize
	b, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%w: %q", ErrEntryTooLarge, clean)
	}
	return b, nil
}

// Size returns the uncompressed size of an archive entry, resolved the same
// way ReadFile resolves it.
func (r *Reader) Size(name string) (int64, bool) {
	clean, err := safeEntryName(name)
	if err != nil {
		return 0, false
	}
	f, ok := r.files[clean]
	if !ok {
		return 0, false
	}
	return int64(f.UncompressedSize64), true
}

// ResolveRoot turns a request path that is relative to the container root into
// an archive entry name, or reports ErrUnsafePath / ErrNotFound. This is the
// address space the HTTP resource route and the EPUB renderer both use:
// "META-INF/container.xml", "OEBPS/chapter1.xhtml".
func (r *Reader) ResolveRoot(rel string) (string, error) {
	clean, err := safeEntryName(rel)
	if err != nil {
		return "", err
	}
	if _, ok := r.files[clean]; !ok {
		return "", fmt.Errorf("%w: %q", ErrNotFound, rel)
	}
	return clean, nil
}

// Resolve turns a request path that is relative to the OPF document into an
// archive-root-relative entry name, or reports ErrUnsafePath / ErrNotFound.
// OPF-relative addressing is what the package document itself uses for
// manifest hrefs and the cover; clients address resources with ResolveRoot.
func (r *Reader) Resolve(rel string) (string, error) {
	if _, err := r.Book(); err != nil {
		return "", err
	}
	base := path.Dir(r.opfPath)
	if base == "." {
		base = ""
	}
	joined := rel
	if base != "" {
		joined = base + "/" + rel
	}
	clean, err := safeEntryName(joined)
	if err != nil {
		return "", err
	}
	// Re-check against the container root: a rel of "../../etc/passwd" cleans
	// to something outside base even when the join still looks relative.
	if base != "" && clean != base && !strings.HasPrefix(clean, base+"/") {
		return "", fmt.Errorf("%w: %q escapes the package directory", ErrUnsafePath, rel)
	}
	if _, ok := r.files[clean]; !ok {
		return "", fmt.Errorf("%w: %q", ErrNotFound, rel)
	}
	return clean, nil
}

// ContentType guesses a MIME type for an archive entry from its extension,
// falling back to application/octet-stream.
func ContentType(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".xhtml", ".html", ".htm":
		return "application/xhtml+xml; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".ncx":
		return "application/x-dtbncx+xml"
	case ".opf":
		return "application/oebps-package+xml"
	case ".otf":
		return "font/otf"
	case ".ttf":
		return "font/ttf"
	case ".woff":
		return "font/woff"
	case ".woff2":
		return "font/woff2"
	}
	if ct := mime.TypeByExtension(strings.ToLower(path.Ext(name))); ct != "" {
		return ct
	}
	return "application/octet-stream"
}
