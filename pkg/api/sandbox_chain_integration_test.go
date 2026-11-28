//go:build integration

package api

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/SAP/astonish/pkg/store/pgutil"
)

// ---------------------------------------------------------------------------
// Test infrastructure: real Postgres, isolated database per test.
//
// Env: ASTONISH_TEST_DSN must point to a Postgres instance. Example:
//   export ASTONISH_TEST_DSN="postgres://postgres:554252@192.168.1.196:5432/astonish_dxmtlc_platform"
// ---------------------------------------------------------------------------

func mustTestDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("ASTONISH_TEST_DSN")
	if dsn == "" {
		t.Skip("ASTONISH_TEST_DSN not set; skipping integration test")
	}
	return dsn
}

// setupChainTestDB creates a fresh database via entstore.BootstrapPlatform,
// returns the entstore.Store, and drops the DB on test cleanup.
func setupChainTestDB(t *testing.T) *entstore.Store {
	t.Helper()
	dsn := mustTestDSN(t)
	ctx := context.Background()

	// Create a unique suffix for this test.
	suffix := fmt.Sprintf("chaintest_%s", sanitizeName(t.Name()))

	// Build the platform DSN.
	dbName := fmt.Sprintf("astonish_%s_platform", suffix)
	platformDSN, err := pgutil.ReplaceDSNDatabase(dsn, dbName)
	if err != nil {
		t.Fatalf("ReplaceDSNDatabase: %v", err)
	}

	// Drop any leftover from a prior run.
	dropTestDB(t, dsn, dbName)

	// Bootstrap.
	if err := entstore.BootstrapPlatform(ctx, entstore.Config{
		DSN:            platformDSN,
		InstanceSuffix: suffix,
	}, nil); err != nil {
		t.Fatalf("BootstrapPlatform: %v", err)
	}
	t.Cleanup(func() { dropTestDB(t, dsn, dbName) })

	// Open the store.
	_, es, err := entstore.NewPlatformServices(ctx, entstore.Config{
		DSN:            platformDSN,
		InstanceSuffix: suffix,
	})
	if err != nil {
		t.Fatalf("NewPlatformServices: %v", err)
	}
	t.Cleanup(func() { es.Close() })

	return es
}

func dropTestDB(t *testing.T, baseDSN, dbName string) {
	t.Helper()
	adminDSN, err := pgutil.ReplaceDSNDatabase(baseDSN, "postgres")
	if err != nil {
		return
	}
	conn, err := pgx.Connect(context.Background(), adminDSN)
	if err != nil {
		return
	}
	defer conn.Close(context.Background())
	_, _ = conn.Exec(context.Background(),
		fmt.Sprintf("SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '%s'", dbName))
	_, _ = conn.Exec(context.Background(), fmt.Sprintf("DROP DATABASE IF EXISTS %s", dbName))
}

func sanitizeName(name string) string {
	result := ""
	for _, c := range name {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' {
			result += string(c)
		} else if c >= 'A' && c <= 'Z' {
			result += string(c + 32)
		} else {
			result += "_"
		}
	}
	if len(result) > 40 {
		result = result[:40]
	}
	return result
}

// helpers to construct store pointers
func strPtr(s string) *string { return &s }

// ---------------------------------------------------------------------------
// Integration test: 4 scenarios
//
// These validate the full layer-chain resolution pipeline against a real
// Postgres instance with the actual schema and seed data. Each test uses
// its own isolated database.
//
// The scenarios mirror the user acceptance matrix:
//   1. Fresh install, no team template      → backend default ["@base"]
//   2. Configured base, no team template    → ["@base", configuredTop]
//   3. Configured base + team template      → ["@base", configuredTop, teamTop]
//   4. Fresh install + team template        → ["@base", teamTop]
// ---------------------------------------------------------------------------

func TestChainScenario1_FreshInstall_NoTeam(t *testing.T) {
	es := setupChainTestDB(t)
	ctx := context.Background()

	tplStore := es.SandboxTemplates()

	// After bootstrap, @base.top_layer_id = '@base' (sentinel).
	// resolveBaseLayerChain must return nil (sentinel filtered).
	baseChain := resolveBaseLayerChainWith(ctx, tplStore)
	if baseChain != nil {
		t.Errorf("Scenario 1: resolveBaseLayerChain = %v, want nil", baseChain)
	}

	// No team template exists → resolveTemplateLayerChain must return nil.
	teamChain := resolveTemplateLayerChainWith(ctx, tplStore, "team-general")
	if teamChain != nil {
		t.Errorf("Scenario 1: resolveTemplateLayerChain = %v, want nil", teamChain)
	}
}

func TestChainScenario2_ConfiguredBase_NoTeam(t *testing.T) {
	es := setupChainTestDB(t)
	ctx := context.Background()

	tplStore := es.SandboxTemplates()
	layers := es.SandboxLayers()

	// Simulate Configure Base: insert a new layer, update @base's top_layer_id.
	configuredTop := "aabbccdd1234567890abcdef1234567890abcdef1234567890abcdef12345678"
	if err := layers.PutLayer(ctx, &store.SandboxLayer{
		LayerID:    configuredTop,
		CephFSPath: "/mnt/astonish-layers/" + configuredTop,
		SizeBytes:  1000,
	}); err != nil {
		t.Fatalf("PutLayer: %v", err)
	}
	if err := tplStore.SetBaseConfig(ctx, configuredTop, []byte(`{}`), ""); err != nil {
		t.Fatalf("SetBaseConfig: %v", err)
	}

	// resolveBaseLayerChain must return ["@base", configuredTop].
	baseChain := resolveBaseLayerChainWith(ctx, tplStore)
	wantBase := []string{sandbox.BaseTemplateID, configuredTop}
	if !slicesEqual(baseChain, wantBase) {
		t.Errorf("Scenario 2: resolveBaseLayerChain = %v, want %v", baseChain, wantBase)
	}

	// No team template → nil.
	teamChain := resolveTemplateLayerChainWith(ctx, tplStore, "team-general")
	if teamChain != nil {
		t.Errorf("Scenario 2: resolveTemplateLayerChain = %v, want nil", teamChain)
	}
}

func TestChainScenario3_ConfiguredBase_TeamTemplate(t *testing.T) {
	es := setupChainTestDB(t)
	ctx := context.Background()

	tplStore := es.SandboxTemplates()
	layers := es.SandboxLayers()

	// 1. Configure base.
	configuredTop := "1111111111111111111111111111111111111111111111111111111111111111"
	if err := layers.PutLayer(ctx, &store.SandboxLayer{
		LayerID:    configuredTop,
		CephFSPath: "/mnt/astonish-layers/" + configuredTop,
		SizeBytes:  2000,
	}); err != nil {
		t.Fatalf("PutLayer configured: %v", err)
	}
	if err := tplStore.SetBaseConfig(ctx, configuredTop, []byte(`{}`), ""); err != nil {
		t.Fatalf("SetBaseConfig: %v", err)
	}

	// 2. Create team layer.
	teamTop := "2222222222222222222222222222222222222222222222222222222222222222"
	if err := layers.PutLayer(ctx, &store.SandboxLayer{
		LayerID:     teamTop,
		ParentLayer: strPtr(sandbox.BaseTemplateID),
		CephFSPath:  "/mnt/astonish-layers/" + teamTop,
		SizeBytes:   500,
	}); err != nil {
		t.Fatalf("PutLayer team: %v", err)
	}

	// 3. Create team template with parent=@base (well-known UUID).
	baseID := getSandboxChainBaseTemplateID(t, ctx, tplStore)
	teamTpl := &store.SandboxTemplate{
		Slug:             "team-general",
		Scope:            store.SandboxTemplateScopeTeam,
		OwnerID:          "general",
		Name:             "Team General",
		ParentTemplateID: &baseID,
		TopLayerID:       &teamTop,
		Version:          1,
	}
	if err := tplStore.Create(ctx, teamTpl); err != nil {
		t.Fatalf("Create team template: %v", err)
	}

	// resolveBaseLayerChain → ["@base", configuredTop].
	baseChain := resolveBaseLayerChainWith(ctx, tplStore)
	wantBase := []string{sandbox.BaseTemplateID, configuredTop}
	if !slicesEqual(baseChain, wantBase) {
		t.Errorf("Scenario 3: resolveBaseLayerChain = %v, want %v", baseChain, wantBase)
	}

	// resolveTemplateLayerChain → ["@base", configuredTop, teamTop].
	teamChain := resolveTemplateLayerChainWith(ctx, tplStore, "team-general")
	wantTeam := []string{sandbox.BaseTemplateID, configuredTop, teamTop}
	if !slicesEqual(teamChain, wantTeam) {
		t.Errorf("Scenario 3: resolveTemplateLayerChain = %v, want %v", teamChain, wantTeam)
	}
}

func TestChainScenario4_FreshInstall_TeamTemplate(t *testing.T) {
	es := setupChainTestDB(t)
	ctx := context.Background()

	tplStore := es.SandboxTemplates()
	layers := es.SandboxLayers()

	// @base is un-configured (top_layer_id = '@base' sentinel from bootstrap).

	// Create team layer.
	teamTop := "3333333333333333333333333333333333333333333333333333333333333333"
	if err := layers.PutLayer(ctx, &store.SandboxLayer{
		LayerID:     teamTop,
		ParentLayer: strPtr(sandbox.BaseTemplateID),
		CephFSPath:  "/mnt/astonish-layers/" + teamTop,
		SizeBytes:   300,
	}); err != nil {
		t.Fatalf("PutLayer team: %v", err)
	}

	// Create team template with parent=@base.
	baseID := getSandboxChainBaseTemplateID(t, ctx, tplStore)
	teamTpl := &store.SandboxTemplate{
		Slug:             "team-general",
		Scope:            store.SandboxTemplateScopeTeam,
		OwnerID:          "general",
		Name:             "Team General",
		ParentTemplateID: &baseID,
		TopLayerID:       &teamTop,
		Version:          1,
	}
	if err := tplStore.Create(ctx, teamTpl); err != nil {
		t.Fatalf("Create team template: %v", err)
	}

	// resolveBaseLayerChain → nil (sentinel filtered).
	baseChain := resolveBaseLayerChainWith(ctx, tplStore)
	if baseChain != nil {
		t.Errorf("Scenario 4: resolveBaseLayerChain = %v, want nil", baseChain)
	}

	// resolveTemplateLayerChain → ["@base", teamTop].
	teamChain := resolveTemplateLayerChainWith(ctx, tplStore, "team-general")
	wantTeam := []string{sandbox.BaseTemplateID, teamTop}
	if !slicesEqual(teamChain, wantTeam) {
		t.Errorf("Scenario 4: resolveTemplateLayerChain = %v, want %v", teamChain, wantTeam)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// getSandboxChainBaseTemplateID retrieves the well-known @base template's UUID from the
// platform database (seeded by entstore.BootstrapPlatform).
func getSandboxChainBaseTemplateID(t *testing.T, ctx context.Context, templates store.SandboxTemplateStore) string {
	t.Helper()
	roots, err := templates.ListRoots(ctx)
	if err != nil {
		t.Fatalf("ListRoots: %v", err)
	}
	for _, r := range roots {
		if r.Slug == "base" && r.Scope == store.SandboxTemplateScopeGlobal {
			return r.ID
		}
	}
	t.Fatal("@base template not found after bootstrap")
	return ""
}
