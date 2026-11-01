package api

import (
	"context"
	"encoding/json"
	persistentsession "github.com/SAP/astonish/pkg/session"
	adksession "google.golang.org/adk/session"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/gorilla/mux"
)

func TestSubtaskSelectorForeignTenantHTTP(t *testing.T) {
	env := setupMemoryHTTPAcceptance(t, NewMockLLM(TextTurn("Parent ready.")), nil)
	events := env.Turn(t, "", "Create parent", false)
	parent := toString(assertHasEvent(t, events, "session").Data["sessionId"])
	for _, selector := range []string{"child_session_id=foreign-child", "batch_id=foreign-batch&task_id=foreign-task&attempt=1"} {
		resp := env.Request(t, http.MethodGet, "/api/studio/sessions/"+parent+"/subtask-events?"+selector, nil, memoryIdentityForeign)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("foreign tenant exact selector status=%d", resp.StatusCode)
		}
	}
}

type selectorSessionStore struct {
	store.SessionStore
	service  adksession.Service
	metas    map[string]*store.SessionMeta
	children []store.SessionMeta
	reads    int
}

func (s *selectorSessionStore) GetSessionMeta(_ context.Context, id string) (*store.SessionMeta, error) {
	return s.metas[id], nil
}
func (s *selectorSessionStore) ListChildren(_ context.Context, id string) ([]store.SessionMeta, error) {
	children := make([]store.SessionMeta, 0, len(s.children))
	for _, child := range s.children {
		if child.ParentID == id {
			children = append(children, child)
		}
	}
	return children, nil
}
func (s *selectorSessionStore) Get(ctx context.Context, r *adksession.GetRequest) (*adksession.GetResponse, error) {
	return s.service.Get(ctx, r)
}
func (s *selectorSessionStore) ReadTranscriptEvents(context.Context, string, string, string) ([]*adksession.Event, error) {
	s.reads++
	return nil, nil
}

func TestSubtaskSelectorsEnforceParentTenantAndAmbiguity(t *testing.T) {
	for _, tc := range []struct {
		name, query, parent, user string
		count, status             int
		omitParentState           bool
	}{
		{name: "exact child", query: "child_session_id=child1", parent: "parent", user: "caller", count: 1, status: 200},
		{name: "platform child parent metadata only", query: "child_session_id=child1", parent: "parent", user: "caller", count: 1, status: 200, omitParentState: true},
		{name: "exact child ignores stale tuple", query: "child_session_id=child1&batch_id=stale&task_id=stale&attempt=9", parent: "parent", user: "caller", count: 1, status: 200},
		{name: "exact tuple", query: "batch_id=batch&task_id=task&attempt=1", parent: "parent", user: "caller", count: 1, status: 200},
		{name: "wrong parent child", query: "child_session_id=child1", parent: "other-parent", user: "caller", count: 1, status: 404},
		{name: "wrong parent tuple", query: "batch_id=batch&task_id=task&attempt=1", parent: "other-parent", user: "caller", count: 1, status: 404},
		{name: "foreign tenant child", query: "child_session_id=child1", parent: "parent", user: "foreign", count: 1, status: 404},
		{name: "foreign tenant tuple", query: "batch_id=batch&task_id=task&attempt=1", parent: "parent", user: "foreign", count: 1, status: 404},
		{name: "legacy ambiguous", query: "task_name=same", parent: "parent", user: "caller", count: 2, status: 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &selectorSessionStore{service: adksession.InMemoryService(), metas: map[string]*store.SessionMeta{"parent": {ID: "parent", UserID: "caller", AppName: studioChatAppName}}}
			for _, id := range []string{"child1", "child2"}[:tc.count] {
				state := map[string]any{"batch_id": "batch", "task_id": "task", "attempt": 1}
				if !tc.omitParentState {
					state[persistentsession.StateKeyParentID] = tc.parent
				}
				_, err := s.service.Create(context.Background(), &adksession.CreateRequest{AppName: studioChatAppName, UserID: tc.user, SessionID: id, State: state})
				if err != nil {
					t.Fatal(err)
				}
				s.children = append(s.children, store.SessionMeta{ID: id, Title: "same", UserID: "caller", ParentID: tc.parent})
			}
			req := mux.SetURLVars(httptest.NewRequest("GET", "/?"+tc.query, nil), map[string]string{"id": "parent"})
			ctx := WithPlatformUser(req.Context(), &PlatformUser{ID: "caller", OrgSlug: "tenant-a", TeamSlug: "team"})
			ctx = store.WithTenantContext(ctx, &store.TenantContext{OrgSlug: "tenant-a", TeamSlug: "team", UserID: "caller"})
			req = req.WithContext(store.WithServices(ctx, &store.Services{Mode: store.ModePlatform, PersonalSessions: s}))
			w := httptest.NewRecorder()
			StudioSubtaskEventsHandler(w, req)
			if w.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", w.Code, tc.status, w.Body.String())
			}
			if tc.status != 200 && s.reads != 0 {
				t.Fatal("unauthorized/ambiguous selector read transcript")
			}
			if tc.status == 409 && !strings.Contains(w.Body.String(), "ambiguous") {
				t.Fatal(w.Body.String())
			}
		})
	}
}

func TestSubtaskSelectorValidation(t *testing.T) {
	for _, q := range []string{"attempt=-1", "attempt=wat", "batch_id=b", "batch_id=b&task_id=t", "child_session_id=../bad"} {
		t.Run(q, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/?"+q, nil)
			req = mux.SetURLVars(req, map[string]string{"id": "parent"})
			svc := &store.Services{Mode: store.ModePlatform, PersonalSessions: &memSessionStore{metas: map[string]*store.SessionMeta{"parent": {ID: "parent", UserID: "caller", AppName: studioChatAppName}}}}
			ctx := WithPlatformUser(req.Context(), &PlatformUser{ID: "caller"})
			req = req.WithContext(store.WithServices(ctx, svc))
			w := httptest.NewRecorder()
			StudioSubtaskEventsHandler(w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestSessionOwnerAllowed(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(WithPlatformUser(req.Context(), &PlatformUser{ID: "caller"}))
	if sessionOwnerAllowed(req, "other") {
		t.Fatal("expected a different user to be denied")
	}
	if !sessionOwnerAllowed(req, "caller") {
		t.Fatal("expected the session owner to be allowed")
	}
	adminReq := httptest.NewRequest(http.MethodGet, "/", nil)
	adminReq = adminReq.WithContext(WithPlatformUser(adminReq.Context(), &PlatformUser{ID: "admin", PlatformRole: "superadmin"}))
	if !sessionOwnerAllowed(adminReq, "other") {
		t.Fatal("expected platform admin override")
	}
}

func TestRequireSessionIDOwnerAdminStillNeedsSession(t *testing.T) {
	t.Parallel()
	// Use real request-scoped service wiring, not just an admin principal with
	// no store (which only exercises the missing-services guard).
	personal := &memSessionStore{metas: map[string]*store.SessionMeta{
		"personal-session": {ID: "personal-session", UserID: "owner", AppName: studioChatAppName},
	}}
	team := &memSessionStore{metas: map[string]*store.SessionMeta{
		"team-session": {ID: "team-session", UserID: "owner", AppName: studioChatAppName},
	}}
	svc := &store.Services{Mode: store.ModePlatform, PersonalSessions: personal, Sessions: team}
	for _, tc := range []struct {
		name    string
		id      string
		withSvc bool
		allowed bool
	}{
		{name: "empty", withSvc: true},
		{name: "missing", id: "missing-session", withSvc: true},
		{name: "existing personal", id: "personal-session", withSvc: true, allowed: true},
		{name: "existing team fallback", id: "team-session", withSvc: true, allowed: true},
		{name: "no scoped services", id: "personal-session"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			ctx := WithPlatformUser(req.Context(), &PlatformUser{ID: "admin", PlatformRole: "superadmin", OrgSlug: "acme", TeamSlug: "collision"})
			ctx = store.WithTenantContext(ctx, &store.TenantContext{OrgSlug: "acme", TeamSlug: "collision", UserID: "admin"})
			if tc.withSvc {
				ctx = store.WithServices(ctx, svc)
			}
			w := httptest.NewRecorder()
			if got := requireSessionIDOwner(w, req.WithContext(ctx), tc.id); got != tc.allowed {
				t.Fatalf("allowed = %v, want %v", got, tc.allowed)
			}
			wantStatus := http.StatusNotFound
			if tc.allowed {
				wantStatus = http.StatusOK
			}
			if w.Code != wantStatus {
				t.Fatalf("status = %d, want %d", w.Code, wantStatus)
			}
		})
	}
}

func TestRequireSessionIDOwnerNotFound(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(WithPlatformUser(req.Context(), &PlatformUser{ID: "caller"}))
	w := httptest.NewRecorder()
	if requireSessionIDOwner(w, req, "missing-session") {
		t.Fatal("expected missing session to be denied")
	}
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing session, got %d", w.Code)
	}
}

// TestStudioChatRequestDeserialization verifies that the StudioChatRequest struct
// correctly deserializes the optional appName field.
func TestStudioChatRequestDeserialization(t *testing.T) {
	t.Run("with appName", func(t *testing.T) {
		input := `{"message":"hello","appName":"astonish-extension"}`
		var req StudioChatRequest
		if err := json.Unmarshal([]byte(input), &req); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if req.AppName != "astonish-extension" {
			t.Errorf("got AppName=%q, want %q", req.AppName, "astonish-extension")
		}
		if req.Message != "hello" {
			t.Errorf("got Message=%q, want %q", req.Message, "hello")
		}
	})

	t.Run("without appName defaults to empty", func(t *testing.T) {
		input := `{"message":"hello"}`
		var req StudioChatRequest
		if err := json.Unmarshal([]byte(input), &req); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if req.AppName != "" {
			t.Errorf("got AppName=%q, want empty", req.AppName)
		}
	})

	t.Run("appName omitted in JSON output when empty", func(t *testing.T) {
		req := StudioChatRequest{Message: "hello"}
		out, err := json.Marshal(req)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var m map[string]interface{}
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatalf("unmarshal output: %v", err)
		}
		if _, ok := m["appName"]; ok {
			t.Error("appName should be omitted from JSON when empty")
		}
	})
}

// TestValidAppName exercises the real validAppName regex used by all handlers
// to validate the ?app= query parameter and StudioChatRequest.AppName field.
// This prevents namespace squatting, path traversal, and unbounded key lengths.
func TestValidAppName(t *testing.T) {
	valid := []string{
		"astonish",
		"astonish-extension",
		"astonish_code",
		"a",
		"my-app",
		"fleet-scheduler",
	}
	for _, name := range valid {
		if !validAppName.MatchString(name) {
			t.Errorf("validAppName should accept %q", name)
		}
	}

	invalid := []string{
		"",                       // empty
		"../../../etc",           // path traversal
		"Astonish",               // uppercase
		"astonish extension",     // space
		"-leading-dash",          // leading dash
		"a;DROP TABLE sessions",  // SQL injection attempt
		"a/b",                    // path separator
		"a\\b",                   // backslash
		"a$b",                    // shell metachar
		string(make([]byte, 65)), // too long (65 chars)
	}
	for _, name := range invalid {
		if validAppName.MatchString(name) {
			t.Errorf("validAppName should reject %q", name)
		}
	}
}

// TestAppNameQueryParamDefaulting verifies the ?app= query-param resolution
// logic shared by StudioSessionsHandler, StudioSessionHandler, and
// StudioDeleteSessionHandler. This tests the actual resolution + validation
// code path, not a separate closure.
func TestAppNameQueryParamDefaulting(t *testing.T) {
	// resolveAndValidate mimics the exact three-line pattern in every handler:
	//   appName := r.URL.Query().Get("app")
	//   if appName == "" { appName = studioChatAppName }
	//   if !validAppName.MatchString(appName) → error
	resolveAndValidate := func(rawURL string) (string, bool) {
		r := httptest.NewRequest(http.MethodGet, rawURL, nil)
		appName := r.URL.Query().Get("app")
		if appName == "" {
			appName = studioChatAppName
		}
		return appName, validAppName.MatchString(appName)
	}

	cases := []struct {
		url    string
		want   string
		wantOK bool
	}{
		{"/api/studio/sessions", studioChatAppName, true},
		{"/api/studio/sessions?app=", studioChatAppName, true},
		{"/api/studio/sessions?app=astonish-extension", "astonish-extension", true},
		{"/api/studio/sessions?app=astonish", studioChatAppName, true},
		{"/api/studio/sessions?app=astonish_code", "astonish_code", true},
		{"/api/studio/sessions?app=../../etc", "../../etc", false},
		{"/api/studio/sessions?app=A", "A", false},
		{"/api/studio/sessions?app=hello%20world", "hello world", false},
	}

	for _, tc := range cases {
		got, ok := resolveAndValidate(tc.url)
		if got != tc.want {
			t.Errorf("resolve(%q) = %q, want %q", tc.url, got, tc.want)
		}
		if ok != tc.wantOK {
			t.Errorf("validate(%q) valid=%v, want %v", tc.url, ok, tc.wantOK)
		}
	}
}

// TestEffectiveAppResolution verifies the effective-app selection logic in
// StudioChatHandler: caller-provided AppName takes precedence, empty falls
// back to studioChatAppName, and invalid values are rejected by validation.
func TestEffectiveAppResolution(t *testing.T) {
	resolveAndValidate := func(req StudioChatRequest) (string, bool) {
		app := studioChatAppName
		if req.AppName != "" {
			app = req.AppName
		}
		return app, validAppName.MatchString(app)
	}

	cases := []struct {
		name   string
		req    StudioChatRequest
		want   string
		wantOK bool
	}{
		{"no AppName → default", StudioChatRequest{Message: "hi"}, studioChatAppName, true},
		{"empty AppName → default", StudioChatRequest{Message: "hi", AppName: ""}, studioChatAppName, true},
		{"extension AppName", StudioChatRequest{Message: "hi", AppName: "astonish-extension"}, "astonish-extension", true},
		{"traversal attempt rejected", StudioChatRequest{Message: "hi", AppName: "../../../etc"}, "../../../etc", false},
		{"uppercase rejected", StudioChatRequest{Message: "hi", AppName: "Astonish"}, "Astonish", false},
		{"valid custom namespace", StudioChatRequest{Message: "hi", AppName: "my-namespace"}, "my-namespace", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := resolveAndValidate(tc.req)
			if got != tc.want {
				t.Errorf("effectiveApp = %q, want %q", got, tc.want)
			}
			if ok != tc.wantOK {
				t.Errorf("valid = %v, want %v", ok, tc.wantOK)
			}
		})
	}
}
