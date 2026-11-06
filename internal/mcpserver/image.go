package mcpserver

import (
	"bytes"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"net/http"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // decoder
)

const (
	// maxImageSide keeps images within what vision models use well.
	maxImageSide = 1568
	// maxImageBytes is the encoded size sent to the model.
	maxImageBytes = 1 << 20
)

// imageMIME reports the MIME type of image data the model can view.
func imageMIME(data []byte) string {
	switch t := http.DetectContentType(data); t {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return t
	}
	return ""
}

// prepareImage returns image bytes ready for the model: unchanged when small
// enough, otherwise decoded, scaled down to maxImageSide and re-encoded
// (PNG keeps transparency and sharp text; JPEG for photos).
func prepareImage(data []byte, mime string) (out []byte, outMIME string, w, h int, err error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, "", 0, 0, err
	}
	w, h = cfg.Width, cfg.Height
	if len(data) <= maxImageBytes && w <= maxImageSide && h <= maxImageSide && mime != "image/webp" {
		return data, mime, w, h, nil
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", 0, 0, err
	}
	if mime == "image/gif" {
		// First frame only.
		if g, err := gif.DecodeAll(bytes.NewReader(data)); err == nil && len(g.Image) > 0 {
			img = g.Image[0]
		}
	}
	img = scaleDown(img, maxImageSide)
	return encodeForModel(img, mime == "image/jpeg")
}

func scaleDown(img image.Image, side int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= side && h <= side {
		return img
	}
	if w >= h {
		h = max(1, h*side/w)
		w = side
	} else {
		w = max(1, w*side/h)
		h = side
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Over, nil)
	return dst
}

// encodeForModel encodes as PNG (or JPEG for photos / when PNG is too big).
func encodeForModel(img image.Image, photo bool) ([]byte, string, int, int, error) {
	b := img.Bounds()
	var buf bytes.Buffer
	if !photo {
		enc := png.Encoder{CompressionLevel: png.BestSpeed}
		if err := enc.Encode(&buf, img); err != nil {
			return nil, "", 0, 0, err
		}
		if buf.Len() <= maxImageBytes {
			return buf.Bytes(), "image/png", b.Dx(), b.Dy(), nil
		}
		buf.Reset()
	}
	for _, q := range []int{85, 70, 50} {
		buf.Reset()
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: q}); err != nil {
			return nil, "", 0, 0, err
		}
		if buf.Len() <= maxImageBytes {
			break
		}
	}
	return buf.Bytes(), "image/jpeg", b.Dx(), b.Dy(), nil
}
