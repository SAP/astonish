package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/a2a"
	"github.com/SAP/astonish/pkg/a2aserver"
	"github.com/SAP/astonish/pkg/channels"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/gorilla/mux"
)

type a2aTestValidator struct {
	principal execution.Principal
	err       error
	calls     int
}

func (v *a2aTestValidator) ValidateBearer(_ context.Context, _ string, _ string, scopes []string, surface execution.Surface) (execution.Principal, error) {
	v.calls++
	if len(scopes) != 1 || scopes[0] != a2aScope || surface != execution.SurfaceA2A {
		return execution.Principal{}, context.Canceled
	}
	return v.principal, v.err
}

type a2aTestDispatcher struct{}

func (a2aTestDispatcher) dispatch(ctx context.Context, _ channels.InboundMessage, reply func(context.Context, channels.OutboundMessage) error) error {
	return reply(ctx, channels.OutboundMessage{Text: "done"})
}

func setupA2ARouter(t *testing.T, principal execution.Principal, err error) *mux.Router {
	t.Helper()
	store := a2a.NewInMemoryTaskStore(time.Hour)
	t.Cleanup(store.Close)
	service, newErr := a2aserver.New(a2aserver.Config{TaskStore: store, BaseURL: "http://example.test", Dispatcher: a2aTestDispatcher{}.dispatch})
	if newErr != nil {
		t.Fatal(newErr)
	}
	SetA2AService(service)
	t.Cleanup(func() { SetA2AService(nil) })
	router := mux.NewRouter()
	RegisterA2ARoutes(router, &a2aTestValidator{principal: principal, err: err}, nil, nil)
	return router
}
func oauthA2APrincipal(scopes ...string) execution.Principal {
	return execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodOAuth, Surface: execution.SurfaceA2A, Subject: "user", OrgSlug: "org", TeamSlug: "team", Scopes: scopes, Authenticated: true}
}
func a2aRequest(t *testing.T) []byte {
	t.Helper()
	params, _ := json.Marshal(a2a.SendMessageParams{Message: a2a.Message{Role: "user", Parts: []a2a.Part{a2a.TextPart{Text: "hello"}}}})
	body, err := json.Marshal(a2a.JSONRPCRequest{JSONRPC: "2.0", ID: 1, Method: "message/send", Params: params})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestA2AAgentCardHandlerIsPublic(t *testing.T) {
	router := setupA2ARouter(t, oauthA2APrincipal(a2aScope), nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/.well-known/agent-card.json", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
}
func TestA2AHandlerRequiresA2AScope(t *testing.T) {
	router := setupA2ARouter(t, oauthA2APrincipal(string(execution.CapabilityChat)), nil)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/a2a", bytes.NewReader(a2aRequest(t)))
	req.Header.Set("Authorization", "Bearer token")
	router.ServeHTTP(w, req)
	var response a2a.JSONRPCResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Error == nil || response.Error.Code != a2a.ErrCodeForbidden {
		t.Fatalf("expected insufficient-scope JSON-RPC error, got status=%d body=%s", w.Code, w.Body.String())
	}
}
func TestA2AHandlerAllowsExactA2AScope(t *testing.T) {
	router := setupA2ARouter(t, oauthA2APrincipal(a2aScope), nil)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/a2a", bytes.NewReader(a2aRequest(t)))
	req.Header.Set("Authorization", "Bearer token")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
}
func TestA2AHandlerRejectsInvalidBearerBeforeDispatch(t *testing.T) {
	router := setupA2ARouter(t, oauthA2APrincipal(a2aScope), context.Canceled)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/a2a", bytes.NewReader(a2aRequest(t)))
	router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("got %d", w.Code)
	}
}
