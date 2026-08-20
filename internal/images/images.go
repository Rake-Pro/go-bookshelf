// Package images handles cover artwork: decoding untrusted image bytes under a
// pixel and time budget, producing thumbnails, and caching both on disk.
package images

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"golang.org/x/image/draw"

	_ "image/gif"  // decoders for the formats found in book covers
	_ "image/jpeg" // (registered for image.Decode)
	_ "image/png"
)

// Limits applied to every decode of untrusted image data.
const (
	MaxPixels     = 100_000_000 // 100 MP
	MaxSourceSize = 32 << 20    // 32 MiB of encoded input
	DecodeTimeout = 10 * time.Second
	ThumbMaxDim   = 400
	FullMaxDim    = 1400
	jpegQuality   = 82
)

// Errors returned for input that exceeds the limits.
var (
	ErrTooLarge      = errors.New("images: source exceeds the size limit")
	ErrTooManyPixels = errors.New("images: image exceeds the pixel limit")
	ErrTimeout       = errors.New("images: decode timed out")
)

// Decode decodes untrusted image bytes, rejecting anything above the byte or
// pixel budget and giving up after DecodeTimeout.
func Decode(ctx context.Context, src []byte) (image.Image, error) {
	if len(src) > MaxSourceSize {
		return nil, fmt.Errorf("%w: %d bytes", ErrTooLarge, len(src))
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(src))
	if err != nil {
		return nil, fmt.Errorf("images: decode header: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > MaxPixels {
		return nil, fmt.Errorf("%w: %dx%d", ErrTooManyPixels, cfg.Width, cfg.Height)
	}

	ctx, cancel := context.WithTimeout(ctx, DecodeTimeout)
	defer cancel()

	type result struct {
		img image.Image
		err error
	}
	done := make(chan result, 1)
	go func() {
		img, _, err := image.Decode(bytes.NewReader(src))
		done <- result{img, err}
	}()
	select {
	case <-ctx.Done():
		return nil, ErrTimeout
	case r := <-done:
		if r.err != nil {
			return nil, fmt.Errorf("images: decode: %w", r.err)
		}
		return r.img, nil
	}
}

// Resize scales img so neither side exceeds maxDim, preserving aspect ratio.
// Images already within the bound are returned unchanged.
func Resize(img image.Image, maxDim int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if maxDim <= 0 || (w <= maxDim && h <= maxDim) {
		return img
	}
	nw, nh := w, h
	if w >= h {
		nw = maxDim
		nh = h * maxDim / w
	} else {
		nh = maxDim
		nw = w * maxDim / h
	}
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Over, nil)
	return dst
}

// EncodeJPEG encodes an image as JPEG.
func EncodeJPEG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: jpegQuality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Convert decodes src and re-encodes it as a JPEG no larger than maxDim on its
// longest side.
func Convert(ctx context.Context, src []byte, maxDim int) ([]byte, error) {
	img, err := Decode(ctx, src)
	if err != nil {
		return nil, err
	}
	return EncodeJPEG(Resize(img, maxDim))
}

// Store is the on-disk cover cache under the data directory.
type Store struct{ dir string }

// NewStore creates the cache directory if needed.
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("images: create cover cache %s: %w", dir, err)
	}
	return &Store{dir: dir}, nil
}

// Dir returns the cache root.
func (s *Store) Dir() string { return s.dir }

// Path is the cache location for one item's cover at a size ("full"/"thumb").
// Paths are derived from the numeric id only, so nothing from a book's
// metadata can influence where bytes land.
func (s *Store) Path(itemID int64, size string) string {
	if size != "thumb" {
		size = "full"
	}
	return filepath.Join(s.dir, strconv.FormatInt(itemID, 10)+"-"+size+".jpg")
}

// Save re-encodes raw cover bytes into the cache at the given size and returns
// the file path.
func (s *Store) Save(ctx context.Context, itemID int64, size string, raw []byte) (string, error) {
	maxDim := FullMaxDim
	if size == "thumb" {
		maxDim = ThumbMaxDim
	}
	encoded, err := Convert(ctx, raw, maxDim)
	if err != nil {
		return "", err
	}
	path := s.Path(itemID, size)
	if err := os.WriteFile(path, encoded, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// Remove deletes any cached covers for an item.
func (s *Store) Remove(itemID int64) {
	for _, size := range []string{"full", "thumb"} {
		_ = os.Remove(s.Path(itemID, size))
	}
}
