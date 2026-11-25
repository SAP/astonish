package entstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/backup"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	adksession "google.golang.org/adk/session"
)

func botFileTestScope() store.BotContentScope {
	return store.BotContentScope{OrgID: "org", TeamID: "team", OwnerID: uuid.NewString(), BotID: uuid.New()}
}
func botFileTestRef(scope store.BotContentScope, kind store.BotContentKind, raw []byte, retained bool) store.BotContentRef {
	sum := sha256.Sum256(raw)
	key := "staged-v1/" + string(kind) + "-1-" + uuid.NewString()
	if retained {
		key = "content-v1/" + string(kind) + "-1-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	return store.BotContentRef{Format: store.BotContentFormatVersion, Scope: scope, Kind: kind, Key: key, SHA256: hex.EncodeToString(sum[:]), SizeBytes: int64(len(raw)), Revision: 1}
}
func botFileTestEnvelope(t *testing.T, value any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{"bot_record_v2": string(raw)}
}
func TestBotFileInventoryRoots(t *testing.T) {
	scope := botFileTestScope()
	templateScope := store.BotContentScope{OrgID: scope.OrgID, TeamID: scope.TeamID}
	intakeScope := store.BotContentScope{OrgID: scope.OrgID, TeamID: scope.TeamID, OwnerID: scope.OwnerID, IntakeID: uuid.New()}
	inv, err := newBotBackupInventory([]store.BotContentScope{scope, templateScope, intakeScope})
	if err != nil {
		t.Fatal(err)
	}
	ref := botFileTestRef(scope, store.BotContentDocument, []byte("document"), false)
	if err = inv.rootsFromHome(scope.BotID, []botHomeConfigurationRecord{{kind: botHomeVersion, data: botFileTestEnvelope(t, botVersionRecord{Format: botDiskRecordFormat, Content: ref})}}); err != nil {
		t.Fatal(err)
	}
	memory := botFileTestRef(scope, store.BotContentMemory, []byte("memory"), false)
	if err = inv.rootsFromMemory(scope.BotID, map[string]any{botMemoryHeadKey: memory, "authority_constraint_ref": "not-content"}); err != nil {
		t.Fatal(err)
	}
	event := adksession.NewEvent("initial-state")
	event.ID = "initial-state"
	eventCodec := newBotContentCodec(t.TempDir(), scope)
	initial, err := eventCodec.encodeSessionEvent(context.Background(), scope.BotID, "session", event)
	if err != nil {
		t.Fatal(err)
	}
	if err = inv.rootsFromSession(scope.BotID, "session", map[string]any{"bot_initial_state": initial}); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		scope store.BotContentScope
		kind  store.BotContentKind
	}{{templateScope, store.BotContentTemplate}, {intakeScope, store.BotContentIntake}} {
		id := uuid.New()
		r := botFileTestRef(item.scope, item.kind, []byte("independent"), false)
		if err = inv.rootFromIndependent(id, item.scope, item.kind, 1, botFileTestEnvelope(t, botIndependentRecord{Format: botDiskRecordFormat, ID: id, Content: r})); err != nil {
			t.Fatal(err)
		}
	}
	if len(inv.objects) != 4 {
		t.Fatalf("roots=%d", len(inv.objects))
	}
	if err = inv.add(inv.objects[0]); err != nil || len(inv.objects) != 4 {
		t.Fatal("dedup", err)
	}
	bad := inv.objects[0]
	bad.Ref.SizeBytes++
	if !errors.Is(inv.add(bad), store.ErrBotConflict) {
		t.Fatal("accepted conflicting ref")
	}
	bad = inv.objects[0]
	bad.Ref.Scope.BotID = uuid.New()
	if !errors.Is(inv.add(bad), store.ErrBotForbidden) {
		t.Fatal("accepted foreign ref")
	}
	bad = inv.objects[0]
	bad.Ref.Key = "../../escape"
	if inv.add(bad) == nil {
		t.Fatal("accepted traversal")
	}
	bad = inv.objects[0]
	bad.Codec = "future"
	if !errors.Is(inv.add(bad), store.ErrBotUnsupported) {
		t.Fatal("accepted unknown codec")
	}
	if inv.rootsFromHome(scope.BotID, []botHomeConfigurationRecord{{kind: botHomeVersion, data: map[string]any{"inline": "body"}}}) == nil {
		t.Fatal("accepted inline development row")
	}
}

func TestBotFileArchiveStreamingClosure(t *testing.T) {
	ctx := context.Background()
	scope := botFileTestScope()
	disk, err := newBotDiskContent(t.TempDir(), scope)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	// Stage one object through the real local immutable content implementation.
	memoryRaw, _ := json.Marshal(botDiskMemoryCatalogue{Format: 1, BotID: scope.BotID})
	memory, err := disk.Put(ctx, store.BotContentMemory, 1, int64(len(memoryRaw)), bytes.NewReader(memoryRaw), func(f func() error) error { return f() })
	if err != nil {
		t.Fatal(err)
	}
	large := bytes.Repeat([]byte("binary-sentinel\x00"), (18<<20)/len("binary-sentinel\x00")+1)
	if len(large) <= 16<<20 {
		t.Fatal("fixture must exceed old ceiling")
	}
	artifact := botFileTestRef(scope, store.BotContentArtifact, large, true)
	a := store.BotArtifact{ContentRef: &artifact, SHA256: artifact.SHA256, SizeBytes: artifact.SizeBytes}
	runRaw, _ := json.Marshal(botRunContent{Format: botRuntimeRecordFormat, Stored: botStoredRun{Run: store.BotRun{BotID: scope.BotID, Report: store.BotReport{Artifacts: []store.BotArtifact{a}}}, Work: &store.BotWorkState{Candidate: &store.BotWorkCandidate{Artifacts: []store.BotArtifact{a}}}}})
	run := botFileTestRef(scope, store.BotContentCheckpoint, runRaw, true)
	activityPayload, _ := json.Marshal(store.BotPublication{Artifacts: []store.BotArtifact{a}})
	activityRaw, _ := json.Marshal(botActivityContent{Activity: store.BotActivity{BotID: scope.BotID, Kind: store.BotActivityReport, Payload: activityPayload}})
	activity := botFileTestRef(scope, store.BotContentArtifact, activityRaw, true)
	eventRaw, _ := json.Marshal(botSessionEventContent{BotID: scope.BotID, SessionID: "s", Event: &adksession.Event{ID: "event"}})
	event := botFileTestRef(scope, store.BotContentEvidence, eventRaw, false)
	bodies := map[string][]byte{run.Key: runRaw, artifact.Key: large, activity.Key: activityRaw, event.Key: eventRaw}
	roots := []botBackupObject{{Ref: run, Codec: botBackupRun}, {Ref: *memory, Codec: botBackupMemory}, {Ref: activity, Codec: botBackupActivity}, {Ref: event, Codec: botBackupSession}}
	fenced := false
	opened := 0
	largestRead := 0
	opts := botFileBackupExportOptions{Scopes: []store.BotContentScope{scope}, Compression: backup.CompressionNone,
		Fence: func(_ context.Context, f func() error) error {
			fenced = true
			defer func() { fenced = false }()
			return f()
		},
		Collect: func(_ context.Context, i *botBackupInventory) error {
			if !fenced {
				t.Fatal("unfenced roots")
			}
			for _, r := range roots {
				if err := i.add(r); err != nil {
					return err
				}
			}
			return nil
		},
		Open: func(ctx context.Context, r store.BotContentRef) (*store.BotFileStream, error) {
			if !fenced {
				t.Fatal("unfenced object")
			}
			opened++
			if r == *memory {
				return disk.Open(ctx, r, store.BotFileRange{})
			}
			raw, ok := bodies[r.Key]
			if !ok {
				return nil, os.ErrNotExist
			}
			return &store.BotFileStream{ReadCloser: &botFileSizedTestReader{Reader: bytes.NewReader(raw), max: &largestRead}}, nil
		},
	}
	path := filepath.Join(t.TempDir(), "content.tar")
	if err = exportBotFileContent(ctx, path, opts); err != nil {
		t.Fatal(err)
	}
	if opened != 5 {
		t.Fatalf("opens=%d, expected roots + deduplicated artifact", opened)
	}
	if largestRead > 32<<10 {
		t.Fatalf("unbounded read %d", largestRead)
	}
	if err = validateBotFileContentArchive(ctx, path, backup.ReaderOptions{}, opts.Scopes, roots); err != nil {
		t.Fatal(err)
	}
	if _, err = backup.Verify(path); err != nil {
		t.Fatal(err)
	}
	reader, err := backup.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	artifactFound := false
	if err = reader.ForEachFile(func(p string, r io.Reader) error {
		if p == botBackupObjectPath(botBackupObject{Ref: artifact}) {
			artifactFound = true
			sum := sha256.New()
			n, e := io.Copy(sum, r)
			if e != nil {
				return e
			}
			if n != artifact.SizeBytes || hex.EncodeToString(sum.Sum(nil)) != artifact.SHA256 {
				t.Fatal("changed exact bytes")
			}
		}
		if p == botBackupIndexPath {
			raw, e := io.ReadAll(r)
			if e != nil {
				return e
			}
			if bytes.Contains(raw, []byte("binary-sentinel")) {
				t.Fatal("body leaked into index")
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !artifactFound {
		t.Fatal("missing artifact")
	}
	if err = validateBotFileContentArchive(ctx, path, backup.ReaderOptions{}, []store.BotContentScope{botFileTestScope()}, roots); err == nil {
		t.Fatal("accepted foreign scope")
	}
	if err = validateBotFileContentArchive(ctx, path, backup.ReaderOptions{}, opts.Scopes, roots[:1]); err == nil {
		t.Fatal("accepted omitted SQL roots")
	}
	// Raw tampering is rejected even when the content reader itself is untrusted.
	for _, name := range []string{"missing", "corrupt", "short", "oversized", "cancel", "no-fence", "swallowed-error"} {
		t.Run(name, func(t *testing.T) {
			bad := opts
			bad.Open = func(c context.Context, r store.BotContentRef) (*store.BotFileStream, error) {
				if r.Key == artifact.Key {
					switch name {
					case "missing", "swallowed-error":
						return nil, os.ErrNotExist
					case "corrupt":
						return &store.BotFileStream{ReadCloser: io.NopCloser(bytes.NewReader(bytes.Repeat([]byte("x"), len(large))))}, nil
					case "short":
						return &store.BotFileStream{ReadCloser: io.NopCloser(bytes.NewReader(large[:len(large)-1]))}, nil
					case "oversized":
						return &store.BotFileStream{ReadCloser: io.NopCloser(io.MultiReader(bytes.NewReader(large), strings.NewReader("x")))}, nil
					}
				}
				return opts.Open(c, r)
			}
			target := filepath.Join(t.TempDir(), "untouched.tar")
			if err := os.WriteFile(target, []byte("old"), 0600); err != nil {
				t.Fatal(err)
			}
			c := ctx
			if name == "cancel" {
				var cancel context.CancelFunc
				c, cancel = context.WithCancel(ctx)
				cancel()
			}
			if name == "no-fence" {
				bad.Fence = nil
			}
			if name == "swallowed-error" {
				bad.Fence = func(_ context.Context, f func() error) error {
					fenced = true
					defer func() { fenced = false }()
					_ = f()
					return nil
				}
			}
			if exportBotFileContent(c, target, bad) == nil {
				t.Fatal("accepted invalid source")
			}
			raw, e := os.ReadFile(target)
			if e != nil || string(raw) != "old" {
				t.Fatal("replaced target on failure", e)
			}
			entries, e := os.ReadDir(filepath.Dir(target))
			if e != nil || len(entries) != 1 {
				t.Fatal("leaked spool", e)
			}
		})
	}
	// Corrupt an archived payload without touching index metadata.
	f, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteAt([]byte("!"), 512); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	if validateBotFileContentArchive(ctx, path, backup.ReaderOptions{}, opts.Scopes, roots) == nil {
		t.Fatal("accepted tampered archive")
	}
}

func TestBotFileArchiveIndependentOwnership(t *testing.T) {
	ctx := t.Context()
	team := store.BotContentScope{OrgID: "org", TeamID: "team"}
	intake := store.BotContentScope{OrgID: "org", TeamID: "team", OwnerID: uuid.NewString(), IntakeID: uuid.New()}
	directory := t.TempDir()
	disks := map[store.BotContentScope]*botDiskContent{}
	refs := map[store.BotContentScope]store.BotContentRef{}
	ids := map[store.BotContentScope]uuid.UUID{team: uuid.New(), intake: intake.IntakeID}
	for _, scope := range []store.BotContentScope{team, intake} {
		disk, err := newBotDiskContent(directory, scope)
		if err != nil {
			t.Fatal(err)
		}
		defer disk.Close()
		disks[scope] = disk
		kind := store.BotContentTemplate
		if scope.IntakeID != uuid.Nil {
			kind = store.BotContentIntake
		}
		raw := []byte(`{"charter":"independent-owned-sentinel"}`)
		ref, err := disk.Put(ctx, kind, 1, int64(len(raw)), bytes.NewReader(raw), func(f func() error) error { return f() })
		if err != nil {
			t.Fatal(err)
		}
		refs[scope] = *ref
	}
	var roots []botBackupObject
	opts := botFileBackupExportOptions{Scopes: []store.BotContentScope{team, intake}, Compression: backup.CompressionNone,
		Fence: func(_ context.Context, f func() error) error { return f() },
		Collect: func(_ context.Context, inv *botBackupInventory) error {
			for _, scope := range []store.BotContentScope{team, intake} {
				ref := refs[scope]
				if err := inv.rootFromIndependent(ids[scope], scope, ref.Kind, 1, botFileTestEnvelope(t, botIndependentRecord{Format: botDiskRecordFormat, ID: ids[scope], Content: ref})); err != nil {
					return err
				}
			}
			roots = append([]botBackupObject(nil), inv.objects...)
			return nil
		},
		Open: func(c context.Context, ref store.BotContentRef) (*store.BotFileStream, error) {
			return disks[ref.Scope].Open(c, ref, store.BotFileRange{})
		},
	}
	path := filepath.Join(t.TempDir(), "independent.tar")
	if err := exportBotFileContent(ctx, path, opts); err != nil {
		t.Fatal(err)
	}
	if err := validateBotFileContentArchive(ctx, path, backup.ReaderOptions{}, opts.Scopes, roots); err != nil {
		t.Fatal(err)
	}
	if err := validateBotFileContentArchive(ctx, path, backup.ReaderOptions{}, []store.BotContentScope{team}, roots); err == nil {
		t.Fatal("intake silently included under team ownership")
	}
}

func TestBotFileInventoryTypedEdges(t *testing.T) {
	scope := botFileTestScope()
	ref := botFileTestRef(scope, store.BotContentArtifact, []byte("bytes"), true)
	artifact := store.BotArtifact{ContentRef: &ref, SHA256: ref.SHA256, SizeBytes: ref.SizeBytes}
	for _, receipt := range []bool{false, true} {
		var payload []byte
		if receipt {
			payload, _ = json.Marshal(store.BotPublication{Artifacts: []store.BotArtifact{artifact}})
		} else {
			payload, _ = json.Marshal(struct {
				Report store.BotReport `json:"report"`
			}{store.BotReport{Artifacts: []store.BotArtifact{artifact}}})
		}
		raw, _ := json.Marshal(botActivityContent{Activity: store.BotActivity{BotID: scope.BotID, Kind: store.BotActivityReport, Payload: payload}})
		object := botBackupObject{Ref: botFileTestRef(scope, store.BotContentArtifact, raw, false), Codec: botBackupActivity}
		inv, _ := newBotBackupInventory([]store.BotContentScope{scope})
		if err := inv.children(object, bytes.NewReader(raw)); err != nil || len(inv.objects) != 1 {
			t.Fatal("report edge", receipt, err)
		}
	}
	// A reference-shaped action argument is not a typed storage edge.
	argument, _ := json.Marshal(ref)
	raw, _ := json.Marshal(botApprovalContent{BotID: scope.BotID, Version: 1, Arguments: argument})
	object := botBackupObject{Ref: botFileTestRef(scope, store.BotContentCheckpoint, raw, false), Codec: botBackupApproval}
	inv, _ := newBotBackupInventory([]store.BotContentScope{scope})
	if err := inv.children(object, bytes.NewReader(raw)); err != nil || len(inv.objects) != 0 {
		t.Fatal("authority interpreted as content", err)
	}
	foreign := artifact
	copied := ref
	copied.Scope.BotID = uuid.New()
	foreign.ContentRef = &copied
	raw, _ = json.Marshal(botRunContent{Format: botRuntimeRecordFormat, Stored: botStoredRun{Run: store.BotRun{BotID: scope.BotID, Report: store.BotReport{Artifacts: []store.BotArtifact{foreign}}}}})
	object = botBackupObject{Ref: botFileTestRef(scope, store.BotContentCheckpoint, raw, false), Codec: botBackupRun}
	if inv.children(object, bytes.NewReader(raw)) == nil {
		t.Fatal("foreign nested edge")
	}
}

func TestBotFileInventoryAllEnvelopeCategories(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, bot, _, _ := selectionFixture(t, scope)
			seedHomeSelectionCategories(t, repo, ctx, bot)
			adapter := repo.db.(botHomeConfigurationDB)
			codec := adapter.contentCodec()
			// The shared Home fixture uses a display-only report artifact. Backup
			// needs an actual immutable deliverable; keep the strict closure gate.
			for _, row := range configurationRecords(t, repo, ctx, bot.ID) {
				if row.kind != botHomeActivity {
					continue
				}
				activity, err := codec.decodeActivity(ctx, bot.ID, row.revision, row.activityKind, row.createdAt, row.data)
				if err != nil {
					t.Fatal(err)
				}
				if activity.Kind != store.BotActivityReport {
					continue
				}
				disk, err := codec.Open(ctx, bot.ID)
				if err != nil {
					t.Fatal(err)
				}
				ref, err := disk.Put(ctx, store.BotContentArtifact, 1, 4, strings.NewReader("real"), contentHomeCommit)
				_ = disk.Close()
				if err != nil {
					t.Fatal(err)
				}
				activity.Payload, _ = json.Marshal(map[string]any{"report": store.BotReport{Artifacts: []store.BotArtifact{{ContentRef: ref, SHA256: ref.SHA256, SizeBytes: ref.SizeBytes}}}})
				data, err := codec.encodeActivity(ctx, *activity)
				if err != nil {
					t.Fatal(err)
				}
				switch d := repo.db.(type) {
				case personalBotDB:
					err = d.client.BotOutbox.UpdateOneID(row.id).SetPayload(data).Exec(ctx)
				case teamBotDB:
					err = d.client.BotOutbox.UpdateOneID(row.id).SetPayload(data).Exec(ctx)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			rows := configurationRecords(t, repo, ctx, bot.ID)
			var scopes []store.BotContentScope
			runtimeScope, err := codec.RuntimeScope(ctx, bot.ID)
			if err != nil {
				t.Fatal(err)
			}
			scopes = append(scopes, runtimeScope)
			for _, row := range rows {
				_, ref, _, err := homeRecordEnvelope(row)
				if err != nil {
					t.Fatal(err)
				}
				if ref != nil {
					scopes = append(scopes, ref.Scope)
				}
			}
			inv, err := newBotBackupInventory(scopes)
			if err != nil {
				t.Fatal(err)
			}
			if err = collectBotBackupRoots(ctx, repo.db, bot.ID, inv); err != nil {
				t.Fatal(err)
			}
			if len(rows) < 10 {
				t.Fatalf("incomplete logical envelope inventory: %d", len(rows))
			}
			// Runtime rows remain in logical inventory, but only the six workspace
			// bodies and the saved report artifact are external byte roots.
			if len(inv.objects) != 7 {
				t.Fatalf("incomplete physical envelope inventory: %d", len(inv.objects))
			}
		})
	}
}

func TestBotFileInventoryScopedSQL(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, bot, _, _ := selectionFixture(t, scope)
			rows := configurationRecords(t, repo, ctx, bot.ID)
			var scopes []store.BotContentScope
			codec := repo.db.(botHomeConfigurationDB).contentCodec()
			runtimeScope, err := codec.RuntimeScope(ctx, bot.ID)
			if err != nil {
				t.Fatal(err)
			}
			scopes = append(scopes, runtimeScope)
			for _, row := range rows {
				_, ref, _, err := homeRecordEnvelope(row)
				if err != nil {
					t.Fatal(err)
				}
				if ref != nil {
					scopes = append(scopes, ref.Scope)
				}
			}
			inv, err := newBotBackupInventory(scopes)
			if err != nil {
				t.Fatal(err)
			}
			if err = collectBotBackupRoots(ctx, repo.db, bot.ID, inv); err != nil {
				t.Fatal(err)
			}
			if len(inv.objects) < 2 {
				t.Fatal("missing SQL roots")
			}
			roots := append([]botBackupObject(nil), inv.objects...)
			path := filepath.Join(t.TempDir(), "scope.tar")
			opts := botFileBackupExportOptions{Scopes: scopes, Compression: backup.CompressionNone,
				Fence: func(_ context.Context, f func() error) error { return f() },
				Collect: func(c context.Context, i *botBackupInventory) error {
					return collectBotBackupRoots(c, repo.db, bot.ID, i)
				},
				Open: func(c context.Context, ref store.BotContentRef) (*store.BotFileStream, error) {
					disk, err := repo.db.(botHomeConfigurationDB).contentCodec().Open(c, bot.ID)
					if err != nil {
						return nil, err
					}
					stream, err := disk.Open(c, ref, store.BotFileRange{})
					if err != nil {
						_ = disk.Close()
						return nil, err
					}
					stream.ReadCloser = &botFileOwnedTestStream{ReadCloser: stream.ReadCloser, disk: disk}
					return stream, nil
				},
			}
			if err = exportBotFileContent(ctx, path, opts); err != nil {
				t.Fatal(err)
			}
			if err = validateBotFileContentArchive(ctx, path, backup.ReaderOptions{}, scopes, roots); err != nil {
				t.Fatal(err)
			}
			for _, row := range configurationRecords(t, repo, ctx, bot.ID) {
				raw, err := json.Marshal(row.data)
				if err != nil {
					t.Fatal(err)
				}
				if bytes.Contains(raw, []byte("sentinel")) {
					t.Fatal("SQL content body")
				}
			}
		})
	}
}

type botFileOwnedTestStream struct {
	io.ReadCloser
	disk   store.BotContentStaging
	closed bool
}

func (s *botFileOwnedTestStream) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	return errors.Join(s.ReadCloser.Close(), s.disk.Close())
}

type botFileSizedTestReader struct {
	*bytes.Reader
	max *int
}

func (r *botFileSizedTestReader) Read(p []byte) (int, error) {
	if len(p) > *r.max {
		*r.max = len(p)
	}
	return r.Reader.Read(p)
}
func (r *botFileSizedTestReader) Close() error { return nil }
