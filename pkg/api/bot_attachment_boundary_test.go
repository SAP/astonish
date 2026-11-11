package api

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type attachmentMessageStore struct{ *botAttachmentStore }

func (s *attachmentMessageStore) AdmitMessage(context.Context, store.BotMessageRequest) (*store.BotMessageReceipt, error) {
	s.calls++
	return nil, store.ErrBotInvalid
}

func TestBotAttachmentErrorsNeverReflectInput(t *testing.T) {
	actor := uuid.NewString()
	ctx, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: actor, OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx = store.WithTenantContext(ctx, &store.TenantContext{UserID: actor, OrgSlug: "org", TeamSlug: "team"})
	for _, handler := range []struct {
		name  string
		serve http.HandlerFunc
	}{{"run", BotRequestRunHandler}, {"conversation", BotConversationHandler}, {"test", BotTestRunHandler}, {"message", BotMessageHandler}} {
		for _, mode := range []string{"base64", "empty", "mime", "count", "malformed", "extra-json"} {
			t.Run(handler.name+"/"+mode, func(t *testing.T) {
				a := ChatAttachment{Filename: "SECRET https://private.invalid I sent the payment", MimeType: "text/plain", Data: "aGVsbG8="}
				switch mode {
				case "base64":
					a.Data = "!!!"
				case "empty":
					a.Data = ""
				case "mime":
					a.MimeType = ""
				}
				atts := []ChatAttachment{a}
				if mode == "count" {
					atts = make([]ChatAttachment, 11)
				}
				raw, _ := json.Marshal(map[string]any{"version": 1, "idempotency_key": "a", "attachments": atts})
				body := string(raw)
				if mode == "malformed" {
					body = "SECRET{"
				}
				if mode == "extra-json" {
					body += " {}"
				}
				repo := &attachmentMessageStore{&botAttachmentStore{}}
				r := httptest.NewRequest("POST", "/runs?scope=team", strings.NewReader(body))
				r = r.WithContext(store.WithServices(ctx, &store.Services{Bots: repo}))
				r = mux.SetURLVars(r, map[string]string{"botID": uuid.NewString()})
				w := httptest.NewRecorder()
				handler.serve(w, r)
				if w.Code != 400 || w.Body.String() != "Invalid Bot request\n" || repo.calls != 0 {
					t.Fatalf("unsafe response/admission: %d %q calls=%d", w.Code, w.Body.String(), repo.calls)
				}
				if mode != "malformed" && mode != "extra-json" {
					if _, err := validateBotAttachments(atts); !errors.Is(err, store.ErrBotInvalid) {
						t.Fatalf("not typed: %v", err)
					}
				}
			})
		}
	}
}
