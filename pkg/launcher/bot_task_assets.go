package launcher

import (
	"fmt"
	"sort"

	"github.com/SAP/astonish/pkg/store"
)

// botTaskToolDescription makes the existing scoped alternatives discoverable;
// descriptions never add tools to a saved mandate or grant filesystem access.
func botTaskToolDescription(name string) string {
	switch name {
	case "bot_task_read":
		return "Inspect a file in the selected Bot Home Task's pinned bundle, not the host filesystem or an arbitrary project. path defaults to TASK.md. Returns content plus a sorted available_paths manifest; missing files return an explicit error. Use task_id only for another owned Task."
	case "bot_task_asset":
		return "Save a bounded text file in the selected Bot Home Task as a new versioned asset: supply relative path and content. Useful for a Markdown design document (references/design.md), Mermaid diagram source (references/architecture.mmd), or supplied project source. Select or capture a Task first. This saves bytes only: it does not render, execute, publish a download, or verify the design. No host filesystem access."
	case "bot_task_capture":
		return "Capture a draft Task in the current Bot Home using title and content when no Task is selected. Then use bot_task_asset for design documents, diagram source, or other bounded text assets. This is not required to deliver a final answer and grants no project or host filesystem access."
	case "bot_task_execute":
		return "Execute one declared executable Python helper from the selected Task's pinned bundle. Requires path under scripts/ ending in .py (for example scripts/helper.py); task_id optionally selects another owned Task. This does not invoke, enqueue or schedule a Task. For a prose TASK.md procedure, perform the work directly using authorized tools. Existing sandbox, lease, budget and authority checks apply."
	default:
		return "Bounded current Bot Home Task capability. Files and IDs grant no authority. Draft capture and updates require no finish ritual. Scripts are untested assets until independently validated."
	}
}

// botTaskBundleRead inspects only retained bytes already loaded and verified by
// TaskWorkspace.Load. It deliberately has no filesystem or backend fallback.
func botTaskBundleRead(ref store.BotTaskRef, bundle map[string][]byte, path string) (map[string]any, error) {
	if path == "" {
		path = "TASK.md"
	}
	if store.ValidateBotTaskAssetPath(path) != nil {
		return nil, fmt.Errorf("%w: path must be relative to the selected Task bundle", store.ErrBotInvalid)
	}
	content, ok := bundle[path]
	if !ok {
		return nil, fmt.Errorf("%w: file %q is not in the selected Task bundle; read TASK.md for available_paths. Host and arbitrary project files are not accessible through this tool", store.ErrBotNotFound, path)
	}
	paths := make([]string, 0, len(bundle))
	for p := range bundle {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return map[string]any{"task": ref, "path": path, "content": string(content), "available_paths": paths}, nil
}
