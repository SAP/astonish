package chatruntime

import (
	"context"
	"errors"
	"fmt"
	"iter"

	"github.com/SAP/astonish/pkg/agent"
	"google.golang.org/adk/model"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
)

type liveAuthorityFixture struct {
	db    *entstore.Store
	org   *store.Organization
	user  *store.User
	team  *store.Team
	teams store.TeamManagementStore
	opts  BuildOptions
}

func newLiveAuthorityFixture(t *testing.T, scope ResourceScope, teamResources bool) liveAuthorityFixture {
	t.Helper()
	dir := t.TempDir()
	_, db, err := entstore.NewPlatformServices(t.Context(), entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	org := &store.Organization{ID: uuid.NewString(), Slug: "authority-org", Name: "Authority", Status: "active"}
	user := &store.User{ID: uuid.NewString(), Email: "authority@example.test", DisplayName: "Authority", Status: "active"}
	mustLiveAuthority(t, db.Organizations().Create(t.Context(), org))
	mustLiveAuthority(t, db.ProvisionOrg(t.Context(), org.ID, org.Slug))
	mustLiveAuthority(t, db.Users().Create(t.Context(), user))
	mustLiveAuthority(t, db.Organizations().AddMember(t.Context(), user.ID, org.ID, "member"))
	ods, err := db.ForOrg(org.Slug)
	mustLiveAuthority(t, err)
	team := &store.Team{ID: uuid.NewString(), Slug: "authority-team", Name: "Authority", SchemaName: "team_authority"}
	mustLiveAuthority(t, ods.Teams().CreateTeam(t.Context(), team))
	mustLiveAuthority(t, ods.Teams().AddMember(t.Context(), &store.TeamMembership{UserID: user.ID, TeamID: team.ID, Role: "member"}))
	svc := &store.Services{Mode: store.ModePlatform, Platform: db, TenantRouter: db}
	if teamResources {
		svc.Credentials = unavailableCredentials{}
	}
	p := execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: user.ID, OrgSlug: org.Slug, TeamSlug: team.Slug, Roles: []string{"member"}, Scopes: []string{"chat"}, Authenticated: true}
	return liveAuthorityFixture{db: db, org: org, user: user, team: team, teams: ods.Teams(), opts: BuildOptions{Principal: p, Services: svc, ResourceScope: scope, AppConfig: &config.AppConfig{}, FallbackPolicy: FallbackForbidden}}
}

func mustLiveAuthority(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestLiveAuthorityBuildAndBindFreshMembership(t *testing.T) {
	for _, scope := range []ResourceScope{ResourceScopePersonal, ResourceScopeTeam} {
		for _, change := range []string{"user-disabled", "org-disabled", "org-removed", "team-removed"} {
			t.Run(string(scope)+"/"+change, func(t *testing.T) {
				f := newLiveAuthorityFixture(t, scope, true)
				in, err := Build(t.Context(), f.opts)
				mustLiveAuthority(t, err)
				defer in.Close()
				_, err = in.Bind(t.Context())
				mustLiveAuthority(t, err)
				var restore func()
				switch change {
				case "user-disabled":
					f.user.Status = "suspended"
					mustLiveAuthority(t, f.db.Users().Update(t.Context(), f.user))
					restore = func() { f.user.Status = "active"; mustLiveAuthority(t, f.db.Users().Update(t.Context(), f.user)) }
				case "org-disabled":
					f.org.Status = "suspended"
					mustLiveAuthority(t, f.db.Organizations().Update(t.Context(), f.org))
					restore = func() { f.org.Status = "active"; mustLiveAuthority(t, f.db.Organizations().Update(t.Context(), f.org)) }
				case "org-removed":
					mustLiveAuthority(t, f.db.Organizations().RemoveMember(t.Context(), f.user.ID, f.org.ID))
					restore = func() {
						mustLiveAuthority(t, f.db.Organizations().AddMember(t.Context(), f.user.ID, f.org.ID, "member"))
					}
				case "team-removed":
					mustLiveAuthority(t, f.teams.RemoveMember(t.Context(), f.user.ID, f.team.ID))
					restore = func() {
						mustLiveAuthority(t, f.teams.AddMember(t.Context(), &store.TeamMembership{UserID: f.user.ID, TeamID: f.team.ID, Role: "member"}))
					}
				}
				if _, err := in.Bind(t.Context()); !errors.Is(err, ErrLiveAuthorityDenied) {
					t.Fatalf("revoked Bind: %v", err)
				}
				if _, err := Build(t.Context(), f.opts); !errors.Is(err, ErrLiveAuthorityDenied) {
					t.Fatalf("revoked Build: %v", err)
				}
				restore()
				bound, err := in.Bind(t.Context())
				mustLiveAuthority(t, err)
				got, _ := execution.PrincipalFromContext(bound)
				if !reflect.DeepEqual(got, f.opts.Principal) {
					t.Fatal("principal ceiling changed")
				}
				if scope == ResourceScopeTeam && store.FromContext(bound).TenantRouter != nil {
					t.Fatal("team tools received authority router")
				}
			})
		}
	}
}

func TestLiveAuthorityPersonalOnlyDoesNotRequireTeamMembership(t *testing.T) {
	f := newLiveAuthorityFixture(t, ResourceScopePersonal, false)
	mustLiveAuthority(t, f.teams.RemoveMember(t.Context(), f.user.ID, f.team.ID))
	in, err := Build(t.Context(), f.opts)
	mustLiveAuthority(t, err)
	defer in.Close()
	_, err = in.Bind(t.Context())
	mustLiveAuthority(t, err)
}

func TestLiveAuthorityPromotionDoesNotBroadenCeilingOrBypassTeam(t *testing.T) {
	f := newLiveAuthorityFixture(t, ResourceScopePersonal, true)
	in, err := Build(t.Context(), f.opts)
	mustLiveAuthority(t, err)
	defer in.Close()
	mustLiveAuthority(t, f.db.Organizations().SetMemberRole(t.Context(), f.user.ID, f.org.ID, "owner"))
	mustLiveAuthority(t, f.db.Users().SetPlatformRole(t.Context(), f.user.ID, "superadmin"))
	bound, err := in.Bind(t.Context())
	mustLiveAuthority(t, err)
	got, _ := execution.PrincipalFromContext(bound)
	if !reflect.DeepEqual(got, f.opts.Principal) {
		t.Fatal("directory promotion broadened captured authority")
	}
	mustLiveAuthority(t, f.db.Organizations().SetMemberRole(t.Context(), f.user.ID, f.org.ID, "member"))
	mustLiveAuthority(t, f.db.Users().SetPlatformRole(t.Context(), f.user.ID, ""))
	_, err = in.Bind(t.Context())
	mustLiveAuthority(t, err) // temporary promotion did not raise the baseline
	mustLiveAuthority(t, f.db.Organizations().SetMemberRole(t.Context(), f.user.ID, f.org.ID, "owner"))
	mustLiveAuthority(t, f.db.Users().SetPlatformRole(t.Context(), f.user.ID, "superadmin"))
	mustLiveAuthority(t, f.teams.RemoveMember(t.Context(), f.user.ID, f.team.ID))
	if _, err := in.Bind(t.Context()); !errors.Is(err, ErrLiveAuthorityDenied) {
		t.Fatalf("admin bypass: %v", err)
	}
}

func TestLiveAuthorityMissingDependenciesFailClosed(t *testing.T) {
	f := newLiveAuthorityFixture(t, ResourceScopePersonal, false)
	for _, fallback := range []FallbackPolicy{FallbackForbidden, FallbackAllowed} {
		for _, missing := range []string{"platform", "router", "both"} {
			t.Run(missing+"/"+string(rune('0'+fallback)), func(t *testing.T) {
				o := f.opts
				s := *o.Services
				o.Services, o.FallbackPolicy = &s, fallback
				if missing != "router" {
					s.Platform = nil
				}
				if missing != "platform" {
					s.TenantRouter = nil
				}
				if _, err := Build(t.Context(), o); !errors.Is(err, ErrLiveAuthorityDenied) {
					t.Fatalf("missing authority accepted: %v", err)
				}
			})
		}
	}
	// Even a local principal cannot relabel an incomplete platform composition.
	o := f.opts
	o.Principal, o.Services = testPrincipal(), &store.Services{Mode: store.ModePlatform}
	if _, err := Build(t.Context(), o); !errors.Is(err, ErrLiveAuthorityDenied) {
		t.Fatalf("local platform exemption: %v", err)
	}
	// Real local Code carries no platform dependencies and remains unaffected.
	o.Services = nil
	o.FallbackPolicy = FallbackAllowed
	o.MCPConfig = &config.MCPConfig{}
	in, err := Build(t.Context(), o)
	mustLiveAuthority(t, err)
	defer in.Close()
	_, err = in.Bind(t.Context())
	mustLiveAuthority(t, err)
}

func TestLiveAuthoritySnapshotCannotBeRetargeted(t *testing.T) {
	f := newLiveAuthorityFixture(t, ResourceScopeTeam, true)
	in, err := Build(t.Context(), f.opts)
	mustLiveAuthority(t, err)
	defer in.Close()
	// Removing dependencies from either the original container or a returned
	// binding cannot turn off the private, captured directory check.
	f.opts.Services.Mode = ""
	f.opts.Services.Platform, f.opts.Services.TenantRouter = nil, nil
	bound, err := in.Bind(t.Context())
	mustLiveAuthority(t, err)
	store.FromContext(bound).Platform = nil
	mustLiveAuthority(t, f.teams.RemoveMember(t.Context(), f.user.ID, f.team.ID))
	if _, err := in.Bind(t.Context()); !errors.Is(err, ErrLiveAuthorityDenied) {
		t.Fatalf("mutated services bypassed captured authority: %v", err)
	}
}

func TestLiveAuthorityContextModelRechecksAtIteration(t *testing.T) {
	for _, scope := range []ResourceScope{ResourceScopePersonal, ResourceScopeTeam} {
		for _, change := range []string{"org-demoted", "owner-demoted", "team-demoted", "platform-demoted", "team-removed"} {
			t.Run(string(scope)+"/"+change, func(t *testing.T) {
				f := newLiveAuthorityFixture(t, scope, true)
				orgRole := "admin"
				if change == "owner-demoted" {
					orgRole = "owner"
				}
				mustLiveAuthority(t, f.db.Organizations().SetMemberRole(t.Context(), f.user.ID, f.org.ID, orgRole))
				mustLiveAuthority(t, f.teams.SetRole(t.Context(), f.user.ID, f.team.ID, "admin"))
				mustLiveAuthority(t, f.db.Users().SetPlatformRole(t.Context(), f.user.ID, "superadmin"))
				f.opts.Principal.Roles = []string{"superadmin", orgRole, "admin"}
				raw := &liveAuthorityModel{}
				f.opts.LLM = raw
				in, err := Build(t.Context(), f.opts)
				mustLiveAuthority(t, err)
				defer in.Close()
				bound, err := in.Bind(t.Context())
				mustLiveAuthority(t, err)
				selected := agent.LLMFromContext(bound)
				if selected == nil {
					t.Fatal("missing context model")
				}
				for _, err := range selected.GenerateContent(bound, &model.LLMRequest{}, false) {
					mustLiveAuthority(t, err)
				}
				if raw.calls != 1 {
					t.Fatalf("positive call count: %d", raw.calls)
				}
				// Creating the lazy sequence is not authorization to invoke it later.
				seq := selected.GenerateContent(bound, &model.LLMRequest{}, false)
				switch change {
				case "org-demoted":
					mustLiveAuthority(t, f.db.Organizations().SetMemberRole(t.Context(), f.user.ID, f.org.ID, "member"))
				case "owner-demoted":
					mustLiveAuthority(t, f.db.Organizations().SetMemberRole(t.Context(), f.user.ID, f.org.ID, "admin"))
				case "team-demoted":
					mustLiveAuthority(t, f.teams.SetRole(t.Context(), f.user.ID, f.team.ID, "member"))
				case "platform-demoted":
					mustLiveAuthority(t, f.db.Users().SetPlatformRole(t.Context(), f.user.ID, ""))
				case "team-removed":
					mustLiveAuthority(t, f.teams.RemoveMember(t.Context(), f.user.ID, f.team.ID))
				}
				if _, err := in.Bind(t.Context()); !errors.Is(err, ErrLiveAuthorityDenied) {
					t.Errorf("demoted Bind: %v", err)
				}
				denied := false
				for response, err := range seq {
					if response != nil || !errors.Is(err, ErrLiveAuthorityDenied) {
						t.Fatalf("context model bypass: response=%v err=%v", response, err)
					}
					denied = true
				}
				if !denied || raw.calls != 1 {
					t.Fatalf("revoked provider invoked: denied=%v calls=%d", denied, raw.calls)
				}
				mustLiveAuthority(t, f.db.Organizations().SetMemberRole(t.Context(), f.user.ID, f.org.ID, orgRole))
				mustLiveAuthority(t, f.db.Users().SetPlatformRole(t.Context(), f.user.ID, "superadmin"))
				if change == "team-removed" {
					mustLiveAuthority(t, f.teams.AddMember(t.Context(), &store.TeamMembership{UserID: f.user.ID, TeamID: f.team.ID, Role: "admin"}))
				} else {
					mustLiveAuthority(t, f.teams.SetRole(t.Context(), f.user.ID, f.team.ID, "admin"))
				}
				for _, err := range selected.GenerateContent(bound, &model.LLMRequest{}, true) {
					mustLiveAuthority(t, err)
				}
				if raw.calls != 2 {
					t.Fatalf("restored call count: %d", raw.calls)
				}
				got, _ := execution.PrincipalFromContext(bound)
				if !reflect.DeepEqual(got, f.opts.Principal) {
					t.Fatal("captured ceiling changed")
				}
			})
		}
	}
}

type liveAuthorityModel struct{ calls int }

func (*liveAuthorityModel) Name() string { return "live-authority-fixture" }
func (m *liveAuthorityModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) { m.calls++; yield(&model.LLMResponse{}, nil) }
}

func TestLiveAuthorityModelPreservesBudgetCancellationAndLocal(t *testing.T) {
	for _, platform := range []bool{false, true} {
		t.Run(fmt.Sprint(platform), func(t *testing.T) {
			f := newLiveAuthorityFixture(t, ResourceScopeTeam, true)
			if !platform {
				f.opts.Services = &store.Services{}
			}
			raw := &liveAuthorityModel{}
			budget := &liveAuthorityBudget{}
			metered, err := provider.NewBudgetLLM(raw, budget, provider.BudgetLLMOptions{MaxOutputTokens: 10, SupportsOutputLimit: true})
			mustLiveAuthority(t, err)
			f.opts.LLM = metered
			in, err := Build(t.Context(), f.opts)
			mustLiveAuthority(t, err)
			defer in.Close()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			bound, err := in.Bind(ctx)
			mustLiveAuthority(t, err)
			selected := agent.LLMFromContext(bound)
			if selected.Name() != raw.Name() {
				t.Fatal("model name changed")
			}
			if in.GuardModel(selected) != selected {
				t.Fatal("guard not idempotent")
			}
			if in.GuardModel(nil) != nil || (*Inputs)(nil).GuardModel(nil) != nil {
				t.Fatal("nil model handling")
			}
			for _, err := range selected.GenerateContent(bound, &model.LLMRequest{}, false) {
				mustLiveAuthority(t, err)
			}
			if raw.calls != 1 || budget.reservations != 1 || budget.settlements != 1 {
				t.Fatalf("lost budget accounting: calls=%d budget=%+v", raw.calls, budget)
			}
			budget.err = errors.New("budget exhausted")
			for _, err := range selected.GenerateContent(bound, &model.LLMRequest{}, false) {
				if !errors.Is(err, budget.err) {
					t.Fatalf("lost budget denial: %v", err)
				}
			}
			if raw.calls != 1 {
				t.Fatal("budget bypass")
			}
			budget.err = nil
			seq := selected.GenerateContent(bound, &model.LLMRequest{}, false)
			cancel()
			denied := false
			for _, err := range seq {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("lost cancellation: %v", err)
				}
				denied = true
			}
			if !denied || raw.calls != 1 || budget.reservations != 1 {
				t.Fatal("canceled call reached budget/provider")
			}
		})
	}
}

type liveAuthorityBudget struct {
	execution.RunBudget
	reservations, settlements int
	err                       error
}

func (b *liveAuthorityBudget) Check(context.Context) error { return b.err }
func (b *liveAuthorityBudget) ReserveModel(_ context.Context, r execution.BudgetReservation) (execution.BudgetReservation, error) {
	b.reservations++
	r.ID = uuid.New()
	return r, nil
}
func (b *liveAuthorityBudget) SettleModel(context.Context, uuid.UUID, execution.BudgetUsage) error {
	b.settlements++
	return nil
}

func TestLiveAuthorityDirectoryFailureAndCancellation(t *testing.T) {
	f := newLiveAuthorityFixture(t, ResourceScopeTeam, true)
	in, err := Build(t.Context(), f.opts)
	mustLiveAuthority(t, err)
	defer in.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := in.Bind(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	mustLiveAuthority(t, f.db.Close())
	if _, err := in.Bind(t.Context()); !errors.Is(err, ErrLiveAuthorityDenied) {
		t.Fatalf("closed directory: %v", err)
	}
}
