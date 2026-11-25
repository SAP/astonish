package launcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/common"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
)

const botFileEditMaxBytes = 1 << 20

// BotPublishedFileReceipt is created only after the trusted file service has
// snapshotted saved bytes. It is not a publication commit or external-effect proof.
type BotPublishedFileReceipt struct {
	RunID         store.RunID         `json:"run_id"`
	Path          string              `json:"path"`
	Title         string              `json:"title"`
	Purpose       string              `json:"purpose"`
	MemorySummary string              `json:"memory_summary"`
	Ref           store.BotContentRef `json:"ref"`
}

// BotFileReceipts is run-local and intentionally has no model-callable setter.
// Publication wiring consumes Snapshot(), never JSON in the model's final answer.
type BotFileReceipts struct {
	mu       sync.Mutex
	receipts []BotPublishedFileReceipt
}

func (r *BotFileReceipts) Snapshot() []BotPublishedFileReceipt {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]BotPublishedFileReceipt(nil), r.receipts...)
}

// Artifacts binds accumulator receipts to this run and Home. It never decodes a
// tool response or final answer and does not copy file bodies into the candidate.
func (r *BotFileReceipts) Artifacts(req bot.RunRequest) []store.BotArtifact {
	var result []store.BotArtifact
	for _, receipt := range r.Snapshot() {
		ref := receipt.Ref
		if receipt.RunID != req.Run.ID || ref.Validate() != nil || ref.Kind != store.BotContentArtifact || ref.Scope.BotID != req.Bot.ID || ref.Scope.OwnerID != req.Bot.OwnerID.String() {
			continue
		}
		result = append(result, store.BotArtifact{ID: uuid.NewSHA1(req.Run.ID, []byte(ref.Key+":"+ref.SHA256)), HomeID: req.Bot.Home.ID, RelativePath: receipt.Path, SHA256: ref.SHA256, Title: receipt.Title, Purpose: receipt.Purpose, MemorySummary: receipt.MemorySummary, SizeBytes: ref.SizeBytes, ContentRef: &ref})
	}
	return result
}

// BotPublicationToolsWithReceipts adds immutable delivery to Chat without
// replacing any ordinary filesystem tool. Construction performs no file I/O;
// the existing scoped file service validates authority when publication runs.
func BotPublicationToolsWithReceipts(r bot.RunRequest, scope *BotChatScope) ([]tool.Tool, *BotFileReceipts, error) {
	if scope == nil || scope.Services == nil || scope.Services.BotFilesFactory == nil {
		return nil, nil, store.ErrBotUnsupported
	}
	if r.Bot.ID == uuid.Nil || r.Run.ID == uuid.Nil || (r.Bot.Scope != store.BotScopePersonal && r.Bot.Scope != store.BotScopeTeam) {
		return nil, nil, store.ErrBotInvalid
	}
	receipts := &BotFileReceipts{}
	a := &botFileAdapter{request: r, scope: scope, receipts: receipts}
	publish, err := functiontool.New(functiontool.Config{Name: "publish_file", Description: "Snapshot an actual saved outputs/ file for delivery. Returns a trusted immutable file receipt, not proof of external execution. Provide a concise title, purpose and memory_summary; binary files are snapshotted without loading bytes into model context."}, a.publish)
	if err != nil {
		return nil, nil, err
	}
	return []tool.Tool{&botConfinedFileTool{Tool: publish}}, receipts, nil
}

func BotFileTools(r bot.RunRequest, scope *BotChatScope) ([]tool.Tool, error) {
	ts, _, err := BotFileToolsWithReceipts(r, scope)
	return ts, err
}

// BotFileToolsWithReceipts exposes the trusted accumulator to production wiring.
// Tools still require the existing bound-tool policy wrapper; this grants nothing.
func BotFileToolsWithReceipts(r bot.RunRequest, scope *BotChatScope) ([]tool.Tool, *BotFileReceipts, error) {
	if scope == nil || scope.Services == nil || scope.Services.BotFilesFactory == nil {
		return nil, nil, store.ErrBotUnsupported
	}
	if r.Bot.ID == uuid.Nil || r.Run.ID == uuid.Nil || (r.Bot.Scope != store.BotScopePersonal && r.Bot.Scope != store.BotScopeTeam) {
		return nil, nil, store.ErrBotInvalid
	}
	publication, receipts, err := BotPublicationToolsWithReceipts(r, scope)
	if err != nil {
		return nil, nil, err
	}
	a := &botFileAdapter{request: r, scope: scope, receipts: receipts}
	initCtx, err := execution.WithPrincipal(context.Background(), r.Principal)
	if err != nil {
		return nil, nil, err
	}
	if _, err := a.files(initCtx); err != nil {
		return nil, nil, err
	}
	read, err := functiontool.New(functiontool.Config{Name: "read_file", Description: "Read a bounded UTF-8 preview from a Bot Home-relative path. offset and limit are bytes (default 64 KiB, maximum 256 KiB). Returns SHA256 for a later compare-and-swap edit; no private files."}, a.read)
	if err != nil {
		return nil, nil, err
	}
	write, err := functiontool.New(functiontool.Config{Name: "write_file", Description: "Save UTF-8 content below outputs/ in this Bot's retained Home. New files require create_only=true; overwrite requires expected_sha256 from a prior read. Maximum 1 MiB per write; no shell execution."}, a.write)
	if err != nil {
		return nil, nil, err
	}
	edit, err := functiontool.New(functiontool.Config{Name: "edit_file", Description: "Exact text replacement in an outputs/ file, guarded by expected_sha256. Ambiguous matches require replace_all=true. Read back before publishing."}, a.edit)
	if err != nil {
		return nil, nil, err
	}
	find, err := functiontool.New(functiontool.Config{Name: "find_files", Description: "List one bounded page of immediate Bot Home children, optionally matching a basename glob. Use next cursor to continue; does not recurse or provision storage."}, a.list)
	if err != nil {
		return nil, nil, err
	}
	tree, err := functiontool.New(functiontool.Config{Name: "file_tree", Description: "Lazy Bot Home tree: list a bounded page of immediate children of path (default .). Expand a directory by calling again with its path. Private files are omitted."}, a.list)
	if err != nil {
		return nil, nil, err
	}
	all := []tool.Tool{read, write, edit, find, tree}
	for i, t := range all {
		all[i] = &botConfinedFileTool{Tool: t}
	}
	return append(all, publication...), receipts, nil
}

// botConfinedFileTool is a concrete trust marker for catalogue replacement.
// A matching name on an arbitrary scoped tool never acquires that privilege.
type botConfinedFileTool struct{ tool.Tool }

func (t *botConfinedFileTool) Declaration() *genai.FunctionDeclaration {
	return t.Tool.(common.ToolWithDeclaration).Declaration()
}
func (t *botConfinedFileTool) ProcessRequest(ctx tool.Context, r *model.LLMRequest) error {
	if r == nil {
		return store.ErrBotInvalid
	}
	if r.Tools == nil {
		r.Tools = map[string]any{}
	}
	if _, ok := r.Tools[t.Name()]; ok {
		return fmt.Errorf("duplicate tool %q", t.Name())
	}
	r.Tools[t.Name()] = t
	if r.Config == nil {
		r.Config = &genai.GenerateContentConfig{}
	}
	r.Config.Tools = append(r.Config.Tools, &genai.Tool{FunctionDeclarations: []*genai.FunctionDeclaration{t.Declaration()}})
	return nil
}
func (t *botConfinedFileTool) Run(ctx tool.Context, args any) (map[string]any, error) {
	if ctx == nil {
		return nil, store.ErrBotForbidden
	}
	return t.Tool.(botRunnableTool).Run(ctx, args)
}

type botFileAdapter struct {
	request  bot.RunRequest
	scope    *BotChatScope
	receipts *BotFileReceipts
}
type BotReadFileArgs struct {
	Path   string `json:"path"`
	Offset int64  `json:"offset,omitempty"`
	Limit  int64  `json:"limit,omitempty"`
}
type BotReadFileResult struct {
	Entry      store.BotFileEntry `json:"entry"`
	Content    string             `json:"content"`
	Offset     int64              `json:"offset"`
	NextOffset int64              `json:"next_offset"`
	Truncated  bool               `json:"truncated"`
}
type BotWriteFileArgs struct {
	Path           string `json:"path"`
	Content        string `json:"content"`
	CreateOnly     bool   `json:"create_only,omitempty"`
	ExpectedSHA256 string `json:"expected_sha256,omitempty"`
}
type BotEditFileArgs struct {
	Path           string `json:"path"`
	OldString      string `json:"old_string"`
	NewString      string `json:"new_string"`
	ExpectedSHA256 string `json:"expected_sha256"`
	ReplaceAll     bool   `json:"replace_all,omitempty"`
}
type BotListFilesArgs struct {
	Path    string `json:"path,omitempty"`
	Cursor  string `json:"cursor,omitempty"`
	Limit   int    `json:"limit,omitempty"`
	Pattern string `json:"pattern,omitempty"`
}
type BotPublishFileArgs struct {
	Path          string `json:"path"`
	Title         string `json:"title"`
	Purpose       string `json:"purpose"`
	MemorySummary string `json:"memory_summary"`
}

func botToolFilePath(p string, directory, write bool) error {
	if directory && p == "." {
		return nil
	}
	if sandbox.ValidateBotHomeArtifactPath(p) != nil {
		return store.ErrBotInvalid
	}
	for _, part := range strings.Split(p, "/") {
		lower := strings.ToLower(part)
		if strings.HasPrefix(part, ".") || lower == "private" || lower == "authority" || lower == "credentials" || lower == "policy.md" || lower == "policy.json" {
			return store.ErrBotForbidden
		}
	}
	// Dedicated reviewed document/memory tools own instruction and memory edits.
	// Ordinary reversible file writes cannot alter authority or executable setup.
	if write && !strings.HasPrefix(p, "outputs/") {
		return fmt.Errorf("%w: write deliverables below outputs/", store.ErrBotForbidden)
	}
	return nil
}
func (a *botFileAdapter) files(ctx context.Context) (store.BotFiles, error) {
	if ctx == nil {
		return nil, store.ErrBotForbidden
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := a.scope.Services.BotFilesFactory(ctx, a.scope.Services, a.request.Bot.Scope)
	if err != nil {
		return nil, err
	}
	if f == nil {
		return nil, store.ErrBotUnsupported
	}
	if m, ok := f.(*BotHomeManager); ok {
		repo := a.scope.Services.Bots
		if a.request.Bot.Scope == store.BotScopePersonal {
			repo = a.scope.Services.PersonalBots
		}
		fence, ok := repo.(store.BotFileCommitStore)
		if !ok {
			return nil, store.ErrBotUnsupported
		}
		copy := *m
		copy.AuthorizeFileCommit = func(c context.Context, h store.BotHomeBinding, commit func() error) error {
			lease := a.request.Run.Lease
			if a.request.CurrentLease != nil {
				lease = a.request.CurrentLease()
			}
			return fence.WithBotFileCommit(c, h, lease, commit)
		}
		f = &copy
	}
	return f, nil
}
func (a *botFileAdapter) read(ctx tool.Context, args BotReadFileArgs) (BotReadFileResult, error) {
	var result BotReadFileResult
	if err := botToolFilePath(args.Path, false, false); err != nil {
		return result, err
	}
	if args.Offset < 0 || args.Limit < 0 || args.Limit > store.BotFilePreviewMaxBytes {
		return result, store.ErrBotInvalid
	}
	if args.Limit == 0 {
		args.Limit = 64 << 10
	}
	f, err := a.files(ctx)
	if err != nil {
		return result, err
	}
	s, err := f.Open(ctx, a.request.Bot.ID, args.Path, store.BotFileRange{Offset: args.Offset, Length: args.Limit})
	if err != nil {
		return result, err
	}
	if s == nil || s.ReadCloser == nil {
		return result, store.ErrBotConflict
	}
	data, readErr := io.ReadAll(io.LimitReader(s, args.Limit))
	closeErr := s.Close()
	if readErr != nil {
		return result, readErr
	}
	if closeErr != nil {
		return result, closeErr
	}
	if s.Entry.Kind != store.BotFileRegular || s.Entry.Preview == store.BotFilePreviewDownload {
		return result, fmt.Errorf("%w: binary file; publish for download", store.ErrBotInvalid)
	}
	// A byte window may end inside a rune. Return only a valid prefix; the next
	// offset resumes at that rune rather than silently corrupting UTF-8.
	for !utf8.Valid(data) && len(data) > 0 && int64(len(data))+args.Offset < s.Entry.SizeBytes && len(data) > int(args.Limit)-4 {
		data = data[:len(data)-1]
	}
	if !utf8.Valid(data) || (len(data) == 0 && args.Offset < s.Entry.SizeBytes) {
		return result, fmt.Errorf("%w: use a UTF-8 boundary and a larger preview limit", store.ErrBotInvalid)
	}
	result = BotReadFileResult{Entry: s.Entry, Content: string(data), Offset: args.Offset, NextOffset: args.Offset + int64(len(data))}
	result.Truncated = result.NextOffset < s.Entry.SizeBytes
	a.recordFileReadObservation(ctx, "Read retained file "+args.Path)
	return result, nil
}
func validBotFileDigest(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == sha256.Size
}
func (a *botFileAdapter) write(ctx tool.Context, args BotWriteFileArgs) (*store.BotFileEntry, error) {
	if err := botToolFilePath(args.Path, false, true); err != nil {
		return nil, err
	}
	if len(args.Content) > botFileEditMaxBytes || !utf8.ValidString(args.Content) || args.CreateOnly == (args.ExpectedSHA256 != "") || (!args.CreateOnly && !validBotFileDigest(args.ExpectedSHA256)) {
		return nil, store.ErrBotInvalid
	}
	f, err := a.files(ctx)
	if err != nil {
		return nil, err
	}
	entry, err := f.Write(ctx, a.request.Bot.ID, store.BotFileWrite{Path: args.Path, Body: strings.NewReader(args.Content), SizeBytes: int64(len(args.Content)), CreateOnly: args.CreateOnly, ExpectedSHA256: args.ExpectedSHA256})
	if err != nil {
		return nil, err
	}
	// Independent retained readback, not successful dispatch or model arguments,
	// is the evidence for this strictly local filesystem effect.
	stream, err := f.Open(ctx, a.request.Bot.ID, args.Path, store.BotFileRange{})
	if err != nil {
		return nil, err
	}
	if stream == nil || stream.ReadCloser == nil {
		return nil, store.ErrBotConflict
	}
	hash := sha256.New()
	n, readErr := io.Copy(hash, io.LimitReader(stream, botFileEditMaxBytes+1))
	closeErr := stream.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if entry == nil || entry.Path != args.Path || entry.Kind != store.BotFileRegular || n != entry.SizeBytes || fmt.Sprintf("%x", hash.Sum(nil)) != entry.SHA256 {
		return nil, store.ErrBotConflict
	}
	if !args.CreateOnly && entry.SHA256 == args.ExpectedSHA256 {
		a.recordFileReadObservation(ctx, "Retained file unchanged "+entry.Path)
	} else {
		a.recordFileObservation(ctx, "Saved retained file "+entry.Path, entry.SHA256)
	}
	return entry, nil
}
func (a *botFileAdapter) edit(ctx tool.Context, args BotEditFileArgs) (*store.BotFileEntry, error) {
	if err := botToolFilePath(args.Path, false, true); err != nil {
		return nil, err
	}
	if args.OldString == "" || !validBotFileDigest(args.ExpectedSHA256) || len(args.NewString) > botFileEditMaxBytes {
		return nil, store.ErrBotInvalid
	}
	f, err := a.files(ctx)
	if err != nil {
		return nil, err
	}
	s, err := f.Open(ctx, a.request.Bot.ID, args.Path, store.BotFileRange{Length: botFileEditMaxBytes + 1})
	if err != nil {
		return nil, err
	}
	if s == nil || s.ReadCloser == nil {
		return nil, store.ErrBotConflict
	}
	data, err := io.ReadAll(io.LimitReader(s, botFileEditMaxBytes+1))
	closeErr := s.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if len(data) > botFileEditMaxBytes || !utf8.Valid(data) {
		return nil, store.ErrBotInvalid
	}
	if fmt.Sprintf("%x", sha256.Sum256(data)) != args.ExpectedSHA256 {
		return nil, store.ErrBotConflict
	}
	n := strings.Count(string(data), args.OldString)
	if n == 0 || (n > 1 && !args.ReplaceAll) {
		return nil, fmt.Errorf("%w: replacement matches %d times", store.ErrBotConflict, n)
	}
	count := 1
	if args.ReplaceAll {
		count = -1
	}
	return a.write(ctx, BotWriteFileArgs{Path: args.Path, Content: strings.Replace(string(data), args.OldString, args.NewString, count), ExpectedSHA256: args.ExpectedSHA256})
}
func (a *botFileAdapter) list(ctx tool.Context, args BotListFilesArgs) (*store.BotFilePage, error) {
	if args.Path == "" {
		args.Path = "."
	}
	if err := botToolFilePath(args.Path, true, false); err != nil {
		return nil, err
	}
	if args.Limit < 0 || args.Limit > store.BotFilePageMax || len(args.Cursor) > 4096 || len(args.Pattern) > 256 {
		return nil, store.ErrBotInvalid
	}
	if args.Limit == 0 {
		args.Limit = store.BotFilePageDefault
	}
	if args.Pattern != "" {
		if _, err := path.Match(args.Pattern, ""); err != nil {
			return nil, store.ErrBotInvalid
		}
	}
	f, err := a.files(ctx)
	if err != nil {
		return nil, err
	}
	page, err := f.List(ctx, a.request.Bot.ID, args.Path, args.Cursor, args.Limit)
	if err != nil {
		return nil, err
	}
	if page == nil {
		return nil, store.ErrBotConflict
	}
	result := &store.BotFilePage{Entries: []store.BotFileEntry{}, Next: page.Next}
	for _, entry := range page.Entries {
		if botToolFilePath(entry.Path, false, false) != nil {
			continue
		}
		if args.Pattern != "" {
			match, _ := path.Match(args.Pattern, path.Base(entry.Path))
			if !match {
				continue
			}
		}
		result.Entries = append(result.Entries, entry)
	}
	a.recordFileReadObservation(ctx, "Listed retained directory "+args.Path)
	return result, nil
}
func (a *botFileAdapter) publish(ctx tool.Context, args BotPublishFileArgs) (BotPublishedFileReceipt, error) {
	var result BotPublishedFileReceipt
	if err := botToolFilePath(args.Path, false, true); err != nil {
		return result, err
	}
	if strings.TrimSpace(args.Title) == "" || len(args.Title) > 200 || strings.TrimSpace(args.Purpose) == "" || len(args.Purpose) > 1000 || strings.TrimSpace(args.MemorySummary) == "" || len(args.MemorySummary) > 1000 {
		return result, store.ErrBotInvalid
	}
	f, err := a.files(ctx)
	if err != nil {
		return result, err
	}
	// Key and digest come from trusted execution, not model-supplied references.
	key := "runs/" + a.request.Run.ID.String() + "/" + uuid.NewString()
	ref, err := f.PublishSnapshot(ctx, a.request.Bot.ID, args.Path, key, nil)
	if err != nil {
		return result, err
	}
	if ref == nil || ref.Validate() != nil || ref.Kind != store.BotContentArtifact || ref.Scope.BotID != a.request.Bot.ID || ref.Scope.OwnerID != a.request.Bot.OwnerID.String() {
		return result, store.ErrBotConflict
	}
	a.recordFileObservation(ctx, "Snapshotted retained file "+args.Path, ref.SHA256)
	result = BotPublishedFileReceipt{RunID: a.request.Run.ID, Path: args.Path, Title: args.Title, Purpose: args.Purpose, MemorySummary: args.MemorySummary, Ref: *ref}
	a.receipts.mu.Lock()
	defer a.receipts.mu.Unlock()
	for _, prior := range a.receipts.receipts {
		if prior.Path == result.Path && prior.Ref.SHA256 == result.Ref.SHA256 {
			return prior, nil
		}
	}
	a.receipts.receipts = append(a.receipts.receipts, result)
	return result, nil
}

// Reads dispatched through execute_tool still need an out-of-band observation:
// the generic bridge name cannot establish that the underlying action was read-only.
// This attests only local retrieval, never content truth or external execution.
func (a *botFileAdapter) recordFileReadObservation(ctx tool.Context, summary string) {
	bot.RecordToolOutcome(ctx, ctx.FunctionCallID(), bot.ToolOutcome{Verified: true, Unchanged: []store.BotEvidence{{Kind: store.BotEvidenceObservation, Summary: summary, Source: "retained-file-read://" + a.request.Bot.ID.String() + "/" + ctx.FunctionCallID(), ObservedAt: time.Now().UTC(), ActorID: a.request.Bot.OwnerID}}})
}

func (a *botFileAdapter) recordFileObservation(ctx tool.Context, summary, digest string) {
	bot.RecordToolOutcome(ctx, ctx.FunctionCallID(), bot.ToolOutcome{Verified: true, Changed: []store.BotEvidence{{Kind: store.BotEvidenceObservation, Summary: summary, Source: "retained-file:sha256:" + digest, ObservedAt: time.Now().UTC(), ActorID: a.request.Bot.OwnerID}}})
}

// BotFileActionClassifier is used only with the confined adapters above. The
// generic sandbox catalogue must never classify arbitrary write_file as safe.
type BotFileActionClassifier struct {
	Fallback interface {
		DescribeAction(context.Context, string, json.RawMessage) (execution.ActionDescriptor, error)
	}
}

func (c BotFileActionClassifier) DescribeAction(ctx context.Context, name string, args json.RawMessage) (execution.ActionDescriptor, error) {
	switch name {
	case "read_file", "find_files", "file_tree":
		return execution.ActionDescriptor{Tool: name, Arguments: args, Effect: execution.EffectRead, ConstraintsVerified: true}, nil
	case "write_file", "edit_file", "publish_file":
		return execution.ActionDescriptor{Tool: name, Arguments: args, Effect: execution.EffectReversibleWrite, ConstraintsVerified: true}, nil
	}
	if c.Fallback != nil {
		return c.Fallback.DescribeAction(ctx, name, args)
	}
	return BotCatalogueClassifier{}.DescribeAction(ctx, name, args)
}
