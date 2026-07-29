package tui

import (
	"bytes"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"strings"
)

// clipboardImageReader reads an image from the system clipboard when supported.
// Tests override this to inject fixtures without touching the real clipboard.
var clipboardImageReader = readClipboardImage

// readClipboardImage attempts to load image bytes from the OS clipboard.
// Returns false when no image is available or the platform is unsupported.
func readClipboardImage() (data []byte, mimeType string, ok bool) {
	return readClipboardImagePlatform()
}

func sniffImageMIME(data []byte) (string, bool) {
	if len(data) == 0 {
		return "", false
	}
	// Prefer stdlib detection for common formats.
	_, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err == nil {
		switch format {
		case "png":
			return "image/png", true
		case "jpeg":
			return "image/jpeg", true
		case "gif":
			return "image/gif", true
		}
	}
	// Magic-byte fallbacks for partial/odd clipboard payloads.
	if len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}) {
		return "image/png", true
	}
	if len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff {
		return "image/jpeg", true
	}
	if len(data) >= 6 && (bytes.Equal(data[:6], []byte("GIF87a")) || bytes.Equal(data[:6], []byte("GIF89a"))) {
		return "image/gif", true
	}
	return "", false
}

func containsMIMETarget(list []string, want string) bool {
	want = strings.ToLower(want)
	for _, item := range list {
		item = strings.ToLower(strings.TrimSpace(item))
		if item == want || strings.HasPrefix(item, want+";") {
			return true
		}
	}
	return false
}

func parseCommandLines(out []byte) []string {
	if len(out) == 0 {
		return nil
	}
	var lines []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}
