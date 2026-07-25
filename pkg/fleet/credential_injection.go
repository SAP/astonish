package fleet

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
)

// CredentialInjection declares how plan credentials are injected into fleet sandboxes.
type CredentialInjection struct {
	Env   []EnvInjection  `yaml:"env,omitempty" json:"env,omitempty"`
	Files []FileInjection `yaml:"files,omitempty" json:"files,omitempty"`
}

// EnvInjection maps a logical plan credential to a container environment variable.
type EnvInjection struct {
	Credential string `yaml:"credential" json:"credential"` // logical name in plan.Credentials
	Var        string `yaml:"var" json:"var"`
	Field      string `yaml:"field" json:"field"`
}

// FileInjection materializes a credential field as a file inside the sandbox.
type FileInjection struct {
	Credential string `yaml:"credential" json:"credential"`
	Path       string `yaml:"path" json:"path"`
	Format     string `yaml:"format,omitempty" json:"format,omitempty"` // yaml | json | raw | dotenv
	Field      string `yaml:"field" json:"field"`
	Mode       string `yaml:"mode,omitempty" json:"mode,omitempty"` // e.g. "0600"
}

// ParseCredentialInjection decodes a credential_injection value from setup draft
// or tool JSON (map, JSON string, or *CredentialInjection).
func ParseCredentialInjection(raw any) (*CredentialInjection, error) {
	if raw == nil {
		return nil, nil
	}
	switch v := raw.(type) {
	case *CredentialInjection:
		return v, nil
	case CredentialInjection:
		cp := v
		return &cp, nil
	case map[string]any:
		return decodeCredentialInjectionMap(v)
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return nil, nil
		}
		var inj CredentialInjection
		if err := json.Unmarshal([]byte(s), &inj); err != nil {
			return nil, fmt.Errorf("credential_injection: %w", err)
		}
		return &inj, nil
	default:
		return nil, fmt.Errorf("credential_injection: unsupported type %T", raw)
	}
}

func decodeCredentialInjectionMap(m map[string]any) (*CredentialInjection, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	var inj CredentialInjection
	if err := json.Unmarshal(b, &inj); err != nil {
		return nil, fmt.Errorf("credential_injection: %w", err)
	}
	return &inj, nil
}

// ValidateCredentialInjectionSpec checks injection entries reference declared credentials
// and file paths are safe.
func ValidateCredentialInjectionSpec(credentials map[string]string, inj *CredentialInjection) error {
	if inj == nil {
		return nil
	}
	if err := ValidateFileInjectionPaths(*inj); err != nil {
		return err
	}
	for _, spec := range inj.Env {
		if spec.Credential == "" || spec.Var == "" || spec.Field == "" {
			return fmt.Errorf("credential_injection.env: credential, var, and field are required")
		}
		if credentials != nil {
			if _, ok := credentials[spec.Credential]; !ok {
				return fmt.Errorf("credential_injection.env: logical credential %q not in plan.credentials", spec.Credential)
			}
		}
	}
	for _, spec := range inj.Files {
		if spec.Credential == "" || spec.Field == "" {
			return fmt.Errorf("credential_injection.files: credential and field are required")
		}
		if credentials != nil {
			if _, ok := credentials[spec.Credential]; !ok {
				return fmt.Errorf("credential_injection.files: logical credential %q not in plan.credentials", spec.Credential)
			}
		}
	}
	return nil
}

// NormalizeCredentialInjection fills in default env injection for known credentials
// (github → GH_TOKEN) when not explicitly declared. Returns a non-nil pointer when
// the plan has injectable credentials.
func NormalizeCredentialInjection(credentials map[string]string, inj *CredentialInjection) *CredentialInjection {
	if len(credentials) == 0 {
		return inj
	}
	out := CredentialInjection{}
	if inj != nil {
		out = *inj
	}
	hasGitHubEnv := false
	for _, spec := range out.Env {
		if spec.Credential == "github" && spec.Var == "GH_TOKEN" {
			hasGitHubEnv = true
			break
		}
	}
	if _, ok := credentials["github"]; ok && !hasGitHubEnv {
		out.Env = append(out.Env, EnvInjection{
			Credential: "github",
			Var:        "GH_TOKEN",
			Field:      "token",
		})
	}
	if len(out.Env) == 0 && len(out.Files) == 0 {
		return inj
	}
	return &out
}

// EffectiveCredentialInjection returns the plan's injection spec, with defaults
// for common credentials (github → GH_TOKEN) when not explicitly declared.
func (p *FleetPlan) EffectiveCredentialInjection() CredentialInjection {
	if p == nil {
		return CredentialInjection{}
	}
	if p.CredentialInjection != nil {
		return *p.CredentialInjection
	}
	var inj CredentialInjection
	if _, ok := p.Credentials["github"]; ok {
		inj.Env = append(inj.Env, EnvInjection{
			Credential: "github",
			Var:        "GH_TOKEN",
			Field:      "token",
		})
	}
	return inj
}

// BuildInjectionEnv builds the sandbox environment map from the plan injection
// manifest and resolved credentials.
func BuildInjectionEnv(plan *FleetPlan, resolved map[string]*ResolvedCredential, cs store.CredentialStore, ctx context.Context) (map[string]string, error) {
	if plan == nil {
		return nil, nil
	}
	inj := plan.EffectiveCredentialInjection()
	return BuildInjectionEnvFromSpec(ctx, plan.Credentials, &inj, resolved, cs, plan.Key)
}

// BuildInjectionEnvFromSpec builds env from an explicit credentials map and injection spec.
// ownerKey is used for audit logs only (plan key or suite name).
func BuildInjectionEnvFromSpec(ctx context.Context, credentials map[string]string, inj *CredentialInjection, resolved map[string]*ResolvedCredential, cs store.CredentialStore, ownerKey string) (map[string]string, error) {
	if inj == nil || len(inj.Env) == 0 {
		return nil, nil
	}

	env := make(map[string]string)
	for _, spec := range inj.Env {
		if spec.Var == "" || spec.Credential == "" || spec.Field == "" {
			return nil, fmt.Errorf("credential_injection.env: credential, var, and field are required")
		}
		storeName, ok := credentials[spec.Credential]
		if !ok {
			return nil, fmt.Errorf("credential_injection.env: logical credential %q not in credentials map", spec.Credential)
		}
		value, err := extractCredentialField(ctx, cs, storeName, spec.Field, resolved[spec.Credential])
		if err != nil {
			return nil, fmt.Errorf("credential_injection.env[%s]: %w", spec.Var, err)
		}
		if value == "" {
			return nil, fmt.Errorf("credential_injection.env[%s]: field %q resolved to empty value", spec.Var, spec.Field)
		}
		env[spec.Var] = value
		LogInjectionAudit(ownerKey, "", spec.Credential, "env", spec.Var, "")
	}
	return env, nil
}

// ValidateFileInjectionPaths ensures all file paths are absolute and safe.
func ValidateFileInjectionPaths(inj CredentialInjection) error {
	for _, f := range inj.Files {
		if f.Path == "" {
			return fmt.Errorf("credential_injection.files: path is required")
		}
		if !filepath.IsAbs(f.Path) {
			return fmt.Errorf("credential_injection.files: path %q must be absolute", f.Path)
		}
		if strings.Contains(f.Path, "..") {
			return fmt.Errorf("credential_injection.files: path %q must not contain ..", f.Path)
		}
	}
	return nil
}

// MaterializeInjectionFiles writes declared credential files into a sandbox via Backend.PushFile.
func MaterializeInjectionFiles(ctx context.Context, backend sandbox.Backend, sessionID string, plan *FleetPlan, resolved map[string]*ResolvedCredential, cs store.CredentialStore) error {
	if plan == nil {
		return nil
	}
	inj := plan.EffectiveCredentialInjection()
	return MaterializeInjectionFilesFromSpec(ctx, backend, sessionID, plan.Credentials, &inj, resolved, cs, plan.Key)
}

// MaterializeInjectionFilesFromSpec writes injection files using an explicit credentials map.
func MaterializeInjectionFilesFromSpec(ctx context.Context, backend sandbox.Backend, sessionID string, credentials map[string]string, inj *CredentialInjection, resolved map[string]*ResolvedCredential, cs store.CredentialStore, ownerKey string) error {
	if backend == nil || sessionID == "" || inj == nil || len(inj.Files) == 0 {
		return nil
	}
	if err := ValidateFileInjectionPaths(*inj); err != nil {
		return err
	}

	for _, spec := range inj.Files {
		if spec.Credential == "" || spec.Field == "" {
			return fmt.Errorf("credential_injection.files: credential and field are required")
		}
		storeName, ok := credentials[spec.Credential]
		if !ok {
			return fmt.Errorf("credential_injection.files: logical credential %q not in credentials map", spec.Credential)
		}
		content, err := extractCredentialField(ctx, cs, storeName, spec.Field, resolved[spec.Credential])
		if err != nil {
			return fmt.Errorf("credential_injection.files[%s]: %w", spec.Path, err)
		}
		if content == "" {
			return fmt.Errorf("credential_injection.files[%s]: field %q resolved to empty value", spec.Path, spec.Field)
		}
		mode := os.FileMode(0o600)
		if spec.Mode != "" {
			var parsed os.FileMode
			if _, scanErr := fmt.Sscanf(spec.Mode, "%o", &parsed); scanErr == nil {
				mode = parsed
			}
		}
		if err := backend.PushFile(ctx, sessionID, spec.Path, strings.NewReader(content), mode); err != nil {
			return fmt.Errorf("credential_injection.files[%s]: %w", spec.Path, err)
		}
		LogInjectionAudit(ownerKey, sessionID, spec.Credential, "file", spec.Path, spec.Format)
	}
	return nil
}

// MaterializeInjectionFilesIncus writes files via shell for Incus fleet sessions
// (IncusBackend PushFile may not exist on all paths; exec is reliable).
func MaterializeInjectionFilesIncus(ctx context.Context, execFn func(command []string, env map[string]string) (stdout, stderr []byte, exitCode int, err error), plan *FleetPlan, resolved map[string]*ResolvedCredential, cs store.CredentialStore) error {
	if plan == nil {
		return nil
	}
	inj := plan.EffectiveCredentialInjection()
	return MaterializeInjectionFilesIncusFromSpec(ctx, execFn, plan.Credentials, &inj, resolved, cs, plan.Key)
}

// MaterializeInjectionFilesIncusFromSpec writes injection files via an exec function.
func MaterializeInjectionFilesIncusFromSpec(ctx context.Context, execFn func(command []string, env map[string]string) (stdout, stderr []byte, exitCode int, err error), credentials map[string]string, inj *CredentialInjection, resolved map[string]*ResolvedCredential, cs store.CredentialStore, ownerKey string) error {
	if execFn == nil || inj == nil || len(inj.Files) == 0 {
		return nil
	}
	if err := ValidateFileInjectionPaths(*inj); err != nil {
		return err
	}

	for _, spec := range inj.Files {
		storeName, ok := credentials[spec.Credential]
		if !ok {
			return fmt.Errorf("credential_injection.files: logical credential %q not in credentials map", spec.Credential)
		}
		content, err := extractCredentialField(ctx, cs, storeName, spec.Field, resolved[spec.Credential])
		if err != nil {
			return fmt.Errorf("credential_injection.files[%s]: %w", spec.Path, err)
		}
		if content == "" {
			return fmt.Errorf("credential_injection.files[%s]: empty content", spec.Path)
		}
		mode := "0600"
		if spec.Mode != "" {
			mode = spec.Mode
		}
		dir := filepath.Dir(spec.Path)
		// Write via base64 to avoid shell escaping issues with multiline YAML/JSON.
		cmd := fmt.Sprintf("mkdir -p %q && printf %%s %q | base64 -d > %q && chmod %s %q",
			dir,
			base64.StdEncoding.EncodeToString([]byte(content)),
			spec.Path,
			mode,
			spec.Path,
		)
		_, stderr, exitCode, err := execFn([]string{"sh", "-c", cmd}, nil)
		if err != nil {
			return fmt.Errorf("credential_injection.files[%s]: exec: %w", spec.Path, err)
		}
		if exitCode != 0 {
			return fmt.Errorf("credential_injection.files[%s]: exit %d: %s", spec.Path, exitCode, string(stderr))
		}
		LogInjectionAudit(ownerKey, "", spec.Credential, "file", spec.Path, spec.Format)
	}
	return nil
}

func extractCredentialField(ctx context.Context, cs store.CredentialStore, storeName, field string, resolved *ResolvedCredential) (string, error) {
	if cs != nil {
		adapter := credentials.NewStoreAdapter(cs)
		if adapter != nil {
			val := credentials.ResolveField(adapter, storeName, field)
			if val != "" {
				return val, nil
			}
		}
	}
	if resolved != nil {
		switch field {
		case "token", "value":
			if resolved.Token != "" {
				return resolved.Token, nil
			}
		case "password":
			if resolved.Password != "" {
				return resolved.Password, nil
			}
		case "username":
			if resolved.Username != "" {
				return resolved.Username, nil
			}
		case "content":
			if resolved.Content != "" {
				return resolved.Content, nil
			}
		}
	}
	return "", fmt.Errorf("credential %q field %q not found", storeName, field)
}

// RegisterInjectionWithRedactor registers injected env values with the credential
// redactor so fleet session logs and traces never leak secrets.
func RegisterInjectionWithRedactor(r *credentials.Redactor, env map[string]string) {
	if r == nil || len(env) == 0 {
		return
	}
	for name, value := range env {
		if value != "" {
			r.AddSecret("fleet-injection/env/"+name, value)
		}
	}
}

// MergeEnv merges injection env into base env (injection wins on conflict).
func MergeEnv(base, injection map[string]string) map[string]string {
	if len(base) == 0 && len(injection) == 0 {
		return nil
	}
	out := make(map[string]string, len(base)+len(injection))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range injection {
		out[k] = v
	}
	return out
}

// injection_audit.go helpers are in injection_audit.go
