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

func TestStoreSavesBothSizes(t *testing.T) {
	dir := t.TempDir()
	store, err := images.NewStore(filepath.Join(dir, "covers"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	src := fixtures.PNG(900, 1200, color.RGBA{G: 200, A: 255})

	full, err := store.Save(context.Background(), 42, "full", src)
	if err != nil {
		t.Fatalf("save full: %v", err)
	}
	thumb, err := store.Save(context.Background(), 42, "thumb", src)
	if err != nil {
		t.Fatalf("save thumb: %v", err)
	}
	if full == thumb {
		t.Fatal("both sizes were written to the same path")
	}

	thumbBytes, err := os.ReadFile(thumb)
	if err != nil {
		t.Fatal(err)
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

	store.Remove(42)
	if _, err := os.Stat(full); err == nil {
		t.Error("Remove left the full-size cover behind")
	}
}
