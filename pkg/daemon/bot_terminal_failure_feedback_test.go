package daemon

import (
	"context"
	"encoding/json"
	"iter"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

type terminalFailureFeedbackModel struct {
	t     *testing.T
	calls int
}

func (*terminalFailureFeedbackModel) Name() string { return "original-model" }
func (m *terminalFailureFeedbackModel) GenerateContent(ctx context.Context, r *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		if len(r.Tools) != 0 || len(r.Config.Tools) != 0 {
			m.t.Fatal("terminal failure regained tools")
		}
		var facts struct {
			SourceDigest          string `json:"source_digest"`
			PreapplicationContext struct {
				Status store.RunStatus `json:"status"`
				Result store.BotReport `json:"result"`
			} `json:"preapplication_context"`
		}
		if len(r.Contents) != 1 || len(r.Contents[0].Parts) != 1 || json.Unmarshal([]byte(r.Contents[0].Parts[0].Text), &facts) != nil || facts.SourceDigest == "" || facts.PreapplicationContext.Result.Failure == nil {
			m.t.Fatal("missing exact committed failure facts")
		}
		if facts.PreapplicationContext.Status != store.RunFailed && facts.PreapplicationContext.Status != store.RunPartial {
			m.t.Fatal("changed failure status")
		}
		raw, _ := json.Marshal(map[string]string{"source_digest": facts.SourceDigest, "text": "I couldn't finish the requested briefing because the source service was unavailable. No schedule was created."})
		yield(&model.LLMResponse{Content: genai.NewContentFromText(string(raw), "model"), UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 9, CandidatesTokenCount: 11, TotalTokenCount: 20}}, nil)
	}
}

func TestEmployeeTerminalFailureFeedbackWithoutSchedule(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, status := range []store.RunStatus{store.RunFailed, store.RunPartial} {
			t.Run(string(scope)+"/"+string(status), func(t *testing.T) {
				ctx := t.Context()
				dir := t.TempDir()
				_, db, err := entstore.NewPlatformServices(ctx, entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				org := &store.Organization{ID: uuid.NewString(), Slug: "feedback", Name: "Feedback", Status: "active"}
				if err = db.Organizations().Create(ctx, org); err != nil {
					t.Fatal(err)
				}
				if err = db.ProvisionOrg(ctx, org.ID, org.Slug); err != nil {
					t.Fatal(err)
				}
				ods, err := db.ForOrg(org.Slug)
				if err != nil {
					t.Fatal(err)
				}
				team := &store.Team{ID: uuid.NewString(), Slug: "team", Name: "Team", SchemaName: "team_team"}
				if err = ods.Teams().CreateTeam(ctx, team); err != nil {
					t.Fatal(err)
				}
				owner := uuid.New()
				if err = db.Users().Create(ctx, &store.User{ID: owner.String(), Email: owner.String() + "@test.invalid", DisplayName: "Owner", Status: "active"}); err != nil {
					t.Fatal(err)
				}
				if err = db.Organizations().AddMember(ctx, owner.String(), org.ID, "member"); err != nil {
					t.Fatal(err)
				}
				if err = ods.Teams().AddMember(ctx, &store.TeamMembership{UserID: owner.String(), TeamID: team.ID, Role: "member"}); err != nil {
					t.Fatal(err)
				}
				auth, err := execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: org.Slug, TeamSlug: team.Slug, Authenticated: true})
				if err != nil {
					t.Fatal(err)
				}
				auth, err = execution.WithAuthenticationEvidence(auth, execution.AuthenticationEvidence{Method: execution.AuthMethodPlatformJWT, ExpiresAt: time.Now().Add(time.Hour)})
				if err != nil {
					t.Fatal(err)
				}
				auth = store.WithBotMessageAdmission(store.WithBotMandateAdmission(auth, authorizedHTTPPolicy), execution.NewBotTurnAuthorizationRegistry(10))
				repo := ods.ForTeam(team.Slug).Bots()
				jobs := ods.ForTeam(team.Slug).ScheduledJobs()
				if scope == store.BotScopePersonal {
					repo = ods.ForUser(owner.String()).Bots()
					jobs = ods.ForUser(owner.String()).ScheduledJobs()
				}
				v := store.DefaultBotVersion()
				v.Charter = "Briefing"
				b, err := repo.Create(auth, store.BotCreateRequest{Name: "Reporter", OwnerID: owner, Version: v, IdempotencyKey: "draft"})
				if err != nil {
					t.Fatal(err)
				}
				backend := newRegressionHomeBackend(t)
				if err = db.InstallBotContentHomeBackend(backend); err != nil {
					t.Fatal(err)
				}
				provisioner := &BotWorker{config: BotWorkerConfig{Backend: db, Sandbox: backend, Pool: workerTestPool{}, WorkerID: "provision"}}
				b, err = provisioner.provisionHome(auth, repo, b.ID, b.Revision)
				if err != nil {
					t.Fatal(err)
				}
				receipt, err := repo.(store.BotMessageStore).AdmitMessage(auth, store.BotMessageRequest{BotID: b.ID, Version: b.Version, IdempotencyKey: "request", Instructions: "Read the briefing"})
				if err != nil {
					t.Fatal(err)
				}
				workerAuth := execution.WithBotRunIdentity(auth, execution.BotRunIdentity{BotID: b.ID, RunID: receipt.Run.ID, Version: receipt.Run.Version, ActorID: owner.String(), OrgSlug: org.Slug, TeamSlug: team.Slug})
				lease, err := repo.ClaimRun(workerAuth, store.BotClaimRequest{BotID: b.ID, RunID: receipt.Run.ID, WorkerID: "source", ExpiresAt: time.Now().Add(time.Minute)})
				if err != nil {
					t.Fatal(err)
				}
				work, err := repo.(store.BotWorkStore).LoadWork(workerAuth, lease)
				if err != nil {
					t.Fatal(err)
				}
				report := store.BotReport{Summary: "Source service unavailable", Failure: &store.BotReportFailure{Kind: store.BotReportFailureOperational}, Work: store.BuildBotWorkProjection(work)}
				if err = repo.CommitRun(workerAuth, store.BotRunCommit{Lease: lease, Status: status, Report: report, ModelSelection: &store.SessionPin{Provider: "original-provider", Model: "original-model"}}); err != nil {
					t.Fatal(err)
				}
				source, err := repo.GetRun(auth, b.ID, receipt.Run.ID)
				if err != nil {
					t.Fatal(err)
				}
				llm := &terminalFailureFeedbackModel{t: t}
				w := &BotWorker{config: BotWorkerConfig{Backend: db, MandatePolicy: authorizedHTTPPolicy, WorkerID: "feedback", ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 100, SupportsOutputLimit: true, EstimateInput: func(context.Context, *model.LLMRequest) (int64, error) { return 100, nil }}, ResolveModel: func(_ context.Context, _ execution.Principal, _ store.BotScope, p, m string) (model.LLM, error) {
					if p != "original-provider" || m != "original-model" {
						t.Fatal("changed original pin")
					}
					return llm, nil
				}}}
				if err = w.completeEmployeeSchedule(auth, repo, b.ID, source.ID); err != nil {
					t.Fatal(err)
				}
				claim := store.BotEmployeePresentationRequest{BotID: b.ID, RunID: source.ID, ClaimToken: "employee-final:" + source.ID.String()}
				p, err := repo.(store.BotEmployeePresentationStore).ReadEmployeePresentation(auth, claim)
				if err != nil || p.State != store.BotEmployeePresentationCompleted || !strings.Contains(p.FinalText, "source service was unavailable") || len(p.Operations) != 0 || len(p.Turns) != 1 || p.Turns[0].Usage.InputTokens != 9 {
					t.Fatalf("missing authored failure: %+v %v", p, err)
				}
				if p.ReportDigest != source.Report.EmployeeMessageSourceDigest(source.Status) || !p.Deadline.IsZero() || p.EffectiveLimits.MaxRuntimeSeconds != 0 {
					t.Fatal("source changed or execution budget inherited")
				}
				if err = w.completeEmployeeSchedule(auth, repo, b.ID, source.ID); err != nil || llm.calls != 1 {
					t.Fatalf("replayed final: calls=%d err=%v", llm.calls, err)
				}
				publication, err := repo.(store.BotPublicationStore).PublishReport(auth, store.BotPublicationRequest{BotID: b.ID, RunID: source.ID, ReportSequence: source.StateSequence})
				if err != nil || !publication.EmployeeSpeech || publication.Text != p.FinalText {
					t.Fatalf("failure did not reach Home: %+v %v", publication, err)
				}
				replay, err := repo.(store.BotPublicationStore).PublishReport(auth, store.BotPublicationRequest{BotID: b.ID, RunID: source.ID, ReportSequence: source.StateSequence})
				if err != nil || replay.EventID != publication.EventID {
					t.Fatal("publication replay changed identity", err)
				}
				if len(jobs.List(auth)) != 0 {
					t.Fatal("failure activated a schedule")
				}
				after, err := repo.GetRun(auth, b.ID, source.ID)
				if err != nil || after.StateSequence != source.StateSequence || after.Report.EmployeeMessageSourceDigest(after.Status) != p.ReportDigest {
					t.Fatal("presentation modified committed source")
				}
			})
		}
	}
}
