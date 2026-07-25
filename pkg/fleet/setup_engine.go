package fleet

import (
	"fmt"
	"strings"
)

// SetupPlanBuild is the assembled plan payload from a setup profile and collected values.
type SetupPlanBuild struct {
	Key                   string
	Name                  string
	Description           string
	BaseFleetKey          string
	SetupProfileKey       string
	ChannelType           string
	ChannelConfig         map[string]any
	ChannelSchedule       string
	Artifacts             map[string]PlanArtifactConfig
	Credentials           map[string]string
	CredentialInjection   *CredentialInjection
	ProjectSource         *ProjectSourceConfig
	Template              string
	ContainerWorkspaceDir string
	IncludeAgents         []string
	BehaviorOverrides     map[string]string
	ValidationPassed      bool
}

// SetupEngine resolves profiles and operates on collected setup data.
type SetupEngine struct {
	resolve func(key string) (*SetupProfile, bool)
}

// NewSetupEngine creates an engine that resolves bundled profiles then optional store.
func NewSetupEngine(resolve func(key string) (*SetupProfile, bool)) *SetupEngine {
	if resolve == nil {
		resolve = func(key string) (*SetupProfile, bool) {
			return GetBundledSetupProfile(key)
		}
	}
	return &SetupEngine{resolve: resolve}
}

// ResolveProfile loads a setup profile by key, falling back to generic.
func (e *SetupEngine) ResolveProfile(key string) (*SetupProfile, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		key = DefaultSetupProfileKey
	}
	if p, ok := e.resolve(key); ok {
		return p, nil
	}
	if p, ok := e.resolve(DefaultSetupProfileKey); ok {
		return p, nil
	}
	return nil, fmt.Errorf("setup profile %q not found", key)
}

// StepActive reports whether a step applies given collected values and when expression.
func (e *SetupEngine) StepActive(step *SetupStep, collected SetupCollected) bool {
	if step == nil {
		return false
	}
	if strings.TrimSpace(step.When) == "" {
		return true
	}
	return evalWhen(step.When, collected)
}

// ValidateStep validates one step's fields.
func (e *SetupEngine) ValidateStep(profile *SetupProfile, stepID string, collected SetupCollected) error {
	step, ok := profile.StepByID(stepID)
	if !ok {
		return fmt.Errorf("unknown setup step %q", stepID)
	}
	if !e.StepActive(step, collected) {
		return nil
	}
	if step.EffectiveType() == "info" {
		if stepValues := collected[stepID]; stepValues != nil {
			if ack, ok := stepValues[setupStepAckField].(bool); ok && ack {
				return nil
			}
		}
		return fmt.Errorf("step %q: acknowledge before continuing with values {\"_ack\": true}", stepID)
	}
	if step.EffectiveType() == "review" {
		return nil
	}

	stepValues := collected[stepID]
	for _, field := range step.Fields {
		if !fieldActive(field, collected) {
			continue
		}
		if !field.Required {
			continue
		}
		val, ok := stepValues[field.ID]
		if !ok || isEmptyValue(val) {
			return fmt.Errorf("step %q: field %q is required", stepID, field.ID)
		}
	}

	if step.Required && step.EffectiveType() == "provision" {
		stepValues := collected[stepID]
		for _, out := range step.Outputs {
			key := outputFieldKey(out.To)
			if key == "" {
				continue
			}
			if val, ok := stepValues[key]; !ok || isEmptyValue(val) {
				return fmt.Errorf("step %q: provisioning output %q is required", stepID, key)
			}
		}
	}

	return nil
}

// StepComplete reports whether all required fields for an active step are present.
func (e *SetupEngine) StepComplete(profile *SetupProfile, stepID string, collected SetupCollected) bool {
	return e.ValidateStep(profile, stepID, collected) == nil
}

// NextIncompleteStep returns the first incomplete active step ID, or empty if all complete.
func (e *SetupEngine) NextIncompleteStep(profile *SetupProfile, collected SetupCollected) string {
	for _, step := range profile.Steps {
		if !e.StepActive(&step, collected) {
			continue
		}
		if !e.StepComplete(profile, step.ID, collected) {
			return step.ID
		}
	}
	return ""
}

// BuildPlanArgs assembles a plan build from collected setup values.
func (e *SetupEngine) BuildPlanArgs(profile *SetupProfile, templateKey string, collected SetupCollected) (*SetupPlanBuild, error) {
	if strings.TrimSpace(templateKey) == "" {
		return nil, fmt.Errorf("template key is required")
	}

	build := &SetupPlanBuild{
		BaseFleetKey:      templateKey,
		SetupProfileKey:   profile.Key,
		ChannelConfig:     map[string]any{},
		Artifacts:         map[string]PlanArtifactConfig{},
		Credentials:       map[string]string{},
		BehaviorOverrides: map[string]string{},
	}

	// Apply step defaults (artifacts, etc.)
	for _, step := range profile.Steps {
		if !e.StepActive(&step, collected) {
			continue
		}
		for cat, raw := range step.Defaults {
			if _, exists := build.Artifacts[cat]; exists {
				continue
			}
			if m, ok := raw.(map[string]any); ok {
				build.Artifacts[cat] = mapToArtifactConfig(m)
			}
		}
	}

	for _, step := range profile.Steps {
		if !e.StepActive(&step, collected) {
			continue
		}
		stepValues := collected[step.ID]
		for _, field := range step.Fields {
			if !fieldActive(field, collected) {
				continue
			}
			val, ok := stepValues[field.ID]
			if !ok {
				if field.Default != nil {
					val = field.Default
				} else {
					continue
				}
			}
			if err := applyMapsTo(build, field.MapsTo, val); err != nil {
				return nil, err
			}
		}
		// Provision outputs live under step values keyed by output field name.
		if step.EffectiveType() == "provision" {
			for _, out := range step.Outputs {
				key := outputFieldKey(out.To)
				if key == "" {
					continue
				}
				if val, ok := stepValues[key]; ok && !isEmptyValue(val) {
					_ = applyMapsTo(build, out.To, val)
				}
			}
		}
	}

	if build.ChannelType == "" {
		build.ChannelType = "chat"
	}

	// Derive artifact repos from project source when missing.
	if build.ProjectSource != nil && build.ProjectSource.Type == "git_repo" && build.ProjectSource.Repo != "" {
		if code, ok := build.Artifacts["code"]; ok && code.Repo == "" {
			code.Repo = build.ProjectSource.Repo
			build.Artifacts["code"] = code
		}
		if docs, ok := build.Artifacts["docs"]; ok && docs.Repo == "" {
			docs.Repo = build.ProjectSource.Repo
			build.Artifacts["docs"] = docs
		}
	}

	if build.Key == "" {
		return nil, fmt.Errorf("plan key is required")
	}
	if build.Name == "" {
		build.Name = build.Key
	}

	return build, nil
}

func fieldActive(field SetupField, collected SetupCollected) bool {
	if strings.TrimSpace(field.When) == "" {
		return true
	}
	return evalWhen(field.When, collected)
}

func evalWhen(expr string, collected SetupCollected) bool {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return true
	}

	// channel.type == 'github_issues'
	if parts := strings.Split(expr, "=="); len(parts) == 2 {
		left := strings.TrimSpace(parts[0])
		right := strings.Trim(strings.TrimSpace(parts[1]), "'\"")
		return getCollectedPath(collected, left) == right
	}
	if parts := strings.Split(expr, "!="); len(parts) == 2 {
		left := strings.TrimSpace(parts[0])
		right := strings.Trim(strings.TrimSpace(parts[1]), "'\"")
		return getCollectedPath(collected, left) != right
	}
	return true
}

func getCollectedPath(collected SetupCollected, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	parts := strings.Split(path, ".")
	if len(parts) < 2 {
		return fmt.Sprintf("%v", collected[path])
	}
	stepID := parts[0]
	fieldID := strings.Join(parts[1:], ".")
	if stepValues, ok := collected[stepID]; ok {
		if val, ok := stepValues[fieldID]; ok {
			return fmt.Sprintf("%v", val)
		}
	}
	return ""
}

func isEmptyValue(v any) bool {
	if v == nil {
		return true
	}
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t) == ""
	case []any:
		return len(t) == 0
	case []string:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	default:
		return false
	}
}

func outputFieldKey(mapsTo string) string {
	mapsTo = strings.TrimSpace(mapsTo)
	if mapsTo == "" {
		return ""
	}
	parts := strings.Split(mapsTo, ".")
	return parts[len(parts)-1]
}

func applyMapsTo(build *SetupPlanBuild, mapsTo string, val any) error {
	mapsTo = strings.TrimSpace(mapsTo)
	if mapsTo == "" {
		return nil
	}
	parts := strings.Split(mapsTo, ".")
	if len(parts) < 2 || parts[0] != "plan" {
		return fmt.Errorf("unsupported maps_to path %q", mapsTo)
	}

	switch parts[1] {
	case "key":
		build.Key = fmt.Sprintf("%v", val)
	case "name":
		build.Name = fmt.Sprintf("%v", val)
	case "description":
		build.Description = fmt.Sprintf("%v", val)
	case "template":
		build.Template = fmt.Sprintf("%v", val)
	case "container_workspace_dir":
		build.ContainerWorkspaceDir = fmt.Sprintf("%v", val)
	case "channel":
		return applyChannelMaps(build, parts[2:], val)
	case "credentials":
		if build.Credentials == nil {
			build.Credentials = map[string]string{}
		}
		if len(parts) >= 3 {
			build.Credentials[parts[2]] = fmt.Sprintf("%v", val)
		}
	case "credential_injection":
		inj, err := ParseCredentialInjection(val)
		if err != nil {
			return err
		}
		build.CredentialInjection = inj
	case "project_source":
		if build.ProjectSource == nil {
			build.ProjectSource = &ProjectSourceConfig{}
		}
		if len(parts) >= 3 {
			switch parts[2] {
			case "type":
				build.ProjectSource.Type = fmt.Sprintf("%v", val)
			case "repo":
				build.ProjectSource.Repo = fmt.Sprintf("%v", val)
			case "path":
				build.ProjectSource.Path = fmt.Sprintf("%v", val)
			}
		}
	case "artifacts":
		if len(parts) >= 3 {
			cat := parts[2]
			cfg := build.Artifacts[cat]
			if len(parts) >= 4 {
				setArtifactField(&cfg, parts[3], val)
			}
			build.Artifacts[cat] = cfg
		}
	case "include_agents":
		switch t := val.(type) {
		case []any:
			for _, item := range t {
				build.IncludeAgents = append(build.IncludeAgents, fmt.Sprintf("%v", item))
			}
		case []string:
			build.IncludeAgents = append(build.IncludeAgents, t...)
		case string:
			if strings.TrimSpace(t) != "" {
				build.IncludeAgents = append(build.IncludeAgents, strings.Split(t, ",")...)
			}
		}
	case "behavior_overrides":
		// JSON object or skip in v1 form UI
	}
	return nil
}

func applyChannelMaps(build *SetupPlanBuild, parts []string, val any) error {
	if len(parts) == 0 {
		return nil
	}
	switch parts[0] {
	case "type":
		build.ChannelType = fmt.Sprintf("%v", val)
	case "schedule":
		build.ChannelSchedule = fmt.Sprintf("%v", val)
	case "config":
		if build.ChannelConfig == nil {
			build.ChannelConfig = map[string]any{}
		}
		if len(parts) >= 2 {
			build.ChannelConfig[parts[1]] = val
		}
	}
	return nil
}

func setArtifactField(cfg *PlanArtifactConfig, field string, val any) {
	switch field {
	case "type":
		cfg.Type = fmt.Sprintf("%v", val)
	case "repo":
		cfg.Repo = fmt.Sprintf("%v", val)
	case "path":
		cfg.Path = fmt.Sprintf("%v", val)
	case "branch_pattern":
		cfg.BranchPattern = fmt.Sprintf("%v", val)
	case "sub_path":
		cfg.SubPath = fmt.Sprintf("%v", val)
	case "auto_pr":
		if b, ok := val.(bool); ok {
			cfg.AutoPR = b
		}
	}
}

func mapToArtifactConfig(m map[string]any) PlanArtifactConfig {
	cfg := PlanArtifactConfig{}
	if v, ok := m["type"].(string); ok {
		cfg.Type = v
	}
	if v, ok := m["repo"].(string); ok {
		cfg.Repo = v
	}
	if v, ok := m["path"].(string); ok {
		cfg.Path = v
	}
	if v, ok := m["branch_pattern"].(string); ok {
		cfg.BranchPattern = v
	}
	if v, ok := m["sub_path"].(string); ok {
		cfg.SubPath = v
	}
	if v, ok := m["auto_pr"].(bool); ok {
		cfg.AutoPR = v
	}
	return cfg
}

// SetupProfileSummaries returns summaries for bundled profiles.
func SetupProfileSummaries() ([]SetupProfileSummary, error) {
	bundled, err := LoadBundledSetupProfiles()
	if err != nil {
		return nil, err
	}
	out := make([]SetupProfileSummary, 0, len(bundled))
	for key, p := range bundled {
		out = append(out, SetupProfileSummary{
			Key:         key,
			Name:        p.Name,
			Description: p.Description,
			Domain:      p.Domain,
			StepCount:   len(p.Steps),
			Source:      "bundled",
		})
	}
	return out, nil
}
