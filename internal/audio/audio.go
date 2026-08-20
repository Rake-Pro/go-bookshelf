// Package audio extracts metadata, duration and chapters from audiobook files
// without shelling out to an external tool: ID3v2 tags and MPEG frame headers
// for MP3, and the ISO base media (MP4) box tree for M4B/M4A.
//
// Tag content is untrusted: strings are truncated to a sane length and never
// interpreted as markup by this package.
package audio

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// maxStringLen bounds any single tag value taken from a file.
const maxStringLen = 4096

// ErrUnsupported is returned for a file extension this package does not read.
var ErrUnsupported = errors.New("audio: unsupported format")

// Chapter is one chapter marker, in milliseconds from the start of the file.
type Chapter struct {
	Title   string
	StartMS int64
	EndMS   int64
}

// Metadata is everything read from a single audio file.
type Metadata struct {
	Format      string // "mp3" or "mp4"
	Title       string
	Album       string
	Subtitle    string
	Artist      string
	AlbumArtist string
	Narrator    string
	Composer    string
	Description string
	Publisher   string
	Language    string
	Date        string
	ASIN        string
	ISBN        string
	Genres      []string
	Track       int
	TrackTotal  int
	Disc        int
	DiscTotal   int
	DurationMS  int64
	Chapters    []Chapter
	Cover       []byte
	CoverType   string
}

// IsAudioFile reports whether name has an extension this package reads.
func IsAudioFile(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mp3", ".m4b", ".m4a", ".mp4":
		return true
	}
	return false
}

// FormatOf returns the short format name for a filename ("mp3", "m4b", ...).
func FormatOf(name string) string {
	return strings.TrimPrefix(strings.ToLower(filepath.Ext(name)), ".")
}

// Probe reads metadata from the file at path, dispatching on its extension.
func Probe(path string) (Metadata, error) {
	f, err := os.Open(path)
	if err != nil {
		return Metadata{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return Metadata{}, err
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp3":
		return ProbeMP3(f, info.Size())
	case ".m4b", ".m4a", ".mp4":
		return ProbeMP4(f, info.Size())
	}
	return Metadata{}, fmt.Errorf("%w: %s", ErrUnsupported, filepath.Ext(path))
}

func clampString(s string) string {
	s = strings.TrimRight(s, "\x00")
	s = strings.TrimSpace(s)
	if len(s) > maxStringLen {
		s = s[:maxStringLen]
	}
	// Tag values are single-line labels; strip control characters so a crafted
	// tag cannot smuggle newlines into logs or headers.
	return strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\t' {
			return -1
		}
		return r
	}, s)
}
