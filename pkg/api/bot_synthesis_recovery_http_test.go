package api_test

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/daemon"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

type synthesisHTTPFiles struct {
	root string
}

func (f *synthesisHTTPFiles) Open(ctx context.Context, _ store.BotID, p string, r store.BotFileRange) (*store.BotFileStream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p != "receipt.txt" && filepath.Base(p) != p {
		return nil, store.ErrBotForbidden
	}
	data, err := os.ReadFile(filepath.Join(f.root, p))
	if err != nil {
		return nil, err
	}
	h, err := os.Open(filepath.Join(f.root, p))
	if err != nil {
		return nil, err
	}
	if _, err = h.Seek(r.Offset, io.SeekStart); err != nil {
		h.Close()
		return nil, err
	}
	return &store.BotFileStream{ReadCloser: h, Entry: store.BotFileEntry{Path: p, Kind: store.BotFileRegular, SizeBytes: int64(len(data)), SHA256: fmt.Sprintf("%x", sha256.Sum256(data)), Preview: store.BotFilePreviewText}}, nil
}

// The publication memory index uses the backend directly after run cleanup.
// Exercise its actual disk IO and commit fence instead of a nil embedded method.
type synthesisHTTPBackend struct {
	journeyHomeBackend
	files *synthesisHTTPFiles
}

var _ sandbox.BotHomeFilesBackend = synthesisHTTPBackend{}

// This fixture exercises document reads and fenced memory updates only. Other
// file capabilities must fail explicitly, not dispatch through a nil interface.
func (synthesisHTTPBackend) ListBotHomeFiles(context.Context, *sandbox.BotHomeBinding, string, string, int) (*store.BotFilePage, error) {
	return nil, store.ErrBotUnsupported
}
func (synthesisHTTPBackend) StatBotHomeFile(context.Context, *sandbox.BotHomeBinding, string) (*store.BotFileEntry, error) {
	return nil, store.ErrBotUnsupported
}
func (synthesisHTTPBackend) PutBotContent(context.Context, *sandbox.BotHomeBinding, store.BotContentKind, int64, io.Reader, int64, func(func() error) error) (*store.BotContentRef, error) {
	return nil, store.ErrBotUnsupported
}
func (synthesisHTTPBackend) OpenBotContent(context.Context, *sandbox.BotHomeBinding, store.BotContentRef, store.BotFileRange) (*store.BotFileStream, error) {
	return nil, store.ErrBotUnsupported
}
func (synthesisHTTPBackend) PublishBotArtifactSnapshot(context.Context, *sandbox.BotHomeBinding, string, string, func(func() error) error) (*store.BotContentRef, error) {
	return nil, store.ErrBotUnsupported
}

func (b synthesisHTTPBackend) OpenBotHomeFile(ctx context.Context, _ *sandbox.BotHomeBinding, p string, r store.BotFileRange) (*store.BotFileStream, error) {
	return b.files.Open(ctx, store.BotID{}, p, r)
}
func (b synthesisHTTPBackend) WriteBotHomeFile(ctx context.Context, _ *sandbox.BotHomeBinding, w store.BotFileWrite) (*store.BotFileEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if filepath.Base(w.Path) != w.Path || w.BeforeCommit == nil {
		return nil, store.ErrBotForbidden
	}
	data, err := io.ReadAll(w.Body)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != w.SizeBytes {
		return nil, store.ErrBotInvalid
	}
	p := filepath.Join(b.files.root, w.Path)
	err = w.BeforeCommit(func() error {
		previous, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if w.CreateOnly || fmt.Sprintf("%x", sha256.Sum256(previous)) != w.ExpectedSHA256 {
			return store.ErrBotConflict
		}
		return os.WriteFile(p, data, 0600)
	})
	if err != nil {
		return nil, err
	}
	return &store.BotFileEntry{Path: w.Path, Kind: store.BotFileRegular, SizeBytes: int64(len(data)), SHA256: fmt.Sprintf("%x", sha256.Sum256(data)), Preview: store.BotFilePreviewText}, nil
}

// This is running HTTP acceptance, not a direct synthesis-wrapper test. The
// production worker/factory owns admission, metering, reserve transitions and
// publication; only the model and isolated tool-node transport are controlled.
func TestBotSynthesisRecoveryHTTP(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, missingTerminal := range []bool{false, true} {
			name := string(scope) + "/output_limit"
			if missingTerminal {
				name = string(scope) + "/missing_terminal"
			}
			t.Run(name, func(t *testing.T) {
				f := newInteractiveHTTPFixture(t, "read_file", map[string]any{"path": "receipt.txt"}, []string{"chat", "tool:execute"}, scope)
				f.worker.Stop()
				for _, p := range []string{"SOUL.md", "IDENTITY.md", "USER.md", "CHARTER.md", "GOALS.md", "MEMORY.md"} {
					if err := os.WriteFile(filepath.Join(f.dataDir, p), []byte("# Bot\nUse scoped evidence and answer honestly.\n"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				version, err := f.repo.GetVersion(f.ctx, f.b.ID, f.b.Version)
				if err != nil {
					t.Fatal(err)
				}
				pauseBody, _ := json.Marshal(map[string]any{"expected_revision": f.b.Revision, "reason": "Review synthesis budget"})
				if err := json.Unmarshal(f.request("POST", f.path+"/pause", string(pauseBody), 200), &f.b); err != nil {
					t.Fatal(err)
				}
				// Synthesis is tied to live authority, never legacy token metadata.
				var synthesisClock atomic.Int64
				var synthesisDeadline time.Time
				body, err := json.Marshal(store.BotConfigurationRequest{ExpectedRevision: f.b.Revision, ExpectedVersion: f.b.Version, Reviewed: true, Reason: "Isolated synthesis recovery HTTP acceptance", Autonomy: version.Autonomy, Budget: version.Budget, Delivery: version.Delivery})
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(f.request("PUT", f.path+"/configuration", string(body), 200), &f.b); err != nil {
					t.Fatal(err)
				}
				workerConfig := f.workerConfig
				workerConfig.ResolveModel = func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
					return progressEmployeeFixture(progressMeteredModel(f.model)), nil
				}
				workerConfig.Sandbox = synthesisHTTPBackend{files: &synthesisHTTPFiles{root: f.dataDir}}
				// Deterministic model-scoped accounting, not a second allowance.
				workerConfig.ModelOptions.EstimateInput = func(context.Context, *model.LLMRequest) (int64, error) { return 100, nil }
				var meter interface{ Usage() store.BotUsage }
				prepare := workerConfig.Prepare
				preparations := 0
				workerConfig.Prepare = func(ctx context.Context, req bot.RunRequest, scope *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
					preparations++
					meter, _ = req.Budget.(interface{ Usage() store.BotUsage })
					resources, err := prepare(ctx, req, scope)
					if err != nil {
						return resources, err
					}
					files := &composedHTTPFiles{disk: &synthesisHTTPFiles{root: f.dataDir}, repo: f.repo, id: f.b.ID, scope: f.scope, fetch: func(name string, args map[string]interface{}) (json.RawMessage, error) {
						return f.pool.Call("", name, args)
					}}
					files.install(ctx, req, scope)
					synthesisDeadline = req.Run.CallerCeiling.ExpiresAt
					synthesisClock.Store(synthesisDeadline.Add(-time.Hour).UnixNano())
					resources.SynthesisNow = func() time.Time { return time.Unix(0, synthesisClock.Load()) }
					return resources, nil
				}
				worker, err := daemon.NewBotWorker(workerConfig)
				if err != nil {
					t.Fatal(err)
				}
				defer worker.Stop()
				resumeBody, _ := json.Marshal(map[string]any{"expected_revision": f.b.Revision, "reason": "Run reviewed synthesis budget"})
				if err := json.Unmarshal(f.request("POST", f.path+"/resume", string(resumeBody), 200), &f.b); err != nil {
					t.Fatal(err)
				}
				const draft = "UNPUBLISHED_TRUNCATED_DRAFT: the receipt indicates"
				const thought = "PRIVATE_SYNTHESIS_REASONING"
				const final = "The source says isolated receipt."
				calls := 0
				var ceiling int32
				f.model.assessment = func(req *model.LLMRequest) (*model.LLMResponse, bool) {
					calls++
					response := &model.LLMResponse{Content: genai.NewContentFromText(final, "model"), FinishReason: genai.FinishReasonStop, TurnComplete: true, UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 5, CandidatesTokenCount: 3, TotalTokenCount: 8}}
					if calls == 1 {
						response.Content.Parts = []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "recovery-receipt", Name: "read_file", Args: map[string]any{"path": "receipt.txt"}}}}
						f.pool.callOverride = func(name string, args map[string]interface{}) (json.RawMessage, error) {
							f.pool.callOverride = nil
							result, err := f.pool.Call("", name, args)
							f.pool.calls--
							synthesisClock.Store(synthesisDeadline.UnixNano())
							return result, err
						}
						return response, true
					}
					raw, _ := json.Marshal(req.Contents)
					if !strings.Contains(string(raw), "Synthesis reserve has begun") || !strings.Contains(string(raw), "isolated receipt") {
						t.Errorf("synthesis did not retain real tool evidence: %s", raw)
					}
					if len(req.Tools) != 0 || req.Config == nil || len(req.Config.Tools) != 0 || req.Config.ToolConfig != nil || req.Config.MaxOutputTokens <= 0 {
						t.Error("synthesis/correction retained tools or lacked an output ceiling")
					}
					switch calls {
					case 2:
						ceiling = req.Config.MaxOutputTokens
						response.Content.Parts = []*genai.Part{{Text: thought, Thought: true}, {Text: draft}}
						response.TurnComplete = false
						response.FinishReason = genai.FinishReasonMaxTokens
						if missingTerminal {
							response.FinishReason = ""
						}
					case 3:
						if missingTerminal {
							t.Error("missing terminal metadata incorrectly authorized a retry")
						}
						if req.Config.MaxOutputTokens != ceiling || !strings.Contains(string(raw), draft) || strings.Contains(string(raw), thought) {
							t.Error("correction changed ceiling, lost visible draft, or retained private reasoning")
						}
						correction := req.Contents[len(req.Contents)-1].Parts[0].Text
						if !strings.Contains(correction, "complete") || !strings.Contains(correction, "concise") {
							t.Error("correction is not a complete concise rewrite")
						}
						if meter == nil {
							t.Error("production run budget missing")
						} else if usage := meter.Usage(); usage.InputTokens != 115 || usage.OutputTokens != 9+int64(ceiling) || usage.ToolCalls != 1 {
							// Usage includes this correction's active 100-token input
							// and ceiling-sized output reservation, plus settled 10/6.
							t.Errorf("original budget lost prior settlement or correction reservation: %+v", usage)
						}
					default:
						t.Errorf("unexpected model/review/recovery call %d", calls)
					}
					return response, true
				}

				// Attach before admission: this checks live delivery, not just two
				// reads of restored history. The bounded context owns the SSE wait.
				streamCtx, cancelStream := context.WithTimeout(f.ctx, 20*time.Second)
				defer cancelStream()
				home := f.path + "/sessions/" + f.b.HomeSessionID.String()
				request, err := http.NewRequestWithContext(streamCtx, http.MethodGet, f.server.URL+home+"/events?scope="+string(scope), nil)
				if err != nil {
					t.Fatal(err)
				}
				live, err := f.server.Client().Do(request)
				if err != nil {
					t.Fatal(err)
				}
				defer live.Body.Close()
				if live.StatusCode != http.StatusOK {
					t.Fatalf("live stream status=%d", live.StatusCode)
				}
				var admitted store.BotMessageReceipt
				if err := json.Unmarshal(f.request("POST", f.path+"/messages", fmt.Sprintf(`{"version":%d,"instructions":"Read /isolated/receipt.txt and report its contents.","idempotency_key":"synthesis-recovery"}`, f.b.Version), 202), &admitted); err != nil {
					t.Fatal(err)
				}
				if err := worker.Poll(f.ctx); err != nil {
					t.Fatal(err)
				}
				var run store.BotRun
				runJSON := f.request("GET", f.path+"/runs/"+admitted.Run.ID.String(), "", 200)
				if err := json.Unmarshal(runJSON, &run); err != nil {
					t.Fatal(err)
				}
				wantCalls := 3
				if missingTerminal {
					wantCalls = 2
					org, readErr := f.db.ForOrg("interactive")
					if readErr != nil {
						t.Fatal(readErr)
					}
					ss := org.ForUser(f.b.OwnerID.String()).Sessions()
					if scope == store.BotScopeTeam {
						ss = org.ForTeam("team").Sessions()
					}
					privateEvents, readErr := ss.ReadTranscriptEvents(f.ctx, "astonish", f.b.OwnerID.String(), admitted.Run.SessionID.String())
					if readErr != nil {
						t.Fatal(readErr)
					}
					privateJSON, _ := json.Marshal(privateEvents)
					if run.Status == store.RunSucceeded || run.Status == store.RunRunning || !strings.Contains(string(privateJSON), "reason=missing_terminal") {
						t.Fatalf("missing-terminal did not fail closed with evidence: %s", runJSON)
					}
				} else if run.Status != store.RunSucceeded || run.Report.Summary != final {
					t.Fatalf("recovery failed: status=%s report=%+v", run.Status, run.Report)
				}
				// This is an ordinary read-only answer: synthesis recovery must not
				// add a second review workflow or repeat its already completed read.
				if calls != wantCalls || preparations != 1 || f.model.reviews != 0 || f.pool.calls != 1 || run.Usage.ToolCalls != 1 || run.Usage.InputTokens != int64((wantCalls+2)*5) || run.Usage.OutputTokens != int64((wantCalls+2)*3) {
					t.Fatalf("extra work or lost cumulative usage: calls=%d preparations=%d reviews=%d reads=%d usage=%+v", calls, preparations, f.model.reviews, f.pool.calls, run.Usage)
				}
				marker := "bot-report:" + run.ID.String()
				var liveText strings.Builder
				reader := bufio.NewReader(live.Body)
				for {
					frame := readBlockedPublicationFrame(t, reader)
					fmt.Fprintf(&liveText, "id: %s\nevent: %s\ndata: %s\n\n", frame.id, frame.kind, frame.data)
					if missingTerminal && frame.kind == store.BotRunStateKey {
						var state store.BotRunState
						if err := json.Unmarshal([]byte(frame.data), &state); err != nil {
							t.Fatal(err)
						}
						if state.RunID == run.ID && state.TerminalReason == store.BotTerminalNoAnswer {
							break
						}
					}
					if frame.kind != "text" {
						continue
					}
					var text canonicalHTTPText
					if err := json.Unmarshal([]byte(frame.data), &text); err != nil {
						t.Fatal(err)
					}
					if text.InvocationID == marker {
						break
					}
				}
				cancelStream()
				var liveNeutral store.BotRunState
				assertPublication := func(label, text string) {
					t.Helper()
					if strings.Contains(text, draft) || strings.Contains(text, thought) {
						t.Fatalf("%s leaked unaccepted model output: %s", label, text)
					}
					if !missingTerminal {
						assertCanonicalHTTPReport(t, text, marker, final)
						if canonicalHTTPTextCount(t, text, final) != 1 {
							t.Fatalf("%s missing exactly one complete answer: %s", label, text)
						}
						return
					}
					// Verify the canonical committed receipt before expecting no speech.
					publication, err := f.repo.(store.BotPublicationStore).PublishReport(f.ctx, store.BotPublicationRequest{BotID: f.b.ID, RunID: run.ID})
					if err != nil || publication == nil {
						t.Fatalf("%s missing durable publication receipt: %+v %v", label, publication, err)
					}
					if publication.EmployeeSpeech || publication.Text != "" || publication.BotID != f.b.ID || publication.RunID != run.ID || publication.SourceSessionID != run.SessionID || publication.SourceUserEventID != run.UserEventID || publication.HomeSessionID != f.b.HomeSessionID || publication.ReportSequence <= 0 {
						t.Fatalf("%s neutral publication lost source binding: %+v", label, publication)
					}
					restored, err := f.repo.GetRun(f.ctx, f.b.ID, run.ID)
					if err != nil || restored == nil || restored.TerminalReason != store.BotTerminalNoAnswer || restored.Status != run.Status {
						t.Fatalf("%s missing durable neutral terminal: %+v %v", label, restored, err)
					}
					if canonicalHTTPReportCount(t, text, marker) != 0 || strings.Contains(text, final) {
						t.Fatalf("%s manufactured an answer after missing-terminal failure", label)
					}
					neutral := 0
					frames := bufio.NewReader(strings.NewReader(text))
					for {
						if _, err := frames.Peek(1); err == io.EOF {
							break
						}
						frame := readBlockedPublicationFrame(t, frames)
						if frame.kind != store.BotRunStateKey {
							continue
						}
						var state store.BotRunState
						if err := json.Unmarshal([]byte(frame.data), &state); err != nil {
							t.Fatal(err)
						}
						if state.RunID == run.ID && state.TerminalReason == store.BotTerminalNoAnswer {
							if state.BotID != f.b.ID || state.SessionID != run.SessionID || state.HomeSessionID != f.b.HomeSessionID || state.Status != run.Status || state.StateSequence <= 0 || state.NeutralTerminalLabel() == "" {
								t.Fatalf("%s neutral state lost source binding: %+v", label, state)
							}
							if label == "live" {
								liveNeutral = state
							} else if state.StateSequence != liveNeutral.StateSequence || state.TerminalReason != liveNeutral.TerminalReason {
								t.Fatalf("restored unavailable state differs from live: live=%+v restored=%+v", liveNeutral, state)
							}
							neutral++
						}
					}
					if neutral != 1 {
						t.Fatalf("%s missing exactly one unavailable state: %d", label, neutral)
					}
				}
				assertPublication("live", liveText.String())
				for range 2 {
					assertPublication("restored", string(f.request("GET", home+"/stream", "", 200)))
					if err := worker.Poll(f.ctx); err != nil {
						t.Fatal(err)
					}
				}
				if calls != wantCalls || preparations != 1 || f.pool.calls != 1 {
					t.Fatalf("restore/publication replayed work: calls=%d preparations=%d reads=%d", calls, preparations, f.pool.calls)
				}
				t.Logf("scope=%s missing_terminal=%t status=%s model_calls=%d preparations=%d reads=%d input_tokens=%d output_tokens=%d live_restore_parity=true draft_leaks=0", scope, missingTerminal, run.Status, calls, preparations, f.pool.calls, run.Usage.InputTokens, run.Usage.OutputTokens)
			})
		}
	}
}
