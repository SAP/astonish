package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

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

// Document is a derived, in-memory view. Content confers no permissions and is
// never imported as policy. Version is the persona/charter source version; goals
// are current independently revisioned records, not part of BotVersion.
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

// Documents uses the existing scoped repository; it neither owns storage nor
// writes host files. The eventual home adapter may materialize these projections,
// but must not interpret edits to a mounted file as an authorized version change.
type Documents struct{ bots store.BotStore }

func NewDocuments(bots store.BotStore) (*Documents, error) {
	if bots == nil {
		return nil, store.ErrBotUnsupported
	}
	return &Documents{bots: bots}, nil
}

// Load returns an authorized version projection. Zero selects the current
// version. Goals are explicitly current: the repository does not store historical
// goal snapshots within versions. This is not a cross-record atomic snapshot.
func (d *Documents) Load(ctx context.Context, id store.BotID, version int64) (*DocumentProjection, error) {
	if version < 0 {
		return nil, store.ErrBotInvalid
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
	if version == 0 {
		version = b.Version
	}
	v, err := d.bots.GetVersion(ctx, id, version)
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, store.ErrBotNotFound
	}
	if v.BotID != id || v.Version != version {
		return nil, store.ErrBotForbidden
	}
	if v.SchemaVersion != store.BotSchemaVersion {
		return nil, store.ErrBotUnsupported
	}
	goals, err := d.bots.ListGoals(ctx, id)
	if err != nil {
		return nil, err
	}
	goals = append([]store.BotGoal{}, goals...)
	seen := map[string]bool{}
	for _, goal := range goals {
		key := goal.ID.String()
		if goal.ID == uuid.Nil || goal.BotID != id || seen[key] {
			return nil, store.ErrBotForbidden
		}
		seen[key] = true
	}
	sort.Slice(goals, func(i, j int) bool { return goals[i].ID.String() < goals[j].ID.String() })
	// JSON inside Markdown preserves full goal revision/evidence rather than
	// fabricating completion claims or losing attribution in a prose summary.
	encoded, err := json.MarshalIndent(goals, "", "  ")
	if err != nil {
		return nil, err
	}
	goalText := "# Goals\n\nCurrent independently revisioned goal records; not a historical BotVersion snapshot.\n\n```json\n" + string(encoded) + "\n```\n"
	projection := &DocumentProjection{BotID: id, Version: version, Revision: b.Revision, Documents: []Document{
		{Name: DocumentSoul, Content: v.Persona.Soul},
		{Name: DocumentIdentity, Content: v.Persona.Identity},
		{Name: DocumentUser, Content: v.Persona.User},
		{Name: DocumentCharter, Content: v.Charter},
		{Name: DocumentGoals, Content: goalText, ReadOnly: true, WriteUnsupportedReason: "Use revisioned goal operations"},
	}}
	for _, goal := range goals {
		data, err := json.MarshalIndent(goal, "", "  ")
		if err != nil {
			return nil, err
		}
		projection.Documents = append(projection.Documents, Document{Name: DocumentName("goals/" + goal.ID.String() + "/GOAL.md"), Content: "# Goal\n\nCurrent independently revisioned record, not a historical BotVersion snapshot.\n\n```json\n" + string(data) + "\n```\n", SourceVersion: fmt.Sprint(goal.Revision), ReadOnly: true, WriteUnsupportedReason: "Use revisioned goal operations"})
	}
	return projection, nil
}

// LoadMapped projects current source records; memory has no historical version or
// revisioned Markdown import contract. The service is request-scoped and must be
// composed from the same authenticated Bot repository and memory scope.
func (d *Documents) LoadMapped(ctx context.Context, id store.BotID, version int64, memory *store.BotMemoryService) (*DocumentProjection, error) {
	projection, err := d.Load(ctx, id, version)
	if err != nil {
		return nil, err
	}
	health, err := NewMemoryHealth(memory)
	if err != nil {
		return nil, err
	}
	snapshot, err := health.Snapshot(ctx, 2000)
	if err != nil {
		return nil, err
	}
	if err := d.checkMappedScope(ctx, id, snapshot.Records); err != nil {
		return nil, err
	}
	groups := map[DocumentName][]store.MemorySearchResult{DocumentMemory: snapshot.Records, DocumentPeople: {}, DocumentGroups: {}}
	for _, row := range snapshot.Records {
		switch strings.ToLower(strings.TrimSpace(row.Category)) {
		case "person", "people":
			groups[DocumentPeople] = append(groups[DocumentPeople], row)
		case "group", "groups":
			groups[DocumentGroups] = append(groups[DocumentGroups], row)
		}
		if date, err := time.Parse(time.RFC3339, row.CreatedAt); err == nil {
			name := DocumentName("notes/" + date.UTC().Format("2006-01-02") + ".md")
			groups[name] = append(groups[name], row)
		}
	}
	names := make([]string, 0, len(groups))
	for name := range groups {
		names = append(names, string(name))
	}
	sort.Strings(names)
	for _, name := range names {
		records := groups[DocumentName(name)]
		data, err := json.MarshalIndent(records, "", "  ")
		if err != nil {
			return nil, err
		}
		mappings := make([]store.BotMemoryReviewSource, 0, len(records))
		for _, row := range records {
			mappings = append(mappings, store.BotMemoryReviewSource{ID: row.ID, SessionID: row.SessionID, ExpectedFingerprint: store.MemoryContentFingerprint(row)})
		}
		notice := "Current attributed memory records; use record_mappings for explicit reviewed ingest, not the document-wide fingerprint. People/group categories are descriptive notes, not identity or permission assignments."
		if snapshot.Truncated {
			notice += " Bounded to 2000 source records; additional records are omitted."
		}
		projection.Documents = append(projection.Documents, Document{Name: DocumentName(name), RecordMappings: mappings, Content: "# " + name + "\n\n" + notice + "\n\n```json\n" + string(data) + "\n```\n", SourceVersion: snapshot.Fingerprint, ReadOnly: true, WriteUnsupportedReason: "Raw Markdown import is unsupported; use explicit reviewed record mappings at the document ingest endpoint"})
	}
	return projection, nil
}

// Defend against accidental composition with another Bot's memory service.
func (d *Documents) checkMappedScope(ctx context.Context, id store.BotID, rows []store.MemorySearchResult) error {
	b, err := d.bots.Get(ctx, id)
	if err != nil {
		return err
	}
	if b == nil {
		return store.ErrBotNotFound
	}
	if b.ID != id {
		return store.ErrBotForbidden
	}
	allowed := map[string]bool{}
	if b.HomeSessionID != uuid.Nil {
		allowed[b.HomeSessionID.String()] = true
	}
	after := ""
	seen := map[store.RunID]bool{}
	for {
		runs, err := d.bots.ListRuns(ctx, id, store.BotPage{Limit: 200, After: after})
		if err != nil {
			return err
		}
		for _, run := range runs {
			if run.BotID != id || run.ID == uuid.Nil || seen[run.ID] {
				return store.ErrBotForbidden
			}
			seen[run.ID] = true
			if run.SessionID != uuid.Nil {
				allowed[run.SessionID.String()] = true
			}
		}
		if len(runs) < 200 {
			break
		}
		after = runs[len(runs)-1].ID.String()
	}
	for _, row := range rows {
		if !allowed[row.SessionID] || (row.Scope != "" && row.Scope != string(b.Scope)) {
			return store.ErrBotForbidden
		}
	}
	return nil
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
