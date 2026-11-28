package bot

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/SAP/astonish/pkg/store"
)

// DocumentName is an allowlist, not a filesystem path supplied by the model.
type DocumentName string

const (
	DocumentSoul     DocumentName = "SOUL.md"
	DocumentIdentity DocumentName = "IDENTITY.md"
	DocumentUser     DocumentName = "USER.md"
	DocumentCharter  DocumentName = "CHARTER.md"
	DocumentGoals    DocumentName = "GOALS.md"
	DocumentMemory   DocumentName = "MEMORY.md"
	DocumentPeople   DocumentName = "people/INDEX.md"
	DocumentGroups   DocumentName = "groups/INDEX.md"
)

// Document is a bounded read of an actual retained Home file. Content confers no
// permissions and is never imported as policy. SourceVersion is the full file
// SHA-256; the projection's Version identifies current reviewed configuration.
type Document struct {
	RecordMappings         []store.BotMemoryReviewSource `json:"record_mappings,omitempty"`
	Name                   DocumentName                  `json:"name"`
	Content                string                        `json:"content"`
	SourceVersion          string                        `json:"source_version,omitempty"`
	ReadOnly               bool                          `json:"read_only,omitempty"`
	WriteUnsupportedReason string                        `json:"write_unsupported_reason,omitempty"`
}

type DocumentProjection struct {
	BotID     store.BotID `json:"bot_id"`
	Version   int64       `json:"version"`
	Revision  int64       `json:"revision"`
	Documents []Document  `json:"documents"`
}

// Documents reads retained Home files. Reviewed configuration edits still use
// ApplyVersion: file text is never imported as permission or owner authority.
type Documents struct {
	bots  store.BotStore
	Files store.BotFiles
}

func NewDocuments(bots store.BotStore, files ...store.BotFiles) (*Documents, error) {
	if bots == nil || len(files) > 1 {
		return nil, store.ErrBotUnsupported
	}
	d := &Documents{bots: bots}
	if len(files) == 1 {
		d.Files = files[0]
	}
	return d, nil
}

const BotMemoryBootstrapBytes = 12 << 10
const BotCriticalDocumentBytes = 16 << 10

// BoundDocumentText limits UTF-8 bytes including the explicit retrieval hint.
// It never changes the underlying file, including externally oversized memory.
func BoundDocumentText(text, name string, limit int) string {
	hint := "\n\n[Content omitted from bootstrap; use read_file on " + name + " to read the retained file.]\n"
	if len(text) <= limit {
		return text
	}
	if limit <= len(hint) {
		return ""
	}
	end := limit - len(hint)
	for end > 0 && !utf8.ValidString(text[:end]) {
		end--
	}
	return text[:end] + hint
}

func readBotDocument(ctx context.Context, files store.BotFiles, id store.BotID, name DocumentName, limit int) (Document, error) {
	stream, err := files.Open(ctx, id, string(name), store.BotFileRange{Length: int64(limit + 1)})
	if err != nil {
		return Document{}, err
	}
	if stream == nil || stream.ReadCloser == nil {
		return Document{}, store.ErrBotInvalid
	}
	data, readErr := io.ReadAll(io.LimitReader(stream, int64(limit+2)))
	closeErr := stream.Close()
	if readErr != nil {
		return Document{}, readErr
	}
	if closeErr != nil {
		return Document{}, closeErr
	}
	if stream.Entry.Kind != store.BotFileRegular || stream.Entry.Path != string(name) {
		return Document{}, store.ErrBotInvalid
	}
	wantBytes := stream.Entry.SizeBytes
	if wantBytes > int64(limit+1) {
		wantBytes = int64(limit + 1)
	}
	if wantBytes < 0 || int64(len(data)) != wantBytes {
		return Document{}, store.ErrBotConflict
	}
	digest := stream.Entry.SHA256
	if int64(len(data)) == stream.Entry.SizeBytes {
		actual := fmt.Sprintf("%x", sha256.Sum256(data))
		if digest != "" && digest != actual {
			return Document{}, store.ErrBotConflict
		}
		digest = actual
	} else if stream.Entry.SizeBytes <= int64(limit) || len(digest) != 64 {
		return Document{}, store.ErrBotConflict
	}
	doc := Document{Name: name, Content: BoundDocumentText(string(data), string(name), limit), SourceVersion: digest, ReadOnly: true, WriteUnsupportedReason: "File text is not authority; use reviewed configuration or authorized file operations"}
	switch name {
	case DocumentSoul, DocumentIdentity, DocumentUser, DocumentCharter:
		// The explicit reviewed Edit API changes configuration, never imports
		// arbitrary file text as policy. Preserve that separately authorized UI.
		doc.ReadOnly, doc.WriteUnsupportedReason = false, ""
	}
	return doc, nil
}

// Load returns bounded critical files and the actual MEMORY.md index, never
// historical reports or a latest-N memory query. A GET cannot initialize Home.
// Historical configuration revisions do not imply historical working files.
func (d *Documents) Load(ctx context.Context, id store.BotID, version int64) (*DocumentProjection, error) {
	if version < 0 {
		return nil, store.ErrBotInvalid
	}
	if d == nil || d.bots == nil || d.Files == nil {
		return nil, store.ErrBotUnsupported
	}
	b, err := d.bots.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if b == nil {
		return nil, store.ErrBotNotFound
	}
	if b.ID != id {
		return nil, store.ErrBotForbidden
	}
	if version != 0 && version != b.Version {
		return nil, fmt.Errorf("%w: working files have no historical version projection", store.ErrBotUnsupported)
	}
	result := &DocumentProjection{BotID: id, Version: b.Version, Revision: b.Revision}
	for _, name := range []DocumentName{DocumentSoul, DocumentIdentity, DocumentUser, DocumentCharter, DocumentMemory} {
		limit := BotCriticalDocumentBytes
		if name == DocumentMemory {
			limit = BotMemoryBootstrapBytes
		}
		doc, err := readBotDocument(ctx, d.Files, id, name, limit)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		result.Documents = append(result.Documents, doc)
	}
	return result, nil
}

// LoadMapped retains its signature for callers but deliberately does not query
// the old memory repository. Optional indexes are actual files, not SQL views.
// Full subtrees and historical bodies are retrieved via the lazy file browser.
func (d *Documents) LoadMapped(ctx context.Context, id store.BotID, version int64, _ *store.BotMemoryService) (*DocumentProjection, error) {
	result, err := d.Load(ctx, id, version)
	if err != nil {
		return nil, err
	}
	for _, name := range []DocumentName{DocumentGoals, DocumentPeople, DocumentGroups, "goals/INDEX.md", "skills/INDEX.md", "tasks/INDEX.md", "knowledge/INDEX.md"} {
		doc, err := readBotDocument(ctx, d.Files, id, name, BotCriticalDocumentBytes)
		if errors.Is(err, store.ErrBotNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		result.Documents = append(result.Documents, doc)
	}
	return result, nil
}

func mappedDocument(name DocumentName) bool {
	switch name {
	case DocumentMemory, DocumentPeople, DocumentGroups:
		return true
	}
	parts := strings.Split(string(name), "/")
	if len(parts) == 3 && parts[0] == "goals" && parts[2] == "GOAL.md" {
		id, err := uuid.Parse(parts[1])
		return err == nil && id != uuid.Nil && id.String() == parts[1]
	}
	if len(parts) == 2 && parts[0] == "notes" {
		date := strings.TrimSuffix(parts[1], ".md")
		_, err := time.Parse("2006-01-02", date)
		return err == nil && parts[1] == date+".md"
	}
	return false
}

type DocumentEdit struct {
	BotID            store.BotID
	ExpectedRevision int64
	Name             DocumentName
	Content          string
	Reason           string
	// Confirmed must originate from explicit user review at the transport adapter,
	// not from projected prose or an autonomous model's self-acknowledgment.
	Confirmed bool
}

type DocumentEditAcknowledgment struct {
	BotID    store.BotID  `json:"bot_id"`
	Version  int64        `json:"version"`
	Revision int64        `json:"revision"`
	Document DocumentName `json:"document"`
	// ActivityKind identifies the activity already committed by ApplyVersion.
	// No second activity/transcript is appended by the projection layer.
	ActivityKind store.BotActivityKind `json:"activity_kind"`
}

// Edit changes only one allowlisted prose field. Authorization, draft-only
// restrictions, actor attribution, immutable version insertion and persona
// activity remain the existing ApplyVersion transaction's responsibility.
func (d *Documents) Edit(ctx context.Context, req DocumentEdit) (*DocumentEditAcknowledgment, error) {
	if !req.Confirmed {
		return nil, store.ErrBotForbidden
	}
	if req.ExpectedRevision < 1 || strings.TrimSpace(req.Reason) == "" {
		return nil, store.ErrBotInvalid
	}
	switch req.Name {
	case DocumentSoul, DocumentIdentity, DocumentUser, DocumentCharter:
	case DocumentGoals:
		return nil, fmt.Errorf("%w: use revisioned goal operations", store.ErrBotUnsupported)
	default:
		if mappedDocument(req.Name) {
			return nil, fmt.Errorf("%w: mapped records require revisioned record operations, not Markdown import", store.ErrBotUnsupported)
		}
		return nil, store.ErrBotInvalid
	}
	b, err := d.bots.Get(ctx, req.BotID)
	if err != nil {
		return nil, err
	}
	if b == nil {
		return nil, store.ErrBotNotFound
	}
	if b.ID != req.BotID {
		return nil, store.ErrBotForbidden
	}
	if b.Revision != req.ExpectedRevision {
		return nil, store.ErrBotConflict
	}
	v, err := d.bots.GetVersion(ctx, req.BotID, b.Version)
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, store.ErrBotNotFound
	}
	if v.BotID != b.ID || v.Version != b.Version {
		return nil, store.ErrBotForbidden
	}
	next := *v
	switch req.Name {
	case DocumentSoul:
		next.Persona.Soul = req.Content
	case DocumentIdentity:
		next.Persona.Identity = req.Content
	case DocumentUser:
		next.Persona.User = req.Content
	case DocumentCharter:
		next.Charter = req.Content
	}
	next.ChangeReason = req.Reason
	if err := ValidateVersion(next); err != nil {
		return nil, err
	}
	saved, err := d.bots.ApplyVersion(ctx, store.BotVersionRequest{BotID: b.ID, ExpectedRevision: req.ExpectedRevision, Version: next})
	if err != nil {
		return nil, err
	}
	if saved == nil {
		return nil, store.ErrBotNotFound
	}
	if saved.ID != b.ID || saved.Version != b.Version+1 || saved.Revision != req.ExpectedRevision+1 {
		return nil, store.ErrBotConflict
	}
	return &DocumentEditAcknowledgment{BotID: saved.ID, Version: saved.Version, Revision: saved.Revision, Document: req.Name, ActivityKind: store.BotActivityPersona}, nil
}
