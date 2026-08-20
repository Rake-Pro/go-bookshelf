// Package fixtures generates synthetic library files - EPUB containers, MPEG
// audio streams and MP4 audiobooks - for the test suite. Nothing here is
// linked into the server binary; it exists so tests can build valid input
// bytes on the fly instead of carrying sample media in the repository.
package fixtures

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
)

// PNG returns a solid-colour PNG of the given size.
func PNG(w, h int, c color.Color) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// JPEG returns a solid-colour JPEG of the given size.
func JPEG(w, h int, c color.Color) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// EPUBOptions describes the book to generate.
type EPUBOptions struct {
	Title       string
	Subtitle    string
	Authors     []string
	Narrator    string
	Language    string
	Description string
	Publisher   string
	Date        string
	ISBN        string
	Series      string
	SeriesIndex float64
	Tags        []string
	// Chapters maps a spine href (relative to the OPF, which lives in OEBPS/)
	// to the XHTML body of that chapter. Order is not preserved; use
	// ChapterOrder for a deterministic spine.
	Chapters      map[string]string
	ChapterOrder  []string
	Cover         []byte // PNG bytes; omitted when nil
	ExtraEntries  map[string][]byte
	OmitContainer bool
}

// EPUBBytes builds a complete EPUB container in memory.
func EPUBBytes(o EPUBOptions) ([]byte, error) {
	if o.Title == "" {
		o.Title = "Untitled"
	}
	if o.Language == "" {
		o.Language = "en"
	}
	if len(o.Chapters) == 0 {
		o.Chapters = map[string]string{"chapter1.xhtml": "<p>Hello.</p>"}
		o.ChapterOrder = []string{"chapter1.xhtml"}
	}
	if len(o.ChapterOrder) == 0 {
		for href := range o.Chapters {
			o.ChapterOrder = append(o.ChapterOrder, href)
		}
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	add := func(name string, body []byte) error {
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = w.Write(body)
		return err
	}

	// The mimetype entry has to be the archive's first, stored uncompressed:
	// that is what lets a reader identify an EPUB from its leading bytes, and
	// the upload path checks for it.
	mimetype, err := zw.CreateHeader(&zip.FileHeader{Name: "mimetype", Method: zip.Store})
	if err != nil {
		return nil, err
	}
	if _, err := mimetype.Write([]byte("application/epub+zip")); err != nil {
		return nil, err
	}
	if !o.OmitContainer {
		container := `<?xml version="1.0" encoding="UTF-8"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles>
</container>`
		if err := add("META-INF/container.xml", []byte(container)); err != nil {
			return nil, err
		}
	}

	var manifest, spine strings.Builder
	for i, href := range o.ChapterOrder {
		id := fmt.Sprintf("ch%d", i+1)
		fmt.Fprintf(&manifest, `    <item id="%s" href="%s" media-type="application/xhtml+xml"/>`+"\n", id, href)
		fmt.Fprintf(&spine, `    <itemref idref="%s"/>`+"\n", id)
		body := o.Chapters[href]
		doc := `<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml"><head><title>` + xmlEscape(href) + `</title></head><body>` + body + `</body></html>`
		if err := add("OEBPS/"+href, []byte(doc)); err != nil {
			return nil, err
		}
	}
	if o.Cover != nil {
		manifest.WriteString(`    <item id="cover-img" href="cover.png" media-type="image/png" properties="cover-image"/>` + "\n")
		if err := add("OEBPS/cover.png", o.Cover); err != nil {
			return nil, err
		}
	}
	for name, body := range o.ExtraEntries {
		if err := add(name, body); err != nil {
			return nil, err
		}
	}

	var meta strings.Builder
	fmt.Fprintf(&meta, `    <dc:title id="maintitle">%s</dc:title>`+"\n", xmlEscape(o.Title))
	if o.Subtitle != "" {
		fmt.Fprintf(&meta, `    <dc:title id="sub">%s</dc:title>`+"\n", xmlEscape(o.Subtitle))
		meta.WriteString(`    <meta refines="#sub" property="title-type">subtitle</meta>` + "\n")
	}
	for i, a := range o.Authors {
		fmt.Fprintf(&meta, `    <dc:creator id="au%d" opf:role="aut">%s</dc:creator>`+"\n", i+1, xmlEscape(a))
	}
	if o.Narrator != "" {
		fmt.Fprintf(&meta, `    <dc:contributor opf:role="nrt">%s</dc:contributor>`+"\n", xmlEscape(o.Narrator))
	}
	fmt.Fprintf(&meta, `    <dc:language>%s</dc:language>`+"\n", xmlEscape(o.Language))
	if o.Description != "" {
		fmt.Fprintf(&meta, `    <dc:description>%s</dc:description>`+"\n", xmlEscape(o.Description))
	}
	if o.Publisher != "" {
		fmt.Fprintf(&meta, `    <dc:publisher>%s</dc:publisher>`+"\n", xmlEscape(o.Publisher))
	}
	if o.Date != "" {
		fmt.Fprintf(&meta, `    <dc:date>%s</dc:date>`+"\n", xmlEscape(o.Date))
	}
	if o.ISBN != "" {
		fmt.Fprintf(&meta, `    <dc:identifier opf:scheme="ISBN">%s</dc:identifier>`+"\n", xmlEscape(o.ISBN))
	}
	for _, t := range o.Tags {
		fmt.Fprintf(&meta, `    <dc:subject>%s</dc:subject>`+"\n", xmlEscape(t))
	}
	if o.Series != "" {
		fmt.Fprintf(&meta, `    <meta name="calibre:series" content="%s"/>`+"\n", xmlEscape(o.Series))
		fmt.Fprintf(&meta, `    <meta name="calibre:series_index" content="%g"/>`+"\n", o.SeriesIndex)
	}
	if o.Cover != nil {
		meta.WriteString(`    <meta name="cover" content="cover-img"/>` + "\n")
	}

	opf := `<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" xmlns:opf="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="pub-id">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
` + meta.String() + `  </metadata>
  <manifest>
` + manifest.String() + `  </manifest>
  <spine>
` + spine.String() + `  </spine>
</package>`
	if err := add("OEBPS/content.opf", []byte(opf)); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// WriteEPUB writes a generated EPUB to path, creating parent directories.
func WriteEPUB(path string, o EPUBOptions) error {
	b, err := EPUBBytes(o)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// ZipWithEntries builds a zip whose entry names are taken verbatim, for
// exercising the archive safety checks.
func ZipWithEntries(entries map[string][]byte) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range entries {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(body); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		case '\'':
			b.WriteString("&apos;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func be32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

func be64(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}
