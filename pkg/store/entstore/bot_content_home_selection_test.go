package entstore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestRetainedHomeActivityExcludesValidatedCleanupReceipt(t *testing.T) {
	botID := uuid.New()
	created := time.Now().UTC()
	activity := store.BotActivity{BotID: botID, Sequence: 9, Kind: store.BotActivityLifecycle, CreatedAt: created, Payload: json.RawMessage(`{"intent":"cleanup","cleanup_confirmation":{"kind":"run_teardown"}}`)}
	data, err := cleanupRecordData(activity)
	if err != nil {
		t.Fatal(err)
	}
	keep, err := retainedHomeActivity(botID, activity.Sequence, activity.Kind, created, data)
	if err != nil || keep {
		t.Fatalf("cleanup receipt entered generic Home closure: keep=%v err=%v", keep, err)
	}
	data[botCleanupRecordKey] = `{"bot_id":"00000000-0000-0000-0000-000000000000"}`
	if _, err = retainedHomeActivity(botID, activity.Sequence, activity.Kind, created, data); err == nil {
		t.Fatal("malformed cleanup receipt was silently omitted")
	}
}

func selectionFixture(t *testing.T, scope store.BotScope) (*botRepository, context.Context, store.Bot, sandbox.BotHomeBinding, *contentHomeBackend) {
	t.Helper()
	owner := uuid.New()
	ctx, err := execution.WithPrincipal(t.Context(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	backend := &contentHomeBackend{objects: map[string][]byte{}}
	platform := &Store{dataDir: t.TempDir()}
	if err := os.Mkdir(botContentDirectory(platform.dataDir), 0700); err != nil {
		t.Fatal(err)
	}
	// Make codecs before install, just like already-created scoped repositories.
	codec := platform.scopedBotContentCodec("org", "team")
	if platform.BotContentHomeInstalled() {
		t.Fatal("unexpected install")
	}
	if err = platform.InstallBotContentHomeBackend(backend); err != nil {
		t.Fatal(err)
	}
	if err = platform.InstallBotContentHomeBackend(backend); !errors.Is(err, store.ErrBotConflict) {
		t.Fatal(err)
	}
	var db botDraftDB
	if scope == store.BotScopePersonal {
		_, client := setupPersonalStore(t)
		db = personalBotDB{client: client, content: codec}
	} else {
		db = teamBotDB{client: newTeamClient(t), content: codec}
	}
	r := &botRepository{db: db, auth: botRepositoryAuth{orgSlug: "org", teamSlug: "team", userID: owner.String(), scope: scope, check: func(context.Context, string) (string, error) { return "member", nil }}}
	version := store.DefaultBotVersion()
	version.Charter = "retained instructions sentinel"
	b, err := r.CreateDraft(ctx, store.BotCreateRequest{Name: "Config", OwnerID: owner, Version: version, IdempotencyKey: "config"})
	if err != nil {
		t.Fatal(err)
	}
	home := store.BotHomeBinding{ID: uuid.New(), BotID: b.ID, Scope: scope, Backend: "docker", StorageKey: "test-home", Revision: 1}
	homeData, err := botJSONMap(home)
	if err != nil {
		t.Fatal(err)
	}
	switch d := db.(type) {
	case personalBotDB:
		err = d.client.Bot.UpdateOneID(b.ID).SetHomeBinding(homeData).Exec(ctx)
	case teamBotDB:
		err = d.client.Bot.UpdateOneID(b.ID).SetHomeBinding(homeData).Exec(ctx)
	}
	if err != nil {
		t.Fatal(err)
	}
	b.Home = home
	goal := store.BotGoal{ID: uuid.New(), BotID: b.ID, Revision: 1, Title: "Goal", Status: store.BotGoalActive, Description: "goal sentinel"}
	if err = db.InsertGoal(ctx, goal); err != nil {
		t.Fatal(err)
	}
	return r, ctx, *b, sandbox.BotHomeBinding{Scope: sandbox.BotHomeScope{OrgID: "org", TeamID: "team", UserID: owner.String(), BotID: b.ID.String()}, Backend: "docker", Ref: home.StorageKey}, backend
}
func configurationRecords(t *testing.T, r *botRepository, ctx context.Context, id store.BotID) []botHomeConfigurationRecord {
	t.Helper()
	rows, err := r.db.(botHomeConfigurationDB).homeConfigurationRecords(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}
func TestBotContentHomeConfigurationSQL(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, ctx, b, exact, backend := selectionFixture(t, scope)
			ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			seedHomeSelectionCategories(t, r, ctx, b)
			before := configurationRecords(t, r, ctx, b.ID)
			if err := r.selectHomeConfiguration(ctx, exact, backend); err != nil {
				t.Fatal(err)
			}
			after := configurationRecords(t, r, ctx, b.ID)
			if len(after) != len(before) || len(after) < 11 || reflect.DeepEqual(before, after) {
				t.Fatal("not selected")
			}
			for i, row := range after {
				oldRecord, oldPointer, _, err := homeRecordEnvelope(before[i])
				if err != nil {
					t.Fatal(err)
				}
				newRecord, newPointer, finish, err := homeRecordEnvelope(row)
				if err != nil {
					t.Fatal(err)
				}
				if err := validateHomeRecord(ctx, r.db.(botHomeConfigurationDB).contentCodec(), b.ID, row); err != nil {
					t.Fatal(row.kind, err)
				}
				if newPointer == nil {
					if !reflect.DeepEqual(oldRecord, newRecord) {
						t.Fatal("metadata index changed")
					}
					continue
				}
				oldRef, newRef := *oldPointer, *newPointer
				*newPointer = oldRef
				if err := finish(); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(oldRecord, newRecord) {
					t.Fatal("coordination metadata changed", row.kind)
				}
				if oldRef.SHA256 != newRef.SHA256 || oldRef.SizeBytes != newRef.SizeBytes || oldRef.Kind != newRef.Kind || oldRef.Revision != newRef.Revision {
					t.Fatal("selection changed logical content identity")
				}
				storage, err := r.db.(botHomeConfigurationDB).contentCodec().Open(ctx, b.ID)
				if err != nil {
					t.Fatal(err)
				}
				if contentHomeRead(t, storage, oldRef) != contentHomeRead(t, storage, newRef) {
					t.Fatal("copy changed bytes")
				}
				if err := storage.Close(); err != nil {
					t.Fatal(err)
				}
				if strings.Contains(row.data["bot_record_v2"].(string), "staged-v1/") {
					t.Fatal("staging still selected")
				}
			}
			v, err := r.GetVersion(ctx, b.ID, 1)
			if err != nil || v.Charter != "retained instructions sentinel" {
				t.Fatal(v, err)
			}
			g, err := r.ListGoals(ctx, b.ID)
			if err != nil || len(g) != 1 || g[0].Description != "goal sentinel" {
				t.Fatal(g, err)
			}
			if err = r.selectHomeConfiguration(ctx, exact, backend); err != nil {
				t.Fatal("retry", err)
			}
			if got := configurationRecords(t, r, ctx, b.ID); !reflect.DeepEqual(after, got) {
				t.Fatal("retry changed references")
			}
			// A new write routes through the exact transaction-bound codec to Home.
			if err = r.db.Transaction(ctx, func(tx botDraftDB) error {
				v.Version = 2
				return tx.InsertVersion(ctx, *v, "unchanged-logical-hash")
			}); err != nil {
				t.Fatal(err)
			}
			seedHomeSelectionCategories(t, r, ctx, b)
			rows := configurationRecords(t, r, ctx, b.ID)
			for _, row := range rows {
				if strings.Contains(row.data["bot_record_v2"].(string), "staged-v1/") {
					t.Fatal("new write staged")
				}
			}
			// Missing selected data never falls back to the still-existing stage.
			for key := range backend.objects {
				delete(backend.objects, key)
			}
			for _, row := range rows {
				_, ref, _, err := homeRecordEnvelope(row)
				if err != nil {
					t.Fatal(err)
				}
				if ref == nil {
					if err := validateHomeRecord(ctx, r.db.(botHomeConfigurationDB).contentCodec(), b.ID, row); err != nil {
						t.Fatal("inline record depended on Home", err)
					}
					continue
				}
				if err := validateHomeRecord(ctx, r.db.(botHomeConfigurationDB).contentCodec(), b.ID, row); err == nil {
					t.Fatal("missing Home fallback", row.kind)
				}
			}
			if _, err = r.GetVersion(ctx, b.ID, 1); err == nil {
				t.Fatal("missing Home fallback")
			}
		})
	}
}

type failHomeSelectionDB struct {
	botDraftDB
	botHomeConfigurationDB
}

func (d failHomeSelectionDB) selectHomeConfigurationRecords(ctx context.Context, row botStoredRecord, records []botHomeConfigurationRecord, metadata map[string]any) error {
	if err := d.botHomeConfigurationDB.selectHomeConfigurationRecords(ctx, row, records, metadata); err != nil {
		return err
	}
	if len(records) > 0 {
		return errors.New("injected SQL failure after selection")
	}
	return nil
}

type failHomeSelectionTransaction struct{ botDraftDB }

func (d failHomeSelectionTransaction) Transaction(ctx context.Context, fn func(botDraftDB) error) error {
	return d.botDraftDB.Transaction(ctx, func(tx botDraftDB) error { return fn(failHomeSelectionDB{tx, tx.(botHomeConfigurationDB)}) })
}
func TestBotContentHomeConfigurationRollback(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, failure := range []string{"corrupt-copy", "sql-after-selection", "wrong-owner", "wrong-home", "cancelled", "missing-installer"} {
			t.Run(string(scope)+"/"+failure, func(t *testing.T) {
				r, ctx, b, exact, backend := selectionFixture(t, scope)
				seedHomeSelectionCategories(t, r, ctx, b)
				before := configurationRecords(t, r, ctx, b.ID)
				original := r.db
				selectionCtx := ctx
				switch failure {
				case "cancelled":
					c, cancel := context.WithCancel(ctx)
					cancel()
					selectionCtx = c
				case "missing-installer":
					var originalCodec *botContentCodec
					switch d := r.db.(type) {
					case personalBotDB:
						originalCodec = d.content
					case teamBotDB:
						originalCodec = d.content
					}
					codec := &botContentCodec{Open: func(c context.Context, id store.BotID) (store.BotContentStaging, error) {
						lookup := c.Value(botContentLookupKey{}).(botContentLookup)
						return originalCodec.Open(context.WithValue(c, botContentLookupKey{}, botContentLookup(func(c context.Context, id store.BotID) (*botStoredRecord, error) {
							row, err := lookup(c, id)
							if err != nil {
								return nil, err
							}
							delete(row.Metadata, botContentHomeSelectionKey)
							return row, nil
						})), id)
					}}
					switch d := r.db.(type) {
					case personalBotDB:
						d.content = codec
						r.db = d
					case teamBotDB:
						d.content = codec
						r.db = d
					}
				case "corrupt-copy":
					backend.corrupt = true
				case "sql-after-selection":
					r.db = failHomeSelectionTransaction{r.db}
				case "wrong-owner":
					exact.Scope.UserID = uuid.NewString()
				case "wrong-home":
					exact.Ref = "other-home"
				}
				if err := r.selectHomeConfiguration(selectionCtx, exact, backend); err == nil {
					t.Fatal("accepted failure")
				}
				if failure == "sql-after-selection" {
					copies := 0
					for _, row := range before {
						_, ref, _, err := homeRecordEnvelope(row)
						if err != nil {
							t.Fatal(err)
						}
						if ref != nil {
							copies++
						}
					}
					if copies == 0 || len(backend.objects) < copies {
						t.Fatal("failure did not follow artifact copies")
					}
				}
				r.db = original
				for _, row := range configurationRecords(t, r, ctx, b.ID) {
					if err := validateHomeRecord(ctx, r.db.(botHomeConfigurationDB).contentCodec(), b.ID, row); err != nil {
						t.Fatal("rollback stage lost", row.kind, err)
					}
				}
				if got := configurationRecords(t, r, ctx, b.ID); !reflect.DeepEqual(before, got) {
					t.Fatal("partial SQL selection")
				}
				current, err := r.db.GetBot(ctx, b.ID)
				if err != nil {
					t.Fatal(err)
				}
				if _, ok := current.Metadata[botContentHomeSelectionKey]; ok {
					t.Fatal("partial routing selection")
				}
				if _, err = r.GetVersion(ctx, b.ID, 1); err != nil {
					t.Fatal("stage lost", err)
				}
				if _, err = r.ListGoals(ctx, b.ID); err != nil {
					t.Fatal("goal stage lost", err)
				}
			})
		}
	}
}

// seedHomeSelectionCategories uses the production write adapters and their
// normal codecs. The fixture includes nested task publication, replay metadata,
// precision-sensitive authority fields and a stored Bot-bound session event.
func seedHomeSelectionCategories(t *testing.T, r *botRepository, ctx context.Context, b store.Bot) {
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	run := botStoredRun{Run: store.BotRun{ID: uuid.New(), BotID: b.ID, Version: 1, SessionID: uuid.New(), IdempotencyKey: uuid.NewString(), ExecutionMode: execution.BotExecutionMode("interactive"), Status: store.RunQueued, CreatedAt: time.Now().UTC(), Report: store.BotReport{Summary: "run retained sentinel"}}, RequestHash: strings.Repeat("a", 64), Instructions: "run instructions sentinel"}
	must(r.db.InsertRun(ctx, run, b.OwnerID))
	approval := botContentApprovalFixture(t)
	approval.BotID = b.ID
	approval.RunID = run.Run.ID
	must(r.db.InsertApproval(ctx, approval))
	activity := activityContentFixture()
	activity.BotID = b.ID
	activity.RunID = &run.Run.ID
	activity.Sequence = time.Now().UnixNano()
	must(r.db.InsertActivity(ctx, activity, uuid.NewString()))
	skill := botContentSkillFixture()
	skill.BotID = b.ID
	for i := range skill.Provenance {
		skill.Provenance[i].BotID = b.ID
	}
	for i := range skill.Expectations {
		skill.Expectations[i].Provenance.BotID = b.ID
	}
	for i := range skill.Sources {
		skill.Sources[i].Provenance.BotID = b.ID
	}
	skill.Digest = skill.ContentDigest()
	root := botStoredSkill{Skill: store.BotSkill{ID: skill.SkillID, BotID: b.ID, Key: skill.SkillID.String(), Title: "Skill", Status: store.BotSkillDraft, Revision: 1, HeadRevision: skill.Revision, HeadDigest: skill.Digest}, Requests: map[string]botSkillReplay{"request": {Hash: strings.Repeat("b", 64)}}}
	candidate := botContentTaskFixture()
	candidate.BotID = b.ID
	candidate.Manifest.BotID = b.ID
	candidate.Manifest.Goal.BotID = b.ID
	candidate.Manifest.Skills[0].BotID = b.ID
	candidate.Ref.Digest = candidate.ContentDigest()
	task := store.BotTask{ID: candidate.Ref.TaskID, BotID: b.ID, Key: candidate.Ref.TaskID.String(), HomePath: "tasks/" + candidate.Ref.TaskID.String(), Title: "Task", Status: store.BotTaskDraft, Revision: 1}
	publication := taskPublicationEnvelope{Task: task, Mutation: store.BotTaskMutation{BotID: b.ID, AuthorityConstraintRef: "constraint"}, Publication: store.BotTaskPublication{ID: candidate.ID, BotID: b.ID, TaskID: task.ID, Candidate: candidate, ExpectedHead: candidate.Parent, AuthorityConstraintRef: "constraint", State: store.BotTaskPublicationPrepared}}
	knowledge := knowledgeContentFixture()
	knowledge.Record.Input.Source.BotID = b.ID
	knowledge.Record.Input.Source.RunID = run.Run.ID
	knowledge.Lease.BotID = b.ID
	goals, err := r.db.ReadGoals(ctx, b.ID)
	must(err)
	tracked := store.BotTrackedItem{ID: uuid.New(), GoalID: goals[0].ID, Title: "Tracked", Status: store.BotTrackedOpen}
	codec := r.db.(botHomeConfigurationDB).contentCodec()
	event, err := codec.encodeSessionEvent(ctx, b.ID, run.Run.SessionID.String(), botSessionEventFixture())
	must(err)
	switch d := r.db.(type) {
	case personalBotDB:
		must(d.writeSkill(ctx, root, 0))
		must(d.insertSkillRevision(ctx, skill))
		must(d.writeTask(ctx, task, 0))
		must(d.insertTaskPublication(ctx, publication))
		must(d.saveKnowledge(ctx, knowledge, 0))
		must(d.WriteTrackedItem(ctx, tracked, 1))
		must(d.client.SessionEvent.Create().SetSessionID(run.Run.SessionID.String()).SetEventData(event).Exec(ctx))
	case teamBotDB:
		must(d.writeSkill(ctx, root, 0))
		must(d.insertSkillRevision(ctx, skill))
		must(d.writeTask(ctx, task, 0))
		must(d.insertTaskPublication(ctx, publication))
		must(d.saveKnowledge(ctx, knowledge, 0))
		must(d.WriteTrackedItem(ctx, tracked, 1))
		must(d.client.SessionEvent.Create().SetSessionID(run.Run.SessionID.String()).SetEventData(event).Exec(ctx))
	}
	for _, row := range configurationRecords(t, r, ctx, b.ID) {
		must(validateHomeRecord(ctx, codec, b.ID, row))
	}
}
