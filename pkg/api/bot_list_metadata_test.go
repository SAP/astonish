package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type botListMetadataStore struct {
	store.BotStore
	bots []store.Bot
}

func (s *botListMetadataStore) List(context.Context, store.BotPage) ([]store.Bot, error) {
	return s.bots, nil
}

type botListMetadataSessions struct {
	store.SessionStore
	meta *store.SessionMeta
	err  error
	ids  []string
}

func (s *botListMetadataSessions) GetSessionMeta(_ context.Context, id string) (*store.SessionMeta, error) {
	s.ids = append(s.ids, id)
	return s.meta, s.err
}

func TestBotListHomeMetadata(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			owner, viewer := uuid.New(), uuid.New()
			b := store.Bot{ID: uuid.New(), HomeSessionID: uuid.New(), OwnerID: owner, Scope: scope, Name: "Home", UpdatedAt: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)}
			updated := time.Date(2026, 11, 27, 1, 2, 3, 0, time.UTC)
			valid := store.SessionMeta{ID: b.HomeSessionID.String(), BotID: b.ID.String(), UserID: owner.String(), Purpose: store.SessionPurposeBotHome, Retention: store.SessionRetentionBot, MessageCount: 3, UpdatedAt: updated}
			for _, tc := range []struct {
				name                               string
				change                             func(*store.SessionMeta)
				missing, fail, noStore, wrongScope bool
				want                               bool
			}{
				{name: "populated", want: true},
				{name: "valid zero", change: func(m *store.SessionMeta) { m.MessageCount = 0 }, want: true},
				{name: "wrong home", change: func(m *store.SessionMeta) { m.ID = uuid.NewString() }},
				{name: "wrong bot", change: func(m *store.SessionMeta) { m.BotID = uuid.NewString() }},
				{name: "wrong owner", change: func(m *store.SessionMeta) { m.UserID = viewer.String() }},
				{name: "foreign purpose", change: func(m *store.SessionMeta) { m.Purpose = store.SessionPurposeChat }},
				{name: "wrong retention", change: func(m *store.SessionMeta) { m.Retention = store.SessionRetentionStandard }},
				{name: "run session", change: func(m *store.SessionMeta) { m.BotRunID = uuid.NewString() }},
				{name: "child session", change: func(m *store.SessionMeta) { m.ParentID = uuid.NewString() }},
				{name: "missing metadata", missing: true},
				{name: "metadata error", fail: true},
				{name: "missing scoped store", noStore: true},
				{name: "wrong scope", wrongScope: true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					meta := valid
					if tc.change != nil {
						tc.change(&meta)
					}
					selected := &botListMetadataSessions{meta: &meta}
					if tc.missing {
						selected.meta = nil
					}
					if tc.fail {
						selected.err = errors.New("secret")
					}
					other := &botListMetadataSessions{meta: &valid}
					listed := b
					if tc.wrongScope {
						if scope == store.BotScopeTeam {
							listed.Scope = store.BotScopePersonal
						} else {
							listed.Scope = store.BotScopeTeam
						}
					}
					repo := &botListMetadataStore{bots: []store.Bot{listed}}
					svc := &store.Services{Bots: repo, PersonalBots: repo, PersonalSessions: selected, Sessions: other}
					if scope == store.BotScopeTeam {
						svc.Sessions = selected
						svc.PersonalSessions = other
					}
					if tc.noStore {
						if scope == store.BotScopeTeam {
							svc.Sessions = nil
						} else {
							svc.PersonalSessions = nil
						}
					}
					ctx, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: viewer.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
					if err != nil {
						t.Fatal(err)
					}
					ctx = store.WithTenantContext(ctx, &store.TenantContext{UserID: viewer.String(), OrgSlug: "org", TeamSlug: "team"})
					ctx = store.WithServices(ctx, svc)
					url := "/api/studio/bots"
					if scope == store.BotScopeTeam {
						url += "?scope=team"
					}
					w := httptest.NewRecorder()
					BotListHandler(w, httptest.NewRequest("GET", url, nil).WithContext(ctx))
					if w.Code != 200 {
						t.Fatalf("status %d: %s", w.Code, w.Body.String())
					}
					var result struct {
						Bots []map[string]json.RawMessage `json:"bots"`
					}
					if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
						t.Fatal(err)
					}
					if len(result.Bots) != 1 {
						t.Fatalf("bots: %s", w.Body.String())
					}
					row := result.Bots[0]
					_, hasCount := row["message_count"]
					_, hasDate := row["home_updated_at"]
					if hasCount != tc.want || hasDate != tc.want {
						t.Fatalf("optional metadata: %s", w.Body.String())
					}
					if tc.want {
						var count int
						var date time.Time
						_ = json.Unmarshal(row["message_count"], &count)
						_ = json.Unmarshal(row["home_updated_at"], &date)
						if count != meta.MessageCount || !date.Equal(updated) {
							t.Fatalf("metadata: %s", w.Body.String())
						}
					}
					if len(other.ids) != 0 {
						t.Fatal("cross-scope fallback")
					}
					if !tc.noStore && !tc.wrongScope && (len(selected.ids) != 1 || selected.ids[0] != b.HomeSessionID.String()) {
						t.Fatalf("lookups: %v", selected.ids)
					}
				})
			}
		})
	}
}
