package web

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/jpeg"
	"net/http"
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

// serveIcon implements the shared body of the driver/vehicle icon GET
// handlers: resolve "id" from the path, fetch the stored icon bytes via
// get, and serve them with ETag-based conditional GET support.
func serveIcon(w http.ResponseWriter, r *http.Request, get func(int64) ([]byte, bool, error)) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	data, ok, err := get(id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}

	etag := etagFor(data)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
}

// setIconBody is the {"icon_b64": ...} JSON shape shared by every
// icon-upload endpoint (own driver/vehicle, admin-managed driver/vehicle).
type setIconBody struct {
	IconB64 string `json:"icon_b64"`
}

// applyIcon implements the shared tail of every icon-upload POST handler:
// decode {"icon_b64":...}, validate+resize it via iconFromB64, persist it
// via set(id, jpg), then publishAll+publishDirectory (an icon change
// touches the driver/vehicle refs embedded in every public snapshot plus
// the admin directory) and respond {"ok":true}. Callers perform their own
// path/ownership resolution beforehand and their own audit call via
// onSuccess (action name and payload differ per caller).
func (s *Server) applyIcon(w http.ResponseWriter, r *http.Request, id int64, set func(int64, []byte) error, onSuccess func()) {
	body, ok := decodeReqJSON[setIconBody](w, r)
	if !ok {
		return
	}
	jpg, err := iconFromB64(body.IconB64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid icon")
		return
	}
	if err := set(id, jpg); err != nil {
		writeErr(w, err)
		return
	}

	s.publishAll()
	s.publishDirectory()
	onSuccess()

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
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
