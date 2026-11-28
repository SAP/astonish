package api

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
)

// Hydrate only trusted immutable text snapshots before saving the candidate digest.
// Bodies remain file-backed work content, never SQL control metadata.
func botEmployeeCaptureArtifacts(ctx context.Context, req bot.RunRequest, resources *BotChatResources, w *store.BotWorkState) error {
	if w == nil || w.Candidate == nil || w.Contract.BotID != req.Bot.ID || w.Contract.RunID != req.Run.ID {
		return store.ErrBotForbidden
	}
	total := len(w.Candidate.Text)
	for _, body := range w.Candidate.Bodies {
		total += len(body)
	}
	for _, a := range w.Candidate.Artifacts {
		if a.ContentRef == nil {
			continue
		}
		switch strings.ToLower(filepath.Ext(a.RelativePath)) {
		case ".md", ".txt", ".json", ".csv", ".tsv", ".yaml", ".yml", ".xml", ".html", ".svg", ".go", ".py", ".js", ".ts", ".sql", ".sh", ".mermaid", ".mmd":
		default:
			return fmt.Errorf("artifact content review unsupported: %s", a.ID)
		}
		ref := a.ContentRef
		if resources == nil || resources.ArtifactBody == nil || ref.Validate() != nil || ref.Kind != store.BotContentArtifact || ref.Scope.BotID != req.Bot.ID || a.SHA256 != ref.SHA256 || a.SizeBytes != ref.SizeBytes {
			return store.ErrBotForbidden
		}
		if a.SizeBytes > int64(store.BotWorkMaxCandidateBytes-total+len(w.Candidate.Bodies[a.ID.String()])) {
			return store.ErrBotInvalid
		}
		body, err := resources.ArtifactBody(ctx, a)
		if err != nil {
			return err
		}
		if int64(len(body)) != a.SizeBytes || fmt.Sprintf("%x", sha256.Sum256(body)) != a.SHA256 || !utf8.Valid(body) {
			return store.ErrBotInvalid
		}
		total += len(body) - len(w.Candidate.Bodies[a.ID.String()])
		if w.Candidate.Bodies == nil {
			w.Candidate.Bodies = map[string]string{}
		}
		w.Candidate.Bodies[a.ID.String()] = string(body)
	}
	return nil
}
