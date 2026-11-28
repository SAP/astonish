package entstore

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func preparedMirrorAdvance(t *testing.T, p *store.BotPreparedPlan) store.BotPreparedPlanAdvance {
	t.Helper()
	a := execution.ActionDescriptor{Tool: "write_file", Effect: execution.EffectReversibleWrite, Arguments: json.RawMessage(`{"path":"PLAN.md","content":"PREPARED-PRIVATE-mirror"}`)}
	digest, err := a.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return store.BotPreparedPlanAdvance{OperationID: p.OperationID, ExpectedRevision: p.Revision, Action: a, ActionDigest: digest}
}

func TestBotPreparedPlanVerifierMirrorReopen(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			ctx, botID, runID, _, reopen := ApprovalScopeReopenFixture(t, scope)
			scoped, _ := reopen()
			principal, _ := execution.PrincipalFromContext(ctx)
			worker := execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: botID, RunID: runID, Version: 1, ActorID: principal.Subject, OrgSlug: "org", TeamSlug: "team"})
			lease, err := scoped.ClaimRun(worker, store.BotClaimRequest{BotID: botID, RunID: runID, WorkerID: "leaf-reopen", ExpiresAt: time.Now().Add(5 * time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			run, err := scoped.GetRun(worker, botID, runID)
			if err != nil {
				t.Fatal(err)
			}
			s := scoped.(store.BotPreparedPlanStore)
			input := preparedPlanFixture(t, run)
			input.LeafKind = store.BotPlanLeafVerifier
			p, err := s.PrepareBotPlan(worker, lease, input)
			if err != nil {
				t.Fatal(err)
			}
			change := func(q store.BotPreparedPlanChange) {
				t.Helper()
				q.OperationID, q.ExpectedRevision = p.OperationID, p.Revision
				p, err = s.ChangeBotPlan(worker, lease, q)
				if err != nil {
					t.Fatal(err)
				}
			}
			reload := func(stage store.BotPreparedPlanStage, approval uuid.UUID) {
				t.Helper()
				scoped, _ = reopen()
				s = scoped.(store.BotPreparedPlanStore)
				if approval == uuid.Nil {
					p, err = s.LoadBotPlan(worker, lease, p.OperationID)
				} else {
					p, err = s.LoadBotPlanByApproval(worker, lease, approval)
				}
				if err != nil || p.Stage != stage {
					t.Fatal("reopen", stage, p, err)
				}
			}
			approve := func() {
				t.Helper()
				bot, err := scoped.Get(ctx, botID)
				if err != nil {
					t.Fatal(err)
				}
				_, err = scoped.DecideApproval(ctx, store.BotApprovalDecision{BotID: botID, ApprovalID: p.ApprovalID, ActorID: bot.OwnerID, ActionDigest: p.ActionDigest, Status: store.ApprovalApproved})
				if err != nil {
					t.Fatal(err)
				}
			}
			assertNoPublication := func() {
				t.Helper()
				_, sessions := reopen()
				events, err := sessions.(store.BotTranscriptReader).ReadBotTranscript(worker, run.SessionID, 0, 200)
				if err != nil {
					t.Fatal(err)
				}
				for _, e := range events {
					if _, ok := e.Event.Actions.StateDelta[botPlanSnapshotKey]; ok {
						t.Fatal("snapshot published early")
					}
				}
				// reopen closes old connections; always recover current scoped handles.
				scoped, _ = reopen()
				s = scoped.(store.BotPreparedPlanStore)
			}
			change(store.BotPreparedPlanChange{Stage: store.BotPlanAwaitingApproval, ApprovalExpiresAt: time.Now().Add(time.Hour)})
			verifierApproval := p.ApprovalID
			approve()
			change(store.BotPreparedPlanChange{Stage: store.BotPlanDispatchStarted, ApprovalID: p.ApprovalID, DispatchID: uuid.New()})
			verifierDispatch := p.DispatchID
			reload(store.BotPlanDispatchStarted, verifierApproval)
			if _, err := s.AdvanceBotPlan(worker, lease, preparedMirrorAdvance(t, p)); !errors.Is(err, store.ErrBotConflict) {
				t.Fatal("advanced in-flight verifier", err)
			}
			result := json.RawMessage(`{"exit_code":0,"output":"PREPARED-PRIVATE-evidence"}`)
			engine := json.RawMessage(`{"stage":"settled","candidate":{"plan_id":"leaf-plan"},"verification":"PREPARED-PRIVATE-evidence"}`)
			badSettle := store.BotPreparedPlanChange{OperationID: p.OperationID, ExpectedRevision: p.Revision, Stage: store.BotPlanSettled, DispatchID: p.DispatchID, TrustedResult: result}
			if _, err := s.ChangeBotPlan(worker, lease, badSettle); !errors.Is(err, store.ErrBotInvalid) {
				t.Fatal("missing settled engine", err)
			}
			badSettle.EngineRecord, badSettle.Snapshot = engine, json.RawMessage(`{"plan_id":"early","revision":1,"sequence":1}`)
			if _, err := s.ChangeBotPlan(worker, lease, badSettle); !errors.Is(err, store.ErrBotInvalid) {
				t.Fatal("verifier accepted snapshot", err)
			}
			change(store.BotPreparedPlanChange{Stage: store.BotPlanSettled, DispatchID: p.DispatchID, TrustedResult: result, EngineRecord: engine})
			reload(store.BotPlanSettled, verifierApproval)
			if string(p.EngineRecord) != string(engine) || string(p.TrustedResult) != string(result) || (len(p.Snapshot) != 0 && string(p.Snapshot) != "null") {
				t.Fatal("verifier evidence", p)
			}
			if _, err := s.ChangeBotPlan(worker, lease, store.BotPreparedPlanChange{OperationID: p.OperationID, ExpectedRevision: p.Revision, Stage: store.BotPlanPublished}); !errors.Is(err, store.ErrBotConflict) {
				t.Fatal("published verifier", err)
			}
			assertNoPublication()
			advance := preparedMirrorAdvance(t, p)
			badAdvance := advance
			badAdvance.ActionDigest = strings.Repeat("0", 64)
			if _, err := s.AdvanceBotPlan(worker, lease, badAdvance); !errors.Is(err, store.ErrBotInvalid) {
				t.Fatal("bad mirror digest", err)
			}
			p, err = s.AdvanceBotPlan(worker, lease, advance)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.AdvanceBotPlan(worker, lease, advance); !errors.Is(err, store.ErrBotConflict) {
				t.Fatal("advance retry not deterministic", err)
			}
			reload(store.BotPlanPrepared, verifierApproval)
			if p.LeafKind != store.BotPlanLeafMirror || p.ApprovalID != uuid.Nil || p.DispatchID != uuid.Nil || p.Base != input.Base || len(p.History) != 1 || p.History[0].DispatchID != verifierDispatch || p.History[0].ApprovalID != verifierApproval || string(p.History[0].TrustedResult) != string(result) || string(p.History[0].EngineRecord) != string(engine) || string(p.EngineRecord) != string(engine) {
				t.Fatal("advance lost evidence or identity", p)
			}
			if _, err := s.ChangeBotPlan(worker, lease, store.BotPreparedPlanChange{OperationID: p.OperationID, ExpectedRevision: p.Revision, Stage: store.BotPlanDispatchStarted, ApprovalID: verifierApproval, DispatchID: uuid.New()}); !errors.Is(err, store.ErrBotConflict) {
				t.Fatal("old approval dispatched prepared mirror", err)
			}
			if _, err := s.ChangeBotPlan(worker, lease, store.BotPreparedPlanChange{OperationID: p.OperationID, ExpectedRevision: p.Revision, Stage: store.BotPlanDispatchStarted, DispatchID: verifierDispatch}); !errors.Is(err, store.ErrBotConflict) {
				t.Fatal("old dispatch reused", err)
			}
			change(store.BotPreparedPlanChange{Stage: store.BotPlanAwaitingApproval, ApprovalExpiresAt: time.Now().Add(time.Hour)})
			mirrorApproval := p.ApprovalID
			if mirrorApproval == verifierApproval {
				t.Fatal("approval reused")
			}
			reload(store.BotPlanAwaitingApproval, mirrorApproval)
			approve()
			if _, err := s.ChangeBotPlan(worker, lease, store.BotPreparedPlanChange{OperationID: p.OperationID, ExpectedRevision: p.Revision, Stage: store.BotPlanDispatchStarted, ApprovalID: verifierApproval, DispatchID: uuid.New()}); !errors.Is(err, store.ErrBotConflict) {
				t.Fatal("old approval dispatched awaiting mirror", err)
			}
			change(store.BotPreparedPlanChange{Stage: store.BotPlanDispatchStarted, ApprovalID: mirrorApproval, DispatchID: uuid.New()})
			reload(store.BotPlanDispatchStarted, mirrorApproval)
			if _, err := s.ChangeBotPlan(worker, lease, store.BotPreparedPlanChange{OperationID: p.OperationID, ExpectedRevision: p.Revision, Stage: store.BotPlanDispatchStarted, ApprovalID: mirrorApproval, DispatchID: uuid.New()}); !errors.Is(err, store.ErrBotConflict) {
				t.Fatal("mirror replay", err)
			}
			if _, err := s.ChangeBotPlan(worker, lease, store.BotPreparedPlanChange{OperationID: p.OperationID, ExpectedRevision: p.Revision, Stage: store.BotPlanSettled, DispatchID: p.DispatchID, TrustedResult: result}); !errors.Is(err, store.ErrBotInvalid) {
				t.Fatal("mirror missing snapshot", err)
			}
			snapshot := json.RawMessage(`{"plan_id":"leaf-plan","revision":1,"sequence":1}`)
			change(store.BotPreparedPlanChange{Stage: store.BotPlanSettled, DispatchID: p.DispatchID, TrustedResult: json.RawMessage(`{"written":true}`), Snapshot: snapshot})
			reload(store.BotPlanSettled, mirrorApproval)
			assertNoPublication()
			publish := store.BotPreparedPlanChange{OperationID: p.OperationID, ExpectedRevision: p.Revision, Stage: store.BotPlanPublished}
			p, err = s.ChangeBotPlan(worker, lease, publish)
			if err != nil {
				t.Fatal(err)
			}
			reload(store.BotPlanPublished, mirrorApproval)
			if _, err := s.ChangeBotPlan(worker, lease, publish); err != nil {
				t.Fatal("publish retry", err)
			}
			reload(store.BotPlanPublished, verifierApproval)
			_, sessions := reopen()
			events, err := sessions.(store.BotTranscriptReader).ReadBotTranscript(worker, run.SessionID, 0, 200)
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, e := range events {
				if e.Event.ID == p.EventID {
					count++
					raw, _ := json.Marshal(e.Event.Actions.StateDelta[botPlanSnapshotKey])
					if string(raw) != string(snapshot) {
						t.Fatal("wrong snapshot", string(raw))
					}
				}
			}
			if count != 1 {
				t.Fatal("publication count", count)
			}
		})
	}
}

func TestBotPreparedPlanVerifierMirrorRollback(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, _, worker, run, lease, _ := botRunFixture(t, scope)
			input := preparedPlanFixture(t, run)
			input.LeafKind = store.BotPlanLeafVerifier
			p, err := repo.PrepareBotPlan(worker, lease, input)
			if err != nil {
				t.Fatal(err)
			}
			p, err = repo.ChangeBotPlan(worker, lease, store.BotPreparedPlanChange{OperationID: p.OperationID, ExpectedRevision: p.Revision, Stage: store.BotPlanDispatchStarted, DispatchID: uuid.New()})
			if err != nil {
				t.Fatal(err)
			}
			settle := store.BotPreparedPlanChange{OperationID: p.OperationID, ExpectedRevision: p.Revision, Stage: store.BotPlanSettled, DispatchID: p.DispatchID, TrustedResult: json.RawMessage(`{"exit_code":0}`), EngineRecord: json.RawMessage(`{"settled":"PREPARED-PRIVATE-engine"}`)}
			failWrites := func() func() {
				original := repo.db
				repo.db = failRuntimeTransaction{original}
				return func() { repo.db = original }
			}
			restore := failWrites()
			if _, err := repo.ChangeBotPlan(worker, lease, settle); err == nil {
				t.Fatal("settlement write failure ignored")
			}
			restore()
			got, err := repo.LoadBotPlan(worker, lease, p.OperationID)
			if err != nil || got.Stage != store.BotPlanDispatchStarted || (len(got.TrustedResult) != 0 && string(got.TrustedResult) != "null") {
				t.Fatal("settlement rollback", got, err)
			}
			p, err = repo.ChangeBotPlan(worker, lease, settle)
			if err != nil {
				t.Fatal(err)
			}
			advance := preparedMirrorAdvance(t, p)
			restore = failWrites()
			if _, err := repo.AdvanceBotPlan(worker, lease, advance); err == nil {
				t.Fatal("advance write failure ignored")
			}
			restore()
			got, err = repo.LoadBotPlan(worker, lease, p.OperationID)
			if err != nil || got.Stage != store.BotPlanSettled || got.LeafKind != store.BotPlanLeafVerifier || len(got.History) != 0 || got.Revision != p.Revision {
				t.Fatal("advance rollback", got, err)
			}
			p, err = repo.AdvanceBotPlan(worker, lease, advance)
			if err != nil {
				t.Fatal(err)
			}
			data, codec := botRunPersistenceData(t, worker, repo.db, run)
			raw, _ := json.Marshal(data)
			if strings.Contains(string(raw), "PREPARED-PRIVATE") {
				t.Fatal("history body in SQL")
			}
			var record botRunRecord
			if err := decodeBotRecord(data, &record); err != nil {
				t.Fatal(err)
			}
			loaded, err := repo.db.ReadRun(worker, run.BotID, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			mutated := loaded.PreparedPlans[p.OperationID.String()]
			mutated.History[0].TrustedResult = json.RawMessage(`{"exit_code":99}`)
			loaded.PreparedPlans[p.OperationID.String()] = mutated
			badHash, _ := runContentDigest(mutated)
			record.Control.PreparedPlanHashes[p.OperationID.String()] = badHash
			changed, err := encodeRuntimeRecord(&record)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := codec.decodeRun(worker, run.BotID, changed); !errors.Is(err, store.ErrBotConflict) {
				t.Fatal("history integrity unchecked", err)
			}
		})
	}
}

func TestBotPreparedPlanApprovalLookupAuthority(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, _, worker, run, lease, now := botRunFixture(t, scope)
			p, err := repo.PrepareBotPlan(worker, lease, preparedPlanFixture(t, run))
			if err != nil {
				t.Fatal(err)
			}
			p, err = repo.ChangeBotPlan(worker, lease, store.BotPreparedPlanChange{OperationID: p.OperationID, ExpectedRevision: p.Revision, Stage: store.BotPlanAwaitingApproval, ApprovalExpiresAt: now.Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := repo.LoadBotPlanByApproval(worker, lease, uuid.New()); !errors.Is(err, store.ErrBotNotFound) {
				t.Fatal("unknown approval", err)
			}
			if _, err := repo.LoadBotPlanByApproval(worker, lease, uuid.Nil); !errors.Is(err, store.ErrBotInvalid) {
				t.Fatal("nil approval", err)
			}
			wrong := lease
			wrong.RunID = uuid.New()
			if _, err := repo.LoadBotPlanByApproval(worker, wrong, p.ApprovalID); err == nil {
				t.Fatal("wrong run")
			}
			wrong = lease
			wrong.Fence++
			if _, err := repo.LoadBotPlanByApproval(worker, wrong, p.ApprovalID); err == nil {
				t.Fatal("wrong fence")
			}
			wrong = lease
			wrong.WorkerID = "replacement"
			if _, err := repo.LoadBotPlanByApproval(worker, wrong, p.ApprovalID); err == nil {
				t.Fatal("wrong worker")
			}
			identity, _ := execution.BotRunIdentityFromContext(worker)
			identity.Version++
			if _, err := repo.LoadBotPlanByApproval(execution.WithBotRunIdentity(worker, identity), lease, p.ApprovalID); err == nil {
				t.Fatal("wrong version")
			}
			renewed, err := repo.RenewLease(worker, lease, lease.ExpiresAt.Add(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			*now = lease.ExpiresAt.Add(time.Second)
			if _, err := repo.LoadBotPlanByApproval(worker, renewed, p.ApprovalID); err != nil {
				t.Fatal("renewed lookup", err)
			}
			*now = renewed.ExpiresAt.Add(time.Second)
			if _, err := repo.LoadBotPlanByApproval(worker, renewed, p.ApprovalID); !errors.Is(err, store.ErrBotExpired) && !errors.Is(err, store.ErrBotConflict) {
				t.Fatal("expired lease", err)
			}
			*now = lease.ExpiresAt.Add(time.Second)
			repo.auth.check = func(context.Context, string) (string, error) { return "", store.ErrBotForbidden }
			if _, err := repo.LoadBotPlanByApproval(worker, renewed, p.ApprovalID); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatal("revoked membership", err)
			}
		})
	}
}
