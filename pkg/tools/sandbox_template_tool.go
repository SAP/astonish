package tools

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

// SaveSandboxTemplateArgs are the arguments for the save_sandbox_template tool.
type SaveSandboxTemplateArgs struct {
	// TemplateName is the name for the new template (lowercase, hyphens).
	// This is the value that goes into save_fleet_plan's template field.
	TemplateName string `json:"template_name" jsonschema:"Name for the new sandbox template (lowercase, hyphens, e.g., 'my-project'). This is the value you'll pass to save_fleet_plan's template field."`
	// Description is a human-readable description of what's installed.
	Description string `json:"description,omitempty" jsonschema:"Human-readable description of the template contents (e.g., 'Go 1.24 + node 22 + project dependencies')"`
	// BootstrapFiles are non-secret scripts injected into every container from
	// this template at session start (mount only — not auto-executed). Include
	// .astonish/start-services.sh (and optionally stop-services.sh) with absolute paths.
	BootstrapFiles []BootstrapFileArg `json:"bootstrap_files,omitempty" jsonschema:"Non-secret bootstrap files to inject on every container launch (e.g. start-services.sh). Absolute paths required."`
	// Overwrite replaces an existing template with the same name (delete then re-snapshot).
	// Required to update a named template after fixing bootstrap scripts or rebuilding deps.
	Overwrite bool `json:"overwrite,omitempty" jsonschema:"If true, replace an existing template with the same name. Refused for the reserved name 'base'."`
}

// BootstrapFileArg is one bootstrap file passed to save_sandbox_template.
type BootstrapFileArg struct {
	Path    string `json:"path" jsonschema:"Absolute path inside the container (e.g. /root/myapp/.astonish/start-services.sh)"`
	Content string `json:"content" jsonschema:"Full file contents"`
	Mode    string `json:"mode,omitempty" jsonschema:"Optional octal mode (default 0755)"`
}

// SaveSandboxTemplateResult is the result of the save_sandbox_template tool.
type SaveSandboxTemplateResult struct {
	Status       string `json:"status"`
	TemplateName string `json:"template_name,omitempty"`
	Message      string `json:"message"`
}

// sandboxTemplateDeps holds the dependencies injected by the factory via closure.
// This follows the same closure capture pattern used by browser and email tools.
type sandboxTemplateDeps struct {
	backend          sandbox.Backend
	nodePool         *sandbox.NodeClientPool
	templateRegistry *sandbox.TemplateRegistry
	sessionRegistry  *sandbox.SessionRegistry
}

var sandboxTemplateDepsVar *sandboxTemplateDeps

// NewSaveSandboxTemplateTool creates the save_sandbox_template tool with the
// given dependencies captured from the factory scope. Returns the tool and
// an error if creation fails.
//
// This tool freezes the current sandbox container as a reusable template.
// The wizard calls it after installing all project dependencies and cloning
// the repo inside the container. Later, fleet sessions clone from this
// custom template instead of @base.
func NewSaveSandboxTemplateTool(nodePool *sandbox.NodeClientPool, templateRegistry *sandbox.TemplateRegistry, sessionRegistry *sandbox.SessionRegistry) (tool.Tool, error) {
	sandboxTemplateDepsVar = &sandboxTemplateDeps{
		nodePool:         nodePool,
		templateRegistry: templateRegistry,
		sessionRegistry:  sessionRegistry,
	}

	t, err := functiontool.New(functiontool.Config{
		Name: "save_sandbox_template",
		Description: "Freeze the current sandbox container as a reusable template. " +
			"Call this after cloning the project repo, installing dependencies, and configuring the " +
			"development environment inside the container. The template captures the entire container " +
			"state so future fleet sessions start with everything pre-installed. " +
			"Pass bootstrap_files with absolute-path start/stop scripts (e.g. .astonish/start-services.sh) " +
			"so every future container from this template gets those files injected (not auto-run). " +
			"Pass overwrite=true to replace an existing template with the same name (e.g. after fixing " +
			"start-services.sh). Self-overwrite (saving the template this session is based on) flattens " +
			"onto the parent (usually @base) and never deletes the source before the new layer is ready. " +
			"Cannot overwrite the reserved name 'base'. " +
			"The returned template_name should be passed to save_fleet_plan's template field. " +
			"IMPORTANT: This tool stops the container node process, snapshots the container, " +
			"and restarts the node. Tool calls will be temporarily unavailable during the snapshot.",
	}, saveSandboxTemplate)
	if err != nil {
		return nil, err
	}

	return t, nil
}

// NewSaveSandboxTemplateToolFromBackend creates save_sandbox_template for
// Docker/K8s/OpenShell sessions. Captures the session upper as a named layer.
func NewSaveSandboxTemplateToolFromBackend(backend sandbox.Backend, templateRegistry *sandbox.TemplateRegistry, sessionRegistry *sandbox.SessionRegistry) (tool.Tool, error) {
	sandboxTemplateDepsVar = &sandboxTemplateDeps{
		backend:          backend,
		templateRegistry: templateRegistry,
		sessionRegistry:  sessionRegistry,
	}

	t, err := functiontool.New(functiontool.Config{
		Name: "save_sandbox_template",
		Description: "Freeze the current sandbox session as a reusable overlay template. " +
			"Call this after cloning the project repo, installing dependencies, and configuring the " +
			"development environment inside the container. The template captures the writable overlay " +
			"so future sessions start with everything pre-installed. " +
			"Pass bootstrap_files with absolute-path start/stop scripts (e.g. .astonish/start-services.sh) " +
			"so every future container from this template gets those files injected (not auto-run). " +
			"Pass overwrite=true to replace an existing template with the same name. " +
			"Cannot overwrite the reserved name 'base'. " +
			"The returned template_name should be passed to save_fleet_plan's template field.",
	}, saveSandboxTemplate)
	if err != nil {
		return nil, err
	}
	return t, nil
}

func saveSandboxTemplate(ctx tool.Context, args SaveSandboxTemplateArgs) (SaveSandboxTemplateResult, error) {
	if sandboxTemplateDepsVar == nil {
		return SaveSandboxTemplateResult{
			Status:  "error",
			Message: "Sandbox template system is not initialized. Ensure sandbox mode is enabled.",
		}, nil
	}

	deps := sandboxTemplateDepsVar
	if deps.backend != nil {
		return saveSandboxTemplateBackend(ctx, args, deps)
	}
	return SaveSandboxTemplateResult{
		Status:  "error",
		Message: "Sandbox template system requires a Docker, Kubernetes, or OpenShell backend.",
	}, nil
}

type layerAliaser interface {
	AliasLayer(name, layerID string) error
}

func saveSandboxTemplateBackend(ctx tool.Context, args SaveSandboxTemplateArgs, deps *sandboxTemplateDeps) (SaveSandboxTemplateResult, error) {
	var sessionID string
	if ctx != nil {
		sessionID = ctx.SessionID()
	}
	if sessionID == "" {
		return SaveSandboxTemplateResult{
			Status:  "error",
			Message: "No session ID available. Cannot determine which sandbox to snapshot.",
		}, nil
	}

	name := strings.TrimSpace(args.TemplateName)
	if name == "" {
		return SaveSandboxTemplateResult{
			Status:  "error",
			Message: "template_name is required. Use a lowercase, hyphenated name like 'my-project'.",
		}, nil
	}
	if name == "base" || name == sandbox.BaseTemplateID {
		return SaveSandboxTemplateResult{
			Status:  "error",
			Message: "Cannot use 'base' as a template name (reserved).",
		}, nil
	}

	bctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	art, err := deps.backend.SaveSessionAsTemplate(bctx, sessionID)
	if err != nil {
		return SaveSandboxTemplateResult{
			Status:  "error",
			Message: fmt.Sprintf("Failed to capture template layer: %v", err),
		}, nil
	}
	if aliaser, ok := deps.backend.(layerAliaser); ok {
		if err := aliaser.AliasLayer(name, art.LayerID); err != nil {
			return SaveSandboxTemplateResult{
				Status:  "error",
				Message: fmt.Sprintf("Failed to name template layer %q: %v", name, err),
			}, nil
		}
	}

	basedOn := sandbox.BaseTemplateID
	if deps.sessionRegistry != nil {
		if entry := deps.sessionRegistry.Get(sessionID); entry != nil && entry.TemplateName != "" {
			basedOn = entry.TemplateName
		}
	}
	if deps.templateRegistry != nil {
		meta := deps.templateRegistry.Get(name)
		if meta == nil {
			meta = &sandbox.TemplateMeta{Name: name, CreatedAt: time.Now().UTC()}
		}
		if desc := strings.TrimSpace(args.Description); desc != "" {
			meta.Description = desc
		}
		meta.BasedOn = basedOn
		meta.SnapshotAt = time.Now().UTC()
		if err := deps.templateRegistry.Add(meta); err != nil {
			return SaveSandboxTemplateResult{
				Status:  "error",
				Message: fmt.Sprintf("Failed to register template %q: %v", name, err),
			}, nil
		}
	}

	bootstrapNote := ""
	if len(args.BootstrapFiles) > 0 && deps.templateRegistry != nil {
		files := make([]store.BootstrapFile, 0, len(args.BootstrapFiles))
		for _, f := range args.BootstrapFiles {
			files = append(files, store.BootstrapFile{Path: f.Path, Content: f.Content, Mode: f.Mode})
		}
		if err := sandbox.PersistBootstrapFiles(deps.templateRegistry, name, files); err != nil {
			slog.Warn("failed to persist bootstrap_files on template registry", "component", "sandbox-template", "template", name, "error", err)
			bootstrapNote = fmt.Sprintf(" Warning: bootstrap_files were not saved (%v).", err)
		} else {
			bootstrapNote = fmt.Sprintf(" Saved %d bootstrap file(s) for injection on every launch.", len(files))
		}
	}

	action := "created"
	if args.Overwrite {
		action = "updated"
	}
	return SaveSandboxTemplateResult{
		Status:       "saved",
		TemplateName: name,
		Message: fmt.Sprintf("Template %q %s and ready for cloning (layer %s, %d bytes). "+
			"Pass template: %q to save_fleet_plan to bind fleet sessions to this template.%s",
			name, action, art.LayerID, art.SizeBytes, name, bootstrapNote),
	}, nil
}
