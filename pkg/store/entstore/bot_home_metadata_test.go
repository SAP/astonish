package entstore

import (
	"context"
	"testing"
	"time"

	personalsession "github.com/SAP/astonish/ent/personal/session"
	teamsession "github.com/SAP/astonish/ent/team/session"
	"github.com/SAP/astonish/pkg/store"
)

func TestBotHomeMetadataCountsStoredRowsWithoutTranscript(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			ctx := context.Background()
			updated := time.Date(2026, 11, 27, 0, 0, 0, 0, time.UTC)
			var ss store.SessionStore
			var insert func(string)
			if scope == store.BotScopeTeam {
				client := newTeamClient(t)
				ss = &teamSessionStore{client: client}
				for _, id := range []string{"home", "empty", "other", "chat"} {
					create := client.Session.Create().SetID(id).SetUpdatedAt(updated).SetMessageCount(99)
					if id != "chat" {
						create.SetPurpose(teamsession.PurposeBotHome).SetRetention(teamsession.RetentionBot)
					}
					if _, err := create.Save(ctx); err != nil {
						t.Fatal(err)
					}
				}
				insert = func(id string) {
					if _, err := client.SessionEvent.Create().SetSessionID(id).SetEventData(map[string]any{"invalid_ciphertext": "not a transcript"}).Save(ctx); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				client := newPersonalClient(t)
				ss = &personalSessionStore{client: client}
				for _, id := range []string{"home", "empty", "other", "chat"} {
					create := client.Session.Create().SetID(id).SetUpdatedAt(updated).SetMessageCount(99)
					if id != "chat" {
						create.SetPurpose(personalsession.PurposeBotHome).SetRetention(personalsession.RetentionBot)
					}
					if _, err := create.Save(ctx); err != nil {
						t.Fatal(err)
					}
				}
				insert = func(id string) {
					if _, err := client.SessionEvent.Create().SetSessionID(id).SetEventData(map[string]any{"invalid_ciphertext": "not a transcript"}).Save(ctx); err != nil {
						t.Fatal(err)
					}
				}
			}
			insert("home")
			insert("home")
			insert("other")
			for _, test := range []struct {
				id    string
				count int
			}{{"home", 2}, {"empty", 0}, {"other", 1}, {"chat", 99}} {
				m, err := ss.GetSessionMeta(ctx, test.id)
				if err != nil {
					t.Fatal(err)
				}
				if m == nil || m.MessageCount != test.count || !m.UpdatedAt.Equal(updated) {
					t.Fatalf("%s metadata: %+v", test.id, m)
				}
			}
			m, err := ss.GetSessionMeta(ctx, "missing")
			if err != nil || m != nil {
				t.Fatalf("missing: %+v %v", m, err)
			}
		})
	}
}
