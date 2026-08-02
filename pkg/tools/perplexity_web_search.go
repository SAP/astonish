package tools

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/SAP/astonish/pkg/config"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
)

// PerplexityWebSearchArgs are the arguments exposed to the main model.
type PerplexityWebSearchArgs struct {
	Query       string   `json:"query" jsonschema:"Search query to answer using live web sources."`
	MaxResults  int      `json:"max_results,omitempty" jsonschema:"Maximum number of web sources to return. Defaults to the configured value."`
	Recency     string   `json:"recency,omitempty" jsonschema:"Optional freshness hint such as day, week, month, year, or recent."`
	Domains     []string `json:"domains,omitempty" jsonschema:"Optional list of domains to prefer or restrict in the search."`
	SearchMode  string   `json:"search_mode,omitempty" jsonschema:"Optional search mode, such as general, academic, news, or finance."`
	DetailLevel string   `json:"detail_level,omitempty" jsonschema:"Optional detail level: concise, normal, or detailed."`
}

// PerplexityWebSearchResult is returned to the main model as tool data.
type PerplexityWebSearchResult struct {
	Query         string                 `json:"query"`
	Provider      string                 `json:"provider"`
	Model         string                 `json:"model"`
	Answer        string                 `json:"answer"`
	Citations     []string               `json:"citations,omitempty"`
	SearchResults []PerplexitySearchHit  `json:"search_results,omitempty"`
	Warning       string                 `json:"warning,omitempty"`
	Metadata      map[string]interface{} `json:"metadata,omitempty"`
}

// PerplexitySearchHit is a normalized source returned by the model-backed search tool.
type PerplexitySearchHit struct {
	Title string `json:"title,omitempty"`
	URL   string `json:"url"`
}

// LLMFactory creates an LLM for the configured provider/model.
type LLMFactory func(ctx context.Context, providerName, modelName string, cfg *config.AppConfig) (model.LLM, error)

// NewPerplexityWebSearchTool creates the model-backed Perplexity/Sonar web search tool.
func NewPerplexityWebSearchTool(appCfg *config.AppConfig, llmFactory LLMFactory) (tool.Tool, error) {
	return functionToolNewPerplexity(appCfg, llmFactory)
}

// functionToolNewPerplexity is isolated to keep tests focused on RunPerplexityWebSearch.
func functionToolNewPerplexity(appCfg *config.AppConfig, llmFactory LLMFactory) (tool.Tool, error) {
	return newFunctionTool("perplexity_web_search", "Search the web using the configured Perplexity/Sonar model and return a concise sourced answer with citations.", func(ctx tool.Context, args PerplexityWebSearchArgs) (PerplexityWebSearchResult, error) {
		return RunPerplexityWebSearch(ctx, args, appCfg, llmFactory)
	})
}

// RunPerplexityWebSearch calls the configured Perplexity/Sonar model and normalizes the result.
func RunPerplexityWebSearch(ctx tool.Context, args PerplexityWebSearchArgs, appCfg *config.AppConfig, llmFactory LLMFactory) (PerplexityWebSearchResult, error) {
	args.Query = strings.TrimSpace(args.Query)
	if args.Query == "" {
		return PerplexityWebSearchResult{}, fmt.Errorf("query is required")
	}
	if appCfg == nil {
		return PerplexityWebSearchResult{}, fmt.Errorf("perplexity web search is not configured")
	}
	cfg := appCfg.PerplexityWebSearch
	if cfg.Provider == "" || cfg.Model == "" {
		return PerplexityWebSearchResult{}, fmt.Errorf("perplexity web search provider and model are not configured")
	}
	if llmFactory == nil {
		return PerplexityWebSearchResult{}, fmt.Errorf("perplexity web search LLM factory is not configured")
	}

	runCtx := context.Background()
	if ctx != nil {
		runCtx = ctx
	}
	llm, err := llmFactory(runCtx, cfg.Provider, cfg.Model, appCfg)
	if err != nil {
		return PerplexityWebSearchResult{}, fmt.Errorf("initialize perplexity search model: %w", err)
	}

	maxResults := args.MaxResults
	if maxResults <= 0 {
		maxResults = cfg.MaxResults
	}
	if maxResults <= 0 {
		maxResults = 5
	}
	if maxResults > 20 {
		maxResults = 20
	}

	prompt := buildPerplexitySearchPrompt(args, cfg.SearchContextSize, maxResults)
	req := &model.LLMRequest{
		Model:    cfg.Model,
		Contents: []*genai.Content{genai.NewContentFromText(prompt, genai.RoleUser)},
		Config: &genai.GenerateContentConfig{
			Temperature:     genai.Ptr(float32(0.1)),
			MaxOutputTokens: 4096,
		},
	}

	var answer strings.Builder
	var lastResp *model.LLMResponse
	for resp, genErr := range llm.GenerateContent(runCtx, req, false) {
		if genErr != nil {
			return PerplexityWebSearchResult{}, fmt.Errorf("run perplexity search model: %w", genErr)
		}
		if resp == nil {
			continue
		}
		lastResp = resp
		answer.WriteString(contentText(resp.Content))
	}

	answerText := strings.TrimSpace(answer.String())
	citations, hits := extractSources(answerText, lastResp)
	result := PerplexityWebSearchResult{
		Query:         args.Query,
		Provider:      cfg.Provider,
		Model:         cfg.Model,
		Answer:        answerText,
		Citations:     citations,
		SearchResults: hits,
	}
	if len(citations) == 0 {
		result.Warning = "The configured model did not return machine-readable citations. Treat the answer as unverified unless URLs are present in the answer text."
	}
	if lastResp != nil {
		result.Metadata = responseMetadata(lastResp)
	}
	return result, nil
}

func buildPerplexitySearchPrompt(args PerplexityWebSearchArgs, defaultContextSize string, maxResults int) string {
	contextSize := strings.TrimSpace(defaultContextSize)
	if contextSize == "" {
		contextSize = "medium"
	}
	detail := strings.TrimSpace(args.DetailLevel)
	if detail == "" {
		detail = "normal"
	}
	mode := strings.TrimSpace(args.SearchMode)
	if mode == "" {
		mode = "general"
	}

	var sb strings.Builder
	sb.WriteString("You are being used as a web search tool inside Astonish. Perform live web research for the user query, then return only useful search data for another model to consume.\n")
	sb.WriteString("Use web search/grounding capabilities available to this Sonar/Perplexity model. Do not answer from memory when current web evidence is needed.\n")
	sb.WriteString("Return a concise answer followed by a Sources section. Include one source per line with title and URL.\n")
	sb.WriteString(fmt.Sprintf("Maximum sources: %d. Detail level: %s. Search mode: %s. Search context size: %s.\n", maxResults, detail, mode, contextSize))
	if args.Recency != "" {
		sb.WriteString("Freshness/recency hint: ")
		sb.WriteString(args.Recency)
		sb.WriteString(". Prefer current sources matching this hint.\n")
	}
	if len(args.Domains) > 0 {
		sb.WriteString("Domain preference/restriction: ")
		sb.WriteString(strings.Join(args.Domains, ", "))
		sb.WriteString(". Prefer these domains when relevant.\n")
	}
	sb.WriteString("Query: ")
	sb.WriteString(args.Query)
	return sb.String()
}

func contentText(content *genai.Content) string {
	if content == nil {
		return ""
	}
	var sb strings.Builder
	for _, part := range content.Parts {
		if part != nil && part.Text != "" {
			sb.WriteString(part.Text)
		}
	}
	return sb.String()
}

func extractSources(answer string, resp *model.LLMResponse) ([]string, []PerplexitySearchHit) {
	seen := map[string]bool{}
	var hits []PerplexitySearchHit
	add := func(title, rawURL string) {
		u := strings.Trim(strings.TrimSpace(rawURL), "[]()<>.,;\"'")
		if u == "" || seen[u] {
			return
		}
		if parsed, err := url.Parse(u); err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return
		}
		seen[u] = true
		hits = append(hits, PerplexitySearchHit{Title: strings.TrimSpace(title), URL: u})
	}

	if resp != nil {
		if resp.CitationMetadata != nil {
			for _, c := range resp.CitationMetadata.Citations {
				if c != nil {
					add(c.Title, c.URI)
				}
			}
		}
		if resp.GroundingMetadata != nil {
			for _, c := range resp.GroundingMetadata.GroundingChunks {
				if c == nil {
					continue
				}
				if c.Web != nil {
					add(c.Web.Title, c.Web.URI)
				}
				if c.RetrievedContext != nil {
					add(c.RetrievedContext.Title, c.RetrievedContext.URI)
				}
			}
		}
	}

	for _, match := range urlRegexp.FindAllString(answer, -1) {
		add("", match)
	}

	sort.SliceStable(hits, func(i, j int) bool { return hits[i].URL < hits[j].URL })
	citations := make([]string, 0, len(hits))
	for _, hit := range hits {
		citations = append(citations, hit.URL)
	}
	return citations, hits
}

var urlRegexp = regexp.MustCompile(`https?://[^\s\])}>"']+`)

func responseMetadata(resp *model.LLMResponse) map[string]interface{} {
	metadata := map[string]interface{}{}
	if resp.CitationMetadata != nil {
		metadata["citation_metadata"] = resp.CitationMetadata
	}
	if resp.GroundingMetadata != nil {
		metadata["grounding_metadata"] = resp.GroundingMetadata
	}
	if resp.UsageMetadata != nil {
		metadata["usage_metadata"] = resp.UsageMetadata
	}
	for k, v := range resp.CustomMetadata {
		metadata[k] = v
	}
	if len(metadata) == 0 {
		return nil
	}
	return metadata
}

// test hook to avoid repeating functiontool.New generic syntax in tests.
var newFunctionTool = func(name, description string, handler func(tool.Context, PerplexityWebSearchArgs) (PerplexityWebSearchResult, error)) (tool.Tool, error) {
	return functiontoolNew(name, description, handler)
}

func functiontoolNew(name, description string, handler func(tool.Context, PerplexityWebSearchArgs) (PerplexityWebSearchResult, error)) (tool.Tool, error) {
	return functiontool.New(functiontool.Config{Name: name, Description: description}, handler)
}
