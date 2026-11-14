package execution

import (
	"errors"
	"slices"
	"strings"
)

// BotPublicResearchScope is an immutable, owner-backed public disclosure grant.
// Subject is admitted independently of private assignment text. Secrets are never
// stored here: CredentialHandles contains symbolic vault/provider handles only.
type BotPublicResearchScope struct {
	Version       int                      `json:"version"`
	Subject       string                   `json:"subject"`
	SourceEventID string                   `json:"source_event_id"`
	Route         BotConfiguredSearchRoute `json:"route"`
	Facets        []BotResearchFacet       `json:"facets"`
	MaxQueryBytes int                      `json:"max_query_bytes"`
	MaxResults    int                      `json:"max_results"`
	MaxCalls      int64                    `json:"max_calls"`
}
type BotSearchEngine string

const (
	BotSearchPerplexity BotSearchEngine = "perplexity"
	BotSearchTavily     BotSearchEngine = "tavily"
	BotSearchBrave      BotSearchEngine = "brave"
	BotSearchFirecrawl  BotSearchEngine = "firecrawl"
)

func (e BotSearchEngine) IsMCP() bool {
	return e == BotSearchTavily || e == BotSearchBrave || e == BotSearchFirecrawl
}

// BotSearchEngineForTool recognizes only implemented search executables, not
// extract/local-search siblings. The selected server name remains exact.
func BotSearchEngineForTool(name string) (BotSearchEngine, error) {
	switch name {
	case "perplexity_web_search":
		return BotSearchPerplexity, nil
	case "tavily_search", "tavily-search":
		return BotSearchTavily, nil
	case "brave_web_search":
		return BotSearchBrave, nil
	case "firecrawl_search":
		return BotSearchFirecrawl, nil
	default:
		return "", errors.New("unsupported configured search executable")
	}
}

type BotConfiguredSearchRoute struct {
	Engine            BotSearchEngine `json:"engine"`
	Provider          string          `json:"provider,omitempty"`
	Model             string          `json:"model,omitempty"`
	Tool              string          `json:"tool"`
	Fingerprint       string          `json:"fingerprint"`
	CredentialHandles []string        `json:"credential_handles"`
}
type BotResearchFacet string

const (
	BotResearchOverview       BotResearchFacet = "overview"
	BotResearchRecent         BotResearchFacet = "recent"
	BotResearchPrimary        BotResearchFacet = "primary"
	BotResearchContradictions BotResearchFacet = "contradictions"
)

func (s BotPublicResearchScope) Clone() BotPublicResearchScope {
	s.Facets = slices.Clone(s.Facets)
	s.Route.CredentialHandles = slices.Clone(s.Route.CredentialHandles)
	return s
}
func (s BotPublicResearchScope) Validate() error {
	if s.Version != 1 || strings.TrimSpace(s.Subject) != s.Subject || s.Subject == "" || len(s.Subject) > 512 || strings.ContainsAny(s.Subject, "\r\n\x00") || strings.Contains(s.Subject, "{{") || s.SourceEventID == "" || s.MaxQueryBytes < len(s.Subject)+32 || s.MaxQueryBytes > 2048 || s.MaxResults < 1 || s.MaxResults > 20 || s.MaxCalls < 1 || s.MaxCalls > 20 || len(s.Facets) == 0 || len(s.Facets) > 4 {
		return errors.New("invalid bounded public research scope")
	}
	for _, f := range s.Facets {
		if _, err := s.Query(f); err != nil {
			return err
		}
	}
	r := s.Route
	if r.Tool == "" || r.Fingerprint == "" || len(r.CredentialHandles) == 0 || len(r.CredentialHandles) > 16 {
		return errors.New("configured search binding required")
	}
	if r.Engine != BotSearchPerplexity && !r.Engine.IsMCP() {
		return errors.New("unsupported configured search engine")
	}
	if r.Engine == BotSearchPerplexity && (r.Provider == "" || r.Model == "") {
		return errors.New("exact search provider/model required")
	}
	if r.Engine.IsMCP() {
		parts := strings.Split(strings.TrimPrefix(r.Tool, "mcp:"), "/")
		if !strings.HasPrefix(r.Tool, "mcp:") || len(parts) != 2 || parts[0] == "" || strings.ContainsAny(parts[0], ":\r\n\x00") || r.Provider != "" || r.Model != "" {
			return errors.New("exact MCP search executable required")
		}
		engine, err := BotSearchEngineForTool(parts[1])
		if err != nil || engine != r.Engine {
			return errors.New("MCP search engine/executable mismatch")
		}
	}
	for _, h := range r.CredentialHandles {
		if h == "" || len(h) > 256 || strings.ContainsAny(h, "\r\n\x00") {
			return errors.New("symbolic credential handle required")
		}
	}
	return nil
}
func (s BotPublicResearchScope) Query(f BotResearchFacet) (string, error) {
	if !slices.Contains(s.Facets, f) {
		return "", errors.New("public research facet not admitted; ask owner to clarify public subject")
	}
	var suffix string
	switch f {
	case BotResearchOverview:
		suffix = "overview"
	case BotResearchRecent:
		suffix = "recent developments"
	case BotResearchPrimary:
		suffix = "primary official sources"
	case BotResearchContradictions:
		suffix = "contradictions and corrections"
	default:
		return "", errors.New("unknown public research facet")
	}
	q := s.Subject + " " + suffix
	if len(q) > s.MaxQueryBytes {
		return "", errors.New("public research query ceiling exceeded")
	}
	return q, nil
}
func (s BotPublicResearchScope) ValidateWithin(c *BotPublicResearchScope) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if c == nil {
		return errors.New("public research no longer authorized")
	}
	if err := c.Validate(); err != nil {
		return err
	}
	// Current policy can narrow quotas/facets, never replace disclosure provenance.
	a, b := s.Route, c.Route
	if s.Subject != c.Subject || s.SourceEventID != c.SourceEventID || a.Engine != b.Engine || a.Provider != b.Provider || a.Model != b.Model || a.Tool != b.Tool || a.Fingerprint != b.Fingerprint || !slices.Equal(a.CredentialHandles, b.CredentialHandles) || s.MaxCalls > c.MaxCalls || s.MaxResults > c.MaxResults || s.MaxQueryBytes > c.MaxQueryBytes {
		return errors.New("public research exceeds current policy")
	}
	for _, f := range s.Facets {
		if !slices.Contains(c.Facets, f) {
			return errors.New("public research facet revoked")
		}
	}
	return nil
}
