package entstore

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"entgo.io/ent/dialect/sql/schema"

	platformmigrate "github.com/SAP/astonish/ent/platform/migrate"
	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/fleet"
	"github.com/SAP/astonish/pkg/session"
	"github.com/SAP/astonish/pkg/store"
)

// --- platformDB interface methods (daemon-level) ---

// NewToolVectorStore creates a ToolVectorStore for semantic tool discovery.
// Uses the in-memory implementation (brute-force cosine). Returns (nil, nil)
// if the embedding function is not configured.
func (s *Store) NewToolVectorStore(ctx context.Context) (agent.ToolVectorStore, error) {
	if s.embedFunc == nil {
		return nil, nil
	}
	embedFn := func(ctx context.Context, text string) ([]float32, error) {
		return s.embedFunc(ctx, text)
	}
	return agent.NewInMemoryToolVectorStore(embedFn)
}

// NewThreadIndex creates a thread indexer for email session routing.
// Uses the file-based thread index backed by the data directory.
func (s *Store) NewThreadIndex() session.ThreadIndexer {
	path := filepath.Join(s.dataDir, "thread_index.json")
	return session.NewThreadIndex(path)
}

// NewLinkCodeStore creates a link code store for channel verification.
func (s *Store) NewLinkCodeStore() store.LinkCodeStore {
	return s.LinkCodes()
}

// NewMonitorStateStore creates a monitor state store for fleet plan monitors.
func (s *Store) NewMonitorStateStore(orgSlug, teamSlug string) fleet.MonitorStateStore {
	dir := filepath.Join(s.dataDir, "fleet_state", orgSlug, teamSlug)
	return fleet.NewFileMonitorStateStore(dir)
}

// TeamSchemaName returns the conventional schema/database name for a team.
// Used by API handlers when creating teams.
func TeamSchemaName(teamSlug string) string {
	return "team_" + teamSlug
}

// OrgDBName returns the conventional database name for an organization.
// Matches config.OrgDBName: astonish_{suffix}_{sanitized_slug} (with suffix)
// or astonish_org_{sanitized_slug} (legacy, no suffix).
func OrgDBName(instanceSuffix, orgSlug string) string {
	safe := strings.ReplaceAll(orgSlug, "-", "_")
	if instanceSuffix == "" {
		return "astonish_org_" + safe
	}
	return "astonish_" + instanceSuffix + "_" + safe
}

// NewPlatformServices creates a Services instance for platform mode.
// This is the unified constructor that replaces pgstore.NewPlatformServices
// and sqlitestore.NewPlatformServices.
func NewPlatformServices(ctx context.Context, cfg Config) (*store.Services, *Store, error) {
	s, err := New(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}

	// Auto-migrate platform schema.
	// For SQLite: run full Schema.Create (creates all tables — safe, no type conflicts).
	// For PostgreSQL: scope to specific tables to avoid Ent trying to reconcile
	// pre-existing column types on other tables (e.g. platform_mcp_servers id →
	// uuid cast issue on existing deployments).
	if s.dialect == DialectSQLite {
		if err := s.platformClient.Schema.Create(ctx); err != nil {
			s.Close()
			return nil, nil, fmt.Errorf("auto-migrate sqlite platform: %w", err)
		}
	} else {
		migrateTables := []*schema.Table{
			platformmigrate.PlatformSkillsTable,
			platformmigrate.PlatformSkillFilesTable,
			platformmigrate.PlatformSlideTemplatesTable,
			platformmigrate.SandboxLayersTable, // required by SandboxTemplatesTable FK
			platformmigrate.SandboxTemplatesTable,
		}
		if err := platformmigrate.Create(ctx, s.platformClient.Schema, migrateTables); err != nil {
			s.Close()
			return nil, nil, fmt.Errorf("auto-migrate platform tables: %w", err)
		}
	}

	// Seed platform defaults for SQLite (e.g. @base sandbox template).
	if s.dialect == DialectSQLite {
		if err := s.applySQLiteExtras(ctx, ScopePlatform, s.platformDB); err != nil {
			s.Close()
			return nil, nil, fmt.Errorf("apply sqlite platform extras: %w", err)
		}
	}

	svc := &store.Services{
		Mode:             store.ModePlatform,
		Platform:         s,
		TenantRouter:     s,
		PlatformSettings: s.PlatformSettings(),
	}

	return svc, s, nil
}

// TenantMiddleware is an HTTP middleware that resolves the per-tenant stores
// for each request. It reads the TenantContext (set by auth middleware) and
// creates a request-scoped Services clone with the correct org/team/personal
// stores populated.
func TenantMiddleware(s *Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			baseSvc := store.FromRequest(r)
			if baseSvc == nil || baseSvc.Mode != store.ModePlatform {
				next.ServeHTTP(w, r)
				return
			}

			tc := store.TenantContextFrom(r.Context())
			if tc == nil || tc.OrgSlug == "" {
				next.ServeHTTP(w, r)
				return
			}

			// Resolve org data store
			orgStore, err := s.ForOrg(tc.OrgSlug)
			if err != nil {
				http.Error(w, "failed to resolve organization store", http.StatusInternalServerError)
				return
			}

			// Create a request-scoped clone with tenant-specific stores
			reqSvc := &store.Services{
				Mode:                    store.ModePlatform,
				Platform:                baseSvc.Platform,
				TenantRouter:            baseSvc.TenantRouter,
				Audit:                   orgStore.OrgAudit(),
				Skills:                  orgStore.OrgSkills(),
				OrgSlideTemplates:       orgStore.OrgSlideTemplates(),
				MCPServers:              orgStore.OrgMCPServers(),
				A2AAgents:               orgStore.OrgA2AAgents(),
				PlatformMCPServers:      s.PlatformMCPServers(),
				PlatformSkills:          s.PlatformSkills(),
				PlatformSlideTemplates:  s.PlatformSlideTemplates(),
				PlatformSettings:        s.PlatformSettings(),
				OrgSettings:             s.OrgSettings(tc.OrgSlug),
				NetworkPolicies:         orgStore.OrgNetworkPolicies(),
				PlatformNetworkPolicies: s.PlatformNetworkPolicies(),
			}

			// Populate team-scoped stores if team is known
			if tc.TeamSlug != "" {
				teamStore := orgStore.ForTeam(tc.TeamSlug)
				if teamStore == nil {
					http.Error(w, `{"error":"team database unavailable"}`, http.StatusServiceUnavailable)
					return
				}
				reqSvc.Sessions = teamStore.Sessions()
				reqSvc.Memory = teamStore.Memories()
				reqSvc.Credentials = teamStore.Credentials()
				reqSvc.Docs = teamStore.Docs()
				reqSvc.Apps = teamStore.Apps()
				reqSvc.Flows = teamStore.Flows()
				reqSvc.Scheduler = teamStore.ScheduledJobs()
				reqSvc.FleetTemplates = teamStore.FleetTemplates()
				reqSvc.FleetPlans = teamStore.FleetPlans()
				reqSvc.FleetSetupProfiles = teamStore.FleetSetupProfiles()
				reqSvc.FleetSetupDrafts = teamStore.FleetSetupDrafts()
				reqSvc.FleetRunStates = teamStore.FleetRunStates()
				reqSvc.FleetMailbox = teamStore.FleetMailbox()
				reqSvc.FleetTaskBoard = teamStore.FleetTaskBoard()
				reqSvc.DrillReports = teamStore.DrillReports()
				reqSvc.TeamSkills = teamStore.Skills()
				reqSvc.TeamMCPServers = teamStore.MCPServers()
				reqSvc.TeamA2AAgents = teamStore.A2AAgents()
				reqSvc.TeamNetworkPolicies = teamStore.NetworkPolicies()
				reqSvc.Settings = teamStore.Settings()
				reqSvc.AppState = teamStore.AppState()
				reqSvc.AppStateSQL = teamStore.AppStateSQL()

				// Wire personal stores
				if tc.UserID != "" {
					personalStore := orgStore.ForUser(tc.UserID)
					if personalStore == nil {
						http.Error(w, `{"error":"personal database unavailable"}`, http.StatusServiceUnavailable)
						return
					}
					reqSvc.PersonalSessions = personalStore.Sessions()
					reqSvc.PersonalDocs = personalStore.Docs()
					reqSvc.PersonalFlows = personalStore.Flows()
					reqSvc.PersonalApps = personalStore.Apps()
					reqSvc.PersonalCredentials = personalStore.Credentials()
					reqSvc.PersonalScheduler = personalStore.ScheduledJobs()
					reqSvc.PersonalSettings = personalStore.PersonalSettings()
				}

				// Build three-tier memory searcher
				var personalMem store.MemoryStore
				if tc.UserID != "" {
					if ps := orgStore.ForUser(tc.UserID); ps != nil {
						personalMem = ps.Memories()
					}
				}
				reqSvc.MemorySearcher = store.NewThreeTierSearcher(store.ThreeTierMemoryStoreConfig{
					Personal: personalMem,
					Team:     teamStore.Memories(),
					Org:      orgStore.OrgMemories(),
					Embed:    s.embedFunc,
				})
			}

			// Inject into request context
			ctx := store.WithServices(r.Context(), reqSvc)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// MigrateAllSchemas runs pending migrations on all existing org databases.
// Placeholder — in the Ent-based store, schema creation is handled by Ent's
// Schema.Create() during provisioning. This is kept for interface compatibility.
func (s *Store) MigrateAllSchemas(ctx context.Context) error {
	// In Ent mode, schemas are auto-created during provisioning.
	// Future: iterate existing org DBs and run Schema.Create.
	return nil
}
