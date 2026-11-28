package entstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	botdomain "github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func appendTitleQuestionFixture(t *testing.T, ctx context.Context, repo *botRepository, lease store.BotLease, sid uuid.UUID, payload json.RawMessage) {
	t.Helper()
	var response map[string]any
	if err := json.Unmarshal(payload, &response); err != nil {
		t.Fatal(err)
	}
	response["status"] = "ok"
	callID, invocation := uuid.NewString(), uuid.NewString()
	for _, e := range []*session.Event{
		{ID: uuid.NewString(), Author: "chat", InvocationID: invocation, LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: callID, Name: "ask_user"}}}}}},
		{ID: uuid.NewString(), Author: "chat", InvocationID: invocation, LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "user", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: callID, Name: "ask_user", Response: response}}}}}},
	} {
		if err := repo.appendBotRunEvent(ctx, lease, sid, e); err != nil {
			t.Fatal(err)
		}
	}
}

// TestBotTitleConfirmationProcessRestart executes two separate copies of the test
// binary. Only the parent owns the directory: the waiting process must exit
// without deleting the database which the answering process subsequently opens.
func TestBotTitleConfirmationProcessRestart(t *testing.T) {
	if stage := os.Getenv("ASTONISH_TITLE_RESTART_STAGE"); stage != "" {
		botTitleConfirmationRestartProcess(t, stage, os.Getenv("ASTONISH_TITLE_RESTART_DIR"), store.BotScope(os.Getenv("ASTONISH_TITLE_RESTART_SCOPE")))
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			dir := t.TempDir()
			for _, stage := range []string{"wait", "answer"} {
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				cmd := exec.CommandContext(ctx, executable, "-test.run=^TestBotTitleConfirmationProcessRestart$", "-test.v", "-test.count=1")
				cmd.Env = append(os.Environ(), "ASTONISH_TITLE_RESTART_STAGE="+stage, "ASTONISH_TITLE_RESTART_DIR="+dir, "ASTONISH_TITLE_RESTART_SCOPE="+string(scope))
				output, err := cmd.CombinedOutput()
				cancel()
				t.Logf("process stage=%s scope=%s:\n%s", stage, scope, output)
				if err != nil {
					t.Fatalf("subprocess %s: %v", stage, err)
				}
			}
		})
	}
}

type botTitleRestartEvidence struct {
	Owner                    string
	BotID, RunID, ApprovalID uuid.UUID
	QuestionID               string
	Checkpoint               json.RawMessage
	Payload                  json.RawMessage
	Usage                    store.BotUsage
	PID                      int
	WaitStarted              time.Time
}

func botTitleConfirmationRestartProcess(t *testing.T, stage, dir string, scope store.BotScope) {
	t.Helper()
	if dir == "" || (stage != "wait" && stage != "answer") {
		t.Fatal("invalid subprocess environment")
	}
	ctx := context.Background()
	_, db, err := NewPlatformServices(ctx, Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	evidencePath := filepath.Join(dir, "restart-evidence.json")
	var evidence botTitleRestartEvidence
	if stage == "wait" {
		evidence.Owner = uuid.NewString()
		org := &store.Organization{ID: uuid.NewString(), Slug: "restart", Name: "Restart", Status: "active"}
		if err := db.Organizations().Create(ctx, org); err != nil {
			t.Fatal(err)
		}
		if err := db.ProvisionOrg(ctx, org.ID, org.Slug); err != nil {
			t.Fatal(err)
		}
		if err := db.Users().Create(ctx, &store.User{ID: evidence.Owner, Email: evidence.Owner + "@example.test", DisplayName: "Owner", Status: "active"}); err != nil {
			t.Fatal(err)
		}
		if err := db.Organizations().AddMember(ctx, evidence.Owner, org.ID, "member"); err != nil {
			t.Fatal(err)
		}
	} else {
		raw, err := os.ReadFile(evidencePath)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &evidence); err != nil {
			t.Fatal(err)
		}
		if evidence.PID == os.Getpid() {
			t.Fatal("database reopened in same OS process")
		}
	}
	ods, err := db.ForOrg("restart")
	if err != nil {
		t.Fatal(err)
	}
	if stage == "wait" {
		team := &store.Team{ID: uuid.NewString(), Slug: "team", Name: "Team", SchemaName: "team_team"}
		if err := ods.Teams().CreateTeam(ctx, team); err != nil {
			t.Fatal(err)
		}
		if err := ods.Teams().AddMember(ctx, &store.TeamMembership{UserID: evidence.Owner, TeamID: team.ID, Role: "member"}); err != nil {
			t.Fatal(err)
		}
	}
	p := execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: evidence.Owner, OrgSlug: "restart", TeamSlug: "team", Authenticated: true, Scopes: []string{"chat", "tool:execute"}}
	ctx, err = execution.WithPrincipal(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err = execution.WithAuthenticationEvidence(ctx, execution.AuthenticationEvidence{Method: p.Authentication, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	ctx = store.WithTenantContext(ctx, &store.TenantContext{UserID: evidence.Owner, OrgSlug: "restart", TeamSlug: "team"})
	var bots store.BotStore = ods.ForTeam("team").Bots()
	if scope == store.BotScopePersonal {
		bots = ods.ForUser(evidence.Owner).Bots()
	}
	var repo *botRepository
	switch s := bots.(type) {
	case *personalBotStore:
		repo = s.repository()
	case *teamBotStore:
		repo = s.repository()
	default:
		t.Fatalf("unsupported scoped store %T", bots)
	}
	registry := execution.NewBotTurnAuthorizationRegistry(20)
	defer registry.Close()
	ctx = store.WithBotMessageAdmission(ctx, registry)
	policy := func(c context.Context, ref store.BotMandateRef) (store.BotMandatePolicy, error) {
		p, err := mandateTestPolicy(c, ref)
		p.PublicResearch = &execution.BotPublicResearchScope{Version: 1, Route: execution.BotConfiguredSearchRoute{Engine: execution.BotSearchPerplexity, Provider: "owned", Model: "sonar", Tool: "perplexity_web_search", Fingerprint: "owned", CredentialHandles: []string{"provider:owned:api_key"}}, Facets: []execution.BotResearchFacet{execution.BotResearchOverview}, MaxQueryBytes: 2048, MaxResults: 5, MaxCalls: 2}
		return p, err
	}
	ctx = store.WithBotMandateAdmission(ctx, policy)
	runtime, err := NewBotMandateRuntime(bots, policy)
	if err != nil {
		t.Fatal(err)
	}
	if stage == "wait" {
		version := store.DefaultBotVersion()
		version.Charter = "Clarify the AI News assignment"
		first, err := repo.CreateAndStart(ctx, store.BotCreateRequest{Name: "AI News", OwnerID: uuid.MustParse(evidence.Owner), Version: version, IdempotencyKey: "restart-title"})
		if err != nil {
			t.Fatal(err)
		}
		evidence.BotID, evidence.RunID = first.ID, first.Run.ID
		worker := execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: first.ID, RunID: first.Run.ID, Version: first.Run.Version, ActorID: evidence.Owner, OrgSlug: "restart", TeamSlug: "team"})
		if err := runtime.ReserveBotMandateRun(ctx, *first.Run.MandateRef, first.Run.ID); err != nil {
			t.Fatal(err)
		}
		if err := runtime.ConsumeBotMandateQuota(ctx, *first.Run.MandateRef, first.Run.ID, 1, 7, 0); err != nil {
			t.Fatal(err)
		}
		lease, err := repo.ClaimRun(worker, store.BotClaimRequest{BotID: first.ID, RunID: first.Run.ID, WorkerID: "before-restart", ExpiresAt: time.Now().Add(time.Minute)})
		if err != nil {
			t.Fatal(err)
		}
		evidence.QuestionID = uuid.NewString()
		evidence.Payload = json.RawMessage(fmt.Sprintf(`{"questionId":%q,"kind":"select","prompt":"Which AI news assignment?","allow_custom_answer":true,"options":[{"id":"brief","label":"Prepare an AI news briefing"},{"id":"compare","label":"Compare recent AI announcements"},{"id":"sources","label":"Find original AI reporting"}]}`, evidence.QuestionID))
		action := execution.ActionDescriptor{Tool: "ask_user", Effect: execution.EffectRead, Arguments: evidence.Payload, ConstraintsVerified: true}
		digest, err := action.Digest()
		if err != nil {
			t.Fatal(err)
		}
		a, err := repo.RequestApproval(worker, lease, store.BotApproval{BotID: first.ID, RunID: first.Run.ID, Version: first.Run.Version, Action: action, ActionDigest: digest, Interaction: &store.BotInteraction{Kind: store.BotInteractionQuestion, QuestionID: evidence.QuestionID, Payload: evidence.Payload}, ExpiresAt: time.Now().Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		appendTitleQuestionFixture(t, worker, repo, lease, first.Run.SessionID, evidence.Payload)
		evidence.ApprovalID = a.ID
		evidence.Checkpoint, err = json.Marshal(botdomain.ApprovalCheckpoint{Format: botdomain.InteractionCheckpointFormat, ApprovalID: a.ID, BotID: first.ID, RunID: first.Run.ID, SessionID: first.Run.SessionID, Version: first.Run.Version, ActionDigest: digest, Fence: lease.Fence})
		if err != nil {
			t.Fatal(err)
		}
		// Record zero active runtime as well as nonzero tool/token usage: wall
		// time while the process is absent must not replace the recorded zero.
		evidence.Usage = store.BotUsage{ToolCalls: 1, InputTokens: 7}
		if err := repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunWaitingApproval, Usage: evidence.Usage, Checkpoint: evidence.Checkpoint, Report: store.BotReport{Summary: "Waiting", NeedsApproval: []uuid.UUID{a.ID}}}); err != nil {
			t.Fatal(err)
		}
		if err := runtime.ReleaseBotMandateRun(ctx, *first.Run.MandateRef, first.Run.ID); err != nil {
			t.Fatal(err)
		}
		evidence.PID, evidence.WaitStarted = os.Getpid(), time.Now()
		raw, err := json.Marshal(evidence)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(evidencePath, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	saved, err := repo.db.ReadRun(ctx, evidence.BotID, evidence.RunID)
	if err != nil {
		t.Fatal(err)
	}
	approval, err := repo.db.ReadApproval(ctx, evidence.BotID, evidence.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Run.Status != store.RunWaitingApproval || saved.Run.TitleConfirmation == nil || saved.Run.TitleConfirmation.State != store.BotTitleConfirmationWaiting || !reflect.DeepEqual(saved.Run.Checkpoint, evidence.Checkpoint) || approval.Status != store.ApprovalPending || approval.Interaction == nil || approval.Interaction.QuestionID != evidence.QuestionID || !reflect.DeepEqual(approval.Interaction.Payload, evidence.Payload) {
		t.Fatalf("persisted question/checkpoint changed: run=%+v approval=%+v", saved.Run, approval)
	}
	checkpoint, err := botdomain.DecodeApprovalCheckpoint(saved.Run.Checkpoint, saved.Run)
	if err != nil || !checkpoint.Matches(*approval) || checkpoint.ApprovalID != evidence.ApprovalID {
		t.Fatalf("restored checkpoint lost approval correlation: %+v %v", checkpoint, err)
	}
	assertUsage := func(run store.BotRun) {
		t.Helper()
		if !run.RuntimeAccountingRecorded || !reflect.DeepEqual(run.Usage, evidence.Usage) {
			t.Fatalf("wait charged or usage reset: %+v", run.Usage)
		}
	}
	assertUsage(saved.Run)
	record, err := readMandate(ctx, repo.db, *saved.Run.MandateRef)
	if err != nil {
		t.Fatal(err)
	}
	if record.Mandate.PublicResearch != nil {
		t.Fatal("research authority available before owner answer")
	}
	for _, constraint := range record.Mandate.Constraints {
		for _, resource := range constraint.Resources {
			if resource != "chat" && resource != "ask_user" {
				t.Fatalf("work authority before answer: %+v", constraint)
			}
		}
	}
	input, err := repo.LoadRunInput(ctx, evidence.BotID, evidence.RunID)
	if !errors.Is(err, store.ErrBotConflict) || input != nil || saved.Task != nil {
		t.Fatalf("waiting run executable or premature task: %+v %v", input, err)
	}
	if stage == "wait" {
		t.Logf("persisted waiting question=%s pid=%d", evidence.QuestionID, os.Getpid())
		return
	}
	identity := execution.BotRunIdentity{BotID: evidence.BotID, RunID: evidence.RunID, Version: saved.Run.Version, ActorID: evidence.Owner, OrgSlug: "restart", TeamSlug: "team"}
	if err := repo.interactiveAnswerAuthority(ctx, saved.Run); err == nil {
		t.Fatal("fresh process inherited interactive authority")
	}
	bot, err := repo.Get(ctx, evidence.BotID)
	if err != nil {
		t.Fatal(err)
	}
	q := store.BotContinueInteractiveRequest{BotID: evidence.BotID, RunID: evidence.RunID, ExpectedRevision: bot.Revision, IdempotencyKey: "answer-after-process-restart", Response: &store.BotInteractionResponse{QuestionID: evidence.QuestionID, Answer: "Private custom assignment after restart"}}
	confirmed, err := repo.ContinueInteractive(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.ID != evidence.RunID || confirmed.Status != store.RunQueued || confirmed.TitleConfirmation.State != store.BotTitleConfirmationConfirmed {
		t.Fatalf("not confirmed once: %+v", confirmed)
	}
	assertUsage(*confirmed)
	input, err = repo.LoadRunInput(ctx, evidence.BotID, evidence.RunID)
	if err != nil || input.Instructions != q.Response.Answer {
		t.Fatalf("confirmed input missing: %+v %v", input, err)
	}
	confirmedSaved, err := repo.db.ReadRun(ctx, evidence.BotID, evidence.RunID)
	if err != nil || confirmedSaved.Instructions != saved.Instructions || confirmedSaved.RequestHash != saved.RequestHash {
		t.Fatal("restart confirmation changed immutable title input", err)
	}
	decided, err := repo.db.ReadApproval(ctx, evidence.BotID, evidence.ApprovalID)
	if err != nil || decided.Status != store.ApprovalApproved || decided.Response == nil || decided.Response.QuestionID != evidence.QuestionID || decided.Response.Answer != q.Response.Answer || decided.DecidedBy == nil || decided.DecidedBy.String() != evidence.Owner {
		t.Fatalf("owner answer not durably correlated: %+v %v", decided, err)
	}
	fresh, err := readMandate(ctx, repo.db, *confirmed.MandateRef)
	if err != nil || fresh.Mandate.PublicResearch == nil {
		t.Fatalf("confirmed research authority missing: %+v %v", fresh, err)
	}
	if err := runtime.ReserveBotMandateRun(ctx, *confirmed.MandateRef, confirmed.ID); err != nil {
		t.Fatal(err)
	}
	storedBot, err := repo.db.GetBot(ctx, evidence.BotID)
	if err != nil {
		t.Fatal(err)
	}
	var usage mandateUsage
	usageJSON, err := json.Marshal(storedBot.Metadata[mandateUsageKey])
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(usageJSON, &usage); err != nil {
		t.Fatal(err)
	}
	if usage.DailyRuns != 1 || usage.Tools != 1 || usage.Tokens != 7 || usage.Active[evidence.RunID.String()].Tokens != 7 {
		t.Fatalf("restart reset cumulative budget: %+v", usage)
	}
	sequence := storedBot.LastSequence
	if _, err := repo.ContinueInteractive(ctx, q); err != nil {
		t.Fatal(err)
	}
	afterDuplicate, err := repo.db.GetBot(ctx, evidence.BotID)
	if err != nil || afterDuplicate.LastSequence != sequence {
		t.Fatal("duplicate answer emitted another queue event", err)
	}
	worker := execution.WithBotRunIdentity(ctx, identity)
	lease, err := repo.ClaimRun(worker, store.BotClaimRequest{BotID: evidence.BotID, RunID: evidence.RunID, WorkerID: "after-restart", ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ClaimRun(worker, store.BotClaimRequest{BotID: evidence.BotID, RunID: evidence.RunID, WorkerID: "duplicate-worker", ExpiresAt: time.Now().Add(time.Minute)}); err == nil {
		t.Fatal("duplicate execution lease granted")
	}
	again, err := repo.ContinueInteractive(ctx, q)
	if err != nil || again.Status != store.RunRunning || again.Lease.Fence != lease.Fence {
		t.Fatalf("duplicate after claim requeued: %+v %v", again, err)
	}
	assertUsage(*again)
	runs, err := repo.ListRuns(ctx, evidence.BotID, store.BotPage{})
	if err != nil || len(runs) != 1 {
		t.Fatalf("duplicate run: %d %v", len(runs), err)
	}
	if elapsed := time.Since(evidence.WaitStarted); elapsed <= 0 {
		t.Fatal("no elapsed waiting time")
	}
	t.Logf("restored identical question=%s old_pid=%d new_pid=%d waiting=%s runtime_nanos=%d; one confirmed queue and one lease", evidence.QuestionID, evidence.PID, os.Getpid(), time.Since(evidence.WaitStarted), again.Usage.RuntimeNanos)
}

func TestBotTitlePublicResearchAdmission(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, bots, ctx, _, _ := runtimeFixture(t, scope)
			ctx = mandateOwnerContext(t, ctx)
			ctx = store.WithBotMessageAdmission(ctx, execution.NewBotTurnAuthorizationRegistry(8))
			policy := func(c context.Context, ref store.BotMandateRef) (store.BotMandatePolicy, error) {
				p, err := mandateTestPolicy(c, ref)
				p.RuntimePolicy = execution.NewBotProgressRuntimePolicy() // a newer ceiling never upgrades this legacy title run
				p.Scopes = append(p.Scopes, string(execution.CapabilityChat))
				p.Constraints = append(p.Constraints, execution.BotActionResourceConstraint{Action: execution.CapabilityChat, Resources: []string{"chat"}}, execution.BotActionResourceConstraint{Action: execution.CapabilityToolExecute, Resources: []string{"ask_user"}})
				p.PublicResearch = &execution.BotPublicResearchScope{Version: 1, Route: execution.BotConfiguredSearchRoute{Engine: execution.BotSearchPerplexity, Provider: "owned", Model: "sonar", Tool: "perplexity_web_search", Fingerprint: "owned", CredentialHandles: []string{"provider:owned:api_key"}}, Facets: []execution.BotResearchFacet{execution.BotResearchOverview}, MaxQueryBytes: 2048, MaxResults: 5, MaxCalls: 2}
				return p, err
			}
			ctx = store.WithBotMandateAdmission(ctx, policy)
			principal, _ := execution.PrincipalFromContext(ctx)
			version := store.DefaultBotVersion()
			version.Charter = "Private charter must never become query text"
			request := store.BotCreateRequest{Name: "AI News", OwnerID: uuid.MustParse(principal.Subject), Version: version, IdempotencyKey: "ordinary-title"}
			first, err := repo.CreateAndStart(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			record, err := readMandate(ctx, repo.db, *first.Run.MandateRef)
			if err != nil {
				t.Fatal(err)
			}
			if record.Provenance.InitiationKind != store.BotInitiationKindTitle || record.Mandate.PublicResearch != nil || first.Run.TitleConfirmation == nil {
				t.Fatalf("ordinary title lost bounded research intent: %+v", record)
			}
			replay, err := repo.CreateAndStart(ctx, request)
			if err != nil || replay.Run.ID != first.Run.ID || *replay.Run.MandateRef != *first.Run.MandateRef {
				t.Fatalf("replay changed authority: %+v %v", replay, err)
			}
			// Confirm in the same persisted run, fail the fresh-policy transaction,
			// then retry and prove neither the title input nor source hash changed.
			original, err := repo.db.ReadRun(ctx, first.ID, first.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			mandateRuntime, err := NewBotMandateRuntime(bots, policy)
			if err != nil {
				t.Fatal(err)
			}
			if err := mandateRuntime.ReserveBotMandateRun(ctx, *first.Run.MandateRef, first.Run.ID); err != nil {
				t.Fatal(err)
			}
			if err := mandateRuntime.ConsumeBotMandateQuota(ctx, *first.Run.MandateRef, first.Run.ID, 1, 7, 0); err != nil {
				t.Fatal(err)
			}
			worker := execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: first.ID, RunID: first.Run.ID, Version: first.Run.Version, ActorID: principal.Subject, OrgSlug: principal.OrgSlug, TeamSlug: principal.TeamSlug})
			lease, err := repo.ClaimRun(worker, store.BotClaimRequest{BotID: first.ID, RunID: first.Run.ID, WorkerID: "confirmation", ExpiresAt: time.Now().Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			payload := json.RawMessage(`{"questionId":"title","kind":"select","prompt":"Which AI news task?","allow_custom_answer":true,"options":[{"id":"brief","label":"Prepare an AI news briefing"},{"id":"compare","label":"Compare recent AI announcements"},{"id":"source","label":"Find original AI reporting"}]}`)
			action := execution.ActionDescriptor{Tool: "ask_user", Effect: execution.EffectRead, Arguments: payload, ConstraintsVerified: true}
			digest, _ := action.Digest()
			requestApproval := store.BotApproval{BotID: first.ID, RunID: first.Run.ID, Version: first.Run.Version, Action: action, ActionDigest: digest, Interaction: &store.BotInteraction{Kind: store.BotInteractionQuestion, QuestionID: "title", Payload: payload}, ExpiresAt: time.Now().Add(time.Hour)}
			a, err := repo.RequestApproval(worker, lease, requestApproval)
			if err != nil {
				t.Fatal(err)
			}
			again, err := repo.RequestApproval(worker, lease, requestApproval)
			if err != nil || again.ID != a.ID {
				t.Fatal("question replay", err)
			}
			appendTitleQuestionFixture(t, worker, repo, lease, first.Run.SessionID, payload)
			checkpoint, _ := json.Marshal(botdomain.ApprovalCheckpoint{Format: botdomain.InteractionCheckpointFormat, ApprovalID: a.ID, BotID: first.ID, RunID: first.Run.ID, SessionID: first.Run.SessionID, Version: first.Run.Version, ActionDigest: digest, Fence: lease.Fence})
			if err := repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunWaitingApproval, Checkpoint: checkpoint, Report: store.BotReport{Summary: "Waiting", NeedsApproval: []uuid.UUID{a.ID}}}); err != nil {
				t.Fatal(err)
			}
			if err := mandateRuntime.ReleaseBotMandateRun(ctx, *first.Run.MandateRef, first.Run.ID); err != nil {
				t.Fatal(err)
			}
			if err := mandateRuntime.ReserveBotMandateRun(ctx, *first.Run.MandateRef, first.Run.ID); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("unconfirmed replay: %v", err)
			}
			answer := store.BotApproveContinueRequest{BotID: first.ID, ApprovalID: a.ID, ActorID: first.OwnerID, ActionDigest: digest, Response: &store.BotInteractionResponse{QuestionID: "title", Answer: "Private custom details"}}
			wrongOwner := answer
			wrongOwner.ActorID = uuid.New()
			if _, err := repo.DecideAndRequeueApprovedRun(ctx, wrongOwner); err == nil {
				t.Fatal("foreign owner answer accepted")
			}
			clock := repo.now
			repo.now = func() time.Time { return a.ExpiresAt.Add(time.Second) }
			if _, err := repo.DecideAndRequeueApprovedRun(ctx, answer); !errors.Is(err, store.ErrBotExpired) {
				t.Fatal("expired answer accepted", err)
			}
			repo.now = clock
			failing := store.WithBotMandateAdmission(ctx, func(context.Context, store.BotMandateRef) (store.BotMandatePolicy, error) {
				return store.BotMandatePolicy{}, errors.New("policy unavailable")
			})
			if _, err := repo.DecideAndRequeueApprovedRun(failing, answer); err == nil {
				t.Fatal("policy failure ignored")
			}
			pending, _ := repo.db.ReadApproval(ctx, first.ID, a.ID)
			waiting, _ := repo.db.ReadRun(ctx, first.ID, first.Run.ID)
			if pending.Status != store.ApprovalPending || waiting.Run.TitleConfirmation.State != store.BotTitleConfirmationWaiting || waiting.Run.Status != store.RunWaitingApproval {
				t.Fatal("failed transaction leaked answer")
			}
			if _, err := repo.DecideAndRequeueApprovedRun(ctx, answer); err != nil {
				t.Fatal(err)
			}
			confirmed, err := repo.db.ReadRun(ctx, first.ID, first.Run.ID)
			if err != nil || confirmed.RequestHash != original.RequestHash || confirmed.Instructions != original.Instructions || *confirmed.Run.MandateRef == *original.Run.MandateRef {
				t.Fatal("immutable input or fresh binding lost", err)
			}
			fresh, err := readMandate(ctx, repo.db, *confirmed.Run.MandateRef)
			if err != nil || fresh.Provenance.TitleConfirmation == nil || fresh.Mandate.PublicResearch == nil || fresh.Mandate.PublicResearch.Subject != "AI News" || fresh.Provenance.SourceDigest != confirmed.Run.TitleConfirmation.AssignmentDigest {
				t.Fatal("fresh authority not bound safely", err)
			}
			if fresh.Mandate.RuntimePolicy != nil || !fresh.Mandate.ExpiresAt.Equal(record.Mandate.ExpiresAt) || fresh.Mandate.Limits != record.Mandate.Limits {
				t.Fatal("title answer expanded saved runtime authority")
			}
			if err := mandateRuntime.ReserveBotMandateRun(ctx, *confirmed.Run.MandateRef, first.Run.ID); err != nil {
				t.Fatalf("confirmed same-run reservation: %v", err)
			}
			if err := mandateRuntime.ReserveBotMandateRun(ctx, *confirmed.Run.MandateRef, first.Run.ID); err != nil {
				t.Fatalf("active duplicate: %v", err)
			}
			storedBot, _ := repo.db.GetBot(ctx, first.ID)
			var usage mandateUsage
			usageJSON, _ := json.Marshal(storedBot.Metadata[mandateUsageKey])
			if err := json.Unmarshal(usageJSON, &usage); err != nil {
				t.Fatal(err)
			}
			if usage.DailyRuns != 1 || usage.Tokens != 7 || usage.Tools != 1 || usage.Active[first.Run.ID.String()].Tokens != 7 || usage.Active[first.Run.ID.String()].Tools != 1 {
				t.Fatalf("continuation reset or doubled usage: %+v", usage)
			}
			if err := mandateRuntime.ReleaseBotMandateRun(ctx, *confirmed.Run.MandateRef, first.Run.ID); err != nil {
				t.Fatal(err)
			}
			if err := mandateRuntime.ReserveBotMandateRun(ctx, *confirmed.Run.MandateRef, first.Run.ID); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("released confirmed replay: %v", err)
			}
			if _, err := mandateRuntime.VerifyBotMandate(context.Background(), *confirmed.Run.MandateRef, execution.BotMandatePurposeTask); err != nil {
				t.Fatal("confirmed authority fails runtime verification", err)
			}
			if _, err := mandateRuntime.VerifyBotMandate(context.Background(), *original.Run.MandateRef, execution.BotMandatePurposeTask); err == nil {
				t.Fatal("old question authority still executable")
			}
			if _, err := repo.DecideAndRequeueApprovedRun(ctx, answer); err != nil {
				t.Fatal("answer replay", err)
			}
			followup, err := repo.AdmitMessage(ctx, store.BotMessageRequest{BotID: first.ID, Version: 1, Instructions: "Search each major AI lab individually and compile a single report.", IdempotencyKey: "public-followup"})
			if err != nil {
				t.Fatal(err)
			}
			followupMandate, err := readMandate(ctx, repo.db, *followup.Run.MandateRef)
			if err != nil || followupMandate.Mandate.PublicResearch == nil || followupMandate.Mandate.PublicResearch.Subject != "AI News" {
				t.Fatalf("ordinary follow-up lost public research availability: %+v %v", followupMandate, err)
			}
			direct, err := repo.AdmitMessage(ctx, store.BotMessageRequest{BotID: first.ID, Version: 1, Instructions: "AI News using my private assignment", IdempotencyKey: "private-turn"})
			if err != nil {
				t.Fatal(err)
			}
			private, err := readMandate(ctx, repo.db, *direct.Run.MandateRef)
			if err != nil || private.Mandate.PublicResearch != nil {
				t.Fatalf("private follow-up inferred disclosure: %+v %v", private, err)
			}
			// Adapter state must not turn attachments or mismatched private text
			// into the initially admitted public subject.
			for _, text := range []string{"Private attachment discussion", "AI News\nPrivate assignment"} {
				bound := context.WithValue(ctx, titleMandateAdmissionKey{}, titleMandateAdmission{Kind: store.BotInitiationKindTitle, Subject: "AI News"})
				_, err := repo.AdmitMessage(bound, store.BotMessageRequest{BotID: first.ID, Version: 1, Instructions: text, IdempotencyKey: text})
				if err == nil {
					t.Fatal("mismatched adapter title accepted")
				}
			}
		})
	}
}
