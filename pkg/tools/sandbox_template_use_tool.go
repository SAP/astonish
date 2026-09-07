package tools

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/sandbox"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

// UseSandboxTemplateArgs are the arguments for the use_sandbox_template tool.
type UseSandboxTemplateArgs struct {
	// TemplateName is the name of the template to switch to.
	TemplateName string `json:"template_name" jsonschema:"Name of the sandbox template to use (e.g., 'juicytrade'). Must be a template returned by list_sandbox_templates."`
}

// UseSandboxTemplateResult is the result of the use_sandbox_template tool.
type UseSandboxTemplateResult struct {
	Status       string `json:"status"`
	TemplateName string `json:"template_name,omitempty"`
	ContainerIP  string `json:"container_ip,omitempty"`
	Message      string `json:"message"`
}

// useSandboxTemplateDeps holds the dependencies injected by the factory.
type useSandboxTemplateDeps struct {
	nodePool         *sandbox.NodeClientPool
	toolPool         sandbox.ToolNodePool
	backend          sandbox.Backend
	templateRegistry *sandbox.TemplateRegistry
}

var useSandboxTemplateDepsVar *useSandboxTemplateDeps

// NewUseSandboxTemplateTool creates the use_sandbox_template tool.
// This tool runs on the HOST (not inside the container) so it can tear down
// the current session container and replace it with one cloned from the
// specified template. It is intentionally NOT listed in sandbox.containerTools.
func NewUseSandboxTemplateTool(nodePool *sandbox.NodeClientPool, templateRegistry *sandbox.TemplateRegistry) (tool.Tool, error) {
	useSandboxTemplateDepsVar = &useSandboxTemplateDeps{
		nodePool:         nodePool,
		templateRegistry: templateRegistry,
	}

	t, err := functiontool.New(functiontool.Config{
		Name: "use_sandbox_template",
		Description: "Switch the current sandbox session to use a specific template. " +
			"This tears down the current container (if any) and creates a new one cloned " +
			"from the specified template, which includes all pre-installed dependencies and " +
			"project code. Call this after the user selects a template from list_sandbox_templates. " +
			"After this call, all file and shell tools will operate inside the new container. " +
			"Browser tools use in-container Chromium — use localhost/127.0.0.1 in " +
			"browser_navigate URLs, not the bridge IP. Before run_drill in Studio, " +
			"use this tool when the suite template differs from the current sandbox. " +
			"IMPORTANT: This operation takes a few seconds as it recreates the container.",
	}, useSandboxTemplate)
	if err != nil {
		return nil, err
	}

	return t, nil
}

// NewUseSandboxTemplateToolFromBackend creates use_sandbox_template for
// Docker/K8s/OpenShell sessions. Destroys the current session sandbox and
// recreates it from the named overlay template.
func NewUseSandboxTemplateToolFromBackend(pool sandbox.ToolNodePool, backend sandbox.Backend, templateRegistry *sandbox.TemplateRegistry) (tool.Tool, error) {
	useSandboxTemplateDepsVar = &useSandboxTemplateDeps{
		toolPool:         pool,
		backend:          backend,
		templateRegistry: templateRegistry,
	}

	t, err := functiontool.New(functiontool.Config{
		Name: "use_sandbox_template",
		Description: "Switch the current sandbox session to use a specific template. " +
			"This tears down the current container (if any) and creates a new one from " +
			"the specified overlay template, which includes all pre-installed dependencies and " +
			"project code. Call this after the user selects a template from list_sandbox_templates. " +
			"After this call, all file and shell tools will operate inside the new container. " +
			"Browser tools use in-container Chromium — use localhost/127.0.0.1 in " +
			"browser_navigate URLs. Before run_drill in Studio, " +
			"use this tool when the suite template differs from the current sandbox.",
	}, useSandboxTemplate)
	if err != nil {
		return nil, err
	}
	return t, nil
}

func useSandboxTemplate(ctx tool.Context, args UseSandboxTemplateArgs) (UseSandboxTemplateResult, error) {
	if useSandboxTemplateDepsVar == nil {
		return UseSandboxTemplateResult{
			Status:  "error",
			Message: "Sandbox template system is not initialized. Ensure sandbox mode is enabled.",
		}, nil
	}

	deps := useSandboxTemplateDepsVar

	// Validate template name
	name := strings.TrimSpace(args.TemplateName)
	if name == "" {
		return UseSandboxTemplateResult{
			Status:  "error",
			Message: "template_name is required. Use list_sandbox_templates to see available templates.",
		}, nil
	}

	// Verify template exists in registry
	if err := deps.templateRegistry.Load(); err != nil {
		return UseSandboxTemplateResult{
			Status:  "error",
			Message: fmt.Sprintf("Failed to load template registry: %v", err),
		}, nil
	}

	if !deps.templateRegistry.Exists(name) {
		return UseSandboxTemplateResult{
			Status:  "error",
			Message: fmt.Sprintf("Template %q not found. Use list_sandbox_templates to see available templates.", name),
		}, nil
	}

	// Get session ID
	var sessionID string
	if ctx != nil {
		sessionID = ctx.SessionID()
	}
	if sessionID == "" {
		return UseSandboxTemplateResult{
			Status:  "error",
			Message: "No session ID available. Cannot determine which container to replace.",
		}, nil
	}

	if deps.backend != nil && deps.toolPool != nil {
		return useSandboxTemplateBackend(ctx, sessionID, name, deps)
	}

	if deps.nodePool == nil {
		return UseSandboxTemplateResult{
			Status:  "error",
			Message: "Sandbox template system is not initialized. Ensure sandbox mode is enabled.",
		}, nil
	}

	// Replace the session container with one cloned from the selected template
	slog.Info("replacing session container with template", "component", "sandbox-template", "session", sessionID[:min(8, len(sessionID))], "template", name)
	if err := deps.nodePool.ReplaceSession(sessionID, name); err != nil {
		return UseSandboxTemplateResult{
			Status:  "error",
			Message: fmt.Sprintf("Failed to switch to template %q: %v", name, err),
		}, nil
	}

	slog.Info("session now using template", "component", "sandbox-template", "session", sessionID[:min(8, len(sessionID))], "template", name)

	// Eagerly bind the new session to trigger container creation, then
	// discover the container's bridge IP (returned as container_ip for
	// {{CONTAINER_IP}} / rare host-side needs — not for browser_navigate).
	var containerIP string
	if client := deps.nodePool.GetOrCreate(sessionID); client != nil {
		if ip, err := client.GetContainerIP(sessionID); err == nil {
			containerIP = ip
			slog.Info("discovered container IP", "component", "sandbox-template", "session", sessionID[:min(8, len(sessionID))], "ip", ip)
		} else {
			slog.Warn("could not discover container IP", "component", "sandbox-template", "session", sessionID[:min(8, len(sessionID))], "error", err)
		}
	}

	// Inject template bootstrap files (mount only — never auto-exec).
	sandbox.InjectBootstrapFilesAfterSwitch(deps.nodePool, deps.templateRegistry, sessionID, name)

	msg := fmt.Sprintf("Sandbox container switched to template %q. All file and shell tools now "+
		"operate inside a container with the template's pre-installed dependencies and project code. "+
		"Browser tools use Chromium inside the same container — use http://localhost:<port> or "+
		"http://127.0.0.1:<port> in browser_navigate (do not rewrite URLs to the container bridge IP).", name)
	if containerIP != "" {
		msg += fmt.Sprintf(" Bridge IP %s is available as container_ip for rare host-side needs "+
			"(e.g. {{CONTAINER_IP}}); prefer localhost for shell and browser.", containerIP)
	}

	return UseSandboxTemplateResult{
		Status:       "ok",
		TemplateName: name,
		ContainerIP:  containerIP,
		Message:      msg,
	}, nil
}

func useSandboxTemplateBackend(_ tool.Context, sessionID, name string, deps *useSandboxTemplateDeps) (UseSandboxTemplateResult, error) {
	slog.Info("replacing session sandbox with template", "component", "sandbox-template", "session", sessionID[:min(8, len(sessionID))], "template", name)
	bctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := deps.backend.DestroySession(bctx, sessionID); err != nil {
		slog.Warn("destroy before template switch failed", "component", "sandbox-template", "error", err)
	}
	deps.toolPool.Remove(sessionID)
	client := deps.toolPool.GetOrCreateWithTemplate(sessionID, name)
	if client == nil {
		return UseSandboxTemplateResult{
			Status:  "error",
			Message: fmt.Sprintf("Failed to switch to template %q: sandbox pool is closed", name),
		}, nil
	}
	client.BindSession(sessionID)
	if err := client.EnsureReady(sessionID); err != nil {
		return UseSandboxTemplateResult{
			Status:  "error",
			Message: fmt.Sprintf("Failed to switch to template %q: %v", name, err),
		}, nil
	}

	files := sandbox.LookupBootstrapFiles(bctx, deps.templateRegistry, nil, name)
	if err := sandbox.MaterializeBootstrapFiles(bctx, deps.backend, sessionID, files); err != nil {
		slog.Warn("bootstrap inject after template switch failed", "component", "sandbox-template", "error", err)
	}

	msg := fmt.Sprintf("Sandbox container switched to template %q. All file and shell tools now "+
		"operate inside a container with the template's pre-installed dependencies and project code. "+
		"Browser tools use Chromium inside the same container — use http://localhost:<port> or "+
		"http://127.0.0.1:<port> in browser_navigate.", name)
	return UseSandboxTemplateResult{
		Status:       "ok",
		TemplateName: name,
		Message:      msg,
	}, nil
}
