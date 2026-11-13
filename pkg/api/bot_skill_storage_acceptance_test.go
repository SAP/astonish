package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"entgo.io/ent/dialect/sql/schema"
	personalmigrate "github.com/SAP/astonish/ent/personal/migrate"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

// This is a storage/HTTP gate, not a model-quality or live-daemon gate. Mutations
// use the trusted service contract with a real admitted run and fenced lease;
// reads traverse production routes and tenant middleware over a loopback socket.
type skillStorageHTTPFixture struct {
	t             *testing.T
	cfg           entstore.Config
	db            *entstore.Store
	base          *store.Services
	owner, member string
	org           *store.Organization
	team          *store.Team
	server        *httptest.Server
}

func newSkillStorageHTTPFixture(t *testing.T) *skillStorageHTTPFixture {
	t.Helper()
	dir := t.TempDir()
	f := &skillStorageHTTPFixture{t: t, cfg: entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir}, owner: uuid.NewString(), member: uuid.NewString(), org: &store.Organization{ID: uuid.NewString(), Slug: "skills-http", Name: "Skills HTTP", Status: "active"}, team: &store.Team{ID: uuid.NewString(), Slug: "team-a", Name: "Team A", SchemaName: "team_team-a"}}
	f.open()
	ctx := context.Background()
	if err := f.db.Organizations().Create(ctx, f.org); err != nil {
		t.Fatal(err)
	}
	if err := f.db.ProvisionOrg(ctx, f.org.ID, f.org.Slug); err != nil {
		t.Fatal(err)
	}
	ods, err := f.db.ForOrg(f.org.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if err := ods.Teams().CreateTeam(ctx, f.team); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{f.owner, f.member} {
		if err := f.db.Users().Create(ctx, &store.User{ID: id, Email: id + "@example.test", DisplayName: "Skills tester", Status: "active"}); err != nil {
			t.Fatal(err)
		}
		if err := f.db.Organizations().AddMember(ctx, id, f.org.ID, "member"); err != nil {
			t.Fatal(err)
		}
		if err := ods.Teams().AddMember(ctx, &store.TeamMembership{UserID: id, TeamID: f.team.ID, Role: "member"}); err != nil {
			t.Fatal(err)
		}
	}
	f.start()
	t.Cleanup(func() { f.server.Close(); _ = f.db.Close() })
	return f
}
func (f *skillStorageHTTPFixture) open() {
	f.t.Helper()
	var err error
	f.base, f.db, err = entstore.NewPlatformServices(context.Background(), f.cfg)
	if err != nil {
		f.t.Fatal(err)
	}
	// Controlled readiness proves storage, not infrastructure availability.
	f.db.SetBotActivationReadiness(func(context.Context, store.Bot, store.BotVersion) error { return nil })
}
func (f *skillStorageHTTPFixture) context(actor string) context.Context {
	f.t.Helper()
	ctx, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: actor, OrgSlug: f.org.Slug, TeamSlug: f.team.Slug, Authenticated: true})
	if err != nil {
		f.t.Fatal(err)
	}
	return store.WithTenantContext(ctx, &store.TenantContext{UserID: actor, OrgSlug: f.org.Slug, TeamSlug: f.team.Slug})
}
func (f *skillStorageHTTPFixture) start() {
	router := mux.NewRouter()
	RegisterRoutes(router, f.base, f.db, entstore.TenantMiddleware(f.db))
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor := r.Header.Get("X-Test-Actor")
		ctx := r.Context()
		if actor != "" {
			ctx = f.context(actor)
			ctx = context.WithValue(ctx, platformUserKey{}, &PlatformUser{ID: actor, OrgSlug: f.org.Slug, TeamSlug: f.team.Slug, Role: "member"})
		}
		router.ServeHTTP(w, r.WithContext(ctx))
	}))
}
func (f *skillStorageHTTPFixture) reopen() {
	f.server.Close()
	if err := f.db.Close(); err != nil {
		f.t.Fatal(err)
	}
	f.open()
	f.start()
}
func (f *skillStorageHTTPFixture) repo(scope store.BotScope) store.BotStore {
	f.t.Helper()
	ods, err := f.db.ForOrg(f.org.Slug)
	if err != nil {
		f.t.Fatal(err)
	}
	if scope == store.BotScopePersonal {
		p := ods.ForUser(f.owner)
		if p == nil {
			f.t.Fatal("personal store unavailable")
		}
		return p.Bots()
	}
	return ods.ForTeam(f.team.Slug).Bots()
}
func (f *skillStorageHTTPFixture) skills(scope store.BotScope) store.BotSkillStore {
	f.t.Helper()
	s, ok := f.repo(scope).(store.BotSkillStore)
	if !ok {
		f.t.Fatal("scoped store lacks BotSkillStore")
	}
	return s
}
func (f *skillStorageHTTPFixture) request(method, path, actor, body string, status int) []byte {
	f.t.Helper()
	q, err := http.NewRequest(method, f.server.URL+path, strings.NewReader(body))
	if err != nil {
		f.t.Fatal(err)
	}
	q.Header.Set("X-Test-Actor", actor)
	q.Header.Set("Content-Type", "application/json")
	response, err := f.server.Client().Do(q)
	if err != nil {
		f.t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		f.t.Fatal(err)
	}
	if response.StatusCode != status {
		f.t.Fatalf("%s %s: %d want %d: %s", method, path, response.StatusCode, status, raw)
	}
	return raw
}
func skillPath(b *store.Bot, suffix string) string {
	return "/api/studio/bots/" + b.ID.String() + suffix + "?scope=" + string(b.Scope)
}
func skillDecode[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}
func (f *skillStorageHTTPFixture) bot(scope store.BotScope) *store.Bot {
	f.t.Helper()
	raw := f.request("POST", "/api/studio/bots?scope="+string(scope), f.owner, fmt.Sprintf(`{"name":"Skill storage","idempotency_key":%q}`, uuid.NewString()), 201)
	b := skillDecode[store.Bot](f.t, raw)
	repo := f.repo(scope)
	ctx := f.context(f.owner)
	service, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindService, ClientID: "registry", Authentication: execution.AuthMethod("internal"), Surface: execution.SurfaceCode, Actor: "registry", OrgSlug: f.org.Slug, TeamSlug: f.team.Slug, Authenticated: true, Scopes: []string{entstore.BotHomeRegistrationScope}})
	if err != nil {
		f.t.Fatal(err)
	}
	home := store.BotHomeBinding{ID: uuid.New(), BotID: b.ID, Scope: scope, Backend: "controlled", StorageKey: uuid.NewString(), Revision: 1}
	if err := entstore.RegisterBotHomeBinding(service, repo, entstore.BotHomeRegistration{BotID: b.ID, ExpectedRevision: b.Revision, Home: home}, func(context.Context, entstore.BotHomeRegistration) error { return nil }); err != nil {
		f.t.Fatal(err)
	}
	updated, err := repo.Get(ctx, b.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	updated, err = repo.Activate(ctx, store.BotLifecycleRequest{BotID: b.ID, ActorID: b.OwnerID, ExpectedRevision: updated.Revision, Reason: "controlled storage test"})
	if err != nil {
		f.t.Fatal(err)
	}
	return updated
}

// pinLegacyAttempt restores only an empty historical work contract in this
// fixture's owned SQLite database before claim. No verdict, candidate or skill
// is seeded; all subsequent mutations still use the fenced production stores.
func (f *skillStorageHTTPFixture) pinLegacyAttempt(b *store.Bot, run *store.BotRun) {
	f.t.Helper()
	path := filepath.Join(f.cfg.DataDir, "orgs", f.org.Slug, "teams", f.team.Slug+".db")
	if b.Scope == store.BotScopePersonal {
		path = filepath.Join(f.cfg.DataDir, "orgs", f.org.Slug, "personal", f.owner+".db")
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rw")
	if err != nil {
		f.t.Fatal(err)
	}
	defer db.Close()
	ctx := f.context(f.owner)
	var raw []byte
	if err := db.QueryRowContext(ctx, "SELECT data FROM bot_runs WHERE id = ? AND bot_id = ? AND status = 'queued'", run.ID.String(), b.ID.String()).Scan(&raw); err != nil {
		f.t.Fatal(err)
	}
	var storage map[string]string
	if err := json.Unmarshal(raw, &storage); err != nil {
		f.t.Fatal(err)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal([]byte(storage["bot_document"]), &envelope); err != nil {
		f.t.Fatal(err)
	}
	if len(envelope["work"]) != 0 {
		f.t.Fatal("historical fixture already has work")
	}
	input, err := f.repo(b.Scope).(store.BotRuntimeStore).LoadRunInput(ctx, b.ID, run.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	work := store.NewBotWorkState(store.BotWorkContract{BotID: b.ID, RunID: run.ID, Version: run.Version, Instructions: input.Instructions, Charter: input.Version.Charter})
	if err := work.Validate(); err != nil {
		f.t.Fatal(err)
	}
	envelope["work"], err = json.Marshal(work)
	if err != nil {
		f.t.Fatal(err)
	}
	updated, err := json.Marshal(envelope)
	if err != nil {
		f.t.Fatal(err)
	}
	storage["bot_document"] = string(updated)
	updated, err = json.Marshal(storage)
	if err != nil {
		f.t.Fatal(err)
	}
	result, err := db.ExecContext(ctx, "UPDATE bot_runs SET data = ? WHERE id = ? AND bot_id = ? AND status = 'queued' AND CAST(data AS TEXT) = ?", string(updated), run.ID.String(), b.ID.String(), string(raw))
	if err != nil {
		f.t.Fatal(err)
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		f.t.Fatalf("legacy storage fixture pin: rows=%d err=%v", n, err)
	}
}

func (f *skillStorageHTTPFixture) claim(b *store.Bot, duration time.Duration) (context.Context, store.BotLease, *store.BotWorkState) {
	f.t.Helper()
	repo := f.repo(b.Scope)
	ctx := f.context(f.owner)
	run, err := repo.RequestRun(ctx, store.BotRunRequest{BotID: b.ID, Version: b.Version, Trigger: store.TriggerManual, Instructions: "Prepare a factual digest", IdempotencyKey: uuid.NewString()})
	if err != nil {
		f.t.Fatal(err)
	}
	f.pinLegacyAttempt(b, run)
	worker := execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: b.ID, RunID: run.ID, Version: b.Version, ActorID: f.owner, OrgSlug: f.org.Slug, TeamSlug: f.team.Slug})
	lease, err := repo.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: run.ID, WorkerID: "skills-test", ExpiresAt: time.Now().Add(duration)})
	if err != nil {
		f.t.Fatal(err)
	}
	work, err := repo.(store.BotWorkStore).LoadWork(worker, lease)
	if err != nil {
		f.t.Fatal(err)
	}
	return worker, lease, work
}
func (f *skillStorageHTTPFixture) draft(b *store.Bot) (store.BotSkillProvenance, *store.BotSkillRevision) {
	f.t.Helper()
	goal, err := f.repo(b.Scope).SaveGoal(f.context(f.owner), store.BotGoalUpdate{BotID: b.ID, Goal: store.BotGoal{BotID: b.ID, Title: "Digest", Description: "Prepare a factual digest", CompletionCriteria: "Cite all sources"}})
	if err != nil {
		f.t.Fatal(err)
	}
	raw, _ := json.Marshal(goal)
	hash := sha256.Sum256(raw)
	source := store.BotSkillProvenance{Level: store.BotSkillOwnerSpecified, BotID: b.ID, GoalID: &goal.ID, GoalRevision: goal.Revision, SourceDigest: hex.EncodeToString(hash[:]), Quote: goal.CompletionCriteria, RecordedAt: time.Now().UTC()}
	proposed := store.BotSkillProvenance{Level: store.BotSkillProposed, BotID: b.ID}
	revision := &store.BotSkillRevision{SchemaVersion: store.BotSkillSchemaVersion, ID: uuid.New(), BotID: b.ID, SkillID: uuid.New(), Revision: 1, Responsibility: "Prepare a factual digest", Expectations: []store.BotSkillExpectation{{ID: "sources", Text: "Cite all sources", Provenance: source}}, Method: []string{"Read sources", "Write digest with citations"}, Provenance: []store.BotSkillProvenance{proposed}, ChangeReason: "Initial owner request"}
	revision.Digest = revision.ContentDigest()
	return source, revision
}
func (f *skillStorageHTTPFixture) orientation(b *store.Bot, duration time.Duration) (context.Context, store.BotSkillOrientation) {
	f.t.Helper()
	source, revision := f.draft(b)
	worker, lease, work := f.claim(b, duration)
	return worker, store.BotSkillOrientation{Mutation: store.BotSkillMutation{BotID: b.ID, Lease: lease, ActorID: b.OwnerID, ExpectedRevision: work.Revision, IdempotencyKey: uuid.NewString(), Source: source}, Kind: store.BotSkillCreateDraft, Key: uuid.NewString(), Title: "Factual digest", Intent: "Prepare a digest", Draft: revision, GoalID: source.GoalID}
}
func (f *skillStorageHTTPFixture) create(b *store.Bot) (context.Context, store.BotSkillOrientation, *store.BotSkillOrientationResult) {
	f.t.Helper()
	worker, q := f.orientation(b, time.Minute)
	result, err := f.skills(b.Scope).OrientSkill(worker, q)
	if err != nil {
		f.t.Fatal(err)
	}
	if result == nil || result.Invocation == nil || result.Work == nil {
		f.t.Fatal("missing pinned invocation/work")
	}
	return worker, q, result
}
func (f *skillStorageHTTPFixture) readHead(b *store.Bot, id uuid.UUID) store.BotSkillRevision {
	f.t.Helper()
	return skillDecode[store.BotSkillRevision](f.t, f.request("GET", skillPath(b, "/skills/"+id.String()+"/head"), f.owner, "", 200))
}

func (f *skillStorageHTTPFixture) finish(b *store.Bot, worker context.Context, lease store.BotLease) {
	f.t.Helper()
	if err := f.repo(b.Scope).CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunPartial, Report: store.BotReport{Summary: "Storage fixture completed without production work"}}); err != nil {
		f.t.Fatal(err)
	}
}
func (f *skillStorageHTTPFixture) updateOrientation(b *store.Bot, previous store.BotSkillRevision) (context.Context, store.BotSkillOrientation) {
	f.t.Helper()
	worker, lease, work := f.claim(b, time.Minute)
	next := previous.Clone()
	next.ID = uuid.New()
	next.Revision++
	next.ParentRevision = previous.Revision
	next.ParentDigest = previous.Digest
	next.ChangeReason = "Add exact owner requirement from the saved goal"
	p := next.Expectations[0].Provenance
	p.Quote = "Prepare a factual digest"
	next.Expectations = append(next.Expectations, store.BotSkillExpectation{ID: "factual", Text: p.Quote, Provenance: p})
	next.Digest = next.ContentDigest()
	return worker, store.BotSkillOrientation{Mutation: store.BotSkillMutation{BotID: b.ID, Lease: lease, ActorID: b.OwnerID, ExpectedRevision: work.Revision, IdempotencyKey: uuid.NewString(), Source: next.Expectations[0].Provenance}, Kind: store.BotSkillUpdateOwnerExpectations, SkillID: &next.SkillID, Intent: "Prepare revised digest", Draft: next}
}

func TestBotSkillStorageHTTPAcceptance(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		f := newSkillStorageHTTPFixture(t)
		for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
			b := f.bot(scope)
			for i := 0; i < 2; i++ {
				raw := f.request("GET", skillPath(b, "/skills"), f.owner, "", 200)
				if string(raw) != "{\"skills\":[]}\n" {
					t.Fatalf("not an empty collection: %s", raw)
				}
			}
			after := skillDecode[store.Bot](t, f.request("GET", skillPath(b, ""), f.owner, "", 200))
			if after.Revision != b.Revision {
				t.Fatal("GET mutated Bot")
			}
		}
	})
	t.Run("worker-create-owner-read", func(t *testing.T) {
		f := newSkillStorageHTTPFixture(t)
		for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
			b := f.bot(scope)
			_, q, result := f.create(b)
			head := f.readHead(b, result.Invocation.SkillID)
			if head.Digest != q.Draft.Digest || head.Validate() != nil {
				t.Fatal("stored head differs")
			}
			root := "/skills/" + head.SkillID.String()
			for _, suffix := range []string{"", "/revisions", "/revisions/1", "/runs"} {
				f.request("GET", skillPath(b, root+suffix), f.owner, "", 200)
			}
			runs := skillDecode[store.BotSkillRunList](t, f.request("GET", skillPath(b, root+"/runs"), f.owner, "", 200))
			if len(runs.Runs) != 1 || runs.Runs[0].ID != q.Mutation.Lease.RunID {
				t.Fatal("run association missing")
			}
			f.request("POST", skillPath(b, "/skills"), f.owner, `{}`, 405)
			f.request("DELETE", skillPath(b, root), f.owner, "", 405)
		}
	})
	t.Run("cross-bot", func(t *testing.T) {
		f := newSkillStorageHTTPFixture(t)
		b := f.bot(store.BotScopePersonal)
		_, _, r := f.create(b)
		other := f.bot(b.Scope)
		for _, suffix := range []string{"", "/head", "/revisions", "/revisions/1", "/runs"} {
			f.request("GET", skillPath(other, "/skills/"+r.Invocation.SkillID.String()+suffix), f.owner, "", 404)
		}
		f.request("GET", skillPath(b, "/skills"), "", "", 401)
		f.request("GET", skillPath(b, "/skills"), f.member, "", 404)
	})
	t.Run("personal-team", func(t *testing.T) {
		f := newSkillStorageHTTPFixture(t)
		for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
			b := f.bot(scope)
			_, _, r := f.create(b)
			other := *b
			if scope == store.BotScopePersonal {
				other.Scope = store.BotScopeTeam
			} else {
				other.Scope = store.BotScopePersonal
			}
			f.request("GET", skillPath(&other, "/skills"), f.owner, "", 404)
			f.request("GET", skillPath(&other, "/skills/"+r.Invocation.SkillID.String()+"/head"), f.owner, "", 404)
		}
	})
	t.Run("replay-digest", func(t *testing.T) {
		f := newSkillStorageHTTPFixture(t)
		b := f.bot(store.BotScopePersonal)
		worker, q, first := f.create(b)
		again, err := f.skills(b.Scope).OrientSkill(worker, q)
		if err != nil || again.Invocation.Digest != first.Invocation.Digest || again.Work.Revision != first.Work.Revision {
			t.Fatalf("replay changed state: %v", err)
		}
		q.Title = "Different request"
		if _, err := f.skills(b.Scope).OrientSkill(worker, q); !errors.Is(err, store.ErrBotConflict) {
			t.Fatalf("changed retry accepted: %v", err)
		}
		head := f.readHead(b, first.Invocation.SkillID)
		if head.Revision != 1 {
			t.Fatal("replay added revision")
		}
	})
	t.Run("concurrent-revisions", func(t *testing.T) {
		f := newSkillStorageHTTPFixture(t)
		b := f.bot(store.BotScopePersonal)
		initialWorker, initialQ, initial := f.create(b)
		f.finish(b, initialWorker, initialQ.Mutation.Lease)
		worker, q := f.updateOrientation(b, initial.Invocation.Snapshot)
		results := make(chan error, 2)
		var wg sync.WaitGroup
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				request := q
				request.Mutation.IdempotencyKey = fmt.Sprint("concurrent-", i)
				_, err := f.skills(b.Scope).OrientSkill(worker, request)
				results <- err
			}(i)
		}
		wg.Wait()
		close(results)
		wins, conflicts := 0, 0
		for err := range results {
			if err == nil {
				wins++
			} else if errors.Is(err, store.ErrBotConflict) {
				conflicts++
			} else {
				t.Fatalf("unexpected race error: %v", err)
			}
		}
		if wins != 1 || conflicts != 1 {
			t.Fatalf("wins %d conflicts %d", wins, conflicts)
		}
		head := f.readHead(b, q.Draft.SkillID)
		if head.Revision != 2 || head.ParentDigest != initial.Invocation.Digest {
			t.Fatal("race did not commit exactly one child revision")
		}
		revisions := skillDecode[store.BotSkillRevisionList](t, f.request("GET", skillPath(b, "/skills/"+head.SkillID.String()+"/revisions")+"&limit=1", f.owner, "", 200))
		if len(revisions.Revisions) != 1 || revisions.Revisions[0].Digest != initial.Invocation.Digest || revisions.Next != "1" {
			t.Fatal("revision first page lost immutable parent")
		}
		last := skillDecode[store.BotSkillRevisionList](t, f.request("GET", skillPath(b, "/skills/"+head.SkillID.String()+"/revisions")+"&limit=1&after="+revisions.Next, f.owner, "", 200))
		if len(last.Revisions) != 1 || last.Revisions[0].Digest != head.Digest || last.Next != "" {
			t.Fatal("revision pagination failed")
		}
	})
	t.Run("expired-lease", func(t *testing.T) {
		f := newSkillStorageHTTPFixture(t)
		b := f.bot(store.BotScopePersonal)
		worker, q := f.orientation(b, 150*time.Millisecond)
		time.Sleep(time.Until(q.Mutation.Lease.ExpiresAt) + 25*time.Millisecond)
		if _, err := f.skills(b.Scope).OrientSkill(worker, q); !errors.Is(err, store.ErrBotExpired) && !errors.Is(err, store.ErrBotConflict) {
			t.Fatalf("expired mutation: %v", err)
		}
		list := skillDecode[store.BotSkillList](t, f.request("GET", skillPath(b, "/skills"), f.owner, "", 200))
		if len(list.Skills) != 0 {
			t.Fatal("expired worker wrote a skill")
		}
	})
	t.Run("pin-reopen", func(t *testing.T) {
		f := newSkillStorageHTTPFixture(t)
		b := f.bot(store.BotScopePersonal)
		worker, q, result := f.create(b)
		f.reopen()
		work, err := f.repo(b.Scope).(store.BotWorkStore).LoadWork(worker, q.Mutation.Lease)
		if err != nil {
			t.Fatal(err)
		}
		if work.Contract.Skill == nil || work.Contract.Skill.Digest != result.Invocation.Digest || work.RequirementsDigest != result.Work.RequirementsDigest {
			t.Fatal("pin lost on reopen")
		}
		head := f.readHead(b, result.Invocation.SkillID)
		if head.Digest != result.Invocation.Digest {
			t.Fatal("head lost on reopen")
		}
	})
	t.Run("quota", func(t *testing.T) {
		f := newSkillStorageHTTPFixture(t)
		b := f.bot(store.BotScopePersonal)
		worker, q := f.orientation(b, time.Minute)
		q.Draft.Method = []string{strings.Repeat("x", store.BotSkillMaxRevisionBytes)}
		q.Draft.Digest = q.Draft.ContentDigest()
		if _, err := f.skills(b.Scope).OrientSkill(worker, q); !errors.Is(err, store.ErrBotInvalid) {
			t.Fatalf("oversize accepted: %v", err)
		}
		list := skillDecode[store.BotSkillList](t, f.request("GET", skillPath(b, "/skills"), f.owner, "", 200))
		if len(list.Skills) != 0 {
			t.Fatal("quota rejection left a skill")
		}
		q.Draft.Method = []string{"Read sources", "Write digest with citations"}
		for i := 0; i <= store.BotSkillMaxSkills; i++ {
			q.Draft = q.Draft.Clone()
			q.Draft.ID = uuid.New()
			q.Draft.SkillID = uuid.New()
			q.Draft.Digest = q.Draft.ContentDigest()
			q.Key = uuid.NewString()
			q.Mutation.IdempotencyKey = uuid.NewString()
			_, err := f.skills(b.Scope).OrientSkill(worker, q)
			if i == store.BotSkillMaxSkills {
				if !errors.Is(err, store.ErrBotInvalid) {
					t.Fatalf("skill-count quota: %v", err)
				}
				break
			}
			if err != nil {
				t.Fatalf("skill %d: %v", i, err)
			}
			f.finish(b, worker, q.Mutation.Lease)
			var work *store.BotWorkState
			worker, q.Mutation.Lease, work = f.claim(b, time.Minute)
			q.Mutation.ExpectedRevision = work.Revision
		}
		list = skillDecode[store.BotSkillList](t, f.request("GET", skillPath(b, "/skills")+"&limit=100", f.owner, "", 200))
		if len(list.Skills) != store.BotSkillMaxSkills || list.Next != "" {
			t.Fatal("quota produced partial/excess skill state")
		}
	})
	t.Run("schema-upgrade", testSkillStorageSchemaUpgrade)
}

// Build the pre-skills schema through Ent's migration API, then let the real
// scoped router upgrade it. No skill or work rows are inserted through SQL.
func testSkillStorageSchemaUpgrade(t *testing.T) {
	f := newSkillStorageHTTPFixture(t)
	path := filepath.Join(f.cfg.DataDir, "orgs", f.org.Slug, "personal", f.owner+".db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		t.Fatal(err)
	}
	driver := entsql.OpenDB(dialect.SQLite, db)
	migration, err := schema.NewMigrate(driver)
	if err != nil {
		t.Fatal(err)
	}
	var oldTables []*schema.Table
	omitted := 0
	for _, table := range personalmigrate.Tables {
		if table.Name == "bot_skills" || table.Name == "bot_skill_revisions" {
			omitted++
			continue
		}
		oldTables = append(oldTables, table)
	}
	if omitted != 2 {
		t.Fatal("pre-skills fixture did not omit both skill tables")
	}
	if err := migration.Create(context.Background(), oldTables...); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('bot_skills','bot_skill_revisions')").Scan(&count); err != nil || count != 0 {
		t.Fatalf("not a pre-skills database: %d %v", count, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	b := f.bot(store.BotScopePersonal)
	_, _, result := f.create(b)
	f.reopen()
	head := f.readHead(b, result.Invocation.SkillID)
	if head.Digest != result.Invocation.Digest {
		t.Fatal("upgraded skill did not survive reopen")
	}
}
