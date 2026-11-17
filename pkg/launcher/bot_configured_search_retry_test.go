package launcher

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
)

func TestBotConfiguredSearchInitializationRetry(t *testing.T) {
	for _, mode := range []string{"progress", "timeout", "reset", "eof", "exhausted", "auth", "configuration", "dns-not-found", "tls", "canceled", "cancel-backoff", "deadline-backoff", "budget-expired", "budget-deadline", "revoked", "route-changed", "generation-timeout"} {
		t.Run(mode, func(t *testing.T) {
			f, runtime, episode := researchFixture(t)
			parent, cancel := context.WithCancel(f.ctx)
			defer cancel()
			deadline := time.Now().Add(5 * time.Second)
			if mode == "deadline-backoff" {
				deadline = time.Now().Add(50 * time.Millisecond)
			}
			var deadlineCancel context.CancelFunc
			f.ctx, deadlineCancel = context.WithDeadline(parent, deadline)
			defer deadlineCancel()
			timeoutErr := &url.Error{Op: "Get", URL: "https://team-secret/deployments", Err: context.DeadlineExceeded}
			firstErr := error(timeoutErr)
			switch mode {
			case "reset":
				firstErr = fmt.Errorf("initialize: %w", syscall.ECONNRESET)
			case "eof":
				firstErr = fmt.Errorf("initialize: %w", io.EOF)
			case "auth":
				firstErr = errors.New("HTTP 401 team-secret")
			case "configuration":
				firstErr = errors.New("missing model team-secret")
			case "tls":
				firstErr = &url.Error{Op: "Get", URL: "https://team-secret", Err: x509.UnknownAuthorityError{}}
			case "dns-not-found":
				firstErr = &net.DNSError{Err: "no such host", Name: "team-secret", IsNotFound: true}
			}
			searchModel := &knowledgeModel{text: "Sources: https://example.com/decisive"}
			if mode == "generation-timeout" {
				searchModel.err = timeoutErr
			}
			if mode == "budget-expired" {
				b, err := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 1}, time.Now().Add(-2*time.Second))
				if err != nil {
					t.Fatal(err)
				}
				f.req.Budget = b
			}
			if mode == "budget-deadline" {
				b, err := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 1}, time.Now().Add(-950*time.Millisecond))
				if err != nil {
					t.Fatal(err)
				}
				f.req.Budget = b
				deadline = b.Deadline()
			}
			if mode == "progress" {
				b, err := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds:300, RuntimePolicy:execution.NewBotProgressRuntimePolicy()},time.Now().Add(-6*time.Hour))
				if err != nil { t.Fatal(err) }; f.req.Budget=b
			}
			calls := 0
			factory := func(ctx context.Context, p, m string, cfg *config.AppConfig) (model.LLM, error) {
				calls++
				if !provider.IsConfinedProviderSecrets(ctx) {
					t.Fatal("unconfined initialization")
				}
				if got, ok := ctx.Deadline(); !ok || !got.Equal(deadline) {
					t.Fatalf("initialization changed parent deadline: %v", got)
				}
				if calls == 1 && mode != "generation-timeout" {
					switch mode {
					case "canceled":
						cancel()
					case "cancel-backoff":
						time.AfterFunc(20*time.Millisecond, cancel)
					case "revoked":
						runtime.revoked = true
					case "route-changed":
						f.services.Settings = botKnowledgeTeamSettings{row: &store.TeamSettings{Providers: map[string]store.ProviderConfig{"search": {"api_key": "changed"}}}}
					}
					return nil, firstErr
				}
				if mode == "exhausted" {
					return nil, firstErr
				}
				return searchModel, nil
			}
			search, err := BotConfiguredResearchTool(f.ctx, f.req, knowledgeScope(f), factory, nil, episode)
			if err != nil {
				t.Fatal(err)
			}
			_, err = search.(botRunnableTool).Run(optionalKnowledgeContext{knowledgeCtx(f)}, map[string]any{"facet": "overview"})
			success := mode == "progress" || mode == "timeout" || mode == "reset" || mode == "eof"
			wantCalls := 1
			if success || mode == "exhausted" {
				wantCalls = 2
			}
			if mode == "budget-expired" {
				wantCalls = 0
			}
			if calls != wantCalls {
				t.Fatalf("initializations=%d want=%d; err=%v", calls, wantCalls, err)
			}
			if runtime.calls != 1 {
				t.Fatalf("retry reserved extra search quota: %d", runtime.calls)
			}
			if success {
				if err != nil || searchModel.calls != 1 || episode.State() != BotResearchSearched {
					t.Fatalf("recovery failed: %v calls=%d state=%v", err, searchModel.calls, episode.State())
				}
				if episode.AllowDetail(f.ctx, f.req.Run.ID, f.req.Run.SessionID.String(), "https://example.com/decisive") != nil {
					t.Fatal("recovery did not unlock detail")
				}
				usage := f.req.Budget.(*bot.SharedRunBudget).Usage()
				if usage.InputTokens != 3 || usage.OutputTokens != 5 {
					t.Fatalf("wrong usage: %+v", usage)
				}
			} else {
				if err == nil || strings.Contains(err.Error(), "team-secret") {
					t.Fatalf("missing/redaction failure: %v", err)
				}
				if mode == "exhausted" && !strings.Contains(err.Error(), "context deadline exceeded") {
					t.Fatalf("lost concrete timeout: %v", err)
				}
				wantGeneration := 0
				if mode == "generation-timeout" {
					wantGeneration = 1
				}
				if searchModel.calls != wantGeneration || episode.State() == BotResearchSearched {
					t.Fatalf("failed search replayed generation or unlocked discovery: %d %v", searchModel.calls, episode.State())
				}
				if episode.AllowDetail(f.ctx, f.req.Run.ID, f.req.Run.SessionID.String(), "https://example.com/decisive") == nil {
					t.Fatal("failed search unlocked detail")
				}
			}
		})
	}
}
