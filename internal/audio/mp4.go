package audio

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
)

// maxBoxSize bounds a single box payload the reader will buffer.
const maxBoxSize = 64 << 20

// ErrBadMP4 is returned for a file whose box structure does not parse.
var ErrBadMP4 = errors.New("audio: malformed mp4 container")

// ProbeMP4 reads the ISO base media box tree: mvhd for duration,
// udta/meta/ilst for tags, and udta/chpl for the chapter list.
func ProbeMP4(r io.ReaderAt, size int64) (Metadata, error) {
	m := Metadata{Format: "mp4"}
	found := false

	err := walkBoxes(r, 0, size, func(typ string, off, length int64) error {
		if typ != "moov" {
			return nil
		}
		found = true
		return walkBoxes(r, off, off+length, func(typ string, off, length int64) error {
			switch typ {
			case "mvhd":
				b, err := readBox(r, off, length)
				if err != nil {
					return err
				}
				if d, ok := mvhdDuration(b); ok {
					m.DurationMS = d
				}
			case "udta":
				return walkBoxes(r, off, off+length, func(typ string, off, length int64) error {
					switch typ {
					case "meta":
						// meta is a FullBox: skip its 4 version/flags bytes.
						if length < 4 {
							return nil
						}
						return walkBoxes(r, off+4, off+length, func(typ string, off, length int64) error {
							if typ != "ilst" {
								return nil
							}
							b, err := readBox(r, off, length)
							if err != nil {
								return err
							}
							parseILST(b, &m)
							return nil
						})
					case "chpl":
						b, err := readBox(r, off, length)
						if err != nil {
							return err
						}
						if chapters, ok := parseCHPL(b); ok {
							m.Chapters = chapters
						}
					}
					return nil
				})
			}
			return nil
		})
	})
	if err != nil {
		return m, err
	}
	if !found {
		return m, fmt.Errorf("%w: no moov box", ErrBadMP4)
	}

	for i := range m.Chapters {
		if m.Chapters[i].EndMS == 0 {
			if i+1 < len(m.Chapters) {
				m.Chapters[i].EndMS = m.Chapters[i+1].StartMS
			} else {
				m.Chapters[i].EndMS = m.DurationMS
			}
		}
	}
	return m, nil
}

// walkBoxes calls fn for each box between start and end. off/length passed to
// fn describe the box payload, not its header.
func walkBoxes(r io.ReaderAt, start, end int64, fn func(typ string, off, length int64) error) error {
	for off := start; off+8 <= end; {
		var hdr [8]byte
		if _, err := r.ReadAt(hdr[:], off); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		boxSize := int64(binary.BigEndian.Uint32(hdr[0:4]))
		typ := string(hdr[4:8])
		headerLen := int64(8)
		switch boxSize {
		case 0:
			boxSize = end - off // extends to the end of the parent
		case 1:
			var ext [8]byte
			if _, err := r.ReadAt(ext[:], off+8); err != nil {
				return err
			}
			boxSize = int64(binary.BigEndian.Uint64(ext[:]))
			headerLen = 16
		}
		if boxSize < headerLen || off+boxSize > end {
			return nil // truncated or nonsense: stop rather than guess
		}
		if err := fn(typ, off+headerLen, boxSize-headerLen); err != nil {
			return err
		}
		off += boxSize
	}
	return nil
}

func readBox(r io.ReaderAt, off, length int64) ([]byte, error) {
	if length < 0 || length > maxBoxSize {
		return nil, fmt.Errorf("%w: box of %d bytes", ErrBadMP4, length)
	}
	b := make([]byte, length)
	if _, err := r.ReadAt(b, off); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return b, nil
}

func mvhdDuration(b []byte) (int64, bool) {
	if len(b) < 4 {
		return 0, false
	}
	version := b[0]
	switch version {
	case 0:
		if len(b) < 20 {
			return 0, false
		}
		timescale := int64(binary.BigEndian.Uint32(b[12:16]))
		duration := int64(binary.BigEndian.Uint32(b[16:20]))
		if timescale == 0 {
			return 0, false
		}
		return duration * 1000 / timescale, true
	case 1:
		if len(b) < 32 {
			return 0, false
		}
		timescale := int64(binary.BigEndian.Uint32(b[20:24]))
		duration := int64(binary.BigEndian.Uint64(b[24:32]))
		if timescale == 0 {
			return 0, false
		}
		return duration * 1000 / timescale, true
	}
	return 0, false
}

// parseILST walks the iTunes-style metadata list. Each child box is named by
// its tag and holds a "data" box with the value.
func parseILST(b []byte, m *Metadata) {
	for off := 0; off+8 <= len(b); {
		boxSize := int(binary.BigEndian.Uint32(b[off : off+4]))
		typ := string(b[off+4 : off+8])
		if boxSize < 8 || off+boxSize > len(b) {
			return
		}
		payload := b[off+8 : off+boxSize]
		if typ == "----" {
			name, value := parseFreeform(payload)
			applyFreeform(name, value, m)
		} else if value, dataType, ok := ilstData(payload); ok {
			applyILST(typ, value, dataType, m)
		}
		off += boxSize
	}
}

// ilstData returns the payload of the first "data" child box.
func ilstData(b []byte) ([]byte, uint32, bool) {
	for off := 0; off+8 <= len(b); {
		boxSize := int(binary.BigEndian.Uint32(b[off : off+4]))
		typ := string(b[off+4 : off+8])
		if boxSize < 8 || off+boxSize > len(b) {
			return nil, 0, false
		}
		if typ == "data" && boxSize >= 16 {
			dataType := binary.BigEndian.Uint32(b[off+8:off+12]) & 0x00FFFFFF
			return b[off+16 : off+boxSize], dataType, true
		}
		off += boxSize
	}
	return nil, 0, false
}

func parseFreeform(b []byte) (string, string) {
	var name string
	var value string
	for off := 0; off+8 <= len(b); {
		boxSize := int(binary.BigEndian.Uint32(b[off : off+4]))
		typ := string(b[off+4 : off+8])
		if boxSize < 8 || off+boxSize > len(b) {
			break
		}
		payload := b[off+8 : off+boxSize]
		switch typ {
		case "name":
			if len(payload) > 4 {
				name = clampString(string(payload[4:]))
			}
		case "data":
			if len(payload) > 8 {
				value = clampString(string(payload[8:]))
			}
		}
		off += boxSize
	}
	return name, value
}

func applyFreeform(name, value string, m *Metadata) {
	if value == "" {
		return
	}
	switch strings.ToUpper(name) {
	case "ASIN":
		m.ASIN = value
	case "ISBN":
		m.ISBN = value
	case "NARRATOR", "NARRATED BY":
		m.Narrator = value
	case "SUBTITLE":
		m.Subtitle = value
	case "PUBLISHER":
		if m.Publisher == "" {
			m.Publisher = value
		}
	}
}

func applyILST(typ string, value []byte, dataType uint32, m *Metadata) {
	text := clampString(string(value))
	switch typ {
	case "\xa9nam":
		m.Title = text
	case "\xa9alb":
		m.Album = text
	case "\xa9ART":
		m.Artist = text
	case "aART":
		m.AlbumArtist = text
	case "\xa9wrt":
		m.Composer = text
	case "\xa9nrt":
		m.Narrator = text
	case "\xa9day":
		m.Date = text
	case "\xa9gen":
		if text != "" {
			m.Genres = append(m.Genres, text)
		}
	case "desc", "ldes", "\xa9des":
		if m.Description == "" || typ == "ldes" {
			m.Description = text
		}
	case "\xa9cmt":
		if m.Description == "" {
			m.Description = text
		}
	case "\xa9pub", "prID":
		if m.Publisher == "" {
			m.Publisher = text
		}
	case "\xa9lan":
		m.Language = text
	case "trkn":
		if len(value) >= 6 {
			m.Track = int(binary.BigEndian.Uint16(value[2:4]))
			m.TrackTotal = int(binary.BigEndian.Uint16(value[4:6]))
		}
	case "disk":
		if len(value) >= 6 {
			m.Disc = int(binary.BigEndian.Uint16(value[2:4]))
			m.DiscTotal = int(binary.BigEndian.Uint16(value[4:6]))
		}
	case "covr":
		if m.Cover == nil && len(value) > 0 {
			m.Cover = append([]byte(nil), value...)
			m.CoverType = "image/jpeg"
			if dataType == 14 || (len(value) > 8 && string(value[1:4]) == "PNG") {
				m.CoverType = "image/png"
			}
		}
	}
}

// parseCHPL reads a Nero-style chapter list. Start times are in 100 ns units.
func parseCHPL(b []byte) ([]Chapter, bool) {
	if len(b) < 5 {
		return nil, false
	}
	version := b[0]
	off := 4 // version + flags
	if version >= 1 {
		off += 4 // reserved
	}
	if off >= len(b) {
		return nil, false
	}
	count := int(b[off])
	off++

	chapters := make([]Chapter, 0, count)
	for i := 0; i < count; i++ {
		if off+9 > len(b) {
			break
		}
		start := binary.BigEndian.Uint64(b[off : off+8])
		off += 8
		titleLen := int(b[off])
		off++
		if off+titleLen > len(b) {
			break
		}
		title := clampString(string(b[off : off+titleLen]))
		off += titleLen
		chapters = append(chapters, Chapter{Title: title, StartMS: int64(start / 10000)})
	}
	if len(chapters) == 0 {
		return nil, false
	}
	return chapters, true
}
