package entstore

import (
	"context"
	"reflect"
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

// Personal settings must use the same replacement/clear semantics as team
// settings, without borrowing a team mailbox or storing credential material.
func TestTeamSettingsBotEmailRoundTripDefaultsAndClear(t *testing.T) {
	ctx := context.Background()
	settings := &teamSettingsStore{client: newTeamClient(t)}
	want := &store.TeamSettings{BotEmail: &store.BotEmailSettings{Provider: "imap", Address: "owner@example.com", SMTPServer: "127.0.0.1:1234", Credential: "mail"}}
	if err := settings.Save(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := settings.Get(ctx)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("team mailbox round trip: %v", err)
	}
	got.BotEmail.Address = "mutated@example.com"
	got, err = settings.Get(ctx)
	if err != nil || got.BotEmail.Address != want.BotEmail.Address {
		t.Fatal("mailbox read aliases persisted configuration", err)
	}
	want.BotEmail = nil
	if err := settings.Save(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err = settings.Get(ctx)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("team mailbox clear changed defaults: %v", err)
	}
}
