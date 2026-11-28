package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

const privateDiagnostic = "ARBITRARY_PROVIDER_BODY_94821 https://user:credential@private.example.test/request"

type publicDiagnosticCause struct{}

func (*publicDiagnosticCause) Error() string { return privateDiagnostic }

func TestBotFailurePublicDiagnostics(t *testing.T) {
	cause := &publicDiagnosticCause{}
	wrapped := fmt.Errorf("provider wrapper: %w", cause)
	for _, factory := range []bool{false, true} {
		var result bot.RunResult
		var err error
		if factory {
			result, err = botChatFactoryFailure(context.Background(), wrapped)
		} else {
			result, err = botChatFailure(bot.ErrorPermanent, wrapped)
		}
		var original *publicDiagnosticCause
		if !errors.Is(err, cause) || !errors.As(err, &original) || original != cause {
			t.Fatal("lost diagnostic cause")
		}
		raw, _ := json.Marshal(result.Report)
		if strings.Contains(string(raw), privateDiagnostic) || strings.Contains(err.Error(), privateDiagnostic) {
			t.Fatalf("public diagnostic leaked: %s %v", raw, err)
		}
	}
	result, err := botChatFactoryFailure(context.Background(), fmt.Errorf("%s: %w", privateDiagnostic, context.DeadlineExceeded))
	if !errors.Is(err, context.DeadlineExceeded) || result.Status != store.RunFailed || strings.Contains(result.Report.Failure.TechnicalDetails, privateDiagnostic) {
		t.Fatal("unsafe setup timeout", result, err)
	}
	uncertain := &bot.RunError{Class: bot.ErrorPermanent, Message: privateDiagnostic, Cause: cause, UncertainEffect: true, TerminalForAttempt: true}
	result, err = botChatFailure(bot.ErrorPermanent, uncertain)
	if result.Status != store.RunPartial || !result.Failure.UncertainEffect || !errors.Is(err, cause) || strings.Contains(err.Error(), privateDiagnostic) {
		t.Fatal("uncertain failure boundary", result, err)
	}
}

func TestBotPublicDiagnosticsHTTP(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f := newSkillStorageHTTPFixture(t)
			b := f.bot(scope)
			repo, ctx := f.repo(scope), f.context(f.owner)
			requestBody := fmt.Sprintf(`{"version":%d,"instructions":"Diagnostic boundary","idempotency_key":%q}`, b.Version, uuid.NewString())
			runValue := skillDecode[store.BotRun](t, f.request("POST", skillPath(b, "/runs"), f.owner, requestBody, 202))
			run := &runValue
			f.pinLegacyAttempt(b, run)
			worker := execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: b.ID, RunID: run.ID, Version: b.Version, ActorID: f.owner, OrgSlug: f.org.Slug, TeamSlug: f.team.Slug})
			lease, err := repo.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: run.ID, WorkerID: "diagnostics", ExpiresAt: time.Now().Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			// Historical reports deliberately contain arbitrary raw bytes: persistence
			// and its integrity digests must remain unchanged by public projection.
			report := store.BotReport{Summary: "I couldn't complete the requested work.", Failure: &store.BotReportFailure{Kind: store.BotReportFailureOperational, TechnicalDetails: privateDiagnostic}}
			message := bot.EmployeeMessageFallback(store.RunFailed, report, nil)
			report.EmployeeMessage = &message
			commit := store.BotRunCommit{Lease: lease, Status: store.RunFailed, Report: report}
			if err := repo.CommitRun(worker, commit); err != nil {
				t.Fatal(err)
			}
			if err := repo.CommitRun(worker, commit); err != nil {
				t.Fatal("commit replay", err)
			}
			for i := 0; i < 2; i++ {
				if i == 1 {
					f.reopen()
					repo = f.repo(scope)
				}
				for _, suffix := range []string{"/runs/" + run.ID.String(), "/runs", "/activity", "/snapshot"} {
					raw := f.request("GET", skillPath(b, suffix), f.owner, "", 200)
					if strings.Contains(string(raw), "technical_details") || strings.Contains(string(raw), privateDiagnostic) {
						t.Fatalf("%s leaked diagnostics: %s", suffix, raw)
					}
				}
				replayed := f.request("POST", skillPath(b, "/runs"), f.owner, requestBody, 202)
				if strings.Contains(string(replayed), "technical_details") || strings.Contains(string(replayed), privateDiagnostic) {
					t.Fatalf("replayed run leaked: %s", replayed)
				}
				if got := skillDecode[store.BotRun](t, replayed); got.ID != run.ID || got.Status != store.RunFailed {
					t.Fatal("not the committed replay", got)
				}
				persisted, err := repo.GetRun(ctx, b.ID, run.ID)
				if err != nil || persisted.Report.Failure.TechnicalDetails != privateDiagnostic {
					t.Fatal("projection mutated source", persisted, err)
				}
				if persisted.Report.ValidateEmployeeMessageIntegrity(persisted.Status) != nil {
					t.Fatal("projection changed source digest")
				}
			}
			f.request("GET", skillPath(b, "/runs/"+run.ID.String()), "", "", 401)
			if scope == store.BotScopePersonal {
				f.request("GET", skillPath(b, "/runs/"+run.ID.String()), f.member, "", 404)
			}
		})
	}
	t.Run("json-boundaries", func(t *testing.T) {
		for _, input := range []any{nil, []any{}, json.RawMessage(`{"sequence":9007199254740993,"nested":[{"technical_details":"secret","ok":null}],"technical_details":"private"}`)} {
			w := httptest.NewRecorder()
			botWriteJSON(w, 200, input)
			if w.Code != 200 || strings.Contains(w.Body.String(), "technical_details") {
				t.Fatal(w.Code, w.Body.String())
			}
			if _, ok := input.(json.RawMessage); ok && !strings.Contains(w.Body.String(), "9007199254740993") {
				t.Fatal("integer precision lost")
			}
		}
		for _, input := range []any{make(chan int), json.RawMessage(`{broken`)} {
			w := httptest.NewRecorder()
			botWriteJSON(w, 200, input)
			if w.Code != 500 || strings.Contains(w.Body.String(), "broken") {
				t.Fatal("marshal failure not closed", w.Code, w.Body.String())
			}
		}
	})
}
