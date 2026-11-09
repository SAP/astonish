package api

import (
	"bytes"
	"context"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

func TestBotWebhookExactAuthException(t *testing.T) {
	device, _ := NewAuthStore(t.TempDir(), time.Hour)
	pa := testPlatformAuth(t)
	for _, kind := range []string{"device", "platform"} {
		t.Run(kind, func(t *testing.T) {
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
			var h http.Handler = AuthMiddleware(NewAuthManager(device), next)
			if kind == "platform" {
				h = PlatformAuthMiddleware(pa, next)
			}
			for _, tc := range []struct {
				method, path string
				want         int
			}{
				{"POST", botWebhookIngressPath, 204},
				{"GET", botWebhookIngressPath, 401},
				{"PUT", botWebhookIngressPath, 401},
				{"POST", botWebhookIngressPath + "/", 401},
				{"POST", "/api/bot-webhooks/other", 401},
				{"POST", "/api/studio/bots/any/triggers/any/events", 401},
			} {
				req := httptest.NewRequest(tc.method, tc.path, nil)
				req.RemoteAddr = "203.0.113.1:1234"
				w := httptest.NewRecorder()
				h.ServeHTTP(w, req)
				if w.Code != tc.want {
					t.Fatalf("%s %s: %d want %d", tc.method, tc.path, w.Code, tc.want)
				}
			}
		})
	}
}

type boundedWebhookBackend struct {
	store.PlatformBackend
	called bool
}

func (b *boundedWebhookBackend) RegisterBotWebhook(context.Context, uuid.UUID, uuid.UUID, store.BotScope) (string, error) {
	return "", store.ErrBotUnsupported
}
func (b *boundedWebhookBackend) AdmitBotWebhook(context.Context, string, store.BotExternalEvent) (*store.BotRun, error) {
	b.called = true
	return nil, store.ErrBotForbidden
}

func TestBotWebhookBodyBound(t *testing.T) {
	backend := &boundedWebhookBackend{}
	router := mux.NewRouter()
	registerBotWebhookRoutes(router, backend)
	req := httptest.NewRequest("POST", botWebhookIngressPath, strings.NewReader(`{"binding_id":"`+strings.Repeat("x", 100000)+`"}`))
	req.Header.Set("X-Astonish-Signature", strings.Repeat("0", 64))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code < 400 || backend.called {
		t.Fatalf("oversized input reached resolver: status=%d called=%t", w.Code, backend.called)
	}
}

func TestBotWebhookRateAndSignatureGuard(t *testing.T) {
	router := mux.NewRouter()
	registerBotWebhookRoutes(router, nil)
	for i := 0; i < 21; i++ {
		req := httptest.NewRequest("POST", botWebhookIngressPath, bytes.NewBufferString(strings.Repeat("x", 100000)))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		want := 401
		if i == 20 {
			want = 429
		}
		if w.Code != want {
			t.Fatalf("request %d: %d", i, w.Code)
		}
	}
}
