package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"strings"

	"github.com/google/uuid"
)

const BotFilePublicationKey = "bot_file_publication"
const BotFilePublicationSchemaVersion = 1

// BotFilePublication is the public, immutable delivery envelope. It deliberately
// excludes content storage keys, memory notes, notification targets and receipts.
type BotFilePublication struct {
	SchemaVersion  int                    `json:"schema_version"`
	BotID          BotID                  `json:"bot_id"`
	RunID          RunID                  `json:"run_id"`
	HomeSessionID  uuid.UUID              `json:"home_session_id"`
	HomeID         uuid.UUID              `json:"home_id"`
	ReportSequence int64                  `json:"report_sequence"`
	EventID        string                 `json:"event_id"`
	Summary        string                 `json:"summary"`
	Artifacts      []BotPublishedArtifact `json:"artifacts"`
}

type BotPublishedArtifact struct {
	ID           uuid.UUID          `json:"id"`
	HomeID       uuid.UUID          `json:"home_id"`
	RelativePath string             `json:"relative_path"`
	SHA256       string             `json:"sha256"`
	SizeBytes    int64              `json:"size_bytes"`
	MediaType    string             `json:"media_type"`
	Title        string             `json:"title"`
	Preview      BotFilePreviewKind `json:"preview"`
}

func BotPublicationEventID(run RunID, sequence int64) string {
	return uuid.NewSHA1(run, []byte(fmt.Sprintf("home-report:%s:%d", run, sequence))).String()
}

func (p BotFilePublication) Validate() error {
	if p.SchemaVersion != BotFilePublicationSchemaVersion || p.BotID == uuid.Nil || p.RunID == uuid.Nil || p.HomeSessionID == uuid.Nil || p.HomeID == uuid.Nil || p.ReportSequence < 1 || p.EventID != BotPublicationEventID(p.RunID, p.ReportSequence) || len(p.Artifacts) == 0 {
		return ErrBotInvalid
	}
	seen := map[uuid.UUID]bool{}
	for _, a := range p.Artifacts {
		digest, err := hex.DecodeString(a.SHA256)
		if a.ID == uuid.Nil || seen[a.ID] || a.HomeID != p.HomeID || a.SizeBytes < 0 || err != nil || len(digest) != sha256.Size || a.RelativePath == "" || a.RelativePath == "." || path.IsAbs(a.RelativePath) || path.Clean(a.RelativePath) != a.RelativePath || a.RelativePath == ".." || strings.HasPrefix(a.RelativePath, "../") || strings.ContainsAny(a.RelativePath, "\\\x00\r\n") {
			return ErrBotInvalid
		}
		if a.Preview != BotFilePreviewText && a.Preview != BotFilePreviewDownload {
			return ErrBotInvalid
		}
		seen[a.ID] = true
	}
	return nil
}

// FilePublication projects only verified immutable artifacts. A mutable working
// path or an arbitrary model-supplied artifact cannot become a delivery card.
func (p BotPublication) FilePublication() (*BotFilePublication, error) {
	if len(p.Artifacts) == 0 {
		return nil, nil
	}
	result := &BotFilePublication{SchemaVersion: BotFilePublicationSchemaVersion, BotID: p.BotID, RunID: p.RunID, HomeSessionID: p.HomeSessionID, HomeID: p.Artifacts[0].HomeID, ReportSequence: p.ReportSequence, EventID: p.EventID, Summary: p.Text}
	for _, a := range p.Artifacts {
		ref := a.ContentRef
		if ref == nil || ref.Validate() != nil || ref.Scope.BotID != p.BotID || ref.Kind != BotContentArtifact || ref.SHA256 != a.SHA256 || ref.SizeBytes != a.SizeBytes {
			return nil, ErrBotInvalid
		}
		preview := BotFilePreviewDownload
		switch strings.ToLower(strings.Split(a.MediaType, ";")[0]) {
		case "text/plain", "text/markdown", "text/csv", "application/json", "application/yaml", "text/yaml":
			preview = BotFilePreviewText
		}
		result.Artifacts = append(result.Artifacts, BotPublishedArtifact{ID: a.ID, HomeID: a.HomeID, RelativePath: a.RelativePath, SHA256: a.SHA256, SizeBytes: a.SizeBytes, MediaType: a.MediaType, Title: a.Title, Preview: preview})
	}
	if err := result.Validate(); err != nil {
		return nil, err
	}
	return result, nil
}
