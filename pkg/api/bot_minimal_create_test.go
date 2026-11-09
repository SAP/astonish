package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type minimalBotCreateStore struct {
	store.BotStore
	request store.BotCreateRequest
}

func (s *minimalBotCreateStore) CreateDraft(_ context.Context, q store.BotCreateRequest) (*store.Bot, error) {
	s.request = q
	return &store.Bot{ID: uuid.New(), Status: store.BotDraft}, nil
}
func TestBotMinimalCreateDefaults(t *testing.T) {
	for _, test := range []struct{ name, body, charter string }{
		{"false remains inert", `{"name":"New Bot","idempotency_key":"create","start_from_title":false}`, ""},
		{"absent", `{"name":"New Bot","idempotency_key":"create"}`, ""},
		{"blank", `{"name":"New Bot","idempotency_key":"create","definition":{"charter":"  "}}`, ""},
		{"explicit", `{"name":"New Bot","idempotency_key":"create","definition":{"charter":"Existing charter"}}`, "Existing charter"},
	} {
		t.Run(test.name, func(t *testing.T) {
			owner := uuid.NewString()
			ctx, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner, OrgSlug: "org", TeamSlug: "team", Authenticated: true})
			if err != nil {
				t.Fatal(err)
			}
			ctx = store.WithTenantContext(ctx, &store.TenantContext{UserID: owner, OrgSlug: "org", TeamSlug: "team"})
			repo := &minimalBotCreateStore{}
			ctx = store.WithServices(ctx, &store.Services{PersonalBots: repo})
			w := httptest.NewRecorder()
			BotCreateHandler(w, httptest.NewRequest("POST", "/api/studio/bots", strings.NewReader(test.body)).WithContext(ctx))
			if w.Code != 201 {
				t.Fatalf("create: %d %s", w.Code, w.Body.String())
			}
			got := repo.request.Version
			if test.charter != "" {
				if got.Charter != test.charter {
					t.Fatal("explicit charter changed")
				}
			} else if !strings.HasPrefix(got.Charter, "Discuss ") {
				t.Fatalf("default: %q", got.Charter)
			}
			want := store.DefaultBotVersion()
			want.Charter = got.Charter
			gotJSON, _ := json.Marshal(got)
			wantJSON, _ := json.Marshal(want)
			if string(gotJSON) != string(wantJSON) {
				t.Fatal("minimal create changed defaults beyond charter")
			}
		})
	}
}
