package protectedassets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	BundleRelPath    = "config/protected/core.bundle"
	bundleMagic      = "FAIRYCB1"
	protectedKeySeed = "fairy-public-protected-assets-v1|c4f38b2e7a5d4091"
)

var ErrAssetMissing = errors.New("protected asset missing from bundle")

type Bundle struct {
	Files map[string][]byte
}

type bundlePayload struct {
	Version int               `json:"version"`
	Files   map[string]string `json:"files"`
}

func EncodeBundle(files map[string][]byte) ([]byte, error) {
	payload := bundlePayload{Version: 1, Files: make(map[string]string, len(files))}
	for name, content := range files {
		clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(name)))
		if clean == "." || strings.HasPrefix(clean, "../") || filepath.IsAbs(clean) {
			return nil, fmt.Errorf("invalid protected asset path %q", name)
		}
		payload.Files[clean] = string(content)
	}
	plaintext, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode protected bundle payload: %w", err)
	}
	key := sha256.Sum256([]byte(protectedKeySeed))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("initialize protected bundle cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("initialize protected bundle GCM: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate protected bundle nonce: %w", err)
	}
	raw := append([]byte(bundleMagic), nonce...)
	raw = gcm.Seal(raw, nonce, plaintext, nil)
	return raw, nil
}

func LoadBundle(repoRoot string) (*Bundle, error) {
	path := filepath.Join(repoRoot, filepath.FromSlash(BundleRelPath))
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read protected bundle: %w", err)
	}
	if len(raw) < len(bundleMagic)+12 {
		return nil, fmt.Errorf("protected bundle is truncated")
	}
	if string(raw[:len(bundleMagic)]) != bundleMagic {
		return nil, fmt.Errorf("protected bundle has invalid magic")
	}
	nonce := raw[len(bundleMagic) : len(bundleMagic)+12]
	ciphertext := raw[len(bundleMagic)+12:]

	key := sha256.Sum256([]byte(protectedKeySeed))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("initialize protected bundle cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("initialize protected bundle GCM: %w", err)
	}
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("authenticate protected bundle: %w", err)
	}
	var payload bundlePayload
	if err := json.Unmarshal(plaintext, &payload); err != nil {
		return nil, fmt.Errorf("parse protected bundle: %w", err)
	}
	if payload.Version != 1 {
		return nil, fmt.Errorf("unsupported protected bundle version %d", payload.Version)
	}
	if len(payload.Files) == 0 {
		return nil, fmt.Errorf("protected bundle has no files")
	}
	files := make(map[string][]byte, len(payload.Files))
	for name, content := range payload.Files {
		clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(name)))
		if clean == "." || strings.HasPrefix(clean, "../") || filepath.IsAbs(clean) {
			return nil, fmt.Errorf("protected bundle contains invalid path %q", name)
		}
		files[clean] = []byte(content)
	}
	return &Bundle{Files: files}, nil
}

func (b *Bundle) Read(relPath string) ([]byte, bool) {
	if b == nil {
		return nil, false
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(relPath)))
	data, ok := b.Files[clean]
	if !ok {
		return nil, false
	}
	return append([]byte(nil), data...), true
}

// ReadFileForPath returns a plaintext file when it exists. If it is absent, it
// walks up to the nearest repository root containing config/protected/core.bundle
// and returns the encrypted bundle entry for the same relative path.
func ReadFileForPath(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		return data, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	abs, absErr := filepath.Abs(path)
	if absErr != nil {
		return nil, fmt.Errorf("resolve %s: %w", path, absErr)
	}
	for dir := filepath.Dir(abs); ; dir = filepath.Dir(dir) {
		bundlePath := filepath.Join(dir, filepath.FromSlash(BundleRelPath))
		if _, statErr := os.Stat(bundlePath); statErr == nil {
			bundle, loadErr := LoadBundle(dir)
			if loadErr != nil {
				return nil, loadErr
			}
			rel, relErr := filepath.Rel(dir, abs)
			if relErr != nil {
				return nil, fmt.Errorf("resolve protected asset path: %w", relErr)
			}
			content, ok := bundle.Read(filepath.ToSlash(rel))
			if !ok {
				return nil, fmt.Errorf("%w: %s", ErrAssetMissing, filepath.ToSlash(rel))
			}
			return content, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	return nil, err
}
