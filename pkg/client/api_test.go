package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSubtaskEventsExactQueryAndErrors(t *testing.T) {
	for _, status := range []int{200, 403, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for key, want := range map[string]string{"child_session_id": "child", "batch_id": "batch", "task_id": "task", "attempt": "2", "task_name": "name & value"} {
					if r.URL.Query().Get(key) != want {
						t.Errorf("%s=%q", key, r.URL.Query().Get(key))
					}
				}
				if r.Header.Get("Authorization") != "Bearer token" {
					t.Error("missing auth")
				}
				w.WriteHeader(status)
				w.Write([]byte(`{"events":[{"type":"tool_call","call_id":"call","child_session_id":"child","sequence":3}]}`))
			}))
			defer s.Close()
			c := NewWithConfig(s.URL)
			c.cached = &Tokens{AccessToken: "token", Kind: TokenKindOAuth}
			rows, err := c.GetSubtaskEvents(context.Background(), "parent", SubtaskInfo{Name: "name & value", ChildSessionID: "child", BatchID: "batch", TaskID: "task", Attempt: 2})
			if status != 200 {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil || len(rows) != 1 || rows[0].CallID != "call" || rows[0].Sequence != 3 {
				t.Fatalf("%+v %v", rows, err)
			}
		})
	}
}

func TestPingUsesStudioSessionsEndpoint(t *testing.T) {
	var gotPath, gotAuthorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuthorization = r.Header.Get("Authorization")
		if r.URL.Path != "/api/studio/sessions" {
			http.Error(w, "legacy auth endpoint does not accept OAuth bearer", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
	}))
	defer server.Close()

	client := NewWithConfig(server.URL)
	client.cached = &Tokens{AccessToken: "oauth-access-token", Kind: TokenKindOAuth}

	if err := client.Ping(); err != nil {
		t.Fatalf("Ping() error = %v", err)
	}
	if gotPath != "/api/studio/sessions" {
		t.Errorf("Ping() path = %q, want %q", gotPath, "/api/studio/sessions")
	}
	if gotAuthorization != "Bearer oauth-access-token" {
		t.Errorf("authorization = %q", gotAuthorization)
	}
}

func TestPingReportsUnauthorizedStudioSessionRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/studio/sessions" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer server.Close()

	client := NewWithConfig(server.URL)
	if err := client.Ping(); err == nil || err.Error() != "not authenticated" {
		t.Fatalf("Ping() error = %v, want not authenticated", err)
	}
}
