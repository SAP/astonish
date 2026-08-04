package agent

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"unicode"

	"google.golang.org/adk/tool"
)

// ToolIndex is a dedicated vector index for tool discovery. Each tool is stored
// as its own document (name + description), enabling accurate semantic retrieval
// without the chunking problems of the general memory store.
//
// Both the main thread and sub-agents use this index:
//   - Main thread: queries it for prompt injection ("Relevant tools for this request")
//     and exposes it via search_tools
//   - Sub-agents: queries it at creation time to auto-discover which tools to load,
//     and also get search_tools for mid-execution discovery
type ToolIndex struct {
	vectorStore ToolVectorStore
	embedFunc   EmbedFunc
	mu          sync.RWMutex

	// toolRegistry maps tool_name → ToolEntry for resolving search results
	// back to group names and concrete tool implementations.
	toolRegistry map[string]ToolEntry

	// knownIDs tracks doc IDs from the last SyncTools call.
	// Used to detect and prune stale entries on the next sync.
	knownIDs map[string]bool

	// BM25 inverted index for keyword-based search.
	// Built during SyncTools() alongside the vector index.
	bm25 *bm25Index
}

// bm25Index is a simple BM25 inverted index for keyword scoring.
// It stores pre-computed TF-IDF weights so queries are O(query_terms * matching_docs).
type bm25Index struct {
	// idf maps term → inverse document frequency: log(N / df)
	idf map[string]float64
	// docTermFreqs maps docID → {term → sublinear TF: 1 + log(tf)}
	docTermFreqs map[string]map[string]float64
	// docNorms maps docID → L2 norm of the TF-IDF vector (for cosine normalization)
	docNorms map[string]float64
	// docMeta maps docID → metadata needed to build ToolMatch results
	docMeta map[string]bm25DocMeta
	// totalDocs is the number of documents in the index
	totalDocs int
}

type bm25DocMeta struct {
	toolName  string
	groupName string
	isMain    bool
}

// ToolEntry holds metadata about a single tool in the index.
type ToolEntry struct {
	Name        string
	Description string
	GroupName   string    // Which tool group this belongs to ("core", "browser", etc.)
	IsMainTool  bool      // True for main-thread tools (no delegation needed)
	Tool        tool.Tool // The concrete tool implementation (nil for main-thread info-only entries)
}

// ToolMatch represents a search result from the tool index.
type ToolMatch struct {
	ToolName    string  `json:"tool_name"`
	GroupName   string  `json:"group_name"`
	Description string  `json:"description"`
	IsMainTool  bool    `json:"is_main_tool"`
	Score       float64 `json:"score"`
}

// NewToolIndex creates a new tool index backed by a ToolVectorStore.
// The vectorStore handles embedding storage and nearest-neighbor search.
// The embedFunc is used to embed query strings before vector search.
func NewToolIndex(vectorStore ToolVectorStore, embedFunc EmbedFunc) (*ToolIndex, error) {
	if vectorStore == nil {
		return nil, fmt.Errorf("vector store is required")
	}
	if embedFunc == nil {
		return nil, fmt.Errorf("embedding function is required")
	}

	return &ToolIndex{
		vectorStore:  vectorStore,
		embedFunc:    embedFunc,
		toolRegistry: make(map[string]ToolEntry),
		bm25:         nil, // built during SyncTools
	}, nil
}

// SortedGroups returns tool groups sorted by name for deterministic output.
func SortedGroups(groups map[string]*ToolGroup) []*ToolGroup {
	sorted := make([]*ToolGroup, 0, len(groups))
	for _, g := range groups {
		sorted = append(sorted, g)
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Name < sorted[j].Name
	})
	return sorted
}

// SyncTools indexes all tools from main-thread tools and tool groups.
// Each tool becomes a single document: "{tool_name}: {tool_description}"
// with metadata for group_name and tool_name.
//
// This is called at startup and whenever tool groups change.
// It performs an incremental sync: only tools with new or changed content
// are re-embedded. Unchanged tools reuse their persisted embeddings,
// making restarts near-instant when the tool set hasn't changed.
func (idx *ToolIndex) SyncTools(ctx context.Context, mainTools []tool.Tool, groups []*ToolGroup) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	// Build the registry and document list
	registry := make(map[string]ToolEntry)
	var docs []ToolVectorDoc

	// Index main-thread tools
	for _, t := range mainTools {
		name := t.Name()
		entry := ToolEntry{
			Name:        name,
			Description: t.Description(),
			GroupName:   "_main",
			IsMainTool:  true,
			Tool:        t,
		}
		registry[name] = entry
		docs = append(docs, ToolVectorDoc{
			ID:      "main:" + name,
			Content: name + ": " + t.Description(),
			Metadata: map[string]string{
				"tool_name":  name,
				"group_name": "_main",
				"is_main":    "true",
			},
		})
	}

	// Index tools from each group
	readCtx := &minimalReadonlyContext{Context: ctx}
	for _, g := range groups {
		// Regular tools
		for _, t := range g.Tools {
			name := t.Name()
			if _, exists := registry[name]; exists {
				continue // dedup (tool might appear in main + group)
			}
			entry := ToolEntry{
				Name:        name,
				Description: t.Description(),
				GroupName:   g.Name,
				IsMainTool:  false,
				Tool:        t,
			}
			registry[name] = entry
			docs = append(docs, ToolVectorDoc{
				ID:      g.Name + ":" + name,
				Content: name + ": " + t.Description(),
				Metadata: map[string]string{
					"tool_name":  name,
					"group_name": g.Name,
					"is_main":    "false",
				},
			})
		}

		// MCP toolset tools
		for _, ts := range g.Toolsets {
			mcpTools, err := ts.Tools(readCtx)
			if err != nil {
				continue
			}
			for _, t := range mcpTools {
				name := t.Name()
				if _, exists := registry[name]; exists {
					continue
				}
				entry := ToolEntry{
					Name:        name,
					Description: t.Description(),
					GroupName:   g.Name,
					IsMainTool:  false,
					Tool:        t,
				}
				registry[name] = entry
				docs = append(docs, ToolVectorDoc{
					ID:      g.Name + ":" + name,
					Content: name + ": " + t.Description(),
					Metadata: map[string]string{
						"tool_name":  name,
						"group_name": g.Name,
						"is_main":    "false",
					},
				})
			}
		}
	}

	// Incremental sync: only embed tools whose content has changed.
	// On a typical restart where tools haven't changed, this skips all
	// embedding calls — the only cost is GetByID lookups against the store.
	var toEmbed []ToolVectorDoc
	currentIDs := make(map[string]bool, len(docs))
	for _, doc := range docs {
		currentIDs[doc.ID] = true
		existing, _ := idx.vectorStore.GetByID(ctx, doc.ID)
		if existing != nil && existing.Content == doc.Content {
			continue // already embedded with same content
		}
		toEmbed = append(toEmbed, doc)
	}

	// Prune stale entries not in the current tool set. Prefer store-wide
	// enumeration (covers process restarts against a persistent backend);
	// fall back to IDs from the previous SyncTools call in this process.
	var staleIDs []string
	if lister, ok := idx.vectorStore.(interface {
		AllIDs(context.Context) ([]string, error)
	}); ok {
		if all, err := lister.AllIDs(ctx); err == nil {
			for _, id := range all {
				if !currentIDs[id] {
					staleIDs = append(staleIDs, id)
				}
			}
		}
	} else if idx.knownIDs != nil {
		for id := range idx.knownIDs {
			if !currentIDs[id] {
				staleIDs = append(staleIDs, id)
			}
		}
	}
	if len(staleIDs) > 0 {
		if err := idx.vectorStore.DeleteByIDs(ctx, staleIDs); err != nil {
			slog.Warn("failed to prune stale tool index entries", "count", len(staleIDs), "error", err)
		} else {
			slog.Debug("pruned stale tool index entries", "count", len(staleIDs))
		}
	}
	idx.knownIDs = currentIDs

	if len(docs) == 0 {
		idx.toolRegistry = registry
		idx.bm25 = nil
		return nil
	}

	if len(toEmbed) > 0 {
		// Only embed the new/changed documents (concurrency=4)
		if err := idx.vectorStore.AddDocuments(ctx, toEmbed, 4); err != nil {
			return fmt.Errorf("failed to index tools: %w", err)
		}
	}

	// Build BM25 inverted index from the same documents.
	idx.bm25 = buildBM25Index(docs)
	idx.toolRegistry = registry
	return nil
}

// Search queries the tool index for tools matching the query string.
// Returns the top-K matches sorted by relevance score.
func (idx *ToolIndex) Search(ctx context.Context, query string, topK int, minScore float64) ([]ToolMatch, error) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	if topK <= 0 {
		topK = 10
	}
	// minScore < 0 means "use default". Zero means "no threshold".
	if minScore < 0 {
		minScore = 0.3
	}

	docCount := idx.vectorStore.Count()
	if docCount == 0 {
		return nil, nil
	}
	if topK > docCount {
		topK = docCount
	}

	// Embed the query
	queryEmb, err := idx.embedFunc(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to embed query: %w", err)
	}

	results, err := idx.vectorStore.QueryByEmbedding(ctx, queryEmb, topK)
	if err != nil {
		return nil, fmt.Errorf("tool search failed: %w", err)
	}

	var matches []ToolMatch
	for _, r := range results {
		score := float64(r.Similarity)
		if score < minScore {
			continue
		}

		toolName := r.Metadata["tool_name"]
		groupName := r.Metadata["group_name"]
		isMain := r.Metadata["is_main"] == "true"

		// Use the registry for the description (it's cleaner than parsing Content).
		// Fall back to the stored document content if the tool isn't in the
		// current registry (e.g., persisted docs from a previous session).
		desc := ""
		if entry, ok := idx.toolRegistry[toolName]; ok {
			desc = entry.Description
		} else if r.Content != "" {
			// Content format is "tool_name: description" — strip the prefix
			if colonIdx := strings.Index(r.Content, ": "); colonIdx >= 0 {
				desc = r.Content[colonIdx+2:]
			} else {
				desc = r.Content
			}
		}

		matches = append(matches, ToolMatch{
			ToolName:    toolName,
			GroupName:   groupName,
			Description: desc,
			IsMainTool:  isMain,
			Score:       score,
		})
	}

	return matches, nil
}

// SearchGroups queries the tool index and returns the unique group names
// of matching tools. This is used by sub-agent auto-discovery to determine
// which tool groups to load.
func (idx *ToolIndex) SearchGroups(ctx context.Context, query string, topK int, minScore float64) []string {
	matches, err := idx.Search(ctx, query, topK, minScore)
	if err != nil || len(matches) == 0 {
		return nil
	}

	seen := make(map[string]bool)
	var groups []string
	for _, m := range matches {
		if m.IsMainTool || seen[m.GroupName] {
			continue
		}
		seen[m.GroupName] = true
		groups = append(groups, m.GroupName)
	}
	sort.Strings(groups)
	return groups
}

// FormatForPrompt formats tool matches as a text block suitable for
// injection into the system prompt. Groups matches by tool group for clarity.
func FormatToolMatchesForPrompt(matches []ToolMatch) string {
	if len(matches) == 0 {
		return ""
	}

	// Group by group_name
	type groupInfo struct {
		tools []ToolMatch
	}
	groups := make(map[string]*groupInfo)
	var mainTools []ToolMatch
	var groupOrder []string

	for _, m := range matches {
		if m.IsMainTool {
			mainTools = append(mainTools, m)
			continue
		}
		if groups[m.GroupName] == nil {
			groups[m.GroupName] = &groupInfo{}
			groupOrder = append(groupOrder, m.GroupName)
		}
		groups[m.GroupName].tools = append(groups[m.GroupName].tools, m)
	}
	sort.Strings(groupOrder)

	var sb strings.Builder

	// Dynamically injected tools (grouped by origin)
	for _, gName := range groupOrder {
		g := groups[gName]
		if strings.HasPrefix(gName, "mcp:") {
			sb.WriteString(fmt.Sprintf("**%s** group (call bare tool names directly — NOT `%s/<tool>`):\n", gName, gName))
		} else {
			sb.WriteString(fmt.Sprintf("**%s** group (call directly):\n", gName))
		}
		for _, m := range g.tools {
			sb.WriteString(fmt.Sprintf("  - `%s` — %s\n", m.ToolName, truncateDesc(m.Description, 120)))
		}
	}

	// Main-thread tools (always available)
	if len(mainTools) > 0 {
		sb.WriteString("**Main thread** (always available):\n")
		for _, m := range mainTools {
			sb.WriteString(fmt.Sprintf("  - `%s` — %s\n", m.ToolName, truncateDesc(m.Description, 120)))
		}
	}

	return sb.String()
}

// truncateDesc truncates a description to maxLen characters, adding "..." if truncated.
func truncateDesc(s string, maxLen int) string {
	// Take first line only
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		s = s[:idx]
	}
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// Count returns the number of tools in the index.
func (idx *ToolIndex) Count() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.vectorStore.Count()
}

// ListAll returns every tool in the registry grouped by group name.
// This is used for full inventory enumeration (e.g., "list all tools").
func (idx *ToolIndex) ListAll() map[string][]ToolMatch {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	groups := make(map[string][]ToolMatch)
	for _, entry := range idx.toolRegistry {
		m := ToolMatch{
			ToolName:    entry.Name,
			GroupName:   entry.GroupName,
			Description: entry.Description,
			IsMainTool:  entry.IsMainTool,
			Score:       1.0,
		}
		groups[entry.GroupName] = append(groups[entry.GroupName], m)
	}

	// Sort tools within each group for deterministic output
	for _, tools := range groups {
		sort.Slice(tools, func(i, j int) bool {
			return tools[i].ToolName < tools[j].ToolName
		})
	}

	return groups
}

// LookupGroup returns the group name for a given tool name, or empty string if not found.
func (idx *ToolIndex) LookupGroup(toolName string) string {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	if entry, ok := idx.toolRegistry[toolName]; ok {
		return entry.GroupName
	}
	return ""
}

// GetToolEntry returns the full ToolEntry for a tool name, or nil if not found.
func (idx *ToolIndex) GetToolEntry(toolName string) *ToolEntry {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	if entry, ok := idx.toolRegistry[toolName]; ok {
		return &entry
	}
	return nil
}

// GetToolsByGroup returns all tool entries belonging to the given group name.
// Returns nil if the group has no tools registered.
func (idx *ToolIndex) GetToolsByGroup(groupName string) []ToolEntry {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	var results []ToolEntry
	for _, entry := range idx.toolRegistry {
		if entry.GroupName == groupName {
			results = append(results, entry)
		}
	}
	return results
}

// MatchMCPGroupsFromQuery finds MCP tool groups mentioned in free text and
// returns all tools in those groups. Handles phrases like "mcp server email",
// "mcp:email", and bare server ids when preceded by "mcp".
//
// This is used so the main thread can inject MCP tools when the user names a
// server even if hybrid search scores poorly.
func MatchMCPGroupsFromQuery(idx *ToolIndex, query string) []ToolMatch {
	if idx == nil || strings.TrimSpace(query) == "" {
		return nil
	}
	q := strings.ToLower(query)
	all := idx.ListAll()

	// Collect candidate mcp group names that appear in the query.
	var matchedGroups []string
	for groupName := range all {
		if !strings.HasPrefix(groupName, "mcp:") {
			continue
		}
		server := strings.TrimPrefix(groupName, "mcp:")
		if server == "" {
			continue
		}
		serverLower := strings.ToLower(server)
		// Explicit refs
		if strings.Contains(q, "mcp:"+serverLower) ||
			strings.Contains(q, "mcp/"+serverLower) ||
			strings.Contains(q, groupName) {
			matchedGroups = append(matchedGroups, groupName)
			continue
		}
		// "mcp server email", "mcp email", "the email mcp"
		if strings.Contains(q, "mcp") && (strings.Contains(q, " "+serverLower) ||
			strings.Contains(q, serverLower+" ") ||
			strings.HasSuffix(q, serverLower) ||
			strings.HasPrefix(q, serverLower+" ") ||
			strings.Contains(q, "server "+serverLower) ||
			strings.Contains(q, serverLower+" mcp") ||
			strings.Contains(q, "mcp "+serverLower)) {
			matchedGroups = append(matchedGroups, groupName)
		}
	}
	if len(matchedGroups) == 0 {
		return nil
	}
	sort.Strings(matchedGroups)

	var out []ToolMatch
	seen := make(map[string]bool)
	for _, g := range matchedGroups {
		for _, m := range all[g] {
			if seen[m.ToolName] {
				continue
			}
			seen[m.ToolName] = true
			out = append(out, m)
		}
	}
	return out
}

// MergeToolMatches appends extras into base without duplicating tool names.
func MergeToolMatches(base []ToolMatch, extras []ToolMatch) []ToolMatch {
	if len(extras) == 0 {
		return base
	}
	seen := make(map[string]bool, len(base))
	for _, m := range base {
		seen[m.ToolName] = true
	}
	for _, m := range extras {
		if seen[m.ToolName] {
			continue
		}
		seen[m.ToolName] = true
		base = append(base, m)
	}
	return base
}

// ---------------------------------------------------------------------------
// BM25 keyword search
// ---------------------------------------------------------------------------

// tokenize splits text into lowercase alphanumeric tokens.
// Underscores are treated as word separators (so "use_sandbox_template" becomes
// ["use", "sandbox", "template"]) to improve keyword matching against tool names.
func tokenize(s string) []string {
	var tokens []string
	var current []rune
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			current = append(current, unicode.ToLower(r))
		} else {
			// Non-alphanumeric (including '_') splits tokens
			if len(current) > 0 {
				tokens = append(tokens, string(current))
				current = current[:0]
			}
		}
	}
	if len(current) > 0 {
		tokens = append(tokens, string(current))
	}
	return tokens
}

// buildBM25Index constructs a BM25 inverted index from tool vector documents.
// Each document's Content is tokenized and scored with sublinear TF * IDF.
func buildBM25Index(docs []ToolVectorDoc) *bm25Index {
	N := len(docs)
	if N == 0 {
		return nil
	}

	// Step 1: Compute raw term frequencies per document
	type docTF struct {
		terms map[string]int // term → raw count
	}
	docTFs := make(map[string]*docTF, N) // docID → docTF
	docDF := make(map[string]int)        // term → number of docs containing it

	for _, doc := range docs {
		tf := &docTF{terms: make(map[string]int)}
		seen := make(map[string]bool) // for DF counting (each term once per doc)
		tokens := tokenize(doc.Content)
		for _, tok := range tokens {
			tf.terms[tok]++
			if !seen[tok] {
				docDF[tok]++
				seen[tok] = true
			}
		}
		docTFs[doc.ID] = tf
	}

	// Step 2: Compute IDF for each term: log(N / df)
	// Using log(1 + N/df) to avoid zero IDF for terms appearing in all docs.
	idf := make(map[string]float64, len(docDF))
	for term, df := range docDF {
		idf[term] = math.Log(1.0 + float64(N)/float64(df))
	}

	// Step 3: Compute sublinear TF-IDF vectors and L2 norms
	docTermFreqs := make(map[string]map[string]float64, N)
	docNorms := make(map[string]float64, N)
	docMeta := make(map[string]bm25DocMeta, N)

	for _, doc := range docs {
		tf := docTFs[doc.ID]
		tfidf := make(map[string]float64, len(tf.terms))
		var normSq float64
		for term, rawTF := range tf.terms {
			// Sublinear TF: 1 + log(tf)
			subTF := 1.0 + math.Log(float64(rawTF))
			w := subTF * idf[term]
			tfidf[term] = w
			normSq += w * w
		}
		docTermFreqs[doc.ID] = tfidf
		if normSq > 0 {
			docNorms[doc.ID] = math.Sqrt(normSq)
		}

		docMeta[doc.ID] = bm25DocMeta{
			toolName:  doc.Metadata["tool_name"],
			groupName: doc.Metadata["group_name"],
			isMain:    doc.Metadata["is_main"] == "true",
		}
	}

	return &bm25Index{
		idf:          idf,
		docTermFreqs: docTermFreqs,
		docNorms:     docNorms,
		docMeta:      docMeta,
		totalDocs:    N,
	}
}

// searchBM25 scores all documents against the query using cosine similarity
// on TF-IDF vectors. Returns results sorted by score descending.
func (b *bm25Index) search(query string, topK int) []bm25Result {
	if b == nil || b.totalDocs == 0 {
		return nil
	}

	queryTokens := tokenize(query)
	if len(queryTokens) == 0 {
		return nil
	}

	// Build query TF-IDF vector
	queryTF := make(map[string]int)
	for _, tok := range queryTokens {
		queryTF[tok]++
	}
	queryVec := make(map[string]float64, len(queryTF))
	var queryNormSq float64
	for term, rawTF := range queryTF {
		termIDF, ok := b.idf[term]
		if !ok {
			continue // term not in any document
		}
		w := (1.0 + math.Log(float64(rawTF))) * termIDF
		queryVec[term] = w
		queryNormSq += w * w
	}
	if queryNormSq == 0 {
		return nil // no query terms found in any document
	}
	queryNorm := math.Sqrt(queryNormSq)

	// Score each document via dot product / (queryNorm * docNorm)
	type scored struct {
		docID string
		score float64
	}
	var results []scored
	for docID, docVec := range b.docTermFreqs {
		var dot float64
		for term, qw := range queryVec {
			if dw, ok := docVec[term]; ok {
				dot += qw * dw
			}
		}
		if dot <= 0 {
			continue
		}
		docNorm := b.docNorms[docID]
		if docNorm <= 0 {
			continue
		}
		score := dot / (queryNorm * docNorm)
		results = append(results, scored{docID: docID, score: score})
	}

	// Sort descending by score
	sort.Slice(results, func(i, j int) bool {
		return results[i].score > results[j].score
	})

	if topK > 0 && len(results) > topK {
		results = results[:topK]
	}

	out := make([]bm25Result, len(results))
	for i, r := range results {
		meta := b.docMeta[r.docID]
		out[i] = bm25Result{
			docID:     r.docID,
			toolName:  meta.toolName,
			groupName: meta.groupName,
			isMain:    meta.isMain,
			score:     r.score,
		}
	}
	return out
}

type bm25Result struct {
	docID     string
	toolName  string
	groupName string
	isMain    bool
	score     float64
}

// ---------------------------------------------------------------------------
// Hybrid search: vector + BM25 with Reciprocal Rank Fusion
// ---------------------------------------------------------------------------

// SearchHybrid runs both vector search and BM25 keyword search, then fuses
// results using Reciprocal Rank Fusion (RRF). This solves the "proper noun
// dilution" problem where unknown words (e.g., project names like "juicytrade")
// shift the dense embedding away from relevant tools, but keyword matching on
// known terms (e.g., "container") still finds the right tool.
//
// RRF formula: score = Σ 1/(k + rank_i) for each retrieval method.
// We use k=60, the standard value from the original RRF paper (Cormack et al. 2009).
//
// The minScore threshold applies to the final RRF score. With k=60 and two
// retrieval methods, the maximum possible RRF score is ~0.0328 (rank 1 in both).
// Reasonable thresholds: 0.005 (permissive) to 0.015 (moderate).
func (idx *ToolIndex) SearchHybrid(ctx context.Context, query string, topK int, minScore float64) ([]ToolMatch, error) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	if topK <= 0 {
		topK = 10
	}

	// We retrieve more candidates from each method than the final topK,
	// so RRF fusion has enough data to work with.
	candidateK := topK * 3
	if candidateK < 20 {
		candidateK = 20
	}

	// --- Vector search ---
	var vectorResults []ToolVectorResult
	docCount := idx.vectorStore.Count()
	if docCount > 0 {
		vK := candidateK
		if vK > docCount {
			vK = docCount
		}

		// Embed the query for vector search
		queryEmb, embErr := idx.embedFunc(ctx, query)
		if embErr == nil && len(queryEmb) > 0 {
			var err error
			vectorResults, err = idx.vectorStore.QueryByEmbedding(ctx, queryEmb, vK)
			if err != nil {
				return nil, fmt.Errorf("vector search failed: %w", err)
			}
		}
		// If embedding fails, fall through to BM25-only
	}

	// --- BM25 keyword search ---
	bm25Results := idx.bm25.search(query, candidateK)

	// --- Reciprocal Rank Fusion ---
	const rrfK = 60.0

	// Track RRF scores by tool_name (not docID, since vector and BM25 may
	// use different ID schemes — though here they share the same IDs).
	type fusedEntry struct {
		toolName  string
		groupName string
		isMain    bool
		rrfScore  float64
		vecScore  float64 // original vector similarity (for diagnostics)
		bm25Score float64 // original BM25 score (for diagnostics)
	}
	fused := make(map[string]*fusedEntry)

	// Add vector results
	for rank, r := range vectorResults {
		toolName := r.Metadata["tool_name"]
		if toolName == "" {
			continue
		}
		e, ok := fused[toolName]
		if !ok {
			e = &fusedEntry{
				toolName:  toolName,
				groupName: r.Metadata["group_name"],
				isMain:    r.Metadata["is_main"] == "true",
			}
			fused[toolName] = e
		}
		e.rrfScore += 1.0 / (rrfK + float64(rank+1))
		e.vecScore = float64(r.Similarity)
	}

	// Add BM25 results
	for rank, r := range bm25Results {
		e, ok := fused[r.toolName]
		if !ok {
			e = &fusedEntry{
				toolName:  r.toolName,
				groupName: r.groupName,
				isMain:    r.isMain,
			}
			fused[r.toolName] = e
		}
		e.rrfScore += 1.0 / (rrfK + float64(rank+1))
		e.bm25Score = r.score
	}

	// Collect and sort by RRF score
	results := make([]fusedEntry, 0, len(fused))
	for _, e := range fused {
		results = append(results, *e)
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].rrfScore > results[j].rrfScore
	})

	if len(results) > topK {
		results = results[:topK]
	}

	// Apply minScore threshold and build output
	var matches []ToolMatch
	for _, r := range results {
		if minScore > 0 && r.rrfScore < minScore {
			continue
		}

		// Resolve description from registry, fall back to stored content
		desc := ""
		if entry, ok := idx.toolRegistry[r.toolName]; ok {
			desc = entry.Description
		}

		matches = append(matches, ToolMatch{
			ToolName:    r.toolName,
			GroupName:   r.groupName,
			Description: desc,
			IsMainTool:  r.isMain,
			Score:       r.rrfScore,
		})
	}

	return matches, nil
}

// SearchGroupsHybrid is like SearchGroups but uses hybrid search (vector + BM25).
func (idx *ToolIndex) SearchGroupsHybrid(ctx context.Context, query string, topK int, minScore float64) []string {
	matches, err := idx.SearchHybrid(ctx, query, topK, minScore)
	if err != nil || len(matches) == 0 {
		return nil
	}

	seen := make(map[string]bool)
	var groups []string
	for _, m := range matches {
		if m.IsMainTool || seen[m.GroupName] {
			continue
		}
		seen[m.GroupName] = true
		groups = append(groups, m.GroupName)
	}
	sort.Strings(groups)
	return groups
}
