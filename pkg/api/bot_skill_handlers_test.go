package api

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

type skillHTTPStore struct {
	store.BotStore
	store.BotSkillStore
	err      error
	calls    int
	page     store.BotSkillPage
	botID    uuid.UUID
	skillID  uuid.UUID
	revision int64
}

func (s *skillHTTPStore) ListSkills(_ context.Context, b uuid.UUID, p store.BotSkillPage) (*store.BotSkillList, error) {
	s.calls++
	s.botID = b
	s.page = p
	return &store.BotSkillList{}, s.err
}
func (s *skillHTTPStore) GetSkill(_ context.Context, b, id uuid.UUID) (*store.BotSkill, error) {
	s.calls++
	s.botID = b
	s.skillID = id
	return &store.BotSkill{ID: id, BotID: b, HeadRevision: 3}, s.err
}
func (s *skillHTTPStore) GetSkillRevision(_ context.Context, b, id uuid.UUID, n int64) (*store.BotSkillRevision, error) {
	s.calls++
	s.botID = b
	s.skillID = id
	s.revision = n
	return &store.BotSkillRevision{SkillID: id, Revision: n}, s.err
}
func (s *skillHTTPStore) ListSkillRevisions(_ context.Context, b, id uuid.UUID, p store.BotSkillPage) (*store.BotSkillRevisionList, error) {
	s.calls++
	s.botID = b
	s.skillID = id
	s.page = p
	return &store.BotSkillRevisionList{}, s.err
}
func (s *skillHTTPStore) ListSkillRuns(_ context.Context, b, id uuid.UUID, p store.BotSkillPage) (*store.BotSkillRunList, error) {
	s.calls++
	s.botID = b
	s.skillID = id
	s.page = p
	return &store.BotSkillRunList{}, s.err
}

func skillHTTPContext(t *testing.T, repo store.BotStore) context.Context {
	t.Helper()
	actor := uuid.NewString()
	ctx, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: actor, OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx = store.WithTenantContext(ctx, &store.TenantContext{UserID: actor, OrgSlug: "org", TeamSlug: "team"})
	return store.WithServices(ctx, &store.Services{PersonalBots: repo})
}
func TestBotSkillHTTPBoundary(t *testing.T) {
	botID, skillID := uuid.New(), uuid.New()
	root := "/api/studio/bots/" + botID.String() + "/skills"
	item := "/" + skillID.String()
	router := mux.NewRouter()
	registerBotRoutes(router)
	for _, tc := range []struct {
		name, path, method string
		status             int
	}{
		{"list", root, "GET", 200}, {"metadata", root + item, "GET", 200}, {"head", root + item + "/head", "GET", 200},
		{"revisions", root + item + "/revisions", "GET", 200}, {"revision", root + item + "/revisions/2", "GET", 200}, {"runs", root + item + "/runs", "GET", 200},
		{"zero-limit", root + "?limit=0", "GET", 400}, {"large-limit", root + "?limit=101", "GET", 400}, {"duplicate-limit", root + "?limit=1&limit=2", "GET", 400},
		{"empty-limit", root + "?limit=", "GET", 400}, {"empty-cursor", root + "?after=", "GET", 400}, {"large-cursor", root + "?after=" + strings.Repeat("x", 201), "GET", 400},
		{"duplicate-cursor", root + "?after=x&after=y", "GET", 400}, {"archive-bool", root + "?include_archived=1", "GET", 400}, {"unknown-query", root + "?actor_id=x", "GET", 400},
		{"bad-query", root + "?limit=%zz", "GET", 400}, {"bad-bot", "/api/studio/bots/no/skills", "GET", 400}, {"bad-skill", root + "/no", "GET", 400},
		{"nil-skill", root + "/" + uuid.Nil.String(), "GET", 400}, {"bad-revision", root + item + "/revisions/0", "GET", 400}, {"overflow", root + item + "/revisions/999999999999999999999", "GET", 400},
		{"noncanonical-revision", root + item + "/revisions/+1", "GET", 400}, {"unsupported-page", root + item + "/head?limit=1", "GET", 400},
		{"bad-cursor", root + "?after=opaque", "GET", 400},
		{"nil-cursor", root + "?after=" + uuid.Nil.String(), "GET", 400},
		{"bad-revision-cursor", root + item + "/revisions?after=+1", "GET", 400},
		{"revision-page", root + item + "/revisions?after=1&limit=1", "GET", 200},
		{"post", root, "POST", 405}, {"put", root + item, "PUT", 405}, {"delete", root + item, "DELETE", 405},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &skillHTTPStore{}
			ctx := skillHTTPContext(t, repo)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil).WithContext(ctx))
			if w.Code != tc.status {
				t.Fatalf("status %d want %d: %s", w.Code, tc.status, w.Body.String())
			}
			if tc.status != 200 && repo.calls != 0 {
				t.Fatal("invalid request reached store")
			}
			if tc.status == 200 && (repo.calls == 0 || repo.botID != botID) {
				t.Fatal("store not scoped")
			}
			if tc.name == "head" && repo.revision != 3 {
				t.Fatal("wrong head revision")
			}
			if (tc.name == "list" || tc.name == "revisions" || tc.name == "runs") && strings.Contains(w.Body.String(), ":null") {
				t.Fatal("null collection")
			}
		})
	}
	for _, tc := range []struct {
		err    error
		status int
	}{{store.ErrBotForbidden, 403}, {store.ErrBotNotFound, 404}, {store.ErrBotInvalid, 400}, {store.ErrBotConflict, 409}, {store.ErrBotExpired, 410}, {store.ErrBotUnsupported, 503}, {errors.New("secret database"), 500}} {
		repo := &skillHTTPStore{err: tc.err}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", root, nil).WithContext(skillHTTPContext(t, repo)))
		if w.Code != tc.status || strings.Contains(w.Body.String(), "secret") {
			t.Fatalf("unsafe error %d %s", w.Code, w.Body.String())
		}
	}
	for _, tc := range []struct {
		ctx    context.Context
		status int
	}{{context.Background(), 401}, {skillHTTPContext(t, &botBoundaryStore{}), 503}, {skillHTTPContext(t, nil), 503}} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", root, nil).WithContext(tc.ctx))
		if w.Code != tc.status {
			t.Fatalf("status %d want %d", w.Code, tc.status)
		}
	}
	repo := &skillHTTPStore{}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", root+"?limit=2&after="+skillID.String()+"&include_archived=true", nil).WithContext(skillHTTPContext(t, repo)))
	if w.Code != 200 || repo.page.Limit != 2 || repo.page.After != skillID.String() || !repo.page.IncludeArchived {
		t.Fatal("page not forwarded")
	}
}
