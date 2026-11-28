package api

import (
	"context"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"net/http/httptest"
	"strings"
	"testing"
)

type botAttachmentStore struct {
	store.BotStore
	request store.BotRunRequest
	calls   int
	deny    bool
}

func (s *botAttachmentStore) Get(context.Context, store.BotID) (*store.Bot, error) {
	if s.deny {
		return nil, store.ErrBotForbidden
	}
	return &store.Bot{}, nil
}
func (s *botAttachmentStore) RequestRun(_ context.Context, q store.BotRunRequest) (*store.BotRun, error) {
	s.request = q
	s.calls++
	return &store.BotRun{ID: uuid.New()}, nil
}
func TestBotAttachmentHTTP(t *testing.T) {
	actor := uuid.NewString()
	ctx, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: actor, OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx = store.WithTenantContext(ctx, &store.TenantContext{UserID: actor, OrgSlug: "org", TeamSlug: "team"})
	for _, tc := range []struct {
		name, data string
		deny       bool
		status     int
	}{{"valid", "aGVsbG8=", false, 202}, {"invalid", "!!!", false, 400}, {"empty", "", false, 400}, {"scope before parsing", "!!!", true, 403}} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &botAttachmentStore{deny: tc.deny}
			r := httptest.NewRequest("POST", "/runs?scope=team", strings.NewReader(`{"version":1,"idempotency_key":"a","instructions":"","attachments":[{"filename":"note.txt","mimeType":"text/plain","data":"`+tc.data+`"}]}`))
			r = r.WithContext(store.WithServices(ctx, &store.Services{Bots: repo}))
			r = mux.SetURLVars(r, map[string]string{"botID": uuid.NewString()})
			w := httptest.NewRecorder()
			BotRequestRunHandler(w, r)
			if w.Code != tc.status {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			if tc.status != 202 {
				if repo.calls != 0 {
					t.Fatal("invalid attachment admitted")
				}
				return
			}
			if len(repo.request.Attachments) != 1 || string(repo.request.Attachments[0].Data) != "hello" || repo.request.Instructions == "" {
				t.Fatalf("lost input: %+v", repo.request)
			}
			content := botAttachmentContent(repo.request.Instructions, repo.request.Attachments)
			if content == nil || content.Role != "user" || len(content.Parts) == 0 {
				t.Fatal("missing shared Chat content")
			}
		})
	}
}
