package fixtures

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
)

// MP3Chapter is a chapter marker written as an ID3v2 CHAP frame.
type MP3Chapter struct {
	Title   string
	StartMS uint32
	EndMS   uint32
}

// MP3Options describes the MPEG audio file to generate. The audio itself is
// silence: valid MPEG-1 Layer III frame headers followed by zero bytes, which
// is all the duration and tag readers look at.
type MP3Options struct {
	Title       string
	Artist      string
	AlbumArtist string
	Album       string
	Comment     string
	Date        string
	Publisher   string
	Narrator    string
	Genre       string
	Track       int
	TrackTotal  int
	Disc        int
	DiscTotal   int
	Cover       []byte
	CoverMIME   string
	Chapters    []MP3Chapter
	// Frames is the number of MPEG frames to emit. Each frame is 1152 samples
	// at 44100 Hz, so roughly 26.12 ms.
	Frames int
	// Xing adds a Xing VBR header to the first frame declaring FrameCount.
	Xing       bool
	FrameCount uint32
}

// MP3FrameSamples and MP3SampleRate describe the generated stream.
const (
	MP3FrameSamples = 1152
	MP3SampleRate   = 44100
	mp3FrameLen     = 417 // 144 * 128000 / 44100, no padding
)

// MP3Bytes builds a complete MP3 file: an ID3v2.3 tag then MPEG frames.
func MP3Bytes(o MP3Options) []byte {
	if o.Frames <= 0 {
		o.Frames = 100
	}
	var tag bytes.Buffer
	textFrame := func(id, value string) {
		if value == "" {
			return
		}
		writeID3Frame(&tag, id, append([]byte{0x03}, []byte(value)...))
	}
	textFrame("TIT2", o.Title)
	textFrame("TPE1", o.Artist)
	textFrame("TPE2", o.AlbumArtist)
	textFrame("TPE3", o.Narrator)
	textFrame("TALB", o.Album)
	textFrame("TCON", o.Genre)
	textFrame("TPUB", o.Publisher)
	textFrame("TDRC", o.Date)
	if o.Track > 0 {
		textFrame("TRCK", pairString(o.Track, o.TrackTotal))
	}
	if o.Disc > 0 {
		textFrame("TPOS", pairString(o.Disc, o.DiscTotal))
	}
	if o.Comment != "" {
		body := []byte{0x03, 'e', 'n', 'g', 0x00}
		body = append(body, []byte(o.Comment)...)
		writeID3Frame(&tag, "COMM", body)
	}
	if len(o.Cover) > 0 {
		mimeType := o.CoverMIME
		if mimeType == "" {
			mimeType = "image/png"
		}
		body := []byte{0x03}
		body = append(body, []byte(mimeType)...)
		body = append(body, 0x00, 0x03, 0x00) // picture type 3 (front cover), empty description
		body = append(body, o.Cover...)
		writeID3Frame(&tag, "APIC", body)
	}
	for i, ch := range o.Chapters {
		var body bytes.Buffer
		body.WriteString("ch" + string(rune('0'+i%10)))
		body.WriteByte(0x00)
		body.Write(be32(ch.StartMS))
		body.Write(be32(ch.EndMS))
		body.Write(be32(0xFFFFFFFF))
		body.Write(be32(0xFFFFFFFF))
		var sub bytes.Buffer
		writeID3Frame(&sub, "TIT2", append([]byte{0x03}, []byte(ch.Title)...))
		body.Write(sub.Bytes())
		writeID3Frame(&tag, "CHAP", body.Bytes())
	}

	var out bytes.Buffer
	out.WriteString("ID3")
	out.Write([]byte{0x03, 0x00, 0x00})
	out.Write(syncsafeBytes(uint32(tag.Len())))
	out.Write(tag.Bytes())

	for i := 0; i < o.Frames; i++ {
		frame := make([]byte, mp3FrameLen)
		copy(frame, []byte{0xFF, 0xFB, 0x90, 0x00})
		if i == 0 && o.Xing {
			copy(frame[36:], []byte("Xing"))
			copy(frame[40:], be32(0x00000001)) // frames field present
			copy(frame[44:], be32(o.FrameCount))
		}
		out.Write(frame)
	}
	return out.Bytes()
}

// WriteMP3 writes a generated MP3 to path.
func WriteMP3(path string, o MP3Options) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, MP3Bytes(o), 0o644)
}

func writeID3Frame(buf *bytes.Buffer, id string, body []byte) {
	buf.WriteString(id)
	buf.Write(be32(uint32(len(body))))
	buf.Write([]byte{0x00, 0x00})
	buf.Write(body)
}

func syncsafeBytes(v uint32) []byte {
	return []byte{
		byte(v >> 21 & 0x7f), byte(v >> 14 & 0x7f), byte(v >> 7 & 0x7f), byte(v & 0x7f),
	}
}

func pairString(a, b int) string {
	s := itoa(a)
	if b > 0 {
		s += "/" + itoa(b)
	}
	return s
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var digits []byte
	for v > 0 {
		digits = append([]byte{byte('0' + v%10)}, digits...)
		v /= 10
	}
	return string(digits)
}

// M4BChapter is a Nero-style chapter entry.
type M4BChapter struct {
	Title   string
	StartMS uint64
}

// M4BOptions describes the MP4 audiobook to generate.
type M4BOptions struct {
	Title       string
	Album       string
	Artist      string
	AlbumArtist string
	Narrator    string
	Description string
	Date        string
	Genre       string
	Publisher   string
	ASIN        string
	Track       int
	TrackTotal  int
	Disc        int
	DiscTotal   int
	DurationMS  uint64
	Chapters    []M4BChapter
	Cover       []byte
	CoverIsPNG  bool
}

// M4BBytes builds an MP4 audiobook container: ftyp, a moov holding mvhd,
// udta/meta/ilst tags and a udta/chpl chapter list, and an empty mdat.
func M4BBytes(o M4BOptions) []byte {
	if o.DurationMS == 0 {
		o.DurationMS = 60000
	}

	var ilst bytes.Buffer
	text := func(typ, value string) {
		if value == "" {
			return
		}
		ilst.Write(box(typ, dataBox(1, []byte(value))))
	}
	text("\xa9nam", o.Title)
	text("\xa9alb", o.Album)
	text("\xa9ART", o.Artist)
	text("aART", o.AlbumArtist)
	text("\xa9nrt", o.Narrator)
	text("desc", o.Description)
	text("\xa9day", o.Date)
	text("\xa9gen", o.Genre)
	text("\xa9pub", o.Publisher)
	if o.Track > 0 {
		payload := make([]byte, 8)
		binary.BigEndian.PutUint16(payload[2:4], uint16(o.Track))
		binary.BigEndian.PutUint16(payload[4:6], uint16(o.TrackTotal))
		ilst.Write(box("trkn", dataBox(0, payload)))
	}
	if o.Disc > 0 {
		payload := make([]byte, 8)
		binary.BigEndian.PutUint16(payload[2:4], uint16(o.Disc))
		binary.BigEndian.PutUint16(payload[4:6], uint16(o.DiscTotal))
		ilst.Write(box("disk", dataBox(0, payload)))
	}
	if len(o.Cover) > 0 {
		kind := uint32(13) // jpeg
		if o.CoverIsPNG {
			kind = 14
		}
		ilst.Write(box("covr", dataBox(kind, o.Cover)))
	}
	if o.ASIN != "" {
		ilst.Write(box("----", freeformBox("ASIN", o.ASIN)))
	}

	meta := append([]byte{0, 0, 0, 0}, box("hdlr", append([]byte{0, 0, 0, 0}, []byte("\x00\x00\x00\x00mdirappl\x00\x00\x00\x00\x00\x00\x00\x00\x00")...))...)
	meta = append(meta, box("ilst", ilst.Bytes())...)

	udta := box("meta", meta)
	if len(o.Chapters) > 0 {
		var chpl bytes.Buffer
		chpl.Write([]byte{0x01, 0x00, 0x00, 0x00}) // version 1, flags
		chpl.Write(be32(0))                        // reserved
		chpl.WriteByte(byte(len(o.Chapters)))
		for _, ch := range o.Chapters {
			chpl.Write(be64(ch.StartMS * 10000)) // 100 ns units
			title := ch.Title
			if len(title) > 255 {
				title = title[:255]
			}
			chpl.WriteByte(byte(len(title)))
			chpl.WriteString(title)
		}
		udta = append(udta, box("chpl", chpl.Bytes())...)
	}

	mvhd := make([]byte, 100)
	binary.BigEndian.PutUint32(mvhd[12:16], 1000) // timescale: milliseconds
	binary.BigEndian.PutUint32(mvhd[16:20], uint32(o.DurationMS))

	moov := append(box("mvhd", mvhd), box("udta", udta)...)

	var out bytes.Buffer
	out.Write(box("ftyp", []byte("M4B \x00\x00\x00\x00M4B mp42isom")))
	out.Write(box("moov", moov))
	out.Write(box("mdat", make([]byte, 64)))
	return out.Bytes()
}

// WriteM4B writes a generated M4B to path.
func WriteM4B(path string, o M4BOptions) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, M4BBytes(o), 0o644)
}

func box(typ string, payload []byte) []byte {
	out := make([]byte, 0, 8+len(payload))
	out = append(out, be32(uint32(8+len(payload)))...)
	out = append(out, []byte(typ)...)
	return append(out, payload...)
}

func dataBox(kind uint32, payload []byte) []byte {
	body := make([]byte, 0, 8+len(payload))
	body = append(body, be32(kind)...)
	body = append(body, be32(0)...) // locale
	body = append(body, payload...)
	return box("data", body)
}

func freeformBox(name, value string) []byte {
	out := box("mean", append(be32(0), []byte("com.apple.iTunes")...))
	out = append(out, box("name", append(be32(0), []byte(name)...))...)
	out = append(out, box("data", append(append(be32(1), be32(0)...), []byte(value)...))...)
	return out
}
