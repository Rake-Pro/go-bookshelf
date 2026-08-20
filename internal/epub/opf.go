package epub

import (
	"encoding/xml"
	"fmt"
	"path"
	"strconv"
	"strings"
)

// Person is a contributor with the role the package document gave them.
type Person struct {
	Name     string
	SortName string
	Role     string // "author", "narrator", "translator"
}

// Metadata is the subset of the OPF metadata the catalog stores.
type Metadata struct {
	Title       string
	SortTitle   string
	Subtitle    string
	Description string
	Language    string
	Published   string
	ISBN        string
	ASIN        string
	Publisher   string
	People      []Person
	Series      string
	SeriesIndex float64
	Tags        []string
}

// ManifestItem is one entry of the OPF manifest.
type ManifestItem struct {
	ID         string
	Href       string // relative to the OPF document
	MediaType  string
	Properties string
}

// Book is the parsed package document.
type Book struct {
	OPFPath   string // archive-relative path of the OPF document
	Meta      Metadata
	Manifest  []ManifestItem
	Spine     []string // manifest hrefs, in reading order
	CoverHref string   // relative to the OPF document; empty when absent
}

type containerXML struct {
	Rootfiles []struct {
		FullPath  string `xml:"full-path,attr"`
		MediaType string `xml:"media-type,attr"`
	} `xml:"rootfiles>rootfile"`
}

type opfPackage struct {
	Metadata struct {
		Titles []struct {
			ID    string `xml:"id,attr"`
			Value string `xml:",chardata"`
		} `xml:"title"`
		Creators []struct {
			ID     string `xml:"id,attr"`
			Role   string `xml:"role,attr"`
			FileAs string `xml:"file-as,attr"`
			Value  string `xml:",chardata"`
		} `xml:"creator"`
		Contributors []struct {
			ID     string `xml:"id,attr"`
			Role   string `xml:"role,attr"`
			FileAs string `xml:"file-as,attr"`
			Value  string `xml:",chardata"`
		} `xml:"contributor"`
		Identifiers []struct {
			Scheme string `xml:"scheme,attr"`
			Value  string `xml:",chardata"`
		} `xml:"identifier"`
		Language    string   `xml:"language"`
		Description string   `xml:"description"`
		Publisher   string   `xml:"publisher"`
		Date        string   `xml:"date"`
		Subjects    []string `xml:"subject"`
		Metas       []struct {
			Name     string `xml:"name,attr"`
			Content  string `xml:"content,attr"`
			Property string `xml:"property,attr"`
			Refines  string `xml:"refines,attr"`
			ID       string `xml:"id,attr"`
			Value    string `xml:",chardata"`
		} `xml:"meta"`
	} `xml:"metadata"`
	Manifest struct {
		Items []struct {
			ID         string `xml:"id,attr"`
			Href       string `xml:"href,attr"`
			MediaType  string `xml:"media-type,attr"`
			Properties string `xml:"properties,attr"`
		} `xml:"item"`
	} `xml:"manifest"`
	Spine struct {
		ItemRefs []struct {
			IDRef  string `xml:"idref,attr"`
			Linear string `xml:"linear,attr"`
		} `xml:"itemref"`
	} `xml:"spine"`
}

// Book parses (once) and returns the package document.
func (r *Reader) Book() (*Book, error) {
	if r.book != nil {
		return r.book, nil
	}
	raw, err := r.ReadFile(containerPath)
	if err != nil {
		return nil, fmt.Errorf("epub: read %s: %w", containerPath, err)
	}
	var c containerXML
	if err := xml.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("epub: parse %s: %w", containerPath, err)
	}
	opfPath := ""
	for _, rf := range c.Rootfiles {
		if rf.FullPath != "" {
			opfPath = rf.FullPath
			break
		}
	}
	if opfPath == "" {
		return nil, ErrNoRootfile
	}
	clean, err := safeEntryName(opfPath)
	if err != nil {
		return nil, err
	}
	opf, err := r.ReadFile(clean)
	if err != nil {
		return nil, fmt.Errorf("epub: read package document: %w", err)
	}
	book, err := ParseOPF(opf)
	if err != nil {
		return nil, err
	}
	book.OPFPath = clean
	r.opfPath = clean
	r.book = book
	return book, nil
}

// ParseOPF parses an OPF package document. It is exported so a sidecar
// metadata.opf can be parsed with the same rules as an embedded one.
func ParseOPF(data []byte) (*Book, error) {
	var p opfPackage
	if err := xml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("epub: parse package document: %w", err)
	}

	book := &Book{}
	m := &book.Meta

	// EPUB 3 refines: property values attached to another element by id.
	refines := map[string]map[string]string{}
	for _, meta := range p.Metadata.Metas {
		if meta.Refines == "" || meta.Property == "" {
			continue
		}
		id := strings.TrimPrefix(meta.Refines, "#")
		if refines[id] == nil {
			refines[id] = map[string]string{}
		}
		refines[id][meta.Property] = strings.TrimSpace(meta.Value)
	}

	for _, t := range p.Metadata.Titles {
		v := strings.TrimSpace(t.Value)
		if v == "" {
			continue
		}
		switch refines[t.ID]["title-type"] {
		case "subtitle":
			if m.Subtitle == "" {
				m.Subtitle = v
			}
		default:
			if m.Title == "" {
				m.Title = v
				m.SortTitle = refines[t.ID]["file-as"]
			}
		}
	}

	addPerson := func(name, fileAs, rawRole, id string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		role := rawRole
		if role == "" {
			role = refines[id]["role"]
		}
		mapped := mapRole(role)
		if mapped == "" {
			return
		}
		if fileAs == "" {
			fileAs = refines[id]["file-as"]
		}
		m.People = append(m.People, Person{Name: name, SortName: strings.TrimSpace(fileAs), Role: mapped})
	}
	for _, c := range p.Metadata.Creators {
		role := c.Role
		if role == "" {
			role = "aut" // dc:creator with no role is the author
		}
		addPerson(c.Value, c.FileAs, role, c.ID)
	}
	for _, c := range p.Metadata.Contributors {
		addPerson(c.Value, c.FileAs, c.Role, c.ID)
	}

	m.Language = strings.TrimSpace(p.Metadata.Language)
	m.Description = strings.TrimSpace(p.Metadata.Description)
	m.Publisher = strings.TrimSpace(p.Metadata.Publisher)
	m.Published = strings.TrimSpace(p.Metadata.Date)
	for _, s := range p.Metadata.Subjects {
		if s = strings.TrimSpace(s); s != "" {
			m.Tags = append(m.Tags, s)
		}
	}
	for _, id := range p.Metadata.Identifiers {
		value := strings.TrimSpace(id.Value)
		if value == "" {
			continue
		}
		scheme := strings.ToUpper(strings.TrimSpace(id.Scheme))
		lower := strings.ToLower(value)
		switch {
		case scheme == "ISBN" || strings.HasPrefix(lower, "urn:isbn:") || strings.HasPrefix(lower, "isbn:"):
			if m.ISBN == "" {
				m.ISBN = normalizeIdentifier(value)
			}
		case scheme == "ASIN" || strings.HasPrefix(lower, "asin:"):
			if m.ASIN == "" {
				m.ASIN = normalizeIdentifier(value)
			}
		}
	}

	coverID := ""
	for _, meta := range p.Metadata.Metas {
		switch {
		case meta.Name == "calibre:series":
			m.Series = strings.TrimSpace(meta.Content)
		case meta.Name == "calibre:series_index":
			m.SeriesIndex = parseFloat(meta.Content)
		case meta.Name == "cover":
			coverID = meta.Content
		case meta.Refines == "" && meta.Property == "belongs-to-collection":
			if m.Series == "" {
				m.Series = strings.TrimSpace(meta.Value)
			}
			if pos := refines[meta.ID]["group-position"]; pos != "" && m.SeriesIndex == 0 {
				m.SeriesIndex = parseFloat(pos)
			}
		}
	}

	byID := make(map[string]ManifestItem, len(p.Manifest.Items))
	for _, it := range p.Manifest.Items {
		mi := ManifestItem{ID: it.ID, Href: strings.TrimSpace(it.Href), MediaType: it.MediaType, Properties: it.Properties}
		book.Manifest = append(book.Manifest, mi)
		byID[it.ID] = mi
		if strings.Contains(it.Properties, "cover-image") && book.CoverHref == "" {
			book.CoverHref = mi.Href
		}
	}
	if book.CoverHref == "" && coverID != "" {
		if mi, ok := byID[coverID]; ok {
			book.CoverHref = mi.Href
		}
	}
	for _, ref := range p.Spine.ItemRefs {
		if mi, ok := byID[ref.IDRef]; ok && mi.Href != "" {
			book.Spine = append(book.Spine, mi.Href)
		}
	}
	if book.CoverHref == "" {
		// Fall back to the first image in the manifest.
		for _, mi := range book.Manifest {
			if strings.HasPrefix(mi.MediaType, "image/") {
				book.CoverHref = mi.Href
				break
			}
		}
	}
	if m.Title == "" {
		m.Title = "Untitled"
	}
	return book, nil
}

// Cover returns the cover image bytes and its media type, or ErrNotFound.
func (r *Reader) Cover() ([]byte, string, error) {
	book, err := r.Book()
	if err != nil {
		return nil, "", err
	}
	if book.CoverHref == "" {
		return nil, "", ErrNotFound
	}
	name, err := r.Resolve(book.CoverHref)
	if err != nil {
		return nil, "", err
	}
	b, err := r.ReadFile(name)
	if err != nil {
		return nil, "", err
	}
	return b, ContentType(name), nil
}

// RelativeHref converts an archive-relative entry name back to a path relative
// to the OPF document, which is how the reader addresses resources.
func (r *Reader) RelativeHref(name string) string {
	base := path.Dir(r.opfPath)
	if base == "." || base == "" {
		return name
	}
	return strings.TrimPrefix(name, base+"/")
}

func mapRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "aut", "author", "":
		return "author"
	case "nrt", "narrator", "voc":
		return "narrator"
	case "trl", "translator":
		return "translator"
	}
	return ""
}

func normalizeIdentifier(v string) string {
	if i := strings.LastIndex(v, ":"); i >= 0 {
		v = v[i+1:]
	}
	return strings.TrimSpace(v)
}

func parseFloat(v string) float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil {
		return 0
	}
	return f
}
