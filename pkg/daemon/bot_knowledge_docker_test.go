package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/sandbox/docker"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// This owned live harness uses the production commit hook, scoped model resolver,
// worker reconciliation and physical Docker publisher. Held is never success.
func TestBotKnowledgeDockerEveryTurn(t *testing.T) {
	if os.Getenv("ASTONISH_TEST_TASK_WORKSPACE") != "1" {
		t.Skip("explicit owned Docker acceptance required")
	}
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			c, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			dir := t.TempDir()
			_, platform, err := entstore.NewPlatformServices(c, entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
			if err != nil {
				t.Fatal(err)
			}
			defer platform.Close()
			org := &store.Organization{ID: uuid.NewString(), Slug: "knowledge", Name: "Knowledge", Status: "active"}
			if err = platform.Organizations().Create(c, org); err != nil {
				t.Fatal(err)
			}
			if err = platform.ProvisionOrg(c, org.ID, org.Slug); err != nil {
				t.Fatal(err)
			}
			data, err := platform.ForOrg(org.Slug)
			if err != nil {
				t.Fatal(err)
			}
			team := &store.Team{ID: uuid.NewString(), Slug: "team", Name: "Team", SchemaName: "team_team"}
			if err = data.Teams().CreateTeam(c, team); err != nil {
				t.Fatal(err)
			}
			owner := uuid.New()
			if err = platform.Users().Create(c, &store.User{ID: owner.String(), Email: owner.String() + "@test.invalid", DisplayName: "Owner", Status: "active"}); err != nil {
				t.Fatal(err)
			}
			if err = platform.Organizations().AddMember(c, owner.String(), org.ID, "member"); err != nil {
				t.Fatal(err)
			}
			if err = data.Teams().AddMember(c, &store.TeamMembership{UserID: owner.String(), TeamID: team.ID, Role: "member"}); err != nil {
				t.Fatal(err)
			}
			backend, err := docker.New(docker.Config{Sessions: sandbox.NewSessionRegistryFromStore(nil), LayersDir: filepath.Join(dir, "layers"), UppersDir: filepath.Join(dir, "uppers"), SandboxImage: os.Getenv("ASTONISH_SANDBOX_IMAGE")})
			if err != nil {
				t.Fatal(err)
			}
			llm := &knowledgeAdapterModel{}
			config := BotWorkerConfig{Backend: platform, Sandbox: backend, Pool: workerTestPool{}, WorkerID: "owned-knowledge", ContextWindow: 8192, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 2048, SupportsOutputLimit: true}, ResolveModel: func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
				return llm, nil
			}, Prepare: func(context.Context, bot.RunRequest, *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
				return nil, store.ErrBotUnsupported
			}}
			w, err := NewBotWorker(config)
			if err != nil {
				t.Fatal(err)
			}
			auth, _, err := w.authenticateOwner(c, BotSchedulerScope{org.Slug, team.Slug, owner.String()})
			if err != nil {
				t.Fatal(err)
			}
			repo, ss, pin := data.ForTeam(team.Slug).Bots(), data.ForTeam(team.Slug).Sessions(), data.ForTeam(team.Slug).SetSessionPin
			if scope == store.BotScopePersonal {
				repo, ss, pin = data.ForUser(owner.String()).Bots(), data.ForUser(owner.String()).Sessions(), data.ForUser(owner.String()).SetSessionPin
			}
			v := store.DefaultBotVersion()
			v.Charter = "Retain owner formatting preferences"
			b, err := repo.CreateDraft(auth, store.BotCreateRequest{Name: "Owned knowledge", OwnerID: owner, Version: v, IdempotencyKey: "create"})
			if err != nil {
				t.Fatal(err)
			}
			h, err := backend.EnsureBotHome(auth, sandbox.BotHomeScope{OrgID: org.Slug, TeamID: team.Slug, UserID: owner.String(), BotID: b.ID.String()})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				clean, stop := context.WithTimeout(context.Background(), 20*time.Second)
				defer stop()
				if e := backend.DeleteBotHome(clean, h); e != nil {
					t.Error(e)
				}
			})
			service, err := execution.WithPrincipal(c, execution.Principal{Kind: execution.PrincipalKindService, ClientID: "owned-knowledge-test", Authentication: execution.AuthMethod("internal"), Surface: execution.SurfaceCode, OrgSlug: org.Slug, TeamSlug: team.Slug, Authenticated: true, Scopes: []string{entstore.BotHomeRegistrationScope}})
			if err != nil {
				t.Fatal(err)
			}
			home := store.BotHomeBinding{ID: uuid.New(), BotID: b.ID, Scope: scope, Backend: string(h.Backend), StorageKey: h.Ref, Revision: 1}
			if err = entstore.RegisterBotHomeBinding(service, repo, entstore.BotHomeRegistration{BotID: b.ID, ExpectedRevision: b.Revision, Home: home}, func(context.Context, entstore.BotHomeRegistration) error { return nil }); err != nil {
				t.Fatal(err)
			}
			if err = pin(auth, b.HomeSessionID.String(), "controlled", "model"); err != nil {
				t.Fatal(err)
			}
			platform.SetBotActivationReadiness(func(context.Context, store.Bot, store.BotVersion) error { return nil })
			b, err = repo.Get(auth, b.ID)
			if err != nil {
				t.Fatal(err)
			}
			b, err = repo.Activate(auth, store.BotLifecycleRequest{BotID: b.ID, ActorID: owner, ExpectedRevision: b.Revision, Reason: "owned test"})
			if err != nil {
				t.Fatal(err)
			}
			journal := repo.(store.BotKnowledgeExtractionStore)
			commit := func(key, message string) *store.BotKnowledgeExtraction {
				t.Helper()
				run, e := repo.RequestRun(auth, store.BotRunRequest{BotID: b.ID, Version: b.Version, Trigger: store.TriggerManual, Instructions: message, IdempotencyKey: key})
				if e != nil {
					t.Fatal(e)
				}
				worker := execution.WithBotRunIdentity(auth, execution.BotRunIdentity{BotID: b.ID, RunID: run.ID, Version: b.Version, ActorID: owner.String(), OrgSlug: org.Slug, TeamSlug: team.Slug})
				lease, e := repo.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: run.ID, WorkerID: w.config.WorkerID, ExpiresAt: time.Now().Add(time.Minute)})
				if e != nil {
					t.Fatal(e)
				}
				worker = w.knowledgeCommitContext(worker, repo, ss)
				report := reviewedRecoveryReply(t, repo, worker, lease, "No tools needed")
				if e = repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: report}); e != nil {
					t.Fatal(e)
				}
				activities, e := repo.ListActivity(auth, b.ID, 0, 200)
				if e != nil {
					t.Fatal(e)
				}
				for _, a := range activities {
					if a.RunID == nil || *a.RunID != run.ID {
						continue
					}
					var env struct {
						Source *store.BotKnowledgeExtractionSource `json:"knowledge_source"`
					}
					if e = json.Unmarshal(a.Payload, &env); e != nil {
						t.Fatal(e)
					}
					if env.Source != nil {
						r, e := journal.ReadBotKnowledgeExtraction(auth, *env.Source)
						if e != nil {
							t.Fatal(e)
						}
						if r.State != store.BotKnowledgeExtractionPending || !r.Input.Complete || r.Input.OwnerMessage != message {
							t.Fatalf("expected complete Pending, got %+v", r)
						}
						return r
					}
				}
				t.Fatal("terminal commit has no extraction receipt")
				return nil
			}
			proposal := func(content, digest string) store.BotKnowledgeExtractionDecision {
				return store.BotKnowledgeExtractionDecision{SchemaVersion: 1, Kind: store.BotKnowledgeDecisionChanges, Reason: "owner format preference", Proposals: []store.BotKnowledgeProposal{{Kind: store.BotKnowledgeProposalPreference, Path: "preferences/format.md", ExpectedDigest: digest, Content: content}}}
			}
			setResponse := func(d store.BotKnowledgeExtractionDecision) {
				raw, e := json.Marshal(d)
				if e != nil {
					t.Fatal(e)
				}
				llm.response = &model.LLMResponse{Content: genai.NewContentFromText(string(raw), "model"), UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 100, CandidatesTokenCount: 80}}
			}
			check := func(r *store.BotKnowledgeExtraction, content string) {
				t.Helper()
				current, e := journal.ReadBotKnowledgeExtraction(auth, r.Input.Source)
				if e != nil {
					t.Fatal(e)
				}
				if current.State != store.BotKnowledgeExtractionCommitted {
					t.Fatalf("not committed: %+v", current)
				}
				files, e := backend.SnapshotBotKnowledge(auth, h)
				if e != nil || len(files) != 1 || files[0].Path != "preferences/format.md" || files[0].Summary != content || files[0].Digest != (sandbox.BotHomeWorkspaceFile{Data: []byte(content)}).Digest() {
					t.Fatalf("physical file bytes %+v error %v", files, e)
				}
			}
			first := "# Format\nUse clear headings.\n"
			r := commit("first", "Use clear headings")
			setResponse(proposal(first, ""))
			if err = w.reconcileKnowledge(auth, repo, ss, *b); err != nil {
				t.Fatal(err)
			}
			check(r, first)
			if llm.calls != 1 || len(llm.request.Tools) != 0 || len(llm.request.Config.Tools) != 0 {
				t.Fatalf("dedicated extraction calls/tools: %+v", llm)
			}
			second := "# Format\nUse compact bullets.\n"
			r = commit("correction", "Use compact bullets")
			if len(r.Input.PriorFiles) != 1 {
				t.Fatalf("prior physical snapshot %+v", r.Input.PriorFiles)
			}
			setResponse(proposal(second, (sandbox.BotHomeWorkspaceFile{Data: []byte(first)}).Digest()))
			if err = w.reconcileKnowledge(auth, repo, ss, *b); err != nil {
				t.Fatal(err)
			}
			check(r, second)
			if llm.calls != 2 {
				t.Fatalf("short correction skipped: %d", llm.calls)
			}
			var modelInput store.BotKnowledgeExtractionInput
			if err = json.Unmarshal([]byte(llm.request.Contents[0].Parts[0].Text), &modelInput); err != nil || modelInput.OwnerMessage != "Use compact bullets" || modelInput.Source.RunID != r.Input.Source.RunID {
				t.Fatalf("terminal-session extraction input %+v %v", modelInput, err)
			}
			metered, err := repo.GetRun(auth, b.ID, r.Input.Source.RunID)
			if err != nil || metered.Usage.InputTokens < 100 || metered.Usage.OutputTokens < 80 {
				t.Fatalf("auxiliary usage not persisted %+v %v", metered, err)
			}
			// Persist a Prepared decision then reconstruct the production worker: no model replay.
			third := "# Format\nUse compact bullets and dated headings.\n"
			r = commit("restart", "Add dated headings")
			lease, err := journal.ClaimBotKnowledgeExtraction(auth, r.Input.Source, w.config.WorkerID, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			extracting, started, err := journal.BeginBotKnowledgeExtraction(auth, lease, r.Revision)
			if err != nil || !started {
				t.Fatalf("begin %v %v", started, err)
			}
			d := proposal(third, (sandbox.BotHomeWorkspaceFile{Data: []byte(second)}).Digest())
			if _, err = journal.TransitionBotKnowledgeExtraction(auth, store.BotKnowledgeExtractionTransition{Lease: lease, ExpectedRevision: extracting.Revision, From: store.BotKnowledgeExtractionExtracting, To: store.BotKnowledgeExtractionPrepared, Decision: &d, DecisionDigest: store.BotKnowledgeDigest(d)}); err != nil {
				t.Fatal(err)
			}
			restarted, err := NewBotWorker(config)
			if err != nil {
				t.Fatal(err)
			}
			if err = restarted.reconcileKnowledge(auth, repo, ss, *b); err != nil {
				t.Fatal(err)
			}
			check(r, third)
			if err = restarted.reconcileKnowledge(auth, repo, ss, *b); err != nil {
				t.Fatal(err)
			}
			if llm.calls != 2 {
				t.Fatalf("prepared/committed model replay: %d", llm.calls)
			}
			t.Log("physical committed preference after first turn, short correction, prepared worker restart; no save tool or model replay")
		})
	}
}
