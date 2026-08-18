package memory

import (
	"context"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

func TestScenarioCardDraftSeparatesRecommendedPathFromTransientFailures(t *testing.T) {
	card := DraftScenarioCardFromMemories("proxmox-console-access", "team", []store.MemorySearchResult{
		{
			ID:        "m1",
			Snippet:   "Use the noVNC ticket endpoint, then open the console websocket with the returned ticket.",
			Category:  "proxmox",
			Scope:     "team",
			SessionID: "session-1",
		},
		{
			ID:       "m2",
			Snippet:  "The API had a temporary 503 outage; do not use this as a permanent avoidance rule.",
			Category: "proxmox",
			Scope:    "team",
		},
	})

	if card.CanonicalKey != "proxmox-console-access" {
		t.Fatalf("CanonicalKey = %q", card.CanonicalKey)
	}
	if len(card.RecommendedRecipe) != 1 {
		t.Fatalf("RecommendedRecipe = %#v, want one efficient path", card.RecommendedRecipe)
	}
	if len(card.CautionsOrConditionalFailures) != 1 {
		t.Fatalf("CautionsOrConditionalFailures = %#v, want one conditional caution", card.CautionsOrConditionalFailures)
	}
	if card.SourceMemoryIDs[0] != "m1" || card.SourceMemoryIDs[1] != "m2" {
		t.Fatalf("SourceMemoryIDs = %#v", card.SourceMemoryIDs)
	}
}

func TestScenarioCardRenderParseRoundTrip(t *testing.T) {
	original := ScenarioCard{
		CanonicalKey:       "proxmox-console-access",
		ScenarioID:         "scenario-proxmox-console",
		Scope:              "team",
		Title:              "Proxmox Console Access",
		Aliases:            []string{"novnc-console"},
		RecommendedRecipe:  []string{"Use noVNC ticket endpoint.", "Open websocket with ticket."},
		Conditions:         []string{"Requires console permission."},
		Verification:       []string{"Console opened successfully."},
		Status:             ScenarioCardStatusVerified,
		SourceMemoryIDs:    []string{"m1"},
		RelatedScenarioIDs: []string{"scenario-proxmox-auth"},
		Identity:           ScenarioIdentity{Domain: "infrastructure", ResourceType: "vm-console", Credentials: []string{"proxmox-token"}},
		Superseded:         []ScenarioSupersededItem{{Value: "old console route", SupersededBy: "noVNC ticket endpoint", Reason: "verified path changed"}},
	}

	parsed, ok := ParseScenarioCard(RenderScenarioCard(original))
	if !ok {
		t.Fatal("expected scenario card to parse")
	}
	if parsed.CanonicalKey != original.CanonicalKey || parsed.Title != original.Title || parsed.Status != original.Status || parsed.ScenarioID != original.ScenarioID {
		t.Fatalf("parsed card mismatch: %#v", parsed)
	}
	if len(parsed.RecommendedRecipe) != 2 {
		t.Fatalf("RecommendedRecipe = %#v", parsed.RecommendedRecipe)
	}
	if len(parsed.Aliases) != 1 || parsed.Aliases[0] != "novnc-console" {
		t.Fatalf("Aliases = %#v", parsed.Aliases)
	}
	if parsed.Identity.ResourceType != "vm-console" || len(parsed.Identity.Credentials) != 1 || parsed.Identity.Credentials[0] != "proxmox-token" {
		t.Fatalf("Identity = %#v", parsed.Identity)
	}
	if len(parsed.Superseded) != 1 || parsed.Superseded[0].SupersededBy != "noVNC ticket endpoint" {
		t.Fatalf("Superseded = %#v", parsed.Superseded)
	}
}

func TestDraftScenarioCardSeparatesCredentialFailuresFromRecommendedPath(t *testing.T) {
	card := DraftScenarioCardFromMemories("infrastructure-openstack-credential-access", "team", []store.MemorySearchResult{{
		Snippet: strings.Join([]string{
			"- The credential name is `openstack-keystone` (type: `openstack_keystone`)",
			"- `resolve_credential(\"openstack\")` returns not found — must use `openstack-keystone`",
			"- Using `{{CREDENTIAL:openstack:username}}` in shell commands does NOT work — placeholders are not substituted in shell",
			"- Correct approach: use `http_request` tool with `credential=\"openstack-keystone\"` — X-Auth-Token is injected automatically",
		}, "\n"),
		Category: "infrastructure/openstack credential access",
		Scope:    "team",
	}})
	// Lines with a conditional-caution marker BUT a resolution indicator should
	// be treated as recipe steps (operational knowledge), not cautions.
	// Only pure failure notes (no resolution) should be cautions.
	for _, step := range card.RecommendedRecipe {
		// "does NOT work ... not substituted" with no resolution indicator → should be a caution, not recipe
		if strings.Contains(strings.ToLower(step), "does not work") && strings.Contains(strings.ToLower(step), "not substituted") {
			t.Fatalf("pure failure note leaked into recommended path: %#v", card.RecommendedRecipe)
		}
	}
	if len(card.CautionsOrConditionalFailures) < 1 {
		t.Fatalf("CautionsOrConditionalFailures = %#v, want at least one pure failure note", card.CautionsOrConditionalFailures)
	}
	// The "not found — must use" line should be in recipe (has resolution indicator "use ")
	foundNotFoundInRecipe := false
	for _, step := range card.RecommendedRecipe {
		if strings.Contains(strings.ToLower(step), "not found") && strings.Contains(strings.ToLower(step), "must use") {
			foundNotFoundInRecipe = true
		}
	}
	if !foundNotFoundInRecipe {
		t.Fatalf("operational knowledge with resolution ('not found — must use') should be in recipe, got recipe=%#v", card.RecommendedRecipe)
	}
}

func TestScenarioIdentityFiltersInternalAndToolAnchors(t *testing.T) {
	identity := ExtractScenarioIdentityFromText(strings.Join([]string{
		"category scenario_card/efficient_successful_path",
		"Use resolve_credential before calling GET https://identity-3.qa-de-1.cloud.sap/v3/auth/catalog/password",
		"Use credential openstack-keystone and X-Auth-Token with https://loadbalancer-3.qa-de-1.cloud.sap/v2/lbaas/loadbalancers",
	}, "\n"))
	for _, credential := range identity.Credentials {
		if credential == "resolve-credential" || credential == "x-auth-token" {
			t.Fatalf("tool/header anchor leaked into credentials: %#v", identity.Credentials)
		}
	}
	for _, path := range identity.URLPaths {
		if path == "/efficient-successful-path" || strings.HasPrefix(path, "//") || path == "/password" {
			t.Fatalf("internal or malformed path leaked into URL paths: %#v", identity.URLPaths)
		}
	}
	if len(identity.EndpointHosts) == 0 || len(identity.URLPaths) == 0 {
		t.Fatalf("expected real host/path anchors: %#v", identity)
	}
}

func TestScenarioIdentityScoresOpenStackOctaviaAndLBaaSAsSameScenario(t *testing.T) {
	existing := ScenarioCard{
		CanonicalKey:      "infrastructure-openstack-lbaas-load",
		Scope:             "personal",
		Title:             "OpenStack LBaaS load balancer list in QA-DE-1",
		RecommendedRecipe: []string{"Use the openstack-keystone credential and GET https://loadbalancer.qa-de-1.cloud.sap/v2/lbaas/loadbalancers."},
		Conditions:        []string{"Applies in qa-de-1."},
		Status:            ScenarioCardStatusVerified,
	}
	incoming := ScenarioCard{
		CanonicalKey:      "infrastructure-openstack-octavia-load",
		Scope:             "personal",
		Title:             "OpenStack Octavia load balancer lookup",
		RecommendedRecipe: []string{"List load balancers through Octavia using the openstack-keystone token at https://octavia.qa-de-1.cloud.sap/v2.0/lbaas/loadbalancers."},
		Conditions:        []string{"Use qa-de-1 OpenStack."},
		Status:            ScenarioCardStatusDraft,
	}

	stats := BuildScenarioCorpusStats([]ScenarioCard{existing, incoming})
	score := ScoreScenarioPair(existing, incoming, stats)
	if score.Decision != "merge" {
		t.Fatalf("Decision = %q score %.2f positives=%#v negatives=%#v, want merge", score.Decision, score.Score, score.PositiveSignals, score.NegativeSignals)
	}
	if len(score.NegativeSignals) != 0 {
		t.Fatalf("NegativeSignals = %#v", score.NegativeSignals)
	}
}

func TestScenarioIdentitySeparatesDifferentOpenStackResources(t *testing.T) {
	loadBalancer := ScenarioCard{
		CanonicalKey:      "infrastructure-openstack-lbaas-load",
		Title:             "OpenStack load balancers in QA-DE-1",
		RecommendedRecipe: []string{"Use openstack-keystone and GET https://octavia.qa-de-1.cloud.sap/v2.0/lbaas/loadbalancers."},
	}
	compute := ScenarioCard{
		CanonicalKey:      "infrastructure-openstack-nova-servers",
		Title:             "OpenStack compute servers in QA-DE-1",
		RecommendedRecipe: []string{"Use openstack-keystone and GET https://compute.qa-de-1.cloud.sap/v2.1/servers."},
	}

	stats := BuildScenarioCorpusStats([]ScenarioCard{loadBalancer, compute})
	score := ScoreScenarioPair(loadBalancer, compute, stats)
	if score.Decision == "merge" {
		t.Fatalf("Decision = merge score %.2f positives=%#v negatives=%#v, want non-merge", score.Score, score.PositiveSignals, score.NegativeSignals)
	}
	if len(score.NegativeSignals) == 0 {
		t.Fatalf("expected negative signals for different resource types: %#v", score)
	}
}

func TestUpsertScenarioCardMergesOpenStackAliasScenario(t *testing.T) {
	ctx := context.Background()
	store := &fakeMemoryStore{}
	first := ScenarioCard{
		CanonicalKey:      "infrastructure-openstack-lbaas-load",
		Scope:             "personal",
		Title:             "OpenStack LBaaS load balancer list in QA-DE-1",
		RecommendedRecipe: []string{"Use the openstack-keystone credential and GET https://loadbalancer.qa-de-1.cloud.sap/v2/lbaas/loadbalancers."},
		Conditions:        []string{"Applies in qa-de-1."},
		Status:            ScenarioCardStatusVerified,
		SourceMemoryIDs:   []string{"m1"},
	}
	result, err := UpsertScenarioCard(ctx, store, first)
	if err != nil {
		t.Fatalf("first upsert failed: %v", err)
	}
	if result.Action != "created" {
		t.Fatalf("first action = %q", result.Action)
	}

	second := ScenarioCard{
		CanonicalKey:      "infrastructure-openstack-octavia-load",
		Scope:             "personal",
		Title:             "OpenStack Octavia load balancer lookup",
		RecommendedRecipe: []string{"List load balancers through Octavia using the openstack-keystone token at https://octavia.qa-de-1.cloud.sap/v2.0/lbaas/loadbalancers."},
		Conditions:        []string{"Use qa-de-1 OpenStack."},
		Status:            ScenarioCardStatusDraft,
		SourceMemoryIDs:   []string{"m2"},
	}
	result, err = UpsertScenarioCard(ctx, store, second)
	if err != nil {
		t.Fatalf("second upsert failed: %v", err)
	}
	if result.Action != "merged" {
		t.Fatalf("second action = %q", result.Action)
	}
	if len(store.entries) != 1 {
		t.Fatalf("entries = %d, want one merged scenario card", len(store.entries))
	}
	merged, ok := ParseScenarioCard(store.entries[0].Snippet)
	if !ok {
		t.Fatal("merged entry did not parse as scenario card")
	}
	if len(merged.SourceMemoryIDs) != 2 {
		t.Fatalf("SourceMemoryIDs = %#v, want both sources", merged.SourceMemoryIDs)
	}
	if len(merged.Aliases) == 0 || merged.Aliases[0] != "infrastructure-openstack-octavia-load" {
		t.Fatalf("Aliases = %#v, want incoming canonical key retained as alias", merged.Aliases)
	}
}

func TestUpsertScenarioCardMergesExistingCard(t *testing.T) {
	ctx := context.Background()
	store := &fakeMemoryStore{}
	first := ScenarioCard{
		CanonicalKey:      "proxmox-console-access",
		Scope:             "team",
		Title:             "Proxmox Console Access",
		RecommendedRecipe: []string{"Use noVNC ticket endpoint."},
		Status:            ScenarioCardStatusDraft,
		SourceMemoryIDs:   []string{"m1"},
	}
	result, err := UpsertScenarioCard(ctx, store, first)
	if err != nil {
		t.Fatalf("first upsert failed: %v", err)
	}
	if result.Action != "created" {
		t.Fatalf("first action = %q", result.Action)
	}

	second := ScenarioCard{
		CanonicalKey:      "proxmox-console-access",
		Scope:             "team",
		Title:             "Proxmox Console Access",
		RecommendedRecipe: []string{"Open websocket with returned ticket."},
		Status:            ScenarioCardStatusVerified,
		SourceMemoryIDs:   []string{"m2"},
	}
	result, err = UpsertScenarioCard(ctx, store, second)
	if err != nil {
		t.Fatalf("second upsert failed: %v", err)
	}
	if result.Action != "merged" {
		t.Fatalf("second action = %q", result.Action)
	}
	if len(store.entries) != 1 {
		t.Fatalf("entries = %d, want one merged scenario card", len(store.entries))
	}
	merged, ok := ParseScenarioCard(store.entries[0].Snippet)
	if !ok {
		t.Fatal("merged entry did not parse as scenario card")
	}
	if merged.Status != ScenarioCardStatusVerified {
		t.Fatalf("Status = %q, want verified", merged.Status)
	}
	if len(merged.RecommendedRecipe) != 2 || len(merged.SourceMemoryIDs) != 2 {
		t.Fatalf("merged card did not combine fields: %#v", merged)
	}
}

func TestFilterPreferredScenarioResultsSuppressesDuplicateScenarioCards(t *testing.T) {
	lbaas := ScenarioCard{
		CanonicalKey:      "infrastructure-openstack-lbaas-load",
		Title:             "OpenStack LBaaS load balancer list in QA-DE-1",
		RecommendedRecipe: []string{"Use openstack-keystone and GET https://loadbalancer.qa-de-1.cloud.sap/v2/lbaas/loadbalancers."},
		Conditions:        []string{"Applies in qa-de-1."},
		Status:            ScenarioCardStatusVerified,
	}
	octavia := ScenarioCard{
		CanonicalKey:      "infrastructure-openstack-octavia-load",
		Title:             "OpenStack Octavia load balancer lookup",
		RecommendedRecipe: []string{"Use openstack-keystone and GET https://octavia.qa-de-1.cloud.sap/v2.0/lbaas/loadbalancers."},
		Conditions:        []string{"Applies in qa-de-1."},
		Status:            ScenarioCardStatusDraft,
	}
	results := []store.MemorySearchResult{
		{ID: "card-1", Snippet: RenderScenarioCard(lbaas), Category: ScenarioCardCategory, Score: 0.95},
		{ID: "card-2", Snippet: RenderScenarioCard(octavia), Category: ScenarioCardCategory, Score: 0.94},
	}

	filtered := FilterPreferredScenarioResults(results)
	if len(filtered) != 1 || filtered[0].ID != "card-1" {
		t.Fatalf("filtered = %#v, want only the first equivalent scenario card", filtered)
	}
}

func TestFilterPreferredScenarioResultsSuppressesSupersededRawMemories(t *testing.T) {
	card := ScenarioCard{
		CanonicalKey:      "proxmox-console-access",
		Title:             "Proxmox Console Access",
		RecommendedRecipe: []string{"Use noVNC ticket endpoint."},
		Status:            ScenarioCardStatusVerified,
		SourceMemoryIDs:   []string{"raw-1"},
	}
	results := []store.MemorySearchResult{
		{ID: "raw-1", Snippet: "Use noVNC ticket endpoint.", Category: "proxmox", Score: 0.99},
		{ID: "card-1", Snippet: RenderScenarioCard(card), Category: ScenarioCardCategory, Score: 0.80},
	}
	filtered := FilterPreferredScenarioResults(results)
	if len(filtered) != 1 || filtered[0].ID != "card-1" {
		t.Fatalf("filtered = %#v, want only scenario card", filtered)
	}
}

func TestFilterPreferredScenarioResultsForQuerySuppressesUnrelatedScenarioCards(t *testing.T) {
	github := ScenarioCard{
		CanonicalKey:      "infrastructure-sap-github-enterprise",
		Title:             "SAP GitHub Enterprise issues API",
		RecommendedRecipe: []string{"Use GET https://github.wdf.sap.corp/api/v3/issues?filter=assigned&state=open&per_page=50."},
		Status:            ScenarioCardStatusVerified,
	}
	openstack := ScenarioCard{
		CanonicalKey:      "infrastructure-openstack-service-endpoints",
		Title:             "OpenStack Octavia load balancer list",
		RecommendedRecipe: []string{"Use openstack-keystone and GET https://loadbalancer-3.qa-de-1.cloud.sap/v2/lbaas/loadbalancers."},
		Status:            ScenarioCardStatusVerified,
	}
	results := []store.MemorySearchResult{
		{ID: "github-card", Snippet: RenderScenarioCard(github), Category: ScenarioCardCategory, Score: 0.60},
		{ID: "openstack-card", Snippet: RenderScenarioCard(openstack), Category: ScenarioCardCategory, Score: 0.60},
	}

	filtered := FilterPreferredScenarioResultsForQuery("github enterprise issues API", results)
	if len(filtered) != 1 || filtered[0].ID != "github-card" {
		t.Fatalf("filtered = %#v, want only the GitHub scenario card", filtered)
	}
}

func TestHasUsableScenarioRecipeRejectsPlaceholderOnlyCard(t *testing.T) {
	if HasUsableScenarioRecipe(ScenarioCard{RecommendedRecipe: []string{ScenarioCardPlaceholderRecipe}}) {
		t.Fatal("placeholder-only scenario card should not be usable durable memory")
	}
	if !HasUsableScenarioRecipe(ScenarioCard{RecommendedRecipe: []string{"Use the noVNC ticket endpoint."}}) {
		t.Fatal("real recommended recipe should be usable durable memory")
	}
}

type fakeMemoryStore struct {
	entries []store.MemorySearchResult
}

func (f *fakeMemoryStore) Search(context.Context, string, int, float64) ([]store.MemorySearchResult, error) {
	return nil, nil
}

func (f *fakeMemoryStore) SearchByCategory(context.Context, string, int, float64, string) ([]store.MemorySearchResult, error) {
	return nil, nil
}

func (f *fakeMemoryStore) Add(_ context.Context, entry store.MemoryEntry) error {
	f.entries = append(f.entries, store.MemorySearchResult{
		ID:       "entry-1",
		Snippet:  entry.Content,
		Category: entry.Category,
	})
	return nil
}

func (f *fakeMemoryStore) Get(_ context.Context, id string) (*store.MemorySearchResult, error) {
	for i := range f.entries {
		if f.entries[i].ID == id {
			return &f.entries[i], nil
		}
	}
	return nil, nil
}

func (f *fakeMemoryStore) Update(_ context.Context, id, content, category string) error {
	for i := range f.entries {
		if f.entries[i].ID == id {
			f.entries[i].Snippet = content
			f.entries[i].Category = category
		}
	}
	return nil
}

func (f *fakeMemoryStore) Delete(context.Context, string) error { return nil }

func (f *fakeMemoryStore) List(_ context.Context, category string, _, _ int) ([]store.MemorySearchResult, error) {
	var out []store.MemorySearchResult
	for _, entry := range f.entries {
		if category == "" || entry.Category == category {
			out = append(out, entry)
		}
	}
	return out, nil
}

func (f *fakeMemoryStore) ListBySession(context.Context, string) ([]store.MemorySearchResult, error) {
	return nil, nil
}

func (f *fakeMemoryStore) Count() int { return len(f.entries) }

func (f *fakeMemoryStore) Close() error { return nil }

// --- Tests for cautionLine() structural heuristic ---

func TestCautionLine_EphemeralFailures(t *testing.T) {
	// Lines with always-caution markers should always be classified as cautions.
	ephemeralLines := []string{
		"The service returned a 503 error",
		"Connection timeout after 30 seconds",
		"A temporary outage prevented access",
		"This was a failed attempt to reach the API",
		"Trial and error showed the old endpoint is broken",
	}
	for _, line := range ephemeralLines {
		if !cautionLine(line) {
			t.Errorf("expected ephemeral line to be caution: %q", line)
		}
	}
}

func TestCautionLine_OperationalKnowledge(t *testing.T) {
	// Lines with conditional-caution markers AND resolution indicators
	// should NOT be classified as cautions — they are operational knowledge.
	operationalLines := []string{
		"Do not use the admin token, use the service-account credential instead",
		"The endpoint does not exist at /v1, use /v2/servers instead",
		"resolve_credential(\"openstack\") returns not found — must use openstack-keystone",
		"The default port is incorrect, the correct port is 9090",
		"Avoid the legacy API, prefer the v3 endpoint",
		"The old path is wrong, should use /api/v2/resources",
	}
	for _, line := range operationalLines {
		if cautionLine(line) {
			t.Errorf("operational knowledge with resolution should NOT be caution: %q", line)
		}
	}
}

func TestCautionLine_PureNegative(t *testing.T) {
	// Lines with conditional-caution markers but NO resolution indicator
	// should be classified as cautions.
	pureNegativeLines := []string{
		"This approach does not work at all",
		"The credential was not found in the store",
		"The configuration is incorrect",
		"Placeholders are not substituted in shell commands",
	}
	for _, line := range pureNegativeLines {
		if !cautionLine(line) {
			t.Errorf("pure negative line without resolution should be caution: %q", line)
		}
	}
}

func TestHasUsableScenarioRecipe_WithCautionOnlyContent(t *testing.T) {
	// A card where all bullets were classified as cautions should have an
	// empty RecommendedRecipe (only placeholder), making HasUsableScenarioRecipe false.
	// The fix in memory_merge.go handles the promotion; this tests the gate itself.
	card := ScenarioCard{
		CanonicalKey:                 "test-card",
		RecommendedRecipe:            []string{ScenarioCardPlaceholderRecipe},
		CautionsOrConditionalFailures: []string{"This does not work at all"},
	}
	if HasUsableScenarioRecipe(card) {
		t.Fatal("card with only placeholder recipe should not be usable")
	}

	// A card with a real recipe step should be usable.
	card.RecommendedRecipe = []string{"Use the /v2/api endpoint with credential X"}
	if !HasUsableScenarioRecipe(card) {
		t.Fatal("card with real recipe step should be usable")
	}
}

func TestIsEphemeralCaution(t *testing.T) {
	ephemeral := []string{
		"Service returned 503 temporarily",
		"Connection timed out after 30s",
		"A temporary network outage occurred",
		"This was a failed attempt",
	}
	for _, line := range ephemeral {
		if !IsEphemeralCaution(line) {
			t.Errorf("expected ephemeral caution: %q", line)
		}
	}

	nonEphemeral := []string{
		"The credential was not found in the store",
		"This approach does not work",
		"The configuration is incorrect",
	}
	for _, line := range nonEphemeral {
		if IsEphemeralCaution(line) {
			t.Errorf("expected non-ephemeral caution: %q", line)
		}
	}
}
