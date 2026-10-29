package agent

import (
	"context"
	"strings"
	"testing"
)

func TestActiveApp_CRUD(t *testing.T) {
	ca := &ChatAgent{
		activeApps: make(map[string]*ActiveApp),
	}
	sessionID := "test-session-1"

	// Initially no active app
	if ca.HasActiveApp(sessionID) {
		t.Fatal("expected no active app initially")
	}
	if ca.GetActiveApp(sessionID) != nil {
		t.Fatal("expected nil for non-existent app")
	}

	// Set an active app
	app := &ActiveApp{
		AppID:   "uuid-1",
		Title:   "Sales Dashboard",
		Code:    "function SalesDashboard() { return <div>hello</div> }",
		Version: 1,
	}
	ca.SetActiveApp(sessionID, app)

	if !ca.HasActiveApp(sessionID) {
		t.Fatal("expected active app after Set")
	}
	got := ca.GetActiveApp(sessionID)
	if got == nil || got.AppID != "uuid-1" {
		t.Fatalf("unexpected app: %+v", got)
	}

	// Record modification
	ca.RecordAppModification(sessionID, "make header blue")
	got = ca.GetActiveApp(sessionID)
	if len(got.Modifications) != 1 || got.Modifications[0] != "make header blue" {
		t.Fatalf("unexpected modifications: %v", got.Modifications)
	}

	// Clear
	ca.ClearActiveApp(sessionID)
	if ca.HasActiveApp(sessionID) {
		t.Fatal("expected no active app after clear")
	}
}

func TestClassifyAppIntent_MagicStrings(t *testing.T) {
	ca := &ChatAgent{
		activeApps: make(map[string]*ActiveApp),
	}

	tests := []struct {
		msg    string
		expect AppRefinementIntent
	}{
		// Magic markers from UI buttons — no LLM needed
		{"__app_save__", AppIntentSave},
		{"__app_done__", AppIntentSave},
		// Without LLM (nil), everything else defaults to refine
		{"make the header blue", AppIntentRefine},
		{"save it", AppIntentRefine},       // no LLM = defaults to refine (heuristics removed)
		{"done", AppIntentRefine},           // no LLM = defaults to refine
		{"looks good", AppIntentRefine},     // no LLM = defaults to refine
		{"what's the weather?", AppIntentRefine}, // no LLM = defaults to refine
	}

	for _, tt := range tests {
		result := ca.ClassifyAppIntent(context.Background(), tt.msg, nil)
		if result.Intent != tt.expect {
			t.Errorf("ClassifyAppIntent(%q, nil) = %d, want %d", tt.msg, result.Intent, tt.expect)
		}
		if result.SaveName != "" {
			t.Errorf("ClassifyAppIntent(%q, nil) SaveName = %q, want empty", tt.msg, result.SaveName)
		}
	}
}

func TestClassifyAppIntent_WithLLM(t *testing.T) {
	ca := &ChatAgent{
		activeApps: make(map[string]*ActiveApp),
	}

	tests := []struct {
		msg      string
		llmReply string
		expect   AppRefinementIntent
		saveName string
	}{
		{"make it blue", "REFINE", AppIntentRefine, ""},
		{"I'm satisfied", "SAVE", AppIntentSave, ""},
		{"I'm satisfied", "DONE", AppIntentSave, ""},  // LLM says DONE → maps to Save
		{"what's the weather?", "UNRELATED", AppIntentUnrelated, ""},
		{"something", "refine\n", AppIntentRefine, ""},              // lowercase + trailing newline
		{"something", "UNKNOWN_RESPONSE", AppIntentRefine, ""},      // unknown → fallback to refine
		{"save as Weather", "SAVE:Weather", AppIntentSave, "Weather"},
		{"save it as My Dashboard", "SAVE:My Dashboard", AppIntentSave, "My Dashboard"},
		{"call it Sales Tracker", "SAVE:Sales Tracker", AppIntentSave, "Sales Tracker"},
	}

	for _, tt := range tests {
		llmFunc := func(_ context.Context, _ string) (string, error) {
			return tt.llmReply, nil
		}
		result := ca.ClassifyAppIntent(context.Background(), tt.msg, llmFunc)
		if result.Intent != tt.expect {
			t.Errorf("ClassifyAppIntent(%q, llm=%q).Intent = %d, want %d", tt.msg, tt.llmReply, result.Intent, tt.expect)
		}
		if result.SaveName != tt.saveName {
			t.Errorf("ClassifyAppIntent(%q, llm=%q).SaveName = %q, want %q", tt.msg, tt.llmReply, result.SaveName, tt.saveName)
		}
	}
}

func TestClassifyAppIntent_LLMError(t *testing.T) {
	ca := &ChatAgent{
		activeApps: make(map[string]*ActiveApp),
	}

	// When LLM returns an error, should fall back to AppIntentRefine
	llmFunc := func(_ context.Context, _ string) (string, error) {
		return "", context.DeadlineExceeded
	}
	result := ca.ClassifyAppIntent(context.Background(), "save it", llmFunc)
	if result.Intent != AppIntentRefine {
		t.Errorf("expected AppIntentRefine on LLM error, got %d", result.Intent)
	}
	if result.SaveName != "" {
		t.Errorf("expected empty SaveName on LLM error, got %q", result.SaveName)
	}
}

func TestBuildAppRefinementContext(t *testing.T) {
	app := &ActiveApp{
		AppID:         "uuid-abc",
		Title:         "Sales Dashboard",
		Code:          "function SalesDashboard() { return <div>v2</div> }",
		Version:       2,
		Modifications: []string{"add a chart", "make header blue"},
	}

	ctx := BuildAppRefinementContext(app)

	// Should contain the current source code
	if !strings.Contains(ctx, "function SalesDashboard()") {
		t.Error("context should contain current source code")
	}

	// Should contain the title
	if !strings.Contains(ctx, "Sales Dashboard") {
		t.Error("context should contain the title")
	}

	// Should contain version number
	if !strings.Contains(ctx, "version 2") {
		t.Error("context should contain version number")
	}

	// Should contain modification history
	if !strings.Contains(ctx, "add a chart") {
		t.Error("context should contain modification history")
	}
	if !strings.Contains(ctx, "make header blue") {
		t.Error("context should contain modification history")
	}

	// Should instruct to output full component in astonish-app fence
	if !strings.Contains(ctx, "```astonish-app") {
		t.Error("context should instruct to use astonish-app fence")
	}
}

func TestBuildAppRefinementContext_NoModifications(t *testing.T) {
	app := &ActiveApp{
		AppID:   "uuid-xyz",
		Title:   "Counter",
		Code:    "function Counter() { return <div>0</div> }",
		Version: 1,
	}

	ctx := BuildAppRefinementContext(app)

	// Should contain code but no modifications section
	if !strings.Contains(ctx, "function Counter()") {
		t.Error("context should contain source code")
	}
	if strings.Contains(ctx, "Previous Modifications") {
		t.Error("context should not contain modifications section when empty")
	}
}
