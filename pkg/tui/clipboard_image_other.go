//go:build !darwin

package tui

// readClipboardImagePlatform is a no-op outside macOS for now.
// Linux/Windows image clipboard support can be added with wl-paste / PowerShell.
func readClipboardImagePlatform() (data []byte, mimeType string, ok bool) {
	return nil, "", false
}
