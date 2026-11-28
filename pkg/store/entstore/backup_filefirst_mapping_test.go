package entstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/backup"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBackupFileFirstMappingIdentityPreservesSQLBytes(t *testing.T) {
	_, _, db, _ := fileFirstExportFixture(t, "personal")
	var definition string
	if err := db.QueryRow("SELECT definition FROM bot_versions").Scan(&definition); err != nil {
		t.Fatal(err)
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, []byte(definition), "", "  "); err != nil {
		t.Fatal(err)
	}
	data, err := backupDocument(map[string]any{"definition": definition}, "definition")
	if err != nil {
		t.Fatal(err)
	}
	_, ref, _, err := homeRecordEnvelope(botHomeConfigurationRecord{kind: botHomeVersion, data: data})
	if err != nil || ref == nil {
		t.Fatal("missing content reference", err)
	}
	state := &fileFirstRestoreState{mapping: newFileFirstMapping()}
	state.mapping.refs[*ref] = *ref
	row := map[string]any{"definition": pretty.String(), "content_hash": "unchanged"}
	if err := mapFileFirstRow("bot_versions", row, state); err != nil {
		t.Fatal(err)
	}
	if row["definition"] != pretty.String() || row["content_hash"] != "unchanged" {
		t.Fatal("identity mapping rewrote immutable SQL bytes")
	}
}

func TestBackupFileFirstMappingClosure(t *testing.T) {
	archive, _, refs := botRestoreFixture(t) // source has already been closed and removed
	old := refs[0].Scope
	next := old
	next.OrgID, next.TeamID, next.OwnerID = "mapped-org", "mapped-team", uuid.NewString()
	state := &fileFirstRestoreState{mapping: &fileFirstMapping{scopes: map[store.BotContentScope]store.BotContentScope{old: next}, refs: map[store.BotContentRef]store.BotContentRef{}, paths: map[store.BotContentRef]string{}}}
	root := t.TempDir()
	if err := mapFileFirstContent(t.Context(), archive, root, t.TempDir(), state, nil, backup.Scope{Kind: "team", OrgSlug: old.OrgID, TeamSlug: old.TeamID}, PlatformRestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	disk, err := newBotDiskContent(root, next)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	for n, ref := range refs {
		mapped, ok := state.mapping.refs[ref]
		if !ok || mapped.Scope != next || mapped.Key != ref.Key {
			t.Fatal("lost mapped identity")
		}
		stream, err := disk.Open(t.Context(), mapped, store.BotFileRange{})
		if err != nil {
			t.Fatal(err)
		}
		inv, err := newBotBackupInventory([]store.BotContentScope{next})
		if err != nil {
			t.Fatal(err)
		}
		codec := botBackupLeaf
		if n == 0 {
			codec = botBackupRun
		}
		if n == 1 {
			codec = botBackupActivity
		}
		if err = inv.children(botBackupObject{Ref: mapped, Codec: codec}, stream); err != nil {
			t.Fatal(err)
		}
		if _, err = io.Copy(io.Discard, stream); err != nil {
			t.Fatal(err)
		}
		if err = stream.Close(); err != nil {
			t.Fatal(err)
		}
		if n < 2 {
			if mapped.SHA256 == ref.SHA256 || len(inv.objects) != 1 || inv.objects[0].Ref != state.mapping.refs[refs[3]] {
				t.Fatal("parent was not rehashed around mapped child")
			}
		} else if mapped.SHA256 != ref.SHA256 || mapped.SizeBytes != ref.SizeBytes {
			t.Fatal("leaf bytes changed")
		}
	}
}

func TestBackupFileFirstMappingRunAuthorityAndOpaqueContent(t *testing.T) {
	old := botFileTestScope()
	root := t.TempDir()
	codec := newBotContentCodec(root, old)
	owner := uuid.New()
	v := botStoredRun{Run: store.BotRun{ID: uuid.New(), BotID: old.BotID, Version: 1, Status: store.RunPartial, Lease: store.BotLease{WorkerID: "old-worker", ExpiresAt: time.Now().Add(time.Hour)}, Checkpoint: json.RawMessage(`{ "target": "org", "arguments": {"owner": "team"} }`)}, ExternalAdmissionValidated: true, SkillAdmissionOwner: &owner}
	v.Run.MandateRef = &store.BotMandateRef{}
	v.Run.CallerCeiling = &execution.BotCallerCeiling{}
	v.ProgressOwnerContinuation = &botProgressOwnerContinuation{BotRevision: 3, CheckpointRevision: 2}
	v.ProgressQuota = &mandateRunUsage{Tools: 9}
	row, err := codec.encodeRun(t.Context(), v)
	if err != nil {
		t.Fatal(err)
	}
	var original botRunRecord
	if err = decodeBotRecord(row, &original); err != nil {
		t.Fatal(err)
	}
	nextScope := old
	nextScope.OrgID = "mapped"
	state := &fileFirstRestoreState{mapping: newFileFirstMapping()}
	state.mapping.scopes[old] = nextScope
	sqlRow := map[string]any{"data": row}
	if err = mapFileFirstRow("bot_runs", sqlRow, state); err != nil {
		t.Fatal(err)
	}
	data, err := backupDocument(sqlRow, "data")
	if err != nil {
		t.Fatal(err)
	}
	var mapped botRunRecord
	if err = decodeBotRecord(data, &mapped); err != nil {
		t.Fatal(err)
	}
	if mapped.Control.ExternalAdmissionValidated || mapped.Control.SkillAdmissionOwner != nil || mapped.Control.MandateRef != nil || mapped.Control.CallerCeiling != nil || mapped.Control.ProgressOwnerContinuation != nil || mapped.Control.ProgressQuota != nil || mapped.Control.Lease.WorkerID != "" || !mapped.Control.Lease.ExpiresAt.IsZero() {
		t.Fatal("restored live authority")
	}
	hydrated, err := newBotContentCodec(t.TempDir(), nextScope).decodeRun(context.Background(), old.BotID, data)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(hydrated.Run.Checkpoint, v.Run.Checkpoint) {
		t.Fatal("action/checkpoint JSON rewritten")
	}
}

func TestBackupFileFirstMappingOpaquePayload(t *testing.T) {
	old := botFileTestScope()
	ref := store.BotContentRef{Scope: old}
	next := ref
	next.Scope.OrgID = "mapped"
	checkpoint := []byte(`{ "scope": "org", "arguments":{"owner":"team"} }`)
	body := botRunContent{Format: botDiskRecordFormat, Checkpoint: checkpoint, Stored: botStoredRun{Run: store.BotRun{BotID: old.BotID, Report: store.BotReport{Summary: "org/team", Artifacts: []store.BotArtifact{{ContentRef: &ref}}}}}}
	raw, _ := json.Marshal(body)
	updated, err := mapFileFirstBody(botBackupObject{Codec: botBackupRun}, raw, map[store.BotContentRef]store.BotContentRef{ref: next})
	if err != nil {
		t.Fatal(err)
	}
	var got botRunContent
	if err = json.Unmarshal(updated, &got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Checkpoint, checkpoint) || got.Stored.Run.Report.Summary != "org/team" || *got.Stored.Run.Report.Artifacts[0].ContentRef != next {
		t.Fatal("opaque content changed or typed edge not mapped")
	}
	if _, err = mapFileFirstBody(botBackupObject{Codec: botBackupRun}, raw, nil); err == nil {
		t.Fatal("missing closure accepted")
	}
}

func TestBackupFileFirstMappingRetainedParent(t *testing.T) {
	old := botFileTestScope()
	next := old
	next.OrgID = "mapped"
	next.TeamID = "mapped-team"
	next.OwnerID = uuid.NewString()
	stage := old
	stage.TeamID = personalSchemaName(old.OwnerID)
	mappedStage := next
	mappedStage.TeamID = personalSchemaName(next.OwnerID)
	disk, err := newBotDiskContent(t.TempDir(), stage)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	leaf, err := disk.Put(t.Context(), store.BotContentArtifact, 1, 4, strings.NewReader("leaf"), contentHomeCommit)
	if err != nil {
		t.Fatal(err)
	}
	body := botRunContent{Format: botRuntimeRecordFormat, Stored: botStoredRun{Run: store.BotRun{BotID: old.BotID, Report: store.BotReport{Artifacts: []store.BotArtifact{{ContentRef: leaf, SHA256: leaf.SHA256, SizeBytes: leaf.SizeBytes}}}}}}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	parent := store.BotContentRef{Format: store.BotContentFormatVersion, Scope: old, Kind: store.BotContentCheckpoint, Revision: 1, Key: fmt.Sprintf("content-v1/checkpoint-1-%s", strings.ReplaceAll(uuid.NewString(), "-", "")), SizeBytes: int64(len(raw)), SHA256: hex.EncodeToString(sum[:])}
	archive := filepath.Join(t.TempDir(), "content")
	err = exportBotFileContent(t.Context(), archive, botFileBackupExportOptions{Scopes: []store.BotContentScope{old, stage}, Compression: backup.CompressionNone, Fence: func(_ context.Context, f func() error) error { return f() }, Collect: func(_ context.Context, i *botBackupInventory) error {
		return i.add(botBackupObject{Ref: parent, Codec: botBackupRun})
	}, Open: func(ctx context.Context, r store.BotContentRef) (*store.BotFileStream, error) {
		if r == parent {
			return &store.BotFileStream{ReadCloser: io.NopCloser(bytes.NewReader(raw))}, nil
		}
		return disk.Open(ctx, r, store.BotFileRange{})
	}})
	if err != nil {
		t.Fatal(err)
	}
	backend := &fileFirstRestoreTestBackend{fileFirstTestBackend: &fileFirstTestBackend{contentHomeBackend: &contentHomeBackend{objects: map[string][]byte{parent.Key: raw}}}}
	binding := sandbox.BotHomeBinding{Scope: sandbox.BotHomeScope{OrgID: next.OrgID, TeamID: next.TeamID, UserID: next.OwnerID, BotID: next.BotID.String()}, Backend: sandbox.BackendKindDocker, Ref: "fresh"}
	state := &fileFirstRestoreState{homes: map[store.BotID]fileFirstBot{old.BotID: {Selection: botContentHomeSelection{Binding: binding}}}, mapping: &fileFirstMapping{scopes: map[store.BotContentScope]store.BotContentScope{old: next, stage: mappedStage}, refs: map[store.BotContentRef]store.BotContentRef{}, paths: map[store.BotContentRef]string{}}}
	if err = mapFileFirstContent(t.Context(), archive, t.TempDir(), t.TempDir(), state, backend, backup.Scope{Kind: "personal", OrgSlug: old.OrgID, UserID: old.OwnerID}, PlatformRestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	mapped := state.mapping.refs[parent]
	if mapped.Scope != next || mapped.Key == parent.Key || mapped.SHA256 == parent.SHA256 {
		t.Fatal("retained parent not republished")
	}
	var got botRunContent
	if err = json.Unmarshal(backend.objects[mapped.Key], &got); err != nil {
		t.Fatal(err)
	}
	if *got.Stored.Run.Report.Artifacts[0].ContentRef != state.mapping.refs[*leaf] {
		t.Fatal("staged child edge not mapped")
	}
}

func TestBackupFileFirstMappingIndependentOwnership(t *testing.T) {
	owner := uuid.NewString()
	for _, personal := range []bool{false, true} {
		scope := backup.Scope{Kind: "team", OrgSlug: "source", TeamSlug: "team"}
		content := store.BotContentScope{OrgID: scope.OrgSlug, TeamID: scope.TeamSlug}
		kind := store.BotContentTemplate
		if personal {
			scope = backup.Scope{Kind: "personal", OrgSlug: "source", UserID: owner}
			content.TeamID, content.OwnerID, content.IntakeID = personalSchemaName(owner), owner, uuid.New()
			kind = store.BotContentIntake
		}
		disk, err := newBotDiskContent(t.TempDir(), content)
		if err != nil {
			t.Fatal(err)
		}
		defer disk.Close()
		ref, err := disk.Put(t.Context(), kind, 1, 4, strings.NewReader("body"), contentHomeCommit)
		if err != nil {
			t.Fatal(err)
		}
		archive := filepath.Join(t.TempDir(), "independent")
		err = exportBotFileContent(t.Context(), archive, botFileBackupExportOptions{
			Scopes: []store.BotContentScope{content}, Compression: backup.CompressionNone,
			Fence: func(_ context.Context, f func() error) error { return f() },
			Collect: func(_ context.Context, i *botBackupInventory) error {
				return i.add(botBackupObject{Ref: *ref, Codec: botBackupLeaf})
			},
			Open: func(ctx context.Context, r store.BotContentRef) (*store.BotFileStream, error) {
				return disk.Open(ctx, r, store.BotFileRange{})
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		mismatches := []string{"", "org", "kind", "team"}
		if personal {
			mismatches = []string{"", "org", "kind", "owner"}
		}
		for _, mismatch := range mismatches {
			t.Run(fmt.Sprintf("%s/%s", scope.Kind, mismatch), func(t *testing.T) {
				source := scope
				switch mismatch {
				case "org":
					source.OrgSlug = "foreign"
				case "team":
					source.TeamSlug = "foreign"
				case "owner":
					source.UserID = uuid.NewString()
				case "kind":
					source.Kind = "unknown"
				}
				state := &fileFirstRestoreState{mapping: &fileFirstMapping{scopes: map[store.BotContentScope]store.BotContentScope{}, refs: map[store.BotContentRef]store.BotContentRef{}, paths: map[store.BotContentRef]string{}}}
				err := mapFileFirstContent(t.Context(), archive, t.TempDir(), t.TempDir(), state, nil, source, PlatformRestoreOptions{MapOrg: map[string]string{"source": "mapped"}})
				if mismatch != "" {
					if err == nil || len(state.mapping.refs) != 0 {
						t.Fatal("foreign independent namespace accepted")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				got := state.mapping.refs[*ref]
				if got.Scope.OrgID != "mapped" || got.Scope.TeamID != content.TeamID || got.Scope.OwnerID != content.OwnerID || got.Scope.IntakeID != content.IntakeID || got.SHA256 != ref.SHA256 {
					t.Fatal("independent ownership changed")
				}
			})
		}
	}
}

func TestBackupFileFirstMappingUnknownBotScope(t *testing.T) {
	archive, _, refs := botRestoreFixture(t)
	state := &fileFirstRestoreState{mapping: &fileFirstMapping{scopes: map[store.BotContentScope]store.BotContentScope{}, refs: map[store.BotContentRef]store.BotContentRef{}, paths: map[store.BotContentRef]string{}}}
	err := mapFileFirstContent(t.Context(), archive, t.TempDir(), t.TempDir(), state, nil, backup.Scope{Kind: "team", OrgSlug: refs[0].Scope.OrgID, TeamSlug: refs[0].Scope.TeamID}, PlatformRestoreOptions{})
	if err == nil || len(state.mapping.refs) != 0 {
		t.Fatal("unknown Bot scope accepted")
	}
}

func TestBackupFileFirstReconciliationPreservesQuiescence(t *testing.T) {
	id := uuid.New()
	state := &fileFirstRestoreState{homes: map[store.BotID]fileFirstBot{id: {}}}
	row := map[string]any{"id": id.String(), "status": string(store.BotActive), "active_run_id": uuid.NewString(), "lease_expires_at": time.Now(), "fence": int64(4), "revision": int64(7), "data": map[string]any{"restore_reconciliation_required": true}}
	if err := quiesceFileFirstRestoreRow("bots", row, state); err != nil {
		t.Fatal(err)
	}
	metadata, err := backupDocument(row, "data")
	if err != nil {
		t.Fatal(err)
	}
	if row["status"] != string(store.BotPaused) || row["active_run_id"] != nil || row["lease_expires_at"] != nil || backupInt(row, "fence") != 5 || backupInt(row, "revision") != 8 || metadata["restore_reconciliation_required"] != true {
		t.Fatal("restore marker reactivated Bot")
	}
	if err := validateFileFirstRootMetadata(metadata); err != nil {
		t.Fatal(err)
	}
	outbox := map[string]any{"delivered_at": nil}
	if err := quiesceFileFirstRestoreRow("bot_outbox", outbox, state); err != nil || outbox["delivered_at"] == nil {
		t.Fatal("restore left pending replay")
	}
	job := map[string]any{"mode": "bot_trigger", "status": "active"}
	if err := quiesceFileFirstRestoreRow("scheduled_jobs", job, state); err != nil || job["status"] != "paused" {
		t.Fatal("restore reactivated schedule")
	}
}

func TestBackupFileFirstReconciliationMarker(t *testing.T) {
	if err := validateFileFirstRootMetadata(map[string]any{"restore_reconciliation_required": true}); err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{false, "true", 1, map[string]any{"body": "not coordination"}, nil} {
		if err := validateFileFirstRootMetadata(map[string]any{"restore_reconciliation_required": value}); err == nil {
			t.Fatalf("invalid marker accepted: %v", value)
		}
	}
}
