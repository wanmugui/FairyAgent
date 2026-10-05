package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"math"
	"os"
	"path/filepath"
	"strings"

	"agentloop/agent/internal/dtypes"
)

const estimatedImageContextTokens = 1200

func toolVisionContextMessage(cfg *APIConfig, results []ToolInvocationResult) (Message, bool) {
	if cfg == nil || !cfg.NativeVisionEnabled() {
		return Message{}, false
	}

	parts := make([]ContentPart, 0)
	labels := make([]string, 0)
	seen := map[string]struct{}{}
	for _, result := range results {
		if result.Err != nil || result.Result.IsError {
			continue
		}
		for _, attachment := range result.Result.Attachments {
			mime := imageAttachmentMIME(attachment)
			if !strings.HasPrefix(mime, "image/") {
				continue
			}
			key := strings.ToLower(strings.TrimSpace(attachment.SHA256))
			if key == "" {
				key = strings.ToLower(filepath.Clean(attachment.Path))
			}
			if key != "" {
				if _, exists := seen[key]; exists {
					continue
				}
				seen[key] = struct{}{}
			}

			dataURL, hash, _, _, err := encodeImageAttachmentDataURL(
				attachment.Path,
				cfg.VisionMaxDimensionOrDefault(),
				cfg.VisionJPEGQualityOrDefault(),
			)
			if err != nil {
				continue
			}
			parts = append(parts, ContentPart{
				Type: "image_url",
				ImageURL: &ImageURLPart{
					URL:    dataURL,
					Detail: "high",
				},
			})
			label := strings.TrimSpace(attachment.Label)
			if label == "" {
				label = strings.TrimSpace(attachment.Path)
			}
			if len(hash) > 12 {
				hash = hash[:12]
			}
			if hash != "" {
				label = fmt.Sprintf("%s sha256=%s", label, hash)
			}
			labels = append(labels, label)
		}
	}
	if len(parts) == 0 {
		return Message{}, false
	}

	content := "[vision_attachment]\nThe following screenshot(s) are attached directly to this turn. Inspect them natively and answer or act from what you see. Do not call image_vqa for ordinary image questions, descriptions, analysis, layout, fonts, colors, comparison, or reasoning; image_vqa is only for text-only models or an explicitly requested offline OCR/coordinate lookup."
	if len(labels) > 0 {
		content += "\n" + strings.Join(labels, "\n")
	}
	message := NewMessage("user", content, nil, "", "")
	message.InternalType = internalTypeVisionContext
	message.ContentParts = parts
	return message, true
}

func trimVisionContextMessages(messages []Message, maxImages int) []Message {
	if maxImages <= 0 {
		return messages
	}
	out := append([]Message(nil), messages...)
	kept := 0
	for index := len(out) - 1; index >= 0; index-- {
		if !isVisionContextMessage(out[index]) {
			continue
		}
		count := contentPartImageCount(out[index].ContentParts)
		if count == 0 {
			out = append(out[:index], out[index+1:]...)
			continue
		}
		if kept+count <= maxImages {
			kept += count
			continue
		}
		if kept == 0 {
			out[index].ContentParts = lastImageParts(out[index].ContentParts, maxImages)
			kept = contentPartImageCount(out[index].ContentParts)
			continue
		}
		out = append(out[:index], out[index+1:]...)
	}
	return out
}

func isVisionContextMessage(message Message) bool {
	return message.InternalType == internalTypeVisionContext || contentPartImageCount(message.ContentParts) > 0
}

func contentPartImageCount(parts []ContentPart) int {
	count := 0
	for _, part := range parts {
		if part.Type == "image_url" && part.ImageURL != nil && part.ImageURL.URL != "" {
			count++
		}
	}
	return count
}

func lastImageParts(parts []ContentPart, limit int) []ContentPart {
	if limit <= 0 {
		return nil
	}
	selected := make([]ContentPart, 0, limit)
	for index := len(parts) - 1; index >= 0 && len(selected) < limit; index-- {
		if parts[index].Type == "image_url" && parts[index].ImageURL != nil && parts[index].ImageURL.URL != "" {
			selected = append(selected, parts[index])
		}
	}
	for left, right := 0, len(selected)-1; left < right; left, right = left+1, right-1 {
		selected[left], selected[right] = selected[right], selected[left]
	}
	return selected
}

func imageAttachmentMIME(attachment dtypes.ToolAttachment) string {
	mime := strings.ToLower(strings.TrimSpace(attachment.MIME))
	if strings.HasPrefix(mime, "image/") {
		return mime
	}
	switch strings.ToLower(filepath.Ext(attachment.Path)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	}
	return mime
}

func encodeImageAttachmentDataURL(path string, maxDimension, quality int) (string, string, int, int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", "", 0, 0, fmt.Errorf("read attachment: %w", err)
	}
	if len(raw) == 0 {
		return "", "", 0, 0, fmt.Errorf("attachment %q is empty", path)
	}
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])

	source, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return "", hash, 0, 0, fmt.Errorf("decode attachment: %w", err)
	}
	source = fitImageDimension(source, maxDimension)
	bounds := source.Bounds()
	canvas := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(canvas, canvas.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	draw.Draw(canvas, canvas.Bounds(), source, bounds.Min, draw.Src)

	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, canvas, &jpeg.Options{Quality: quality}); err != nil {
		return "", hash, 0, 0, fmt.Errorf("encode attachment: %w", err)
	}
	dataURL := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(encoded.Bytes())
	return dataURL, hash, canvas.Bounds().Dx(), canvas.Bounds().Dy(), nil
}

func fitImageDimension(source image.Image, maxDimension int) image.Image {
	if source == nil || maxDimension <= 0 {
		return source
	}
	bounds := source.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width <= 0 || height <= 0 || (width <= maxDimension && height <= maxDimension) {
		return source
	}
	scale := float64(maxDimension) / float64(width)
	if height > width {
		scale = float64(maxDimension) / float64(height)
	}
	targetWidth := maxInt(1, int(math.Round(float64(width)*scale)))
	targetHeight := maxInt(1, int(math.Round(float64(height)*scale)))
	return resizeBilinear(source, targetWidth, targetHeight)
}

func resizeBilinear(source image.Image, width, height int) image.Image {
	target := image.NewRGBA(image.Rect(0, 0, width, height))
	bounds := source.Bounds()
	sourceWidth, sourceHeight := bounds.Dx(), bounds.Dy()
	if sourceWidth == 0 || sourceHeight == 0 {
		return target
	}
	for y := 0; y < height; y++ {
		sourceY := (float64(y)+0.5)*float64(sourceHeight)/float64(height) - 0.5
		y0 := int(math.Floor(sourceY))
		y1 := y0 + 1
		fy := sourceY - float64(y0)
		if y0 < 0 {
			y0 = 0
			fy = 0
		}
		if y1 >= sourceHeight {
			y1 = sourceHeight - 1
		}
		for x := 0; x < width; x++ {
			sourceX := (float64(x)+0.5)*float64(sourceWidth)/float64(width) - 0.5
			x0 := int(math.Floor(sourceX))
			x1 := x0 + 1
			fx := sourceX - float64(x0)
			if x0 < 0 {
				x0 = 0
				fx = 0
			}
			if x1 >= sourceWidth {
				x1 = sourceWidth - 1
			}
			c00 := color.RGBAModel.Convert(source.At(bounds.Min.X+x0, bounds.Min.Y+y0)).(color.RGBA)
			c10 := color.RGBAModel.Convert(source.At(bounds.Min.X+x1, bounds.Min.Y+y0)).(color.RGBA)
			c01 := color.RGBAModel.Convert(source.At(bounds.Min.X+x0, bounds.Min.Y+y1)).(color.RGBA)
			c11 := color.RGBAModel.Convert(source.At(bounds.Min.X+x1, bounds.Min.Y+y1)).(color.RGBA)
			target.SetRGBA(x, y, color.RGBA{
				R: bilinearChannel(c00.R, c10.R, c01.R, c11.R, fx, fy),
				G: bilinearChannel(c00.G, c10.G, c01.G, c11.G, fx, fy),
				B: bilinearChannel(c00.B, c10.B, c01.B, c11.B, fx, fy),
				A: 255,
			})
		}
	}
	return target
}

func bilinearChannel(c00, c10, c01, c11 uint8, fx, fy float64) uint8 {
	top := float64(c00)*(1-fx) + float64(c10)*fx
	bottom := float64(c01)*(1-fx) + float64(c11)*fx
	value := top*(1-fy) + bottom*fy
	if value < 0 {
		value = 0
	}
	if value > 255 {
		value = 255
	}
	return uint8(math.Round(value))
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
