package audio

import (
	"encoding/binary"
	"io"
)

// frameHeader is a decoded MPEG audio frame header.
type frameHeader struct {
	versionID  int // 3 = MPEG1, 2 = MPEG2, 0 = MPEG2.5
	layer      int // 1, 2 or 3
	bitrate    int // bits per second
	sampleRate int
	padding    int
	channels   int
	frameLen   int
	samples    int
}

var bitrateTable = map[[2]int][15]int{
	{3, 1}: {0, 32, 64, 96, 128, 160, 192, 224, 256, 288, 320, 352, 384, 416, 448},
	{3, 2}: {0, 32, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 384},
	{3, 3}: {0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320},
	{2, 1}: {0, 32, 48, 56, 64, 80, 96, 112, 128, 144, 160, 176, 192, 224, 256},
	{2, 2}: {0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160},
	{2, 3}: {0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160},
}

var sampleRateTable = map[int][3]int{
	3: {44100, 48000, 32000},
	2: {22050, 24000, 16000},
	0: {11025, 12000, 8000},
}

// parseFrameHeader decodes the 4-byte header at b, or reports false when the
// bytes are not a valid frame header.
func parseFrameHeader(b []byte) (frameHeader, bool) {
	if len(b) < 4 || b[0] != 0xFF || b[1]&0xE0 != 0xE0 {
		return frameHeader{}, false
	}
	h := frameHeader{}
	h.versionID = int(b[1] >> 3 & 0x03)
	if h.versionID == 1 { // reserved
		return frameHeader{}, false
	}
	layerBits := int(b[1] >> 1 & 0x03)
	if layerBits == 0 { // reserved
		return frameHeader{}, false
	}
	h.layer = 4 - layerBits

	bitrateIdx := int(b[2] >> 4 & 0x0F)
	sampleIdx := int(b[2] >> 2 & 0x03)
	if bitrateIdx == 0 || bitrateIdx == 15 || sampleIdx == 3 {
		return frameHeader{}, false
	}
	tableKey := [2]int{2, h.layer}
	if h.versionID == 3 {
		tableKey[0] = 3
	}
	rates, ok := bitrateTable[tableKey]
	if !ok {
		return frameHeader{}, false
	}
	h.bitrate = rates[bitrateIdx] * 1000
	h.sampleRate = sampleRateTable[h.versionID][sampleIdx]
	if h.bitrate == 0 || h.sampleRate == 0 {
		return frameHeader{}, false
	}
	h.padding = int(b[2] >> 1 & 0x01)
	if int(b[3]>>6&0x03) == 3 {
		h.channels = 1
	} else {
		h.channels = 2
	}

	switch {
	case h.layer == 1:
		h.samples = 384
		h.frameLen = (12*h.bitrate/h.sampleRate + h.padding) * 4
	case h.layer == 2:
		h.samples = 1152
		h.frameLen = 144*h.bitrate/h.sampleRate + h.padding
	default: // layer 3
		if h.versionID == 3 {
			h.samples = 1152
			h.frameLen = 144*h.bitrate/h.sampleRate + h.padding
		} else {
			h.samples = 576
			h.frameLen = 72*h.bitrate/h.sampleRate + h.padding
		}
	}
	if h.frameLen < 4 {
		return frameHeader{}, false
	}
	return h, true
}

// sideInfoLen is the distance from the end of the frame header to the Xing tag.
func (h frameHeader) sideInfoLen() int {
	if h.versionID == 3 { // MPEG1
		if h.channels == 1 {
			return 17
		}
		return 32
	}
	if h.channels == 1 {
		return 9
	}
	return 17
}

// mp3Duration returns the stream duration in milliseconds. A Xing/Info or VBRI
// header gives an exact frame count for VBR streams; otherwise the CBR bitrate
// of the first frame is applied to the remaining bytes.
func mp3Duration(r io.ReaderAt, start, end int64) (int64, error) {
	const searchWindow = 512 << 10
	buf := make([]byte, min64(searchWindow, end-start))
	if len(buf) < 4 {
		return 0, ErrNoFrame
	}
	n, err := r.ReadAt(buf, start)
	if err != nil && n < 4 {
		return 0, ErrNoFrame
	}
	buf = buf[:n]

	for i := 0; i+4 <= len(buf); i++ {
		h, ok := parseFrameHeader(buf[i:])
		if !ok {
			continue
		}
		// Confirm with a second header where the first one says it should be,
		// so that tag bytes resembling a sync word are not mistaken for audio.
		next := i + h.frameLen
		if next+4 <= len(buf) {
			if _, ok := parseFrameHeader(buf[next:]); !ok {
				if !hasXing(buf[i:], h) {
					continue
				}
			}
		}
		if frames, ok := vbrFrameCount(buf[i:], h); ok {
			return int64(frames) * int64(h.samples) * 1000 / int64(h.sampleRate), nil
		}
		audioBytes := end - (start + int64(i))
		if audioBytes <= 0 {
			return 0, nil
		}
		return audioBytes * 8 * 1000 / int64(h.bitrate), nil
	}
	return 0, ErrNoFrame
}

func hasXing(frame []byte, h frameHeader) bool {
	off := 4 + h.sideInfoLen()
	if off+4 > len(frame) {
		return false
	}
	tag := string(frame[off : off+4])
	return tag == "Xing" || tag == "Info"
}

// vbrFrameCount reads the frame count from a Xing/Info or VBRI header.
func vbrFrameCount(frame []byte, h frameHeader) (uint32, bool) {
	off := 4 + h.sideInfoLen()
	if off+12 <= len(frame) {
		if tag := string(frame[off : off+4]); tag == "Xing" || tag == "Info" {
			flags := binary.BigEndian.Uint32(frame[off+4 : off+8])
			if flags&0x01 != 0 {
				return binary.BigEndian.Uint32(frame[off+8 : off+12]), true
			}
		}
	}
	// VBRI sits at a fixed offset from the frame header instead.
	if 4+32+18 <= len(frame) && string(frame[36:40]) == "VBRI" {
		return binary.BigEndian.Uint32(frame[50:54]), true
	}
	return 0, false
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
