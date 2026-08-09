package launcher

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// codegraphInitFailedNotice is emitted when the .codegraph/ index cannot be
// created. It instructs the model to call gplan_gaps to skip the graph phase
// rather than failing or downgrading the mode.
const codegraphInitFailedNotice = "codegraph index is not available for this project " +
	"(npx @colbymchenry/codegraph init failed). " +
	"codegraph_explore will not be usable — call `gplan_gaps` immediately to skip to the GAP phase."

// EnsureCodegraph ensures the .codegraph/ index exists in workingDir so the
// codegraph MCP server can answer codegraph_explore queries. If the index
// already exists it returns "" immediately. If not, it attempts to create it
// via `npx --yes @colbymchenry/codegraph init`. On failure it returns a notice
// telling the model to call gplan_gaps to skip straight to the GAP phase.
//
// The function never forces a mode change — callers remain in Graph-Optimized
// Plan mode regardless of the return value.
func EnsureCodegraph(ctx context.Context, workingDir string) (notice string) {
	if workingDir == "" {
		workingDir = "."
	}

	indexDir := filepath.Join(workingDir, ".codegraph")
	if info, err := os.Stat(indexDir); err == nil && info.IsDir() {
		return "" // index already exists
	}

	cmd := exec.CommandContext(ctx, "npx", "--yes", "@colbymchenry/codegraph", "init")
	cmd.Dir = workingDir
	if out, err := cmd.CombinedOutput(); err != nil {
		detail := strings.TrimSpace(string(out))
		if detail != "" {
			return codegraphInitFailedNotice + " (detail: " + detail + ")"
		}
		return codegraphInitFailedNotice
	}
	return ""
}
