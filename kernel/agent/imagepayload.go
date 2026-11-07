package agent

// Image payload detection for channel drivers (docs/presentation-engine.md
// §9.3): messengers render pictures inline only when the delivery says
// "image" — Matrix needs msgtype m.image, Telegram needs sendPhoto. The
// decision combines the filename extension with the magic bytes, so a
// misnamed payload never produces a broken inline image.

import (
	"bytes"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"strings"
)

// imageExtensions are the filename extensions treated as inline pictures.
// SVG is deliberately absent: Element does not render SVG images (and
// Telegram's sendPhoto rejects them) — static renders ship PNG for exactly
// this reason.
var imageExtensions = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true,
}

// magicBytesAreImage verifies the payload against the registered image
// formats (png/jpeg/gif via the stdlib imports above). A payload that only
// claims .png in its name stays a file attachment.
func magicBytesAreImage(payload []byte) bool {
	_, _, err := image.DecodeConfig(bytes.NewReader(payload))
	return err == nil
}

// imageDimensions reports whether the payload is an inline-renderable image
// and, when it is, its pixel dimensions for the Matrix m.image info block.
func imageDimensions(name string, payload []byte) (width, height int, isImage bool) {
	ext := strings.ToLower(strings.TrimSpace(name))
	if dot := strings.LastIndexByte(ext, '.'); dot >= 0 {
		ext = ext[dot:]
	} else {
		ext = ""
	}
	if !imageExtensions[ext] || !magicBytesAreImage(payload) {
		return 0, 0, false
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(payload))
	if err != nil {
		// The magic bytes said image but the header is unreadable: still an
		// image for msgtype purposes, just without dimensions.
		return 0, 0, true
	}
	return config.Width, config.Height, true
}
