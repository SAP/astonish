package daemon

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/gorilla/mux"
)

// Replay uses a real HTTP socket after execution. Its lifetime is independent
// from the worker; reconnecting cannot start a model or write a run.
func assertWorkerHistoryHTTP(t *testing.T, ctx context.Context, repo *workerEventRepo, ss workerEventSessions) {
	t.Helper()
	ss.meta.Purpose = store.SessionPurposeBotRun
	ss.meta.Retention = store.SessionRetentionBot
	principal, _ := execution.PrincipalFromContext(ctx)
	ctx = store.WithTenantContext(ctx, &store.TenantContext{UserID: principal.Subject, OrgSlug: principal.OrgSlug, TeamSlug: principal.TeamSlug})
	ctx = store.WithServices(ctx, &store.Services{Bots: repo, Sessions: ss})
	router := mux.NewRouter()
	router.HandleFunc("/bots/{botID}/sessions/{sessionID}/stream", api.BotHistoryStreamHandler)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { router.ServeHTTP(w, r.WithContext(ctx)) }))
	defer server.Close()
	for i := 0; i < 2; i++ {
		response, err := server.Client().Get(server.URL + "/bots/" + repo.b.ID.String() + "/sessions/" + repo.r.SessionID.String() + "/stream?scope=team")
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != 200 || response.Header.Get("Content-Type") != "text/event-stream" || !strings.Contains(string(body), "durable controlled result") || !strings.Contains(string(body), "bot_runtime_event") || !strings.Contains(string(body), "event: done") {
			t.Fatalf("HTTP replay %d: %s", response.StatusCode, body)
		}
	}
}
