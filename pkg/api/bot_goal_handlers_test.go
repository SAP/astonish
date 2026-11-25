package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

type correctionScopedMemory struct{ store.MemoryStore }
type correctionAPIRepo struct {
	store.BotStore
	bot          store.Bot
	memory       store.MemoryStore
	providerErr  error
	reconcileErr error
	selected     []store.BotID
	reconciled   []uuid.UUID
}

func (s *correctionAPIRepo) Get(context.Context, store.BotID) (*store.Bot, error) { return &s.bot, nil }
func (s *correctionAPIRepo) BotMemoryStore(id store.BotID) (store.MemoryStore, error) {
	s.selected = append(s.selected, id)
	return s.memory, s.providerErr
}
func (s *correctionAPIRepo) ReconcileCorrection(_ context.Context, id store.BotID, correction uuid.UUID, memory store.MemoryStore) error {
	if id != s.bot.ID || memory != s.memory {
		return store.ErrBotForbidden
	}
	s.reconciled = append(s.reconciled, correction)
	return s.reconcileErr
}

// Deliberately lacks BotMemoryStoreProvider; generic services must not be a fallback.
type correctionAPIWithoutProvider struct {
	store.BotStore
	reconciler store.BotCorrectionReconciler
}

func (s *correctionAPIWithoutProvider) ReconcileCorrection(ctx context.Context, id store.BotID, correction uuid.UUID, memory store.MemoryStore) error {
	return s.reconciler.ReconcileCorrection(ctx, id, correction, memory)
}

func TestBotReconcileCorrectionScopedMemoryHTTP(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, tc := range []struct {
			name                       string
			providerErr, reconcileErr  error
			missingProvider, nilMemory bool
			body                       string
			status, calls              int
		}{
			{name: "scoped disk memory", status: 200, calls: 1},
			{name: "provider unavailable", providerErr: store.ErrBotUnsupported, status: 503},
			{name: "provider forbidden", providerErr: store.ErrBotForbidden, status: 403},
			{name: "nil memory", nilMemory: true, status: 503},
			{name: "no generic fallback", missingProvider: true, status: 503},
			{name: "permission revoked", reconcileErr: store.ErrBotForbidden, status: 403, calls: 1},
			{name: "saved intent only", body: `{"replacement":"unreviewed"}`, status: 400},
		} {
			t.Run(string(scope)+"/"+tc.name, func(t *testing.T) {
				actor, id, correction := uuid.New(), uuid.New(), uuid.New()
				ctx, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: actor.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
				if err != nil {
					t.Fatal(err)
				}
				ctx = store.WithTenantContext(ctx, &store.TenantContext{UserID: actor.String(), OrgSlug: "org", TeamSlug: "team"})
				repo := &correctionAPIRepo{bot: store.Bot{ID: id, Scope: scope, OwnerID: actor}, memory: &correctionScopedMemory{}, providerErr: tc.providerErr, reconcileErr: tc.reconcileErr}
				if tc.nilMemory {
					repo.memory = nil
				}
				var selected store.BotStore = repo
				if tc.missingProvider {
					selected = &correctionAPIWithoutProvider{BotStore: repo, reconciler: repo}
				}
				svc := &store.Services{Memory: &correctionScopedMemory{}}
				if scope == store.BotScopePersonal {
					svc.PersonalBots = selected
				} else {
					svc.Bots = selected
				}
				ctx = store.WithServices(ctx, svc)
				router := mux.NewRouter()
				registerBotGoalRoutes(router)
				request := httptest.NewRequest(http.MethodPost, "/api/studio/bots/"+id.String()+"/corrections/"+correction.String()+"/reconcile?scope="+string(scope), strings.NewReader(tc.body)).WithContext(ctx)
				response := httptest.NewRecorder()
				router.ServeHTTP(response, request)
				if response.Code != tc.status {
					t.Fatalf("status=%d want=%d body=%s", response.Code, tc.status, response.Body.String())
				}
				if len(repo.reconciled) != tc.calls {
					t.Fatalf("reconcile calls=%v", repo.reconciled)
				}
				if tc.calls > 0 && (len(repo.selected) != 1 || repo.selected[0] != id || repo.reconciled[0] != correction) {
					t.Fatal("scope or saved correction identity lost")
				}
				if tc.status == 200 && !strings.Contains(response.Body.String(), `"memory_applied":true`) {
					t.Fatal("missing application receipt")
				}
			})
		}
	}
}

// Real scoped SQLite persistence and production HTTP routes; only the Home
// transport is controlled. Configuration stays readable; runtime files fail.
type databaseActivityFiles struct {
	*skillRecoveryFiles
	denyRuntime   atomic.Bool
	runtimeOpens  atomic.Int64
	runtimeWrites atomic.Int64
}

func (b *databaseActivityFiles) OpenBotContent(ctx context.Context, home *sandbox.BotHomeBinding, ref store.BotContentRef, span store.BotFileRange) (*store.BotFileStream, error) {
	if b.denyRuntime.Load() && (ref.Kind == store.BotContentEvidence || ref.Kind == store.BotContentArtifact) {
		b.runtimeOpens.Add(1)
		return nil, store.ErrBotUnsupported
	}
	return b.skillRecoveryFiles.OpenBotContent(ctx, home, ref, span)
}
func (b *databaseActivityFiles) PutBotContent(ctx context.Context, home *sandbox.BotHomeBinding, kind store.BotContentKind, size int64, body io.Reader, revision int64, guard func(func() error) error) (*store.BotContentRef, error) {
	if b.denyRuntime.Load() && (kind == store.BotContentEvidence || kind == store.BotContentArtifact) {
		b.runtimeWrites.Add(1)
		return nil, store.ErrBotUnsupported
	}
	return b.skillRecoveryFiles.PutBotContent(ctx, home, kind, size, body, revision, guard)
}

func TestBotDatabaseActivityHTTP(t *testing.T) {
	t.Setenv("ASTONISH_PROFILE_BOT_ACTIVITY", "1")
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f := newSkillStorageHTTPFixture(t)
			files := &databaseActivityFiles{skillRecoveryFiles: &skillRecoveryFiles{root: t.TempDir()}}
			f.homeBackend = files.skillRecoveryFiles
			if err := f.db.InstallBotContentHomeBackend(files); err != nil {
				t.Fatal(err)
			}
			b := f.bot(scope)
			repo, ctx := f.repo(scope), f.context(f.owner)
			home := sandbox.BotHomeBinding{Scope: sandbox.BotHomeScope{OrgID: f.org.Slug, TeamID: f.team.Slug, UserID: f.owner, BotID: b.ID.String()}, Backend: sandbox.BackendKindDocker, Ref: b.Home.StorageKey}
			if err := entstore.SelectBotHomeConfiguration(ctx, repo, home, files); err != nil {
				t.Fatal(err)
			}
			files.denyRuntime.Store(true)
			t.Cleanup(func() {
				if opens, writes := files.runtimeOpens.Load(), files.runtimeWrites.Load(); opens != 0 || writes != 0 {
					t.Errorf("runtime file access: opens=%d writes=%d; want zero", opens, writes)
				}
			})
			run, err := repo.RequestRun(ctx, store.BotRunRequest{BotID: b.ID, Version: b.Version, Trigger: store.TriggerManual, Instructions: "Database activity acceptance", IdempotencyKey: uuid.NewString()})
			if err != nil {
				t.Fatal(err)
			}
			worker := execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: b.ID, RunID: run.ID, Version: b.Version, ActorID: f.owner, OrgSlug: f.org.Slug, TeamSlug: f.team.Slug})
			lease, err := repo.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: run.ID, WorkerID: "database-activity", ExpiresAt: time.Now().Add(5 * time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			initial, err := repo.ListActivity(ctx, b.ID, 0, 200)
			if err != nil || len(initial) == 0 {
				t.Fatalf("initial activity: %v %v", initial, err)
			}
			after := initial[len(initial)-1].Sequence
			want := make([]store.BotActivity, 100)
			for i := range want {
				payload := json.RawMessage(fmt.Sprintf(`{"ordinal":%d,"integer":9007199254740993,"text":"U.S. markets — 日本","nested":{"items":[true,null,"ack"]}}`, i))
				want[i], err = repo.AppendActivity(worker, lease, store.BotActivity{BotID: b.ID, RunID: &run.ID, Kind: store.BotActivityRun, Payload: payload})
				if err != nil {
					t.Fatalf("append %d: %v", i, err)
				}
				if want[i].Sequence != after+int64(i)+1 || string(want[i].Payload) != string(payload) {
					t.Fatalf("append changed payload/order at %d: %+v", i, want[i])
				}
			}
			path := skillPath(b, "/activity")
			read := func(actor string, cursor int64, limit int) []store.BotActivity {
				t.Helper()
				q, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s%s&after=%d&limit=%d", f.server.URL, path, cursor, limit), nil)
				if err != nil {
					t.Fatal(err)
				}
				q.Header.Set("X-Test-Actor", actor)
				result, err := f.server.Client().Do(q)
				if err != nil {
					t.Fatal(err)
				}
				defer result.Body.Close()
				raw, err := io.ReadAll(result.Body)
				if err != nil {
					t.Fatal(err)
				}
				if result.StatusCode != http.StatusOK {
					t.Fatalf("activity HTTP %d: %s", result.StatusCode, raw)
				}
				if result.Header.Get("X-Astonish-Bot-Runtime-IO-Covered") != "true" {
					t.Fatal("real database activity read lacks runtime I/O coverage")
				}
				for _, header := range []string{"X-Astonish-Bot-Runtime-Opens", "X-Astonish-Bot-Runtime-Puts", "X-Astonish-Bot-Runtime-Failures"} {
					if result.Header.Get(header) != "0" {
						t.Fatalf("%s=%q", header, result.Header.Get(header))
					}
				}
				var response struct {
					Activity []store.BotActivity `json:"activity"`
				}
				if err := json.Unmarshal(raw, &response); err != nil {
					t.Fatal(err)
				}
				if response.Activity == nil {
					t.Fatalf("activity must be [] rather than null: %s", raw)
				}
				return response.Activity
			}
			check := func(got, expected []store.BotActivity) {
				t.Helper()
				if len(got) != len(expected) {
					t.Fatalf("activity count=%d want=%d", len(got), len(expected))
				}
				for i := range got {
					g, w := got[i], expected[i]
					if g.BotID != w.BotID || g.RunID == nil || w.RunID == nil || *g.RunID != *w.RunID || g.Sequence != w.Sequence || g.Kind != w.Kind || !g.CreatedAt.Equal(w.CreatedAt) || string(g.Payload) != string(w.Payload) {
						t.Fatalf("activity %d changed: got=%+v want=%+v", i, g, w)
					}
				}
			}
			check(read(f.owner, after, 100), want)
			all := read(f.owner, 0, 200)
			allJSON, err := json.Marshal(all)
			if err != nil {
				t.Fatal(err)
			}
			expectedJSON, err := json.Marshal(append(initial, want...))
			if err != nil || string(allJSON) != string(expectedJSON) {
				t.Fatalf("after=0 changed full logical activity/order: %v", err)
			}
			var paginated []store.BotActivity
			cursor := after
			for {
				page := read(f.owner, cursor, 17)
				if len(page) == 0 {
					break
				}
				paginated = append(paginated, page...)
				if len(paginated) > 100 {
					t.Fatal("pagination repeated activity")
				}
				cursor = page[len(page)-1].Sequence
			}
			check(paginated, want)
			if got := read(f.owner, want[99].Sequence, 100); len(got) != 0 {
				t.Fatalf("nonempty exhausted activity: %v", got)
			}
			f.request(http.MethodGet, path, "", "", http.StatusUnauthorized)
			otherScope := store.BotScopeTeam
			if scope == store.BotScopeTeam {
				otherScope = store.BotScopePersonal
				check(read(f.member, after, 100), want)
				org, err := f.db.ForOrg(f.org.Slug)
				if err != nil {
					t.Fatal(err)
				}
				if err := org.Teams().RemoveMember(ctx, f.member, f.team.ID); err != nil {
					t.Fatal(err)
				}
				f.request(http.MethodGet, path, f.member, "", http.StatusForbidden)
			} else {
				f.request(http.MethodGet, path, f.member, "", http.StatusNotFound)
			}
			f.request(http.MethodGet, "/api/studio/bots/"+b.ID.String()+"/activity?scope="+string(otherScope), f.owner, "", http.StatusNotFound)
			f.request(http.MethodGet, "/api/studio/bots/"+uuid.NewString()+"/activity?scope="+string(scope), f.owner, "", http.StatusNotFound)
			for _, query := range []string{"&after=-1", "&after=bad", "&limit=0", "&limit=201", "&limit=bad"} {
				f.request(http.MethodGet, path+query, f.owner, "", http.StatusBadRequest)
			}
			f.request(http.MethodGet, "/api/studio/bots/bad-id/activity?scope="+string(scope), f.owner, "", http.StatusBadRequest)
			f.request(http.MethodGet, "/api/studio/bots/"+b.ID.String()+"/activity?scope=unknown", f.owner, "", http.StatusBadRequest)
			// Close all repositories/connections: an in-memory projection cannot pass.
			f.homeBackend = nil
			f.server.Close()
			if err := f.db.Close(); err != nil {
				t.Fatal(err)
			}
			f.open()
			if err := f.db.InstallBotContentHomeBackend(files); err != nil {
				t.Fatal(err)
			}
			f.start()
			check(read(f.owner, after, 100), want)
		})
	}
}

// Embedding BotStore keeps diagnostics independent of persistence setup while
// exercising the actual activity handler and its authentication/scope selection.
type activityProfileRepo struct {
	store.BotStore
	covered  bool
	attempts bool
	err      error
}

func (s *activityProfileRepo) ListActivity(ctx context.Context, _ store.BotID, _ int64, _ int) ([]store.BotActivity, error) {
	if s.covered {
		store.MarkBotRuntimeIOProfileCovered(ctx)
	}
	if s.attempts {
		store.ObserveBotRuntimeIO(ctx, store.BotRuntimeIOOpen, 7)(errors.New("secret path/token"))
		store.ObserveBotRuntimeIO(ctx, store.BotRuntimeIOPut, 7)(nil)
	}
	return nil, s.err
}

func TestBotActivityProfileHTTPDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		enabled, covered, attempts bool
		err                        error
		status                     int
	}{
		{name: "disabled", covered: true, attempts: true, status: 200},
		{name: "uncovered zero is not evidence", enabled: true, status: 200},
		{name: "covered empty zero", enabled: true, covered: true, status: 200},
		{name: "failed attempt", enabled: true, covered: true, attempts: true, err: store.ErrBotUnsupported, status: 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.enabled {
				t.Setenv("ASTONISH_PROFILE_BOT_ACTIVITY", "1")
			} else {
				t.Setenv("ASTONISH_PROFILE_BOT_ACTIVITY", "")
			}
			var logs bytes.Buffer
			old := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(old) })
			actor, id := uuid.New(), uuid.New()
			ctx, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: actor.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
			if err != nil {
				t.Fatal(err)
			}
			ctx = store.WithTenantContext(ctx, &store.TenantContext{UserID: actor.String(), OrgSlug: "org", TeamSlug: "team"})
			ctx = store.WithServices(ctx, &store.Services{PersonalBots: &activityProfileRepo{covered: tc.covered, attempts: tc.attempts, err: tc.err}})
			router := mux.NewRouter()
			registerBotGoalRoutes(router)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest("GET", "/api/studio/bots/"+id.String()+"/activity?scope=personal&after=0&limit=100", nil).WithContext(ctx))
			result := response.Result()
			defer result.Body.Close()
			body, err := io.ReadAll(result.Body)
			if err != nil {
				t.Fatal(err)
			}
			if result.StatusCode != tc.status {
				t.Fatalf("status=%d body=%s", result.StatusCode, body)
			}
			if tc.status == 200 && string(body) != "{\"activity\":[]}\n" {
				t.Fatalf("JSON changed: %s", body)
			}
			requestID := result.Header.Get("X-Astonish-Bot-Activity-Request-ID")
			if !tc.enabled {
				if requestID != "" || logs.Len() != 0 || result.Header.Get("Server-Timing") != "" || len(result.Trailer) != 0 {
					t.Fatal("profiling was not opt-in")
				}
				return
			}
			if _, err := uuid.Parse(requestID); err != nil {
				t.Fatalf("invalid request ID: %q", requestID)
			}
			wantCovered := "false"
			if tc.covered {
				wantCovered = "true"
			}
			if result.Header.Get("X-Astonish-Bot-Runtime-IO-Covered") != wantCovered {
				t.Fatal("coverage missing or fabricated")
			}
			wantCount := "0"
			if tc.attempts {
				wantCount = "1"
			}
			for _, header := range []string{"X-Astonish-Bot-Runtime-Opens", "X-Astonish-Bot-Runtime-Puts", "X-Astonish-Bot-Runtime-Failures"} {
				if result.Header.Get(header) != wantCount {
					t.Fatalf("%s=%q", header, result.Header.Get(header))
				}
			}
			if result.Trailer.Get("X-Astonish-Bot-Activity-Handler-Total-Ns") == "" || !strings.Contains(result.Header.Get("Server-Timing"), "bot_activity_headers;dur=") {
				t.Fatal("timing contract missing")
			}
			if !strings.Contains(logs.String(), `"stage":"handler_total"`) || !strings.Contains(logs.String(), requestID) || !strings.Contains(logs.String(), `"runtime_io_covered":`+wantCovered) {
				t.Fatalf("incomplete correlated diagnostics: %s", logs.String())
			}
			if strings.Contains(logs.String(), "secret") || strings.Contains(logs.String(), actor.String()) || strings.Contains(logs.String(), id.String()) {
				t.Fatal("sensitive diagnostic data")
			}
		})
	}
}

type activityProfileFailedWriter struct{ *httptest.ResponseRecorder }

func (w activityProfileFailedWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestBotActivityProfileWriterFailure(t *testing.T) {
	ctx, profile := store.WithBotRuntimeIOProfiler(context.Background(), nil)
	store.MarkBotRuntimeIOProfileCovered(ctx)
	recorder := httptest.NewRecorder()
	w := &botActivityProfileWriter{ResponseWriter: activityProfileFailedWriter{recorder}, profile: profile, start: time.Now()}
	if n, err := w.Write([]byte("body")); n != 0 || !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("n=%d err=%v", n, err)
	}
	w.WriteHeader(500)
	if !w.writeFailed || w.bytes != 0 || w.status != 200 || recorder.Code != 200 {
		t.Fatalf("bad write accounting: %+v", w)
	}
	if recorder.Header().Get("X-Astonish-Bot-Runtime-IO-Covered") != "true" {
		t.Fatal("coverage missing")
	}
}
