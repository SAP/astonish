package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/SAP/astonish/pkg/tools"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/toolconfirmation"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type mandateHTTPToolContext struct {
	tool.Context
	base    context.Context
	session string
}

func (c mandateHTTPToolContext) ToolConfirmation() *toolconfirmation.ToolConfirmation { return nil }
func (c mandateHTTPToolContext) Value(k any) any                                      { return c.base.Value(k) }
func (c mandateHTTPToolContext) Deadline() (time.Time, bool)                          { return c.base.Deadline() }
func (c mandateHTTPToolContext) Done() <-chan struct{}                                { return c.base.Done() }
func (c mandateHTTPToolContext) Err() error                                           { return c.base.Err() }
func (c mandateHTTPToolContext) SessionID() string                                    { return c.session }
func (c mandateHTTPToolContext) FunctionCallID() string                               { return "read-weather" }

// This acceptance uses actual HTTP admission and an actual HTTP retrieval after
// reconstructing the durable verifier, with no interactive registry available.
func TestBotMandateRuntimeHTTPAcceptance(t *testing.T) {
	t.Run("SQLiteRestartWorkerFactory", testMandateWorkerRestartHTTP)
	f := newInteractiveHTTPFixture(t, "web_fetch", nil, []string{"chat", "tool:execute"}, store.BotScopeTeam)
	calls := 0
	weather := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; fmt.Fprint(w, "weather: 21 C") }))
	defer weather.Close()
	origin, _ := tools.RoutineReadOrigin(weather.URL)
	allowed := true
	policy := func(context.Context, store.BotMandateRef) (store.BotMandatePolicy, error) {
		if !allowed {
			return store.BotMandatePolicy{}, store.ErrBotForbidden
		}
		return store.BotMandatePolicy{Scopes: []string{"chat", "tool:execute"}, Constraints: []execution.BotActionResourceConstraint{{Action: execution.CapabilityChat, Resources: []string{"task"}}, {Action: execution.CapabilityToolExecute, Resources: []string{"web_fetch", "http_request", origin}}}, Limits: execution.BotMandateLimits(config.DefaultBotMandateConfig()), TTL: time.Hour}, nil
	}
	principal, _ := execution.PrincipalFromContext(f.ctx)
	principal.Authentication = execution.AuthMethodPlatformJWT
	auth, err := execution.WithPrincipal(f.ctx, principal)
	if err != nil {
		t.Fatal(err)
	}
	auth, err = execution.WithAuthenticationEvidence(auth, execution.AuthenticationEvidence{Method: execution.AuthMethodPlatformJWT, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	svc := &store.Services{Bots: f.repo, BotMandatePolicy: policy}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.BotCreateHandler(w, r.WithContext(store.WithServices(auth, svc)))
	}))
	defer server.Close()
	response, err := http.Post(server.URL+"?scope=team", "application/json", strings.NewReader(`{"name":"Read weather now","idempotency_key":"mandate-weather","start_from_title":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	if response.StatusCode != 201 {
		t.Fatalf("admission %d %s", response.StatusCode, raw)
	}
	var receipt titleHTTPReceipt
	if err = json.Unmarshal(raw, &receipt); err != nil {
		t.Fatal(err)
	}
	run, err := f.repo.GetRun(auth, receipt.ID, receipt.InitialTurn.Run.ID)
	if err != nil || run.MandateRef == nil {
		t.Fatalf("missing durable pin %+v %v", run, err)
	}
	f.registry.Close()
	runtime, err := entstore.NewBotMandateRuntime(f.repo, policy)
	if err != nil {
		t.Fatal(err)
	}
	authorization, err := execution.VerifyBotMandateAuthorization(context.Background(), runtime, *run.MandateRef, execution.BotMandatePurposeTask)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = execution.VerifyBotMandateAuthorization(context.Background(), runtime, *run.MandateRef, execution.BotMandatePurposeSchedule); err == nil {
		t.Fatal("title granted recurrence")
	}
	identity := execution.BotRunIdentity{BotID: receipt.ID, RunID: run.ID, Version: run.Version, ActorID: principal.Subject, OrgSlug: principal.OrgSlug, TeamSlug: principal.TeamSlug}
	m := &bot.MandateExecution{Authorization: authorization, Runtime: runtime, Identity: identity}
	catalog, err := launcher.BotMandatedReadTools(m, tools.RoutineReadPolicy{Authorize: func(_ context.Context, o string) error {
		if o != origin {
			return store.ErrBotForbidden
		}
		return nil
	}, AllowIP: func(_ context.Context, _ string, ip net.IP) error {
		if !ip.IsLoopback() {
			return store.ErrBotForbidden
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	tc := mandateHTTPToolContext{base: execution.WithBotRunIdentity(context.Background(), identity), session: run.SessionID.String()}
	executable := catalog[0].(interface {
		Run(tool.Context, any) (map[string]any, error)
	})
	result, err := executable.Run(tc, map[string]any{"url": weather.URL})
	if err != nil || !strings.Contains(fmt.Sprint(result), "21 C") || calls != 1 {
		t.Fatalf("HTTP retrieval %+v %v calls=%d", result, err, calls)
	}
	for _, mode := range []string{"raw", "readable", "markdown"} {
		result, err = executable.Run(tc, map[string]any{"url": weather.URL, "mode": mode})
		if err != nil || !strings.Contains(fmt.Sprint(result), "21 C") {
			t.Fatalf("extraction %s: %+v %v", mode, result, err)
		}
	}
	before := calls
	if _, err = executable.Run(tc, map[string]any{"url": weather.URL, "mode": "invalid"}); err == nil || calls != before {
		t.Fatal("invalid extraction mode dispatched")
	}
	head := catalog[1].(interface {
		Run(tool.Context, any) (map[string]any, error)
	})
	if _, err = head.Run(tc, map[string]any{"url": weather.URL, "method": "HEAD"}); err != nil {
		t.Fatal(err)
	}
	before = calls
	if _, err = head.Run(tc, map[string]any{"url": weather.URL, "method": "POST"}); err == nil || calls != before {
		t.Fatal("non-read HTTP request dispatched")
	}
	allowed = false
	if _, err = executable.Run(tc, map[string]any{"url": weather.URL}); err == nil || calls != before {
		t.Fatalf("revoked policy dispatched %v calls=%d", err, calls)
	}
}
