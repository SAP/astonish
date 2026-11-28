package api

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type botSessionFixtureStore struct {
	store.BotStore
	b      store.Bot
	run    store.BotRun
	denied bool
}

func (s *botSessionFixtureStore) Get(context.Context, store.BotID) (*store.Bot, error) {
	if s.denied {
		return nil, store.ErrBotForbidden
	}
	return &s.b, nil
}
func (s *botSessionFixtureStore) GetRun(context.Context, store.BotID, store.RunID) (*store.BotRun, error) {
	return &s.run, nil
}

type botSessionMetaFixture struct {
	store.SessionStore
	meta *store.SessionMeta
	err  error
}

func (s *botSessionMetaFixture) GetSessionMeta(context.Context, string) (*store.SessionMeta, error) {
	return s.meta, s.err
}
func TestBotSessionResolver(t *testing.T) {
	actor, owner, id, rid, sid := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	ctx, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: actor.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx = store.WithTenantContext(ctx, &store.TenantContext{UserID: actor.String(), OrgSlug: "org", TeamSlug: "team"})
	repo := &botSessionFixtureStore{b: store.Bot{ID: id, OwnerID: owner, Scope: store.BotScopeTeam, HomeSessionID: sid}, run: store.BotRun{ID: rid, BotID: id, SessionID: sid}}
	meta := store.SessionMeta{ID: sid.String(), UserID: owner.String(), BotID: id.String(), Purpose: store.SessionPurposeBotHome, Retention: store.SessionRetentionBot}
	ss := &botSessionMetaFixture{meta: &meta}
	svc := &store.Services{Bots: repo, Sessions: ss}
	get := httptest.NewRequest("GET", "/", nil).WithContext(ctx)
	if resolveSessionStore(svc, sid.String(), get) != ss {
		t.Fatal("team member read denied")
	}
	if resolveSessionStore(svc, sid.String()) != nil {
		t.Fatal("request-less Bot read")
	}
	post := httptest.NewRequest("POST", "/", nil).WithContext(ctx)
	if resolveSessionStore(svc, sid.String(), post) != nil {
		t.Fatal("generic mutation allowed")
	}
	meta.Purpose = store.SessionPurposeBotRun
	meta.BotRunID = rid.String()
	if resolveSessionStore(svc, sid.String(), get) != ss {
		t.Fatal("run read denied")
	}
	repo.run.SessionID = uuid.New()
	if resolveSessionStore(svc, sid.String(), get) != nil {
		t.Fatal("foreign run session allowed")
	}
	repo.run.SessionID = sid
	repo.denied = true
	if resolveSessionStore(svc, sid.String(), get) != nil {
		t.Fatal("revoked membership allowed")
	}
	repo.denied = false
	meta.Retention = ""
	if resolveSessionStore(svc, sid.String(), get) != nil {
		t.Fatal("partial linkage allowed")
	}
	meta.Retention = store.SessionRetentionBot
	svc.PersonalSessions = &botSessionMetaFixture{meta: &store.SessionMeta{ID: sid.String(), UserID: uuid.NewString()}}
	if resolveSessionStore(svc, sid.String(), get) != nil {
		t.Fatal("private denial fell through to team")
	}
	svc.PersonalSessions = &botSessionMetaFixture{err: store.ErrBotUnsupported}
	if resolveSessionStore(svc, sid.String(), get) != nil {
		t.Fatal("private error fell through to team")
	}
}
