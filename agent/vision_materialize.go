package main

// Materialising image references.
//
// The conversation context holds image *placeholders* (a path plus the
// parameters needed to encode it) rather than base64 payloads. The payload is
// produced here, at the wire boundary, and only for the images that are
// actually being sent.
//
// Why this matters: the context is rebuilt and re-sent on every turn. When it
// carried the encoded image, a transcript that mentioned the same screenshot
// five times carried five copies of it, each roughly a megabyte of base64,
// on every subsequent request. With placeholders the transcript costs a few
// bytes per image, and the encoding happens once per request for the images
// that are still relevant.
//
// The encoding itself is cached (see vision_cache.go), so an image repeated
// across turns is decoded and re-encoded only when it actually changed.

// materializeImageRef turns a placeholder into a wire image part.
//
// It returns false when the image cannot be encoded -- the file was removed,
// is unreadable, or is not a decodable image. The caller drops the image in
// that case rather than failing the request: the surrounding text still
// describes what was attached, and a missing screenshot should not cost the
// user their whole turn.
func materializeImageRef(ref ImageRefPart) (ContentPart, bool) {
	if ref.Path == "" {
		return ContentPart{}, false
	}
	maxDimension := ref.MaxDimension
	if maxDimension <= 0 {
		maxDimension = visionDefaultMaxDimension
	}
	quality := ref.Quality
	if quality <= 0 {
		quality = visionDefaultQuality
	}

	dataURL, _, _, _, err := encodeImageAttachmentDataURL(ref.Path, maxDimension, quality)
	if err != nil || dataURL == "" {
		return ContentPart{}, false
	}
	return ContentPart{
		Type: "image_url",
		ImageURL: &ImageURLPart{
			URL:    dataURL,
			Detail: "high",
		},
	}, true
}

// visionDefaultMaxDimension and visionDefaultQuality mirror the defaults the
// API config applies, so a placeholder created without explicit parameters
// still encodes the same way the old inline path did.
const (
	visionDefaultMaxDimension = 1600
	visionDefaultQuality      = 82
)
