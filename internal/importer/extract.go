package importer

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// The readability-style scorer below is deliberately small. It is not trying to
// be a browser: it is trying to answer one question - which subtree of this
// page is the story - well enough that the resulting EPUB reads like a book,
// and to fail visibly rather than subtly when it cannot.

var (
	// Class and id fragments that suggest a node is, or is not, the story.
	positivePattern = regexp.MustCompile(`(?i)article|body|content|entry|hentry|main|page|post|story|chapter|text|prose|blog`)
	negativePattern = regexp.MustCompile(`(?i)comment|contact|foot|masthead|meta|outbrain|promo|related|scroll|shoutbox|sidebar|sponsor|shopping|tags|tool|widget|share|social|nav|menu|banner|advert|\bads?\b|ad-|-ad|popup|newsletter|subscribe|cookie|paywall|breadcrumb|pagination|disqus|tracking|analytics|recommend|byline|author-bio|hidden|modal|overlay|skip`)

	// Anchor text that means "the next part of this work".
	nextTextPattern = regexp.MustCompile(`(?i)^(next|next\s*(chapter|part|page|section)?|continue( reading)?|older|forward|>>?|›|»|→)$`)
	chapterNumberRe = regexp.MustCompile(`(?i)^(?:chapter|part|ch\.?|episode|ep\.?)\s*0*(\d{1,5})\b`)
)

// tagsDropped are removed with everything inside them. Scripts and styles are
// the obvious ones; the rest are page furniture that would otherwise be read
// as prose, and forms, which have no meaning in a book.
var tagsDropped = map[string]bool{
	"script": true, "style": true, "noscript": true, "iframe": true, "frame": true,
	"frameset": true, "object": true, "embed": true, "applet": true, "canvas": true,
	"form": true, "input": true, "button": true, "select": true, "textarea": true,
	"label": true, "fieldset": true, "legend": true, "nav": true, "footer": true,
	"header": true, "aside": true, "menu": true, "dialog": true, "template": true,
	"svg": true, "math": true, "video": true, "audio": true, "map": true, "area": true,
	"link": true, "meta": true, "base": true, "title": true,
}

// tagsKept survive sanitising with their name intact. Everything else that is
// not dropped outright is unwrapped: its children are kept, its own element is
// not, which is what turns a nest of layout <div>s into flat prose.
var tagsKept = map[string]bool{
	"p": true, "br": true, "hr": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"em": true, "strong": true, "i": true, "b": true, "u": true, "s": true,
	"small": true, "sub": true, "sup": true, "code": true, "pre": true, "kbd": true,
	"blockquote": true, "q": true, "cite": true, "abbr": true, "span": true, "div": true,
	"ul": true, "ol": true, "li": true, "dl": true, "dt": true, "dd": true,
	"figure": true, "figcaption": true, "img": true,
	"table": true, "thead": true, "tbody": true, "tfoot": true, "tr": true, "td": true, "th": true,
}

// attrsKept is the whole attribute allowlist. Nothing that can carry script
// (every on* handler), style (which can load a remote URL) or an identity
// (class, id, data-*) survives, so the book cannot phone home and cannot be
// fingerprinted by whatever the page wanted to fingerprint.
var attrsKept = map[string]map[string]bool{
	"img":  {"src": true, "alt": true},
	"td":   {"colspan": true, "rowspan": true},
	"th":   {"colspan": true, "rowspan": true, "scope": true},
	"ol":   {"start": true},
	"abbr": {"title": true},
}

// voidTags must be written self-closing, because the output is XHTML inside an
// EPUB and an unclosed <br> makes the whole chapter unparseable.
var voidTags = map[string]bool{"br": true, "hr": true, "img": true, "wbr": true}

// pageMeta is what a page says about itself.
type pageMeta struct {
	Title       string
	Author      string
	Language    string
	Description string
}

// parseHTML parses a fetched page. golang.org/x/net/html is the same parser
// the standard library's template escaping is built around: it never fails, it
// recovers the way a browser does, so malformed markup yields a tree rather
// than an error.
func parseHTML(body []byte) (*html.Node, error) {
	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("importer: parsing the page failed: %w", err)
	}
	return doc, nil
}

// readMeta pulls the title, author and language out of a page, preferring the
// sources that were written to be read by machines.
func readMeta(doc *html.Node) pageMeta {
	var (
		m         pageMeta
		ogTitle   string
		docTitle  string
		h1Title   string
		metaAuth  string
		ldTitle   string
		ldAuthor  string
		bylineTxt string
	)

	walk(doc, func(n *html.Node) {
		if n.Type != html.ElementNode {
			return
		}
		switch n.DataAtom {
		case atom.Html:
			if m.Language == "" {
				m.Language = strings.TrimSpace(attr(n, "lang"))
			}
		case atom.Title:
			if docTitle == "" {
				docTitle = collapse(textOf(n))
			}
		case atom.H1:
			if h1Title == "" {
				h1Title = collapse(textOf(n))
			}
		case atom.Meta:
			name := strings.ToLower(attr(n, "property"))
			if name == "" {
				name = strings.ToLower(attr(n, "name"))
			}
			content := collapse(attr(n, "content"))
			if content == "" {
				return
			}
			switch name {
			case "og:title", "twitter:title":
				if ogTitle == "" {
					ogTitle = content
				}
			case "author", "article:author", "book:author", "twitter:creator", "dc.creator", "citation_author":
				// Some sites put a profile URL in article:author; a URL is not
				// a name and is worse than having none.
				if metaAuth == "" && !looksLikeURL(content) {
					metaAuth = content
				}
			case "description", "og:description":
				if m.Description == "" {
					m.Description = content
				}
			case "og:locale":
				if m.Language == "" {
					m.Language = strings.ReplaceAll(content, "_", "-")
				}
			}
		case atom.Script:
			if !strings.Contains(strings.ToLower(attr(n, "type")), "ld+json") {
				return
			}
			t, a := readLinkedData(textOf(n))
			if ldTitle == "" {
				ldTitle = t
			}
			if ldAuthor == "" {
				ldAuthor = a
			}
		case atom.A, atom.Span, atom.Div, atom.P:
			if bylineTxt != "" {
				return
			}
			if strings.EqualFold(attr(n, "rel"), "author") ||
				strings.EqualFold(attr(n, "itemprop"), "author") ||
				regexp.MustCompile(`(?i)byline|author`).MatchString(attr(n, "class")+" "+attr(n, "id")) {
				if txt := collapse(textOf(n)); len(txt) > 1 && len(txt) < 80 {
					bylineTxt = strings.TrimPrefix(strings.TrimPrefix(txt, "By "), "by ")
				}
			}
		}
	})

	m.Title = firstNonEmpty(ldTitle, ogTitle, docTitle, h1Title)
	m.Author = firstNonEmpty(ldAuthor, metaAuth, bylineTxt)
	return m
}

// readLinkedData reads a schema.org JSON-LD block. Only Book and the article
// types are honoured: a WebSite or Organization block names the site, not the
// work, and taking its name would file every story under the publisher.
func readLinkedData(body string) (title, author string) {
	var raw any
	if err := json.Unmarshal([]byte(strings.TrimSpace(body)), &raw); err != nil {
		return "", ""
	}
	var visit func(v any)
	visit = func(v any) {
		switch t := v.(type) {
		case []any:
			for _, e := range t {
				visit(e)
			}
		case map[string]any:
			if graph, ok := t["@graph"]; ok {
				visit(graph)
			}
			switch strings.ToLower(asString(t["@type"])) {
			case "book", "article", "newsarticle", "blogposting", "chapter", "creativework", "shortstory":
				if title == "" {
					title = collapse(firstNonEmpty(asString(t["name"]), asString(t["headline"])))
				}
				if author == "" {
					author = collapse(authorName(t["author"]))
				}
			}
		}
	}
	visit(raw)
	return title, author
}

func authorName(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []any:
		for _, e := range t {
			if n := authorName(e); n != "" {
				return n
			}
		}
	case map[string]any:
		return asString(t["name"])
	}
	return ""
}

func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []any:
		if len(t) > 0 {
			return asString(t[0])
		}
	}
	return ""
}

// findContent returns the subtree most likely to be the story, or nil.
func findContent(doc *html.Node) *html.Node {
	scores := map[*html.Node]float64{}

	walk(doc, func(n *html.Node) {
		if n.Type != html.ElementNode {
			return
		}
		switch n.DataAtom {
		case atom.P, atom.Pre, atom.Blockquote, atom.Td, atom.Article, atom.Section, atom.Div:
		default:
			return
		}
		text := collapse(textOf(n))
		if len(text) < 25 {
			return
		}
		base := 1.0 + float64(strings.Count(text, ",")) + minFloat(float64(len(text))/100, 3)
		// The paragraph's own score goes to its containers, not to itself: a
		// wrapper that holds many paragraphs is what we are looking for.
		for i, up := 0, n.Parent; i < 3 && up != nil; i, up = i+1, up.Parent {
			if up.Type != html.ElementNode {
				break
			}
			scores[up] += base / float64(i+1)
		}
	})

	var (
		best      *html.Node
		bestScore float64
	)
	for n, score := range scores {
		score += classWeight(n)
		if density := linkDensity(n); density > 0.5 {
			continue
		} else {
			score *= 1 - density
		}
		if isDenied(n) {
			continue
		}
		if score > bestScore {
			best, bestScore = n, score
		}
	}
	if best == nil || bestScore < 8 {
		return nil
	}
	return best
}

// classWeight nudges a candidate by what its own class and id say it is.
func classWeight(n *html.Node) float64 {
	sig := attr(n, "class") + " " + attr(n, "id") + " " + attr(n, "itemprop")
	var w float64
	if positivePattern.MatchString(sig) {
		w += 25
	}
	if negativePattern.MatchString(sig) {
		w -= 40
	}
	if n.DataAtom == atom.Article || strings.EqualFold(attr(n, "role"), "main") {
		w += 30
	}
	return w
}

// isDenied reports whether a node's own class, id or role marks it as page
// furniture - a share bar, an ad slot, a comment thread.
func isDenied(n *html.Node) bool {
	if n.Type != html.ElementNode {
		return false
	}
	if tagsDropped[n.Data] {
		return true
	}
	if strings.EqualFold(attr(n, "aria-hidden"), "true") {
		return true
	}
	switch strings.ToLower(attr(n, "role")) {
	case "navigation", "banner", "complementary", "contentinfo", "search", "dialog", "alert":
		return true
	}
	sig := attr(n, "class") + " " + attr(n, "id") + " " + attr(n, "data-testid")
	if sig == "  " {
		return false
	}
	return negativePattern.MatchString(sig) && !positivePattern.MatchString(sig)
}

// linkDensity is the share of a node's text that sits inside links. A block
// that is mostly links is a menu however well it scored.
func linkDensity(n *html.Node) float64 {
	total := len(collapse(textOf(n)))
	if total == 0 {
		return 0
	}
	var linked int
	walk(n, func(c *html.Node) {
		if c.Type == html.ElementNode && c.DataAtom == atom.A {
			linked += len(collapse(textOf(c)))
		}
	})
	return minFloat(float64(linked)/float64(total), 1)
}

// sanitized is one cleaned chapter body plus the images it wants.
type sanitized struct {
	XHTML  string
	Images []string // absolute URLs, in the order they appear
	Words  int
}

// sanitize rewrites a content subtree into XHTML that is safe to put in a
// book: allowlisted elements, allowlisted attributes, no scripts, no styles,
// no external references except the images, which the caller re-fetches
// through the guard and rewrites to local paths.
// dropHeading names a heading to leave out: the chapter's own title is
// written by the document template, so keeping the <h1> it was read from would
// print it twice.
func sanitize(n *html.Node, base *url.URL, dropHeading string) sanitized {
	var (
		out     strings.Builder
		imgs    []string
		dropped bool
	)
	var emit func(n *html.Node)
	emit = func(n *html.Node) {
		switch n.Type {
		case html.TextNode:
			out.WriteString(escapeXML(n.Data))
			return
		case html.ElementNode:
		default:
			return
		}
		name := strings.ToLower(n.Data)
		if tagsDropped[name] || isDenied(n) {
			return
		}
		if !dropped && dropHeading != "" && (name == "h1" || name == "h2") &&
			collapse(textOf(n)) == dropHeading {
			dropped = true
			return
		}
		if name == "a" {
			// Links are unwrapped rather than kept: a book that links back out
			// to the site it was scraped from is a book with a tracking pixel
			// in prose form, and the text reads the same without them.
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				emit(c)
			}
			return
		}
		if name == "img" {
			src := strings.TrimSpace(firstNonEmpty(attr(n, "src"), attr(n, "data-src"), attr(n, "data-original")))
			abs := resolve(base, src)
			if abs == "" {
				return
			}
			imgs = append(imgs, abs)
			out.WriteString(`<img src="` + escapeXML(imagePlaceholder(len(imgs)-1)) + `" alt="` +
				escapeXML(collapse(attr(n, "alt"))) + `"/>`)
			return
		}
		if !tagsKept[name] {
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				emit(c)
			}
			return
		}
		// div and span carry no meaning once the layout is gone; keeping them
		// would only add depth.
		if name == "div" || name == "span" {
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				emit(c)
			}
			return
		}

		out.WriteString("<" + name)
		if allowed, ok := attrsKept[name]; ok {
			for _, a := range n.Attr {
				if allowed[strings.ToLower(a.Key)] {
					out.WriteString(" " + strings.ToLower(a.Key) + `="` + escapeXML(a.Val) + `"`)
				}
			}
		}
		if voidTags[name] {
			out.WriteString("/>")
			return
		}
		out.WriteString(">")
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			emit(c)
		}
		out.WriteString("</" + name + ">")
	}

	for c := n.FirstChild; c != nil; c = c.NextSibling {
		emit(c)
	}
	body := strings.TrimSpace(out.String())
	if body == "" {
		body = "<p></p>"
	}
	return sanitized{XHTML: body, Images: imgs, Words: len(strings.Fields(collapse(textOf(n))))}
}

// imagePlaceholder is the href written into a chapter for its Nth image. The
// real filename is substituted once the image has been fetched and accepted,
// so a chapter whose images all fail still renders. It is spelled as a URI in
// a scheme nothing uses, because it has to survive XML escaping unchanged and
// must never be mistaken for a real reference if a substitution is ever missed.
func imagePlaceholder(i int) string { return "x-gbs-image:" + strconv.Itoa(i) }

// chapterTitle names one chapter: the heading inside the content when there is
// one, then the page's own title, then a number.
func chapterTitle(content *html.Node, meta pageMeta, index int) string {
	if content != nil {
		var found string
		walk(content, func(n *html.Node) {
			if found != "" || n.Type != html.ElementNode {
				return
			}
			switch n.DataAtom {
			case atom.H1, atom.H2:
				if txt := collapse(textOf(n)); len(txt) > 1 && len(txt) < 200 {
					found = txt
				}
			}
		})
		if found != "" {
			return found
		}
	}
	if meta.Title != "" {
		return meta.Title
	}
	return "Chapter " + strconv.Itoa(index+1)
}

// nextLink finds the link to the following chapter, or "".
//
// rel="next" is authoritative and is checked first. Failing that, an anchor
// whose visible text says "next" (in one of the handful of forms sites use) or
// names the chapter after this one is followed. Anything on another host is
// refused outright: following an off-site link is how a chapter walk turns
// into a crawl of the open internet.
func nextLink(doc *html.Node, base *url.URL, wantChapter int) string {
	var relNext, textNext, numbered string

	walk(doc, func(n *html.Node) {
		if n.Type != html.ElementNode {
			return
		}
		if n.DataAtom != atom.A && n.DataAtom != atom.Link {
			return
		}
		href := strings.TrimSpace(attr(n, "href"))
		if href == "" || strings.HasPrefix(href, "#") {
			return
		}
		abs := resolve(base, href)
		if abs == "" || !sameHost(base, abs) {
			return
		}
		for _, rel := range strings.Fields(strings.ToLower(attr(n, "rel"))) {
			if rel == "next" && relNext == "" {
				relNext = abs
				return
			}
		}
		if n.DataAtom != atom.A {
			return
		}
		if isDenied(n) {
			return
		}
		text := collapse(textOf(n))
		if text == "" {
			text = collapse(attr(n, "title")) // an icon-only "next" arrow
		}
		if textNext == "" && nextTextPattern.MatchString(text) {
			textNext = abs
			return
		}
		if numbered == "" && wantChapter > 0 {
			if m := chapterNumberRe.FindStringSubmatch(text); m != nil {
				if n, err := strconv.Atoi(m[1]); err == nil && n == wantChapter {
					numbered = abs
				}
			}
		}
	})

	return firstNonEmpty(relNext, textNext, numbered)
}

// chapterNumberOf reads a chapter number out of a title, so the walk can look
// for "Chapter 8" after "Chapter 7". Zero means the title carries no number.
func chapterNumberOf(title string) int {
	if m := chapterNumberRe.FindStringSubmatch(strings.TrimSpace(title)); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			return n
		}
	}
	return 0
}

/* ---------------- small helpers over the parse tree ---------------- */

func walk(n *html.Node, fn func(*html.Node)) {
	if n == nil {
		return
	}
	fn(n)
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walk(c, fn)
	}
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val
		}
	}
	return ""
}

func textOf(n *html.Node) string {
	var b strings.Builder
	walk(n, func(c *html.Node) {
		if c.Type == html.TextNode {
			b.WriteString(c.Data)
		}
	})
	return b.String()
}

// collapse trims a string and squeezes every run of whitespace into one space,
// which is what turns markup indentation into a readable label.
func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

func resolve(base *url.URL, ref string) string {
	if ref == "" || strings.HasPrefix(ref, "data:") || strings.HasPrefix(ref, "javascript:") {
		return ""
	}
	u, err := url.Parse(ref)
	if err != nil {
		return ""
	}
	abs := base.ResolveReference(u)
	if abs.Scheme != "http" && abs.Scheme != "https" {
		return ""
	}
	abs.Fragment = ""
	return abs.String()
}

// sameHost reports whether a link stays on the site the walk started on.
//
// The hostname has to match, and so does the port - with the one exception
// that a link between two default ports counts as the same site, so a page
// fetched over http whose "next" link is the https form of itself is still
// followed. An explicit, different port is a different service.
func sameHost(base *url.URL, raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if !strings.EqualFold(u.Hostname(), base.Hostname()) {
		return false
	}
	return u.Port() == base.Port() || (u.Port() == "" && base.Port() == "")
}

func looksLikeURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// escapeXML escapes for XHTML, where the five predefined entities are all that
// exist: an EPUB chapter is parsed as XML, and a bare "&" is a fatal error.
func escapeXML(s string) string {
	var b strings.Builder
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
			// XML 1.0 forbids most control characters outright, so they are
			// dropped rather than escaped.
			if r < 0x20 && r != '\t' && r != '\n' && r != '\r' {
				continue
			}
			b.WriteRune(r)
		}
	}
	return b.String()
}
