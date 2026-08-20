package importer

import (
	"archive/zip"
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"path"
	"strings"
	"time"
)

// Book is what a Site produces: a work, its chapters, and the images those
// chapters reference. It is the only thing an adapter has to build, and
// BuildEPUB turns it into a container the rest of the server already knows how
// to read.
type Book struct {
	Title       string
	Author      string
	Language    string
	Description string
	// Source is the URL the book was built from, recorded in the package
	// document so a reader can see where a book came from.
	Source   string
	Chapters []Chapter
	Images   []Image
}

// Chapter is one chapter's sanitized XHTML body - flow content only, no
// <html> or <body> wrapper, no scripts, no styles.
type Chapter struct {
	Title string
	XHTML string
}

// Image is one embedded illustration.
type Image struct {
	// Name is the file name inside the container, without a directory.
	Name        string
	ContentType string
	Data        []byte
}

// minimalCSS is the whole stylesheet. A generated book should look like the
// reader's settings say it should, so this sets only what the markup needs and
// leaves type, colour and measure to the reading system.
const minimalCSS = `html { font-size: 100%; }
body { margin: 0 5%; line-height: 1.5; }
h1, h2, h3 { line-height: 1.25; margin: 1.5em 0 0.5em; }
p { margin: 0 0 1em; text-indent: 0; }
img { max-width: 100%; height: auto; }
blockquote { margin: 1em 2em; }
figure { margin: 1.5em 0; }
figcaption { font-size: 0.9em; }
`

// BuildEPUB packages a Book as a valid EPUB 3 container.
//
// The mimetype entry is written first and stored uncompressed, which is what
// the specification requires and what lets any reader identify the file from
// its first bytes; everything after it is deflated.
func BuildEPUB(b *Book) ([]byte, error) {
	if b == nil || len(b.Chapters) == 0 {
		return nil, fmt.Errorf("importer: nothing to build a book from")
	}
	title := strings.TrimSpace(b.Title)
	if title == "" {
		title = "Untitled"
	}
	lang := strings.TrimSpace(b.Language)
	if lang == "" {
		lang = "en"
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	if w, err := zw.CreateHeader(&zip.FileHeader{Name: "mimetype", Method: zip.Store}); err != nil {
		return nil, err
	} else if _, err := w.Write([]byte("application/epub+zip")); err != nil {
		return nil, err
	}

	add := func(name string, body []byte) error {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
		if err != nil {
			return err
		}
		_, err = w.Write(body)
		return err
	}

	container := `<?xml version="1.0" encoding="UTF-8"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>
`
	if err := add("META-INF/container.xml", []byte(container)); err != nil {
		return nil, err
	}
	if err := add("OEBPS/style.css", []byte(minimalCSS)); err != nil {
		return nil, err
	}

	type manifestItem struct{ id, href, mediaType, properties string }
	items := []manifestItem{
		{"nav", "nav.xhtml", "application/xhtml+xml", "nav"},
		{"css", "style.css", "text/css", ""},
	}
	var spine []string

	for i, ch := range b.Chapters {
		id := fmt.Sprintf("ch%04d", i+1)
		href := fmt.Sprintf("text/%s.xhtml", id)
		title := strings.TrimSpace(ch.Title)
		if title == "" {
			title = fmt.Sprintf("Chapter %d", i+1)
		}
		doc := chapterDocument(title, ch.XHTML, lang)
		if err := add("OEBPS/"+href, []byte(doc)); err != nil {
			return nil, err
		}
		items = append(items, manifestItem{id, href, "application/xhtml+xml", ""})
		spine = append(spine, id)
	}

	for i, img := range b.Images {
		href := "images/" + img.Name
		if err := add("OEBPS/"+href, img.Data); err != nil {
			return nil, err
		}
		items = append(items, manifestItem{fmt.Sprintf("img%04d", i+1), href, img.ContentType, ""})
	}

	// nav.xhtml is the EPUB 3 table of contents, and is also a readable page
	// in its own right, which is why it carries a heading.
	var nav strings.Builder
	nav.WriteString(`    <nav epub:type="toc" id="toc"><h1>Contents</h1>` + "\n      <ol>\n")
	for i, ch := range b.Chapters {
		title := strings.TrimSpace(ch.Title)
		if title == "" {
			title = fmt.Sprintf("Chapter %d", i+1)
		}
		fmt.Fprintf(&nav, `        <li><a href="text/ch%04d.xhtml">%s</a></li>`+"\n", i+1, escapeXML(title))
	}
	nav.WriteString("      </ol>\n    </nav>\n")
	navDoc := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops" xml:lang="` +
		escapeXML(lang) + `" lang="` + escapeXML(lang) + `">
  <head><title>Contents</title><meta charset="utf-8"/><link rel="stylesheet" type="text/css" href="style.css"/></head>
  <body>
` + nav.String() + `  </body>
</html>
`
	if err := add("OEBPS/nav.xhtml", []byte(navDoc)); err != nil {
		return nil, err
	}

	var manifest, spineXML strings.Builder
	for _, it := range items {
		props := ""
		if it.properties != "" {
			props = fmt.Sprintf(` properties="%s"`, it.properties)
		}
		fmt.Fprintf(&manifest, `    <item id="%s" href="%s" media-type="%s"%s/>`+"\n",
			it.id, escapeXML(it.href), escapeXML(it.mediaType), props)
	}
	for _, id := range spine {
		fmt.Fprintf(&spineXML, `    <itemref idref="%s"/>`+"\n", id)
	}

	var meta strings.Builder
	fmt.Fprintf(&meta, `    <dc:identifier id="pub-id">%s</dc:identifier>`+"\n", bookID(b))
	fmt.Fprintf(&meta, `    <dc:title>%s</dc:title>`+"\n", escapeXML(title))
	fmt.Fprintf(&meta, `    <dc:language>%s</dc:language>`+"\n", escapeXML(lang))
	if author := strings.TrimSpace(b.Author); author != "" {
		fmt.Fprintf(&meta, `    <dc:creator id="creator">%s</dc:creator>`+"\n", escapeXML(author))
		meta.WriteString(`    <meta refines="#creator" property="role" scheme="marc:relators">aut</meta>` + "\n")
	}
	if d := strings.TrimSpace(b.Description); d != "" {
		fmt.Fprintf(&meta, `    <dc:description>%s</dc:description>`+"\n", escapeXML(truncate(d, 2000)))
	}
	if s := strings.TrimSpace(b.Source); s != "" {
		fmt.Fprintf(&meta, `    <dc:source>%s</dc:source>`+"\n", escapeXML(s))
	}
	fmt.Fprintf(&meta, `    <meta property="dcterms:modified">%s</meta>`+"\n",
		time.Now().UTC().Format("2006-01-02T15:04:05Z"))

	opf := `<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="pub-id" xml:lang="` +
		escapeXML(lang) + `">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
` + meta.String() + `  </metadata>
  <manifest>
` + manifest.String() + `  </manifest>
  <spine>
` + spineXML.String() + `  </spine>
</package>
`
	if err := add("OEBPS/content.opf", []byte(opf)); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// chapterDocument wraps a sanitized body in a complete XHTML document.
func chapterDocument(title, body, lang string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml" xml:lang="` + escapeXML(lang) + `" lang="` + escapeXML(lang) + `">
  <head>
    <meta charset="utf-8"/>
    <title>` + escapeXML(title) + `</title>
    <link rel="stylesheet" type="text/css" href="../style.css"/>
  </head>
  <body>
    <h1>` + escapeXML(title) + `</h1>
` + body + `
  </body>
</html>
`
}

// bookID is a stable identifier derived from the source URL and title, so
// re-importing the same work produces the same identifier rather than a new
// book every time.
func bookID(b *Book) string {
	sum := sha1.Sum([]byte(b.Source + "\x00" + b.Title + "\x00" + b.Author))
	h := hex.EncodeToString(sum[:])
	return fmt.Sprintf("urn:uuid:%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32])
}

// imageName derives the in-container filename for an embedded image from its
// URL and media type. The extension follows the type the server actually sent,
// not the one in the URL.
func imageName(index int, srcURL, contentType string) string {
	ext := ".jpg"
	switch {
	case strings.Contains(contentType, "png"):
		ext = ".png"
	case strings.Contains(contentType, "gif"):
		ext = ".gif"
	case strings.Contains(contentType, "webp"):
		ext = ".webp"
	case strings.Contains(contentType, "svg"):
		// SVG is a document format that can carry script, so it is never
		// embedded; the caller drops these before reaching here.
		ext = ".svg"
	default:
		if e := strings.ToLower(path.Ext(strings.SplitN(srcURL, "?", 2)[0])); e == ".png" || e == ".gif" || e == ".webp" {
			ext = e
		}
	}
	return fmt.Sprintf("img%04d%s", index+1, ext)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n])) + "..."
}
