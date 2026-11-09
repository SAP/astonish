package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

type setupHomeBackend struct {
	sandbox.Backend
	calls  int
	before func()
}

func (s *setupHomeBackend) EnsureBotHome(_ context.Context, scope sandbox.BotHomeScope) (*sandbox.BotHomeBinding, error) {
	s.calls++
	if s.before != nil {
		s.before()
	}
	return &sandbox.BotHomeBinding{Scope: scope, Backend: "controlled", Ref: "home-" + scope.BotID}, nil
}

// Real HTTP routes and tenant middleware, SQLite and the daemon's installed
// provisioner. Storage backend is controlled; this does not claim live Docker.
func TestBotHomeSetupHTTPConfiguredDaemon(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	svc, db, err := entstore.NewPlatformServices(ctx, entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	org := &store.Organization{ID: uuid.NewString(), Slug: "home-setup", Name: "Home", Status: "active"}
	if err = db.Organizations().Create(ctx, org); err != nil {
		t.Fatal(err)
	}
	if err = db.ProvisionOrg(ctx, org.ID, org.Slug); err != nil {
		t.Fatal(err)
	}
	ods, err := db.ForOrg(org.Slug)
	if err != nil {
		t.Fatal(err)
	}
	team := &store.Team{ID: uuid.NewString(), Slug: "team", Name: "Team", SchemaName: "team_team"}
	if err = ods.Teams().CreateTeam(ctx, team); err != nil {
		t.Fatal(err)
	}
	owner, reader, admin := uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, id := range []string{owner, reader, admin} {
		if err = db.Users().Create(ctx, &store.User{ID: id, Email: id + "@test.invalid", DisplayName: "Tester", Status: "active"}); err != nil {
			t.Fatal(err)
		}
		if err = db.Organizations().AddMember(ctx, id, org.ID, "member"); err != nil {
			t.Fatal(err)
		}
		role := "member"
		if id == reader {
			role = "viewer"
		}
		if id == admin {
			role = "admin"
		}
		if err = ods.Teams().AddMember(ctx, &store.TeamMembership{UserID: id, TeamID: team.ID, Role: role}); err != nil {
			t.Fatal(err)
		}
	}
	backend := &setupHomeBackend{}
	worker := &BotWorker{config: BotWorkerConfig{Backend: db, Sandbox: backend, WorkerID: "home-test"}}
	svc.BotHomeSetup = worker.provisionHome
	router := mux.NewRouter()
	api.RegisterRoutes(router, svc, db, entstore.TenantMiddleware(db))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Test-Actor")
		c := r.Context()
		if id != "" {
			c, err = execution.WithPrincipal(c, execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: id, OrgSlug: org.Slug, TeamSlug: team.Slug, Authenticated: true})
			if err != nil {
				t.Error(err)
				return
			}
			c = store.WithTenantContext(c, &store.TenantContext{OrgSlug: org.Slug, TeamSlug: team.Slug, UserID: id})
		}
		router.ServeHTTP(w, r.WithContext(c))
	}))
	defer server.Close()
	request := func(method, path, actor, body string, status int) store.Bot {
		t.Helper()
		r, _ := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		r.Header.Set("X-Test-Actor", actor)
		r.Header.Set("Content-Type", "application/json")
		resp, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != status {
			t.Fatalf("%s %s got %d want %d", method, path, resp.StatusCode, status)
		}
		var b store.Bot
		if status == 200 || status == 201 {
			if err = json.NewDecoder(resp.Body).Decode(&b); err != nil {
				t.Fatal(err)
			}
		}
		return b
	}
	for _, scope := range []string{"personal", "team"} {
		t.Run(scope, func(t *testing.T) {
			suffix := "?scope=" + scope
			b := request("POST", "/api/studio/bots"+suffix, owner, fmt.Sprintf(`{"name":"Home","idempotency_key":%q,"definition":{"charter":"Do not run tools"}}`, scope), 201)
			path := "/api/studio/bots/" + b.ID.String() + "/home"
			request("POST", path+suffix, "", `{"expected_revision":1}`, 401)
			denied := 403
			if scope == "personal" {
				denied = 404
			}
			request("POST", path+suffix, reader, `{"expected_revision":1}`, denied)
			other := "team"
			if scope == "team" {
				other = "personal"
			}
			request("POST", path+"?scope="+other, owner, `{"expected_revision":1}`, 404)
			request("POST", path+suffix, owner, `{"expected_revision":1,"storage_key":"/tmp/forged"}`, 400)
			request("POST", path+suffix, owner, `{"expected_revision":9}`, 409)
			if backend.calls != 0 {
				t.Fatal("backend called before authorization")
			}
			saved := svc.BotHomeSetup
			svc.BotHomeSetup = nil
			request("POST", path+suffix, owner, `{"expected_revision":1}`, 503)
			svc.BotHomeSetup = saved
			actor := owner
			if scope == "team" {
				actor = admin
			}
			provisioned := request("POST", path+suffix, actor, `{"expected_revision":1}`, 200)
			if provisioned.Status != store.BotDraft || provisioned.Revision != 2 || provisioned.Home.ID == uuid.Nil || provisioned.Version != b.Version {
				t.Fatalf("unsafe setup: %+v", provisioned)
			}
			again := request("POST", path+suffix, owner, `{"expected_revision":1}`, 200)
			if again.Home != provisioned.Home || again.Revision != 2 {
				t.Fatal("retry mutated home")
			}
			request("POST", path+suffix, owner, `{"expected_revision":2}`, 200)
			request("POST", "/api/studio/bots/"+b.ID.String()+"/versions"+suffix, owner, `{"expected_revision":2,"change_reason":"edit after setup","definition":{"charter":"Changed after home"}}`, 200)
			request("POST", path+suffix, owner, `{"expected_revision":2}`, 409)
			// Mutating configuration during the backend call must lose the registration CAS.
			race := request("POST", "/api/studio/bots"+suffix, owner, fmt.Sprintf(`{"name":"Race","idempotency_key":%q,"definition":{"charter":"Inert"}}`, scope+"-race"), 201)
			backend.before = func() {
				backend.before = nil
				request("POST", "/api/studio/bots/"+race.ID.String()+"/versions"+suffix, owner, `{"expected_revision":1,"change_reason":"concurrent edit","definition":{"charter":"Changed"}}`, 200)
			}
			request("POST", "/api/studio/bots/"+race.ID.String()+"/home"+suffix, owner, `{"expected_revision":1}`, 409)
			for _, change := range []string{"owner-team", "owner-org", "owner-inactive", "owner-deleted", "caller-team", "caller-org", "caller-inactive", "caller-deleted", "caller-demoted"} {
				if scope == "personal" && strings.HasPrefix(change, "caller-") {
					continue
				}
				t.Run(change, func(t *testing.T) {
					revoked := request("POST", "/api/studio/bots"+suffix, owner, fmt.Sprintf(`{"name":"Revoked","idempotency_key":%q,"definition":{"charter":"Inert"}}`, scope+change), 201)
					target := owner
					if strings.HasPrefix(change, "caller-") {
						target = actor
					}
					user, e := db.Users().GetByID(ctx, target)
					if e != nil {
						t.Fatal(e)
					}
					backend.before = func() {
						backend.before = nil
						var e error
						switch {
						case strings.HasSuffix(change, "-team"):
							e = ods.Teams().RemoveMember(ctx, target, team.ID)
						case strings.HasSuffix(change, "-org"):
							e = db.Organizations().RemoveMember(ctx, target, org.ID)
						case strings.HasSuffix(change, "-inactive"):
							user.Status = "suspended"
							e = db.Users().Update(ctx, user)
						case strings.HasSuffix(change, "-deleted"):
							e = db.Organizations().RemoveMember(ctx, target, org.ID)
							if e == nil {
								e = db.Users().Delete(ctx, target)
							}
						case strings.HasSuffix(change, "-demoted"):
							e = ods.Teams().SetRole(ctx, target, team.ID, "viewer")
						}
						if e != nil {
							t.Error(e)
						}
					}
					beforeCalls := backend.calls
					request("POST", "/api/studio/bots/"+revoked.ID.String()+"/home"+suffix, actor, `{"expected_revision":1}`, 403)
					if backend.calls != beforeCalls+1 {
						t.Fatal("revocation test did not reach EnsureBotHome")
					}
					// Restore authority only to inspect persisted state and isolate cases.
					user.Status = "active"
					if strings.HasSuffix(change, "-deleted") {
						e = db.Users().Create(ctx, user)
					} else {
						e = db.Users().Update(ctx, user)
					}
					if e != nil {
						t.Fatal(e)
					}
					if strings.HasSuffix(change, "-org") || strings.HasSuffix(change, "-deleted") {
						if e = db.Organizations().AddMember(ctx, target, org.ID, "member"); e != nil {
							t.Fatal(e)
						}
					}
					role := "member"
					if target == admin {
						role = "admin"
					}
					if strings.HasSuffix(change, "-team") {
						if e = ods.Teams().AddMember(ctx, &store.TeamMembership{UserID: target, TeamID: team.ID, Role: role}); e != nil {
							t.Fatal(e)
						}
					}
					if e = ods.Teams().SetRole(ctx, target, team.ID, role); e != nil {
						t.Fatal(e)
					}
					unchanged := request("GET", "/api/studio/bots/"+revoked.ID.String()+suffix, actor, "", 200)
					if unchanged.Revision != 1 || unchanged.Home.ID != uuid.Nil {
						t.Fatalf("revocation registered home: %+v", unchanged)
					}
				})
			}
			// Only explicit execution controls may prepare a missing home. Mere
			// messages, forbidden requests and stale activation do not provision.
			for _, action := range []string{"test-run", "activate"} {
				auto := request("POST", "/api/studio/bots"+suffix, owner, fmt.Sprintf(`{"name":"Automatic home","idempotency_key":%q,"definition":{"charter":"Inert"}}`, scope+action), 201)
				autoPath := "/api/studio/bots/" + auto.ID.String()
				before := backend.calls
				request("POST", autoPath+"/messages"+suffix, reader, `{"version":1,"instructions":"hello","idempotency_key":"denied"}`, denied)
				request("POST", autoPath+"/activate"+suffix, owner, `{"expected_revision":99,"reason":"stale"}`, 409)
				if backend.calls != before {
					t.Fatal("non-execution or stale request provisioned home")
				}
				body := `{"version":1,"instructions":"Explicit test","idempotency_key":"test"}`
				status := 202
				db.SetBotActivationReadiness(func(context.Context, store.Bot, store.BotVersion) error { return nil })
				if action == "activate" {
					body = `{"expected_revision":1,"reason":"Explicit activation"}`
					status = 200
				}
				request("POST", autoPath+"/"+action+suffix, owner, body, status)
				after := request("GET", autoPath+suffix, owner, "", 200)
				want := store.BotDraft
				if action == "activate" {
					want = store.BotActive
				}
				if backend.calls != before+1 || after.Home.ID == uuid.Nil || after.Status != want {
					t.Fatalf("automatic %s setup: calls=%d before=%d bot=%+v", action, backend.calls, before, after)
				}
				db.SetBotActivationReadiness(nil)
			}
			for _, op := range []store.BotControlOperation{store.BotControlTaskOnceOperation, store.BotControlActivate} {
				t.Run("proposal-"+string(op), func(t *testing.T) {
					auto := request("POST", "/api/studio/bots"+suffix, owner, fmt.Sprintf(`{"name":"Proposal home","idempotency_key":%q,"definition":{"charter":"Inert"}}`, scope+"-proposal-"+string(op)), 201)
					db.SetBotActivationReadiness(func(context.Context, store.Bot, store.BotVersion) error { return nil })
					pctx, e := execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner, OrgSlug: org.Slug, TeamSlug: team.Slug, Authenticated: true})
					if e != nil {
						t.Fatal(e)
					}
					repo := ods.ForTeam(team.Slug).Bots()
					if scope == "personal" {
						repo = ods.ForUser(owner).Bots()
					}
					source, e := repo.RequestRun(pctx, store.BotRunRequest{BotID: auto.ID, Version: auto.Version, Intent: store.BotIntentDraftConversation, Trigger: store.TriggerManual, Instructions: "propose only", IdempotencyKey: "source"})
					if e != nil {
						t.Fatal(e)
					}
					wctx := execution.WithBotRunIdentity(pctx, execution.BotRunIdentity{BotID: auto.ID, RunID: source.ID, Version: auto.Version, ActorID: owner, OrgSlug: org.Slug, TeamSlug: team.Slug})
					lease, e := repo.ClaimRun(wctx, store.BotClaimRequest{BotID: auto.ID, RunID: source.ID, WorkerID: "proposal", ExpiresAt: time.Now().Add(time.Minute)})
					if e != nil {
						t.Fatal(e)
					}
					payload := store.BotControlPayload{Operation: op, Reason: "explicit review"}
					if op == store.BotControlTaskOnceOperation {
						payload.TaskOnce = &store.BotControlTaskOnce{Instructions: "exact one-time task"}
					}
					proposals := repo.(store.BotControlProposalStore)
					p, e := proposals.ProposeControl(wctx, store.BotControlProposalRequest{Lease: lease, IdempotencyKey: "proposal", ExpiresAt: time.Now().Add(time.Hour), Payload: payload})
					if e != nil {
						t.Fatal(e)
					}
					expired, e := proposals.ProposeControl(wctx, store.BotControlProposalRequest{Lease: lease, IdempotencyKey: "expires-before-confirm", ExpiresAt: time.Now().Add(50 * time.Millisecond), Payload: payload})
					if e != nil {
						t.Fatal(e)
					}
					path := "/api/studio/bots/" + auto.ID.String() + "/proposals/" + p.ID.String() + "/decision" + suffix
					body := fmt.Sprintf(`{"digest":%q,"decision":"confirm"}`, p.Digest)
					before := backend.calls
					request("POST", path, owner, body, 409)
					if backend.calls != before {
						t.Fatal("nonterminal source allocated home")
					}
					if e = repo.CommitRun(wctx, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: store.BotReport{Summary: "review ready"}}); e != nil {
						t.Fatal(e)
					}
					db.SetBotActivationReadiness(func(_ context.Context, b store.Bot, _ store.BotVersion) error {
						if b.Home.ID == uuid.Nil {
							return store.ErrBotUnsupported
						}
						return nil
					})
					time.Sleep(time.Until(expired.ExpiresAt) + time.Millisecond)
					request("POST", "/api/studio/bots/"+auto.ID.String()+"/proposals/"+expired.ID.String()+"/decision"+suffix, owner, fmt.Sprintf(`{"digest":%q,"decision":"confirm"}`, expired.Digest), 200)
					expiredReceipt, e := proposals.ReadControlProposal(pctx, auto.ID, expired.ID)
					if e != nil || expiredReceipt.Status != store.BotControlExpired || backend.calls != before {
						t.Fatalf("expiry allocated or applied: %+v %v", expiredReceipt, e)
					}
					request("POST", path, owner, `{"digest":"wrong","decision":"confirm"}`, 409)
					request("POST", path, reader, body, denied)
					if backend.calls != before {
						t.Fatal("invalid confirmation allocated home")
					}
					// Live revocation must fail before storage allocation, even with a
					// valid saved digest and an already terminal proposal source.
					if e = ods.Teams().RemoveMember(ctx, owner, team.ID); e != nil {
						t.Fatal(e)
					}
					request("POST", path, owner, body, 403)
					if e = ods.Teams().AddMember(ctx, &store.TeamMembership{UserID: owner, TeamID: team.ID, Role: "member"}); e != nil {
						t.Fatal(e)
					}
					if backend.calls != before {
						t.Fatal("revoked confirmation allocated home")
					}
					// A failed provisioner leaves the proposal unapplied and admits no task.
					savedSetup := svc.BotHomeSetup
					svc.BotHomeSetup = func(context.Context, store.BotStore, store.BotID, int64) (*store.Bot, error) {
						return nil, store.ErrBotUnsupported
					}
					request("POST", path, owner, body, 503)
					svc.BotHomeSetup = savedSetup
					pending, e := proposals.ReadControlProposal(pctx, auto.ID, p.ID)
					if e != nil || pending.Status != store.BotControlProposed {
						t.Fatalf("failed setup receipt: %+v %v", pending, e)
					}
					unchanged, e := repo.ListRuns(pctx, auto.ID, store.BotPage{Limit: 100})
					if e != nil || len(unchanged) != 1 {
						t.Fatalf("failed setup admitted task: %+v %v", unchanged, e)
					}
					request("POST", path, owner, body, 200)
					got, e := proposals.ReadControlProposal(pctx, auto.ID, p.ID)
					if e != nil || got.Status != store.BotControlApplied || got.Digest != p.Digest {
						t.Fatalf("receipt: %+v %v", got, e)
					}
					request("POST", path, owner, body, 200)
					if backend.calls != before+1 {
						t.Fatal("home missing or duplicate provisioning")
					}
					b, _ := repo.Get(pctx, auto.ID)
					if b.Home.ID == uuid.Nil {
						t.Fatal("home missing")
					}
					runs, e := repo.ListRuns(pctx, auto.ID, store.BotPage{Limit: 100})
					if e != nil {
						t.Fatal(e)
					}
					if op == store.BotControlTaskOnceOperation && (b.Status != store.BotDraft || len(runs) != 2) {
						t.Fatalf("one-time admission: %+v %+v", b, runs)
					}
					db.SetBotActivationReadiness(nil)
				})
			}
			backend.calls = 0
		})
	}
}
