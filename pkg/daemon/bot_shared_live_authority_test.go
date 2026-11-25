package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/chatruntime"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/adk/tool/toolconfirmation"
)

// These tests exercise production owner-service composition and shared dispatch,
// not scheduler admission, lease policy, physical compute or provider execution.
func TestBotWorkerSharedLiveAuthority(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, revokeTeam := range []bool{false, true} {
			name := "organization"
			if revokeTeam {
				name = "team"
			}
			t.Run(string(scope)+"/"+name, func(t *testing.T) {
				ctx := t.Context()
				must := func(err error) {
					t.Helper()
					if err != nil {
						t.Fatal(err)
					}
				}
				dir := t.TempDir()
				_, db, err := entstore.NewPlatformServices(ctx, entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
				must(err)
				t.Cleanup(func() { _ = db.Close() })
				org := &store.Organization{ID: uuid.NewString(), Slug: "authority", Name: "Authority", Status: "active"}
				must(db.Organizations().Create(ctx, org))
				must(db.ProvisionOrg(ctx, org.ID, org.Slug))
				ods, err := db.ForOrg(org.Slug)
				must(err)
				team := &store.Team{ID: uuid.NewString(), Slug: "team", Name: "Team", SchemaName: "team_team"}
				must(ods.Teams().CreateTeam(ctx, team))
				owner := uuid.New()
				must(db.Users().Create(ctx, &store.User{ID: owner.String(), Email: "authority@example.test", DisplayName: "Owner", Status: "active"}))
				must(db.Organizations().AddMember(ctx, owner.String(), org.ID, "member"))
				membership := &store.TeamMembership{UserID: owner.String(), TeamID: team.ID, Role: "member"}
				must(ods.Teams().AddMember(ctx, membership))
				w := &BotWorker{config: BotWorkerConfig{Backend: db, WorkerID: "authority-worker"}}
				ownerCtx, _, err := w.authenticateOwner(ctx, BotSchedulerScope{OrgSlug: org.Slug, TeamSlug: team.Slug, UserID: owner.String()})
				must(err)
				principal, ok := execution.PrincipalFromContext(ownerCtx)
				if !ok {
					t.Fatal("worker did not authenticate owner")
				}
				scoped, err := w.loadScope(ownerCtx, bot.RunRequest{Principal: principal, Bot: store.Bot{OwnerID: owner, Scope: scope}})
				must(err)
				defer scoped.Cleanup()
				if scoped.Services.Mode != store.ModePlatform || scoped.Services.Platform != db || scoped.Services.TenantRouter != db {
					t.Fatal("worker owner services lack authoritative platform and tenant router")
				}
				if scope == store.BotScopeTeam && scoped.Services.PersonalBots != nil {
					t.Fatal("team worker opened private Bot repository")
				}
				cfg := &config.AppConfig{}
				pool := &workerAuthorityPool{t: t}
				inputs, err := chatruntime.Build(ownerCtx, chatruntime.BuildOptions{Principal: principal, Services: scoped.Services, ResourceScope: chatruntime.ResourceScope(scope), AppConfig: cfg, Pool: pool, FallbackPolicy: chatruntime.FallbackForbidden})
				must(err)
				t.Cleanup(func() { _ = inputs.Close() })
				calls := 0
				probe, err := functiontool.New(functiontool.Config{Name: "worker_authority_probe", Description: "Count a controlled dispatch"}, func(c tool.Context, _ struct{}) (map[string]any, error) {
					services := store.FromContext(c)
					if services == nil {
						t.Fatal("missing bound services")
					}
					if scope == store.BotScopeTeam && (services.TenantRouter != nil || services.PersonalBots != nil || services.PersonalSessions != nil) {
						t.Fatal("team tool received private routing capability")
					}
					p, ok := execution.PrincipalFromContext(c)
					if !ok || p.Subject != principal.Subject || p.OrgSlug != principal.OrgSlug || p.TeamSlug != principal.TeamSlug {
						t.Fatal("dispatch changed owner")
					}
					calls++
					return map[string]any{"calls": calls}, nil
				})
				must(err)
				sid := uuid.NewString()
				runtime, err := launcher.NewScopedChatRuntime(ownerCtx, launcher.ChatRuntimeDependencies{Inputs: inputs, Config: &launcher.ChatFactoryConfig{AppConfig: cfg}, LLM: provider.NewPlaceholderLLM(), Sessions: scoped.Sessions, SessionID: sid, Resources: &launcher.ChatCatalogResources{Pool: pool}}, launcher.ChatRuntimeHooks{AdditionalTools: []tool.Tool{probe}})
				must(err)
				t.Cleanup(runtime.Cleanup)
				var target interface {
					Run(tool.Context, any) (map[string]any, error)
				}
				for _, candidate := range runtime.InternalTools {
					if candidate.Name() == probe.Name() {
						target, ok = candidate.(interface {
							Run(tool.Context, any) (map[string]any, error)
						})
						if !ok {
							t.Fatal("probe not executable")
						}
					}
				}
				if target == nil {
					t.Fatal("shared catalog omitted probe")
				}
				call := workerAuthorityToolContext{base: ownerCtx, sid: sid, uid: owner.String()}
				_, err = target.Run(call, map[string]any{})
				must(err)
				if calls != 1 {
					t.Fatal("positive dispatch did not reach executable")
				}
				if revokeTeam {
					must(ods.Teams().RemoveMember(ctx, owner.String(), team.ID))
				} else {
					must(db.Organizations().RemoveMember(ctx, owner.String(), org.ID))
				}
				_, err = target.Run(call, map[string]any{})
				if !errors.Is(err, chatruntime.ErrLiveAuthorityDenied) {
					t.Fatalf("revoked dispatch: %v", err)
				}
				if calls != 1 {
					t.Fatal("revoked dispatch reached executable")
				}
				if revokeTeam {
					must(ods.Teams().AddMember(ctx, membership))
				} else {
					must(db.Organizations().AddMember(ctx, owner.String(), org.ID, "member"))
				}
				_, err = target.Run(call, map[string]any{})
				must(err)
				if calls != 2 {
					t.Fatal("same executable did not recover after membership restoration")
				}
			})
		}
	}
}

type workerAuthorityPool struct {
	sandbox.ToolNodePool
	t *testing.T
}

func (p *workerAuthorityPool) GetBackend() sandbox.Backend { return nil }
func (p *workerAuthorityPool) GetOrCreate(string) sandbox.ToolNodeClient {
	p.t.Fatal("authority probe must not provision compute")
	return nil
}

type workerAuthorityToolContext struct {
	tool.Context
	base     context.Context
	sid, uid string
}

func (c workerAuthorityToolContext) Deadline() (time.Time, bool)                        { return c.base.Deadline() }
func (c workerAuthorityToolContext) Done() <-chan struct{}                              { return c.base.Done() }
func (c workerAuthorityToolContext) Err() error                                         { return c.base.Err() }
func (c workerAuthorityToolContext) Value(key any) any                                  { return c.base.Value(key) }
func (workerAuthorityToolContext) ToolConfirmation() *toolconfirmation.ToolConfirmation { return nil }
func (c workerAuthorityToolContext) SessionID() string                                  { return c.sid }
func (c workerAuthorityToolContext) UserID() string                                     { return c.uid }
