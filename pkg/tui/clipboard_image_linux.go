//go:build linux

package tui

import (
	"os/exec"
)

// readClipboardImagePlatform reads an image from the Linux clipboard.
// Preference order:
//  1. Wayland: wl-paste (image/png, then image/jpeg, image/gif)
//  2. X11: xclip -t image/png (then jpeg/gif)
//
// Returns false when no image tool is available or the clipboard has no image.
func readClipboardImagePlatform() (data []byte, mimeType string, ok bool) {
	if data, mime, ok := readLinuxWaylandImage(); ok {
		return data, mime, true
	}
	if data, mime, ok := readLinuxX11Image(); ok {
		return data, mime, true
	}
	return nil, "", false
}

func readLinuxWaylandImage() ([]byte, string, bool) {
	if _, err := exec.LookPath("wl-paste"); err != nil {
		return nil, "", false
	}
	// Only attempt image types when the compositor advertises them.
	targets := listCommandLines("wl-paste", "--list-types")
	candidates := []struct {
		mime string
		args []string
	}{
		{"image/png", []string{"--type", "image/png", "--no-newline"}},
		{"image/jpeg", []string{"--type", "image/jpeg", "--no-newline"}},
		{"image/gif", []string{"--type", "image/gif", "--no-newline"}},
	}
	for _, c := range candidates {
		if len(targets) > 0 && !containsMIMETarget(targets, c.mime) {
			continue
		}
		out, err := exec.Command("wl-paste", c.args...).Output()
		if err != nil || len(out) == 0 {
			continue
		}
		if mime, ok := sniffImageMIME(out); ok {
			return out, mime, true
		}
		return out, c.mime, true
	}
	return nil, "", false
}

func readLinuxX11Image() ([]byte, string, bool) {
	if _, err := exec.LookPath("xclip"); err != nil {
		return nil, "", false
	}
	targets := listCommandLines("xclip", "-selection", "clipboard", "-t", "TARGETS", "-o")
	candidates := []string{"image/png", "image/jpeg", "image/gif"}
	for _, mime := range candidates {
		if len(targets) > 0 && !containsMIMETarget(targets, mime) {
			continue
		}
		out, err := exec.Command("xclip", "-selection", "clipboard", "-t", mime, "-o").Output()
		if err != nil || len(out) == 0 {
			continue
		}
		if sniffed, ok := sniffImageMIME(out); ok {
			return out, sniffed, true
		}
		return out, mime, true
	}
	return nil, "", false
}

func listCommandLines(name string, args ...string) []string {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return nil
	}
	return parseCommandLines(out)
}
