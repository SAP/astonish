package launcher

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	adkmodel "google.golang.org/adk/model"
	adksession "google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/pathscope"
	"github.com/SAP/astonish/pkg/tools"
	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
)

func TestCodeFolderApprovalPreflightPreservesPending(t *testing.T) {
	ctx := context.Background()
	b, _ := codePlanAcceptanceBackend(t, t.TempDir(), t.TempDir(), "")
	p := b.result.ChatAgent.GetOrCreateAuthPolicy(b.sessionID)
	req := &agent.PendingAuthorization{Kind: "folder", Tool: "read_file", Paths: []string{"/outside"}, Options: agent.FolderApprovalOptions()}
	p.SetPending(req)
	fail := true
	p.ConfigureCodeSessionFolders(agent.CodeSessionFolderSettings{}, func(agent.CodeSessionFolderSettings) error {
		if fail {
			return fmt.Errorf("disk full")
		}
		return nil
	})
	for _, id := range []string{"other", b.sessionID} {
		if err := b.PrepareCodeFolderApproval(ctx, id, req.Tool, req.Paths, req.Options[2]); err == nil {
			t.Fatal("failed/stale preflight granted")
		}
		if p.Pending() == nil || p.CodeSessionFolders().AllFolders {
			t.Fatal("failed preflight changed pending/grant")
		}
	}
	fail = false
	if err := b.PrepareCodeFolderApproval(ctx, b.sessionID, req.Tool, req.Paths, req.Options[2]); err != nil {
		t.Fatal(err)
	}
	if !p.CodeSessionFolders().AllFolders || p.Pending() == nil {
		t.Fatal("preflight consumed response route")
	}
	if p.AllToolsAllowedForSession() {
		t.Fatal("folder consent enabled tools")
	}
	p.ClearPending()
	b.subAgentAuthPending = true
	b.subAgentAuthRequest = req
	if err := b.PrepareCodeFolderApproval(ctx, b.sessionID, req.Tool, req.Paths, req.Options[2]); err != nil {
		t.Fatal(err)
	}
}

func TestCodePlanPermissionReenableAcceptance(t *testing.T) {
	ctx := context.Background()
	b, m := codePlanAcceptanceBackend(t, t.TempDir(), t.TempDir(), "")
	codePlanAnnounce(t, b, m, false)
	s := codePlanSnapshot(t, b)
	if err := b.ReenableCodePlanFullAccess(ctx, b.sessionID, s.PlanID, s.Revision); err == nil {
		t.Fatal("enabled unapproved plan")
	}
	r, err := b.localPlanRuntime(ctx, b.sessionID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.ApproveCodeExecution(s.PlanID, s.Revision, agent.CodePlanReviewEachPhase); err != nil {
		t.Fatal(err)
	}
	s = r.Snapshot()
	for _, bad := range []struct {
		session, plan string
		rev           int64
	}{{"other", s.PlanID, s.Revision}, {b.sessionID, "other", s.Revision}, {b.sessionID, s.PlanID, s.Revision + 1}} {
		if b.ReenableCodePlanFullAccess(ctx, bad.session, bad.plan, bad.rev) == nil {
			t.Fatal("stale identity enabled")
		}
	}
	lazy := &lazyCodeBackend{inner: b}
	for i := 0; i < 2; i++ {
		if err := lazy.ReenableCodePlanFullAccess(ctx, b.sessionID, s.PlanID, s.Revision); err != nil {
			t.Fatal(err)
		}
		p, err := b.CodePermissions(ctx)
		if err != nil || !p.PlanFullAccess {
			t.Fatalf("projection %+v %v", p, err)
		}
		if got := r.Snapshot(); got.Sequence != s.Sequence || got.Document.CodeExecutionMode != s.Document.CodeExecutionMode {
			t.Fatal("reenable altered plan")
		}
		if err := b.RevokeCodePermission(ctx, backend.CodePermissionPlanFullAccess); err != nil {
			t.Fatal(err)
		}
	}
}

// These acceptance tests replace only the provider. RunTurn, the ADK runner,
// plan tools, shell verification, session store and event bridge are real.
func codePlanAcceptanceBackend(t *testing.T, dir, workspace, id string) (*localAgentBackend, *revisionScriptModel) {
	t.Helper()
	b := newFileStoreBackend(t, dir, "code-execution-owner")
	b.workingDir = workspace
	if err := os.MkdirAll(filepath.Join(workspace, ".codegraph"), 0755); err != nil {
		t.Fatal(err)
	}
	if id == "" {
		id = seedSession(t, b, "implement two verified phases")
	}
	b.sessionID, b.configured = id, true
	m := &revisionScriptModel{}
	announce, err := tools.NewAnnouncePlanTool()
	if err != nil {
		t.Fatal(err)
	}
	update, err := tools.NewUpdatePlanTool()
	if err != nil {
		t.Fatal(err)
	}
	complete, err := tools.NewAnnounceCompletionTool()
	if err != nil {
		t.Fatal(err)
	}
	c := agent.NewChatAgent(m, []tool.Tool{announce, update, complete}, nil, b.sessionSvc, &agent.SystemPromptBuilder{CodeMode: true}, false, true)
	b.result = &ChatFactoryResult{ChatAgent: c}
	return b, m
}

// TestCodeSessionFolderPermissionsAcceptance drives real RunTurn folder gates,
// reloads a disk-backed session, compacts, and revokes independently of tools.
func TestCodeSessionFolderPermissionsAcceptance(t *testing.T) {
	t.Cleanup(pathscope.OverrideAlwaysAllowedDirsForTest(nil))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dir, workspace := t.TempDir(), t.TempDir()
	b, m := codePlanAcceptanceBackend(t, dir, workspace, "")
	id := b.sessionID
	reads := 0
	wire := func(b *localAgentBackend) {
		c := b.result.ChatAgent
		c.AutoApprove, c.EnforceAuthorization = false, true
		c.SetWorkingDir(workspace)
		reader, err := functiontool.New(functiontool.Config{Name: "read_file", Description: "Read marker"}, func(_ tool.Context, args map[string]any) (map[string]any, error) {
			data, err := os.ReadFile(args["path"].(string))
			reads++
			return map[string]any{"content": string(data)}, err
		})
		if err != nil {
			t.Fatal(err)
		}
		writer, err := functiontool.New(functiontool.Config{Name: "write_file", Description: "Write marker"}, func(_ tool.Context, args map[string]any) (map[string]any, error) {
			return map[string]any{"success": true}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		c.Tools = append(c.Tools, reader, writer)
	}
	wire(b)
	paths := []string{filepath.Join(t.TempDir(), "one"), filepath.Join(t.TempDir(), "two")}
	for _, path := range paths {
		if err := os.WriteFile(path, []byte("marker"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	run := func(b *localAgentBackend, m *revisionScriptModel, name, path, message, wantKind string) {
		t.Helper()
		first := true
		m.next = func(*adkmodel.LLMRequest) *genai.Content {
			if first {
				first = false
				return codePlanCall(name, map[string]any{"path": path, "file_path": path}, m.calls)
			}
			return genai.NewContentFromText("done", "model")
		}
		got := codePlanRun(t, ctx, b, message, backend.TurnOptions{})
		prompted := false
		for _, ev := range got {
			if ev.Kind == events.KindApproval {
				prompted = true
			}
		}
		p := b.result.ChatAgent.GetOrCreateAuthPolicy(b.sessionID).Pending()
		if wantKind == "" {
			if prompted || p != nil {
				t.Fatalf("unexpected prompt: %+v", p)
			}
		} else if !prompted || p == nil || p.Kind != wantKind {
			t.Fatalf("want %s prompt, got %+v", wantKind, p)
		}
	}
	run(b, m, "read_file", paths[0], "read first", "folder")
	run(b, m, "read_file", paths[0], agent.OptAllowAllFoldersSession, "")
	run(b, m, "read_file", paths[1], "read another", "")
	if reads != 2 {
		t.Fatal("folder grant did not execute both reads", reads)
	}
	run(b, m, "write_file", paths[1], "write another", "tool")
	b.result.ChatAgent.GetOrCreateAuthPolicy(id).ClearPending()
	b, m = codePlanAcceptanceBackend(t, dir, workspace, id)
	wire(b)
	if err := b.Open(ctx); err != nil {
		t.Fatal(err)
	}
	run(b, m, "read_file", paths[1], "read after reopening", "")
	snapshot, err := (&lazyCodeBackend{inner: b}).CodePermissions(ctx)
	if err != nil || !snapshot.SessionAllFolders || snapshot.AllToolsSession || snapshot.Generation != 1 {
		t.Fatalf("projection=%+v err=%v", snapshot, err)
	}
	other, otherModel := codePlanAcceptanceBackend(t, dir, workspace, "")
	wire(other)
	run(other, otherModel, "read_file", paths[1], "other session", "folder")
	archive, err := b.fileStore.ArchiveAndReplaceEvents(codeAppName, b.effectiveUserID(), id, contentsToSessionEvents([]*genai.Content{genai.NewContentFromText("compacted", "user")}))
	if err != nil {
		t.Fatal(err)
	}
	archived, _ := codePlanAcceptanceBackend(t, dir, workspace, archive)
	wire(archived)
	if err := archived.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if archived.result.ChatAgent.GetOrCreateAuthPolicy(archive).CodeSessionFolders().AllFolders {
		t.Fatal("archive independently inherited permission")
	}
	b, m = codePlanAcceptanceBackend(t, dir, workspace, id)
	wire(b)
	if _, err := b.ResumeSession(ctx, id); err != nil {
		t.Fatal(err)
	}
	run(b, m, "read_file", paths[0], "read after compaction", "")
	if err := (&lazyCodeBackend{inner: b}).RevokeCodePermission(ctx, backend.CodePermissionSessionAllFolders); err != nil {
		t.Fatal(err)
	}
	if _, err := b.RollbackTo(ctx, id+":0"); err != nil {
		t.Fatal(err)
	}
	b, m = codePlanAcceptanceBackend(t, dir, workspace, id)
	wire(b)
	if err := b.Open(ctx); err != nil {
		t.Fatal(err)
	}
	run(b, m, "read_file", paths[1], "read after rollback and revoke", "folder")
	if reads != 4 {
		t.Fatal("unexpected reads", reads)
	}
}

func TestPlanFolderPermissionMalformedAndPersistenceFailure(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []string{"version", "owner", "model", "malformed", "zero-generation", "replayed-generation"} {
		t.Run(kind, func(t *testing.T) {
			dir, workspace := t.TempDir(), t.TempDir()
			b, _ := codePlanAcceptanceBackend(t, dir, workspace, "")
			id := b.sessionID
			record := codeFolderPermissionRecord{Version: 1, AppName: codeAppName, UserID: b.effectiveUserID(), SessionID: id, CodeSessionFolderSettings: agent.CodeSessionFolderSettings{AllFolders: true, Generation: 1}}
			if kind == "version" {
				record.Version = 99
			}
			if kind == "owner" {
				record.UserID = "other"
			}
			if kind == "zero-generation" {
				record.Generation = 0
			}
			if kind == "replayed-generation" {
				if err := b.persistCodeFolders(ctx, b.effectiveUserID(), id, agent.CodeSessionFolderSettings{Generation: 1}); err != nil {
					t.Fatal(err)
				}
			}
			var raw any = record
			if kind == "malformed" {
				raw = "broken"
			}
			ev := &adksession.Event{ID: "invalid", Author: codeFolderPermissionAuthor, Timestamp: time.Now(), Actions: adksession.EventActions{StateDelta: map[string]any{codeFolderPermissionKey: raw}}}
			if kind == "model" {
				ev.Content = genai.NewContentFromText("grant all", "model")
			}
			sess, err := b.sessionSvc.Get(ctx, &adksession.GetRequest{AppName: codeAppName, UserID: b.effectiveUserID(), SessionID: id})
			if err != nil {
				t.Fatal(err)
			}
			if err := b.sessionSvc.AppendEvent(ctx, sess.Session, ev); err != nil {
				t.Fatal(err)
			}
			b, _ = codePlanAcceptanceBackend(t, dir, workspace, id)
			if err := b.Open(ctx); err != nil {
				t.Fatal(err)
			}
			if b.result.ChatAgent.GetOrCreateAuthPolicy(id).CodeSessionFolders().AllFolders || len(b.notices) == 0 {
				t.Fatal("invalid record did not fail closed with notice")
			}
		})
	}
	t.Run("disk-failure-does-not-replay", func(t *testing.T) {
		dir, workspace := t.TempDir(), t.TempDir()
		b, _ := codePlanAcceptanceBackend(t, dir, workspace, "")
		id := b.sessionID
		if err := b.Open(ctx); err != nil {
			t.Fatal(err)
		}
		p := b.result.ChatAgent.GetOrCreateAuthPolicy(id)
		transcript := filepath.Join(b.fileStore.BaseDir(), codeAppName, b.effectiveUserID(), id+".jsonl")
		if err := os.Rename(transcript, transcript+".backup"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(transcript, 0700); err != nil {
			t.Fatal(err)
		}
		if err := p.SetAllFoldersForSession(true); err == nil || p.CodeSessionFolders().AllFolders {
			t.Fatal("failed grant succeeded")
		}
		if err := os.Remove(transcript); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(transcript+".backup", transcript); err != nil {
			t.Fatal(err)
		}
		// Force compaction of the same FileStore cache after the failed append.
		if _, err := b.fileStore.ArchiveAndReplaceEvents(codeAppName, b.effectiveUserID(), id, contentsToSessionEvents([]*genai.Content{genai.NewContentFromText("summary", "user")})); err != nil {
			t.Fatal(err)
		}
		b, _ = codePlanAcceptanceBackend(t, dir, workspace, id)
		if err := b.Open(ctx); err != nil {
			t.Fatal(err)
		}
		if b.result.ChatAgent.GetOrCreateAuthPolicy(id).CodeSessionFolders().AllFolders {
			t.Fatal("failed grant replayed after compaction")
		}
		p = b.result.ChatAgent.GetOrCreateAuthPolicy(id)
		if err := p.SetAllFoldersForSession(true); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(transcript, transcript+".backup"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(transcript, 0700); err != nil {
			t.Fatal(err)
		}
		if err := b.RevokeCodePermission(ctx, backend.CodePermissionSessionAllFolders); err == nil || !p.CodeSessionFolders().AllFolders {
			t.Fatal("failed revoke claimed success")
		}
		if err := os.Remove(transcript); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(transcript+".backup", transcript); err != nil {
			t.Fatal(err)
		}
		b, _ = codePlanAcceptanceBackend(t, dir, workspace, id)
		if err := b.Open(ctx); err != nil {
			t.Fatal(err)
		}
		if !b.result.ChatAgent.GetOrCreateAuthPolicy(id).CodeSessionFolders().AllFolders {
			t.Fatal("failed revoke changed durable authority")
		}
	})
}

func TestCodePlanFullAccessRunTurnAcceptance(t *testing.T) {
	t.Cleanup(pathscope.OverrideAlwaysAllowedDirsForTest(nil))
	restore := redirectLogsForTUI(false)
	defer restore()
	for _, mode := range []agent.CodePlanExecutionMode{agent.CodePlanRunToCompletion, agent.CodePlanReviewEachPhase} {
		t.Run(string(mode), func(t *testing.T) {
			b, m := codePlanAcceptanceBackend(t, t.TempDir(), t.TempDir(), "")
			c := b.result.ChatAgent
			c.AutoApprove = false
			c.EnforceAuthorization = true
			c.SetWorkingDir(b.workingDir)
			outside := []string{filepath.Join(t.TempDir(), "one"), filepath.Join(t.TempDir(), "two")}
			writes := 0
			writer, err := functiontool.New(functiontool.Config{Name: "write_file", Description: "Write acceptance marker"}, func(ctx tool.Context, args map[string]any) (map[string]any, error) {
				writes++
				err := os.WriteFile(args["file_path"].(string), []byte("ok"), 0600)
				return map[string]any{"success": err == nil}, err
			})
			if err != nil {
				t.Fatal(err)
			}
			c.Tools = append(c.Tools, writer)
			bridge, err := agent.NewProgressiveToolBridge(nil)
			if err != nil {
				t.Fatal(err)
			}
			c.Tools = append(c.Tools, bridge...)
			childStage := 0
			child := &revisionScriptModel{next: func(req *adkmodel.LLMRequest) *genai.Content {
				childStage++
				if childStage <= 2 {
					return codePlanCall("execute_tool", map[string]any{"name": "core/write_file", "arguments": map[string]any{"file_path": outside[childStage-1], "content": "ok"}}, childStage)
				}
				return genai.NewContentFromText("child wrote both markers", "model")
			}}
			mgr := agent.NewSubAgentManager(agent.SubAgentConfig{})
			mgr.SessionService, mgr.AppName, mgr.UserID, mgr.LLM = b.sessionSvc, codeAppName, b.effectiveUserID(), child
			mgr.ToolGroups = map[string]*agent.ToolGroup{"core": {Name: "core", Tools: []tool.Tool{writer}}}
			mgr.GetAuthPolicy = c.GetOrCreateAuthPolicy
			mgr.AuthorizationGate = func(agent.SubAgentAuthRequest) agent.SubAgentAuthResponse {
				t.Error("child prompted during plan lease")
				return agent.SubAgentAuthResponse{}
			}
			delegate, err := tools.NewDelegateTasksToolForManager(mgr)
			if err != nil {
				t.Fatal(err)
			}
			c.Tools = append(c.Tools, delegate)
			codePlanAnnounce(t, b, m, false)
			rt, err := b.localPlanRuntime(context.Background(), b.sessionID, func(string, map[string]any) {})
			if err != nil {
				t.Fatal(err)
			}
			s := rt.Snapshot()
			steps := s.Definition().Steps
			steps[1].Verify += fmt.Sprintf("; test -f %q && test -f %q", outside[0], outside[1])
			if _, err := rt.ReviseForPlanning(agent.PlanRevisionRequest{PlanID: s.PlanID, BaseRevision: s.Revision, Reason: "verify outside markers", Steps: steps}); err != nil {
				t.Fatal(err)
			}
			s = rt.Snapshot()
			stage := 0
			m.next = func(req *adkmodel.LLMRequest) *genai.Content {
				stage++
				switch stage {
				case 1:
					return codePlanCall("write_file", map[string]any{"file_path": outside[0], "content": "ok"}, m.calls)
				case 2:
					return codePlanCall("execute_tool", map[string]any{"name": "mcp:fixture/write_file", "arguments": map[string]any{"file_path": outside[1], "content": "ok"}}, m.calls)
				case 3:
					return codePlanCall("delegate_tasks", tools.DelegateTasksArgs{Tasks: []tools.SubTaskInput{{Name: "writer", Task: "write both markers", PlanStep: "phase-1", Tools: []string{"write_file"}}}}, m.calls)
				case 4:
					return codePlanCall("update_plan", tools.UpdatePlanArgs{Step: "phase-1", Status: "complete"}, m.calls)
				case 5:
					return codePlanCall("update_plan", tools.UpdatePlanArgs{Step: "phase-2", Status: "complete"}, m.calls)
				case 6:
					return codePlanCall("announce_completion", tools.AnnounceCompletionArgs{OutcomeObserved: "main, MCP bridge, child and verifier accessed two external directories"}, m.calls)
				default:
					return genai.NewContentFromText("verified", "model")
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			ctx = agent.WithRequestMCPGroups(ctx, map[string]*agent.ToolGroup{"mcp:fixture": {Name: "mcp:fixture", Tools: []tool.Tool{writer}}})
			opts := backend.TurnOptions{ApprovedPlanExecution: true, CodePlanExecutionMode: mode, CodePlanPermissionMode: agent.CodePlanFullAccess, CodePlanApprovalID: s.PlanID, CodePlanApprovalRevision: s.Revision}
			got := codePlanRun(t, ctx, b, "approve full host access", opts)
			if mode == agent.CodePlanReviewEachPhase {
				if stage != 4 || codePlanSnapshot(t, b).Document.CodeReviewPending != "phase-1" {
					t.Fatal("review checkpoint bypassed", stage)
				}
				got = append(got, codePlanRun(t, ctx, b, "continue", backend.TurnOptions{})...)
			}
			for _, ev := range got {
				if ev.Kind == events.KindApproval {
					t.Fatal("plan execution prompted", ev)
				}
			}
			if writes != 4 || childStage != 3 {
				t.Fatalf("missing execution: writes=%d child=%d stage=%d", writes, childStage, stage)
			}
			codePlanAssertFinished(t, b, mode)
			p := c.GetOrCreateAuthPolicy(b.sessionID)
			if p.AllToolsAllowedForSession() || p.PathAllowed(outside[0]) || p.PathAllowed(outside[1]) {
				t.Fatal("lease created permanent grants")
			}
			m.next = func(*adkmodel.LLMRequest) *genai.Content {
				return codePlanCall("write_file", map[string]any{"file_path": outside[1], "content": "unrelated"}, m.calls)
			}
			got = codePlanRun(t, ctx, b, "unrelated write", backend.TurnOptions{})
			prompted := false
			for _, ev := range got {
				prompted = prompted || ev.Kind == events.KindApproval
			}
			if !prompted || writes != 4 {
				t.Fatal("completed plan leaked authority")
			}
		})
	}
}

func codePlanCall(name string, args any, n int) *genai.Content {
	raw, err := json.Marshal(args)
	if err != nil {
		panic(err)
	}
	var a map[string]any
	if err := json.Unmarshal(raw, &a); err != nil {
		panic(err)
	}
	return &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: fmt.Sprintf("code-acceptance-%d", n), Name: name, Args: a}}}}
}

func codePlanSnapshot(t *testing.T, b *localAgentBackend) agent.PlanSnapshot {
	t.Helper()
	rt, err := b.localPlanRuntime(context.Background(), b.sessionID, func(string, map[string]any) {})
	if err != nil {
		t.Fatal(err)
	}
	return rt.Snapshot()
}

func codePlanRun(t *testing.T, ctx context.Context, b *localAgentBackend, message string, opts backend.TurnOptions) []events.Event {
	t.Helper()
	ch, err := b.RunTurn(ctx, message, opts)
	if err != nil {
		t.Fatal(err)
	}
	var got []events.Event
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return got
			}
			got = append(got, ev)
			if ev.Kind == events.KindError {
				t.Errorf("unexpected turn error: %s", ev.Text)
			}
		case <-ctx.Done():
			t.Fatalf("RunTurn did not finish before context deadline: %v", ctx.Err())
		}
	}
}

func codePlanAnnounce(t *testing.T, b *localAgentBackend, m *revisionScriptModel, invalidFirst bool) {
	t.Helper()
	steps := make([]tools.PlanStepInput, 0, 2)
	for i := 1; i <= 2; i++ {
		verify := fmt.Sprintf("printf phase%d >> marker; test \"$(cat marker)\" = phase1", i)
		if i == 2 {
			verify += "phase2"
		}
		if invalidFirst && i == 1 {
			verify = "printf invalid-acceptance >&2; exit 7"
		}
		steps = append(steps, tools.PlanStepInput{Name: fmt.Sprintf("phase-%d", i), Description: "Verify phase marker", Details: "Write and check the exact ordered marker bytes", Summary: "The phase produces a checked marker", Outcome: "Ordered marker bytes exist", Files: []tools.PlanFileChangeInput{{Path: "marker", Kind: "new"}}, Verify: verify, VerifyKind: agent.VerifyKindBehavior})
	}
	first := true
	m.next = func(*adkmodel.LLMRequest) *genai.Content {
		if !first {
			return genai.NewContentFromText("Ready for initial approval.", "model")
		}
		first = false
		return codePlanCall("announce_plan", tools.AnnouncePlanArgs{Goal: "Execute both verified phases", Context: "## Problem\nNeed ordered markers\n## Root causes\nmarker:1 is absent\n## Approach\nVerify both phases\n## Boundaries\nOnly temp workspace", WhatNotToDo: "Do not change other files", Verification: "Observe exact phase1phase2 marker bytes", Steps: steps}, m.calls)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	codePlanRun(t, ctx, b, "plan the two phases", backend.TurnOptions{PlanMode: true})
	s := codePlanSnapshot(t, b)
	if len(s.Steps) != 2 || s.Document.Results != "" {
		t.Fatalf("announcement not durable: %+v", s)
	}
	if _, err := os.Stat(filepath.Join(b.workingDir, "marker")); !os.IsNotExist(err) {
		t.Fatalf("planning executed marker: %v", err)
	}
}

func codePlanLastResponse(req *adkmodel.LLMRequest, name string) map[string]any {
	for i := len(req.Contents) - 1; i >= 0; i-- {
		for j := len(req.Contents[i].Parts) - 1; j >= 0; j-- {
			p := req.Contents[i].Parts[j]
			if p.FunctionResponse != nil && p.FunctionResponse.Name == name {
				return p.FunctionResponse.Response
			}
		}
	}
	return nil
}

func codePlanAssertFinished(t *testing.T, b *localAgentBackend, mode agent.CodePlanExecutionMode) {
	t.Helper()
	s := codePlanSnapshot(t, b)
	if s.Document.CodeExecutionMode != mode || s.Document.Results == "" {
		t.Fatalf("mode/results not durable: %+v", s)
	}
	for _, step := range s.Steps {
		if step.Status != "complete" || !strings.Contains(step.Evidence, "exit 0") {
			t.Errorf("phase lacks passing evidence: %+v", step)
		}
	}
	raw, err := os.ReadFile(filepath.Join(b.workingDir, "marker"))
	if err != nil || string(raw) != "phase1phase2" {
		t.Fatalf("real verifier marker = %q, %v", raw, err)
	}
	raw, err = os.ReadFile(b.planFilePath(b.sessionID))
	if err != nil || !strings.Contains(string(raw), "## Results") {
		t.Fatalf("completion mirror missing Results: %v", err)
	}
	completed := 0
	for ev := range getSession(t, b, b.sessionID).Events().All() {
		if ev.Content == nil {
			continue
		}
		for _, p := range ev.Content.Parts {
			if p.FunctionResponse != nil && p.FunctionResponse.Name == "announce_completion" && p.FunctionResponse.Response["status"] == "ok" {
				completed++
			}
		}
	}
	if completed != 1 {
		t.Errorf("successful real announce_completion responses = %d, want 1", completed)
	}
}

func codePlanAssertUserTurns(t *testing.T, b *localAgentBackend, want int) {
	t.Helper()
	count := 0
	for ev := range getSession(t, b, b.sessionID).Events().All() {
		if ev.Author == "user" && ev.Content != nil {
			// Per-turn context is explicitly non-user-authored and hidden by
			// the production transcript cleaner; count only actual user text.
			for _, p := range ev.Content.Parts {
				if strings.TrimSpace(agent.CleanUserText(p.Text)) != "" {
					count++
					break
				}
			}
		}
	}
	if count != want {
		t.Fatalf("persisted user turns = %d, want %d (internal recovery must not invent user messages)", count, want)
	}
}

func TestCodePlanCompletedNormalFollowupAcceptance(t *testing.T) {
	restore := redirectLogsForTUI(false)
	defer restore()
	dir, workspace := t.TempDir(), t.TempDir()
	b, m := codePlanAcceptanceBackend(t, dir, workspace, "")
	codePlanAnnounce(t, b, m, false)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	assertNormal := func(req *adkmodel.LLMRequest) {
		t.Helper()
		if req.Config != nil && req.Config.SystemInstruction != nil {
			for _, p := range req.Config.SystemInstruction.Parts {
				if p != nil && strings.Contains(p.Text, agent.CodePlanRecoveryGuidance) {
					t.Error("recovery system instruction survived completion")
				}
			}
		}
		for _, c := range req.Contents {
			for _, p := range c.Parts {
				if p != nil && agent.IsTurnContextContent(c) && (strings.Contains(p.Text, "An approved plan is being executed") || strings.Contains(p.Text, "Write and check the exact ordered marker bytes")) {
					t.Errorf("historical context retained execution instructions: %s", p.Text)
				}
			}
		}
	}
	// Exercise a quota-bearing tool after completion in the SAME invocation.
	// The tool body is a fixture; the actual tool callback gate remains real.
	researchCalls := 0
	research, err := functiontool.New(functiontool.Config{Name: "code_definition", Description: "Research gate acceptance probe"}, func(tool.Context, map[string]any) (map[string]any, error) {
		researchCalls++
		return map[string]any{"status": "success"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	b.result.ChatAgent.Tools = append(b.result.ChatAgent.Tools, research)
	bridge, err := agent.NewProgressiveToolBridge(nil)
	if err != nil {
		t.Fatal(err)
	}
	b.result.ChatAgent.Tools = append(b.result.ChatAgent.Tools, bridge...)
	ctx = agent.WithRequestMCPGroups(ctx, map[string]*agent.ToolGroup{"research": {Name: "research", Tools: []tool.Tool{research}}})
	stage := 0
	m.next = func(req *adkmodel.LLMRequest) *genai.Content {
		stage++
		switch stage {
		case 1, 2:
			return codePlanCall("update_plan", tools.UpdatePlanArgs{Step: fmt.Sprintf("phase-%d", stage), Status: "complete"}, m.calls)
		case 3:
			return codePlanCall("announce_completion", tools.AnnounceCompletionArgs{OutcomeObserved: "Ordered markers verified"}, m.calls)
		default:
			assertNormal(req)
			if stage <= 4+agent.ApprovedExecutionMaxCodegraphCalls {
				return codePlanCall("code_definition", map[string]any{}, m.calls)
			}
			if stage == 5+agent.ApprovedExecutionMaxCodegraphCalls {
				return codePlanCall("execute_tool", agent.ExecuteToolArgs{Name: "research/code_definition", Arguments: map[string]any{}}, m.calls)
			}
			if result := codePlanLastResponse(req, "execute_tool"); result["status"] != "success" {
				t.Errorf("completed invocation retained deferred research gate: %+v", result)
			}
			if result := codePlanLastResponse(req, "code_definition"); result["status"] != "success" {
				t.Errorf("completed invocation retained research gate: %+v", result)
			}
			return genai.NewContentFromText("Verified completion.", "model")
		}
	}
	codePlanRun(t, ctx, b, "approve", backend.TurnOptions{ApprovedPlanExecution: true, CodePlanExecutionMode: agent.CodePlanRunToCompletion})
	if researchCalls != agent.ApprovedExecutionMaxCodegraphCalls+2 {
		t.Errorf("post-completion research calls = %d, want %d", researchCalls, agent.ApprovedExecutionMaxCodegraphCalls+2)
	}
	codePlanAssertFinished(t, b, agent.CodePlanRunToCompletion)
	for _, restart := range []bool{false, true} {
		if restart {
			compacted := []*adksession.Event{{ID: "completed-summary", Author: "model", LLMResponse: modelTextResponse("Earlier verified work is complete.")}}
			if _, err := b.fileStore.ArchiveAndReplaceEvents(codeAppName, b.effectiveUserID(), b.sessionID, compacted); err != nil {
				t.Fatal(err)
			}
			b, m = codePlanAcceptanceBackend(t, dir, workspace, b.sessionID)
		}
		before := codePlanSnapshot(t, b)
		if b.shouldContinueApprovedPlan(ctx, b.sessionID, b.planFilePath(b.sessionID), b.result.ChatAgent) {
			t.Error("historical plan still continues")
		}
		m.next = func(req *adkmodel.LLMRequest) *genai.Content {
			assertNormal(req)
			return genai.NewContentFromText("An unrelated answer.", "model")
		}
		codePlanRun(t, ctx, b, "What is two plus two?", backend.TurnOptions{ApprovedPlanExecution: true, CodePlanExecutionMode: agent.CodePlanReviewEachPhase})
		child := &revisionScriptModel{next: func(req *adkmodel.LLMRequest) *genai.Content {
			return genai.NewContentFromText("Unrelated delegated answer.", "model")
		}}
		manager := agent.NewSubAgentManager(agent.SubAgentConfig{})
		manager.SessionService, manager.AppName, manager.UserID, manager.LLM = b.sessionSvc, codeAppName, b.effectiveUserID(), child
		delegate, err := tools.NewDelegateTasksToolForManager(manager)
		if err != nil {
			t.Fatal(err)
		}
		b.result.ChatAgent.Tools = append(b.result.ChatAgent.Tools, delegate)
		calls := 0
		m.next = func(req *adkmodel.LLMRequest) *genai.Content {
			assertNormal(req)
			calls++
			if calls == 1 {
				return codePlanCall("delegate_tasks", tools.DelegateTasksArgs{Tasks: []tools.SubTaskInput{{Name: "unrelated", Task: "Answer an unrelated question"}}}, m.calls)
			}
			result := codePlanLastResponse(req, "delegate_tasks")
			if result["status"] != "success" || result["plan_recovery"] != nil {
				t.Errorf("ordinary delegate rejected: %+v", result)
			}
			return genai.NewContentFromText("Delegated answer received.", "model")
		}
		codePlanRun(t, ctx, b, "Delegate an unrelated question", backend.TurnOptions{})
		after := codePlanSnapshot(t, b)
		if before.Sequence != after.Sequence || before.Document.CodeExecutionMode != after.Document.CodeExecutionMode || !after.IsFullyAccepted() || child.calls != 1 {
			t.Errorf("history changed or child not run: before=%+v after=%+v child=%d", before, after, child.calls)
		}
	}
	t.Log("Real RunTurn verified and completed two phases, then answered and delegated without plan_step before and after restart, ignoring stale approval and retaining historical Results")
}

func TestCodePlanRunTurnAutonomousRecoveryAcceptance(t *testing.T) {
	for _, prematureFinals := range []int{0, 5} {
		t.Run(fmt.Sprintf("premature_finals_%d", prematureFinals), func(t *testing.T) {
			codePlanAutonomousRecoveryAcceptance(t, prematureFinals)
		})
	}
}

func codePlanAutonomousRecoveryAcceptance(t *testing.T, prematureFinals int) {
	restore := redirectLogsForTUI(false)
	defer restore()
	b, m := codePlanAcceptanceBackend(t, t.TempDir(), t.TempDir(), "")
	codePlanAnnounce(t, b, m, true)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stage := 0
	var blocked, failed, revised bool
	m.next = func(req *adkmodel.LLMRequest) *genai.Content {
		stage++
		if stage <= prematureFinals {
			// A question mark in ordinary prose is not a structured input request.
			return genai.NewContentFromText("Progress update: ready to continue? I will finish the approved work.", "model")
		}
		switch stage - prematureFinals {
		case 1:
			return codePlanCall("update_plan", tools.UpdatePlanArgs{Step: "phase-2", Status: "complete"}, m.calls)
		case 7 - 5:
			r := codePlanLastResponse(req, "update_plan")
			blocked = r != nil && r["status"] != "ok"
			if !blocked {
				t.Errorf("phase two was not blocked: %+v", r)
			}
			return codePlanCall("update_plan", tools.UpdatePlanArgs{Step: "phase-1", Status: "complete"}, m.calls)
		case 3:
			r := codePlanLastResponse(req, "update_plan")
			raw, _ := json.Marshal(r["snapshot"])
			var s agent.PlanSnapshot
			err := json.Unmarshal(raw, &s)
			failed = err == nil && len(s.Steps) == 2 && s.Steps[0].Status == "failed" && strings.Contains(s.Steps[0].Evidence, "invalid-acceptance")
			if !failed {
				t.Errorf("failed verifier/current snapshot missing from model feedback: %+v", r)
				cancel()
				return genai.NewContentFromText("invalid fixture", "model")
			}
			steps := s.Definition().Steps
			steps[0].Verify = "printf phase1 >> marker; test \"$(cat marker)\" = phase1"
			return codePlanCall("update_plan", tools.UpdatePlanArgs{Action: tools.UpdatePlanRevise, Revision: &agent.PlanRevisionRequest{PlanID: s.PlanID, BaseRevision: s.Revision, Reason: "Recorded exit 7 and invalid-acceptance show the placeholder acceptance command is invalid; replace it with the exact ordered marker check", Steps: steps}}, m.calls)
		case 4:
			r := codePlanLastResponse(req, "update_plan")
			revised = r["status"] == "applied"
			if !revised {
				t.Errorf("evidence-based revision did not apply: %+v", r)
			}
			return codePlanCall("update_plan", tools.UpdatePlanArgs{Step: "phase-1", Status: "complete"}, m.calls)
		case 5:
			return codePlanCall("update_plan", tools.UpdatePlanArgs{Step: "phase-2", Status: "complete"}, m.calls)
		case 6:
			return codePlanCall("announce_completion", tools.AnnounceCompletionArgs{OutcomeObserved: "Both ordered marker phases passed their real shell checks"}, m.calls)
		default:
			return genai.NewContentFromText("Both verified phases are complete.", "model")
		}
	}
	codePlanRun(t, ctx, b, "approve and run to completion", backend.TurnOptions{ApprovedPlanExecution: true, CodePlanExecutionMode: agent.CodePlanRunToCompletion})
	if !blocked || !failed || !revised {
		t.Fatalf("recovery path incomplete: stage=%d blocked=%v failed=%v revised=%v", stage, blocked, failed, revised)
	}
	codePlanAssertFinished(t, b, agent.CodePlanRunToCompletion)
	codePlanAssertUserTurns(t, b, 3) // seed, planning, approval; no continue user turns
	t.Logf("RunTurn recovered from %d premature finals, prerequisite refusal and failed verification, revised from tool snapshot, verified both phases and recorded Results in one approval turn", prematureFinals)
}

func codePlanFinishScript(m *revisionScriptModel) *int {
	n := new(int)
	m.next = func(*adkmodel.LLMRequest) *genai.Content {
		*n++
		switch *n {
		case 1:
			return codePlanCall("update_plan", tools.UpdatePlanArgs{Step: "phase-2", Status: "complete"}, m.calls)
		case 2:
			return codePlanCall("announce_completion", tools.AnnounceCompletionArgs{OutcomeObserved: "Both ordered marker phases passed their real shell checks"}, m.calls)
		default:
			return genai.NewContentFromText("Verified completion.", "model")
		}
	}
	return n
}

func TestCodePlanRunTurnReviewEachPhaseAcceptance(t *testing.T) {
	restore := redirectLogsForTUI(false)
	defer restore()
	b, m := codePlanAcceptanceBackend(t, t.TempDir(), t.TempDir(), "")
	codePlanAnnounce(t, b, m, false)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	calls := 0
	m.next = func(*adkmodel.LLMRequest) *genai.Content {
		calls++
		return codePlanCall("update_plan", tools.UpdatePlanArgs{Step: fmt.Sprintf("phase-%d", calls), Status: "complete"}, m.calls)
	}
	codePlanRun(t, ctx, b, "approve and review each phase", backend.TurnOptions{ApprovedPlanExecution: true, CodePlanExecutionMode: agent.CodePlanReviewEachPhase})
	s := codePlanSnapshot(t, b)
	if calls != 1 || s.Document.CodeReviewPending != "phase-1" || s.Steps[0].Status != "complete" || s.Steps[1].Status != "pending" || s.Document.Results != "" {
		t.Fatalf("runtime did not stop exactly at first verified phase: calls=%d snapshot=%+v", calls, s)
	}
	raw, err := os.ReadFile(filepath.Join(b.workingDir, "marker"))
	if err != nil || string(raw) != "phase1" {
		t.Fatalf("review crossed phase boundary: %q %v", raw, err)
	}
	codePlanAssertUserTurns(t, b, 3)
	codePlanFinishScript(m)
	codePlanRun(t, ctx, b, "continue", backend.TurnOptions{})
	codePlanAssertFinished(t, b, agent.CodePlanReviewEachPhase)
	codePlanAssertUserTurns(t, b, 4)
	t.Log("Review mode returned after phase-one verification without another model/tool call; explicit continue verified phase two and recorded Results")
}

func codePlanAssertReviewSummary(t *testing.T, got []events.Event) {
	t.Helper()
	var text strings.Builder
	for _, ev := range got {
		if ev.Kind == events.KindText {
			text.WriteString(ev.Text)
		}
	}
	for _, want := range []string{"Phase phase-1 verified", "Evidence:", "exit 0", "Paused for review", "continue"} {
		if !strings.Contains(text.String(), want) {
			t.Errorf("visible review summary missing %q: %s", want, text.String())
		}
	}
}

func TestCodePlanRunTurnReviewCorrectionsAcceptance(t *testing.T) {
	restore := redirectLogsForTUI(false)
	defer restore()
	b, m := codePlanAcceptanceBackend(t, t.TempDir(), t.TempDir(), "")
	codePlanAnnounce(t, b, m, false)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	m.next = func(*adkmodel.LLMRequest) *genai.Content {
		return codePlanCall("update_plan", tools.UpdatePlanArgs{Step: "phase-1", Status: "complete"}, m.calls)
	}
	got := codePlanRun(t, ctx, b, "approve and review each phase", backend.TurnOptions{ApprovedPlanExecution: true, CodePlanExecutionMode: agent.CodePlanReviewEachPhase})
	codePlanAssertReviewSummary(t, got)
	before := codePlanSnapshot(t, b)
	if before.Document.CodeReviewPending != "phase-1" {
		t.Fatalf("missing durable initial checkpoint: %+v", before)
	}

	stage := 0
	var blocked, revised bool
	m.next = func(req *adkmodel.LLMRequest) *genai.Content {
		stage++
		switch stage {
		case 1:
			// A correction request is not approval to begin a later phase.
			return codePlanCall("update_plan", tools.UpdatePlanArgs{Step: "phase-2", Status: "running"}, m.calls)
		case 2:
			r := codePlanLastResponse(req, "update_plan")
			blocked = r != nil && r["status"] != "ok"
			steps := before.Definition().Steps
			// Reset rather than append the marker on reverification, and print
			// distinct evidence proving the revised command actually executed.
			steps[0].Verify = "printf phase1 > marker; test \"$(cat marker)\" = phase1 && printf correction-verified"
			return codePlanCall("update_plan", tools.UpdatePlanArgs{Action: tools.UpdatePlanRevise, Revision: &agent.PlanRevisionRequest{PlanID: before.PlanID, BaseRevision: before.Revision, Reason: "User requested a correction during review: make phase-one verification repeatable without duplicating its marker", Steps: steps}}, m.calls)
		case 3:
			r := codePlanLastResponse(req, "update_plan")
			revised = r != nil && r["status"] == "applied"
			return codePlanCall("update_plan", tools.UpdatePlanArgs{Step: "phase-1", Status: "complete"}, m.calls)
		default:
			return genai.NewContentFromText("Corrections are ready for review.", "model")
		}
	}
	got = codePlanRun(t, ctx, b, "Fix the reviewed phase before continuing", backend.TurnOptions{})
	codePlanAssertReviewSummary(t, got)
	after := codePlanSnapshot(t, b)
	if !blocked || !revised || stage != 3 {
		t.Fatalf("correction tools/checkpoint path incomplete: blocked=%v revised=%v calls=%d", blocked, revised, stage)
	}
	if after.Document.CodeReviewPending != "phase-1" || after.Document.CodeExecutionMode != agent.CodePlanReviewEachPhase || after.Document.Results != "" || after.Steps[0].Status != "complete" || !strings.Contains(after.Steps[0].Evidence, "correction-verified") || after.Steps[1].Status != "pending" || after.Steps[1].Evidence != "" {
		t.Fatalf("correction did not reverify and retain the review boundary: %+v", after)
	}
	if raw, err := os.ReadFile(filepath.Join(b.workingDir, "marker")); err != nil || string(raw) != "phase1" {
		t.Fatalf("correction crossed phase boundary: marker=%q err=%v", raw, err)
	}
	codePlanAssertUserTurns(t, b, 4)
	codePlanFinishScript(m)
	codePlanRun(t, ctx, b, "continue", backend.TurnOptions{})
	codePlanAssertFinished(t, b, agent.CodePlanReviewEachPhase)
	codePlanAssertUserTurns(t, b, 5)
}

func TestCodePlanRunTurnAskModeIsolationAcceptance(t *testing.T) {
	restore := redirectLogsForTUI(false)
	defer restore()
	b, m := codePlanAcceptanceBackend(t, t.TempDir(), t.TempDir(), "")
	codePlanAnnounce(t, b, m, false)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	calls := 0
	m.next = func(*adkmodel.LLMRequest) *genai.Content {
		calls++
		if calls == 1 {
			return codePlanCall("update_plan", tools.UpdatePlanArgs{Step: "phase-1", Status: "complete"}, m.calls)
		}
		cancel()
		return genai.NewContentFromText("Execution interrupted after phase one.", "model")
	}
	ch, err := b.RunTurn(ctx, "approve and run to completion", backend.TurnOptions{ApprovedPlanExecution: true, CodePlanExecutionMode: agent.CodePlanRunToCompletion})
	if err != nil {
		t.Fatal(err)
	}
	drained := make(chan struct{})
	go func() {
		for range ch {
		}
		close(drained)
	}()
	select {
	case <-drained:
	case <-time.After(25 * time.Second):
		t.Fatal("interrupted execution did not terminate")
	}
	before := codePlanSnapshot(t, b)
	if before.Document.CodeExecutionMode != agent.CodePlanRunToCompletion || before.Steps[0].Status != "complete" || before.Steps[1].Status != "pending" {
		t.Fatalf("expected an approved unfinished execution: %+v", before)
	}

	askCalls := 0
	const answer = "Phase one passed; phase two is still pending."
	m.next = func(*adkmodel.LLMRequest) *genai.Content {
		askCalls++
		return genai.NewContentFromText(answer, "model")
	}
	askCtx, askCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer askCancel()
	got := codePlanRun(t, askCtx, b, "Explain the current progress without executing anything", backend.TurnOptions{AskMode: true})
	if askCalls != 1 {
		t.Fatalf("Ask mode automatically continued approved work: model calls=%d", askCalls)
	}
	var visible strings.Builder
	for _, ev := range got {
		if ev.Kind == events.KindText {
			visible.WriteString(ev.Text)
		}
	}
	if !strings.Contains(visible.String(), answer) {
		t.Fatalf("Ask answer was not visible: %s", visible.String())
	}
	after := codePlanSnapshot(t, b)
	if after.Document.CodeExecutionMode != before.Document.CodeExecutionMode || after.Document.Results != "" || after.Steps[0].Evidence != before.Steps[0].Evidence || after.Steps[1].Status != "pending" || after.Steps[1].Evidence != "" {
		t.Fatalf("Ask mode altered execution state: before=%+v after=%+v", before, after)
	}
	if raw, err := os.ReadFile(filepath.Join(b.workingDir, "marker")); err != nil || string(raw) != "phase1" {
		t.Fatalf("Ask mode executed the next verifier: marker=%q err=%v", raw, err)
	}
	codePlanAssertUserTurns(t, b, 4)
	codePlanFinishScript(m)
	codePlanRun(t, askCtx, b, "continue", backend.TurnOptions{})
	codePlanAssertFinished(t, b, agent.CodePlanRunToCompletion)
	codePlanAssertUserTurns(t, b, 5)
}

func TestCodePlanRunTurnModeSurvivesCompactionAndRestartAcceptance(t *testing.T) {
	restore := redirectLogsForTUI(false)
	defer restore()
	for _, mode := range []agent.CodePlanExecutionMode{agent.CodePlanRunToCompletion, agent.CodePlanReviewEachPhase} {
		t.Run(string(mode), func(t *testing.T) {
			dir, workspace := t.TempDir(), t.TempDir()
			b, m := codePlanAcceptanceBackend(t, dir, workspace, "")
			codePlanAnnounce(t, b, m, false)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			calls := 0
			m.next = func(*adkmodel.LLMRequest) *genai.Content {
				calls++
				if calls == 1 {
					return codePlanCall("update_plan", tools.UpdatePlanArgs{Step: "phase-1", Status: "complete"}, m.calls)
				}
				// Simulate a user cancellation after durable phase-one verification.
				cancel()
				return genai.NewContentFromText("Interrupted after the verified phase.", "model")
			}
			ch, err := b.RunTurn(ctx, "approve execution", backend.TurnOptions{ApprovedPlanExecution: true, CodePlanExecutionMode: mode})
			if err != nil {
				t.Fatal(err)
			}
			drained := make(chan struct{})
			go func() {
				for range ch {
				}
				close(drained)
			}()
			select {
			case <-drained:
			case <-time.After(25 * time.Second):
				t.Fatal("cancelled/review turn did not terminate")
			}
			before := codePlanSnapshot(t, b)
			if before.Steps[0].Status != "complete" || before.Steps[1].Status != "pending" {
				t.Fatalf("bad interrupted fixture: %+v", before)
			}
			id := b.sessionID
			summary := []*adksession.Event{{ID: "execution-compacted-summary", Author: "model", LLMResponse: modelTextResponse("[Context Summary — previous messages compacted]")}}
			if _, err := b.fileStore.ArchiveAndReplaceEvents(codeAppName, b.effectiveUserID(), id, summary); err != nil {
				t.Fatal(err)
			}
			b, m = codePlanAcceptanceBackend(t, dir, workspace, id)
			after := codePlanSnapshot(t, b)
			if after.Document.CodeExecutionMode != mode || after.Document.CodeReviewPending != before.Document.CodeReviewPending || after.Steps[0].Evidence != before.Steps[0].Evidence || after.PlanID != before.PlanID {
				t.Fatalf("compaction/restart lost trusted state: before=%+v after=%+v", before, after)
			}
			codePlanFinishScript(m)
			resumeCtx, resumeCancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer resumeCancel()
			codePlanRun(t, resumeCtx, b, "continue", backend.TurnOptions{})
			codePlanAssertFinished(t, b, mode)
			codePlanAssertUserTurns(t, b, 1) // only the real post-compaction continue
			t.Logf("%s survives disk compaction and fresh backend restart, preserves phase-one evidence and completes phase two", mode)
		})
	}
}
