//go:build !darwin && !linux

package tui

// readClipboardImagePlatform is a no-op on unsupported platforms.
func readClipboardImagePlatform() (data []byte, mimeType string, ok bool) {
	return nil, "", false
}
