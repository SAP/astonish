package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

// credentialStoreVar holds the file-based credential store reference.
// Set by the daemon or console launcher via SetCredentialStore.
// Used as fallback when no platform credential store is available in the context.
var credentialStoreVar *credentials.Store

// SetCredentialStore registers the credential store for LLM tool access.
func SetCredentialStore(store *credentials.Store) {
	credentialStoreVar = store
}

// GetCredentialStore returns the current credential store (for redaction wiring).
func GetCredentialStore() *credentials.Store {
	return credentialStoreVar
}

// getEffectiveCredStore returns the tenant-scoped credential store from the
// context (injected by ChatRunner in platform mode).
// Falls back to the global credentialStoreVar (file-based, for console mode and tests).
// Returns nil if no store is available.
func getEffectiveCredStore(ctx context.Context) store.CredentialStore {
	if ctx != nil {
		if cs := store.CredentialStoreFromContext(ctx); cs != nil {
			return cs
		}
	}
	// Fallback: wrap the file-based credential store for console/test use.
	if credentialStoreVar != nil {
		return &fileCredStoreAdapter{s: credentialStoreVar}
	}
	return nil
}

// fileCredStoreAdapter adapts *credentials.Store (file-based, no context)
// to the store.CredentialStore interface. Used as fallback in console mode
// and unit tests where no platform DB is available.
type fileCredStoreAdapter struct {
	s *credentials.Store
}

func (a *fileCredStoreAdapter) Get(_ context.Context, name string) *store.Credential {
	c := a.s.Get(name)
	if c == nil {
		return nil
	}
	return credToStoreCred(c)
}

func (a *fileCredStoreAdapter) Set(_ context.Context, name string, cred *store.Credential) error {
	return a.s.Set(name, storeCredToCred(cred))
}

func (a *fileCredStoreAdapter) Remove(_ context.Context, name string) error {
	return a.s.Remove(name)
}

func (a *fileCredStoreAdapter) List(_ context.Context) map[string]store.CredentialType {
	raw := a.s.List()
	out := make(map[string]store.CredentialType, len(raw))
	for k, v := range raw {
		out[k] = store.CredentialType(v)
	}
	return out
}

func (a *fileCredStoreAdapter) Count(_ context.Context) int {
	return a.s.Count()
}

func (a *fileCredStoreAdapter) Resolve(_ context.Context, name string) (string, string, error) {
	return a.s.Resolve(name)
}

func (a *fileCredStoreAdapter) InvalidateToken(_ context.Context, name string) {
	a.s.InvalidateToken(name)
}

func (a *fileCredStoreAdapter) SetSecret(_ context.Context, key, value string) error {
	return a.s.SetSecret(key, value)
}

func (a *fileCredStoreAdapter) SetSecretBatch(_ context.Context, secrets map[string]string) error {
	return a.s.SetSecretBatch(secrets)
}

func (a *fileCredStoreAdapter) GetSecret(_ context.Context, key string) string {
	return a.s.GetSecret(key)
}

func (a *fileCredStoreAdapter) RemoveSecret(_ context.Context, key string) error {
	return a.s.RemoveSecret(key)
}

func (a *fileCredStoreAdapter) HasSecrets(_ context.Context) bool {
	return a.s.HasSecrets()
}

func (a *fileCredStoreAdapter) SecretCount(_ context.Context) int {
	return a.s.SecretCount()
}

func (a *fileCredStoreAdapter) ListSecrets(_ context.Context) []string {
	return a.s.ListSecrets()
}

func (a *fileCredStoreAdapter) Reload(_ context.Context) error {
	return a.s.Reload()
}

// credToStoreCred converts a file-based credentials.Credential to a store.Credential.
func credToStoreCred(c *credentials.Credential) *store.Credential {
	return &store.Credential{
		Type:         store.CredentialType(c.Type),
		Header:       c.Header,
		Value:        c.Value,
		Token:        c.Token,
		Username:     c.Username,
		Password:     c.Password,
		AuthURL:      c.AuthURL,
		ClientID:     c.ClientID,
		ClientSecret: c.ClientSecret,
		Scope:        c.Scope,
		TokenURL:     c.TokenURL,
		AccessToken:  c.AccessToken,
		RefreshToken: c.RefreshToken,
		TokenExpiry:  c.TokenExpiry,
		Content:      c.Content,
		ContentType:  c.ContentType,
	}
}

// storeCredToCred converts a store.Credential to a file-based credentials.Credential.
func storeCredToCred(c *store.Credential) *credentials.Credential {
	return &credentials.Credential{
		Type:         credentials.CredentialType(c.Type),
		Header:       c.Header,
		Value:        c.Value,
		Token:        c.Token,
		Username:     c.Username,
		Password:     c.Password,
		AuthURL:      c.AuthURL,
		ClientID:     c.ClientID,
		ClientSecret: c.ClientSecret,
		Scope:        c.Scope,
		TokenURL:     c.TokenURL,
		AccessToken:  c.AccessToken,
		RefreshToken: c.RefreshToken,
		TokenExpiry:  c.TokenExpiry,
		Content:      c.Content,
		ContentType:  c.ContentType,
	}
}

// --- save_credential tool ---

type SaveCredentialArgs struct {
	Name                        string `json:"name" jsonschema:"Short identifier for this credential (e.g., 'my-api', 'proxmox', 'google-calendar')"`
	Type                        string `json:"type" jsonschema:"Credential type: 'api_key' (custom header+value), 'bearer' (Authorization: Bearer token), 'basic' (HTTP Basic Auth), 'password' (plain username+password for SSH/FTP/SMTP/databases), 'oauth_client_credentials' (auto-refreshing OAuth2 server-to-server), 'oauth_authorization_code' (user-authorized OAuth2 with refresh token — Google, GitHub, etc.), 'openstack_keystone' (OpenStack Keystone v3 token auth — password or application credential), 'raw_content' (arbitrary JSON/YAML/text file content for sandbox file injection; not HTTP auth)"`
	Header                      string `json:"header,omitempty" jsonschema:"Header name for api_key type (e.g., 'X-API-Key', 'Authorization'). Required for api_key type."`
	Value                       string `json:"value,omitempty" jsonschema:"The API key value. Required for api_key type."`
	Token                       string `json:"token,omitempty" jsonschema:"The bearer token. Required for bearer type."`
	Username                    string `json:"username,omitempty" jsonschema:"Username for basic, password, or openstack_keystone password auth. Required for basic/password; for Keystone password method."`
	Password                    string `json:"password,omitempty" jsonschema:"Password for basic, password, or openstack_keystone password auth."`
	AuthURL                     string `json:"auth_url,omitempty" jsonschema:"OAuth token endpoint URL (oauth_client_credentials) or Keystone tokens URL (openstack_keystone, e.g. https://identity.example.com/v3/auth/tokens)."`
	ClientID                    string `json:"client_id,omitempty" jsonschema:"OAuth client ID. Required for oauth_client_credentials and oauth_authorization_code types."`
	ClientSecret                string `json:"client_secret,omitempty" jsonschema:"OAuth client secret. Required for oauth_client_credentials and oauth_authorization_code types."`
	Scope                       string `json:"scope,omitempty" jsonschema:"OAuth scope (optional, for oauth_client_credentials and oauth_authorization_code types)."`
	TokenURL                    string `json:"token_url,omitempty" jsonschema:"Token endpoint URL for exchanging/refreshing tokens. Required for oauth_authorization_code type (e.g., https://oauth2.googleapis.com/token)."`
	AccessToken                 string `json:"access_token,omitempty" jsonschema:"Current access token. Required for oauth_authorization_code type."`
	RefreshToken                string `json:"refresh_token,omitempty" jsonschema:"Refresh token for obtaining new access tokens. Required for oauth_authorization_code type."`
	UserDomain                  string `json:"user_domain,omitempty" jsonschema:"Keystone user domain name (openstack_keystone password auth). Defaults to 'Default'."`
	ProjectID                   string `json:"project_id,omitempty" jsonschema:"Keystone project ID (openstack_keystone password auth). Provide project_id or project_name."`
	ProjectName                 string `json:"project_name,omitempty" jsonschema:"Keystone project name (openstack_keystone password auth). Used with project_domain when project_id is empty."`
	ProjectDomain               string `json:"project_domain,omitempty" jsonschema:"Keystone project domain name (openstack_keystone password auth). Defaults to 'Default'."`
	ApplicationCredentialID     string `json:"application_credential_id,omitempty" jsonschema:"Keystone application credential ID (openstack_keystone). Prefer over password auth when available."`
	ApplicationCredentialSecret string `json:"application_credential_secret,omitempty" jsonschema:"Keystone application credential secret (openstack_keystone)."`
	Content                     string `json:"content,omitempty" jsonschema:"Raw JSON, YAML, dotenv, or text content. Required for raw_content type. Preserve exact newlines and formatting."`
	ContentType                 string `json:"content_type,omitempty" jsonschema:"Optional MIME/content hint for raw_content, e.g. text/plain, application/json, application/yaml, text/x-dotenv."`
}

type SaveCredentialResult struct {
	ToolResult
}

func saveCredential(ctx tool.Context, args SaveCredentialArgs) (SaveCredentialResult, error) {
	cs := getEffectiveCredStore(ctx)
	if cs == nil {
		return SaveCredentialResult{ToolResult: toolError("Credential store is not available")}, nil
	}

	if args.Name == "" {
		return SaveCredentialResult{ToolResult: toolError("Name is required")}, nil
	}

	var storeCred *store.Credential

	credType := store.CredentialType(args.Type)
	switch credType {
	case store.CredAPIKey:
		if args.Header == "" {
			return SaveCredentialResult{ToolResult: toolError("Header is required for api_key type")}, nil
		}
		if args.Value == "" {
			return SaveCredentialResult{ToolResult: toolError("Value is required for api_key type")}, nil
		}
		storeCred = &store.Credential{
			Type:   store.CredAPIKey,
			Header: args.Header,
			Value:  args.Value,
		}

	case store.CredBearer:
		if args.Token == "" {
			return SaveCredentialResult{ToolResult: toolError("Token is required for bearer type")}, nil
		}
		storeCred = &store.Credential{
			Type:  store.CredBearer,
			Token: args.Token,
		}

	case store.CredBasic:
		if args.Username == "" {
			return SaveCredentialResult{ToolResult: toolError("Username is required for basic type")}, nil
		}
		storeCred = &store.Credential{
			Type:     store.CredBasic,
			Username: args.Username,
			Password: args.Password,
		}

	case store.CredOAuthClientCreds:
		if args.AuthURL == "" {
			return SaveCredentialResult{ToolResult: toolError("auth_url is required for oauth_client_credentials type")}, nil
		}
		if args.ClientID == "" {
			return SaveCredentialResult{ToolResult: toolError("client_id is required for oauth_client_credentials type")}, nil
		}
		if args.ClientSecret == "" {
			return SaveCredentialResult{ToolResult: toolError("client_secret is required for oauth_client_credentials type")}, nil
		}
		storeCred = &store.Credential{
			Type:         store.CredOAuthClientCreds,
			AuthURL:      args.AuthURL,
			ClientID:     args.ClientID,
			ClientSecret: args.ClientSecret,
			Scope:        args.Scope,
		}

	case store.CredPassword:
		if args.Username == "" {
			return SaveCredentialResult{ToolResult: toolError("Username is required for password type")}, nil
		}
		storeCred = &store.Credential{
			Type:     store.CredPassword,
			Username: args.Username,
			Password: args.Password,
		}

	case store.CredOAuthAuthCode:
		if args.TokenURL == "" {
			return SaveCredentialResult{ToolResult: toolError("token_url is required for oauth_authorization_code type (e.g., https://oauth2.googleapis.com/token)")}, nil
		}
		if args.ClientID == "" {
			return SaveCredentialResult{ToolResult: toolError("client_id is required for oauth_authorization_code type")}, nil
		}
		if args.ClientSecret == "" {
			return SaveCredentialResult{ToolResult: toolError("client_secret is required for oauth_authorization_code type")}, nil
		}
		if args.AccessToken == "" {
			return SaveCredentialResult{ToolResult: toolError("access_token is required for oauth_authorization_code type — exchange the authorization code first, then save the resulting tokens")}, nil
		}
		if args.RefreshToken == "" {
			return SaveCredentialResult{ToolResult: toolError("refresh_token is required for oauth_authorization_code type — ensure access_type=offline was used in the authorization URL")}, nil
		}
		storeCred = &store.Credential{
			Type:         store.CredOAuthAuthCode,
			TokenURL:     args.TokenURL,
			ClientID:     args.ClientID,
			ClientSecret: args.ClientSecret,
			AccessToken:  args.AccessToken,
			RefreshToken: args.RefreshToken,
			Scope:        args.Scope,
		}

	case store.CredRawContent:
		if args.Content == "" {
			return SaveCredentialResult{ToolResult: toolError("content is required for raw_content type")}, nil
		}
		storeCred = &store.Credential{
			Type:        store.CredRawContent,
			Content:     args.Content,
			ContentType: args.ContentType,
		}

	case store.CredOpenStackKeystone:
		if args.AuthURL == "" {
			return SaveCredentialResult{ToolResult: toolError("auth_url is required for openstack_keystone type (e.g., https://identity.example.com/v3/auth/tokens)")}, nil
		}
		hasAppCred := args.ApplicationCredentialID != "" && args.ApplicationCredentialSecret != ""
		hasPassword := args.Username != "" && args.Password != ""
		if !hasAppCred && !hasPassword {
			return SaveCredentialResult{ToolResult: toolError("openstack_keystone requires either application_credential_id+application_credential_secret, or username+password")}, nil
		}
		if !hasAppCred {
			if args.ProjectID == "" && args.ProjectName == "" {
				return SaveCredentialResult{ToolResult: toolError("openstack_keystone password auth requires project_id or project_name")}, nil
			}
		}
		storeCred = &store.Credential{
			Type:                        store.CredOpenStackKeystone,
			AuthURL:                     args.AuthURL,
			Username:                    args.Username,
			Password:                    args.Password,
			UserDomain:                  args.UserDomain,
			ProjectID:                   args.ProjectID,
			ProjectName:                 args.ProjectName,
			ProjectDomain:               args.ProjectDomain,
			ApplicationCredentialID:     args.ApplicationCredentialID,
			ApplicationCredentialSecret: args.ApplicationCredentialSecret,
		}

	default:
		return SaveCredentialResult{
			ToolResult: toolError("Unknown type %q. Use: api_key, bearer, basic, password, oauth_client_credentials, oauth_authorization_code, openstack_keystone, or raw_content", args.Type),
		}, nil
	}

	if err := cs.Set(ctx, args.Name, storeCred); err != nil {
		return SaveCredentialResult{
			ToolResult: toolError("Failed to save: %v", err),
		}, nil
	}

	// Immediately register the new credential's secret values with the
	// Redactor so that tool outputs in the same session are protected.
	if r := credentials.RedactorFromContext(ctx); r != nil {
		r.HydrateFromStore(cs)
	}

	message := fmt.Sprintf("Credential %q saved (%s). The value is encrypted and will not appear in conversation history.", args.Name, args.Type)
	if credType == store.CredRawContent {
		message += fmt.Sprintf(" Use it with resolve_credential field content or fleet credential_injection files using field content. IMPORTANT: Now save a memory note documenting credential name %q, the intended file path/use, and content type only. Do NOT include the actual content in the memory note.", args.Name)
	} else {
		message += fmt.Sprintf(" IMPORTANT: Now save a memory note documenting: (1) credential name %q, (2) what format/prefix is already included in the stored value (so you never double-prefix it), (3) the header name and how to use it with api_call, (4) the target API base URL. Do NOT include the actual secret value in the memory note — reference it by name only.", args.Name)
	}

	return SaveCredentialResult{
		ToolResult: ToolResult{Status: "saved", Message: message},
	}, nil
}

// --- list_credentials tool ---

type ListCredentialsArgs struct {
	Filter string `json:"filter,omitempty" jsonschema:"Optional filter to match by name/key (e.g., 'anthropic', 'telegram'). If empty, lists everything."`
}

type CredentialSummary struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Scope    string `json:"scope,omitempty"`    // "personal" or "team" — only set in platform mode
	Shadowed bool   `json:"shadowed,omitempty"` // true if a personal credential overrides this team credential
}

type SecretSummary struct {
	Key   string `json:"key"`
	Scope string `json:"scope,omitempty"` // "personal" or "team" — only set in platform mode
}

type ListCredentialsResult struct {
	Credentials []CredentialSummary `json:"credentials"`
	Secrets     []SecretSummary     `json:"secrets"`
	Count       int                 `json:"count"`
	Message     string              `json:"message,omitempty"`
}

func listCredentials(ctx tool.Context, args ListCredentialsArgs) (ListCredentialsResult, error) {
	cs := getEffectiveCredStore(ctx)
	if cs == nil {
		return ListCredentialsResult{Message: "Credential store is not available"}, nil
	}

	// Reload to pick up changes made outside this process
	cs.Reload(ctx) //nolint:errcheck

	// Check if we have a merged store for scope-aware listing
	merged, isMerged := cs.(*store.MergedCredentialStore)

	var credSummaries []CredentialSummary
	var secretSummaries []SecretSummary

	if isMerged {
		// Platform mode: list from both stores with scope labels
		personalCreds := make(map[string]store.CredentialType)
		teamCreds := make(map[string]store.CredentialType)
		personalSecrets := make(map[string]bool)

		if merged.Personal != nil {
			personalCreds = merged.Personal.List(ctx)
			for _, k := range merged.Personal.ListSecrets(ctx) {
				personalSecrets[k] = true
			}
		}
		if merged.Team != nil {
			teamCreds = merged.Team.List(ctx)
		}

		// Personal credentials first
		for name, credType := range personalCreds {
			if args.Filter != "" && !strings.Contains(name, args.Filter) {
				continue
			}
			credSummaries = append(credSummaries, CredentialSummary{
				Name:  name,
				Type:  string(credType),
				Scope: "personal",
			})
		}

		// Team credentials (mark shadowed if personal has same name)
		for name, credType := range teamCreds {
			if args.Filter != "" && !strings.Contains(name, args.Filter) {
				continue
			}
			_, shadowed := personalCreds[name]
			credSummaries = append(credSummaries, CredentialSummary{
				Name:     name,
				Type:     string(credType),
				Scope:    "team",
				Shadowed: shadowed,
			})
		}

		// Personal secrets
		if merged.Personal != nil {
			for _, key := range merged.Personal.ListSecrets(ctx) {
				if args.Filter != "" && !strings.Contains(key, args.Filter) {
					continue
				}
				secretSummaries = append(secretSummaries, SecretSummary{Key: key, Scope: "personal"})
			}
		}

		// Team secrets
		if merged.Team != nil {
			for _, key := range merged.Team.ListSecrets(ctx) {
				if args.Filter != "" && !strings.Contains(key, args.Filter) {
					continue
				}
				if !personalSecrets[key] {
					secretSummaries = append(secretSummaries, SecretSummary{Key: key, Scope: "team"})
				} else {
					secretSummaries = append(secretSummaries, SecretSummary{Key: key, Scope: "team (shadowed)"})
				}
			}
		}
	} else {
		// Personal mode: single store, no scope labels
		creds := cs.List(ctx)
		credSummaries = make([]CredentialSummary, 0, len(creds))
		for name, credType := range creds {
			if args.Filter != "" && !strings.Contains(name, args.Filter) {
				continue
			}
			credSummaries = append(credSummaries, CredentialSummary{
				Name: name,
				Type: string(credType),
			})
		}

		secretKeys := cs.ListSecrets(ctx)
		secretSummaries = make([]SecretSummary, 0, len(secretKeys))
		for _, key := range secretKeys {
			if args.Filter != "" && !strings.Contains(key, args.Filter) {
				continue
			}
			secretSummaries = append(secretSummaries, SecretSummary{Key: key})
		}
	}

	total := len(credSummaries) + len(secretSummaries)
	var msg string
	if total == 0 {
		if args.Filter != "" {
			msg = fmt.Sprintf("Nothing matching %q. Use save_credential for credentials or raw file content, or the setup commands for provider keys.", args.Filter)
		} else {
			msg = "No stored credentials or secrets."
		}
	} else {
		parts := make([]string, 0, 2)
		if len(credSummaries) > 0 {
			parts = append(parts, fmt.Sprintf("%d credential(s)", len(credSummaries)))
		}
		if len(secretSummaries) > 0 {
			parts = append(parts, fmt.Sprintf("%d secret(s)", len(secretSummaries)))
		}
		msg = strings.Join(parts, ", ")
	}

	return ListCredentialsResult{
		Credentials: credSummaries,
		Secrets:     secretSummaries,
		Count:       total,
		Message:     msg,
	}, nil
}

// --- remove_credential tool ---

type RemoveCredentialArgs struct {
	Name string `json:"name" jsonschema:"The credential name to remove"`
}

type RemoveCredentialResult struct {
	ToolResult
}

func removeCredential(ctx tool.Context, args RemoveCredentialArgs) (RemoveCredentialResult, error) {
	cs := getEffectiveCredStore(ctx)
	if cs == nil {
		return RemoveCredentialResult{ToolResult: toolError("Credential store is not available")}, nil
	}

	if args.Name == "" {
		return RemoveCredentialResult{ToolResult: toolError("Name is required")}, nil
	}

	if err := cs.Remove(ctx, args.Name); err != nil {
		return RemoveCredentialResult{
			ToolResult: toolError("Failed to remove: %v", err),
		}, nil
	}

	return RemoveCredentialResult{
		ToolResult: ToolResult{Status: "removed", Message: fmt.Sprintf("Credential %q removed", args.Name)},
	}, nil
}

// --- test_credential tool ---

type TestCredentialArgs struct {
	Name string `json:"name" jsonschema:"The credential name to test"`
}

type TestCredentialResult struct {
	ToolResult
}

func testCredential(ctx tool.Context, args TestCredentialArgs) (TestCredentialResult, error) {
	cs := getEffectiveCredStore(ctx)
	if cs == nil {
		return TestCredentialResult{ToolResult: toolError("Credential store is not available")}, nil
	}

	if args.Name == "" {
		return TestCredentialResult{ToolResult: toolError("Name is required")}, nil
	}

	cred := cs.Get(ctx, args.Name)
	if cred == nil {
		return TestCredentialResult{
			ToolResult: toolError("Credential %q not found", args.Name),
		}, nil
	}

	switch cred.Type {
	case store.CredOAuthClientCreds:
		// Actually test the OAuth flow
		_, _, err := cs.Resolve(ctx, args.Name)
		if err != nil {
			return TestCredentialResult{
				ToolResult: ToolResult{Status: "failed", Message: fmt.Sprintf("OAuth token acquisition failed: %v", err)},
			}, nil
		}
		return TestCredentialResult{
			ToolResult: ToolResult{Status: "ok", Message: fmt.Sprintf("OAuth credential %q: token acquired successfully", args.Name)},
		}, nil

	case store.CredOAuthAuthCode:
		// Test by resolving (which triggers refresh if expired)
		_, _, err := cs.Resolve(ctx, args.Name)
		if err != nil {
			return TestCredentialResult{
				ToolResult: ToolResult{Status: "failed", Message: fmt.Sprintf("OAuth authorization code credential %q: token refresh failed: %v", args.Name, err)},
			}, nil
		}
		return TestCredentialResult{
			ToolResult: ToolResult{Status: "ok", Message: fmt.Sprintf("OAuth credential %q: access token is valid (auto-refreshes when expired)", args.Name)},
		}, nil

	case store.CredOpenStackKeystone:
		_, _, err := cs.Resolve(ctx, args.Name)
		if err != nil {
			return TestCredentialResult{
				ToolResult: ToolResult{Status: "failed", Message: fmt.Sprintf("Keystone token acquisition failed: %v", err)},
			}, nil
		}
		return TestCredentialResult{
			ToolResult: ToolResult{Status: "ok", Message: fmt.Sprintf("Keystone credential %q: token acquired successfully", args.Name)},
		}, nil

	case store.CredPassword:
		return TestCredentialResult{
			ToolResult: ToolResult{Status: "ok", Message: fmt.Sprintf("Credential %q configured (%s, user: %s). Use resolve_credential to retrieve the username and password for SSH/FTP/database connections.", args.Name, cred.Type, cred.Username)},
		}, nil

	case store.CredRawContent:
		return TestCredentialResult{
			ToolResult: ToolResult{Status: "ok", Message: fmt.Sprintf("Credential %q configured (raw_content, %d bytes). Use resolve_credential field content or fleet credential_injection field content.", args.Name, len(cred.Content))},
		}, nil

	default:
		return TestCredentialResult{
			ToolResult: ToolResult{Status: "ok", Message: fmt.Sprintf("Credential %q configured (%s). Use http_request with credential=%q to test connectivity.", args.Name, cred.Type, args.Name)},
		}, nil
	}
}

// --- resolve_credential tool ---

type ResolveCredentialArgs struct {
	Name string `json:"name" jsonschema:"The credential name to resolve"`
}

type ResolveCredentialResult struct {
	Status      string `json:"status"`
	Message     string `json:"message,omitempty"`
	Type        string `json:"type,omitempty"`
	Username    string `json:"username,omitempty"`
	Password    string `json:"password,omitempty"`
	Token       string `json:"token,omitempty"`
	Header      string `json:"header,omitempty"`
	Value       string `json:"value,omitempty"`
	AuthURL     string `json:"auth_url,omitempty"`
	ClientID    string `json:"client_id,omitempty"`
	Content     string `json:"content,omitempty"`
	ContentType string `json:"content_type,omitempty"`
}

func resolveCredential(ctx tool.Context, args ResolveCredentialArgs) (ResolveCredentialResult, error) {
	cs := getEffectiveCredStore(ctx)
	if cs == nil {
		return ResolveCredentialResult{Status: "error", Message: "Credential store is not available"}, nil
	}

	if args.Name == "" {
		return ResolveCredentialResult{Status: "error", Message: "Name is required"}, nil
	}

	// Reload to pick up changes made outside this process
	cs.Reload(ctx) //nolint:errcheck

	cred := cs.Get(ctx, args.Name)
	if cred == nil {
		return ResolveCredentialResult{
			Status:  "error",
			Message: fmt.Sprintf("Credential %q not found", args.Name),
		}, nil
	}

	result := ResolveCredentialResult{
		Status: "ok",
		Type:   string(cred.Type),
	}

	switch cred.Type {
	case store.CredPassword:
		result.Username = cred.Username
		result.Password = credentials.FormatPlaceholder(args.Name, "password")
	case store.CredBasic:
		result.Username = cred.Username
		result.Password = credentials.FormatPlaceholder(args.Name, "password")
	case store.CredBearer:
		result.Token = credentials.FormatPlaceholder(args.Name, "token")
	case store.CredAPIKey:
		result.Header = cred.Header
		result.Value = credentials.FormatPlaceholder(args.Name, "value")
	case store.CredOAuthClientCreds:
		result.AuthURL = cred.AuthURL
		result.ClientID = cred.ClientID
		result.Message = "Use http_request with credential parameter for OAuth — the token is managed automatically."
	case store.CredOAuthAuthCode:
		result.Token = credentials.FormatPlaceholder(args.Name, "token")
		result.ClientID = cred.ClientID
		result.Message = "Access token placeholder returned (auto-refreshed when used). Prefer using http_request with credential parameter — it handles the Bearer header automatically."
	case store.CredOpenStackKeystone:
		result.AuthURL = cred.AuthURL
		result.Token = credentials.FormatPlaceholder(args.Name, "token")
		result.Message = "Use http_request with credential parameter for Keystone — X-Auth-Token is injected automatically."
	case store.CredRawContent:
		result.Content = credentials.FormatPlaceholder(args.Name, "content")
		result.ContentType = cred.ContentType
		result.Message = "Raw content credential. Use the content placeholder directly where file content is needed, or configure fleet credential_injection files with field=content."
	}

	return result, nil
}

// --- Tool constructors ---

func NewSaveCredentialTool() (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:        "save_credential",
		Description: `Save a credential to the encrypted store. Credentials saved here are PERSONAL (only you can see/use them). To share with the team, use the Settings UI 'Publish to Team'. Use IMMEDIATELY when the user provides any secret. Types: api_key, bearer, basic, password, oauth_client_credentials, oauth_authorization_code, openstack_keystone, raw_content. HTTP credentials work with http_request; password and raw_content credentials with resolve_credential. Use raw_content for JSON/YAML/dotenv/text files that will be injected into sandboxes; store payload in content and inject field content. After saving, ALWAYS use memory_save to document safe metadata such as credential name, required prefix/header or target file path, and API base URL/content type — but NEVER include the actual secret value/content in the memory note.`,
	}, saveCredential)
}

func NewListCredentialsTool() (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:        "list_credentials",
		Description: "List all stored credentials and secrets. Shows named credentials (name + type, including HTTP auth and raw_content) and provider/channel/MCP secrets (dot-notation keys like 'provider.anthropic.api_key'). Secret values are never exposed. Optionally filter by name or key.",
	}, listCredentials)
}

func NewRemoveCredentialTool() (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:        "remove_credential",
		Description: "Remove a credential from the encrypted store by name.",
	}, removeCredential)
}

func NewTestCredentialTool() (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:        "test_credential",
		Description: "Test a stored credential. For OAuth and Keystone: performs the token acquisition flow. For others: confirms configuration is valid.",
	}, testCredential)
}

func NewResolveCredentialTool() (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:        "resolve_credential",
		Description: `Retrieve fields of a stored credential by name. Use for non-HTTP auth (SSH, FTP, databases) and raw_content file payloads. Returns type-specific fields: non-secret fields (username, header, content_type) as plaintext, secret fields (password, token, value, content) as {{CREDENTIAL:name:field}} placeholders. Pass placeholders directly to process_write, shell_command, browser_type, etc. — the system substitutes real values at execution time. The real secrets never appear in your context.`,
	}, resolveCredential)
}

// GetCredentialTools returns all credential management tools.
func GetCredentialTools() ([]tool.Tool, error) {
	var tools []tool.Tool

	saveTool, err := NewSaveCredentialTool()
	if err != nil {
		return nil, fmt.Errorf("save_credential: %w", err)
	}
	tools = append(tools, saveTool)

	listTool, err := NewListCredentialsTool()
	if err != nil {
		return nil, fmt.Errorf("list_credentials: %w", err)
	}
	tools = append(tools, listTool)

	removeTool, err := NewRemoveCredentialTool()
	if err != nil {
		return nil, fmt.Errorf("remove_credential: %w", err)
	}
	tools = append(tools, removeTool)

	testTool, err := NewTestCredentialTool()
	if err != nil {
		return nil, fmt.Errorf("test_credential: %w", err)
	}
	tools = append(tools, testTool)

	resolveTool, err := NewResolveCredentialTool()
	if err != nil {
		return nil, fmt.Errorf("resolve_credential: %w", err)
	}
	tools = append(tools, resolveTool)

	return tools, nil
}
