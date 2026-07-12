package web

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/jpeg"
	"strings"
)

// iconFromB64 decodes, validates, and resizes a driver icon submitted as
// base64-encoded JPEG. The client (cropper.min.js) is expected to have
// already cropped the image to a square, so this only resizes - it does
// not crop.
func iconFromB64(b64 string) ([]byte, error) {
	if strings.HasPrefix(b64, "data:") {
		if idx := strings.Index(b64, ","); idx != -1 {
			b64 = b64[idx+1:]
		}
	}

	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		raw, err = base64.RawStdEncoding.DecodeString(b64)
		if err != nil {
			return nil, fmt.Errorf("invalid base64: %w", err)
		}
	}

	img, err := jpeg.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("invalid jpeg: %w", err)
	}

	dst := resizeTo128(img)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 85}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// resizeTo128 does a simple nearest-neighbor resize to 128x128 using only
// the standard library (no golang.org/x/image).
func resizeTo128(src image.Image) *image.RGBA {
	const size = 128

	bounds := src.Bounds()
	sw, sh := bounds.Dx(), bounds.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	if sw <= 0 || sh <= 0 {
		return dst
	}

	for y := 0; y < size; y++ {
		sy := bounds.Min.Y + y*sh/size
		for x := 0; x < size; x++ {
			sx := bounds.Min.X + x*sw/size
			dst.Set(x, y, src.At(sx, sy))
		}
	}
	return dst
}
