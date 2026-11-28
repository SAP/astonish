package api

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/genai"
)

// Match Chat's ten 10 MiB files, allowing base64 and JSON framing.
func decodeBotAttachmentRequest(w http.ResponseWriter, r *http.Request, out any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 140<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		botHTTPError(w, store.ErrBotInvalid)
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		botHTTPError(w, store.ErrBotInvalid)
		return false
	}
	return true
}

func validateBotAttachments(atts []ChatAttachment) ([]store.ChatFile, error) {
	if len(atts) > 10 {
		return nil, store.ErrBotInvalid
	}
	for _, a := range atts {
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(a.Data))
		if err != nil || len(raw) == 0 || len(raw) > 10<<20 || strings.TrimSpace(a.Filename) == "" || strings.TrimSpace(a.MimeType) == "" {
			return nil, store.ErrBotInvalid
		}
	}
	return chatFilesFromAttachments(atts), nil
}

func botAttachmentContent(text string, files []store.ChatFile) *genai.Content {
	atts := make([]ChatAttachment, len(files))
	for i, f := range files {
		atts[i] = ChatAttachment{Filename: f.Filename, MimeType: f.MimeType, Data: base64.StdEncoding.EncodeToString(f.Data)}
	}
	return studioUserContent(text, atts)
}
