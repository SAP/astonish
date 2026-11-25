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

func preparedPlanFixture(t *testing.T, run *store.BotRun) store.BotPreparedPlan {
	t.Helper()
	a := execution.ActionDescriptor{Tool: "shell_command", Effect: execution.EffectRead, Arguments: json.RawMessage(`{"command":"PREPARED-PRIVATE-command"}`)}
	digest, err := a.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return store.BotPreparedPlan{BotID: run.BotID, RunID: run.ID, SessionID: run.SessionID, Version: run.Version, OperationID: uuid.New(), Action: a, ActionDigest: digest, EngineRecord: json.RawMessage(`{"markdown":"PREPARED-PRIVATE-markdown"}`)}
}
func preparedPlanStore(repo *botRepository) store.BotPreparedPlanStore {
	switch d := repo.db.(type) {
	case personalBotDB:
		return &personalBotStore{client: d.client, content: d.content, auth: repo.auth}
	case teamBotDB:
		return &teamBotStore{client: d.client, content: d.content, auth: repo.auth}
	}
	panic("unsupported test scope")
}
func TestBotPreparedPlanExactApprovalPublication(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, owner, worker, run, lease, now := botRunFixture(t, scope)
			s := preparedPlanStore(repo)
			input := preparedPlanFixture(t, run)
			p, err := s.PrepareBotPlan(worker, lease, input)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.PrepareBotPlan(worker, lease, input); err != nil {
				t.Fatal("prepare retry", err)
			}
			changed := input
			changed.EngineRecord = json.RawMessage(`{"different":true}`)
			if _, err = s.PrepareBotPlan(worker, lease, changed); !errors.Is(err, store.ErrBotConflict) {
				t.Fatal("conflicting prepare", err)
			}
			p, err = s.ChangeBotPlan(worker, lease, store.BotPreparedPlanChange{OperationID: p.OperationID, ExpectedRevision: p.Revision, Stage: store.BotPlanAwaitingApproval, ApprovalExpiresAt: now.Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			start := store.BotPreparedPlanChange{OperationID: p.OperationID, ExpectedRevision: p.Revision, Stage: store.BotPlanDispatchStarted, ApprovalID: p.ApprovalID, DispatchID: uuid.New()}
			if _, err = s.ChangeBotPlan(worker, lease, start); !errors.Is(err, store.ErrBotConflict) {
				t.Fatal("pending approval dispatched", err)
			}
			b, err := repo.db.GetBot(owner, run.BotID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = repo.DecideApproval(owner, store.BotApprovalDecision{BotID: run.BotID, ApprovalID: p.ApprovalID, ActorID: b.Bot.OwnerID, ActionDigest: p.ActionDigest, Status: store.ApprovalApproved}); err != nil {
				t.Fatal(err)
			}
			bad := start
			bad.ApprovalID = uuid.New()
			if _, err = s.ChangeBotPlan(worker, lease, bad); !errors.Is(err, store.ErrBotConflict) {
				t.Fatal("wrong approval", err)
			}
			p, err = s.ChangeBotPlan(worker, lease, start)
			if err != nil {
				t.Fatal(err)
			}
			a, err := repo.db.ReadApproval(worker, run.BotID, p.ApprovalID)
			if err != nil || a.Status != store.ApprovalConsumed {
				t.Fatal("approval not consumed", a, err)
			}
			// A lost dispatch response is not a license to replay, including a fresh CAS.
			start.ExpectedRevision = p.Revision
			if _, err = s.ChangeBotPlan(worker, lease, start); !errors.Is(err, store.ErrBotConflict) {
				t.Fatal("dispatch replay", err)
			}
			s = preparedPlanStore(repo) // no process-local operation memory
			loaded, err := s.LoadBotPlan(worker, lease, p.OperationID)
			if err != nil || loaded.Stage != store.BotPlanDispatchStarted {
				t.Fatal("reload", loaded, err)
			}
			settle := store.BotPreparedPlanChange{OperationID: p.OperationID, ExpectedRevision: p.Revision, Stage: store.BotPlanSettled, DispatchID: p.DispatchID, TrustedResult: json.RawMessage(`{"stdout":"PREPARED-PRIVATE-output","exit_code":0}`), Snapshot: json.RawMessage(`{"plan_id":"plan-one","revision":1,"sequence":1,"goal":"PREPARED-PRIVATE-goal"}`)}
			p, err = s.ChangeBotPlan(worker, lease, settle)
			if err != nil {
				t.Fatal(err)
			}
			data, _ := botRunPersistenceData(t, worker, repo.db, run)
			raw, _ := json.Marshal(data)
			if strings.Contains(string(raw), "PREPARED-PRIVATE") {
				t.Fatal("prepared body leaked to SQL")
			}
			publish := store.BotPreparedPlanChange{OperationID: p.OperationID, ExpectedRevision: p.Revision, Stage: store.BotPlanPublished}
			p, err = s.ChangeBotPlan(worker, lease, publish)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.ChangeBotPlan(worker, lease, publish); err != nil {
				t.Fatal("publication retry", err)
			}
			publish.Snapshot = json.RawMessage(`{"different":true}`)
			if _, err = s.ChangeBotPlan(worker, lease, publish); !errors.Is(err, store.ErrBotInvalid) {
				t.Fatal("replacement publication", err)
			}
			events, err := preparedPlanEvents(worker, repo.db, *run)
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, e := range events {
				if e.ID == p.EventID {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("publication count %d", count)
			}
			next := preparedPlanFixture(t, run)
			if _, err = s.PrepareBotPlan(worker, lease, next); !errors.Is(err, store.ErrBotConflict) {
				t.Fatal("stale base", err)
			}
			next.Base = store.BotPlanBase{PlanID: "plan-one", Revision: 1, Sequence: 1}
			if _, err = s.PrepareBotPlan(worker, lease, next); err != nil {
				t.Fatal("next operation", err)
			}
		})
	}
}
func TestBotPreparedPlanSQLiteReopen(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			ctx, botID, runID, _, reopen := ApprovalScopeReopenFixture(t, scope)
			scoped, _ := reopen()
			principal, _ := execution.PrincipalFromContext(ctx)
			worker := execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: botID, RunID: runID, Version: 1, ActorID: principal.Subject, OrgSlug: "org", TeamSlug: "team"})
			lease, err := scoped.ClaimRun(worker, store.BotClaimRequest{BotID: botID, RunID: runID, WorkerID: "prepared-reopen", ExpiresAt: time.Now().Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			run, err := scoped.GetRun(worker, botID, runID)
			if err != nil {
				t.Fatal(err)
			}
			s := scoped.(store.BotPreparedPlanStore)
			p, err := s.PrepareBotPlan(worker, lease, preparedPlanFixture(t, run))
			if err != nil {
				t.Fatal(err)
			}
			p, err = s.ChangeBotPlan(worker, lease, store.BotPreparedPlanChange{OperationID: p.OperationID, ExpectedRevision: p.Revision, Stage: store.BotPlanDispatchStarted, DispatchID: uuid.New()})
			if err != nil {
				t.Fatal(err)
			}
			scoped, _ = reopen()
			s = scoped.(store.BotPreparedPlanStore)
			p, err = s.LoadBotPlan(worker, lease, p.OperationID)
			if err != nil || p.Stage != store.BotPlanDispatchStarted {
				t.Fatal("reopen dispatch", p, err)
			}
			p, err = s.ChangeBotPlan(worker, lease, store.BotPreparedPlanChange{OperationID: p.OperationID, ExpectedRevision: p.Revision, Stage: store.BotPlanSettled, DispatchID: p.DispatchID, TrustedResult: json.RawMessage(`{"exit_code":0}`), Snapshot: json.RawMessage(`{"plan_id":"reopened","revision":1,"sequence":1}`)})
			if err != nil {
				t.Fatal(err)
			}
			scoped, _ = reopen()
			s = scoped.(store.BotPreparedPlanStore)
			p, err = s.LoadBotPlan(worker, lease, p.OperationID)
			if err != nil || p.Stage != store.BotPlanSettled {
				t.Fatal("reopen settled", p, err)
			}
			q := store.BotPreparedPlanChange{OperationID: p.OperationID, ExpectedRevision: p.Revision, Stage: store.BotPlanPublished}
			if _, err = s.ChangeBotPlan(worker, lease, q); err != nil {
				t.Fatal(err)
			}
			scoped, sessions := reopen()
			s = scoped.(store.BotPreparedPlanStore)
			if _, err = s.ChangeBotPlan(worker, lease, q); err != nil {
				t.Fatal("reopen publication retry", err)
			}
			events, err := sessions.(store.BotTranscriptReader).ReadBotTranscript(worker, run.SessionID, 0, 200)
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, e := range events {
				if e.Event.ID == p.EventID {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("reopen count %d", count)
			}
		})
	}
}

func TestBotPreparedPlanApprovalRollbackAndIntegrity(t *testing.T) {
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
			// Approve through the normal owner decision boundary.
			b, err := repo.db.GetBot(worker, run.BotID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = repo.DecideApproval(worker, store.BotApprovalDecision{BotID: run.BotID, ApprovalID: p.ApprovalID, ActorID: b.Bot.OwnerID, ActionDigest: p.ActionDigest, Status: store.ApprovalApproved}); err != nil {
				t.Fatal(err)
			}
			original := repo.db
			repo.db = failRuntimeTransaction{original}
			start := store.BotPreparedPlanChange{OperationID: p.OperationID, ExpectedRevision: p.Revision, Stage: store.BotPlanDispatchStarted, ApprovalID: p.ApprovalID, DispatchID: uuid.New()}
			if _, err = repo.ChangeBotPlan(worker, lease, start); err == nil {
				t.Fatal("injected content failure ignored")
			}
			repo.db = original
			a, err := repo.db.ReadApproval(worker, run.BotID, p.ApprovalID)
			if err != nil || a.Status != store.ApprovalApproved {
				t.Fatal("rollback consumed approval", a, err)
			}
			got, err := repo.LoadBotPlan(worker, lease, p.OperationID)
			if err != nil || got.Stage != store.BotPlanAwaitingApproval {
				t.Fatal("rollback changed stage", got, err)
			}
			data, codec := botRunPersistenceData(t, worker, repo.db, run)
			var record botRunRecord
			if err = decodeBotRecord(data, &record); err != nil {
				t.Fatal(err)
			}
			record.Control.PreparedPlanHashes[p.OperationID.String()] = strings.Repeat("0", 64)
			changed, err := encodeRuntimeRecord(&record)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = codec.decodeRun(worker, run.BotID, changed); !errors.Is(err, store.ErrBotConflict) {
				t.Fatal("SQL digest tampering accepted", err)
			}
			stored, err := repo.db.ReadRun(worker, run.BotID, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			_ = stored
		})
	}
}

func TestBotPreparedPlanAuthorityAndUncertain(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, owner, worker, run, lease, now := botRunFixture(t, scope)
			p, err := repo.PrepareBotPlan(worker, lease, preparedPlanFixture(t, run))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = repo.LoadBotPlan(owner, lease, p.OperationID); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatal("owner without worker grant", err)
			}
			stale := lease
			stale.Fence++
			if _, err = repo.LoadBotPlan(worker, stale, p.OperationID); !errors.Is(err, store.ErrBotConflict) {
				t.Fatal("stale fence", err)
			}
			original := repo.auth.check
			repo.auth.check = func(context.Context, string) (string, error) { return "", store.ErrBotForbidden }
			if _, err = repo.LoadBotPlan(worker, lease, p.OperationID); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatal("revoked membership", err)
			}
			repo.auth.check = original
			dispatch := uuid.New()
			p, err = repo.ChangeBotPlan(worker, lease, store.BotPreparedPlanChange{OperationID: p.OperationID, ExpectedRevision: p.Revision, Stage: store.BotPlanDispatchStarted, DispatchID: dispatch})
			if err != nil {
				t.Fatal(err)
			}
			p, err = repo.ChangeBotPlan(worker, lease, store.BotPreparedPlanChange{OperationID: p.OperationID, ExpectedRevision: p.Revision, Stage: store.BotPlanUncertain, DispatchID: dispatch})
			if err != nil {
				t.Fatal(err)
			}
			for _, stage := range []store.BotPreparedPlanStage{store.BotPlanPrepared, store.BotPlanDispatchStarted, store.BotPlanSettled, store.BotPlanPublished, store.BotPlanObsolete} {
				if _, err = repo.ChangeBotPlan(worker, lease, store.BotPreparedPlanChange{OperationID: p.OperationID, ExpectedRevision: p.Revision, Stage: stage, DispatchID: dispatch}); err == nil {
					t.Fatal("uncertain replay", stage)
				}
			}
			*now = lease.ExpiresAt.Add(time.Second)
			if _, err = repo.LoadBotPlan(worker, lease, p.OperationID); !errors.Is(err, store.ErrBotConflict) {
				t.Fatal("expired fence", err)
			}
		})
	}
}
