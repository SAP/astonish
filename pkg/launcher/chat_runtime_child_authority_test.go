package launcher

import (
	"testing"
)

// childAuthorityBinding resolves the shared chatRuntimeTool binding used by the
// surviving delegate-execution tests. The Bot child-authority acceptance tests
// that used to live here asserted execution tools (run_drill/shell) on the
// tools-less Employee agent and were removed with the Employee private-worker
// migration; this helper remains only for the still-passing delegate tests.
func childAuthorityBinding(t *testing.T, r *servicesParityRuntime) *chatRuntimeBinding {
	t.Helper()
	return r.chat.ToolIndex.GetToolEntry("run_drill").Tool.(*chatRuntimeTool).binding
}
