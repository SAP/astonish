package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

// Bot history authorization is not authority to inspect a generic Chat runner
// or resolve a model using the viewer's credentials. Dedicated runtime adapters
// must establish that authority before these endpoints can support Bots.
func TestBotTeamSessionAccess(t *testing.T) {
	actor, owner, botID, sid := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	repo := &botSessionFixtureStore{b: store.Bot{ID: botID, OwnerID: owner, Scope: store.BotScopeTeam, HomeSessionID: sid}}
	meta := &store.SessionMeta{ID: sid.String(), UserID: owner.String(), BotID: botID.String(), Purpose: store.SessionPurposeBotHome, Retention: store.SessionRetentionBot}
	svc := &store.Services{Sessions: &botSessionMetaFixture{meta: meta}, Bots: repo}
	runner := newChatRunner(sid.String(), owner.String(), studioChatAppName, false)
	runner.emitEvent("text", map[string]any{"text": "private runtime event"})
	runner.closeSubscribers()
	registry := getChatRunnerRegistry()
	registry.Register(sid.String(), runner)
	t.Cleanup(func() { registry.Stop(sid.String()) })

	router := mux.NewRouter()
	router.HandleFunc("/sessions/{id}/stream", StudioChatStreamHandler).Methods(http.MethodGet)
	router.HandleFunc("/sessions/{id}/status", StudioChatStatusHandler).Methods(http.MethodGet)
	router.HandleFunc("/sessions/{id}/model-status", GetSessionModelStatusHandler).Methods(http.MethodGet)
	router.HandleFunc("/sessions/{id}/model", PatchSessionModelHandler).Methods(http.MethodPatch)
	router.HandleFunc("/sessions/{id}/stop", StudioStopHandler).Methods(http.MethodPost)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Test-Actor")
		ctx, err := execution.WithPrincipal(r.Context(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: id, OrgSlug: "org", TeamSlug: "team", Authenticated: true})
		if err != nil {
			t.Error(err)
			http.Error(w, "bad fixture", 500)
			return
		}
		ctx = store.WithTenantContext(ctx, &store.TenantContext{UserID: id, OrgSlug: "org", TeamSlug: "team"})
		ctx = context.WithValue(ctx, platformUserKey{}, &PlatformUser{ID: id, OrgSlug: "org", TeamSlug: "team", Role: "member"})
		router.ServeHTTP(w, r.WithContext(store.WithServices(ctx, svc)))
	}))
	defer server.Close()
	for _, id := range []uuid.UUID{actor, owner} {
		for _, tc := range []struct{ method, suffix string }{
			{http.MethodGet, "stream"}, {http.MethodGet, "status"}, {http.MethodGet, "model-status"},
			{http.MethodPatch, "model"}, {http.MethodPost, "stop"},
		} {
			t.Run(id.String()+"/"+tc.suffix, func(t *testing.T) {
				request, err := http.NewRequest(tc.method, server.URL+"/sessions/"+sid.String()+"/"+tc.suffix, strings.NewReader(`{}`))
				if err != nil {
					t.Fatal(err)
				}
				request.Header.Set("X-Test-Actor", id.String())
				response, err := server.Client().Do(request)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				body, err := io.ReadAll(response.Body)
				if err != nil {
					t.Fatal(err)
				}
				if response.StatusCode != http.StatusNotFound {
					t.Errorf("runtime endpoint returned %d, want 404: %s", response.StatusCode, body)
				}
				if strings.Contains(string(body), "private runtime event") {
					t.Error("Bot history permission leaked generic runner events")
				}
			})
		}
	}
	if registry.Get(sid.String()) != runner {
		t.Fatal("generic stop mutated Bot runner")
	}
	// The guard must preserve ordinary Chat owner access, including actual SSE
	// replay, while denying another viewer of the same scoped session record.
	meta.BotID, meta.BotRunID, meta.Retention = "", "", ""
	meta.Purpose = store.SessionPurposeChat
	for _, tc := range []struct {
		actor  uuid.UUID
		status int
	}{{owner, http.StatusOK}, {actor, http.StatusNotFound}} {
		for _, suffix := range []string{"stream", "status", "model-status"} {
			request, err := http.NewRequest(http.MethodGet, server.URL+"/sessions/"+sid.String()+"/"+suffix, nil)
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("X-Test-Actor", tc.actor.String())
			response, err := server.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != tc.status {
				t.Fatalf("ordinary Chat %s: %d want %d: %s", suffix, response.StatusCode, tc.status, body)
			}
			if suffix == "stream" && tc.status == http.StatusOK && !strings.Contains(string(body), "private runtime event") {
				t.Fatal("ordinary owner lost SSE replay")
			}
		}
	}
}
