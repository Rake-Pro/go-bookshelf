// Package importer adds a book to a library from a URL.
//
// A URL is one of two things. It is either a book file - an EPUB or an
// audiobook - in which case the bytes are streamed straight into the upload
// validation path, which is the same code an uploaded file goes through and
// the only code allowed to write into a library. Or it is a web page, in which
// case the page and the chapters that follow it are extracted, sanitized and
// built into an EPUB, which is then handed to that same path.
//
// Which of the two it is comes from the bytes, never from the URL's extension
// or the server's Content-Type: both are chosen by the other end, and this
// package is by definition pointed at somewhere the operator does not control.
//
// Every outbound request goes through internal/remote, so the SSRF guard - the
// scheme check, the post-DNS address check, and the redirect check that re-runs
// both per hop - applies to the page, to every chapter after it and to every
// image inside them.
package importer

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/rake-pro/go-bookshelf/internal/library"
	"github.com/rake-pro/go-bookshelf/internal/remote"
	"github.com/rake-pro/go-bookshelf/internal/upload"
	"github.com/rs/zerolog/log"
)

// Limits on what one import may pull in.
const (
	// MaxChapters bounds a chapter walk. A serial with more parts than this is
	// not something a "next" link should be trusted to have counted correctly.
	MaxChapters = 2000
	// MaxPageBytes bounds one HTML page.
	MaxPageBytes = 8 << 20
	// MaxImageBytes bounds one embedded image, and MaxImages the whole book.
	MaxImageBytes = 8 << 20
	MaxImages     = 300
	// PoliteDelay is the pause between requests to the same site. A chapter
	// walk is a crawl of somebody else's server, and one request a second is
	// the least it is owed.
	PoliteDelay = time.Second
	// MaxTotalTime bounds one import end to end.
	MaxTotalTime = 30 * time.Minute
)

// Errors the job worker reports back to the user.
var (
	ErrUnsupported = errors.New("importer: that URL is neither a book file nor a readable page")
	ErrNoContent   = errors.New("importer: no readable text was found on that page")
)

// Site turns a URL into a book. The generic web-story extractor implements it
// and is the fallback; a per-site adapter that knows a particular publisher's
// markup registers itself and is preferred for the URLs it matches, without
// the rest of the pipeline changing at all.
type Site interface {
	// Match reports whether this adapter handles the URL.
	Match(u *url.URL) bool
	// Book fetches the work and everything it links on to.
	Book(ctx context.Context, u *url.URL) (*Book, error)
}

// SiteFactory builds an adapter bound to the guarded HTTP client it must use.
// Adapters get a fetcher rather than making their own, because the guard is
// not optional and an adapter that could opt out of it would be a hole.
type SiteFactory func(*remote.Fetcher) Site

var (
	sitesMu sync.RWMutex
	sites   []SiteFactory
)

// RegisterSite adds a per-site adapter. Adapters are tried in registration
// order and the generic extractor answers whatever none of them claims.
func RegisterSite(f SiteFactory) {
	sitesMu.Lock()
	defer sitesMu.Unlock()
	sites = append(sites, f)
}

// siteFor picks the adapter for a URL, falling back to the generic extractor.
func siteFor(u *url.URL, fetch *remote.Fetcher) Site {
	sitesMu.RLock()
	defer sitesMu.RUnlock()
	for _, factory := range sites {
		s := factory(fetch)
		if s.Match(u) {
			return s
		}
	}
	return &webStory{fetch: fetch}
}

// Importer runs one import at a time on behalf of the job worker.
type Importer struct {
	up    *upload.Service
	fetch *remote.Fetcher
}

// New builds an Importer over an upload service and a guarded fetcher.
func New(up *upload.Service, fetch *remote.Fetcher) *Importer {
	return &Importer{up: up, fetch: fetch}
}

// Import fetches raw and files whatever it turns out to be into lib.
func (im *Importer) Import(ctx context.Context, lib *library.Library, raw string) ([]upload.Accepted, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("importer: %q is not a URL", raw)
	}
	if err := im.fetch.CheckURL(u.String()); err != nil {
		return nil, err
	}

	body, contentType, err := im.fetch.Open(ctx, u.String(), remote.MaxDownloadSize)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	// Peeking rather than reading: a book file is streamed on from exactly
	// where the sniff stopped, so a 2 GiB audiobook is never in memory.
	buffered := bufio.NewReaderSize(body, 64<<10)
	head, err := buffered.Peek(64 << 10)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, bufio.ErrBufferFull) {
		return nil, fmt.Errorf("importer: reading %s failed: %w", u.Host, err)
	}

	if format := upload.Sniff(head); format != "" {
		name := "download" + upload.ExtensionFor(format)
		log.Info().Str("host", u.Host).Str("format", format).Msg("import: the URL is a book file")
		return im.up.Accept(ctx, lib, "", upload.Files(upload.Incoming{Filename: name, Body: buffered}))
	}

	if !looksLikeHTML(head, contentType) {
		return nil, fmt.Errorf("%w: %s", ErrUnsupported, describeType(contentType))
	}
	// The page is read again by the extractor rather than passed down: the
	// site adapter owns fetching, so a per-site adapter can start somewhere
	// else entirely (a table of contents, an API) given the same URL.
	body.Close()

	deadline, cancel := context.WithTimeout(ctx, MaxTotalTime)
	defer cancel()

	book, err := siteFor(u, im.fetch).Book(deadline, u)
	if err != nil {
		return nil, err
	}
	epubBytes, err := BuildEPUB(book)
	if err != nil {
		return nil, err
	}
	log.Info().Str("title", book.Title).Int("chapters", len(book.Chapters)).
		Int("images", len(book.Images)).Int("bytes", len(epubBytes)).Msg("import: built a book from a web story")

	// The generated book goes through the identical validation an uploaded
	// file does. Building it here is no reason to trust it: if this package
	// ever emits something the reader cannot open, that is caught now rather
	// than by a user whose library has a broken book in it.
	return im.up.Accept(ctx, lib, "", upload.Files(upload.Incoming{
		Filename: upload.SafeName(book.Title) + ".epub",
		Body:     bytes.NewReader(epubBytes),
	}))
}

// looksLikeHTML decides whether to hand a body to the page extractor. The
// bytes come first; the declared type only breaks a tie.
func looksLikeHTML(head []byte, contentType string) bool {
	trimmed := bytes.TrimLeft(head, " \t\r\n")
	trimmed = bytes.TrimPrefix(trimmed, []byte{0xEF, 0xBB, 0xBF})
	lower := bytes.ToLower(trimmed)
	for _, prefix := range [][]byte{[]byte("<!doctype html"), []byte("<html"), []byte("<?xml")} {
		if bytes.HasPrefix(lower, prefix) {
			return true
		}
	}
	ct := strings.ToLower(contentType)
	if strings.Contains(ct, "text/html") || strings.Contains(ct, "application/xhtml") {
		return len(trimmed) > 0 && trimmed[0] == '<'
	}
	return false
}

func describeType(contentType string) string {
	if ct := strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]); ct != "" {
		return ct
	}
	return "an unrecognised file"
}

/* ---------------------------- the generic extractor ---------------------- */

// webStory is the fallback Site: it reads any page well enough to make a book
// out of it, and follows "next chapter" links to collect a serial.
type webStory struct{ fetch *remote.Fetcher }

// Match claims every URL. It is only ever consulted after the registered
// adapters have declined.
func (w *webStory) Match(*url.URL) bool { return true }

func (w *webStory) Book(ctx context.Context, start *url.URL) (*Book, error) {
	book := &Book{Source: start.String()}
	visited := map[string]bool{start.String(): true}
	images := map[string]int{} // absolute URL -> index in book.Images

	current := start
	for i := 0; i < MaxChapters; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if i > 0 {
			// Politeness applies between requests, not before the first.
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(PoliteDelay):
			}
		}

		page, _, err := w.fetch.Get(ctx, current.String())
		if err != nil {
			if i == 0 {
				return nil, err
			}
			// A serial that ends in a dead link is still a book; keep what was
			// collected and say so.
			log.Warn().Err(err).Str("url", current.String()).Msg("import: stopping the chapter walk")
			break
		}
		if len(page) > MaxPageBytes {
			page = page[:MaxPageBytes]
		}
		doc, err := parseHTML(page)
		if err != nil {
			if i == 0 {
				return nil, err
			}
			break
		}

		meta := readMeta(doc)
		if i == 0 {
			book.Title = meta.Title
			book.Author = meta.Author
			book.Language = meta.Language
			book.Description = meta.Description
		}

		content := findContent(doc)
		if content == nil {
			if i == 0 {
				return nil, ErrNoContent
			}
			log.Warn().Str("url", current.String()).Msg("import: a chapter had no readable content")
			break
		}
		title := chapterTitle(content, meta, i)
		clean := sanitize(content, current, title)
		body := w.embedImages(ctx, clean, book, images)
		book.Chapters = append(book.Chapters, Chapter{Title: title, XHTML: body})

		next := nextLink(doc, current, chapterNumberOf(title)+1)
		if next == "" || visited[next] {
			break
		}
		nextURL, err := url.Parse(next)
		if err != nil {
			break
		}
		visited[next] = true
		current = nextURL
	}

	if len(book.Chapters) == 0 {
		return nil, ErrNoContent
	}
	// A single-page import is titled by the page; a serial is titled by its
	// first chapter's page, which is usually the work rather than the part.
	if book.Title == "" {
		book.Title = book.Chapters[0].Title
	}
	return book, nil
}

// embedImages fetches the images a chapter referenced and rewrites the
// placeholders to point at them. An image that cannot be fetched, is too big,
// or is not actually an image is dropped and its placeholder with it, so a
// chapter never ends up referencing a file that is not in the container.
func (w *webStory) embedImages(ctx context.Context, clean sanitized, book *Book, index map[string]int) string {
	body := clean.XHTML
	for i, src := range clean.Images {
		placeholder := imagePlaceholder(i)
		name, ok := w.imageFor(ctx, src, book, index)
		if !ok {
			body = dropImage(body, placeholder)
			continue
		}
		body = strings.ReplaceAll(body, placeholder, "../images/"+name)
	}
	return body
}

func (w *webStory) imageFor(ctx context.Context, src string, book *Book, index map[string]int) (string, bool) {
	if i, ok := index[src]; ok {
		return book.Images[i].Name, true
	}
	if len(book.Images) >= MaxImages {
		return "", false
	}
	data, contentType, err := w.fetch.Get(ctx, src)
	if err != nil || len(data) == 0 || len(data) > MaxImageBytes {
		return "", false
	}
	kind := sniffImage(data)
	if kind == "" {
		// Whatever it is, it is not one of the four raster formats a reading
		// system is required to render. SVG is excluded on purpose: it is a
		// document that can carry script.
		log.Debug().Str("url", src).Str("declared", contentType).Msg("import: skipped a non-image")
		return "", false
	}
	name := imageName(len(book.Images), src, kind)
	book.Images = append(book.Images, Image{Name: name, ContentType: kind, Data: data})
	index[src] = len(book.Images) - 1
	return name, true
}

// dropImage removes the <img> element whose src is the given placeholder.
func dropImage(body, placeholder string) string {
	for {
		i := strings.Index(body, placeholder)
		if i < 0 {
			return body
		}
		start := strings.LastIndex(body[:i], "<img")
		end := strings.Index(body[i:], ">")
		if start < 0 || end < 0 {
			// Should not happen; leave the body alone rather than cutting it
			// at a guess.
			return strings.ReplaceAll(body, placeholder, "")
		}
		body = body[:start] + body[i+end+1:]
	}
}

// sniffImage returns the media type of an image from its magic bytes, or "".
func sniffImage(b []byte) string {
	switch {
	case len(b) > 8 && bytes.HasPrefix(b, []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}):
		return "image/png"
	case len(b) > 3 && bytes.HasPrefix(b, []byte{0xFF, 0xD8, 0xFF}):
		return "image/jpeg"
	case len(b) > 6 && (bytes.HasPrefix(b, []byte("GIF87a")) || bytes.HasPrefix(b, []byte("GIF89a"))):
		return "image/gif"
	case len(b) > 12 && bytes.HasPrefix(b, []byte("RIFF")) && bytes.Equal(b[8:12], []byte("WEBP")):
		return "image/webp"
	}
	return ""
}
