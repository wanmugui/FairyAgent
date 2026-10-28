package main

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestVisionCacheServesSecondEncodeWithoutRework(t *testing.T) {
	visionEncodings.clear()
	path := writeTestPNG(t, 400, 300)

	firstURL, firstHash, firstW, firstH, err := encodeImageAttachmentDataURL(path, 1600, 82)
	if err != nil {
		t.Fatal(err)
	}
	if firstURL == "" || firstHash == "" {
		t.Fatalf("first encode must produce a data URL and hash: %q %q", firstURL, firstHash)
	}
	if firstW != 400 || firstH != 300 {
		t.Fatalf("unexpected dimensions: %dx%d", firstW, firstH)
	}

	entries, _ := visionEncodings.stats()
	if entries != 1 {
		t.Fatalf("the first encode must be cached, entries=%d", entries)
	}

	// The second call must return the identical result and come from cache.
	secondURL, secondHash, secondW, secondH, err := encodeImageAttachmentDataURL(path, 1600, 82)
	if err != nil {
		t.Fatal(err)
	}
	if secondURL != firstURL || secondHash != firstHash {
		t.Fatal("the cached encode must be byte-identical to the first")
	}
	if secondW != firstW || secondH != firstH {
		t.Fatal("cached dimensions must match")
	}
	if entries, _ := visionEncodings.stats(); entries != 1 {
		t.Fatalf("the second encode must reuse the entry, entries=%d", entries)
	}
}

func TestVisionCacheDistinguishesParameters(t *testing.T) {
	visionEncodings.clear()
	path := writeTestPNG(t, 200, 200)

	small, _, smallW, _, err := encodeImageAttachmentDataURL(path, 100, 82)
	if err != nil {
		t.Fatal(err)
	}
	large, _, largeW, _, err := encodeImageAttachmentDataURL(path, 1600, 82)
	if err != nil {
		t.Fatal(err)
	}
	if small == large {
		t.Fatal("a different maxDimension must produce a different encode")
	}
	if smallW != 100 {
		t.Fatalf("small encode should be scaled to 100px, got %d", smallW)
	}
	if largeW != 200 {
		t.Fatalf("large encode should keep the source width, got %d", largeW)
	}
	if entries, _ := visionEncodings.stats(); entries != 2 {
		t.Fatalf("both parameter sets must be cached separately, entries=%d", entries)
	}
}

func TestVisionCacheInvalidatesOnFileChange(t *testing.T) {
	visionEncodings.clear()
	path := writeTestPNG(t, 120, 120)

	firstURL, firstHash, _, _, err := encodeImageAttachmentDataURL(path, 1600, 82)
	if err != nil {
		t.Fatal(err)
	}

	// Replace the file with different contents and a newer mtime. The
	// pattern has to differ, otherwise the hash would be identical and the
	// test would pass for the wrong reason.
	time.Sleep(10 * time.Millisecond)
	img := image.NewRGBA(image.Rect(0, 0, 120, 120))
	for y := 0; y < 120; y++ {
		for x := 0; x < 120; x++ {
			img.Set(x, y, color.RGBA{R: 200, G: uint8(y), B: 40, A: 255})
		}
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(file, img); err != nil {
		t.Fatal(err)
	}
	file.Close()

	secondURL, secondHash, _, _, err := encodeImageAttachmentDataURL(path, 1600, 82)
	if err != nil {
		t.Fatal(err)
	}
	if secondHash == firstHash {
		t.Fatal("a changed file must not serve the stale hash")
	}
	if secondURL == firstURL {
		t.Fatal("a changed file must not serve the stale data URL")
	}
}

func TestVisionCacheEvictsLeastRecentlyUsed(t *testing.T) {
	cache := newVisionCache(2, 1<<20)

	pathA := writeTestPNG(t, 50, 50)
	pathB := writeTestPNG(t, 50, 50)
	pathC := writeTestPNG(t, 50, 50)

	entry := func() *visionCacheEntry {
		return &visionCacheEntry{dataURL: "data:image/jpeg;base64,AAAA", hash: "h"}
	}
	cache.put(visionCacheKey(pathA, 1600, 82), pathA, entry())
	cache.put(visionCacheKey(pathB, 1600, 82), pathB, entry())

	// Touch A so B becomes the least recently used.
	if _, ok := cache.get(visionCacheKey(pathA, 1600, 82), pathA); !ok {
		t.Fatal("A must be in the cache")
	}
	cache.put(visionCacheKey(pathC, 1600, 82), pathC, entry())

	if entries, _ := cache.stats(); entries != 2 {
		t.Fatalf("the cache must hold at most 2 entries, got %d", entries)
	}
	if _, ok := cache.get(visionCacheKey(pathB, 1600, 82), pathB); ok {
		t.Error("B was the least recently used and must have been evicted")
	}
	if _, ok := cache.get(visionCacheKey(pathA, 1600, 82), pathA); !ok {
		t.Error("A was used recently and must have survived")
	}
	if _, ok := cache.get(visionCacheKey(pathC, 1600, 82), pathC); !ok {
		t.Error("C must be in the cache")
	}
}

func TestVisionCacheRespectsByteCap(t *testing.T) {
	cache := newVisionCache(100, 300)
	dir := t.TempDir()
	for _, name := range []string{"a.png", "b.png", "c.png"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
		cache.put(visionCacheKey(path, 1600, 82), path, &visionCacheEntry{
			dataURL: "data:image/jpeg;base64,0123456789012345678901234567890123456789",
			hash:    "h",
		})
	}
	entries, bytes := cache.stats()
	if bytes > 300 {
		t.Fatalf("the byte cap must hold, got %d bytes", bytes)
	}
	if entries == 0 {
		t.Fatal("the cache must not be empty")
	}
}

func TestVisionCacheSkipsOversizedEntry(t *testing.T) {
	cache := newVisionCache(10, 100)
	path := writeTestPNG(t, 20, 20)

	cache.put(visionCacheKey(path, 1600, 82), path, &visionCacheEntry{
		dataURL: "data:image/jpeg;base64," + string(make([]byte, 500)),
		hash:    "h",
	})
	if entries, _ := cache.stats(); entries != 0 {
		t.Fatalf("an entry larger than the whole cap must not be cached, entries=%d", entries)
	}
}

func TestVisionCacheMissesOnMissingFile(t *testing.T) {
	cache := newVisionCache(10, 1<<20)
	if _, ok := cache.get(visionCacheKey("/nonexistent/x.png", 1600, 82), "/nonexistent/x.png"); ok {
		t.Error("a missing file must never be a cache hit")
	}
}
