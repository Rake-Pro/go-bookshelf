package upload

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/rs/zerolog/log"
)

// Formats the uploader understands. These are the short names the rest of the
// package switches on; they match internal/audio's FormatOf where they overlap.
const (
	FormatEPUB = "epub"
	FormatMP4  = "mp4"
	FormatMP3  = "mp3"
)

// sniffWindow is how much of a file the magic-byte checks look at. An MP3 may
// carry a large ID3v2 tag (cover art) before its first audio frame, so the
// frame search needs room; 64 KiB covers every real tag and bounds the work.
const sniffWindow = 64 << 10

// epubMimetype is the exact payload the "mimetype" entry of an EPUB must hold.
const epubMimetype = "application/epub+zip"

// mp4Brands are the ftyp brands an audiobook container legitimately declares.
var mp4Brands = map[string]bool{
	"M4A ": true,
	"M4B ": true,
	"mp42": true,
	"isom": true,
}

// zipLocalHeader is the signature every zip archive starts with.
var zipLocalHeader = []byte{'P', 'K', 0x03, 0x04}

// magicOK reports whether head - the first bytes of the upload - is consistent
// with the claimed format. This runs before any parser, because the parsers
// are the expensive, attackable part and there is no reason to hand them a
// file that is not even the right shape.
func magicOK(format string, head []byte) error {
	switch format {
	case FormatEPUB:
		if !bytes.HasPrefix(head, zipLocalHeader) {
			return fmt.Errorf("%w: not a zip archive", ErrMagic)
		}
		return nil
	case FormatMP4:
		// The ftyp box is size(4) + "ftyp"(4) + major brand(4).
		if len(head) < 12 || string(head[4:8]) != "ftyp" {
			return fmt.Errorf("%w: no ftyp box", ErrMagic)
		}
		if !mp4Brands[string(head[8:12])] {
			return fmt.Errorf("%w: unexpected MP4 brand %q", ErrMagic, printable(head[8:12]))
		}
		return nil
	case FormatMP3:
		if bytes.HasPrefix(head, []byte("ID3")) {
			return nil
		}
		if findMPEGFrame(head) >= 0 {
			return nil
		}
		return fmt.Errorf("%w: no ID3 tag and no MPEG frame in the first %d bytes", ErrMagic, len(head))
	}
	return fmt.Errorf("%w: unknown format %q", ErrMagic, format)
}

// findMPEGFrame returns the offset of the first plausible MPEG audio frame
// header in b, or -1. The four reserved bit patterns are what separate a real
// header from two bytes of coincidence, so all four are checked.
func findMPEGFrame(b []byte) int {
	for i := 0; i+4 <= len(b); i++ {
		if b[i] != 0xFF || b[i+1]&0xE0 != 0xE0 {
			continue
		}
		version := (b[i+1] >> 3) & 0x03
		layer := (b[i+1] >> 1) & 0x03
		bitrate := (b[i+2] >> 4) & 0x0F
		sample := (b[i+2] >> 2) & 0x03
		if version == 0x01 || layer == 0x00 || bitrate == 0x0F || bitrate == 0x00 || sample == 0x03 {
			continue
		}
		return i
	}
	return -1
}

// checkEPUBContainer verifies the archive really is an EPUB, by the container
// rule rather than by its extension: the "mimetype" entry must exist and hold
// exactly the EPUB media type.
//
// The specification also requires that entry to be first and stored
// uncompressed, which is what lets a reader identify the format from the first
// bytes alone. Plenty of real books in circulation break that rule while being
// otherwise valid, so a deviation is accepted and logged rather than refused:
// the check that matters for safety is the one the reader already performs on
// every entry.
func checkEPUBContainer(path string, name string) error {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrMagic, err)
	}
	defer zr.Close()

	var (
		entry *zip.File
		index int
	)
	for i, f := range zr.File {
		if f.Name == "mimetype" {
			entry, index = f, i
			break
		}
	}
	if entry == nil {
		return fmt.Errorf("%w: the archive has no mimetype entry", ErrMagic)
	}
	rc, err := entry.Open()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrMagic, err)
	}
	defer rc.Close()
	// The declared type is 20 bytes; read a little more so a longer value is
	// seen as wrong rather than silently truncated to the right answer.
	body, err := io.ReadAll(io.LimitReader(rc, 64))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrMagic, err)
	}
	if strings.TrimSpace(string(body)) != epubMimetype {
		return fmt.Errorf("%w: mimetype entry declares %q", ErrMagic, printable(body))
	}
	if index != 0 || entry.Method != zip.Store {
		log.Info().Str("file", name).Int("index", index).Uint16("method", entry.Method).
			Msg("EPUB mimetype entry is not the first, stored entry; accepting it anyway")
	}
	return nil
}

// printable renders untrusted bytes for an error message without letting them
// carry control characters into a log line.
func printable(b []byte) string {
	const max = 32
	if len(b) > max {
		b = b[:max]
	}
	var sb strings.Builder
	for _, c := range b {
		if c < 0x20 || c > 0x7e {
			sb.WriteByte('.')
			continue
		}
		sb.WriteByte(c)
	}
	return sb.String()
}

// ErrMagic is returned when a file's bytes do not match the format its
// extension claims.
var ErrMagic = errors.New("upload: the file's content does not match its type")

// Sniff identifies a file from its leading bytes alone, returning one of the
// Format constants or "". It is what the URL importer decides on: a server's
// Content-Type and a URL's extension are both things the other end chose, and
// neither is a reason to hand a file to a parser.
func Sniff(head []byte) string {
	for _, format := range []string{FormatEPUB, FormatMP4, FormatMP3} {
		if magicOK(format, head) == nil {
			return format
		}
	}
	return ""
}

// ExtensionFor is the extension a sniffed format is filed under.
func ExtensionFor(format string) string {
	switch format {
	case FormatEPUB:
		return ".epub"
	case FormatMP4:
		return ".m4b"
	case FormatMP3:
		return ".mp3"
	}
	return ""
}
