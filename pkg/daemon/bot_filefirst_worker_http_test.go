package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// Controlled compute, real temporary retained bytes. No Docker or external model
// is involved; physical backend/remount acceptance remains a separate tier.
type fileFirstHTTPBackend struct{ setupHomeFilesBackend }

func (*fileFirstHTTPBackend) DestroySession(context.Context, string) error         { return nil }
func (*fileFirstHTTPBackend) VerifySessionDestroyed(context.Context, string) error { return nil }
func (s *fileFirstHTTPBackend) OpenBotHomeFile(c context.Context, h *sandbox.BotHomeBinding, p string, r store.BotFileRange) (*store.BotFileStream, error) {
	e, err := s.StatBotHomeFile(c, h, p)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(s.documentsRoot, h.Scope.BotID, p))
	if err != nil {
		return nil, err
	}
	if r.Offset < 0 || r.Offset > int64(len(data)) {
		return nil, store.ErrBotInvalid
	}
	data = data[r.Offset:]
	if r.Length > 0 && r.Length < int64(len(data)) {
		data = data[:r.Length]
	}
	return &store.BotFileStream{ReadCloser: io.NopCloser(bytes.NewReader(data)), Entry: *e}, nil
}
func (s *fileFirstHTTPBackend) WriteBotHomeFile(c context.Context, h *sandbox.BotHomeBinding, w store.BotFileWrite) (*store.BotFileEntry, error) {
	if w.CreateOnly {
		return s.setupHomeFilesBackend.WriteBotHomeFile(c, h, w)
	}
	if w.BeforeCommit == nil {
		return nil, store.ErrBotForbidden
	}
	data, err := io.ReadAll(w.Body)
	if err != nil {
		return nil, err
	}
	err = w.BeforeCommit(func() error {
		e, err := s.StatBotHomeFile(c, h, w.Path)
		if err != nil {
			return err
		}
		if e.SHA256 != w.ExpectedSHA256 {
			return store.ErrBotConflict
		}
		return os.WriteFile(filepath.Join(s.documentsRoot, h.Scope.BotID, w.Path), data, 0600)
	})
	if err != nil {
		return nil, err
	}
	return s.StatBotHomeFile(c, h, w.Path)
}
func (s *fileFirstHTTPBackend) PublishBotArtifactSnapshot(c context.Context, h *sandbox.BotHomeBinding, p, key string, barrier func(func() error) error) (*store.BotContentRef, error) {
	if barrier == nil {
		return nil, store.ErrBotForbidden
	}
	data, err := os.ReadFile(filepath.Join(s.documentsRoot, h.Scope.BotID, p))
	if err != nil {
		return nil, err
	}
	err = barrier(func() error {
		target := filepath.Join(s.documentsRoot, h.Scope.BotID, ".snapshots", key)
		if e := os.MkdirAll(filepath.Dir(target), 0700); e != nil {
			return e
		}
		return os.WriteFile(target, data, 0400)
	})
	if err != nil {
		return nil, err
	}
	return &store.BotContentRef{Format: store.BotContentFormatVersion, Scope: store.BotContentScope{OrgID: h.Scope.OrgID, TeamID: h.Scope.TeamID, OwnerID: h.Scope.UserID, BotID: uuid.MustParse(h.Scope.BotID)}, Kind: store.BotContentArtifact, Key: key, SHA256: fmt.Sprintf("%x", sha256.Sum256(data)), SizeBytes: int64(len(data)), Revision: 1}, nil
}

func (s *fileFirstHTTPBackend) OpenBotContent(c context.Context, h *sandbox.BotHomeBinding, ref store.BotContentRef, r store.BotFileRange) (*store.BotFileStream, error) {
	data, err := os.ReadFile(filepath.Join(s.documentsRoot, h.Scope.BotID, ".snapshots", ref.Key))
	if err != nil {
		return nil, err
	}
	if ref.SHA256 != fmt.Sprintf("%x", sha256.Sum256(data)) || ref.SizeBytes != int64(len(data)) {
		return nil, store.ErrBotConflict
	}
	entry := store.BotFileEntry{Kind: store.BotFileRegular, Path: ref.Key, SHA256: ref.SHA256, SizeBytes: ref.SizeBytes}
	if r.Offset > int64(len(data)) {
		return nil, store.ErrBotInvalid
	}
	data = data[r.Offset:]
	if r.Length > 0 && r.Length < int64(len(data)) {
		data = data[:r.Length]
	}
	return &store.BotFileStream{Entry: entry, ReadCloser: io.NopCloser(bytes.NewReader(data))}, nil
}

// fileFirstHTTPPool is the controlled node transport, not a replacement tool
// catalog. It executes Chat's real file implementation against test-owned bytes.
// Reject everything outside this fixture before allowing local execution.
type fileFirstHTTPPool struct {
	titleSkillPool
	root    string
	session string
}

func (p *fileFirstHTTPPool) GetOrCreate(string) sandbox.ToolNodeClient                     { return p }
func (p *fileFirstHTTPPool) GetOrCreateWithTemplate(string, string) sandbox.ToolNodeClient { return p }
func (p *fileFirstHTTPPool) Call(session, name string, args map[string]interface{}) (json.RawMessage, error) {
	if session != p.session || (name != "write_file" && name != "read_file") {
		return nil, store.ErrBotForbidden
	}
	path, _ := args["path"].(string)
	if path != "outputs/report.md" && path != "outputs/design.mmd" {
		return nil, store.ErrBotForbidden
	}
	local := make(map[string]interface{}, len(args))
	for key, value := range args {
		local[key] = value
	}
	local["path"] = filepath.Join(p.root, path)
	result, err := tools.ExecuteTool(context.Background(), name, local, "file-first-test")
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}

// fileFirstHTTPApprovals represents explicit fixture-owner approval, not a
// classifier. Each exact action is bound to this live attempt and consumed once.
// Production membership, lease and budget checks still run before consumption.
type fileFirstHTTPApprovals struct {
	live   botLiveGuard
	mu     sync.Mutex
	grants map[string]execution.ScopedGrant
	used   map[string]bool
}

func (a *fileFirstHTTPApprovals) Check(ctx context.Context) error { return a.live.check(ctx) }
func (a *fileFirstHTTPApprovals) AuthorizeAction(ctx context.Context, action execution.ActionDescriptor) (execution.ScopedGrant, error) {
	if err := a.Check(ctx); err != nil {
		return execution.ScopedGrant{}, err
	}
	digest, err := action.Digest()
	if err != nil {
		return execution.ScopedGrant{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	grant, ok := a.grants[digest]
	identity, _ := execution.BotRunIdentityFromContext(ctx)
	if !ok || a.used[digest] || identity != grant.Identity || !time.Now().Before(grant.ExpiresAt) {
		return execution.ScopedGrant{}, store.ErrBotForbidden
	}
	return grant, nil
}
func (a *fileFirstHTTPApprovals) Authorize(ctx context.Context, action execution.ActionDescriptor) (execution.PolicyDecision, error) {
	decision := execution.EvaluateRunPolicy(ctx, action, time.Now())
	if decision.Kind != execution.PolicyConsumeGrant {
		return a.live.Authorize(ctx, action)
	}
	grant, err := a.AuthorizeAction(ctx, action)
	if err != nil {
		return decision, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.used[grant.ActionDigest] || decision.GrantID != grant.ID || decision.ActionDigest != grant.ActionDigest {
		return decision, store.ErrBotForbidden
	}
	a.used[grant.ActionDigest] = true
	decision.Kind = execution.PolicyAllow
	return decision, nil
}

func fileFirstWriteArgs(path, content string) map[string]any {
	return map[string]any{"path": path, "content": content}
}

const fileFirstReport = "PRIVATE_OLD_DELIVERABLE_7c91: a detailed design retained only in the report."
const fileFirstSource = "graph TD\n  A[Editable source] --> B[Retained file]\n"

type fileFirstHTTPModel struct {
	t               *testing.T
	stage           int
	bridge          bool
	followup        bool
	initial         string
	retrieved       bool
	verifyApprovals func()
}

func (*fileFirstHTTPModel) Name() string { return "controlled-file-first" }
func (m *fileFirstHTTPModel) GenerateContent(_ context.Context, r *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	if response, ok := workerWorkAssessment(r, func(w *store.BotWorkState) bool {
		return w.Candidate != nil && (strings.Contains(w.Candidate.Text, "Snapshotted retained file outputs/report.md") || (m.followup && m.retrieved && w.Candidate.Text == "durable controlled result"))
	}); ok {
		return func(yield func(*model.LLMResponse, error) bool) {
			for answer, err := range response {
				if err != nil || answer == nil {
					yield(answer, err)
					return
				}
				var input workerContentInput
				_ = json.Unmarshal([]byte(r.Contents[0].Parts[0].Text), &input)
				var review map[string]any
				_ = json.Unmarshal([]byte(answer.Content.Parts[0].Text), &review)
				if _, reviewing := review["content_assessments"]; reviewing {
					assessments := []workerContentAssessment{}
					for _, item := range input.Items {
						a := workerAssessContent(input, item)
						switch item.Text {
						case fileFirstReport, "graph TD", "  A[Editable source] --> B[Retained file]":
							// These exact fixture bodies are an illustrative design and
							// diagram, not factual claims about an external system.
							a.ClaimType, a.Support = workerClaimNonfactual, store.BotCriterionSatisfied
							a.Explanation = "Controlled illustrative design/source content; no external fact or execution outcome asserted."
						}
						for ref, raw := range input.ExecutionEvidence {
							var evidence store.BotEvidence
							if json.Unmarshal([]byte(raw), &evidence) == nil && evidence.Summary == item.Text && strings.HasPrefix(evidence.Source, "retained-file:sha256:") {
								a.ClaimType, a.Support = workerClaimExecution, store.BotCriterionSatisfied
								a.ExecutionSources = []workerContentSource{{Ref: ref, Quote: raw}}
								a.Explanation = "Exact independently read-back retained file observation."
							}
						}
						assessments = append(assessments, a)
					}
					review["content_assessments"] = assessments
					data, _ := json.Marshal(review)
					answer.Content = genai.NewContentFromText(string(data), "model")
				}
				if !yield(answer, nil) {
					return
				}
			}
		}
	}
	return func(yield func(*model.LLMResponse, error) bool) {
		raw, _ := json.Marshal(r)
		text := string(raw)
		if m.stage == 0 {
			m.initial = text
			if !strings.Contains(text, "CRITICAL_FILE_INSTRUCTION") || !strings.Contains(text, "MEMORY.md") {
				m.t.Fatal("critical files missing from actual provider request")
			}
			if m.followup && (!strings.Contains(text, "outputs/design.mmd") || !strings.Contains(text, "Committed report published") || !strings.Contains(text, "Create a report and an editable diagram")) {
				m.t.Fatal("compact publication memory absent from follow-up provider request")
			}
			if m.followup && strings.Contains(text, fileFirstReport) {
				m.t.Fatal("old deliverable auto-loaded before explicit read")
			}
		}
		name := ""
		args := map[string]any{}
		if m.followup {
			if m.stage == 0 {
				name = "read_file"
				args = map[string]any{"path": "outputs/report.md"}
			} else {
				m.retrieved = strings.Contains(text, fileFirstReport)
				if !m.retrieved {
					m.t.Fatalf("explicit read did not retrieve report: %s", text)
				}
			}
		} else {
			switch m.stage {
			case 0:
				name = "write_file"
				args = fileFirstWriteArgs("outputs/report.md", fileFirstReport)
			case 1:
				name = "write_file"
				args = fileFirstWriteArgs("outputs/design.mmd", fileFirstSource)
			case 2:
				m.verifyApprovals()
				name = "publish_file"
				args = map[string]any{"path": "outputs/report.md", "title": "Design report", "purpose": "Design report", "memory_summary": "Design report saved at outputs/report.md"}
			case 3:
				name = "publish_file"
				args = map[string]any{"path": "outputs/design.mmd", "title": "Editable diagram", "purpose": "Editable source", "memory_summary": "Diagram saved at outputs/design.mmd"}
			}
		}
		m.stage++
		part := &genai.Part{Text: "Snapshotted retained file outputs/report.md\nSnapshotted retained file outputs/design.mmd"}
		if m.followup {
			// Retrieval is asserted against the actual tool response above; this
			// placeholder makes no unsupported execution claim in work review.
			part.Text = "durable controlled result"
		}
		if name != "" {
			if m.bridge || name == "publish_file" {
				name, args = "execute_tool", map[string]any{"name": name, "arguments": args}
			}
			part = &genai.Part{FunctionCall: &genai.FunctionCall{ID: uuid.NewString(), Name: name, Args: args}}
		}
		yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{part}}, TurnComplete: name == ""}, nil)
	}
}

// Real isolated HTTP admission, production worker/agent factory and SQLite.
// The controlled provider drives actual confined tools; assertions inspect bytes
// and provider requests, never fabricated model claims about tool success.
func TestBotFileFirstWorkerHTTP(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			db, owner := setupChannelResolverFixture(t, "file-first", "team", "telegram", "linked")
			org, err := db.ForOrg("file-first")
			if err != nil {
				t.Fatal(err)
			}
			repo, ss := org.ForTeam("team").Bots(), org.ForTeam("team").Sessions()
			pin := org.ForTeam("team").SetSessionPin
			if scope == store.BotScopePersonal {
				repo, ss = org.ForUser(owner).Bots(), org.ForUser(owner).Sessions()
				pin = org.ForUser(owner).SetSessionPin
			}
			auth, err := execution.WithPrincipal(t.Context(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner, OrgSlug: "file-first", TeamSlug: "team", Authenticated: true})
			if err != nil {
				t.Fatal(err)
			}
			auth, err = execution.WithAuthenticationEvidence(auth, execution.AuthenticationEvidence{Method: execution.AuthMethodPlatformJWT, ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			auth = store.WithTenantContext(auth, &store.TenantContext{OrgSlug: "file-first", TeamSlug: "team", UserID: owner})
			registry := execution.NewBotTurnAuthorizationRegistry(10)
			defer registry.Close()
			backend := &fileFirstHTTPBackend{setupHomeFilesBackend: setupHomeFilesBackend{documentsRoot: t.TempDir()}}
			mdl := &fileFirstHTTPModel{t: t, bridge: scope == store.BotScopeTeam}
			policy := newProductionBotMandatePolicy(db, &config.AppConfig{})
			var receipts *launcher.BotFileReceipts
			worker, err := NewBotWorker(BotWorkerConfig{Backend: db, MandatePolicy: policy, TurnRegistry: registry, Sandbox: backend, Pool: workerTestPool{}, WorkerID: "file-first-http", ContextWindow: 32768, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 100, SupportsOutputLimit: true, EstimateInput: func(context.Context, *model.LLMRequest) (int64, error) { return 100, nil }}, ResolveModel: func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
				return mdl, nil
			}, Prepare: func(c context.Context, q bot.RunRequest, s *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
				s.Services.BotFilesFactory = func(c context.Context, svc *store.Services, sc store.BotScope) (store.BotFiles, error) {
					return productionBotFiles(c, backend, svc, sc)
				}
				ts, rs, e := launcher.BotPublicationToolsWithReceipts(q, s)
				if e != nil {
					return nil, e
				}
				receipts = rs
				approvals := &fileFirstHTTPApprovals{live: botLiveGuard{repo: repo, request: q}, grants: map[string]execution.ScopedGrant{}, used: map[string]bool{}}
				identity, _ := execution.BotRunIdentityFromContext(c)
				if !mdl.followup {
					for path, content := range map[string]string{"outputs/report.md": fileFirstReport, "outputs/design.mmd": fileFirstSource} {
						raw, _ := json.Marshal(fileFirstWriteArgs(path, content))
						action := execution.ActionDescriptor{Tool: "write_file", Arguments: raw, Effect: execution.EffectUnknown}
						digest, err := action.Digest()
						if err != nil {
							return nil, err
						}
						approvals.grants[digest] = execution.ScopedGrant{ID: uuid.New(), Identity: identity, ActionDigest: digest, ExpiresAt: q.Run.Lease.ExpiresAt}
						for _, changed := range []map[string]any{fileFirstWriteArgs(path, content+" altered"), fileFirstWriteArgs("outputs/other.md", content), {"path": path, "content": content, "create_only": true}} {
							altered, _ := json.Marshal(changed)
							if _, err := approvals.AuthorizeAction(c, execution.ActionDescriptor{Tool: "write_file", Arguments: altered, Effect: execution.EffectUnknown}); err == nil {
								t.Fatal("fixture approval accepted changed ordinary write arguments")
							}
						}
					}
					mdl.verifyApprovals = func() {
						approvals.mu.Lock()
						consumed := len(approvals.used)
						approvals.mu.Unlock()
						if consumed != 2 {
							t.Fatalf("exact write approvals consumed: %d", consumed)
						}
						for path, content := range map[string]string{"outputs/report.md": fileFirstReport, "outputs/design.mmd": fileFirstSource} {
							raw, _ := json.Marshal(fileFirstWriteArgs(path, content))
							if _, err := approvals.AuthorizeAction(c, execution.ActionDescriptor{Tool: "write_file", Arguments: raw, Effect: execution.EffectUnknown}); err == nil {
								t.Fatal("consumed ordinary write approval replayed")
							}
						}
					}
				}
				pool := &fileFirstHTTPPool{root: filepath.Join(backend.documentsRoot, q.Bot.ID.String()), session: q.Run.SessionID.String()}
				return &launcher.BotChatSafeResources{Tools: ts, FileReceipts: rs, WorkspaceDir: "/retained", ChatConfig: &config.AppConfig{}, Catalog: &launcher.ChatCatalogResources{Pool: pool}, PolicyRuntime: agent.BotPolicyRuntime{Guard: approvals, Mandate: approvals, Classifier: launcher.BotCatalogueClassifier{}}, VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}}, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer worker.Stop()
			worker.schedulerReady.Store(true)
			v := store.DefaultBotVersion()
			v.Charter = "Create retained report and editable source files"
			v.Persona.Soul = "CRITICAL_FILE_INSTRUCTION"
			v.Budget.MaxTokens = nil
			b, err := repo.Create(auth, store.BotCreateRequest{Name: "File first", OwnerID: uuid.MustParse(owner), Version: v, IdempotencyKey: "draft"})
			if err != nil {
				t.Fatal(err)
			}
			b, err = worker.provisionHome(auth, repo, b.ID, b.Revision)
			if err != nil {
				t.Fatal(err)
			}
			if err = pin(auth, b.HomeSessionID.String(), "controlled", "model"); err != nil {
				t.Fatal(err)
			}
			svc := &store.Services{Bots: repo, Sessions: ss, BotMandatePolicy: policy, BotTurnRegistry: registry, BotInteractiveReady: true, BotFilesFactory: worker.filesForRequest}
			if scope == store.BotScopePersonal {
				svc.PersonalBots, svc.PersonalSessions = repo, ss
			}
			router := mux.NewRouter()
			router.HandleFunc("/bots/{botID}/messages", api.BotMessageHandler).Methods("POST")
			router.HandleFunc("/bots/{botID}/documents", api.BotDocumentsHandler).Methods("GET")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				router.ServeHTTP(w, r.WithContext(store.WithServices(auth, svc)))
			}))
			defer server.Close()
			turn := func(text string) store.RunID {
				t.Helper()
				body, _ := json.Marshal(map[string]any{"version": b.Version, "idempotency_key": uuid.NewString(), "instructions": text})
				response, e := server.Client().Post(server.URL+"/bots/"+b.ID.String()+"/messages?scope="+string(scope), "application/json", bytes.NewReader(body))
				if e != nil {
					t.Fatal(e)
				}
				raw, e := io.ReadAll(response.Body)
				response.Body.Close()
				if e != nil || response.StatusCode != 202 {
					t.Fatalf("admission %d %s %v", response.StatusCode, raw, e)
				}
				var receipt store.BotMessageReceipt
				if e = json.Unmarshal(raw, &receipt); e != nil {
					t.Fatal(e)
				}
				ownerCtx, _, e := worker.authenticateOwner(auth, BotSchedulerScope{"file-first", "team", owner})
				if e != nil {
					t.Fatal(e)
				}
				if e = worker.execute(ownerCtx, repo, ss, b.ID, receipt.Run.ID); e != nil {
					t.Fatal(e)
				}
				saved, e := repo.GetRun(auth, b.ID, receipt.Run.ID)
				if e != nil || saved.Status != store.RunSucceeded {
					t.Fatalf("run %+v %v", saved, e)
				}
				return receipt.Run.ID
			}
			firstRun := turn("Create a report and an editable diagram. Save and publish both files.")
			publication, err := repo.(store.BotPublicationStore).PublishReport(auth, store.BotPublicationRequest{BotID: b.ID, RunID: firstRun})
			if err != nil || publication == nil || len(publication.Artifacts) != 2 || publication.PublishedAt.IsZero() {
				t.Fatalf("committed file publication: %+v %v", publication, err)
			}
			for _, artifact := range publication.Artifacts {
				if artifact.ContentRef == nil || artifact.HomeID != b.Home.ID || artifact.ContentRef.SHA256 != artifact.SHA256 {
					t.Fatalf("publication missing immutable trusted reference: %+v", artifact)
				}
			}
			for p, want := range map[string]string{"outputs/report.md": fileFirstReport, "outputs/design.mmd": fileFirstSource} {
				data, e := os.ReadFile(filepath.Join(backend.documentsRoot, b.ID.String(), p))
				if e != nil || string(data) != want {
					t.Fatalf("saved %s: %q %v", p, data, e)
				}
			}
			if len(receipts.Snapshot()) != 2 {
				t.Fatalf("trusted publication receipts: %+v", receipts.Snapshot())
			}
			memoryPath := filepath.Join(backend.documentsRoot, b.ID.String(), "MEMORY.md")
			memoryBytes, e := os.ReadFile(memoryPath)
			if e != nil {
				t.Fatal(e)
			}
			for _, want := range []string{"Create a report and an editable diagram", "Committed report published", "outputs/report.md", "outputs/design.mmd", `"completed"`} {
				if !strings.Contains(string(memoryBytes), want) {
					t.Fatalf("memory missing %q: %s", want, memoryBytes)
				}
			}
			for _, artifact := range publication.Artifacts {
				if strings.Count(string(memoryBytes), artifact.ID.String()) != 1 {
					t.Fatalf("artifact identity missing or duplicated in memory: %s", memoryBytes)
				}
			}
			if !strings.Contains(string(memoryBytes), firstRun.String()) {
				t.Fatal("memory lost source run identity")
			}
			if strings.Contains(string(memoryBytes), fileFirstReport) {
				t.Fatal("report body copied to memory")
			}
			if e = worker.reconcilePublications(auth, repo, b.ID); e != nil {
				t.Fatal(e)
			}
			replayed, e := os.ReadFile(memoryPath)
			if e != nil || !bytes.Equal(memoryBytes, replayed) {
				t.Fatal("publication retry duplicated or rewrote memory", e)
			}
			// Reopening creates a fresh employee/model context over the same retained Home.
			mdl = &fileFirstHTTPModel{t: t, bridge: scope == store.BotScopeTeam, followup: true}
			turn("Read outputs/report.md to recover the earlier design.")
			if !mdl.retrieved {
				t.Fatal("follow-up did not explicitly retrieve retained bytes")
			}
			before := backend.calls
			response, err := server.Client().Get(server.URL + "/bots/" + b.ID.String() + "/documents?scope=" + string(scope))
			if err != nil {
				t.Fatal(err)
			}
			raw, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil || response.StatusCode != 200 || !strings.Contains(string(raw), "CRITICAL_FILE_INSTRUCTION") || backend.calls != before {
				t.Fatalf("documents GET: %d %s %v", response.StatusCode, raw, err)
			}
			t.Log("HTTP owner admission -> confined report/source writes -> trusted snapshots; fresh employee loads critical files and MEMORY.md, old report appears only after explicit read_file; GET does not provision")
		})
	}
}
