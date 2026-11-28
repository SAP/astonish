package tools

import (
	"fmt"

	"github.com/SAP/astonish/pkg/sandbox"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

// ListSandboxTemplatesArgs are the arguments for the list_sandbox_templates tool.
// No arguments are required — this tool lists all available templates.
type ListSandboxTemplatesArgs struct {
	// Name optionally filters to a single template for detailed info.
	Name string `json:"name,omitempty" jsonschema:"Optional template name to get details for a specific template. Omit to list all templates."`
}

// TemplateInfo is a single template entry returned by list_sandbox_templates.
type TemplateInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	CreatedAt   string `json:"created_at"`
	SnapshotAt  string `json:"snapshot_at,omitempty"`
	FleetPlans  string `json:"fleet_plans,omitempty"`
	BasedOn     string `json:"based_on,omitempty"`
}

// ListSandboxTemplatesResult is the result of the list_sandbox_templates tool.
type ListSandboxTemplatesResult struct {
	Status    string         `json:"status"`
	Count     int            `json:"count"`
	Templates []TemplateInfo `json:"templates"`
	Message   string         `json:"message"`
}

// NewListSandboxTemplatesTool creates the list_sandbox_templates tool.
// This tool runs on the HOST (not inside the container) so it can access the
// template registry. It is intentionally NOT listed in sandbox.containerTools.
func NewListSandboxTemplatesTool(templateRegistry *sandbox.TemplateRegistry) (tool.Tool, error) {
	t, err := functiontool.New(functiontool.Config{
		Name: "list_sandbox_templates",
		Description: "List available sandbox templates. Returns all saved templates with their " +
			"name, description, creation date, and snapshot status. Optionally pass a template " +
			"name to get details for a specific template. Use this tool instead of " +
			"shell_command with 'astonish sandbox template list' — shell_command runs inside " +
			"the container and cannot see host templates.",
	}, func(ctx tool.Context, args ListSandboxTemplatesArgs) (ListSandboxTemplatesResult, error) {
		return listSandboxTemplates(ctx, args, templateRegistry)
	})
	if err != nil {
		return nil, err
	}

	return t, nil
}

func listSandboxTemplates(_ tool.Context, args ListSandboxTemplatesArgs, registry *sandbox.TemplateRegistry) (ListSandboxTemplatesResult, error) {
	if registry == nil {
		return ListSandboxTemplatesResult{
			Status:  "error",
			Message: "Sandbox template system is not initialized. Ensure sandbox mode is enabled.",
		}, nil
	}

	// Reload from disk to get latest state
	if err := registry.Load(); err != nil {
		return ListSandboxTemplatesResult{
			Status:  "error",
			Message: fmt.Sprintf("Failed to load template registry: %v", err),
		}, nil
	}

	allTemplates := registry.List()

	// If a specific name was requested, filter to that template
	if args.Name != "" {
		meta := registry.Get(args.Name)
		if meta == nil {
			return ListSandboxTemplatesResult{
				Status:  "not_found",
				Count:   0,
				Message: fmt.Sprintf("No template named %q found. Use this tool without a name to list all templates.", args.Name),
			}, nil
		}
		allTemplates = []*sandbox.TemplateMeta{meta}
	}

	if len(allTemplates) == 0 {
		return ListSandboxTemplatesResult{
			Status:  "empty",
			Count:   0,
			Message: "No sandbox templates found. Use option (B) to set up a new container from scratch.",
		}, nil
	}

	templates := make([]TemplateInfo, 0, len(allTemplates))
	for _, meta := range allTemplates {
		info := TemplateInfo{
			Name:      meta.Name,
			CreatedAt: meta.CreatedAt.Format("2006-01-02 15:04:05"),
			BasedOn:   meta.BasedOn,
		}
		if meta.Description != "" {
			info.Description = meta.Description
		}
		if !meta.SnapshotAt.IsZero() {
			info.SnapshotAt = meta.SnapshotAt.Format("2006-01-02 15:04:05")
		}
		if len(meta.FleetPlans) > 0 {
			info.FleetPlans = fmt.Sprintf("%v", meta.FleetPlans)
		}
		templates = append(templates, info)
	}

	return ListSandboxTemplatesResult{
		Status:    "ok",
		Count:     len(templates),
		Templates: templates,
		Message:   fmt.Sprintf("Found %d template(s).", len(templates)),
	}, nil
}
