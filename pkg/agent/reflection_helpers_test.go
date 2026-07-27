package agent

import (
	"strings"
	"testing"
)

func TestBuildReflectionContextIncludesToolResultSummary(t *testing.T) {
	trace := NewExecutionTrace("list the kubernetes cluster in openstack")
	trace.RecordStep("resolve_credential", map[string]any{"name": "openstack"}, map[string]any{
		"status":  "error",
		"message": "Credential \"openstack\" not found",
	}, nil)
	trace.RecordStep("list_credentials", map[string]any{"filter": "openstack"}, map[string]any{
		"count": 2,
		"credentials": []any{
			map[string]any{"name": "openstack-keystone", "type": "openstack_keystone", "scope": "personal"},
		},
	}, nil)
	trace.RecordStep("resolve_credential", map[string]any{"name": "openstack-keystone"}, map[string]any{
		"status":   "ok",
		"auth_url": "https://identity-3.qa-de-1.cloud.sap/v3/auth/tokens",
		"token":    "{{CREDENTIAL:openstack-keystone:token}}",
		"message":  "Use http_request with credential parameter for Keystone — X-Auth-Token is injected automatically.",
	}, nil)
	trace.RecordStep("shell_command", map[string]any{"command": "curl Kubernikus"}, map[string]any{
		"exit_code": 0,
		"stdout": "Kubernikus endpoint: https://kubernikus.qa-de-1.cloud.sap\n" +
			"=== Kubernikus Clusters ===\n" +
			"GET /api/v1/clusters returned cluster p-qa-de-1",
	}, nil)
	trace.Finalize()

	ctx := buildReflectionContext(trace, nil)
	for _, want := range []string{
		"openstack-keystone",
		"openstack_keystone",
		"{{CREDENTIAL:openstack-keystone:token}}",
		"https://kubernikus.qa-de-1.cloud.sap",
		"/api/v1/clusters",
	} {
		if !strings.Contains(ctx, want) {
			t.Fatalf("reflection context missing %q:\n%s", want, ctx)
		}
	}
}

func TestSummarizeReflectionToolResultTruncatesLargeShellOutput(t *testing.T) {
	trace := NewExecutionTrace("large output")
	trace.RecordStep("shell_command", map[string]any{"command": "produce output"}, map[string]any{
		"exit_code": 0,
		"stdout":    strings.Repeat("x", maxReflectionShellStreamLen+500),
	}, nil)
	trace.Finalize()

	ctx := buildReflectionContext(trace, nil)
	if !strings.Contains(ctx, "...") {
		t.Fatalf("expected truncated shell output marker in reflection context:\n%s", ctx)
	}
	if strings.Contains(ctx, strings.Repeat("x", maxReflectionShellStreamLen+100)) {
		t.Fatalf("reflection context contains unbounded shell output")
	}
}

func TestPlatformReflectionPromptDistinguishesAccessRecipeFromLiveInventory(t *testing.T) {
	for _, want := range []string{
		"Discovered access recipes",
		"openstack-keystone",
		"kubernikus",
		"GET $KUBERNIKUS_URL/api/v1/clusters",
		"Do NOT save the current cluster list, node counts, health, or phase",
		"Lists of resources that change over time",
	} {
		if !strings.Contains(platformReflectionPrompt, want) {
			t.Fatalf("platform reflection prompt missing %q", want)
		}
	}
}
