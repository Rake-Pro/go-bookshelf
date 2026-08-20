package images_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"testing"

	"github.com/rake-pro/go-bookshelf/internal/fixtures"
	"github.com/rake-pro/go-bookshelf/internal/images"
)

func TestDecodeAndResize(t *testing.T) {
	src := fixtures.PNG(1200, 1800, color.RGBA{R: 10, G: 90, B: 200, A: 255})
	img, err := images.Decode(context.Background(), src)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if img.Bounds().Dx() != 1200 {
		t.Fatalf("width = %d", img.Bounds().Dx())
	}

	thumb := images.Resize(img, images.ThumbMaxDim)
	if thumb.Bounds().Dy() != images.ThumbMaxDim {
		t.Errorf("thumbnail height = %d, want %d", thumb.Bounds().Dy(), images.ThumbMaxDim)
	}
	// Aspect ratio is preserved.
	if got, want := thumb.Bounds().Dx(), 1200*images.ThumbMaxDim/1800; got != want {
		t.Errorf("thumbnail width = %d, want %d", got, want)
	}
	// An image already inside the bound is untouched.
	small := images.Resize(img, 4000)
	if small.Bounds() != img.Bounds() {
		t.Error("an image below the limit should not be rescaled")
	}
}

func TestDecodeRejectsOversizedInput(t *testing.T) {
	if _, err := images.Decode(context.Background(), make([]byte, images.MaxSourceSize+1)); !errors.Is(err, images.ErrTooLarge) {
		t.Errorf("oversized source = %v, want ErrTooLarge", err)
	}
	if _, err := images.Decode(context.Background(), []byte("not an image")); err == nil {
		t.Error("garbage input should not decode")
	}
}

// A crafted header claiming an enormous canvas is refused before any pixels
// are allocated.
func TestDecodeRejectsPixelBomb(t *testing.T) {
	src := fixtures.PNG(8, 8, color.Black)
	header := bytes.Clone(src)
	// The IHDR chunk starts at offset 8: length, type, then width and height
	// at offsets 16 and 20, followed by the chunk CRC.
	binary.BigEndian.PutUint32(header[16:20], 1<<20)
	binary.BigEndian.PutUint32(header[20:24], 1<<20)
	binary.BigEndian.PutUint32(header[29:33], crc32.ChecksumIEEE(header[12:29]))

	if _, err := images.Decode(context.Background(), header); !errors.Is(err, images.ErrTooManyPixels) {
		t.Errorf("pixel bomb = %v, want ErrTooManyPixels", err)
	}
	if images.MaxPixels != 100_000_000 {
		t.Errorf("pixel cap = %d, want 100 MP", images.MaxPixels)
	}
}

func TestCacheHoldsBothVariants(t *testing.T) {
	dir := t.TempDir()
	cache, err := images.NewStore(filepath.Join(dir, "covers"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if !cache.Enabled() {
		t.Fatal("a store built from a real directory must be enabled")
	}
	src := fixtures.PNG(900, 1200, color.RGBA{G: 200, A: 255})

	for _, variant := range []string{images.VariantFull, images.VariantThumb} {
		encoded, err := images.Convert(context.Background(), src, images.MaxDim(variant))
		if err != nil {
			t.Fatalf("convert %s: %v", variant, err)
		}
		if err := cache.Put(42, variant, encoded); err != nil {
			t.Fatalf("put %s: %v", variant, err)
		}
	}
	if cache.Path(42, images.VariantFull) == cache.Path(42, images.VariantThumb) {
		t.Fatal("both variants were written to the same path")
	}

	thumbBytes, _, ok := cache.Read(42, images.VariantThumb)
	if !ok {
		t.Fatal("the thumbnail was not readable back")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(thumbBytes))
	if err != nil {
		t.Fatalf("decode thumb: %v", err)
	}
	if format != "jpeg" {
		t.Errorf("cached cover format = %q, want jpeg", format)
	}
	if cfg.Height > images.ThumbMaxDim {
		t.Errorf("thumb height = %d, want at most %d", cfg.Height, images.ThumbMaxDim)
	}

	cache.Remove(42)
	if _, err := os.Stat(cache.Path(42, images.VariantFull)); err == nil {
		t.Error("Remove left the full-size cover behind")
	}
}

// Without a data directory the cache is inert: nothing is written, nothing is
// read back, and no method panics. This is the configuration a deployment with
// no local volume runs in.
func TestDisabledCacheWritesNothing(t *testing.T) {
	cache, err := images.NewStore("")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if cache.Enabled() {
		t.Fatal("a store built from an empty directory must be disabled")
	}
	if cache.Dir() != "" || cache.Path(1, images.VariantFull) != "" {
		t.Errorf("a disabled cache named a path: dir=%q path=%q", cache.Dir(), cache.Path(1, images.VariantFull))
	}
	if err := cache.Put(1, images.VariantFull, []byte("data")); err != nil {
		t.Errorf("put on a disabled cache = %v, want nil", err)
	}
	if _, _, ok := cache.Read(1, images.VariantFull); ok {
		t.Error("a disabled cache reported a hit")
	}
	cache.Remove(1)
}

func TestVariantAndMaxDim(t *testing.T) {
	if got := images.Variant("thumb"); got != images.VariantThumb {
		t.Errorf("Variant(thumb) = %q", got)
	}
	for _, in := range []string{"", "full", "nonsense", "THUMB"} {
		if got := images.Variant(in); got != images.VariantFull {
			t.Errorf("Variant(%q) = %q, want full", in, got)
		}
	}
	if images.MaxDim(images.VariantThumb) != images.ThumbMaxDim ||
		images.MaxDim(images.VariantFull) != images.FullMaxDim {
		t.Error("MaxDim does not follow the variant")
	}
	if images.ThumbMaxDim > 400 || images.FullMaxDim > 1600 {
		t.Errorf("cover bounds grew: thumb=%d full=%d", images.ThumbMaxDim, images.FullMaxDim)
	}
}
