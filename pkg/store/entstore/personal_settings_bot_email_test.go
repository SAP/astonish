package entstore

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestPersonalSettingsBotEmailRoundTripScopeAndClear(t *testing.T) {
	client := newPersonalClient(t)
	ctx := context.Background()
	owner := &personalSettingsStore{client: client, userID: uuid.NewString()}
	other := &personalSettingsStore{client: client, userID: uuid.NewString()}
	want := &store.PersonalSettings{DefaultProvider: "controlled", DefaultModel: "model", BrandTheme: "nova", BotEmail: &store.BotEmailSettings{Provider: "imap", Address: "owner@example.com", SMTPServer: "127.0.0.1:1234", Credential: "mail"}}
	raw, err := json.Marshal(want)
	if err != nil || !strings.Contains(string(raw), `"credential":"mail"`) {
		t.Fatal("mailbox must serialize a scoped credential reference", err)
	}
	for _, key := range []string{`"password"`, `"token"`, `"access_token"`, `"refresh_token"`} {
		if strings.Contains(string(raw), key) {
			t.Fatal("mailbox settings contain secret material field", key)
		}
	}
	if err := owner.Save(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := owner.Get(ctx)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("personal mailbox config silently dropped: got=%+v err=%v", got, err)
	}
	foreign, err := other.Get(ctx)
	if err != nil || foreign.BotEmail != nil {
		t.Fatalf("cross-owner mailbox: %+v %v", foreign, err)
	}
	// Detached read: caller mutation cannot mutate persisted settings.
	got.BotEmail.Address = "mutated@example.com"
	got, err = owner.Get(ctx)
	if err != nil || got.BotEmail.Address != want.BotEmail.Address {
		t.Fatal("read alias", err)
	}
	want.BotEmail = nil
	if err = owner.Save(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err = owner.Get(ctx)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("clear: %+v %v", got, err)
	}
}
