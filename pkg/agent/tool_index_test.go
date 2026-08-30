package agent

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"math"
	"reflect"
	"sync/atomic"
	"testing"

	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/tool"
)

// newTestToolIndex creates a ToolIndex backed by an in-memory vector store.
// This is the standard way to create a ToolIndex in tests.
func TestLexicalToolIndexSearchesWithoutVectorStore(t *testing.T) {
	idx := NewLexicalToolIndex()
	if err := idx.PrimeTools(context.Background(), nil, []*ToolGroup{{Name: "web", Tools: mockTools("web_fetch")}}); err != nil {
		t.Fatalf("PrimeTools: %v", err)
	}
	matches, err := idx.SearchHybrid(context.Background(), "web fetch", 5, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 || matches[0].ToolName != "web_fetch" {
		t.Fatalf("lexical matches = %#v, want web_fetch", matches)
	}
	if idx.Count() != 1 {
		t.Fatalf("Count() = %d, want 1", idx.Count())
	}
}

type failingToolset struct{ err error }

func (f failingToolset) Name() string { return "failing" }
func (f failingToolset) Tools(adkagent.ReadonlyContext) ([]tool.Tool, error) {
	return nil, f.err
}

func TestToolIndexPrimePropagatesToolsetError(t *testing.T) {
	want := errors.New("toolset unavailable")
	idx := NewLexicalToolIndex()
	err := idx.PrimeTools(context.Background(), nil, []*ToolGroup{{Name: "broken", Toolsets: []tool.Toolset{failingToolset{err: want}}}})
	if !errors.Is(err, want) {
		t.Fatalf("PrimeTools error = %v, want %v", err, want)
	}
	if idx.Count() != 0 {
		t.Fatalf("Count() = %d, want unpublished catalog", idx.Count())
	}
}

func TestToolIndexSearchHybridPropagatesEmbeddingError(t *testing.T) {
	want := errors.New("embed unavailable")
	embed := func(context.Context, string) ([]float32, error) { return nil, want }
	idx := newTestToolIndex(t, embed)
	docEmbed := []float32{1, 0}
	if err := idx.vectorStore.AddDocuments(context.Background(), []ToolVectorDoc{{ID: "web:web_fetch", Content: "web_fetch: fetch web", Metadata: map[string]string{"tool_name": "web_fetch", "group_name": "web"}, Embedding: docEmbed}}, 1); err != nil {
		t.Fatal(err)
	}
	_, err := idx.SearchHybrid(context.Background(), "web fetch", 5, 0)
	if !errors.Is(err, want) {
		t.Fatalf("SearchHybrid error = %v, want %v", err, want)
	}
}

func TestInMemoryToolVectorStoreUpsertsAndUsesPrecomputedEmbedding(t *testing.T) {
	var calls atomic.Int32
	embed := func(context.Context, string) ([]float32, error) {
		calls.Add(1)
		return []float32{0, 1}, nil
	}
	store, err := NewInMemoryToolVectorStore(embed)
	if err != nil {
		t.Fatal(err)
	}
	docs := []ToolVectorDoc{{ID: "same", Content: "first", Embedding: []float32{1, 0}}}
	if err := store.AddDocuments(context.Background(), docs, 1); err != nil {
		t.Fatal(err)
	}
	docs[0] = ToolVectorDoc{ID: "same", Content: "second", Embedding: []float32{0, 1}}
	if err := store.AddDocuments(context.Background(), docs, 1); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 || store.Count() != 1 {
		t.Fatalf("calls=%d count=%d, want 0 and 1", calls.Load(), store.Count())
	}
	got, err := store.GetByID(context.Background(), "same")
	if err != nil || got == nil || got.Content != "second" {
		t.Fatalf("GetByID = %#v, %v", got, err)
	}
}

func TestInMemoryToolVectorStoreHonorsCancellation(t *testing.T) {
	store, err := NewInMemoryToolVectorStore(testEmbeddingFunc())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.AddDocuments(ctx, []ToolVectorDoc{{ID: "x", Content: "x"}}, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("AddDocuments error = %v, want context canceled", err)
	}
	if _, err := store.QueryByEmbedding(ctx, []float32{1}, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("QueryByEmbedding error = %v, want context canceled", err)
	}
}

func newTestToolIndex(t *testing.T, ef EmbedFunc) *ToolIndex {
	t.Helper()
	vs, err := NewInMemoryToolVectorStore(ef)
	if err != nil {
		t.Fatalf("NewInMemoryToolVectorStore: %v", err)
	}
	idx, err := NewToolIndex(vs, ef)
	if err != nil {
		t.Fatalf("NewToolIndex: %v", err)
	}
	return idx
}

// testEmbeddingFunc creates a deterministic embedding function for tests.
// It uses SHA-256 of the text to produce a 384-dim vector. This doesn't
// capture semantic similarity but allows testing the index mechanics.
func testEmbeddingFunc() EmbedFunc {
	return func(_ context.Context, text string) ([]float32, error) {
		hash := sha256.Sum256([]byte(text))
		vec := make([]float32, 384)
		for i := 0; i < 384; i++ {
			// Use hash bytes cyclically to fill the vector
			byteIdx := i % 32
			vec[i] = float32(hash[byteIdx]) / 255.0
		}
		// Normalize to unit length for cosine similarity
		var norm float64
		for _, v := range vec {
			norm += float64(v) * float64(v)
		}
		norm = math.Sqrt(norm)
		if norm > 0 {
			for i := range vec {
				vec[i] = float32(float64(vec[i]) / norm)
			}
		}
		return vec, nil
	}
}

// testSemanticEmbeddingFunc creates a bag-of-words embedding function for tests.
// This provides basic semantic similarity: texts sharing words will have
// higher cosine similarity than unrelated texts.
func testSemanticEmbeddingFunc() EmbedFunc {
	return func(_ context.Context, text string) ([]float32, error) {
		vec := make([]float32, 384)
		// Hash each word and accumulate into the vector
		words := splitWords(text)
		for _, word := range words {
			h := sha256.Sum256([]byte(word))
			// Map each word to specific dimensions using its hash
			for i := 0; i < 8; i++ {
				dim := int(binary.LittleEndian.Uint16(h[i*2:])) % 384
				vec[dim] += 1.0
			}
		}
		// Normalize
		var norm float64
		for _, v := range vec {
			norm += float64(v) * float64(v)
		}
		norm = math.Sqrt(norm)
		if norm > 0 {
			for i := range vec {
				vec[i] = float32(float64(vec[i]) / norm)
			}
		}
		return vec, nil
	}
}

// splitWords is a simple word tokenizer for the test embedding function.
func splitWords(s string) []string {
	var words []string
	current := ""
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' {
			current += string(r)
		} else {
			if current != "" {
				words = append(words, current)
				current = ""
			}
		}
	}
	if current != "" {
		words = append(words, current)
	}
	return words
}

func TestToolIndex_NewToolIndex(t *testing.T) {
	idx := newTestToolIndex(t, testEmbeddingFunc())
	if idx == nil {
		t.Fatal("NewToolIndex returned nil")
	}
	if idx.Count() != 0 {
		t.Errorf("new index should be empty, got %d", idx.Count())
	}
}

func TestToolIndex_NewToolIndex_Errors(t *testing.T) {
	_, err := NewToolIndex(nil, EmbedFunc(testEmbeddingFunc()))
	if err == nil {
		t.Error("expected error for nil vector store")
	}

	vs, _ := NewInMemoryToolVectorStore(testEmbeddingFunc())
	_, err = NewToolIndex(vs, nil)
	if err == nil {
		t.Error("expected error for nil embedding func")
	}
}

func TestToolIndex_SyncTools(t *testing.T) {
	idx := newTestToolIndex(t, testEmbeddingFunc())

	mainTools := mockTools("read_file", "write_file", "grep_search")
	groups := []*ToolGroup{
		{
			Name:        "browser",
			Description: "Web automation and screenshots",
			Tools:       mockTools("browser_navigate", "browser_click", "browser_take_screenshot"),
		},
		{
			Name:        "web",
			Description: "HTTP requests and web fetching",
			Tools:       mockTools("http_request", "web_fetch"),
		},
	}

	err := idx.SyncTools(context.Background(), mainTools, groups)
	if err != nil {
		t.Fatalf("SyncTools: %v", err)
	}

	// 3 main + 3 browser + 2 web = 8 tools
	if idx.Count() != 8 {
		t.Errorf("expected 8 indexed tools, got %d", idx.Count())
	}

	// Verify registry
	entry := idx.GetToolEntry("browser_navigate")
	if entry == nil {
		t.Fatal("browser_navigate not in registry")
	}
	if entry.GroupName != "browser" {
		t.Errorf("expected group 'browser', got %q", entry.GroupName)
	}
	if entry.IsMainTool {
		t.Error("browser_navigate should not be main tool")
	}

	entry = idx.GetToolEntry("read_file")
	if entry == nil {
		t.Fatal("read_file not in registry")
	}
	if !entry.IsMainTool {
		t.Error("read_file should be main tool")
	}
	if entry.GroupName != "_main" {
		t.Errorf("expected group '_main', got %q", entry.GroupName)
	}
}

func TestToolIndex_FirstPartyWinsOverMCPNameCollision(t *testing.T) {
	idx := newTestToolIndex(t, testEmbeddingFunc())
	// MCP group sorts before "slides"; previously first-wins would keep the MCP
	// tool and hide the built-in list_templates.
	groups := []*ToolGroup{
		{Name: "mcp:email-mcp", Tools: mockTools("list_templates")},
		{Name: "slides", Tools: mockTools("list_templates")},
	}
	if err := idx.SyncTools(context.Background(), nil, groups); err != nil {
		t.Fatalf("SyncTools: %v", err)
	}
	entry := idx.GetToolEntry("list_templates")
	if entry == nil {
		t.Fatal("list_templates missing from registry")
	}
	if entry.GroupName != "slides" {
		t.Fatalf("first-party slides tool must win over MCP, got group %q", entry.GroupName)
	}
	if idx.FirstPartyToolEntry("list_templates") == nil {
		t.Fatal("FirstPartyToolEntry should find the slides tool")
	}
}

func TestDropMCPShadowsOfFirstParty(t *testing.T) {
	idx := newTestToolIndex(t, testEmbeddingFunc())
	if err := idx.SyncTools(context.Background(), nil, []*ToolGroup{
		{Name: "slides", Tools: mockTools("list_templates")},
	}); err != nil {
		t.Fatalf("SyncTools: %v", err)
	}
	matches := []ToolMatch{
		{ToolName: "list_templates", GroupName: "mcp:email-mcp", Description: "email templates"},
		{ToolName: "send_email", GroupName: "mcp:email-mcp", Description: "send"},
	}
	got := DropMCPShadowsOfFirstParty(idx, matches)
	if len(got) != 1 || got[0].ToolName != "send_email" {
		t.Fatalf("expected only send_email, got %#v", got)
	}
}

func TestToolIndex_SyncTools_Idempotent(t *testing.T) {
	idx := newTestToolIndex(t, testEmbeddingFunc())

	groups := []*ToolGroup{
		{
			Name:  "core",
			Tools: mockTools("read_file", "write_file"),
		},
	}

	// Sync twice — should be idempotent
	err := idx.SyncTools(context.Background(), nil, groups)
	if err != nil {
		t.Fatalf("first SyncTools: %v", err)
	}
	count1 := idx.Count()

	err = idx.SyncTools(context.Background(), nil, groups)
	if err != nil {
		t.Fatalf("second SyncTools: %v", err)
	}
	count2 := idx.Count()

	if count1 != count2 {
		t.Errorf("count changed after re-sync: %d → %d", count1, count2)
	}
}

func TestToolIndex_Search(t *testing.T) {
	// Use the semantic embedding function so word overlap produces similarity
	idx := newTestToolIndex(t, testSemanticEmbeddingFunc())

	groups := []*ToolGroup{
		{
			Name:        "browser",
			Description: "Web automation and screenshots",
			Tools:       mockTools("browser_navigate", "browser_click", "browser_take_screenshot"),
		},
		{
			Name:        "web",
			Description: "HTTP requests and web fetching",
			Tools:       mockTools("http_request", "web_fetch"),
		},
		{
			Name:        "core",
			Description: "File operations and shell commands",
			Tools:       mockTools("read_file", "write_file", "shell_command"),
		},
	}

	err := idx.SyncTools(context.Background(), nil, groups)
	if err != nil {
		t.Fatalf("SyncTools: %v", err)
	}

	// Search should return results (use very low minScore since test embeddings
	// don't produce real semantic similarity)
	matches, err := idx.Search(context.Background(), "take a screenshot of the webpage", 5, 0.01)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("expected at least one match")
	}

	// Verify match structure
	for _, m := range matches {
		if m.ToolName == "" {
			t.Error("match has empty tool name")
		}
		if m.GroupName == "" {
			t.Error("match has empty group name")
		}
		if m.Score <= 0 {
			t.Errorf("match %s has non-positive score: %f", m.ToolName, m.Score)
		}
	}
}

func TestToolIndex_SearchGroups(t *testing.T) {
	idx := newTestToolIndex(t, testSemanticEmbeddingFunc())

	mainTools := mockTools("read_file")
	groups := []*ToolGroup{
		{
			Name:  "browser",
			Tools: mockTools("browser_navigate", "browser_take_screenshot"),
		},
		{
			Name:  "web",
			Tools: mockTools("http_request"),
		},
	}

	err := idx.SyncTools(context.Background(), mainTools, groups)
	if err != nil {
		t.Fatalf("SyncTools: %v", err)
	}

	// SearchGroups should return group names only, excluding main tools
	groupNames, err := idx.SearchGroups(context.Background(), "navigate browser", 5, 0.01)
	if err != nil {
		t.Fatalf("SearchGroups: %v", err)
	}
	// Should have some groups (exact content depends on embedding)
	if len(groupNames) == 0 {
		t.Log("Warning: SearchGroups returned no groups (embedding may not capture semantics)")
	}
	// Verify no "_main" in results
	for _, g := range groupNames {
		if g == "_main" {
			t.Error("SearchGroups should not return '_main'")
		}
	}
}

func TestToolIndex_LookupGroup(t *testing.T) {
	idx := newTestToolIndex(t, testEmbeddingFunc())

	groups := []*ToolGroup{
		{
			Name:  "browser",
			Tools: mockTools("browser_navigate"),
		},
	}

	err := idx.SyncTools(context.Background(), mockTools("read_file"), groups)
	if err != nil {
		t.Fatalf("SyncTools: %v", err)
	}

	if g := idx.LookupGroup("browser_navigate"); g != "browser" {
		t.Errorf("expected 'browser', got %q", g)
	}
	if g := idx.LookupGroup("read_file"); g != "_main" {
		t.Errorf("expected '_main', got %q", g)
	}
	if g := idx.LookupGroup("nonexistent"); g != "" {
		t.Errorf("expected empty string, got %q", g)
	}
}

func TestToolIndex_Dedup(t *testing.T) {
	idx := newTestToolIndex(t, testEmbeddingFunc())

	// read_file appears in both main tools and core group — should be deduped
	mainTools := mockTools("read_file")
	groups := []*ToolGroup{
		{
			Name:  "core",
			Tools: mockTools("read_file", "write_file"),
		},
	}

	err := idx.SyncTools(context.Background(), mainTools, groups)
	if err != nil {
		t.Fatalf("SyncTools: %v", err)
	}

	// Should be 2 (read_file deduped, main wins), not 3
	if idx.Count() != 2 {
		t.Errorf("expected 2 tools after dedup, got %d", idx.Count())
	}

	// read_file should be registered as main tool (main wins)
	entry := idx.GetToolEntry("read_file")
	if entry == nil {
		t.Fatal("read_file not in registry")
	}
	if !entry.IsMainTool {
		t.Error("read_file should be main tool (main takes precedence)")
	}
}

func TestMatchMCPGroupsFromQuery(t *testing.T) {
	idx := newTestToolIndex(t, testEmbeddingFunc())
	if err := idx.SyncTools(context.Background(), mockTools("read_file"), []*ToolGroup{
		{Name: "mcp:email", Tools: mockTools("send_email")},
		{Name: "mcp:github", Tools: mockTools("create_issue")},
		{Name: "browser", Tools: mockTools("browser_navigate")},
	}); err != nil {
		t.Fatalf("SyncTools: %v", err)
	}

	hits := MatchMCPGroupsFromQuery(idx, "Use the mcp server email to send a test e-mail")
	if len(hits) != 1 || hits[0].ToolName != "send_email" {
		t.Fatalf("MatchMCPGroupsFromQuery = %#v, want send_email", hits)
	}

	// Explicit mcp: prefix
	hits = MatchMCPGroupsFromQuery(idx, "call mcp:github please")
	if len(hits) != 1 || hits[0].ToolName != "create_issue" {
		t.Fatalf("explicit mcp: ref = %#v, want create_issue", hits)
	}

	// Non-MCP mention should not match
	if got := MatchMCPGroupsFromQuery(idx, "open the browser"); len(got) != 0 {
		t.Fatalf("browser query should not match MCP groups, got %#v", got)
	}
}

func TestMergeToolMatches(t *testing.T) {
	base := []ToolMatch{{ToolName: "a", GroupName: "g"}}
	extras := []ToolMatch{{ToolName: "a", GroupName: "g"}, {ToolName: "b", GroupName: "g"}}
	got := MergeToolMatches(base, extras)
	if len(got) != 2 {
		t.Fatalf("MergeToolMatches len = %d, want 2", len(got))
	}
}

func TestFormatToolMatchesForPrompt(t *testing.T) {
	matches := []ToolMatch{
		{ToolName: "browser_navigate", GroupName: "browser", Description: "Navigate to a URL", Score: 0.9},
		{ToolName: "browser_take_screenshot", GroupName: "browser", Description: "Take a screenshot", Score: 0.85},
		{ToolName: "http_request", GroupName: "web", Description: "Make HTTP requests", Score: 0.7},
		{ToolName: "read_file", GroupName: "_main", Description: "Read a file", IsMainTool: true, Score: 0.6},
	}

	result := FormatToolMatchesForPrompt(matches)
	if result == "" {
		t.Fatal("expected non-empty result")
	}

	// Check that groups are present
	if !contains(result, "browser") {
		t.Error("result should contain 'browser' group")
	}
	if !contains(result, "web") {
		t.Error("result should contain 'web' group")
	}
	if !contains(result, "execute_tool") {
		t.Error("result should direct deferred tools through execute_tool")
	}
	if !contains(result, "Main thread") {
		t.Error("result should contain main thread section")
	}
}

func TestFormatToolMatchesForPrompt_Empty(t *testing.T) {
	result := FormatToolMatchesForPrompt(nil)
	if result != "" {
		t.Errorf("expected empty result for nil matches, got %q", result)
	}
}

func TestToolIndex_SearchEmpty(t *testing.T) {
	idx := newTestToolIndex(t, testEmbeddingFunc())

	// Search on empty index should return nil, not error
	matches, err := idx.Search(context.Background(), "anything", 5, 0.0)
	if err != nil {
		t.Fatalf("Search on empty index: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("expected 0 matches on empty index, got %d", len(matches))
	}
}

func TestToolIndex_ListAll(t *testing.T) {
	idx := newTestToolIndex(t, testEmbeddingFunc())

	mainTools := mockTools("read_file", "write_file")
	groups := []*ToolGroup{
		{
			Name:        "browser",
			Description: "Web automation",
			Tools:       mockTools("browser_navigate", "browser_click"),
		},
		{
			Name:        "web",
			Description: "HTTP requests",
			Tools:       mockTools("http_request"),
		},
	}

	err := idx.SyncTools(context.Background(), mainTools, groups)
	if err != nil {
		t.Fatalf("SyncTools: %v", err)
	}

	all := idx.ListAll()

	// Should have 3 groups: _main, browser, web
	if len(all) != 3 {
		t.Errorf("expected 3 groups, got %d", len(all))
	}

	// Check main tools
	mainGroup := all["_main"]
	if len(mainGroup) != 2 {
		t.Errorf("expected 2 main tools, got %d", len(mainGroup))
	}
	// Should be sorted alphabetically
	if len(mainGroup) == 2 && mainGroup[0].ToolName != "read_file" {
		t.Errorf("expected first main tool to be 'read_file', got %q", mainGroup[0].ToolName)
	}

	// Check browser group
	browserGroup := all["browser"]
	if len(browserGroup) != 2 {
		t.Errorf("expected 2 browser tools, got %d", len(browserGroup))
	}

	// Check web group
	webGroup := all["web"]
	if len(webGroup) != 1 {
		t.Errorf("expected 1 web tool, got %d", len(webGroup))
	}

	// Verify all entries have IsMainTool set correctly
	for _, m := range mainGroup {
		if !m.IsMainTool {
			t.Errorf("main tool %s should have IsMainTool=true", m.ToolName)
		}
	}
	for _, m := range browserGroup {
		if m.IsMainTool {
			t.Errorf("browser tool %s should have IsMainTool=false", m.ToolName)
		}
	}
}

func TestToolIndex_ListAll_Empty(t *testing.T) {
	idx := newTestToolIndex(t, testEmbeddingFunc())

	all := idx.ListAll()
	if len(all) != 0 {
		t.Errorf("expected 0 groups on empty index, got %d", len(all))
	}
}

// Ensure the _ import for binary is used
var _ = binary.LittleEndian

// ---------------------------------------------------------------------------
// Tokenizer tests
// ---------------------------------------------------------------------------

func TestTokenize(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"use_sandbox_template", []string{"use", "sandbox", "template"}},
		{"browser_take_screenshot", []string{"browser", "take", "screenshot"}},
		{"HTTP request", []string{"http", "request"}},
		{"", nil},
		{"hello", []string{"hello"}},
		{"a_b-c.d", []string{"a", "b", "c", "d"}},
		{"ALL CAPS", []string{"all", "caps"}},
		{"CamelCase", []string{"camelcase"}}, // treated as single token (no camelCase split)
	}
	for _, tc := range tests {
		got := tokenize(tc.input)
		if len(got) != len(tc.want) {
			t.Errorf("tokenize(%q) = %v, want %v", tc.input, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("tokenize(%q)[%d] = %q, want %q", tc.input, i, got[i], tc.want[i])
			}
		}
	}
}

// ---------------------------------------------------------------------------
// BM25 index tests
// ---------------------------------------------------------------------------

func TestBuildBM25Index(t *testing.T) {
	docs := []ToolVectorDoc{
		{ID: "g1:tool_a", Content: "tool_a: Activate a sandbox container", Metadata: map[string]string{"tool_name": "tool_a", "group_name": "g1", "is_main": "false"}},
		{ID: "g1:tool_b", Content: "tool_b: Take a screenshot of the browser", Metadata: map[string]string{"tool_name": "tool_b", "group_name": "g1", "is_main": "false"}},
		{ID: "g2:tool_c", Content: "tool_c: Send an HTTP request to a web API", Metadata: map[string]string{"tool_name": "tool_c", "group_name": "g2", "is_main": "true"}},
	}

	idx := buildBM25Index(docs)
	if idx == nil {
		t.Fatal("buildBM25Index returned nil")
	}
	if idx.totalDocs != 3 {
		t.Errorf("totalDocs = %d, want 3", idx.totalDocs)
	}

	// "container" should appear in exactly 1 document
	if idf, ok := idx.idf["container"]; !ok {
		t.Error("expected IDF entry for 'container'")
	} else if idf <= 0 {
		t.Errorf("IDF for 'container' should be positive, got %f", idf)
	}

	// "a" appears in all 3 docs — IDF should be lower than "container"
	idfA := idx.idf["a"]
	idfContainer := idx.idf["container"]
	if idfA >= idfContainer {
		t.Errorf("IDF('a')=%f should be < IDF('container')=%f since 'a' appears in more docs", idfA, idfContainer)
	}
}

func TestBuildBM25Index_Empty(t *testing.T) {
	idx := buildBM25Index(nil)
	if idx != nil {
		t.Error("buildBM25Index(nil) should return nil")
	}
	idx = buildBM25Index([]ToolVectorDoc{})
	if idx != nil {
		t.Error("buildBM25Index([]) should return nil")
	}
}

func TestBM25Search_BasicKeywordMatch(t *testing.T) {
	docs := []ToolVectorDoc{
		{ID: "sandbox:use_sandbox_template", Content: "use_sandbox_template: Activate a sandbox container from a template", Metadata: map[string]string{"tool_name": "use_sandbox_template", "group_name": "sandbox_templates", "is_main": "false"}},
		{ID: "browser:browser_navigate", Content: "browser_navigate: Navigate the browser to a URL", Metadata: map[string]string{"tool_name": "browser_navigate", "group_name": "browser", "is_main": "false"}},
		{ID: "web:http_request", Content: "http_request: Make an HTTP request to a web API endpoint", Metadata: map[string]string{"tool_name": "http_request", "group_name": "web", "is_main": "false"}},
		{ID: "core:read_file", Content: "read_file: Read the contents of a file", Metadata: map[string]string{"tool_name": "read_file", "group_name": "core", "is_main": "true"}},
	}

	idx := buildBM25Index(docs)
	if idx == nil {
		t.Fatal("buildBM25Index returned nil")
	}

	// "activate container" should strongly match use_sandbox_template
	results := idx.search("activate container", 10)
	if len(results) == 0 {
		t.Fatal("expected at least one result for 'activate container'")
	}
	if results[0].toolName != "use_sandbox_template" {
		t.Errorf("top result for 'activate container' = %q, want 'use_sandbox_template'", results[0].toolName)
	}
}

func TestBM25Search_ProperNounDilution(t *testing.T) {
	// This is the core test: "activate the container juicytrade" should still
	// match use_sandbox_template via BM25 even though "juicytrade" is noise.
	docs := []ToolVectorDoc{
		{ID: "sandbox:use_sandbox_template", Content: "use_sandbox_template: Activate a sandbox container from a template", Metadata: map[string]string{"tool_name": "use_sandbox_template", "group_name": "sandbox_templates", "is_main": "false"}},
		{ID: "browser:browser_navigate", Content: "browser_navigate: Navigate the browser to a URL", Metadata: map[string]string{"tool_name": "browser_navigate", "group_name": "browser", "is_main": "false"}},
		{ID: "web:http_request", Content: "http_request: Make an HTTP request to a web API endpoint", Metadata: map[string]string{"tool_name": "http_request", "group_name": "web", "is_main": "false"}},
		{ID: "core:read_file", Content: "read_file: Read the contents of a file", Metadata: map[string]string{"tool_name": "read_file", "group_name": "core", "is_main": "true"}},
		{ID: "core:shell_command", Content: "shell_command: Execute a shell command in a terminal", Metadata: map[string]string{"tool_name": "shell_command", "group_name": "core", "is_main": "true"}},
	}

	idx := buildBM25Index(docs)
	if idx == nil {
		t.Fatal("buildBM25Index returned nil")
	}

	// "juicytrade" doesn't appear in any document, so BM25 ignores it.
	// "activate", "the", "container" are real words — "activate" and "container"
	// match use_sandbox_template.
	results := idx.search("activate the container juicytrade", 10)
	if len(results) == 0 {
		t.Fatal("expected at least one result for 'activate the container juicytrade'")
	}
	if results[0].toolName != "use_sandbox_template" {
		t.Errorf("top result for noisy query = %q, want 'use_sandbox_template'", results[0].toolName)
	}
	t.Logf("BM25 scores for 'activate the container juicytrade':")
	for _, r := range results {
		t.Logf("  %s (group=%s): %.4f", r.toolName, r.groupName, r.score)
	}
}

func TestBM25Search_NoMatch(t *testing.T) {
	docs := []ToolVectorDoc{
		{ID: "a:tool_a", Content: "tool_a: Do something specific", Metadata: map[string]string{"tool_name": "tool_a", "group_name": "a", "is_main": "false"}},
	}

	idx := buildBM25Index(docs)
	results := idx.search("completely unrelated xyzzy", 5)
	// "completely", "unrelated", "xyzzy" don't appear in any doc
	if len(results) != 0 {
		t.Errorf("expected 0 results for unrelated query, got %d", len(results))
	}
}

func TestBM25Search_Empty(t *testing.T) {
	var idx *bm25Index
	results := idx.search("anything", 5)
	if results != nil {
		t.Error("search on nil index should return nil")
	}
}

// ---------------------------------------------------------------------------
// Hybrid search tests
// ---------------------------------------------------------------------------

func TestToolIndexSearchHybridPreparedReusesCompatibleEmbedding(t *testing.T) {
	calls := 0
	embed := EmbedFunc(func(context.Context, string) ([]float32, error) {
		calls++
		return []float32{1, 0}, nil
	})
	store, err := NewInMemoryToolVectorStore(embed)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := NewToolIndex(store, embed)
	if err != nil {
		t.Fatal(err)
	}
	if err := idx.SyncTools(context.Background(), mockTools("read_file"), nil); err != nil {
		t.Fatal(err)
	}
	calls = 0
	identity := reflect.ValueOf(embed).Pointer()
	if !idx.CanUsePreparedEmbedding(identity, []float32{1, 0}) {
		t.Fatal("compatible prepared embedding rejected")
	}
	if idx.CanUsePreparedEmbedding(identity, []float32{1}) {
		t.Fatal("dimension mismatch accepted")
	}
	if _, err := idx.SearchHybridPrepared(context.Background(), "read file", []float32{1, 0}, 5, 0); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("embedding calls = %d, want 0", calls)
	}
}

func TestToolIndex_SearchHybrid(t *testing.T) {
	idx := newTestToolIndex(t, testSemanticEmbeddingFunc())

	groups := []*ToolGroup{
		{
			Name:        "browser",
			Description: "Web automation and screenshots",
			Tools:       mockTools("browser_navigate", "browser_click", "browser_take_screenshot"),
		},
		{
			Name:        "web",
			Description: "HTTP requests and web fetching",
			Tools:       mockTools("http_request", "web_fetch"),
		},
		{
			Name:        "core",
			Description: "File operations and shell commands",
			Tools:       mockTools("read_file", "write_file", "shell_command"),
		},
	}

	err := idx.SyncTools(context.Background(), nil, groups)
	if err != nil {
		t.Fatalf("SyncTools: %v", err)
	}

	// SearchHybrid should return results
	matches, err := idx.SearchHybrid(context.Background(), "take a screenshot of the webpage", 5, 0)
	if err != nil {
		t.Fatalf("SearchHybrid: %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("expected at least one match from hybrid search")
	}

	// Verify match structure
	for _, m := range matches {
		if m.ToolName == "" {
			t.Error("match has empty tool name")
		}
		if m.GroupName == "" {
			t.Error("match has empty group name")
		}
		if m.Score <= 0 {
			t.Errorf("match %s has non-positive score: %f", m.ToolName, m.Score)
		}
	}

	t.Logf("SearchHybrid results for 'take a screenshot of the webpage':")
	for i, m := range matches {
		t.Logf("  %d. %s (group=%s, score=%.4f)", i+1, m.ToolName, m.GroupName, m.Score)
	}
}

func TestToolIndex_SearchHybridEmbeddingFailureDoesNotFallBack(t *testing.T) {
	embedErr := errors.New("embedding unavailable")
	calls := 0
	embed := func(context.Context, string) ([]float32, error) {
		calls++
		if calls > 1 {
			return nil, embedErr
		}
		return []float32{1, 0, 0}, nil
	}
	idx := newTestToolIndex(t, embed)
	if err := idx.SyncTools(context.Background(), nil, []*ToolGroup{{
		Name:  "core",
		Tools: mockTools("read_file"),
	}}); err != nil {
		t.Fatalf("SyncTools: %v", err)
	}

	matches, err := idx.SearchHybrid(context.Background(), "read file", 5, 0)
	if !errors.Is(err, embedErr) {
		t.Fatalf("SearchHybrid error = %v, want %v", err, embedErr)
	}
	if len(matches) != 0 {
		t.Fatalf("SearchHybrid returned lexical fallback: %#v", matches)
	}
}

func TestToolIndex_SearchHybrid_Empty(t *testing.T) {
	idx := newTestToolIndex(t, testEmbeddingFunc())

	matches, err := idx.SearchHybrid(context.Background(), "anything", 5, 0)
	if err != nil {
		t.Fatalf("SearchHybrid on empty: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("expected 0 matches on empty index, got %d", len(matches))
	}
}

func TestToolIndex_SearchGroupsHybrid(t *testing.T) {
	idx := newTestToolIndex(t, testSemanticEmbeddingFunc())

	mainTools := mockTools("read_file")
	groups := []*ToolGroup{
		{
			Name:  "browser",
			Tools: mockTools("browser_navigate", "browser_take_screenshot"),
		},
		{
			Name:  "web",
			Tools: mockTools("http_request"),
		},
	}

	err := idx.SyncTools(context.Background(), mainTools, groups)
	if err != nil {
		t.Fatalf("SyncTools: %v", err)
	}

	groupNames, err := idx.SearchGroupsHybrid(context.Background(), "navigate browser", 5, 0)
	if err != nil {
		t.Fatalf("SearchGroupsHybrid: %v", err)
	}
	// Verify no "_main" in results
	for _, g := range groupNames {
		if g == "_main" {
			t.Error("SearchGroupsHybrid should not return '_main'")
		}
	}
	t.Logf("SearchGroupsHybrid groups for 'navigate browser': %v", groupNames)
}

func TestToolIndex_SearchHybrid_BM25BoostsWeakVector(t *testing.T) {
	// This test verifies that BM25 can rescue a result that scores poorly
	// in vector search. We use the deterministic (non-semantic) hash embedding
	// which gives essentially random similarity. BM25 keyword matching should
	// still surface the right tool via RRF fusion.
	idx := newTestToolIndex(t, testEmbeddingFunc()) // hash-based, NOT semantic

	groups := []*ToolGroup{
		{
			Name:        "sandbox_templates",
			Description: "Container sandbox management",
			Tools:       mockTools("use_sandbox_template"),
		},
		{
			Name:        "browser",
			Description: "Web browser automation",
			Tools:       mockTools("browser_navigate", "browser_click"),
		},
	}

	err := idx.SyncTools(context.Background(), nil, groups)
	if err != nil {
		t.Fatalf("SyncTools: %v", err)
	}

	// With hash embeddings, vector search is basically random.
	// BM25 should find "use_sandbox_template" because its description contains
	// "sandbox" which overlaps with "sandbox" in the query.
	matches, err := idx.SearchHybrid(context.Background(), "sandbox template", 5, 0)
	if err != nil {
		t.Fatalf("SearchHybrid: %v", err)
	}

	// use_sandbox_template should appear in results thanks to BM25
	found := false
	for _, m := range matches {
		if m.ToolName == "use_sandbox_template" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected use_sandbox_template in results (BM25 should find it via keyword match)")
	}

	t.Logf("Hybrid results for 'sandbox template':")
	for i, m := range matches {
		t.Logf("  %d. %s (score=%.4f)", i+1, m.ToolName, m.Score)
	}
}
