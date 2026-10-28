package main

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"sync"
)

// Vision encoding cache.
//
// The vision context is rebuilt on every turn, and every rebuild re-read the
// image file, decoded it, resized it, re-encoded it to JPEG and base64'd the
// result. A conversation that carries the same screenshot across ten turns
// therefore paid for the same encode ten times, and a large screenshot makes
// that expensive: decode + bilinear resize + JPEG encode + base64 for a
// multi-megabyte PNG is hundreds of milliseconds and tens of megabytes of
// allocation, per turn, per image.
//
// The cache keys on (path, maxDimension, quality) and validates against the
// file's size and modification time, so an edited or replaced file is
// re-encoded instead of serving a stale image. It is an LRU with both an
// entry cap and a byte cap, because base64 payloads are large: a cap on
// entries alone would let a handful of big images pin a lot of memory.
//
// The cache lives in the process, so it is dropped when the agent restarts.
// That is deliberate -- it is a latency optimisation, not a correctness
// mechanism, and every entry can be rebuilt from the file on disk.

// visionCacheMaxEntries caps how many encoded images are kept.
const visionCacheMaxEntries = 32

// visionCacheMaxBytes caps the total size of the cached data URLs. A base64
// payload is roughly 4/3 of the JPEG it came from, so this is a proxy for
// resident memory.
const visionCacheMaxBytes = 64 << 20 // 64 MiB

type visionCacheEntry struct {
	key      string
	dataURL  string
	hash     string
	width    int
	height   int
	fileSize int64
	fileMod  int64
	bytes    int
}

type visionCache struct {
	mu       sync.Mutex
	items    map[string]*list.Element
	order    *list.List // front = most recently used
	total    int
	maxItems int
	maxBytes int
}

var visionEncodings = newVisionCache(visionCacheMaxEntries, visionCacheMaxBytes)

func newVisionCache(maxItems, maxBytes int) *visionCache {
	if maxItems <= 0 {
		maxItems = visionCacheMaxEntries
	}
	if maxBytes <= 0 {
		maxBytes = visionCacheMaxBytes
	}
	return &visionCache{
		items:    make(map[string]*list.Element),
		order:    list.New(),
		maxItems: maxItems,
		maxBytes: maxBytes,
	}
}

// visionCacheKey identifies an encode by its inputs. The file's identity is
// deliberately not part of the key: the same path with different contents
// must miss, and that is handled by the mtime/size check on lookup.
func visionCacheKey(path string, maxDimension, quality int) string {
	return path + "\x00" + itoa(maxDimension) + "\x00" + itoa(quality)
}

// fileSHA256 hashes a file's bytes without decoding it.
//
// The placeholder needs the hash to label the image for the model, but
// labelling must not require the expensive path: decode + resize + JPEG
// encode. Reading the bytes and hashing them is orders of magnitude cheaper
// and keeps "build the context" free of image work.
func fileSHA256(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// get returns a cached entry when the key matches and the file on disk is
// unchanged since the entry was written.
func (c *visionCache) get(key, path string) (entry *visionCacheEntry, ok bool) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	element, found := c.items[key]
	if !found {
		return nil, false
	}
	cached := element.Value.(*visionCacheEntry)
	if cached.fileSize != info.Size() || cached.fileMod != info.ModTime().UnixNano() {
		// The file changed under us: drop the stale entry and miss.
		c.removeElement(element)
		return nil, false
	}
	c.order.MoveToFront(element)
	return cached, true
}

// put stores an encoded image, evicting least-recently-used entries until the
// cache is back within its limits.
func (c *visionCache) put(key, path string, entry *visionCacheEntry) {
	info, err := os.Stat(path)
	if err != nil {
		// Without a stat we cannot validate the entry later, so do not cache
		// it. The next turn simply re-encodes.
		return
	}
	entry.key = key
	entry.fileSize = info.Size()
	entry.fileMod = info.ModTime().UnixNano()
	entry.bytes = len(entry.dataURL)

	// A single oversized image would evict everything and then still not
	// fit; caching it would evict more than it is worth.
	if entry.bytes > c.maxBytes {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if element, found := c.items[key]; found {
		c.total -= element.Value.(*visionCacheEntry).bytes
		c.removeElement(element)
	}

	c.items[key] = c.order.PushFront(entry)
	c.total += entry.bytes

	for len(c.items) > c.maxItems {
		oldest := c.order.Back()
		if oldest == nil {
			break
		}
		c.removeElement(oldest)
	}
	for c.total > c.maxBytes {
		oldest := c.order.Back()
		if oldest == nil {
			break
		}
		c.removeElement(oldest)
	}
}

// removeElement unlinks an element and updates the byte accounting. The
// caller holds the lock.
func (c *visionCache) removeElement(element *list.Element) {
	entry, ok := element.Value.(*visionCacheEntry)
	if !ok {
		return
	}
	c.order.Remove(element)
	delete(c.items, entry.key)
	c.total -= entry.bytes
	if c.total < 0 {
		c.total = 0
	}
}

// stats reports the cache size, for tests and diagnostics.
func (c *visionCache) stats() (entries int, bytes int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items), c.total
}

// clear drops every entry. Tests use it to start from a known state.
func (c *visionCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = make(map[string]*list.Element)
	c.order.Init()
	c.total = 0
}

// itoa renders a small non-negative int without pulling in strconv for a
// hot path that runs once per image per turn.
func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
