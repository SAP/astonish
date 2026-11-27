package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
	"iter"
	"strings"
	"testing"
	"time"
)

type presentationHTTPModel struct {
	t         *testing.T
	calls     int
	fail      bool
	operation *store.BotOperation
}

func (*presentationHTTPModel) Name() string { return "immutable-pin" }
func (m *presentationHTTPModel) GenerateContent(_ context.Context, q *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		if q.Config == nil || len(q.Config.Tools) != 0 {
			m.t.Fatal("presentation exposed tools")
		}
		if m.fail {
			yield(nil, errors.New("scripted provider unavailable"))
			return
		}
		var input struct {
			Digest     string               `json:"source_digest"`
			Operations []store.BotOperation `json:"operations"`
		}
		if err := json.Unmarshal([]byte(q.Contents[0].Parts[0].Text), &input); err != nil {
			m.t.Fatal(err)
		}
		text := "I could not complete the requested inspection."
		if m.operation != nil {
			got, _ := json.Marshal(input.Operations)
			want, _ := json.Marshal([]store.BotOperation{*m.operation})
			if string(got) != string(want) {
				m.t.Fatalf("model did not receive persisted operation: %s", got)
			}
			if m.operation.Status == store.BotOperationApplied {
				text = "I updated my requested voice."
			} else {
				if m.operation.Receipt != nil && m.operation.Receipt.Schedule != nil {
					m.t.Fatal("blocked operation invented a success receipt")
				}
				if m.operation.Payload.Schedule != nil {
					text = "I could not activate the requested schedule."
				} else {
					text = "I could not update my voice without your authorization."
				}
			}
		}
		raw, _ := json.Marshal(map[string]string{"source_digest": input.Digest, "text": text})
		yield(&model.LLMResponse{Content: genai.NewContentFromText(string(raw), "model"), TurnComplete: true}, nil)
	}
}

func TestEmployeePresentationHTTPAcceptance(t *testing.T) {
	t.Run("unavailable-artifact-eligibility", employeePresentationUnavailableArtifactEligibility)
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, mode := range []string{"accepted", "model-failure", "cancelled", "expired", "operation-applied", "operation-blocked", "schedule-cancelled", "schedule-blocked-source"} {
			t.Run(fmt.Sprintf("%s/%s", scope, mode), func(t *testing.T) {
				f := newInteractiveHTTPFixture(t, "", nil, []string{"chat", "tool:execute"}, scope)
				principal, _ := execution.PrincipalFromContext(f.ctx)
				principal.Authentication = execution.AuthMethodPlatformJWT
				ctx, err := execution.WithPrincipal(f.ctx, principal)
				if err != nil {
					t.Fatal(err)
				}
				ctx, err = execution.WithAuthenticationEvidence(ctx, execution.AuthenticationEvidence{Method: principal.Authentication, ExpiresAt: time.Now().Add(time.Hour)})
				if err != nil {
					t.Fatal(err)
				}
				policy := func(_ context.Context, ref store.BotMandateRef) (store.BotMandatePolicy, error) {
					ttl := time.Hour
					if mode == "expired" {
						ttl = time.Second
					}
					constraints := []execution.BotActionResourceConstraint{{Action: execution.CapabilityChat, Resources: []string{ref.BotID.String()}}, {Action: execution.CapabilityToolExecute, Resources: []string{"ask_user"}}}
					if mode == "operation-applied" {
						constraints = append(constraints, execution.BotActionResourceConstraint{Action: execution.CapabilityToolExecute, Resources: []string{entstore.RoutineControlResource(store.BotControlPersona)}})
					}
					return store.BotMandatePolicy{Scopes: []string{"chat", "tool:execute"}, Constraints: constraints, Limits: execution.BotMandateLimits(config.DefaultBotMandateConfig()), TTL: ttl}, nil
				}
				ctx = store.WithBotMandateAdmission(store.WithBotMessageAdmission(ctx, f.registry), policy)
				receipt, err := f.repo.(store.BotMessageStore).AdmitMessage(ctx, store.BotMessageRequest{BotID: f.b.ID, Version: f.b.Version, Instructions: "Inspect the signal", IdempotencyKey: "final"})
				if err != nil {
					t.Fatal(err)
				}
				run := receipt.Run
				runtime, err := entstore.NewBotMandateRuntime(f.repo, policy)
				if err != nil {
					t.Fatal(err)
				}
				ctx = execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: f.b.ID, RunID: run.ID, Version: run.Version, ActorID: principal.Subject, OrgSlug: principal.OrgSlug, TeamSlug: principal.TeamSlug})
				if err = runtime.ReserveBotMandateRun(ctx, *run.MandateRef, run.ID); err != nil {
					t.Fatal(err)
				}
				lease, err := f.repo.ClaimRun(ctx, store.BotClaimRequest{BotID: f.b.ID, RunID: run.ID, WorkerID: "presentation-http", ExpiresAt: time.Now().Add(time.Minute)})
				if err != nil {
					t.Fatal(err)
				}
				work, err := f.repo.(store.BotWorkStore).LoadWork(ctx, lease)
				if err != nil {
					t.Fatal(err)
				}
				var operation *store.BotOperation
				var operations store.BotAuthorizedOperationStore
				if strings.HasPrefix(mode, "operation-") || strings.HasPrefix(mode, "schedule-") {
					operations, err = entstore.NewBotAuthorizedOperationRuntime(f.repo, policy, func(context.Context, store.BotScheduleAuthorization) error { return nil }, func(context.Context, store.Bot, store.BotVersion) error {
						if mode == "schedule-blocked-source" {
							return store.ErrBotUnsupported
						}
						return nil
					})
					if err != nil {
						t.Fatal(err)
					}
					payload := store.BotControlPayload{Operation: store.BotControlPersona, Reason: "owner requested voice", Persona: &store.BotPersona{Language: "en", Timezone: "UTC", Soul: "concise", Identity: "Employee", User: "owner"}}
					if strings.HasPrefix(mode, "schedule-") {
						payload = store.BotControlPayload{Operation: store.BotControlScheduleCreate, Reason: "owner requested recurrence", Schedule: &store.BotControlSchedule{ExpectedRevision: f.b.Revision, Trigger: store.TriggerSpec{Kind: store.TriggerCron, Cron: "0 9 * * *", Timezone: "UTC", Instructions: "Inspect the signal", Enabled: true}}}
					}
					operation, err = operations.EnqueueAuthorizedOperation(ctx, store.BotAuthorizedOperationRequest{Lease: lease, MandateRef: *run.MandateRef, IdempotencyKey: "persisted-persona", Payload: payload})
					if err != nil {
						t.Fatal(err)
					}
				}
				status := store.RunFailed
				if mode == "cancelled" || mode == "schedule-cancelled" {
					status = store.RunCancelled
				}
				if mode == "schedule-blocked-source" {
					status = store.RunPartial
				}
				commitCtx := ctx
				if strings.HasPrefix(mode, "schedule-") {
					commitCtx = entstore.WithEmployeeScheduleCoordinator(ctx)
				}
				if err = f.repo.CommitRun(commitCtx, store.BotRunCommit{Lease: lease, Status: status, ModelSelection: &store.SessionPin{Provider: "controlled", Model: "immutable-pin"}, Report: store.BotReport{Summary: "PRIVATE_WORKER_DIAGNOSTIC", Work: store.BuildBotWorkProjection(work)}}); err != nil {
					t.Fatal(err)
				}
				if err = runtime.ReleaseBotMandateRun(ctx, *run.MandateRef, run.ID); err != nil {
					t.Fatal(err)
				}
				if operation != nil {
					if strings.HasPrefix(mode, "schedule-") {
						coordinator := operations.(interface {
							CoordinateEmployeeSchedules(context.Context, store.BotMandateRef, uuid.UUID) ([]store.BotOperation, error)
						})
						ops, coordinateErr := coordinator.CoordinateEmployeeSchedules(ctx, *run.MandateRef, run.ID)
						if len(ops) != 1 || ops[0].Status != store.BotOperationCancelled || ops[0].Receipt == nil || ops[0].Receipt.Schedule != nil {
							t.Fatalf("definite schedule cancellation missing: %+v %v", ops, coordinateErr)
						}
					}
					if mode == "operation-applied" {
						operation, err = operations.ApplyAuthorizedOperation(ctx, *run.MandateRef, operation.ID)
						if err != nil {
							t.Fatal(err)
						}
					}
					operation, err = operations.ReadAuthorizedOperation(ctx, *run.MandateRef, operation.ID)
					if err != nil {
						t.Fatal(err)
					}
					if (operation.Status == store.BotOperationApplied) != (mode == "operation-applied") || (mode == "operation-blocked" && (operation.Status != store.BotOperationBlocked || operation.Receipt != nil)) {
						t.Fatalf("unexpected durable operation: %+v", operation)
					}
					current, readErr := f.repo.Get(ctx, f.b.ID)
					if readErr != nil {
						t.Fatal(readErr)
					}
					version, readErr := f.repo.GetVersion(ctx, f.b.ID, current.Version)
					if readErr != nil {
						t.Fatal(readErr)
					}
					if mode == "operation-applied" {
						if !operation.HasVerifiedMetadataReceipt() || version.Persona.Soul != "concise" {
							t.Fatal("applied evidence did not match independently read persona")
						}
					} else if current.Version != f.b.Version {
						t.Fatal("blocked operation mutated the Bot version")
					}
				}
				source, err := f.repo.GetRun(ctx, f.b.ID, run.ID)
				if err != nil {
					t.Fatal(err)
				}
				q := store.BotEmployeePresentationRequest{BotID: f.b.ID, RunID: run.ID}
				if mode == "expired" {
					time.Sleep(1100 * time.Millisecond)
				}
				p, err := f.repo.(store.BotEmployeePresentationPreparer).PrepareEmployeePresentation(ctx, q)
				if err != nil {
					t.Fatal(err)
				}
				presentation := f.repo.(store.BotEmployeePresentationStore)
				llm := &presentationHTTPModel{t: t, fail: mode == "model-failure", operation: operation}
				if mode != "cancelled" && mode != "expired" {
					q.ClaimToken = "private-claim"
					p, err = presentation.ClaimEmployeePresentation(ctx, store.BotEmployeePresentationClaim{BotEmployeePresentationRequest: q, ExpectedReportSequence: p.ReportSequence, ExpectedReportDigest: p.ReportDigest})
					if err != nil {
						t.Fatal(err)
					}
					if p.State != store.BotEmployeePresentationClaimed || p.ClaimToken != q.ClaimToken || p.BotID != q.BotID || p.RunID != q.RunID {
						t.Fatal("presentation claim lost state or binding")
					}
					factory, err := launcher.NewBotEmployeePresentationFactory(launcher.BotEmployeePresentationFactoryConfig{
						LoadScope: func(c context.Context, r bot.RunRequest) (*launcher.BotChatScope, error) {
							return &launcher.BotChatScope{Services: &store.Services{Bots: f.repo, PersonalBots: f.repo}, Cleanup: func() {}}, nil
						},
						ResolvePinned: func(_ context.Context, _ *launcher.BotChatScope, provider, name string) (model.LLM, error) {
							if provider != "controlled" || name != "immutable-pin" {
								t.Fatal("lost original pin")
							}
							return llm, nil
						},
						WrapModel: func(c context.Context, m model.LLM, b execution.RunBudget) (model.LLM, error) {
							if err := b.ReserveTool(c, "anything"); !errors.Is(err, store.ErrBotForbidden) {
								t.Fatal("presentation gained tools")
							}
							return &presentationHTTPMeter{LLM: m, budget: b}, nil
						},
					})
					if err != nil {
						t.Fatal(err)
					}
					request := launcher.BotEmployeePresentationRequest{Bot: f.b, Source: *source, Claim: q, Store: presentation, DispatchToken: "private-dispatch", Voice: "Helpful Employee"}
					if operation != nil {
						request.Operations = []store.BotOperation{*operation}
					}
					text, generateErr := factory(ctx, request)
					// A started/settled dispatch cannot be retried after a crash
					// window between generation and completion.
					if _, retryErr := factory(ctx, request); retryErr == nil || llm.calls != 1 {
						t.Fatal("claim redispatched the model")
					}
					complete := store.BotEmployeePresentationCompletion{BotEmployeePresentationRequest: q, DispatchToken: request.DispatchToken, Text: text}
					if mode == "model-failure" {
						if generateErr == nil || text != "" {
							t.Fatal("model failure fabricated speech")
						}
						complete.UnavailableReason = store.BotEmployeeReasonAgentNoResult
					} else if generateErr != nil {
						t.Fatal(generateErr)
					}
					p, err = presentation.CompleteEmployeePresentation(ctx, complete)
					if err != nil {
						t.Fatal(err)
					}
					if _, err = factory(ctx, request); err == nil || llm.calls != 1 {
						t.Fatal("completed dispatch called model again")
					}
				}
				if _, err := f.repo.(store.BotPublicationStore).PublishReport(ctx, store.BotPublicationRequest{BotID: f.b.ID, RunID: run.ID, ReportSequence: p.ReportSequence + 1}); err == nil {
					t.Fatal("publication accepted a foreign sequence")
				}
				pub, err := f.repo.(store.BotPublicationStore).PublishReport(ctx, store.BotPublicationRequest{BotID: f.b.ID, RunID: run.ID, ReportSequence: p.ReportSequence})
				if err != nil {
					t.Fatal(err)
				}
				replay, err := f.repo.(store.BotPublicationStore).PublishReport(ctx, store.BotPublicationRequest{BotID: f.b.ID, RunID: run.ID, ReportSequence: p.ReportSequence})
				if err != nil || replay.EventID != pub.EventID {
					t.Fatal("duplicate publication")
				}
				if mode == "accepted" || strings.HasPrefix(mode, "operation-") || strings.HasPrefix(mode, "schedule-") {
					if pub.Suppressed || pub.Text != p.FinalText {
						t.Fatalf("final not published: %+v", pub)
					}
				} else if pub.EmployeeSpeech || pub.Text != "" {
					t.Fatalf("unavailable presentation fabricated assistant speech: %+v", pub)
				}
				after, err := f.repo.GetRun(ctx, f.b.ID, run.ID)
				if err != nil || after.Status != source.Status || after.StateSequence != source.StateSequence || !after.StateCommittedAt.Equal(source.StateCommittedAt) || after.Report.EmployeeMessageSourceDigest(after.Status) != p.ReportDigest {
					t.Fatalf("presentation rewrote source: read=%v before=%+v after=%+v presentation=%+v", err, source, after, p)
				}
				if q.ClaimToken != "" {
					if _, readErr := presentation.ReadEmployeePresentation(ctx, q); readErr != nil {
						t.Fatalf("publication invalidated the private presentation binding: %v", readErr)
					}
				}
				raw := f.request("GET", f.path+"/sessions/"+f.b.HomeSessionID.String()+"/stream", "", 200)
				if strings.Contains(string(raw), "PRIVATE_WORKER_DIAGNOSTIC") {
					t.Fatal("private worker leaked")
				}
				if (mode == "accepted" || strings.HasPrefix(mode, "operation-") || strings.HasPrefix(mode, "schedule-")) && strings.Count(string(raw), p.FinalText) != 1 {
					t.Fatalf("HTTP history must show exactly one reply: %s", raw)
				}
				if mode == "model-failure" || mode == "cancelled" || mode == "expired" {
					// The stream reloads and projects actual persisted Home rows.
					// Check assistant event kinds, not a particular fallback phrase;
					// neutral product status remains allowed.
					for _, frame := range strings.Split(string(raw), "\n\n") {
						for _, line := range strings.Split(frame, "\n") {
							if line == "event: text" || line == "event: thinking" {
								t.Fatalf("unavailable presentation leaked assistant content into persisted Home: %s", raw)
							}
						}
					}
					files, fileErr := pub.FilePublication()
					if fileErr != nil || files != nil || len(pub.Artifacts) != 0 {
						t.Fatalf("artifact-free source invented file delivery: %+v %v", files, fileErr)
					}
				}
			})
		}
	}
}

// File delivery has its own immutable-content contract; it does not require
// Employee speech. This is a projection fixture, not a claimed runtime receipt.
func employeePresentationUnavailableArtifactEligibility(t *testing.T) {
	botID, runID, home := uuid.New(), uuid.New(), uuid.New()
	digest := strings.Repeat("a", 64)
	artifact := store.BotArtifact{ID: uuid.New(), HomeID: home, RelativePath: "result.txt", SHA256: digest, SizeBytes: 1, MediaType: "text/plain", ContentRef: &store.BotContentRef{Format: store.BotContentFormatVersion, Scope: store.BotContentScope{OrgID: "org", TeamID: "team", OwnerID: uuid.NewString(), BotID: botID}, Kind: store.BotContentArtifact, Key: "private-key", SHA256: digest, SizeBytes: 1, Revision: 1}}
	for _, suppressed := range []bool{false, true} {
		t.Run(fmt.Sprintf("suppressed=%t", suppressed), func(t *testing.T) {
			publication := store.BotPublication{BotID: botID, RunID: runID, HomeSessionID: uuid.New(), ReportSequence: 7, EventID: store.BotPublicationEventID(runID, 7), Suppressed: suppressed, EmployeeSpeech: false, Text: "", Artifacts: []store.BotArtifact{artifact}}
			file, err := publication.FilePublication()
			if err != nil || file == nil || len(file.Artifacts) != 1 || file.Summary != "" || file.Artifacts[0].ID != artifact.ID || file.Artifacts[0].SHA256 != digest {
				t.Fatalf("unavailable speech blocked immutable artifact eligibility: %+v %v", file, err)
			}
			publication.Artifacts[0].ContentRef = nil
			if _, err := publication.FilePublication(); !errors.Is(err, store.ErrBotInvalid) {
				t.Fatalf("unavailable speech admitted unverified artifact: %v", err)
			}
		})
	}
}

type presentationHTTPMeter struct {
	model.LLM
	budget execution.RunBudget
}

func (m *presentationHTTPMeter) GenerateContent(ctx context.Context, q *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		reservation, err := m.budget.ReserveModel(ctx, execution.BudgetReservation{ID: uuid.New(), InputTokens: 10, OutputTokens: 10})
		if err != nil {
			yield(nil, err)
			return
		}
		defer func() {
			if err := m.budget.SettleModel(ctx, reservation.ID, execution.BudgetUsage{InputTokens: 3, OutputTokens: 3}); err != nil {
				yield(nil, err)
			}
		}()
		for response, err := range m.LLM.GenerateContent(ctx, q, stream) {
			if !yield(response, err) {
				return
			}
		}
	}
}
