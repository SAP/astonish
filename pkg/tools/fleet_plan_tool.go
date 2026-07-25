package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/fleet"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"gopkg.in/yaml.v3"
)

// fleetPlanRegistryVar holds the plan registry for the save_fleet_plan tool.
// Set by the launcher via SetFleetPlanRegistry.
var fleetPlanRegistryVar *fleet.PlanRegistry

// SetFleetPlanRegistry registers the plan registry for the fleet plan tool.
func SetFleetPlanRegistry(reg *fleet.PlanRegistry) {
	fleetPlanRegistryVar = reg
}

// GetFleetPlanRegistry returns the plan registry (for use by other packages).
func GetFleetPlanRegistry() *fleet.PlanRegistry {
	return fleetPlanRegistryVar
}

// getEffectiveFleetPlanStore returns the FleetPlanStore from context (platform mode)
// or wraps the file-based registry (personal mode).
func getEffectiveFleetPlanStore(ctx context.Context) store.FleetPlanStore {
	if ctx != nil {
		if fs := store.FleetPlanStoreFromContext(ctx); fs != nil {
			return fs
		}
	}
	// Personal mode fallback: wrap the file-based registry
	if fleetPlanRegistryVar != nil {
		return &fleetPlanRegistryStoreAdapter{reg: fleetPlanRegistryVar}
	}
	return nil
}

// fleetPlanRegistryStoreAdapter adapts *fleet.PlanRegistry to store.FleetPlanStore
// for use in personal mode fallback within tools.
type fleetPlanRegistryStoreAdapter struct {
	reg *fleet.PlanRegistry
}

func (a *fleetPlanRegistryStoreAdapter) GetPlan(_ context.Context, key string) (any, bool) {
	return a.reg.GetPlan(key)
}

func (a *fleetPlanRegistryStoreAdapter) ListPlans(_ context.Context) []store.FleetPlanSummary {
	plans := a.reg.ListPlans()
	result := make([]store.FleetPlanSummary, len(plans))
	for i, p := range plans {
		result[i] = store.FleetPlanSummary{
			Key:         p.Key,
			Name:        p.Name,
			Description: p.Description,
			CreatedFrom: p.CreatedFrom,
			ChannelType: p.ChannelType,
			AgentCount:  p.AgentCount,
			AgentNames:  p.AgentNames,
		}
	}
	return result
}

func (a *fleetPlanRegistryStoreAdapter) Save(_ context.Context, plan any) error {
	p, ok := plan.(*fleet.FleetPlan)
	if !ok {
		return fmt.Errorf("expected *fleet.FleetPlan, got %T", plan)
	}
	return a.reg.Save(p)
}

func (a *fleetPlanRegistryStoreAdapter) Delete(_ context.Context, key string) error {
	return a.reg.Delete(key)
}

func (a *fleetPlanRegistryStoreAdapter) Count(_ context.Context) int {
	return a.reg.Count()
}

func (a *fleetPlanRegistryStoreAdapter) Reload(_ context.Context) error {
	return a.reg.Reload()
}

func (a *fleetPlanRegistryStoreAdapter) GetPlanYAML(_ context.Context, key string) (string, error) {
	dir := a.reg.Dir()
	if dir == "" {
		return "", fmt.Errorf("fleet plan directory not configured")
	}
	yamlPath := dir + "/" + key + ".yaml"
	data, err := os.ReadFile(yamlPath)
	if err != nil {
		return "", fmt.Errorf("fleet plan %q not found: %w", key, err)
	}
	return string(data), nil
}

func (a *fleetPlanRegistryStoreAdapter) SavePlanYAML(_ context.Context, key string, yamlContent string) error {
	var plan fleet.FleetPlan
	if err := yaml.Unmarshal([]byte(yamlContent), &plan); err != nil {
		return fmt.Errorf("invalid YAML: %w", err)
	}
	plan.Key = key
	return a.reg.Save(&plan)
}

// PlanActivatorFunc is a function that activates a fleet plan by creating
// the scheduler job for its channel polling. Used to auto-activate non-chat
// plans immediately after saving.
type PlanActivatorFunc func(ctx context.Context, planKey string) error

var planActivatorFuncVar PlanActivatorFunc

// SetPlanActivatorFunc registers the activation function for auto-activation
// after save. Called by the daemon during initialization.
func SetPlanActivatorFunc(fn PlanActivatorFunc) {
	planActivatorFuncVar = fn
}

// SaveFleetPlanArgs are the arguments for the save_fleet_plan tool.
type SaveFleetPlanArgs struct {
	// Key is a unique identifier for the plan (lowercase, hyphens, e.g., "frontend-bugs")
	Key string `json:"key" jsonschema:"Unique identifier for the plan (lowercase, hyphens, e.g., 'frontend-bugs')"`
	// Name is the human-readable display name
	Name string `json:"name" jsonschema:"Human-readable display name for the plan"`
	// Description explains what this plan does
	Description string `json:"description" jsonschema:"Short description of what this plan does"`
	// BaseFleetKey is the fleet this plan is based on (e.g., "software-dev")
	BaseFleetKey string `json:"base_fleet_key" jsonschema:"Fleet template key this plan is based on (e.g., 'software-dev'). Use list_fleets to see available templates."`
	// ChannelType is the input channel: "chat", "github_issues"
	ChannelType string `json:"channel_type" jsonschema:"Communication channel type: 'chat' (manual start via UI/CLI) or 'github_issues' (auto-triggered by new GitHub issues). These are the currently supported channels; other integrations may be added in the future."`
	// ChannelConfig holds channel-specific settings as a JSON object
	ChannelConfig map[string]any `json:"channel_config,omitempty" jsonschema:"Channel-specific settings. For github_issues: {repo, label}. Not needed for chat channels."`
	// ChannelSchedule is a cron expression for polling (non-chat channels)
	ChannelSchedule string `json:"channel_schedule,omitempty" jsonschema:"Cron expression for polling non-chat channels (e.g., '*/5 * * * *' for every 5 minutes, '* * * * *' for every minute). IMPORTANT: always set this field for non-chat channels; do NOT put the schedule inside channel_config."`
	// Artifacts maps artifact categories to their destinations (JSON object)
	Artifacts map[string]SaveFleetPlanArtifact `json:"artifacts,omitempty" jsonschema:"Artifact destinations mapping category names to their config (e.g., code -> git_repo, docs -> local path)"`
	// BehaviorOverrides maps agent keys to behavior text additions.
	BehaviorOverrides map[string]string `json:"behavior_overrides,omitempty" jsonschema:"Additional behavior instructions per agent. Keyed by agent role (e.g., 'dev', 'qa'). These are APPENDED to the base fleet behaviors, not replacing them."`
	// IncludeAgents restricts which agents from the base fleet are included in the plan.
	// If empty or nil, ALL agents from the base fleet are included (default behavior).
	// When set, only the listed agent roles are kept; all others are removed from both
	// the agents map and the communication flow graph.
	IncludeAgents []string `json:"include_agents,omitempty" jsonschema:"Optional list of agent roles to include from the base fleet (e.g., ['dev'] or ['po', 'dev']). If omitted, all agents are included. When set, agents NOT in this list are removed along with their communication flow entries."`
	// Credentials maps logical names to credential store entry names.
	Credentials map[string]string `json:"credentials,omitempty" jsonschema:"Credential mappings for external service authentication. Key is a logical name agents use (e.g., 'github', 'trading'). Value is the credential name in the encrypted store. IMPORTANT: For github_issues channel plans, include a 'github' entry so the GitHub token is auto-injected into gh CLI commands. If credentials were validated with validate_fleet_plan, include the same mappings here."`
	// CredentialInjection declares how credentials are injected into fleet sandbox containers at session start (env vars and files).
	CredentialInjection *SaveFleetPlanCredentialInjection `json:"credential_injection,omitempty" jsonschema:"How plan credentials are injected at fleet session start. env: map logical credentials to container environment variables. files: materialize credential fields to absolute paths inside the container (e.g., /root/app/config/credentials.yaml). Required for apps that read config files — store file payloads via save_credential (type raw_content, field content) and reference them here. Existing api_key/value mappings remain supported for old plans. GitHub-only plans get GH_TOKEN by default when omitted."`
	// WorkspaceBaseDir overrides the base directory where project files are stored.
	// The final workspace path will be <workspace_base_dir>/<repo-name or plan-key>.
	// If omitted, the template's default is used (typically ~/astonish_projects).
	// Deprecated: Use ProjectSource instead. Per-session workspaces are now created
	// automatically from the project source. This field is kept for backward compat.
	WorkspaceBaseDir string `json:"workspace_base_dir,omitempty" jsonschema:"Deprecated. Optional override for the base directory where project files are stored. Prefer project_source instead."`
	// ProjectSource describes where the project code lives so each session can
	// create its own isolated workspace by cloning or copying.
	ProjectSource *SaveFleetPlanProjectSource `json:"project_source,omitempty" jsonschema:"Where the project code lives. Each session clones (git_repo) or copies (local) from this source into an isolated workspace. If omitted, the system infers from artifact config."`
	// ValidationPassed should be true if validate_fleet_plan was called and passed.
	ValidationPassed bool `json:"validation_passed,omitempty" jsonschema:"Set to true after validate_fleet_plan passes. Required for non-chat channel plans."`
	// Template is the name of the sandbox container template for this plan.
	// When sandbox mode is enabled, fleet sessions clone from this template
	// instead of @base. The template should have the project repo pre-cloned
	// and dependencies installed. Created by the wizard via save_sandbox_template.
	Template string `json:"template,omitempty" jsonschema:"Optional sandbox container template name. When set and sandbox is enabled, fleet sessions use this pre-provisioned template instead of @base. Created by save_sandbox_template during wizard setup."`
	// ContainerWorkspaceDir is the absolute path inside the sandbox container
	// where the project lives (e.g., "/root/juicytrade"). Set during wizard
	// template creation to tell fleet sessions where the project root is.
	ContainerWorkspaceDir string `json:"container_workspace_dir,omitempty" jsonschema:"Absolute path inside the sandbox container where the project was cloned (e.g., '/root/juicytrade'). Set this when a sandbox template is used so fleet agents know the project root."`
}

// SaveFleetPlanArtifact describes a single artifact destination.
type SaveFleetPlanArtifact struct {
	Type          string `json:"type" jsonschema:"Artifact storage type: 'local' (filesystem path) or 'git_repo' (GitHub repository)"`
	Path          string `json:"path,omitempty" jsonschema:"Filesystem path for 'local' type artifacts"`
	Repo          string `json:"repo,omitempty" jsonschema:"GitHub repository as 'owner/repo' for 'git_repo' type"`
	BranchPattern string `json:"branch_pattern,omitempty" jsonschema:"Git branch naming pattern for 'git_repo' type (e.g., 'fleet/{task}')"`
	SubPath       string `json:"sub_path,omitempty" jsonschema:"Subdirectory within the repo for 'git_repo' type (e.g., '/src')"`
	AutoPR        bool   `json:"auto_pr,omitempty" jsonschema:"Automatically create a pull request when work is complete (for 'git_repo' type)"`
}

// SaveFleetPlanProjectSource describes where the project code lives.
type SaveFleetPlanProjectSource struct {
	Type string `json:"type" jsonschema:"Source type: 'git_repo' (clone from GitHub) or 'local' (copy from local path)"`
	Repo string `json:"repo,omitempty" jsonschema:"GitHub repository as 'owner/repo' for 'git_repo' type"`
	Path string `json:"path,omitempty" jsonschema:"Filesystem path for 'local' type"`
}

// SaveFleetPlanCredentialInjection declares env and file injection for fleet sessions.
type SaveFleetPlanCredentialInjection struct {
	Env   []SaveFleetPlanEnvInjection  `json:"env,omitempty" jsonschema:"Environment variable injection entries"`
	Files []SaveFleetPlanFileInjection `json:"files,omitempty" jsonschema:"File materialization entries"`
}

// SaveFleetPlanEnvInjection maps a logical credential to a container env var.
type SaveFleetPlanEnvInjection struct {
	Credential string `json:"credential" jsonschema:"Logical name from the credentials map (e.g., 'github', 'trading')"`
	Var        string `json:"var" jsonschema:"Container environment variable name (e.g., 'GH_TOKEN')"`
	Field      string `json:"field" jsonschema:"Credential field to read: token, value, password, username"`
}

// SaveFleetPlanFileInjection materializes a credential field as a file in the container.
type SaveFleetPlanFileInjection struct {
	Credential string `json:"credential" jsonschema:"Logical name from the credentials map"`
	Path       string `json:"path" jsonschema:"Absolute path inside the container (e.g., '/root/app/config/credentials.yaml')"`
	Field      string `json:"field" jsonschema:"Credential field to write (usually 'value' for api_key blobs)"`
	Format     string `json:"format,omitempty" jsonschema:"Optional format hint: yaml, json, raw, dotenv"`
	Mode       string `json:"mode,omitempty" jsonschema:"Optional file permissions in octal (e.g., '0600')"`
}

func (inj *SaveFleetPlanCredentialInjection) toFleet() *fleet.CredentialInjection {
	if inj == nil {
		return nil
	}
	out := &fleet.CredentialInjection{}
	for _, e := range inj.Env {
		out.Env = append(out.Env, fleet.EnvInjection{
			Credential: e.Credential,
			Var:        e.Var,
			Field:      e.Field,
		})
	}
	for _, f := range inj.Files {
		out.Files = append(out.Files, fleet.FileInjection{
			Credential: f.Credential,
			Path:       f.Path,
			Field:      f.Field,
			Format:     f.Format,
			Mode:       f.Mode,
		})
	}
	if len(out.Env) == 0 && len(out.Files) == 0 {
		return nil
	}
	return out
}

// SaveFleetPlanResult is the result of the save_fleet_plan tool.
type SaveFleetPlanResult struct {
	Status  string `json:"status"`
	Key     string `json:"key,omitempty"`
	Message string `json:"message"`
}

func saveFleetPlan(tc tool.Context, args SaveFleetPlanArgs) (SaveFleetPlanResult, error) {
	// Resolve effective stores from context (platform mode) or globals (personal mode)
	planStore := getEffectiveFleetPlanStore(tc)
	if planStore == nil {
		return SaveFleetPlanResult{
			Status:  "error",
			Message: "Fleet plan system is not initialized.",
		}, nil
	}

	templateStore := getEffectiveFleetTemplateStore(tc)
	if templateStore == nil {
		return SaveFleetPlanResult{
			Status:  "error",
			Message: "Fleet system is not initialized.",
		}, nil
	}

	// Validate required fields
	key := strings.TrimSpace(args.Key)
	if key == "" {
		return SaveFleetPlanResult{
			Status:  "error",
			Message: "key is required. Use a lowercase, hyphenated identifier like 'frontend-bugs'.",
		}, nil
	}
	name := strings.TrimSpace(args.Name)
	if name == "" {
		return SaveFleetPlanResult{
			Status:  "error",
			Message: "name is required.",
		}, nil
	}
	baseKey := strings.TrimSpace(args.BaseFleetKey)
	if baseKey == "" {
		return SaveFleetPlanResult{
			Status:  "error",
			Message: "base_fleet_key is required. List available fleets with ListAvailableFleets.",
		}, nil
	}

	// Load the base fleet config
	baseCfgAny, ok := templateStore.GetFleet(tc, baseKey)
	if !ok {
		summaries := templateStore.ListFleets(tc)
		keys := make([]string, len(summaries))
		for i, s := range summaries {
			keys[i] = s.Key
		}
		return SaveFleetPlanResult{
			Status:  "error",
			Message: fmt.Sprintf("Fleet %q not found. Available fleets: %s", baseKey, strings.Join(keys, ", ")),
		}, nil
	}
	baseCfg, ok := baseCfgAny.(*fleet.FleetConfig)
	if !ok {
		return SaveFleetPlanResult{
			Status:  "error",
			Message: fmt.Sprintf("Fleet %q has an unexpected type: %T", baseKey, baseCfgAny),
		}, nil
	}

	// Deep copy the base fleet config by marshalling/unmarshalling through JSON
	cfgJSON, err := json.Marshal(baseCfg)
	if err != nil {
		return SaveFleetPlanResult{
			Status:  "error",
			Message: fmt.Sprintf("Failed to snapshot base fleet config: %v", err),
		}, nil
	}
	var snapshotCfg fleet.FleetConfig
	if err := json.Unmarshal(cfgJSON, &snapshotCfg); err != nil {
		return SaveFleetPlanResult{
			Status:  "error",
			Message: fmt.Sprintf("Failed to snapshot base fleet config: %v", err),
		}, nil
	}

	// Filter agents if include_agents is specified
	if len(args.IncludeAgents) > 0 {
		if err := filterAgents(&snapshotCfg, args.IncludeAgents, baseKey); err != nil {
			return SaveFleetPlanResult{
				Status:  "error",
				Message: err.Error(),
			}, nil
		}
	}

	// Apply behavior overrides (append to existing behaviors)
	if len(args.BehaviorOverrides) > 0 {
		for agentKey, override := range args.BehaviorOverrides {
			agentCfg, exists := snapshotCfg.Agents[agentKey]
			if !exists {
				return SaveFleetPlanResult{
					Status:  "error",
					Message: fmt.Sprintf("Agent %q does not exist in fleet %q. Available agents: %s", agentKey, baseKey, agentKeysString(snapshotCfg.Agents)),
				}, nil
			}
			override = strings.TrimSpace(override)
			if override != "" {
				agentCfg.Behaviors = agentCfg.Behaviors + "\n\n" + override
				snapshotCfg.Agents[agentKey] = agentCfg
			}
		}
	}

	// Build channel config
	channelType := strings.TrimSpace(args.ChannelType)
	if channelType == "" {
		channelType = "chat"
	}

	// Non-chat channels require validation before saving
	if channelType != "chat" && !args.ValidationPassed {
		return SaveFleetPlanResult{
			Status:  "error",
			Message: fmt.Sprintf("Channel type %q requires validation before saving. Call validate_fleet_plan first and set validation_passed=true after it passes.", channelType),
		}, nil
	}

	channelCfg := fleet.PlanChannelConfig{
		Type:     channelType,
		Config:   args.ChannelConfig,
		Schedule: strings.TrimSpace(args.ChannelSchedule),
	}

	// Build artifact configs
	var artifacts map[string]fleet.PlanArtifactConfig
	if len(args.Artifacts) > 0 {
		artifacts = make(map[string]fleet.PlanArtifactConfig, len(args.Artifacts))
		for aName, a := range args.Artifacts {
			artifacts[aName] = fleet.PlanArtifactConfig{
				Type:          a.Type,
				Path:          a.Path,
				Repo:          a.Repo,
				BranchPattern: a.BranchPattern,
				SubPath:       a.SubPath,
				AutoPR:        a.AutoPR,
			}
		}
	}

	// Build and save the plan
	now := time.Now()

	// Build validation state
	validationStatus := "pending"
	if channelType == "chat" || args.ValidationPassed {
		validationStatus = "passed"
	}

	// Build ProjectSource config if provided
	var projectSource *fleet.ProjectSourceConfig
	if args.ProjectSource != nil {
		projectSource = &fleet.ProjectSourceConfig{
			Type: args.ProjectSource.Type,
			Repo: args.ProjectSource.Repo,
			Path: args.ProjectSource.Path,
		}
	}

	credInjection := args.CredentialInjection.toFleet()
	if err := fleet.ValidateCredentialInjectionSpec(args.Credentials, credInjection); err != nil {
		return SaveFleetPlanResult{
			Status:  "error",
			Message: err.Error(),
		}, nil
	}
	credInjection = fleet.NormalizeCredentialInjection(args.Credentials, credInjection)

	plan := &fleet.FleetPlan{
		Name:                  name,
		Key:                   key,
		Description:           strings.TrimSpace(args.Description),
		CreatedFrom:           baseKey,
		FleetConfig:           snapshotCfg,
		Credentials:           args.Credentials,
		CredentialInjection:   credInjection,
		Channel:               channelCfg,
		Artifacts:             artifacts,
		ProjectSource:         projectSource,
		Template:              strings.TrimSpace(args.Template),
		ContainerWorkspaceDir: strings.TrimSpace(args.ContainerWorkspaceDir),
		WorkspaceBaseDir:      strings.TrimSpace(args.WorkspaceBaseDir),
		Validation: fleet.PlanValidationState{
			Status:        validationStatus,
			LastValidated: now,
		},
		CreatedBy: store.UserIDFromContext(tc),
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := planStore.Save(tc, plan); err != nil {
		return SaveFleetPlanResult{
			Status:  "error",
			Message: fmt.Sprintf("Failed to save fleet plan: %v", err),
		}, nil
	}

	// Auto-activate non-chat plans so polling starts immediately.
	activated := false
	if channelType != "chat" && planActivatorFuncVar != nil {
		if err := planActivatorFuncVar(context.Background(), key); err != nil {
			slog.Warn("auto-activation failed for fleet plan", "component", "fleet-plan", "plan", key, "error", err)
		} else {
			activated = true
		}
	}

	// Build a channel-type-aware success message.
	var msg string
	switch {
	case channelType == "chat":
		msg = fmt.Sprintf("Fleet plan %q saved successfully. To start a session, go to the Fleet tab in Studio and click Launch on the plan.", name)
	case activated:
		msg = fmt.Sprintf("Fleet plan %q saved and activated. Monitoring is now live — new items on the configured channel will automatically trigger fleet sessions.", name)
	default:
		msg = fmt.Sprintf("Fleet plan %q saved successfully, but automatic activation failed. Go to the Fleet tab in Studio, select the plan, and click Activate to start monitoring.", name)
	}

	return SaveFleetPlanResult{
		Status:  "saved",
		Key:     key,
		Message: msg,
	}, nil
}

// filterAgents removes agents not in the includeList from the fleet config,
// and rebuilds the communication flow to only reference kept agents.
func filterAgents(cfg *fleet.FleetConfig, includeList []string, baseKey string) error {
	includeSet := make(map[string]bool, len(includeList))
	for _, role := range includeList {
		role = strings.TrimSpace(strings.ToLower(role))
		if role != "" {
			includeSet[role] = true
		}
	}

	// Validate that all requested agents exist in the base fleet
	for role := range includeSet {
		if _, exists := cfg.Agents[role]; !exists {
			return fmt.Errorf("agent %q does not exist in fleet %q. Available agents: %s", role, baseKey, agentKeysString(cfg.Agents))
		}
	}

	// Remove agents not in the include set
	for agentKey := range cfg.Agents {
		if !includeSet[agentKey] {
			delete(cfg.Agents, agentKey)
		}
	}

	// Rebuild communication flow to only include kept agents
	if cfg.Communication != nil && len(cfg.Communication.Flow) > 0 {
		var filteredFlow []fleet.CommunicationNode
		for _, node := range cfg.Communication.Flow {
			if !includeSet[node.Role] {
				continue
			}
			// Filter talks_to to only reference kept agents + "customer"
			var filteredTalksTo []string
			for _, target := range node.TalksTo {
				if includeSet[target] || target == "customer" {
					filteredTalksTo = append(filteredTalksTo, target)
				}
			}
			node.TalksTo = filteredTalksTo
			filteredFlow = append(filteredFlow, node)
		}

		// If only one agent remains, make it the entry point and ensure
		// it can talk to the customer (the human).
		if len(filteredFlow) == 1 {
			filteredFlow[0].EntryPoint = true
			hasCustomer := false
			for _, t := range filteredFlow[0].TalksTo {
				if t == "customer" {
					hasCustomer = true
					break
				}
			}
			if !hasCustomer {
				filteredFlow[0].TalksTo = append(filteredFlow[0].TalksTo, "customer")
			}
		}

		cfg.Communication.Flow = filteredFlow
	}

	return nil
}

// agentKeysString returns a comma-separated list of agent keys.
func agentKeysString(agents map[string]fleet.FleetAgentConfig) string {
	keys := make([]string, 0, len(agents))
	for k := range agents {
		keys = append(keys, k)
	}
	return strings.Join(keys, ", ")
}

// GetFleetPlanTools returns the fleet plan creation tool.
func GetFleetPlanTools() ([]tool.Tool, error) {
	t, err := functiontool.New(functiontool.Config{
		Name: "save_fleet_plan",
		Description: "Save a fleet plan configuration. Creates a reusable, fully-configured fleet definition " +
			"that includes the team composition (snapshotted from a base fleet), environment-specific " +
			"settings like communication channel and artifact destinations, credential mappings " +
			"for authenticating with external services, and credential_injection for runtime env/file " +
			"injection into fleet sandbox containers. " +
			"IMPORTANT: If credentials were validated with validate_fleet_plan, pass the same " +
			"credentials map and credential_injection here. Use save_credential during setup to store " +
			"secrets in the encrypted store. For file payloads, use type raw_content and inject field content. " +
			"Then reference store entry names in credentials and declare how they are injected " +
			"(env vars and/or file paths) in credential_injection. " +
			"Use include_agents to select a subset of agents from the template (e.g., only ['dev']). " +
			"The plan is stored as a YAML file and can be launched from the Studio UI.",
	}, saveFleetPlan)
	if err != nil {
		return nil, err
	}
	return []tool.Tool{t}, nil
}
