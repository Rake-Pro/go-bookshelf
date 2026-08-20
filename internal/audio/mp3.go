package audio

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf16"
)

// maxTagSize bounds an ID3v2 tag so a crafted header cannot make the reader
// allocate arbitrarily.
const maxTagSize = 32 << 20 // 32 MiB

// ErrNoFrame is returned when no MPEG audio frame header can be located.
var ErrNoFrame = errors.New("audio: no mpeg frame header found")

// ProbeMP3 reads ID3v2 tags and computes the duration of an MPEG audio stream.
func ProbeMP3(r io.ReaderAt, size int64) (Metadata, error) {
	m := Metadata{Format: "mp3"}

	audioStart, err := parseID3v2(r, size, &m)
	if err != nil {
		return m, err
	}
	audioEnd := size
	// An ID3v1 trailer is not audio; excluding it keeps the CBR estimate honest.
	if size >= 128 {
		var tag [3]byte
		if _, err := r.ReadAt(tag[:], size-128); err == nil && string(tag[:]) == "TAG" {
			audioEnd = size - 128
		}
	}

	dur, err := mp3Duration(r, audioStart, audioEnd)
	if err != nil && !errors.Is(err, ErrNoFrame) {
		return m, err
	}
	m.DurationMS = dur
	for i := range m.Chapters {
		if m.Chapters[i].EndMS == 0 && i+1 < len(m.Chapters) {
			m.Chapters[i].EndMS = m.Chapters[i+1].StartMS
		}
	}
	if n := len(m.Chapters); n > 0 && m.Chapters[n-1].EndMS == 0 {
		m.Chapters[n-1].EndMS = m.DurationMS
	}
	return m, nil
}

// parseID3v2 fills m from the ID3v2 tag (when present) and returns the offset
// at which audio frames begin.
func parseID3v2(r io.ReaderAt, size int64, m *Metadata) (int64, error) {
	var hdr [10]byte
	if size < 10 {
		return 0, nil
	}
	if _, err := r.ReadAt(hdr[:], 0); err != nil {
		return 0, err
	}
	if string(hdr[0:3]) != "ID3" {
		return 0, nil
	}
	major := hdr[3]
	flags := hdr[5]
	tagSize := int64(syncsafe(hdr[6:10]))
	if tagSize < 0 || tagSize > maxTagSize || 10+tagSize > size {
		return 0, fmt.Errorf("audio: implausible ID3v2 tag size %d", tagSize)
	}
	body := make([]byte, tagSize)
	if _, err := r.ReadAt(body, 10); err != nil {
		return 0, err
	}
	audioStart := 10 + tagSize
	if flags&0x10 != 0 { // footer present
		audioStart += 10
	}
	if major > 4 {
		return audioStart, nil // future version: skip the tag, keep the audio
	}
	if flags&0x40 != 0 && major >= 3 { // extended header
		if len(body) < 4 {
			return audioStart, nil
		}
		extSize := int(binary.BigEndian.Uint32(body[0:4]))
		if major == 4 {
			extSize = int(syncsafe(body[0:4]))
		} else {
			extSize += 4
		}
		if extSize > 0 && extSize < len(body) {
			body = body[extSize:]
		}
	}
	if flags&0x80 != 0 && major < 4 {
		body = deunsync(body)
	}
	parseID3Frames(body, major, m)
	return audioStart, nil
}

func parseID3Frames(body []byte, major byte, m *Metadata) {
	idLen, sizeLen, flagLen := 4, 4, 2
	if major == 2 {
		idLen, sizeLen, flagLen = 3, 3, 0
	}
	for off := 0; off+idLen+sizeLen+flagLen <= len(body); {
		id := string(body[off : off+idLen])
		if strings.TrimRight(id, "\x00") == "" {
			return // padding
		}
		var frameSize int
		switch {
		case major == 2:
			frameSize = int(body[off+3])<<16 | int(body[off+4])<<8 | int(body[off+5])
		case major == 4:
			frameSize = int(syncsafe(body[off+4 : off+8]))
		default:
			frameSize = int(binary.BigEndian.Uint32(body[off+4 : off+8]))
		}
		dataStart := off + idLen + sizeLen + flagLen
		if frameSize < 0 || dataStart+frameSize > len(body) {
			return
		}
		data := body[dataStart : dataStart+frameSize]
		applyID3Frame(canonicalFrameID(id), data, major, m)
		off = dataStart + frameSize
	}
}

// canonicalFrameID maps the 3-character ID3v2.2 ids onto their v2.3/v2.4
// equivalents so the rest of the parser only handles one set.
func canonicalFrameID(id string) string {
	switch id {
	case "TT2":
		return "TIT2"
	case "TT3":
		return "TIT3"
	case "TAL":
		return "TALB"
	case "TP1":
		return "TPE1"
	case "TP2":
		return "TPE2"
	case "TP3":
		return "TPE3"
	case "TCM":
		return "TCOM"
	case "TCO":
		return "TCON"
	case "TRK":
		return "TRCK"
	case "TPA":
		return "TPOS"
	case "TYE":
		return "TYER"
	case "TPB":
		return "TPUB"
	case "TLA":
		return "TLAN"
	case "COM":
		return "COMM"
	case "PIC":
		return "APIC"
	case "TXX":
		return "TXXX"
	}
	return id
}

func applyID3Frame(id string, data []byte, major byte, m *Metadata) {
	switch id {
	case "TIT2":
		m.Title = decodeText(data)
	case "TIT3":
		m.Subtitle = decodeText(data)
	case "TALB":
		m.Album = decodeText(data)
	case "TPE1":
		m.Artist = decodeText(data)
	case "TPE2":
		m.AlbumArtist = decodeText(data)
	case "TPE3":
		m.Narrator = decodeText(data)
	case "TCOM":
		m.Composer = decodeText(data)
	case "TPUB":
		m.Publisher = decodeText(data)
	case "TLAN":
		m.Language = decodeText(data)
	case "TCON":
		if g := decodeText(data); g != "" {
			m.Genres = append(m.Genres, g)
		}
	case "TRCK":
		m.Track, m.TrackTotal = splitPair(decodeText(data))
	case "TPOS":
		m.Disc, m.DiscTotal = splitPair(decodeText(data))
	case "TYER", "TDRC", "TDRL":
		if m.Date == "" {
			m.Date = decodeText(data)
		}
	case "COMM":
		if m.Description == "" {
			m.Description = decodeComment(data)
		}
	case "TXXX":
		key, value := decodeUserText(data)
		switch strings.ToUpper(key) {
		case "ASIN":
			m.ASIN = value
		case "ISBN":
			m.ISBN = value
		case "NARRATOR", "NARRATED BY":
			m.Narrator = value
		case "SUBTITLE":
			m.Subtitle = value
		case "SERIES", "SHOW", "MVNM":
			// Series lives on the item, not the file; the scanner reads it
			// from the album/series tags it already collects.
		}
	case "APIC":
		if m.Cover == nil {
			if img, mimeType := decodePicture(data, major); img != nil {
				m.Cover, m.CoverType = img, mimeType
			}
		}
	case "CHAP":
		if ch, ok := decodeChapter(data, major); ok {
			m.Chapters = append(m.Chapters, ch)
		}
	}
}

func decodeChapter(data []byte, major byte) (Chapter, bool) {
	i := indexZero(data)
	if i < 0 || len(data) < i+1+16 {
		return Chapter{}, false
	}
	rest := data[i+1:]
	ch := Chapter{
		StartMS: int64(binary.BigEndian.Uint32(rest[0:4])),
		EndMS:   int64(binary.BigEndian.Uint32(rest[4:8])),
	}
	if ch.EndMS == 0xFFFFFFFF {
		ch.EndMS = 0
	}
	sub := rest[16:]
	var m2 Metadata
	parseID3Frames(sub, major, &m2)
	ch.Title = m2.Title
	if ch.Title == "" {
		ch.Title = clampString(string(data[:max(i, 0)]))
	}
	return ch, true
}

// decodeText decodes an ID3 text frame: one encoding byte then the payload.
func decodeText(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	return clampString(decodeString(data[0], data[1:]))
}

func decodeUserText(data []byte) (string, string) {
	if len(data) < 2 {
		return "", ""
	}
	enc, rest := data[0], data[1:]
	desc, value := splitEncoded(enc, rest)
	return clampString(decodeString(enc, desc)), clampString(decodeString(enc, value))
}

func decodeComment(data []byte) string {
	if len(data) < 5 {
		return ""
	}
	enc, rest := data[0], data[4:] // skip the 3-byte language code
	_, value := splitEncoded(enc, rest)
	return clampString(decodeString(enc, value))
}

// splitEncoded splits a description from the value that follows it, honouring
// the two-byte terminator used by the UTF-16 encodings.
func splitEncoded(enc byte, b []byte) (desc, value []byte) {
	if enc == 1 || enc == 2 {
		for i := 0; i+1 < len(b); i += 2 {
			if b[i] == 0 && b[i+1] == 0 {
				return b[:i], b[i+2:]
			}
		}
		return b, nil
	}
	if i := indexZero(b); i >= 0 {
		return b[:i], b[i+1:]
	}
	return b, nil
}

func decodeString(enc byte, b []byte) string {
	switch enc {
	case 0:
		out := make([]rune, 0, len(b))
		for _, c := range b {
			out = append(out, rune(c)) // ISO-8859-1
		}
		return string(out)
	case 1:
		return decodeUTF16(b, true)
	case 2:
		return decodeUTF16(b, false)
	default:
		return string(b)
	}
}

func decodeUTF16(b []byte, withBOM bool) string {
	bigEndian := true
	if withBOM && len(b) >= 2 {
		switch {
		case b[0] == 0xFF && b[1] == 0xFE:
			bigEndian, b = false, b[2:]
		case b[0] == 0xFE && b[1] == 0xFF:
			bigEndian, b = true, b[2:]
		}
	}
	units := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		if bigEndian {
			units = append(units, binary.BigEndian.Uint16(b[i:i+2]))
		} else {
			units = append(units, binary.LittleEndian.Uint16(b[i:i+2]))
		}
	}
	return string(utf16.Decode(units))
}

func decodePicture(data []byte, major byte) ([]byte, string) {
	if len(data) < 4 {
		return nil, ""
	}
	rest := data[1:]
	var mimeType string
	if major == 2 {
		if len(rest) < 3 {
			return nil, ""
		}
		mimeType = "image/" + strings.ToLower(strings.TrimSpace(string(rest[:3])))
		rest = rest[3:]
	} else {
		i := indexZero(rest)
		if i < 0 {
			return nil, ""
		}
		mimeType = strings.TrimSpace(string(rest[:i]))
		rest = rest[i+1:]
	}
	if len(rest) < 2 {
		return nil, ""
	}
	rest = rest[1:] // picture type
	// Description, terminated per the frame's text encoding.
	_, img := splitEncoded(data[0], rest)
	if img == nil {
		return nil, ""
	}
	if !strings.HasPrefix(mimeType, "image/") {
		mimeType = "image/jpeg"
	}
	return img, mimeType
}

func indexZero(b []byte) int {
	for i, c := range b {
		if c == 0 {
			return i
		}
	}
	return -1
}

func splitPair(v string) (int, int) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, 0
	}
	a, b, _ := strings.Cut(v, "/")
	n, _ := strconv.Atoi(strings.TrimSpace(a))
	t, _ := strconv.Atoi(strings.TrimSpace(b))
	return n, t
}

func syncsafe(b []byte) uint32 {
	if len(b) < 4 {
		return 0
	}
	return uint32(b[0]&0x7f)<<21 | uint32(b[1]&0x7f)<<14 | uint32(b[2]&0x7f)<<7 | uint32(b[3]&0x7f)
}

// deunsync reverses ID3v2 unsynchronisation (0xFF 0x00 -> 0xFF).
func deunsync(b []byte) []byte {
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		out = append(out, b[i])
		if b[i] == 0xFF && i+1 < len(b) && b[i+1] == 0x00 {
			i++
		}
	}
	return out
}
