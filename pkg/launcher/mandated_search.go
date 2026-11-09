package launcher

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

const BotConfiguredSearchToolName = "perplexity_web_search"

// BotConfiguredSearchSource reloads the explicit Chat search executable and its
// scoped vault. Missing config/credentials MUST error; no host fallback is valid.
// Composition must bind this source to the mandate's saved tenant/owner.
type BotConfiguredSearchSource func(context.Context) (tools.ConfiguredSearchBackend, store.CredentialStore, error)

// BotConfiguredSearchResources must be admitted with the user's bounded public
// search intent. Queries are exact, not model-generated disclosures. This grants
// neither arbitrary prompts nor general model access. Do not derive query grants
// from untrusted retrieved content or private task memory.
func BotConfiguredSearchResources(b tools.ConfiguredSearchBackend, queries []string) ([]string, error) {
	if err := b.Validate(); err != nil {
		return nil, err
	}
	encoded, _ := json.Marshal(b)
	sum := sha256.Sum256(encoded)
	origin, _ := tools.RoutineReadOrigin(b.Endpoint)
	resources := []string{BotConfiguredSearchToolName, origin, "search:backend:" + hex.EncodeToString(sum[:])}
	for _, query := range queries {
		if strings.TrimSpace(query) == "" || len(query) > 2048 || strings.Contains(query, "{{") {
			return nil, store.ErrBotForbidden
		}
		sum := sha256.Sum256([]byte(query))
		resources = append(resources, "search:query:"+hex.EncodeToString(sum[:]))
	}
	return resources, nil
}

type botConfiguredSearchArgs struct {
	Query string `json:"query" jsonschema:"Exact public search query authorized by the saved mandate; no private context or secrets."`
}
type botMandatedSearchTool struct {
	botRunnableTool
	mandate *bot.MandateExecution
	source  BotConfiguredSearchSource
	network tools.RoutineReadPolicy
}

// BotMandatedConfiguredSearchTool builds the only trusted search read adapter.
// Append to the mandate catalogue before WithBotMandatedReadClassifier. Native
// search tools with the same name do not acquire this classification.
func BotMandatedConfiguredSearchTool(m *bot.MandateExecution, source BotConfiguredSearchSource, network tools.RoutineReadPolicy) (tool.Tool, error) {
	if m == nil || source == nil || network.Authorize == nil {
		return nil, store.ErrBotForbidden
	}
	t := &botMandatedSearchTool{mandate: m, source: source, network: network}
	native, err := functiontool.New(functiontool.Config{Name: BotConfiguredSearchToolName, Description: "Search public sources using the saved mandate's exact query and configured backend"}, func(c tool.Context, a botConfiguredSearchArgs) (tools.PerplexityWebSearchResult, error) {
		b, token, p, err := t.prepare(c, a.Query)
		if err != nil {
			return tools.PerplexityWebSearchResult{}, err
		}
		// Reserve a conservative bounded search-model token envelope before I/O.
		// Tool-call quota is independently charged by the bound dispatcher.
		if err := m.Runtime.ConsumeBotMandateQuota(c, m.Authorization.Ref(), m.Identity.RunID, 0, 8192, 0); err != nil {
			return tools.PerplexityWebSearchResult{}, err
		}
		return tools.RunConfiguredSearch(c, a.Query, b, token, p)
	})
	if err != nil {
		return nil, err
	}
	t.botRunnableTool = native.(botRunnableTool)
	return t, nil
}
func (t *botMandatedSearchTool) prepare(ctx context.Context, query string) (tools.ConfiguredSearchBackend, string, tools.RoutineReadPolicy, error) {
	var empty tools.ConfiguredSearchBackend
	deny := func() (tools.ConfiguredSearchBackend, string, tools.RoutineReadPolicy, error) {
		return empty, "", tools.RoutineReadPolicy{}, store.ErrBotForbidden
	}
	if t.mandate.Check(ctx) != nil {
		return deny()
	}
	b, vault, err := t.source(ctx)
	if err != nil || vault == nil {
		return deny()
	}
	resources, err := BotConfiguredSearchResources(b, []string{query})
	if err != nil {
		return deny()
	}
	for _, r := range resources {
		if t.mandate.Authorization.Authorize(execution.CapabilityToolExecute, r) != nil {
			return deny()
		}
	}
	origin, _ := tools.RoutineReadOrigin(b.Endpoint)
	if t.network.Authorize(ctx, origin) != nil {
		return deny()
	}
	// Get, never Resolve: OAuth refresh could perform uncontrolled network I/O.
	credential := vault.Get(ctx, b.Credential)
	if credential == nil {
		return deny()
	}
	token := ""
	switch credential.Type {
	case store.CredAPIKey:
		token = credential.Value
	case store.CredBearer:
		token = credential.Token
	default:
		return deny()
	}
	if token == "" || strings.ContainsAny(token, "\r\n") || strings.Contains(query, token) {
		return deny()
	}
	p := tools.RoutineReadPolicy{Authorize: func(c context.Context, want string) error {
		if want != origin || t.mandate.Check(c) != nil {
			return store.ErrBotForbidden
		}
		current, _, e := t.source(c)
		if e != nil || current != b {
			return store.ErrBotForbidden
		}
		for _, r := range resources {
			if t.mandate.Authorization.Authorize(execution.CapabilityToolExecute, r) != nil {
				return store.ErrBotForbidden
			}
		}
		return t.network.Authorize(c, want)
	}, AllowIP: func(c context.Context, address string, ip net.IP) error {
		// Exact origin above is mandatory even for public destinations. No wildcard
		// public-read grant authorizes sending an authenticated search prompt.
		if t.network.AllowIP != nil {
			return t.network.AllowIP(c, address, ip)
		}
		if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			return store.ErrBotForbidden
		}
		return nil
	}}
	return b, token, p, nil
}
func (t *botMandatedSearchTool) describe(ctx context.Context, raw json.RawMessage) (execution.ActionDescriptor, error) {
	var args botConfiguredSearchArgs
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&args); err != nil {
		return execution.ActionDescriptor{}, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return execution.ActionDescriptor{}, fmt.Errorf("invalid search arguments")
	}
	if _, _, _, err := t.prepare(ctx, args.Query); err != nil {
		return execution.ActionDescriptor{}, err
	}
	return execution.ActionDescriptor{Tool: BotConfiguredSearchToolName, Arguments: raw, Effect: execution.EffectRead, ConstraintsVerified: true}, nil
}
func (t *botMandatedSearchTool) Run(c tool.Context, a any) (map[string]any, error) {
	raw, err := json.Marshal(a)
	if err != nil {
		return nil, err
	}
	if _, err = t.describe(c, raw); err != nil {
		return nil, err
	}
	return t.botRunnableTool.Run(c, a)
}
