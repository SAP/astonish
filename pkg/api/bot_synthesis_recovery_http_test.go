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
	"testing"
	"time"

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
				// A genuinely small, owner-reviewed run budget enters synthesis
				// after the first real tool read, without sleeps or a fake clock.
				limit := int64(2000)
				version.Budget.MaxTokens = &limit
				body, err := json.Marshal(store.BotConfigurationRequest{ExpectedRevision: f.b.Revision, ExpectedVersion: f.b.Version, Reviewed: true, Reason: "Isolated synthesis recovery HTTP acceptance", Autonomy: version.Autonomy, Budget: version.Budget, Delivery: version.Delivery})
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(f.request("PUT", f.path+"/configuration", string(body), 200), &f.b); err != nil {
					t.Fatal(err)
				}
				workerConfig := f.workerConfig
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
					files := &composedHTTPFiles{disk: &synthesisHTTPFiles{root: f.dataDir}, repo: f.repo, id: f.b.ID, scope: f.scope}
					files.install(ctx, req, scope)
					return resources, nil
				}
				worker, err := daemon.NewBotWorker(workerConfig)
				if err != nil {
					t.Fatal(err)
				}
				defer worker.Stop()
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
						} else if usage := meter.Usage(); usage.InputTokens != 110 || usage.OutputTokens != 6+int64(ceiling) || usage.ToolCalls != 1 {
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
					if run.Status == store.RunSucceeded || run.Status == store.RunRunning || !strings.Contains(string(runJSON), "reason=missing_terminal") {
						t.Fatalf("missing-terminal did not fail closed with evidence: %s", runJSON)
					}
				} else if run.Status != store.RunSucceeded || run.Report.Summary != final {
					t.Fatalf("recovery failed: status=%s report=%+v", run.Status, run.Report)
				}
				// This is an ordinary read-only answer: synthesis recovery must not
				// add a second review workflow or repeat its already completed read.
				if calls != wantCalls || preparations != 1 || f.model.reviews != 0 || f.pool.calls != 1 || run.Usage.ToolCalls != 1 || run.Usage.InputTokens != int64(wantCalls*5) || run.Usage.OutputTokens != int64(wantCalls*3) {
					t.Fatalf("extra work or lost cumulative usage: calls=%d preparations=%d reviews=%d reads=%d usage=%+v", calls, preparations, f.model.reviews, f.pool.calls, run.Usage)
				}
				marker := "bot-report:" + run.ID.String()
				var liveText strings.Builder
				reader := bufio.NewReader(live.Body)
				for {
					frame := readBlockedPublicationFrame(t, reader)
					fmt.Fprintf(&liveText, "id: %s\nevent: %s\ndata: %s\n\n", frame.id, frame.kind, frame.data)
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
				assertPublication := func(label, text string) {
					t.Helper()
					if strings.Contains(text, draft) || strings.Contains(text, thought) {
						t.Fatalf("%s leaked unaccepted model output: %s", label, text)
					}
					wantPublication := final
					if missingTerminal {
						wantPublication = "I couldn't complete this request during execution. No publishable final answer is available from this run. The run recorded some observations; this does not confirm that all requested actions completed. You can revise the request or start a new run; no retry has been started."
					}
					assertCanonicalHTTPReport(t, text, marker, wantPublication)
					if !missingTerminal && canonicalHTTPTextCount(t, text, final) != 1 {
						t.Fatalf("%s missing exactly one complete answer: %s", label, text)
					}
					if missingTerminal && strings.Contains(text, final) {
						t.Fatalf("%s published an answer after missing-terminal failure", label)
					}
				}
				assertPublication("live", liveText.String())
				for range 2 {
					assertPublication("restored", string(f.request("GET", home+"/stream", "", 200)))
					if err := worker.Poll(f.ctx); err != nil {
						t.Fatal(err)
					}
				}
				t.Logf("scope=%s missing_terminal=%t status=%s model_calls=%d preparations=%d reads=%d input_tokens=%d output_tokens=%d live_final=1 restored_final=1 draft_leaks=0", scope, missingTerminal, run.Status, calls, preparations, f.pool.calls, run.Usage.InputTokens, run.Usage.OutputTokens)
			})
		}
	}
}
