//go:build darwin

package tui

import (
	"os"
	"os/exec"
	"strings"
)

// clipImageScript reads the macOS pasteboard and writes a PNG to outPath.
//
// It uses JavaScript for Automation (JXA) + AppKit rather than AppleScript's
// `the clipboard as «class PNGf»` coercion. The old coercion approach was
// unreliable for some sources (large images, or images placed on the pasteboard
// without a public.png representation): the coercion or AppleScript's binary
// `write` would fail, the handler fell through to "empty", and no image was
// pasted — while re-encoding the same image (e.g. a slight resize) happened to
// produce a clean PNG that did work. NSPasteboard + NSBitmapImageRep converts
// any pasteboard image representation to PNG deterministically, regardless of
// size or original format.
const clipImageScript = `
ObjC.import('AppKit');
function run(argv) {
  var out = argv[0];
  var pb = $.NSPasteboard.generalPasteboard;
  var data = pb.dataForType($.NSPasteboardTypePNG);
  if (!data || data.isNil()) {
    var img = $.NSImage.alloc.initWithPasteboard(pb);
    if (!img || img.isNil()) { return 'empty'; }
    var tiff = img.TIFFRepresentation;
    if (!tiff || tiff.isNil()) { return 'empty'; }
    var rep = $.NSBitmapImageRep.imageRepWithData(tiff);
    if (!rep || rep.isNil()) { return 'empty'; }
    data = rep.representationUsingTypeProperties($.NSBitmapImageFileTypePNG, $());
  }
  if (!data || data.isNil() || data.length === 0) { return 'empty'; }
  if (!data.writeToFileAtomically(out, true)) { return 'empty'; }
  return 'png';
}
`

// readClipboardImagePlatform reads an image from the macOS pasteboard as PNG.
// Terminals rarely deliver image paste events to the app, so we read the system
// clipboard when the user triggers paste. Returns false when the clipboard holds
// no image.
func readClipboardImagePlatform() (data []byte, mimeType string, ok bool) {
	f, err := os.CreateTemp("", "astonish-clip-*.png")
	if err != nil {
		return nil, "", false
	}
	path := f.Name()
	_ = f.Close()
	defer os.Remove(path)

	out, err := exec.Command("osascript", "-l", "JavaScript", "-e", clipImageScript, path).CombinedOutput()
	if err != nil {
		return nil, "", false
	}
	if strings.TrimSpace(string(out)) != "png" {
		return nil, "", false
	}

	data, err = os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return nil, "", false
	}
	if mime, ok := sniffImageMIME(data); ok {
		return data, mime, true
	}
	return data, "image/png", true
}
