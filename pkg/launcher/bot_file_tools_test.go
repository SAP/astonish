package launcher

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/chatruntime"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"github.com/google/uuid"
	"google.golang.org/adk/tool"
)

// This test service uses actual saved bytes, not the write tool's arguments, to
// form snapshots. Embedded unsupported methods deliberately cannot be called.
type confinedTestFiles struct {
	store.BotFiles
	root         string
	scope        store.BotContentScope
	snapshots    map[string][]byte
	writes       int
	calls        int
	openOverride func()
	fail         error
}

func (f *confinedTestFiles) entry(p string) (*store.BotFileEntry, error) {
	b, e := os.ReadFile(filepath.Join(f.root, p))
	if os.IsNotExist(e) {
		return nil, store.ErrBotNotFound
	}
	if e != nil {
		return nil, e
	}
	return &store.BotFileEntry{Path: p, Kind: store.BotFileRegular, SizeBytes: int64(len(b)), SHA256: fmt.Sprintf("%x", sha256.Sum256(b)), Preview: store.BotFilePreviewText}, nil
}
func (f *confinedTestFiles) Open(_ context.Context, _ store.BotID, p string, r store.BotFileRange) (*store.BotFileStream, error) {
	if f.openOverride != nil {
		f.openOverride()
	}
	f.calls++
	if f.fail != nil {
		return nil, f.fail
	}
	e, err := f.entry(p)
	if err != nil {
		return nil, err
	}
	h, err := os.Open(filepath.Join(f.root, p))
	if err != nil {
		return nil, err
	}
	if _, err = h.Seek(r.Offset, io.SeekStart); err != nil {
		h.Close()
		return nil, err
	}
	return &store.BotFileStream{ReadCloser: h, Entry: *e}, nil
}
func (f *confinedTestFiles) Write(_ context.Context, _ store.BotID, w store.BotFileWrite) (*store.BotFileEntry, error) {
	f.calls++
	if f.fail != nil {
		return nil, f.fail
	}
	prior, err := f.entry(w.Path)
	if w.CreateOnly && err == nil {
		return nil, store.ErrBotConflict
	}
	if !w.CreateOnly && (err != nil || prior.SHA256 != w.ExpectedSHA256) {
		return nil, store.ErrBotConflict
	}
	b, err := io.ReadAll(w.Body)
	if err != nil {
		return nil, err
	}
	p := filepath.Join(f.root, w.Path)
	if err = os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return nil, err
	}
	if err = os.WriteFile(p, b, 0600); err != nil {
		return nil, err
	}
	f.writes++
	return f.entry(w.Path)
}
func (f *confinedTestFiles) List(_ context.Context, _ store.BotID, p, cursor string, limit int) (*store.BotFilePage, error) {
	f.calls++
	if f.fail != nil {
		return nil, f.fail
	}
	return &store.BotFilePage{Next: "opaque-next", Entries: []store.BotFileEntry{{Path: "outputs", Kind: store.BotFileDirectory}, {Path: ".private", Kind: store.BotFileDirectory}, {Path: "POLICY.md", Kind: store.BotFileRegular}, {Path: "MEMORY.md", Kind: store.BotFileRegular}}}, nil
}
func (f *confinedTestFiles) PublishSnapshot(_ context.Context, _ store.BotID, p, key string, _ func(func() error) error) (*store.BotContentRef, error) {
	f.calls++
	if f.fail != nil {
		return nil, f.fail
	}
	b, err := os.ReadFile(filepath.Join(f.root, p))
	if os.IsNotExist(err) {
		return nil, store.ErrBotNotFound
	}
	if err != nil {
		return nil, err
	}
	f.snapshots[key] = append([]byte(nil), b...)
	return &store.BotContentRef{Format: store.BotContentFormatVersion, Scope: f.scope, Kind: store.BotContentArtifact, Key: key, SHA256: fmt.Sprintf("%x", sha256.Sum256(b)), SizeBytes: int64(len(b)), Revision: 1}, nil
}
func confinedFixture(t *testing.T) (bot.RunRequest, *BotChatScope, *confinedTestFiles, tool.Context) {
	t.Helper()
	f := newBotFactoryFixture(t)
	files := &confinedTestFiles{root: t.TempDir(), scope: store.BotContentScope{BotID: f.req.Bot.ID, OwnerID: f.req.Bot.OwnerID.String()}, snapshots: map[string][]byte{}}
	services := &store.Services{BotFilesFactory: func(ctx context.Context, _ *store.Services, scope store.BotScope) (store.BotFiles, error) {
		if scope != f.req.Bot.Scope {
			t.Fatal("wrong scope")
		}
		return files, nil
	}}
	return f.req, &BotChatScope{Services: services}, files, basicToolContext{botFactoryHandoffContext: botFactoryHandoffContext{base: f.ctx, session: f.req.Run.SessionID.String()}}
}
func confinedCall(ts []tool.Tool, ctx tool.Context, name string, args map[string]any) (map[string]any, error) {
	for _, t := range ts {
		if t.Name() == name {
			return t.(botRunnableTool).Run(ctx, args)
		}
	}
	return nil, fmt.Errorf("missing %s", name)
}
func TestBotPublicationExtensionUsesSavedBytes(t *testing.T) {
	r, scope, files, ctx := confinedFixture(t)
	// Chat owns file creation; the Bot extension only snapshots saved bytes.
	body := []byte("# Shared Chat deliverable\n")
	if err := os.MkdirAll(filepath.Join(files.root, "outputs"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(files.root, "outputs/report.md"), body, 0600); err != nil {
		t.Fatal(err)
	}
	ts, receipts, err := BotPublicationToolsWithReceipts(r, scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) != 1 || ts[0].Name() != "publish_file" {
		t.Fatalf("publication extension replaces ordinary Chat tools: %v", ts)
	}
	if files.calls != 0 || files.writes != 0 || len(receipts.Snapshot()) != 0 {
		t.Fatal("constructing publication extension touched retained files")
	}
	args := map[string]any{"path": "outputs/report.md", "title": "Report", "purpose": "Deliver result", "memory_summary": "Saved report"}
	if _, err := confinedCall(ts, ctx, "publish_file", args); err != nil {
		t.Fatal(err)
	}
	got := receipts.Snapshot()
	if len(got) != 1 || got[0].Ref.SHA256 != fmt.Sprintf("%x", sha256.Sum256(body)) || string(files.snapshots[got[0].Ref.Key]) != string(body) || files.writes != 0 {
		t.Fatalf("publication did not snapshot saved bytes: %+v", got)
	}
	if err := os.WriteFile(filepath.Join(files.root, "outputs/report.md"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if string(files.snapshots[got[0].Ref.Key]) != string(body) {
		t.Fatal("snapshot changed with workspace")
	}
	for _, badPath := range []string{"outputs/missing.md", "../private", ".private/report.md"} {
		args["path"] = badPath
		if _, err := confinedCall(ts, ctx, "publish_file", args); err == nil {
			t.Fatalf("invalid publication accepted: %s", badPath)
		}
	}
	if len(receipts.Snapshot()) != 1 {
		t.Fatal("failed publication acquired receipt")
	}
}

func TestBotPublicationExtensionSharedRuntime(t *testing.T) {
	f := newBotFactoryFixture(t)
	cfg := &config.AppConfig{}
	inputs, err := chatruntime.Build(t.Context(), chatruntime.BuildOptions{Principal: f.req.Principal, Services: f.services, ResourceScope: chatruntime.ResourceScopePersonal, AppConfig: cfg})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inputs.Close() })
	ctx, err := inputs.Bind(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	extensions, _, err := BotPublicationToolsWithReceipts(f.req, &BotChatScope{Services: f.services})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewScopedChatRuntime(ctx, ChatRuntimeDependencies{
		Inputs: inputs, Config: &ChatFactoryConfig{AppConfig: cfg},
		LLM: f.resolver.model, Sessions: f.sessions, SessionID: f.req.Run.SessionID.String(),
		Resources: &ChatCatalogResources{Pool: &catalogTestPool{}},
	}, ChatRuntimeHooks{AdditionalTools: extensions})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Cleanup()
	if runtime.ChatAgent.ToolIndex.GetToolEntry("publish_file") == nil {
		t.Fatal("shared runtime omitted publication extension")
	}
	ordinary, err := tools.GetInternalTools()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"read_file", "write_file", "edit_file", "find_files", "file_tree"} {
		var want, got []byte
		for _, candidate := range ordinary {
			if candidate.Name() == name {
				want, err = json.Marshal(candidate.(botRunnableTool).Declaration())
				if err != nil {
					t.Fatal(err)
				}
			}
		}
		for _, candidate := range runtime.InternalTools {
			if candidate.Name() == name {
				got, err = json.Marshal(candidate.(botRunnableTool).Declaration())
				if err != nil {
					t.Fatal(err)
				}
			}
		}
		if len(want) == 0 || string(want) != string(got) {
			t.Fatalf("Bot publication changed ordinary %s schema: want %s, got %s", name, want, got)
		}
	}
}

func TestBotFileFirstToolsReadbackAttestation(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(fmt.Sprint(corrupt), func(t *testing.T) {
			r, scope, files, raw := confinedFixture(t)
			base, _ := bot.WithToolOutcomes(raw)
			ctx := basicToolContext{botFactoryHandoffContext: botFactoryHandoffContext{base: base, session: r.Run.SessionID.String()}}
			if corrupt {
				files.openOverride = func() {
					if err := os.WriteFile(filepath.Join(files.root, "outputs/report.md"), []byte("different saved bytes"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			ts, _, err := BotFileToolsWithReceipts(r, scope)
			if err != nil {
				t.Fatal(err)
			}
			_, err = confinedCall(ts, ctx, "write_file", map[string]any{"path": "outputs/report.md", "content": "intended saved bytes", "create_only": true})
			outcome, ok := bot.TakeToolOutcome(ctx, ctx.FunctionCallID())
			if corrupt {
				if !errors.Is(err, store.ErrBotConflict) || ok {
					t.Fatalf("corrupt readback attested: %+v %v %v", outcome, ok, err)
				}
				return
			}
			if err != nil || !ok || !outcome.Verified || len(outcome.Changed) != 1 || !bot.ValidObservation(outcome.Changed[0]) {
				t.Fatalf("missing readback: %+v %v %v", outcome, ok, err)
			}
		})
	}
}

func TestBotFileFirstReadOnlyObservations(t *testing.T) {
	r, scope, files, raw := confinedFixture(t)
	if err := os.MkdirAll(filepath.Join(files.root, "outputs"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(files.root, "outputs/report.md"), []byte("retained text"), 0600); err != nil {
		t.Fatal(err)
	}
	ts, _, err := BotFileToolsWithReceipts(r, scope)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"read_file", "find_files", "file_tree"} {
		t.Run(name, func(t *testing.T) {
			base, _ := bot.WithToolOutcomes(raw)
			ctx := basicToolContext{botFactoryHandoffContext: botFactoryHandoffContext{base: base, session: r.Run.SessionID.String()}}
			path := "outputs"
			if name == "read_file" {
				path = "outputs/report.md"
			}
			_, err := confinedCall(ts, ctx, name, map[string]any{"path": path})
			outcome, ok := bot.TakeToolOutcome(ctx, ctx.FunctionCallID())
			if err != nil || !ok || !outcome.Verified || len(outcome.Changed) != 0 || len(outcome.Unchanged) != 1 || !bot.ValidObservation(outcome.Unchanged[0]) {
				t.Fatalf("read-only retrieval observation: %+v %v %v", outcome, ok, err)
			}
		})
	}
	base, _ := bot.WithToolOutcomes(raw)
	ctx := basicToolContext{botFactoryHandoffContext: botFactoryHandoffContext{base: base, session: r.Run.SessionID.String()}}
	files.fail = store.ErrBotForbidden
	_, err = confinedCall(ts, ctx, "read_file", map[string]any{"path": "outputs/report.md"})
	if _, ok := bot.TakeToolOutcome(ctx, ctx.FunctionCallID()); err == nil || ok {
		t.Fatal("failed read acquired success observation")
	}
}

func TestBotFileFirstToolsSavedBytesAndReceipts(t *testing.T) {
	r, scope, files, ctx := confinedFixture(t)
	ts, receipts, err := BotFileToolsWithReceipts(r, scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) != 6 || len(receipts.Snapshot()) != 0 {
		t.Fatal("wrong initial tools/receipts")
	}
	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		out, err := confinedCall(ts, ctx, name, args)
		if err != nil {
			t.Fatal(name, err)
		}
		return out
	}
	call("write_file", map[string]any{"path": "outputs/report.md", "content": "real saved report", "create_only": true})
	read := call("read_file", map[string]any{"path": "outputs/report.md"})
	if read["content"] != "real saved report" {
		t.Fatalf("readback: %v", read)
	}
	prior, _ := files.entry("outputs/report.md")
	call("edit_file", map[string]any{"path": "outputs/report.md", "old_string": "real", "new_string": "revised", "expected_sha256": prior.SHA256})
	if _, err := confinedCall(ts, ctx, "write_file", map[string]any{"path": "outputs/report.md", "content": "stale", "expected_sha256": prior.SHA256}); err == nil {
		t.Fatal("stale overwrite allowed")
	}
	call("publish_file", map[string]any{"path": "outputs/report.md", "title": "Report", "purpose": "Deliver report", "memory_summary": "Finished report at outputs/report.md"})
	saved := receipts.Snapshot()
	if len(saved) != 1 || string(files.snapshots[saved[0].Ref.Key]) != "revised saved report" {
		t.Fatalf("receipt did not snapshot disk: %+v", saved)
	}
	current, _ := files.entry("outputs/report.md")
	call("write_file", map[string]any{"path": "outputs/report.md", "content": "later working revision", "expected_sha256": current.SHA256})
	if string(files.snapshots[saved[0].Ref.Key]) != "revised saved report" {
		t.Fatal("snapshot mutated with working file")
	}
	artifacts := receipts.Artifacts(r)
	if len(artifacts) != 1 || artifacts[0].ContentRef == nil || *artifacts[0].ContentRef != saved[0].Ref || artifacts[0].RelativePath != "outputs/report.md" || artifacts[0].Title != "Report" {
		t.Fatalf("trusted receipt was not bound to artifact: %+v", artifacts)
	}
	if again := receipts.Artifacts(r); again[0].ID != artifacts[0].ID {
		t.Fatal("artifact identity changed on metadata retry")
	}
	artifacts[0].ContentRef.Key = "forged"
	if receipts.Artifacts(r)[0].ContentRef.Key != saved[0].Ref.Key {
		t.Fatal("artifact reference aliases trusted receipt")
	}
	otherRun := r
	otherRun.Run.ID = uuid.New()
	if len(receipts.Artifacts(otherRun)) != 0 {
		t.Fatal("receipt crossed run boundary")
	}
	otherOwner := r
	otherOwner.Bot.OwnerID = uuid.New()
	if len(receipts.Artifacts(otherOwner)) != 0 {
		t.Fatal("receipt crossed owner boundary")
	}
	saved[0].Title = "forged"
	if receipts.Snapshot()[0].Title != "Report" {
		t.Fatal("receipt alias leaked")
	}
	page := call("file_tree", map[string]any{})
	b, _ := json.Marshal(page)
	if strings.Contains(string(b), ".private") || strings.Contains(string(b), "POLICY.md") || !strings.Contains(string(b), "MEMORY.md") {
		t.Fatal(string(b))
	}
}
func TestBotFileFirstToolsRejectInvalidAndUnavailable(t *testing.T) {
	r, scope, files, ctx := confinedFixture(t)
	if _, err := BotFileTools(r, nil); !errors.Is(err, store.ErrBotUnsupported) {
		t.Fatal(err)
	}
	unavailable := &BotChatScope{Services: &store.Services{BotFilesFactory: func(context.Context, *store.Services, store.BotScope) (store.BotFiles, error) {
		return nil, store.ErrBotUnsupported
	}}}
	if _, err := BotFileTools(r, unavailable); !errors.Is(err, store.ErrBotUnsupported) {
		t.Fatal("factory must fail closed", err)
	}
	ts, receipts, err := BotFileToolsWithReceipts(r, scope)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/etc/passwd", "../other", "outputs/../SOUL.md", ".private/receipt", "outputs/.credentials/key", "private/a", "POLICY.md"} {
		before := files.calls
		if _, err := confinedCall(ts, ctx, "read_file", map[string]any{"path": p}); err == nil {
			t.Errorf("read accepted %q", p)
		}
		if files.calls != before {
			t.Fatal("unsafe path reached backend")
		}
	}
	for _, args := range []map[string]any{{"path": "SOUL.md", "content": "change", "create_only": true}, {"path": "outputs/a", "content": "missing CAS"}, {"path": "outputs/a", "content": "ambiguous CAS", "create_only": true, "expected_sha256": strings.Repeat("0", 64)}, {"path": "outputs/a", "content": "bad CAS", "expected_sha256": "wrong"}} {
		if _, err := confinedCall(ts, ctx, "write_file", args); err == nil {
			t.Error("invalid write accepted", args)
		}
	}
	if _, err := confinedCall(ts, ctx, "read_file", map[string]any{"path": 7}); err == nil {
		t.Fatal("bad typed args accepted")
	}
	if _, err := confinedCall(ts, ctx, "read_file", map[string]any{"path": "outputs/a", "limit": store.BotFilePreviewMaxBytes + 1}); err == nil {
		t.Fatal("oversized preview accepted")
	}
	if _, err := confinedCall(ts, ctx, "publish_file", map[string]any{"path": "outputs/missing", "title": "Missing", "purpose": "Deliver", "memory_summary": "Not real"}); err == nil {
		t.Fatal("missing publication accepted")
	}
	if len(receipts.Snapshot()) != 0 {
		t.Fatal("failed call produced receipt")
	}
	files.fail = store.ErrBotConflict
	if _, err := confinedCall(ts, ctx, "write_file", map[string]any{"path": "outputs/a", "content": "stale lease", "create_only": true}); err == nil {
		t.Fatal("stale lease accepted")
	}
}
func TestBotFileFirstToolsBoundedAndAmbiguousEdit(t *testing.T) {
	r, scope, files, ctx := confinedFixture(t)
	ts, _, err := BotFileToolsWithReceipts(r, scope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = confinedCall(ts, ctx, "write_file", map[string]any{"path": "outputs/a", "content": strings.Repeat("x", 100000), "create_only": true}); err != nil {
		t.Fatal(err)
	}
	out, err := confinedCall(ts, ctx, "read_file", map[string]any{"path": "outputs/a", "limit": 8})
	if err != nil || out["content"] != "xxxxxxxx" || out["truncated"] != true {
		t.Fatal(out, err)
	}
	entry, _ := files.entry("outputs/a")
	if _, err = confinedCall(ts, ctx, "edit_file", map[string]any{"path": "outputs/a", "old_string": "x", "new_string": "y", "expected_sha256": entry.SHA256}); err == nil {
		t.Fatal("ambiguous edit accepted")
	}
	if _, err = confinedCall(ts, ctx, "edit_file", map[string]any{"path": "outputs/a", "old_string": "x", "new_string": "y", "replace_all": true, "expected_sha256": entry.SHA256}); err != nil {
		t.Fatal(err)
	}
}
func TestBotFileFirstCatalogueUsesConfinedAdapters(t *testing.T) {
	r, scope, _, ctx := confinedFixture(t)
	ts, err := BotFileTools(r, scope)
	if err != nil {
		t.Fatal(err)
	}
	client := &catalogueClient{}
	catalogue, err := BotCatalogueTools(client, r.Run.SessionID.String(), ts...)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range catalogue {
		if entry.Name() == "write_file" {
			count++
			if _, ok := entry.(*botConfinedFileTool); !ok {
				t.Fatal("unconfined file adapter")
			}
		}
	}
	if count != 1 {
		t.Fatal("duplicate/missing writer", count)
	}
	if _, err = confinedCall(catalogue, ctx, "write_file", map[string]any{"path": "outputs/a", "content": "confined", "create_only": true}); err != nil {
		t.Fatal(err)
	}
	if _, err = BotCatalogueTools(client, r.Run.SessionID.String(), append(ts, ts[0])...); err == nil {
		t.Fatal("duplicate confined adapter accepted")
	}
}

func TestBotFileFirstClassifierDoesNotAuthorizeShell(t *testing.T) {
	c := BotFileActionClassifier{}
	for _, name := range []string{"write_file", "edit_file", "publish_file"} {
		a, err := c.DescribeAction(context.Background(), name, json.RawMessage(`{}`))
		if err != nil || a.Effect != execution.EffectReversibleWrite || !a.ConstraintsVerified {
			t.Fatal(name, a, err)
		}
	}
	for _, name := range []string{"shell_command", "unknown", "http_request"} {
		a, err := c.DescribeAction(context.Background(), name, json.RawMessage(`{}`))
		if err != nil || a.Effect != execution.EffectUnknown || a.ConstraintsVerified {
			t.Fatal(name, a, err)
		}
	}
	a, _ := (BotCatalogueClassifier{}).DescribeAction(context.Background(), "write_file", json.RawMessage(`{}`))
	if a.Effect != execution.EffectUnknown {
		t.Fatal("generic sandbox write became authorized")
	}
}
