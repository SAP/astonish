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

// placeholderizeContent uses the Redactor from context to replace any raw
// credential values in memory content with {{CREDENTIAL:name:field}} tokens.
// Returns the (possibly modified) content and how many credential values were replaced.
// If no Redactor is in context, returns the content unchanged (0 replacements).
func placeholderizeContent(ctx context.Context, content string) (string, int) {
	r := credentials.RedactorFromContext(ctx)
	if r == nil {
		return content, 0
	}
	return r.Placeholderize(content)
}

// MemorySaveArgs defines the arguments for the memory_save tool.
type MemorySaveArgs struct {
	Category  string `json:"category" jsonschema:"A short category heading for organizing the memory (e.g., SSH Interactive Login, Proxmox API, Browser Quirks)"`
	Content   string `json:"content" jsonschema:"The facts to save, one per line. Use '- ' prefix for bullet points."`
	Kind      string `json:"kind,omitempty" jsonschema:"Knowledge category: tools, workarounds, infrastructure, projects, others. Omit for core MEMORY.md."`
	Overwrite bool   `json:"overwrite,omitempty" jsonschema:"When true, replaces the entire section instead of appending. Use this to correct outdated facts — provide the complete corrected section content."`
}

// MemorySaveResult is returned after saving to memory.
type MemorySaveResult struct {
	Saved   bool   `json:"saved"`
	Message string `json:"message"`
}

// MemorySave creates the memory_save handler. In platform mode it routes to
// PG-backed stores from the request context.
func MemorySave() func(ctx tool.Context, args MemorySaveArgs) (MemorySaveResult, error) {
	return func(ctx tool.Context, args MemorySaveArgs) (MemorySaveResult, error) {
		if args.Category == "" {
			return MemorySaveResult{}, fmt.Errorf("category is required")
		}
		if args.Content == "" {
			return MemorySaveResult{}, fmt.Errorf("content is required")
		}

		// Auto-replace any raw credential values with {{CREDENTIAL:name:field}}
		// placeholders. This prevents secrets from being persisted to memory
		// while preserving actionable references the agent can use later.
		content, redactCount := placeholderizeContent(ctx, args.Content)

		// Platform mode: use the PG-backed memory store from context.
		if pgMem := store.MemoryStoreFromContext(ctx); pgMem != nil {
			return platformMemorySave(ctx, args, content, redactCount, pgMem)
		}

		return MemorySaveResult{
			Saved:   false,
			Message: "Memory save is not available (no memory store in context).",
		}, nil
	}
}

// platformMemorySave is the shared implementation for platform-mode saves.
// Writes to the PG team memory store.
// Uses cross-session merge when a MemorySaveOrMergeFunc is available in context.
func platformMemorySave(ctx context.Context, args MemorySaveArgs, content string, redactCount int, pgMem store.MemoryStore) (MemorySaveResult, error) {
	kind := strings.TrimSpace(strings.ToLower(args.Kind))

	// Determine the category for the database entry.
	// In platform mode, "kind" maps to the PG category column.
	dbCategory := args.Category
	if kind != "" {
		dbCategory = kind + "/" + args.Category
	}

	// Save to the team memory store (PG)
	entry := store.MemoryEntry{
		Content:   content,
		Category:  dbCategory,
		SessionID: store.SessionIDFromContext(ctx),
		CreatedBy: store.UserIDFromContext(ctx),
	}

	mergeFunc := store.MemorySaveOrMergeFromContext(ctx)
	if mergeFunc == nil {
		return MemorySaveResult{}, fmt.Errorf("failed to save memory: scenario-card merger is not available")
	}
	if err := mergeFunc(ctx, pgMem, entry); err != nil {
		return MemorySaveResult{}, fmt.Errorf("failed to save memory: %w", err)
	}

	msg := fmt.Sprintf("Saved to team memory under '%s'", dbCategory)
	if redactCount > 0 {
		msg += fmt.Sprintf(" (%d credential value(s) auto-replaced with {{CREDENTIAL:...}} placeholders)", redactCount)
	}
	return MemorySaveResult{
		Saved:   true,
		Message: msg,
	}, nil
}

// PlatformMemorySave saves facts to memory using the store.MemoryStore interface.
// Used in platform mode where memories are stored in PostgreSQL instead of files.
// The memMgr is used for MEMORY.md core tier (file-based), while the memStore
// handles team-scoped knowledge tier saves.
func PlatformMemorySave(memMgr store.MemoryManager, memStore store.MemoryStore) func(ctx tool.Context, args MemorySaveArgs) (MemorySaveResult, error) {
	return func(ctx tool.Context, args MemorySaveArgs) (MemorySaveResult, error) {
		if args.Category == "" {
			return MemorySaveResult{}, fmt.Errorf("category is required")
		}
		if args.Content == "" {
			return MemorySaveResult{}, fmt.Errorf("content is required")
		}

		// Auto-replace any raw credential values with {{CREDENTIAL:name:field}}
		// placeholders.
		content, redactCount := placeholderizeContent(ctx, args.Content)

		// Route to the PG store
		if memStore != nil {
			return platformMemorySave(ctx, args, content, redactCount, memStore)
		}

		return MemorySaveResult{
			Saved:   false,
			Message: "Memory save is not available.",
		}, nil
	}
}

// NewMemorySaveTool creates the memory_save tool.
// In platform mode, the tool checks the context for a PG-backed MemoryStore
// (injected by ChatRunner.InjectMemoryStores) and writes there.
func NewMemorySaveTool() (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name: "memory_save",
		Description: "Save durable facts to persistent memory. Omit 'kind' for core MEMORY.md (identity/preferences/connections). " +
			"Use kind='tools' for tool quirks/CLI syntax/API patterns, kind='workarounds' for problems+solutions, " +
			"kind='infrastructure' for servers/networking/services, kind='projects' for project-specific knowledge, " +
			"kind='others' for anything else. Set overwrite=true to replace outdated sections. NEVER save ephemeral data.",
	}, MemorySave())
}

// NewPlatformMemorySaveTool creates the memory_save tool for platform mode.
// Saves to the team-scoped PostgreSQL memory store.
func NewPlatformMemorySaveTool(memMgr store.MemoryManager, memStore store.MemoryStore) (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name: "memory_save",
		Description: "Save durable facts to persistent memory. Omit 'kind' for core MEMORY.md (identity/preferences/connections). " +
			"Use kind='tools' for tool quirks/CLI syntax/API patterns, kind='workarounds' for problems+solutions, " +
			"kind='infrastructure' for servers/networking/services, kind='projects' for project-specific knowledge, " +
			"kind='others' for anything else. Set overwrite=true to replace outdated sections. NEVER save ephemeral data.",
	}, PlatformMemorySave(memMgr, memStore))
}
