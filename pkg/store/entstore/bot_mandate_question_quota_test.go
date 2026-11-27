package entstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	personalent "github.com/SAP/astonish/ent/personal"
	teament "github.com/SAP/astonish/ent/team"
	botdomain "github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func questionQuotaPolicy(c context.Context, ref store.BotMandateRef) (store.BotMandatePolicy, error) {
	p, err := mandateTestPolicy(c, ref)
	p.Limits.MaxTokens, p.Limits.MaxTotalTokens, p.Limits.MaxSpendMicros = 100, 150, 100
	p.Limits.MaxDailyRuns = 1
	p.PublicResearch = &execution.BotPublicResearchScope{Version: 1, Route: execution.BotConfiguredSearchRoute{Engine: execution.BotSearchPerplexity, Provider: "owned", Model: "sonar", Tool: "perplexity_web_search", Fingerprint: "owned", CredentialHandles: []string{"provider:owned:api_key"}}, Facets: []execution.BotResearchFacet{execution.BotResearchOverview}, MaxQueryBytes: 2048, MaxResults: 5, MaxCalls: 1}
	return p, err
}

func questionQuotaFixture(t *testing.T, scope store.BotScope) (*botRepository, context.Context, store.BotRun, func()) {
	t.Helper()
	owner := uuid.New()
	ctx, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx, err = execution.WithAuthenticationEvidence(ctx, execution.AuthenticationEvidence{Method: execution.AuthMethodPlatformJWT, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	ctx = store.WithBotMessageAdmission(ctx, execution.NewBotTurnAuthorizationRegistry(10))
	ctx = store.WithBotMandateAdmission(ctx, questionQuotaPolicy)
	auth := botRepositoryAuth{orgSlug: "org", teamSlug: "team", userID: owner.String(), scope: scope, check: func(context.Context, string) (string, error) { return "member", nil }}
	if scope == store.BotScopePersonal {
		auth.teamSlug = ""
	}
	path := filepath.Join(t.TempDir(), "question.db")
	var repo *botRepository
	var closeDB func() error
	open := func() {
		db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)")
		if err != nil {
			t.Fatal(err)
		}
		drv := entsql.OpenDB(dialect.SQLite, db)
		content := newReopenBotContentCodec(t, filepath.Join(filepath.Dir(path), "content"), store.BotContentScope{OrgID: "org", TeamID: "team", OwnerID: owner.String()})
		if scope == store.BotScopeTeam {
			c := teament.NewClient(teament.Driver(drv))
			if err := c.Schema.Create(ctx); err != nil {
				t.Fatal(err)
			}
			repo = (&teamBotStore{client: c, content: content, auth: auth}).repository()
			closeDB = c.Close
		} else {
			c := personalent.NewClient(personalent.Driver(drv))
			if err := c.Schema.Create(ctx); err != nil {
				t.Fatal(err)
			}
			repo = (&personalBotStore{client: c, content: content, auth: auth}).repository()
			closeDB = c.Close
		}
	}
	open()
	t.Cleanup(func() { _ = closeDB() })
	v := store.DefaultBotVersion()
	v.Charter = "Briefing"
	b, err := repo.Create(ctx, store.BotCreateRequest{Name: "Briefing", OwnerID: owner, Version: v, IdempotencyKey: "create"})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := repo.AdmitMessage(ctx, store.BotMessageRequest{BotID: b.ID, Version: b.Version, IdempotencyKey: "request", Instructions: "Schedule this briefing every day at 9am"})
	if err != nil {
		t.Fatal(err)
	}
	run := receipt.Run
	ctx = execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: b.ID, RunID: run.ID, Version: run.Version, ActorID: owner.String(), OrgSlug: "org", TeamSlug: "team"})
	original := repo
	return repo, ctx, run, func() {
		if err := closeDB(); err != nil {
			t.Fatal(err)
		}
		open()
		*original = *repo
	}
}

func questionQuotaUsage(t *testing.T, repo *botRepository, ctx context.Context, bot uuid.UUID) mandateUsage {
	t.Helper()
	rec, err := repo.db.GetBot(ctx, bot)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(rec.Metadata[mandateUsageKey])
	if err != nil {
		t.Fatal(err)
	}
	var usage mandateUsage
	if err := json.Unmarshal(raw, &usage); err != nil {
		t.Fatal(err)
	}
	return usage
}

func TestMandateQuestionQuotaContinuation(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, run, reopen := questionQuotaFixture(t, scope)
			runtime := &botMandateRuntime{repo: repo, policy: questionQuotaPolicy}
			ref := *run.MandateRef
			if err := runtime.ReserveBotMandateRun(ctx, ref, run.ID); err != nil {
				t.Fatal(err)
			}
			model := uuid.New()
			if err := runtime.ReserveBotMandateModel(ctx, ref, run.ID, model, 30); err != nil {
				t.Fatal(err)
			}
			if err := runtime.SettleBotMandateModel(ctx, ref, run.ID, model, 7); err != nil {
				t.Fatal(err)
			}
			if err := runtime.ConsumeBotMandateQuota(ctx, ref, run.ID, 1, 2, 3); err != nil {
				t.Fatal(err)
			}
			if err := runtime.ReserveBotPublicResearch(ctx, ref, run.SessionID.String()); err != nil {
				t.Fatal(err)
			}
			before := questionQuotaUsage(t, repo, ctx, run.BotID)
			approval := savedContinuationQuestion(t, repo, ctx, run)
			if err := runtime.ReleaseBotMandateRun(ctx, ref, run.ID); err != nil {
				t.Fatal(err)
			}
			if got := questionQuotaUsage(t, repo, ctx, run.BotID); len(got.Active) != 0 {
				t.Fatal("question retained occupancy")
			}
			reopen()
			ctx = store.WithBotMessageAdmission(ctx, execution.NewBotTurnAuthorizationRegistry(10))
			q := store.BotApproveContinueRequest{BotID: run.BotID, ApprovalID: approval.ID, ActorID: uuid.MustParse(ref.OwnerID), ActionDigest: approval.ActionDigest, Response: &store.BotInteractionResponse{QuestionID: "timezone", Answer: "America/New_York"}}
			if _, err := repo.DecideAndRequeueApprovedRun(ctx, q); err != nil {
				t.Fatal(err)
			}
			if err := runtime.ReserveBotMandateRun(ctx, ref, run.ID); err != nil {
				t.Fatalf("answered ordinary question cannot reacquire quota: %v", err)
			}
			after := questionQuotaUsage(t, repo, ctx, run.BotID)
			if !reflect.DeepEqual(before.Active, after.Active) || before.DailyRuns != after.DailyRuns || before.Tools != after.Tools || before.Tokens != after.Tokens || before.Spend != after.Spend {
				t.Fatalf("ledger reset: before=%+v after=%+v", before, after)
			}
			if err := runtime.ReserveBotMandateRun(ctx, ref, run.ID); err != nil {
				t.Fatal(err)
			}
			if err := runtime.ReserveBotMandateModel(ctx, ref, run.ID, model, 1); err == nil {
				t.Fatal("duplicate model reset")
			}
			if err := runtime.ReserveBotPublicResearch(ctx, ref, run.SessionID.String()); err == nil {
				t.Fatal("research reset")
			}
			if err := runtime.ConsumeBotMandateQuota(ctx, ref, run.ID, 2, 0, 0); err == nil {
				t.Fatal("tool limit reset")
			}
			if err := runtime.ConsumeBotMandateQuota(ctx, ref, run.ID, 0, 0, 98); err == nil {
				t.Fatal("spend limit reset")
			}
			if err := runtime.ConsumeBotMandateQuota(ctx, ref, run.ID, 0, 92, 0); err == nil {
				t.Fatal("token limit reset")
			}
		})
	}
}

// Read faults exercise the quota transaction against real scoped SQLite without
// manufacturing malformed historical rows or changing any live data.
type questionQuotaFaultDB struct {
	botDB
	mode string
}

func (d questionQuotaFaultDB) Transaction(ctx context.Context, fn func(botDB) error) error {
	return d.botDB.Transaction(ctx, func(tx botDB) error { return fn(questionQuotaFaultDB{tx, d.mode}) })
}
func (d questionQuotaFaultDB) ReadRun(ctx context.Context, b, r uuid.UUID) (*botStoredRun, error) {
	saved, err := d.botDB.ReadRun(ctx, b, r)
	if err != nil {
		return nil, err
	}
	switch d.mode {
	case "source":
		saved.Instructions += " tampered"
	case "ref":
		ref := *saved.Run.MandateRef
		ref.ID = uuid.New()
		saved.Run.MandateRef = &ref
	case "fence":
		saved.Run.Lease.Fence++
	case "terminal":
		saved.Run.Status = store.RunFailed
	case "checkpoint":
		var c botdomain.ApprovalCheckpoint
		_ = json.Unmarshal(saved.Run.Checkpoint, &c)
		c.ApprovalID = uuid.New()
		saved.Run.Checkpoint, _ = json.Marshal(c)
	}
	return saved, nil
}
func (d questionQuotaFaultDB) ReadApproval(ctx context.Context, b, a uuid.UUID) (*store.BotApproval, error) {
	approval, err := d.botDB.ReadApproval(ctx, b, a)
	if err != nil {
		return nil, err
	}
	switch d.mode {
	case "consumed":
		approval.Status = store.ApprovalConsumed
	case "revoked":
		approval.Status = store.ApprovalRevoked
	case "expired":
		approval.ExpiresAt = time.Now().Add(-time.Hour)
	case "response":
		approval.Response = nil
	case "owner":
		id := uuid.New()
		approval.DecidedBy = &id
	case "not-question":
		approval.Interaction.Kind = store.BotInteractionPlanDefinition
	}
	return approval, nil
}

func (d questionQuotaFaultDB) saveMandateMetadata(ctx context.Context, rec *botStoredRecord) error {
	return saveMandateMetadata(ctx, d.botDB, rec)
}

func (d questionQuotaFaultDB) GetBot(ctx context.Context, b uuid.UUID) (*botStoredRecord, error) {
	rec, err := d.botDB.GetBot(ctx, b)
	if err != nil {
		return nil, err
	}
	if d.mode == "active-run" {
		id := uuid.New()
		rec.ActiveRunID = &id
	}
	if d.mode == "missing-ledger" || d.mode == "unfinished-retained" || d.mode == "retained-ref" || d.mode == "concurrent" {
		raw, _ := json.Marshal(rec.Metadata[mandateUsageKey])
		var usage mandateUsage
		_ = json.Unmarshal(raw, &usage)
		for id, pending := range usage.QuestionContinuations {
			switch d.mode {
			case "missing-ledger":
				delete(usage.QuestionContinuations, id)
			case "unfinished-retained":
				pending.Usage.Models = map[string]mandateModelCharge{uuid.NewString(): {Tokens: 1}}
				usage.QuestionContinuations[id] = pending
			case "retained-ref":
				pending.Usage.Ref.ID = uuid.New()
				usage.QuestionContinuations[id] = pending
			}
		}
		if d.mode == "concurrent" {
			usage.Active[uuid.NewString()] = mandateRunUsage{}
		}
		rec.Metadata[mandateUsageKey] = usage
	}
	return rec, nil
}

func TestMandateQuestionQuotaSafety(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, mode := range []string{"consumed", "revoked", "expired", "response", "owner", "not-question", "source", "ref", "retained-ref", "fence", "terminal", "checkpoint", "active-run", "concurrent", "missing-ledger", "unfinished-retained", "unfinished-release", "terminal-release", "expired-mandate", "revoked-mandate"} {
			t.Run(string(scope)+"/"+mode, func(t *testing.T) {
				repo, ctx, run, _ := questionQuotaFixture(t, scope)
				runtime := &botMandateRuntime{repo: repo, policy: questionQuotaPolicy}
				ref := *run.MandateRef
				if err := runtime.ReserveBotMandateRun(ctx, ref, run.ID); err != nil {
					t.Fatal(err)
				}
				if err := runtime.ConsumeBotMandateQuota(ctx, ref, run.ID, 1, 2, 3); err != nil {
					t.Fatal(err)
				}
				if mode == "unfinished-release" {
					if err := runtime.ReserveBotMandateModel(ctx, ref, run.ID, uuid.New(), 1); err != nil {
						t.Fatal(err)
					}
				}
				a := savedContinuationQuestion(t, repo, ctx, run)
				original := repo.db
				if mode == "terminal-release" {
					repo.db = questionQuotaFaultDB{original, "terminal"}
				}
				if err := runtime.ReleaseBotMandateRun(ctx, ref, run.ID); err != nil {
					t.Fatal(err)
				}
				repo.db = original
				if err := runtime.ReserveBotMandateRun(ctx, ref, run.ID); err == nil {
					t.Fatal("pending question reacquired")
				}
				q := store.BotApproveContinueRequest{BotID: run.BotID, ApprovalID: a.ID, ActorID: uuid.MustParse(ref.OwnerID), ActionDigest: a.ActionDigest, Response: &store.BotInteractionResponse{QuestionID: "timezone", Answer: "America/New_York"}}
				if _, err := repo.DecideAndRequeueApprovedRun(ctx, q); err != nil {
					t.Fatal(err)
				}
				before := questionQuotaUsage(t, repo, ctx, run.BotID)
				switch mode {
				case "expired-mandate":
					m, err := readMandate(ctx, repo.db, ref)
					if err != nil {
						t.Fatal(err)
					}
					repo.now = func() time.Time { return m.Mandate.ExpiresAt.Add(time.Second) }
				case "revoked-mandate":
					if err := runtime.RevokeBotMandate(ctx, ref); err != nil {
						t.Fatal(err)
					}
				default:
					repo.db = questionQuotaFaultDB{original, mode}
				}
				if err := runtime.ReserveBotMandateRun(ctx, ref, run.ID); err == nil {
					t.Fatal("unsafe occupancy reacquisition")
				}
				repo.db = original
				after := questionQuotaUsage(t, repo, ctx, run.BotID)
				if !reflect.DeepEqual(before, after) {
					t.Fatalf("rejection changed ledger: before=%+v after=%+v", before, after)
				}
			})
		}
	}
}
