package launcher

import (
	"context"
	"encoding/json"
	"iter"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/adk/model"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/chatruntime"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

// childCancellationModel reuses the authority script's initial list_drills call,
// then waits inside the actual child model iterator. The script's second call
// (which would request shell_command) is deliberately never dispatched. The exit
// gate lets the test distinguish cancellation notification from joined return.
type childCancellationModel struct {
	script    *childAuthorityModel
	inspect   func(context.Context, *model.LLMRequest)
	entered   chan struct{}
	cancelled chan struct{}
	allowExit chan struct{}
	exited    chan struct{}
	calls     atomic.Int32
}

func (*childCancellationModel) Name() string { return "offline-child-cancellation" }

func (m *childCancellationModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		switch m.calls.Add(1) {
		case 1:
			m.script.GenerateContent(ctx, req, stream)(yield)
		case 2:
			defer close(m.exited)
			m.inspect(ctx, req)
			close(m.entered)
			<-ctx.Done()
			close(m.cancelled)
			<-m.allowExit
			yield(nil, ctx.Err())
		default:
			m.script.t.Error("cancelled child invoked the model again")
			yield(nil, context.Canceled)
		}
	}
}

// This is local shared-constructor/manager acceptance, not Studio HTTP admission,
// scheduler admission, physical sandbox aliasing, or transport/process reaping.
// Ordinary team Chat is explicitly composed by newServicesParityRuntime; it does
// not claim that Studio accepts team-scoped ordinary Chat requests.
func TestChatRuntimeChildCancellationJoinedReturn(t *testing.T) {
	const bound = 5 * time.Second
	for _, scope := range []chatruntime.ResourceScope{chatruntime.ResourceScopePersonal, chatruntime.ResourceScopeTeam} {
		for _, mode := range []string{"ordinary-chat", "interactive-bot", "autonomous-bot"} {
			t.Run(string(scope)+"/"+mode, func(t *testing.T) {
				r := newServicesParityRuntime(t, scope, mode)
				shell := map[string]any{"command": "printf services-parity-ok", "working_dir": r.node.root}
				m := &childCancellationModel{
					script:  &childAuthorityModel{t: t, mode: mode, shell: shell},
					entered: make(chan struct{}), cancelled: make(chan struct{}),
					allowExit: make(chan struct{}), exited: make(chan struct{}),
				}
				if mode != "ordinary-chat" {
					r.f.cfg.WrapModel = func(context.Context, model.LLM, execution.RunBudget) (model.LLM, error) { return m, nil }
					childAuthorityRebuildBot(t, r, mode, 0)
				}
				parent, release, err := childAuthorityBinding(t, r).bind(r.f.ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer release()
				expectedPrincipal, ok := execution.PrincipalFromContext(parent)
				if !ok || !reflect.DeepEqual(expectedPrincipal, r.f.req.Principal) {
					t.Fatal("parent is not bound to the requested principal")
				}
				// Personal composition uses the production personal-first merged store;
				// team composition uses only its team store. Compare the child to the
				// bound parent and resolve the colliding fixture key to prove scope.
				expectedCredentials := store.CredentialStoreFromContext(parent)
				if expectedCredentials == nil {
					t.Fatal("missing scoped parent credentials")
				}
				m.inspect = func(ctx context.Context, req *model.LLMRequest) {
					principal, present := execution.PrincipalFromContext(ctx)
					if !present || !reflect.DeepEqual(principal, expectedPrincipal) {
						t.Error("child changed authenticated principal or tenant")
					}
					childCredentials := store.CredentialStoreFromContext(ctx)
					if childCredentials != expectedCredentials {
						t.Errorf("child did not retain %s scoped credentials", scope)
					}
					if childCredentials == nil {
						t.Error("child credentials missing")
					} else if credential := childCredentials.Get(ctx, "mail"); credential == nil || credential.Username != "owner" || credential.Password != productionTestSecret {
						t.Errorf("child selected foreign credentials in %s scope", scope)
					}
					if scope == chatruntime.ResourceScopeTeam && childCredentials != r.f.services.Credentials {
						t.Error("team child retained a personal credential overlay")
					}
					identity, hasIdentity := execution.BotRunIdentityFromContext(ctx)
					policy := execution.RunPolicyFromContext(ctx)
					_, interactive := execution.BotTurnAuthorizerFromContext(ctx)
					switch mode {
					case "ordinary-chat":
						if hasIdentity || policy != nil || interactive {
							t.Error("ordinary child acquired Bot authority")
						}
					case "interactive-bot":
						expectedIdentity, _ := execution.BotRunIdentityFromContext(parent)
						if !hasIdentity || identity != expectedIdentity || policy != nil || !interactive {
							t.Error("interactive child lost caller authority or acquired autonomous policy")
						}
					case "autonomous-bot":
						if !hasIdentity || identity != r.f.req.Policy.Identity() || policy == nil || policy == execution.RunPolicyFromContext(parent) || policy.Identity() != identity || interactive {
							t.Error("autonomous child did not receive its own narrowed policy")
						}
					}
					// Ensure cancellation happens after actual child tool processing,
					// not merely after session creation or before the first model call.
					var responses []string
					for _, content := range req.Contents {
						for _, part := range content.Parts {
							if part.FunctionResponse != nil {
								raw, _ := json.Marshal(part.FunctionResponse.Response)
								responses = append(responses, string(raw))
							}
						}
					}
					want := "first"
					if mode == "autonomous-bot" {
						// list_drills is consequential in the production classifier.
						want = "child consequential action prohibited"
					}
					if !strings.Contains(strings.Join(responses, "\n"), want) {
						t.Errorf("missing pre-cancellation child response %q: %v", want, responses)
					}
				}
				mgr := r.chat.SubAgentManager
				mgr.LLM = m
				var childID string
				// The test pool has no physical Alias implementation. No child node
				// dispatch is intended; observe the real session creation instead.
				mgr.OnChildSession = func(parentID, id string) {
					if parentID != r.node.session || id == "" || id == parentID {
						t.Error("invalid child session identity")
					}
					childID = id
				}
				ctx, cancel := context.WithCancel(parent)
				var exitOnce sync.Once
				allowExit := func() { exitOnce.Do(func() { close(m.allowExit) }) }
				returned := make(chan agent.TaskResult, 1)
				done := make(chan struct{})
				defer func() {
					cancel()
					allowExit()
					select {
					case <-done:
					case <-time.After(bound):
						t.Error("child manager did not join during test cleanup")
					}
				}()
				go func() {
					defer close(done)
					returned <- mgr.RunTask(ctx, agent.SubAgentTask{Name: "cancelled_child", Description: "List drills, then wait", ToolFilter: []string{"list_drills"}, ToolSelectionMode: agent.ChildToolSelectionStrict, ParentID: r.node.session})
				}()
				select {
				case <-m.entered:
				case result := <-returned:
					t.Fatalf("child returned before cancellation point: %+v", result)
				case <-time.After(bound):
					t.Fatal("child did not enter its blocking model call")
				}
				cancel()
				select {
				case <-m.cancelled:
				case <-time.After(bound):
					t.Fatal("child model did not observe cancellation")
				}
				select {
				case result := <-returned:
					t.Fatalf("manager returned before child iterator exited: %+v", result)
				case <-time.After(30 * time.Millisecond):
				}
				allowExit()
				var result agent.TaskResult
				select {
				case result = <-returned:
				case <-time.After(bound):
					t.Fatal("cancelled child did not return within bound")
				}
				select {
				case <-m.exited:
				default:
					t.Fatal("manager return did not join the child model iterator")
				}
				if result.Status != string(agent.TaskCancelled) || result.ChildSessionID != childID || childID == "" || result.Attempts != 1 || m.calls.Load() != 2 {
					t.Fatalf("unexpected cancellation result: %+v model calls=%d child=%q", result, m.calls.Load(), childID)
				}
				if len(r.node.calls) != 0 || len(r.guard.consumed) != 0 || len(r.reports.reports) != 0 {
					t.Fatal("cancelled child caused a node effect, consumed a parent grant, or published a report")
				}
				if parent.Err() != nil {
					t.Fatalf("child cancellation cancelled the parent: %v", parent.Err())
				}
				// Positive control after joined return: the real parent shell action
				// still executes and the autonomous exact-action grant is still fresh.
				if _, err := r.invoke(t, r.f.ctx, "shell_command", shell); err != nil {
					t.Fatal(err)
				}
				if len(r.node.calls) != 1 || r.node.outputs[0]["stdout"] != "services-parity-ok" || m.calls.Load() != 2 {
					t.Fatal("parent control failed or child performed late work")
				}
				wantGrants := 0
				if mode == "autonomous-bot" {
					wantGrants = 1
				}
				if len(r.guard.consumed) != wantGrants {
					t.Fatalf("parent consumed %d grants, want %d", len(r.guard.consumed), wantGrants)
				}
			})
		}
	}
}
