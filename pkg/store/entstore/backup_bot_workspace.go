package entstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/SAP/astonish/pkg/backup"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

const botWorkspaceArchiveEntity = "bot_workspace_snapshots"
const botWorkspaceArchiveKind = "bot-workspace-v1"

type BotWorkspaceArchive struct {
	SchemaVersion int                              `json:"schema_version"`
	BotID         uuid.UUID                        `json:"bot_id"`
	Home          store.BotHomeBinding             `json:"home"`
	Scope         sandbox.BotHomeScope             `json:"scope"`
	Snapshot      sandbox.BotHomeWorkspaceSnapshot `json:"snapshot"`
}

func (a *BotWorkspaceArchive) Validate() error {
	if a.SchemaVersion != 1 || a.BotID == uuid.Nil || a.Home.ID == uuid.Nil || a.Home.BotID != a.BotID || a.Home.Revision < 1 || a.Scope.BotID != a.BotID.String() || a.Scope.Validate() != nil || a.Snapshot.Receipt.BindingKey != a.Scope.Key() {
		return fmt.Errorf("invalid workspace archive binding")
	}
	if a.Home.Scope != store.BotScopePersonal && a.Home.Scope != store.BotScopeTeam {
		return fmt.Errorf("invalid workspace archive owner scope")
	}
	b := sandbox.BotHomeBinding{Scope: a.Scope, Backend: sandbox.BackendKind(a.Home.Backend), Ref: a.Home.StorageKey}
	if b.Validate() != nil {
		return fmt.Errorf("invalid workspace archive Home")
	}
	return sandbox.ValidateBotHomeWorkspaceSnapshot(&a.Snapshot)
}

func exportBotWorkspaceSnapshots(ctx context.Context, w *backup.Writer, m *backup.Manifest, d logicalDB, opts PlatformBackupExportOptions) error {
	raw, _, _, err := exportLogicalTable(ctx, d.DB, d.Dialect, d.Schema, "bots", false)
	if err != nil {
		return err
	}
	scanner := backup.NewRecordScanner(bytes.NewReader(raw), "bots")
	for scanner.Next() {
		record, err := scanner.Record()
		if err != nil {
			return err
		}
		row, err := backup.DecodeRecordData(record)
		if err != nil {
			return err
		}
		if row["home_binding"] == nil {
			continue
		}
		home := store.BotHomeBinding{}
		encoded, err := json.Marshal(row["home_binding"])
		if err != nil {
			return err
		}
		if text, ok := row["home_binding"].(string); ok {
			encoded = []byte(text)
		}
		doc, err := restoredBotDocument(string(encoded))
		if err != nil {
			return err
		}
		if err = botDecode(doc, &home); err != nil {
			return err
		}
		if home == (store.BotHomeBinding{}) {
			continue // Drafts without a provisioned Home have no physical workspace.
		}
		id, err := uuid.Parse(fmt.Sprint(row["id"]))
		if err != nil {
			return err
		}
		if opts.WorkspaceBinding == nil {
			return fmt.Errorf("workspace export requires exact server-derived Home scope resolver")
		}
		binding, err := opts.WorkspaceBinding(d.Scope, id, home)
		if err != nil {
			return err
		}
		if binding == nil || binding.Scope.OrgID != d.Scope.OrgSlug || binding.Scope.UserID != fmt.Sprint(row["owner_id"]) || binding.Scope.BotID != id.String() || d.Scope.Kind == "team" && binding.Scope.TeamID != d.Scope.TeamSlug || binding.Ref != home.StorageKey || string(binding.Backend) != home.Backend {
			return fmt.Errorf("workspace scope resolver mismatch")
		}
		scope := binding.Scope
		if home.BotID != id || binding.Validate() != nil {
			return fmt.Errorf("workspace Home mismatch")
		}
		snap, err := opts.WorkspaceBackend.SnapshotBotHomeWorkspace(ctx, binding, "workspace", sandbox.MaxBotHomeWorkspaceBundleBytes)
		if err != nil {
			return fmt.Errorf("snapshot Bot %s: %w", id, err)
		}
		archive := BotWorkspaceArchive{SchemaVersion: 1, BotID: id, Home: home, Scope: scope, Snapshot: *snap}
		if err = archive.Validate(); err != nil {
			return err
		}
		data, err := json.Marshal(archive)
		if err != nil {
			return err
		}
		path := archiveJoin(d.ArchiveDir, "bot_workspaces", id.String()+".json")
		if _, err = w.AddFile(path, bytes.NewReader(data)); err != nil {
			return err
		}
		m.Entries = append(m.Entries, backup.Entry{Path: path, Kind: botWorkspaceArchiveKind, Entity: botWorkspaceArchiveEntity, Scope: d.Scope, Records: 1})
	}
	return scanner.Err()
}

func decodeBotWorkspaceArchive(data []byte) (*BotWorkspaceArchive, error) {
	if len(data) > 24<<20 {
		return nil, fmt.Errorf("workspace archive transport quota")
	}
	var a BotWorkspaceArchive
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		return nil, err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("workspace archive trailing data")
	}
	return &a, a.Validate()
}

// ReadBotWorkspaceArchive verifies archive checksums and the exact declared scope.
// This is read-only and never provisions a Home or changes execution authority.
func ReadBotWorkspaceArchive(path string, options backup.ReaderOptions, scope backup.Scope, id store.BotID) (*BotWorkspaceArchive, error) {
	summary, err := backup.Verify(path, options)
	if err != nil {
		return nil, err
	}
	files, err := backup.ReadArchiveFiles(path, options)
	if err != nil {
		return nil, err
	}
	for _, e := range summary.Manifest.Entries {
		if e.Entity != botWorkspaceArchiveEntity || e.Scope != scope {
			continue
		}
		a, err := decodeBotWorkspaceArchive(files[e.Path])
		if err != nil {
			return nil, err
		}
		if a.BotID == id {
			if e.Kind != botWorkspaceArchiveKind || e.Records != 1 || a.Scope.OrgID != scope.OrgSlug || scope.Kind == "team" && a.Scope.TeamID != scope.TeamSlug || scope.Kind == "personal" && a.Scope.UserID != scope.UserID {
				return nil, fmt.Errorf("workspace archive scope mismatch")
			}
			return a, nil
		}
	}
	return nil, store.ErrBotNotFound
}

// RestoreBotWorkspaceArchive is an explicit offline setup operation. The caller
// must provide the freshly provisioned, already registered target Home. No GET,
// SQL import, schedule, approval or Task file can call this to acquire authority.
func RestoreBotWorkspaceArchive(ctx context.Context, bots store.BotStore, backend sandbox.BotHomeWorkspaceArchiveBackend, a *BotWorkspaceArchive, target *sandbox.BotHomeBinding) error {
	if backend == nil || bots == nil || target == nil || a == nil {
		return store.ErrBotUnsupported
	}
	if err := a.Validate(); err != nil {
		return err
	}
	p, ok := execution.PrincipalFromContext(ctx)
	if !ok || p.Kind != execution.PrincipalKindUser || p.Validate() != nil {
		return store.ErrBotForbidden
	}
	b, err := bots.Get(ctx, a.BotID)
	if err != nil {
		return err
	}
	expected := sandbox.BotHomeScope{OrgID: p.OrgSlug, TeamID: p.TeamSlug, UserID: b.OwnerID.String(), BotID: b.ID.String()}
	if p.Subject != b.OwnerID.String() || target.Scope != expected || target.Validate() != nil || b.Home.BotID != b.ID || b.Home.Revision < 1 || b.Home.StorageKey != target.Ref || b.Home.Backend != string(target.Backend) || b.Status == store.BotActive {
		return store.ErrBotForbidden
	}
	if target.Scope.Key() == a.Scope.Key() && target.Ref == a.Home.StorageKey {
		return fmt.Errorf("workspace recovery requires a new target Home binding")
	}
	mutation := store.BotTaskMutation{BotID: b.ID, ActorID: b.OwnerID, IdempotencyKey: "explicit-workspace-recovery"}
	guarded := sandbox.WithBotHomeWorkspaceCommitGuard(ctx, func(live context.Context) error { return live.Err() })
	return WithBotTaskWorkspaceCommitGuard(ctx, bots, mutation, func(check func(context.Context) error) error {
		guarded = sandbox.WithBotHomeWorkspaceCommitGuard(guarded, check)
		return backend.RestoreBotHomeWorkspace(guarded, target, &a.Snapshot)
	})
}

func validateBotWorkspaceArchiveEntries(path string, options backup.ReaderOptions, summary backup.Summary) error {
	var files map[string][]byte
	for _, e := range summary.Manifest.Entries {
		if e.Entity != botWorkspaceArchiveEntity {
			continue
		}
		if files == nil {
			var err error
			files, err = backup.ReadArchiveFiles(path, options)
			if err != nil {
				return err
			}
		}
		a, err := decodeBotWorkspaceArchive(files[e.Path])
		if err != nil {
			return err
		}
		expected := archiveJoin("orgs", e.Scope.OrgSlug, e.Scope.Kind, e.Scope.UserID, "bot_workspaces", a.BotID.String()+".json")
		if e.Scope.Kind == "team" {
			expected = archiveJoin("orgs", e.Scope.OrgSlug, "teams", e.Scope.TeamSlug, "bot_workspaces", a.BotID.String()+".json")
		}
		if e.Kind != botWorkspaceArchiveKind || e.Records != 1 || e.Path != expected || a.Scope.OrgID != e.Scope.OrgSlug || e.Scope.Kind == "team" && a.Scope.TeamID != e.Scope.TeamSlug || e.Scope.Kind == "personal" && a.Scope.UserID != e.Scope.UserID || !strings.Contains(e.Path, "/bot_workspaces/") {
			return fmt.Errorf("invalid workspace archive entry")
		}
	}
	return nil
}
