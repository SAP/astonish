package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/SAP/astonish/pkg/pathscope"
)

// This file implements the code-mode authorization model: two independent
// gates that make `astonish code` safe-by-default even though it runs tools
// directly on the host (no sandbox).
//
//  1. Tool-execution authorization (Normal mode only). Read-only / navigation
//     tools (agent.SafeTools) auto-run. Any other tool (write_file, edit_file,
//     shell_command, memory_save, delegate_tasks, …) requires the user to
//     authorize it. The user can allow it once, or allow every
//     not-whitelisted tool for the remainder of the current iteration (turn).
//     Grants are reset when a new user message begins.
//
//  2. Folder-access authorization. By default tools may only touch the project
//     working directory and its subtree. A path outside that root requires the
//     user to authorize it — once, or for the whole session.
//
// Plan mode is unchanged: the SafeTools list is a hard allow-list there and
// non-safe tools are refused outright (see chat_agent_run.go plan-mode gate).
// In Normal mode the SAME SafeTools list is reused as the auto-allow baseline;
// the difference is that a non-whitelisted tool prompts for authorization
// instead of being blocked.

// pathArgKeys are the tool-argument keys that carry a filesystem path. They
// match the JSON arg names used by the built-in tools (read_file, write_file,
// edit_file, file_tree, find_files, grep_search, shell_command, …). Kept
// domain-agnostic: this is a structural list of the path-shaped inputs the
// tools already declare, not per-tool special-casing.
var pathArgKeys = []string{"path", "file_path", "working_dir", "dir", "search_path"}

// pathArgSliceKeys carry a list of paths (e.g. browser_file_upload "paths").
var pathArgSliceKeys = []string{"paths"}

// commandArgKeys carry a free-form shell command string whose operands may
// embed filesystem paths (shell_command "command"). Unlike pathArgKeys these
// are not themselves a path — the paths are extracted heuristically from the
// command text via pathscope.ExtractCommandPaths. Kept structural (a command
// arg MAY embed paths), not per-command special-casing.
var commandArgKeys = []string{"command"}

// RequiresToolAuthorization reports whether a tool needs explicit user
// authorization to run in Normal (non-plan) code mode. Read-only / safe tools
// (agent.SafeTools) and completion bookkeeping never do; everything else does.
// In plan mode the caller uses the plan-mode gate instead, so planMode
// short-circuits to false here (plan mode never routes through this path).
func RequiresToolAuthorization(name string, planMode bool) bool {
	if planMode {
		return false
	}
	// Like announce_plan/update_plan, completion records session plan state.
	// It uses existing phase evidence and executes no commands. Keep this
	// Code consent exemption separate from the Plan/Ask mode allow-lists.
	if name == "announce_completion" {
		return false
	}
	return !IsToolSafe(name)
}

// SessionAuthPolicy holds the per-session authorization state for one code-mode
// session. It is safe for concurrent use.
//
// Two lifetimes are tracked:
//   - iteration-scoped (tool grants): reset at the start of every new user turn
//     via ResetForNewTurn.
//   - session-scoped (folder grants marked "for session"): persist for the life
//     of the policy.
type CodePlanPermissionMode string

const (
	CodePlanCurrentApprovals CodePlanPermissionMode = "current_approvals"
	CodePlanFullAccess       CodePlanPermissionMode = "full_access"
)

func (m CodePlanPermissionMode) Valid() bool {
	return m == CodePlanCurrentApprovals || m == CodePlanFullAccess
}

// A lease is deliberately process-local. Neither snapshots nor tool payloads
// can recreate one. Every dispatch requires a private context binding as well.
type codePlanLease struct {
	sessionID string
	planID    string
	revoked   atomic.Bool
}
type codePlanInvocationKey struct{}
type codePlanInvocation struct {
	lease   *codePlanLease
	runtime *PlanRuntime
}

// GrantCodePlanFullAccess consumes trusted approval intent only after its atomic
// durable commit. It never changes ordinary tool or folder grants.
func (p *SessionAuthPolicy) GrantCodePlanFullAccess(sessionID string, r *PlanRuntime) error {
	if p == nil || r == nil || sessionID == "" {
		return fmt.Errorf("missing Code approval scope")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.codeApprovalCommitted || r.plan == nil {
		return fmt.Errorf("full access requires fresh trusted Code approval")
	}
	r.plan.mu.Lock()
	defer r.plan.mu.Unlock()
	s := snapshotPlanLocked(r.plan)
	if !codePlanSnapshotActive(s) {
		return fmt.Errorf("Code execution is not active")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.codePlanLease != nil {
		p.codePlanLease.revoked.Store(true)
	}
	p.codePlanLease = &codePlanLease{sessionID: sessionID, planID: s.PlanID}
	r.codeLease = p.codePlanLease
	r.codeApprovalCommitted = false
	return nil
}

// ReenableCodePlanFullAccess is a fresh trusted local consent for an already
// approved plan. It checks displayed identity without changing cadence, progress,
// or starting execution. It is never callable from model tools.
func (p *SessionAuthPolicy) ReenableCodePlanFullAccess(sessionID string, r *PlanRuntime, planID string, revision int64) error {
	if p == nil || r == nil || sessionID == "" {
		return fmt.Errorf("missing Code approval scope")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.plan == nil || !r.options.CodeExecution {
		return fmt.Errorf("no active Code plan")
	}
	r.plan.mu.Lock()
	defer r.plan.mu.Unlock()
	s := snapshotPlanLocked(r.plan)
	if s.PlanID != planID || s.Revision != revision || !codePlanSnapshotActive(s) {
		return fmt.Errorf("plan changed or is not approved for execution")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.codePlanLease != nil {
		p.codePlanLease.revoked.Store(true)
	}
	p.codePlanLease = &codePlanLease{sessionID: sessionID, planID: planID}
	r.codeLease = p.codePlanLease
	return nil
}

// CodePlanFullAccessGranted projects process-local consent for trusted local UI.
// It is not dispatch authorization: callers must use CodePlanFullAccessAllowed
// with an invocation binding before bypassing either consent gate.
func (p *SessionAuthPolicy) CodePlanFullAccessGranted(sessionID string, snapshot PlanSnapshot) bool {
	if p == nil || !codePlanSnapshotActive(snapshot) {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	l := p.codePlanLease
	return l != nil && !l.revoked.Load() && l.sessionID == sessionID && l.planID == snapshot.PlanID
}

func (p *SessionAuthPolicy) RevokeCodePlanFullAccess() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.codePlanLease != nil {
		p.codePlanLease.revoked.Store(true)
		p.codePlanLease = nil
	}
}

// ObserveCodePlanSnapshot retires authority on replacement, planning or Results.
// Call on restore and after durable publication, never before a failed commit.
func (p *SessionAuthPolicy) ObserveCodePlanSnapshot(s PlanSnapshot) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if l := p.codePlanLease; l != nil && (l.planID != s.PlanID || !codePlanSnapshotActive(s)) {
		l.revoked.Store(true)
		p.codePlanLease = nil
	}
}

// BindCodePlanExecution is a trusted local invocation boundary, not a grant.
// Read-only/planning modes and ordinary unapproved turns cannot activate a lease.
func (p *SessionAuthPolicy) BindCodePlanExecution(ctx context.Context, sessionID string, r *PlanRuntime) context.Context {
	if p == nil || r == nil {
		return ctx
	}
	po := PromptOverridesFromContext(ctx)
	if po == nil || !po.ApprovedPlanExecution || po.PlanMode || po.GraphPlanMode || po.AskMode || po.Bot != nil {
		return ctx
	}
	s := r.Snapshot()
	p.ObserveCodePlanSnapshot(s)
	p.mu.Lock()
	l := p.codePlanLease
	p.mu.Unlock()
	if l == nil || l.sessionID != sessionID || l.revoked.Load() || !r.CodeExecutionActive() {
		return ctx
	}
	r.mu.Lock()
	r.codeLease = l
	r.mu.Unlock()
	return context.WithValue(ctx, codePlanInvocationKey{}, codePlanInvocation{lease: l, runtime: r})
}

// CodePlanFullAccessAllowed rechecks the live runtime on every main/deferred/child
// call. The session argument is the parent owner for child callbacks.
func (p *SessionAuthPolicy) CodePlanFullAccessAllowed(ctx context.Context, sessionID string) bool {
	if p == nil || ctx == nil || ctx.Err() != nil {
		return false
	}
	b, ok := ctx.Value(codePlanInvocationKey{}).(codePlanInvocation)
	if !ok || b.lease == nil || b.runtime == nil || b.lease.revoked.Load() || b.lease.sessionID != sessionID {
		return false
	}
	if po := PromptOverridesFromContext(ctx); po == nil || !po.ApprovedPlanExecution || po.PlanMode || po.GraphPlanMode || po.AskMode || po.Bot != nil {
		return false
	}
	p.mu.Lock()
	current := p.codePlanLease == b.lease
	p.mu.Unlock()
	if !current {
		return false
	}
	s := b.runtime.Snapshot()
	return !b.lease.revoked.Load() && s.PlanID == b.lease.planID && codePlanSnapshotActive(s)
}

func codePlanSnapshotActive(s PlanSnapshot) bool {
	return s.PlanID != "" && s.Document.CodeExecutionMode.Valid() && s.Document.Lifecycle == PlanLifecycleApproved && len(s.Steps) > 0 && !s.IsFullyAccepted()
}

type SessionAuthPolicy struct {
	mu            sync.Mutex
	codePlanLease *codePlanLease
	// folderSettingsMu serializes durable changes, including revocation.
	folderSettingsMu      sync.Mutex
	folderSettings        CodeSessionFolderSettings
	persistFolderSettings func(CodeSessionFolderSettings) error

	// root is the absolute, symlink-resolved project working directory. Paths
	// inside root (or equal to it) are always allowed without a grant.
	root string

	// allowedRoots are additional absolute, symlink-resolved directories that
	// are implicitly in-scope (paths under any of them are always allowed
	// without a prompt), on top of root. Unlike pathSessionGrants these are
	// fixed at construction and never depend on a user grant. They exist so the
	// agent can freely read/write Astonish's own state (session transcripts,
	// PLAN.md, workspaces, config) which live outside the project directory —
	// prompting for those would be noise, and the agent writes there routinely.
	allowedRoots []string

	// allowAllToolsThisIteration, when true, auto-approves any not-whitelisted
	// tool for the remainder of the current turn. Reset by ResetForNewTurn.
	allowAllToolsThisIteration bool

	// allowAllToolsSession, when true, auto-approves any not-whitelisted tool
	// for the remainder of the SESSION. This is what the tool gate's "Always
	// Allow" grants: the user opted out of tool-execution prompts entirely, so
	// it must survive ResetForNewTurn (unlike allowAllToolsThisIteration, which
	// only covers the current turn). Mirrors the folder gate's "Always Allow",
	// which is likewise session-scoped (pathSessionGrants).
	allowAllToolsSession bool

	// toolOnceGrants holds one-shot grants for specific tools within the current
	// iteration. A grant is consumed on the next execution of that tool.
	toolOnceGrants map[string]bool

	// pathSessionGrants holds absolute, symlink-resolved directories the user
	// authorized for the whole session. A path under any of these is allowed.
	pathSessionGrants map[string]bool

	// pathOnceGrants holds one-shot grants for specific absolute paths. Consumed
	// on the next access to that path. These survive ResetForNewTurn so that a
	// path authorized "once" during an approval pause is honored when the tool
	// is re-driven in the resumed turn.
	pathOnceGrants map[string]bool

	// pending holds the single authorization request that currently owns the
	// approval UI. Concurrent callbacks must not replace it: one user decision
	// always applies to exactly one suspended operation.
	pending *PendingAuthorization
}

// PendingAuthorization records the authorization request a suspended turn is
// waiting on, so the decision (arriving as the next user message) can be
// applied to the right tool / paths.
type PendingAuthorization struct {
	// Kind is "tool" or "folder".
	Kind string
	// Tool is the tool name the request is for.
	Tool string
	// Paths are the out-of-scope absolute paths (folder requests only).
	Paths []string
	// Options captures the exact displayed order; nil retains legacy 1/2/3 semantics.
	Options []string
}

// TrySetPending records req only when no authorization request currently owns
// the approval UI. It returns true to the owner that must emit the prompt.
// Concurrent duplicate or distinct requests return false and must remain
// suspended behind the existing owner rather than replacing it.
func (p *SessionAuthPolicy) TrySetPending(req *PendingAuthorization) bool {
	if p == nil || req == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pending != nil {
		return false
	}
	p.pending = clonePendingAuthorization(req)
	return true
}

// SetPending records an authorization request for compatibility with callers
// that establish state serially. It never replaces a request that already owns
// the approval UI.
func (p *SessionAuthPolicy) SetPending(req *PendingAuthorization) {
	p.TrySetPending(req)
}

// Pending returns a snapshot of the request owning the approval UI, or nil.
func (p *SessionAuthPolicy) Pending() *PendingAuthorization {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return clonePendingAuthorization(p.pending)
}

func clonePendingAuthorization(req *PendingAuthorization) *PendingAuthorization {
	if req == nil {
		return nil
	}
	clone := *req
	clone.Paths = append([]string(nil), req.Paths...)
	clone.Options = append([]string(nil), req.Options...)
	return &clone
}

// ClearPending drops any recorded pending authorization request.
func (p *SessionAuthPolicy) ClearPending() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pending = nil
}

// NewSessionAuthPolicy creates a policy scoped to the given project root. An
// empty or invalid root disables folder scoping (everything allowed) — callers
// in code mode always pass the resolved working directory. Any extraRoots are
// additional directories treated as implicitly in-scope (their subtrees never
// prompt); empty/invalid entries are ignored. These are for the agent's own
// state directories (session store, workspaces, config), not user paths.
func NewSessionAuthPolicy(root string, extraRoots ...string) *SessionAuthPolicy {
	var allowed []string
	seen := make(map[string]bool)
	for _, r := range extraRoots {
		nd := normalizeDir(r)
		if nd == "" || seen[nd] {
			continue
		}
		seen[nd] = true
		allowed = append(allowed, nd)
	}
	return &SessionAuthPolicy{
		root:              normalizeDir(root),
		allowedRoots:      allowed,
		toolOnceGrants:    make(map[string]bool),
		pathSessionGrants: make(map[string]bool),
		pathOnceGrants:    make(map[string]bool),
	}
}

// Root returns the absolute, resolved project root the policy scopes to.
func (p *SessionAuthPolicy) Root() string {
	if p == nil {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.root
}

// ResetForNewTurn clears iteration-scoped grants (allow-all-this-iteration and
// per-tool once grants). Session-scoped folder grants are preserved. Call this
// when a genuinely new user message begins — NOT when resuming after an
// approval pause.
func (p *SessionAuthPolicy) ResetForNewTurn() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.allowAllToolsThisIteration = false
	p.toolOnceGrants = make(map[string]bool)
	// pathOnceGrants intentionally preserved: a "once" path grant is issued
	// during an approval pause and must survive into the resumed turn where the
	// tool actually runs. It is consumed on use instead.
}

// GrantToolOnce authorizes a single upcoming execution of the named tool.
func (p *SessionAuthPolicy) GrantToolOnce(name string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.toolOnceGrants[name] = true
}

// GrantAllToolsThisIteration authorizes every not-whitelisted tool for the
// remainder of the current turn.
func (p *SessionAuthPolicy) GrantAllToolsThisIteration() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.allowAllToolsThisIteration = true
}

// GrantAllToolsSession authorizes every not-whitelisted tool for the remainder
// of the SESSION (survives ResetForNewTurn). This backs the tool gate's "Always
// Allow": the user has opted out of tool-execution prompts, so subsequent turns
// must not re-prompt. Folder-access scoping is unaffected — an out-of-scope
// path still prompts via the folder gate.
func (p *SessionAuthPolicy) GrantAllToolsSession() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.allowAllToolsSession = true
}

// AllToolsAllowedForSession reports whether the durable "Always Allow" tool
// grant is active without consuming any one-shot grant.
func (p *SessionAuthPolicy) AllToolsAllowedForSession() bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.allowAllToolsSession
}

// ToolAuthorized reports whether the named tool may run right now given the
// current grants, consuming a one-shot grant if one applies. It does NOT
// consider the whitelist — callers should check RequiresToolAuthorization
// first and only call this for tools that need authorization.
func (p *SessionAuthPolicy) ToolAuthorized(name string) bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.allowAllToolsSession {
		return true
	}
	if p.allowAllToolsThisIteration {
		return true
	}
	if p.toolOnceGrants[name] {
		delete(p.toolOnceGrants, name)
		return true
	}
	return false
}

// GrantPathOnce authorizes a single upcoming access to the given path.
func (p *SessionAuthPolicy) GrantPathOnce(path string) {
	if p == nil {
		return
	}
	abs := normalizePath(path)
	if abs == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pathOnceGrants[abs] = true
}

// GrantPathForSession authorizes the given directory (and its subtree) for the
// remainder of the session. If path points at a file, its parent directory is
// granted.
func (p *SessionAuthPolicy) GrantPathForSession(path string) {
	if p == nil {
		return
	}
	dir := dirOf(path)
	if dir == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pathSessionGrants[dir] = true
}

// PathAllowed reports whether the given path may be accessed, consuming a
// one-shot grant if one applies. A path inside the project root or any
// session-granted directory is always allowed.
func (p *SessionAuthPolicy) PathAllowed(path string) bool {
	if p == nil {
		return true
	}
	abs := normalizePath(path)
	if abs == "" {
		// Unresolvable path: don't block on it here — let the tool surface a
		// real error rather than a confusing authorization prompt.
		return true
	}
	// Well-known harmless special paths (e.g. /dev/null) are always allowed.
	if pathscope.IsAlwaysAllowedPath(abs) {
		return true
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	// No root configured → folder scoping disabled.
	if p.root == "" || p.folderSettings.AllFolders {
		return true
	}
	if pathWithin(p.root, abs) {
		return true
	}
	for _, ar := range p.allowedRoots {
		if pathWithin(ar, abs) {
			return true
		}
	}
	for granted := range p.pathSessionGrants {
		if pathWithin(granted, abs) {
			return true
		}
	}
	if p.pathOnceGrants[abs] {
		delete(p.pathOnceGrants, abs)
		return true
	}
	return false
}

// OutOfScopePaths returns the resolved path args of a tool call that fall
// outside the allowed set. It does NOT consume grants — call ConsumePathGrants
// at execution time to consume any one-shot grant that covered an access. The
// returned slice is deduplicated and order-stable by first appearance.
func (p *SessionAuthPolicy) OutOfScopePaths(args map[string]any) []string {
	if p == nil || args == nil {
		return nil
	}
	var out []string
	for _, abs := range p.resolvedPathArgs(args) {
		p.mu.Lock()
		allowed := p.pathAllowedNoConsume(abs)
		p.mu.Unlock()
		if !allowed {
			out = append(out, abs)
		}
	}
	return out
}

// resolvedPathArgs extracts every path-shaped argument of a tool call and
// returns the normalized (absolute, symlink-resolved) form, deduplicated and
// order-stable by first appearance. It covers the discrete path arg keys, the
// path-slice keys, and heuristically-extracted operands from free-form command
// args (shell_command "command"). Shared by OutOfScopePaths and
// ConsumePathGrants so both see exactly the same set of paths.
func (p *SessionAuthPolicy) resolvedPathArgs(args map[string]any) []string {
	var out []string
	seen := make(map[string]bool)
	// Use the policy's project root to resolve relative paths, so that
	// "pkg/tools/internal.go" resolves against the project directory rather
	// than the Go process CWD (which may differ).
	root := p.root
	consider := func(raw string) {
		abs := pathscope.NormalizePathInRoot(raw, root)
		if abs == "" || seen[abs] {
			return
		}
		seen[abs] = true
		out = append(out, abs)
	}
	for _, k := range pathArgKeys {
		if v, ok := args[k]; ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				consider(s)
			}
		}
	}
	for _, k := range pathArgSliceKeys {
		if v, ok := args[k]; ok {
			switch vs := v.(type) {
			case []string:
				for _, s := range vs {
					if strings.TrimSpace(s) != "" {
						consider(s)
					}
				}
			case []any:
				for _, item := range vs {
					if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
						consider(s)
					}
				}
			}
		}
	}
	// Free-form command args (shell_command "command"): the paths are not a
	// discrete arg but embedded in the command text. Extract path-shaped
	// operands from known filesystem commands and run them through the SAME
	// containment check. This closes the shell_command bypass where a command
	// like `cat ~/Downloads/x` or `ls /` reads outside the project root
	// without ever tripping the folder-access gate. See
	// pathscope.ExtractCommandPaths for the command-aware extraction contract.
	for _, k := range commandArgKeys {
		if v, ok := args[k]; ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				for _, tok := range pathscope.ExtractCommandPaths(s) {
					consider(tok)
				}
			}
		}
	}
	return out
}

// ConsumePathGrants consumes any one-shot ("Allow") path grant that authorizes a
// path in the tool call. Call this exactly once, at execution time, when the
// folder gate has decided the call may proceed (OutOfScopePaths returned empty).
// Without this, a one-shot path grant issued for an "Allow" decision would never
// be consumed and would silently persist for the rest of the session — turning
// "Allow" into "Always Allow". Paths covered by the project root or a session
// grant are unaffected (there is no one-shot grant to consume for them).
func (p *SessionAuthPolicy) ConsumePathGrants(args map[string]any) {
	if p == nil || args == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, abs := range p.resolvedPathArgs(args) {
		if p.pathOnceGrants[abs] {
			delete(p.pathOnceGrants, abs)
		}
	}
}

// pathAllowedNoConsume mirrors PathAllowed but never consumes a one-shot grant.
// Caller must hold p.mu.
func (p *SessionAuthPolicy) pathAllowedNoConsume(abs string) bool {
	if p.root == "" || p.folderSettings.AllFolders {
		return true
	}
	// Well-known harmless special paths (e.g. /dev/null) are always allowed.
	if pathscope.IsAlwaysAllowedPath(abs) {
		return true
	}
	if pathWithin(p.root, abs) {
		return true
	}
	for _, ar := range p.allowedRoots {
		if pathWithin(ar, abs) {
			return true
		}
	}
	for granted := range p.pathSessionGrants {
		if pathWithin(granted, abs) {
			return true
		}
	}
	return p.pathOnceGrants[abs]
}

// --- path helpers (delegated to pkg/pathscope, the single source of truth) ---

func normalizeDir(dir string) string   { return pathscope.NormalizeDir(dir) }
func normalizePath(path string) string { return pathscope.NormalizePath(path) }
func dirOf(path string) string         { return pathscope.DirOf(path) }
func pathWithin(root, candidate string) bool {
	return pathscope.PathWithin(root, candidate)
}

// AuthorizationDeniedMessage is returned as a tool result when the user denies
// authorization. Like PlanModeBlockedMessage it returns a result (not an error)
// so the model self-corrects instead of aborting the turn.
func AuthorizationDeniedMessage(toolName string) string {
	return fmt.Sprintf(
		"Denied: the user did not authorize `%s` to run. Do not retry it. "+
			"Explain what you intended and ask the user how they'd like to proceed.",
		toolName,
	)
}

// FolderAccessDeniedMessage is returned when the user denies access to an
// out-of-project path.
func FolderAccessDeniedMessage(toolName string, paths []string) string {
	return fmt.Sprintf(
		"Denied: the user did not authorize `%s` to access %s (outside the project directory). "+
			"Stay within the project folder, or ask the user to grant access.",
		toolName, strings.Join(paths, ", "),
	)
}

// Canonical authorization option labels. These are the exact strings the TUI
// sends back as the user's decision (the next user message), so the labels are
// the contract between the approval overlay and ApplyAuthorizationDecision.
//
// The prompt presents three options — Allow / Always Allow / Deny — regardless
// of kind. "Allow" is a one-shot grant; "Always Allow" is the broader,
// SESSION-scoped grant (all not-whitelisted tools for the rest of the session,
// or the directory for the rest of the session). Position 2 ("Always Allow") is
// disambiguated by the pending request's Kind in ApplyAuthorizationDecision.
const (
	// Tool-execution options.
	OptAllowToolOnce = "Allow"
	OptAllowAllTools = "Always Allow"
	// Folder-access options.
	OptAllowPathOnce          = "Allow once"
	OptAllowPathSession       = "Always allow this folder"
	OptAllowAllFoldersSession = "Always allow any folder for this session"
	// Shared deny option.
	OptDeny = "Deny"
)

// ToolApprovalOptions is the ordered option list presented for a tool-execution
// authorization prompt.
func ToolApprovalOptions() []string {
	return []string{OptAllowToolOnce, OptAllowAllTools, OptDeny}
}

// FolderApprovalOptions is the ordered option list presented for a folder-access
// authorization prompt.
func FolderApprovalOptions() []string {
	return []string{OptAllowPathOnce, OptAllowPathSession, OptAllowAllFoldersSession, OptDeny}
}

// AuthorizationDecision is the outcome of applying a user's response to the
// pending authorization request.
type AuthorizationDecision struct {
	Granted bool   // true = proceed with the tool; false = denied
	Kind    string // "tool" or "folder"
	Tool    string // tool the decision applied to
}

// ApplyAuthorizationDecision interprets the user's response text against the
// pending request and records the appropriate grant on the policy. It returns
// the decision so the caller can drive the retry (granted) or return a denial
// message (not granted). The pending request is cleared. If there is no pending
// request, it returns (nil).
func (p *SessionAuthPolicy) ApplyAuthorizationDecision(response string) *AuthorizationDecision {
	d, _ := p.ApplyAuthorizationDecisionWithError(response)
	return d
}

// ApplyAuthorizationDecisionWithError persists consent before permitting a retry.
func (p *SessionAuthPolicy) ApplyAuthorizationDecisionWithError(response string) (*AuthorizationDecision, error) {
	if p == nil {
		return nil, nil
	}
	p.mu.Lock()
	pending := clonePendingAuthorization(p.pending)
	p.pending = nil
	p.mu.Unlock()
	return p.ApplyAuthorizationRequest(pending, response)
}

// ApplyAuthorizationRequest centralizes decisions for an owned main or child request.
func (p *SessionAuthPolicy) ApplyAuthorizationRequest(pending *PendingAuthorization, response string) (*AuthorizationDecision, error) {
	if p == nil || pending == nil {
		return nil, nil
	}
	choice := DecodeAuthorizationChoice(pending, response)
	d := &AuthorizationDecision{Kind: pending.Kind, Tool: pending.Tool}
	switch choice {
	case AuthorizationAllFolders:
		if pending.Kind != "folder" {
			break
		}
		if err := p.SetAllFoldersForSession(true); err != nil {
			return d, err
		}
		d.Granted = true
	case AuthorizationBroad:
		if pending.Kind == "tool" {
			p.GrantAllToolsSession()
			d.Granted = true
		}
		if pending.Kind == "folder" {
			for _, path := range pending.Paths {
				p.GrantPathForSession(path)
			}
			d.Granted = true
		}
	case AuthorizationOnce:
		if pending.Kind == "tool" {
			p.GrantToolOnce(pending.Tool)
			d.Granted = true
		}
		if pending.Kind == "folder" {
			for _, path := range pending.Paths {
				p.GrantPathOnce(path)
			}
			d.Granted = true
		}
	}
	if d.Granted && pending.Kind == "folder" && RequiresToolAuthorization(pending.Tool, false) {
		p.GrantToolOnce(pending.Tool)
	}
	return d, nil
}

type AuthorizationChoice string

const (
	AuthorizationDeny       AuthorizationChoice = "deny"
	AuthorizationOnce       AuthorizationChoice = "once"
	AuthorizationBroad      AuthorizationChoice = "broad"
	AuthorizationAllFolders AuthorizationChoice = "all_folders"
)

func DecodeAuthorizationChoice(req *PendingAuthorization, response string) AuthorizationChoice {
	s := strings.TrimSpace(response)
	if req == nil {
		return AuthorizationDeny
	}
	if len(s) == 1 && s[0] >= '1' && s[0] <= '9' && len(req.Options) > 0 {
		i := int(s[0] - '1')
		if i >= len(req.Options) {
			return AuthorizationDeny
		}
		s = req.Options[i]
	}
	if strings.EqualFold(s, OptAllowAllFoldersSession) {
		if req.Kind == "folder" {
			return AuthorizationAllFolders
		}
		return AuthorizationDeny
	}
	switch normalizeChoice(s) {
	case "once", "yes":
		return AuthorizationOnce
	case "broad2", "all", "session":
		return AuthorizationBroad
	default:
		return AuthorizationDeny
	}
}

// CodeSessionFolderSettings contains only independently persisted folder consent.
// Owner validation belongs to the trusted local launcher, not the model runtime.
type CodeSessionFolderSettings struct {
	AllFolders bool   `json:"all_folders"`
	Generation uint64 `json:"generation"`
}

// ConfigureCodeSessionFolders hydrates trusted state and installs its durable writer.
func (p *SessionAuthPolicy) ConfigureCodeSessionFolders(s CodeSessionFolderSettings, persist func(CodeSessionFolderSettings) error) {
	p.folderSettingsMu.Lock()
	defer p.folderSettingsMu.Unlock()
	p.mu.Lock()
	p.folderSettings = s
	p.persistFolderSettings = persist
	p.mu.Unlock()
}

func (p *SessionAuthPolicy) CodeSessionFolders() CodeSessionFolderSettings {
	if p == nil {
		return CodeSessionFolderSettings{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.folderSettings
}

func (p *SessionAuthPolicy) SetAllFoldersForSession(allow bool) error {
	if p == nil {
		return fmt.Errorf("session folder permissions unavailable")
	}
	p.folderSettingsMu.Lock()
	defer p.folderSettingsMu.Unlock()
	p.mu.Lock()
	next, persist := p.folderSettings, p.persistFolderSettings
	p.mu.Unlock()
	if next.AllFolders == allow {
		return nil
	}
	if persist == nil {
		return fmt.Errorf("cannot save folder permission: reopen a saved Code session and try again")
	}
	next.AllFolders = allow
	next.Generation++
	if err := persist(next); err != nil {
		return fmt.Errorf("cannot save folder permission; permission unchanged: %w", err)
	}
	p.mu.Lock()
	p.folderSettings = next
	p.mu.Unlock()
	return nil
}

// NormalizeAuthChoice is the exported version of normalizeChoice. It maps a
// free-form user response to a canonical decision token: "broad2", "once",
// "yes", or "deny". Used by the TUI backend to determine grant/deny for
// sub-agent authorization responses without duplicating the logic.
func NormalizeAuthChoice(response string) string {
	return normalizeChoice(response)
}

// normalizeChoice maps a free-form user response to a canonical decision token:
// "all", "session", "once", "yes", or "deny". Accepts the exact option labels
// (Allow / Always Allow / Deny), numeric picks (1/2/3), and short y/n forms so
// the flow is robust to how the TUI or a human submits the response.
//
// "Always Allow" is the broader grant (position 2); it maps to "broad2" so the
// caller's pending Kind decides whether it becomes all-this-iteration (tool) or
// for-session (folder). Order matters: the "always"/"all"/"session" broad forms
// are checked before the plain "allow"/"once" one-shot forms.
func normalizeChoice(response string) string {
	s := strings.ToLower(strings.TrimSpace(response))
	switch s {
	case "3", "n", "no", "deny", "reject", "esc":
		return "deny"
	case "2":
		// Position 2 is the "broader" grant in both prompts (all-iteration for
		// tools, for-session for folders). The caller's pending Kind
		// disambiguates which grant is recorded.
		return "broad2"
	case "1", "y", "yes", "approve":
		return "once"
	}
	// Decode only known labels and legacy aliases. Free-form model text is never consent.
	switch s {
	case "always allow", "always allow this folder", "allow all", "allow all this iteration", "allow for session", "allow for this session", "all", "session":
		return "broad2"
	case "allow", "allow once", "once":
		return "once"
	}
	return "deny"
}
