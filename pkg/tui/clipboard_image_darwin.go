//go:build darwin

package tui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// readClipboardImagePlatform reads a PNG (preferred) from the macOS pasteboard
// using osascript. Terminals rarely deliver image paste events to the app, so
// we read the system clipboard when the user triggers paste.
func readClipboardImagePlatform() (data []byte, mimeType string, ok bool) {
	f, err := os.CreateTemp("", "astonish-clip-*.png")
	if err != nil {
		return nil, "", false
	}
	path := f.Name()
	_ = f.Close()
	defer os.Remove(path)

	// Prefer PNG; fall back to TIFF then convert via sips if needed.
	script := fmt.Sprintf(`
try
  set pngData to (the clipboard as «class PNGf»)
  set outFile to open for access POSIX file %q with write permission
  set eof outFile to 0
  write pngData to outFile
  close access outFile
  return "png"
on error
  try
    set tiffData to (the clipboard as «class TIFF»)
    set tiffPath to %q & ".tiff"
    set outFile to open for access POSIX file tiffPath with write permission
    set eof outFile to 0
    write tiffData to outFile
    close access outFile
    return "tiff:" & tiffPath
  on error
    return "empty"
  end try
end try
`, path, path)

	out, err := exec.Command("osascript", "-e", script).CombinedOutput()
	if err != nil {
		return nil, "", false
	}
	result := strings.TrimSpace(string(out))
	switch {
	case result == "png":
		data, err = os.ReadFile(path)
		if err != nil || len(data) == 0 {
			return nil, "", false
		}
		if mime, ok := sniffImageMIME(data); ok {
			return data, mime, true
		}
		return data, "image/png", true
	case strings.HasPrefix(result, "tiff:"):
		tiffPath := strings.TrimPrefix(result, "tiff:")
		defer os.Remove(tiffPath)
		// Convert TIFF → PNG with sips (always available on macOS).
		if err := exec.Command("sips", "-s", "format", "png", tiffPath, "--out", path).Run(); err != nil {
			return nil, "", false
		}
		data, err = os.ReadFile(path)
		if err != nil || len(data) == 0 {
			return nil, "", false
		}
		return data, "image/png", true
	default:
		return nil, "", false
	}
}
