// Package images handles cover artwork: decoding untrusted image bytes under a
// pixel and time budget, re-encoding them as bounded JPEGs, and an optional
// on-disk cache.
//
// The artwork itself lives in the database. The cache here is a pure
// optimisation and may be switched off entirely - NewStore("") returns a store
// that reports itself disabled and never touches the filesystem - so a
// deployment can run with no writable volume.
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
	FullMaxDim    = 1600
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

// Variants of a cover. Both are JPEG; the thumbnail is what a grid of covers
// loads, the full size is what the reader and the detail page show.
const (
	VariantThumb = "thumb"
	VariantFull  = "full"
)

// MaxDim returns the pixel bound for a variant.
func MaxDim(variant string) int {
	if variant == VariantThumb {
		return ThumbMaxDim
	}
	return FullMaxDim
}

// Variant normalises an arbitrary size parameter to one of the two variants.
func Variant(s string) string {
	if s == VariantThumb {
		return VariantThumb
	}
	return VariantFull
}

// Store is the optional on-disk cover cache under the data directory. A store
// built from an empty directory is disabled: every method is a no-op and no
// path is ever created.
type Store struct{ dir string }

// NewStore creates the cache directory if needed. An empty dir yields a
// disabled store rather than an error, because running without a local data
// directory is a supported configuration and not a misconfiguration.
func NewStore(dir string) (*Store, error) {
	if dir == "" {
		return &Store{}, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("images: create cover cache %s: %w", dir, err)
	}
	return &Store{dir: dir}, nil
}

// Enabled reports whether this store caches anything.
func (s *Store) Enabled() bool { return s != nil && s.dir != "" }

// Dir returns the cache root, or "" when the cache is off.
func (s *Store) Dir() string {
	if s == nil {
		return ""
	}
	return s.dir
}

// Path is the cache location for one item's cover variant. Paths are derived
// from the numeric id only, so nothing from a book's metadata can influence
// where bytes land.
func (s *Store) Path(itemID int64, variant string) string {
	if !s.Enabled() {
		return ""
	}
	return filepath.Join(s.dir, strconv.FormatInt(itemID, 10)+"-"+Variant(variant)+".jpg")
}

// Read returns a cached variant and its modification time. The boolean is
// false when the cache is off or the file is not there, which is not an error:
// the caller falls back to the database.
func (s *Store) Read(itemID int64, variant string) ([]byte, time.Time, bool) {
	if !s.Enabled() {
		return nil, time.Time{}, false
	}
	path := s.Path(itemID, variant)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, time.Time{}, false
	}
	var mod time.Time
	if info, err := os.Stat(path); err == nil {
		mod = info.ModTime()
	}
	return data, mod, true
}

// Put writes already-encoded JPEG bytes into the cache. A failure is reported
// so the caller can log it, but it never invalidates the write that already
// went to the database.
func (s *Store) Put(itemID int64, variant string, jpeg []byte) error {
	if !s.Enabled() {
		return nil
	}
	return os.WriteFile(s.Path(itemID, variant), jpeg, 0o644)
}

// Remove deletes any cached covers for an item.
func (s *Store) Remove(itemID int64) {
	if !s.Enabled() {
		return
	}
	for _, variant := range []string{VariantFull, VariantThumb} {
		_ = os.Remove(s.Path(itemID, variant))
	}
}
