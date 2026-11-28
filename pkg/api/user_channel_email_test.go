package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

type channelEmailBackend struct {
	store.PlatformBackend
	user *store.User
	err  error
}
type channelEmailUsers struct {
	store.UserStore
	b *channelEmailBackend
}

func (b *channelEmailBackend) Users() store.UserStore { return channelEmailUsers{b: b} }
func (u channelEmailUsers) GetByID(context.Context, string) (*store.User, error) {
	return u.b.user, u.b.err
}

func TestChannelLinkEmailLiveAccount(t *testing.T) {
	for _, name := range []string{"account", "changed", "explicit", "missing", "inactive", "foreign", "lookup-error", "header", "list"} {
		t.Run(name, func(t *testing.T) {
			b := &channelEmailBackend{user: &store.User{ID: "owner", Email: "Owner@Example.Test", Status: "active"}}
			supplied := ""
			want := "owner@example.test"
			switch name {
			case "changed":
				b.user.Email = "new@example.test"
				want = b.user.Email
			case "explicit":
				supplied = "Other@Example.Test"
				want = "other@example.test"
			case "missing":
				b.user.Email = ""
			case "inactive":
				b.user.Status = "suspended"
			case "foreign":
				b.user.ID = "other"
			case "lookup-error":
				b.err = errors.New("unavailable")
			case "header":
				supplied = "owner@example.test\r\nBcc: other@example.test"
			case "list":
				supplied = "owner@example.test, other@example.test"
			}
			got, err := channelLinkEmail(context.Background(), b, "owner", supplied)
			if name == "account" || name == "changed" || name == "explicit" {
				if err != nil || got != want {
					t.Fatalf("got %q %v", got, err)
				}
			} else if err == nil || got != "" {
				t.Fatalf("unsafe recipient %q %v", got, err)
			}
		})
	}
	if _, err := channelLinkEmail(context.Background(), nil, "owner", ""); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatal(err)
	}
}

func TestChannelLinkEmailHTTPRejectsMissingAccount(t *testing.T) {
	oldBackend, oldCodes := platformBackendInstance, GetLinkCodeStore()
	platformBackendInstance = &channelEmailBackend{user: &store.User{ID: "owner", Status: "active"}}
	SetLinkCodeStore(NewLinkCodeStore())
	t.Cleanup(func() { platformBackendInstance = oldBackend; SetLinkCodeStore(oldCodes) })
	r := httptest.NewRequest(http.MethodPost, "/api/user/channels/link-code", strings.NewReader(`{"channel_type":"email"}`))
	r = r.WithContext(WithPlatformUser(r.Context(), &PlatformUser{ID: "owner", Email: "stale@example.test"}))
	w := httptest.NewRecorder()
	handleGenerateLinkCode(w, r)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "account email") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}
