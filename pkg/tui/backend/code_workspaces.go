package backend

import "context"

// WorkspaceMode selects local checkout isolation; it is not a sandbox policy.
type WorkspaceMode string

const (
	WorkspaceCurrent     WorkspaceMode = "current"
	WorkspaceIndependent WorkspaceMode = "independent"
	WorkspaceAsk         WorkspaceMode = "ask"
)

type WorkspaceCopyMode string

const (
	WorkspaceCommitted   WorkspaceCopyMode = "committed"
	WorkspaceCurrentWork WorkspaceCopyMode = "current-work"
)

// WorkspaceLaunchRequest is prepared by the local backend, then handed to a
// fresh Code process only after the terminal and runtime resources are closed.
type WorkspaceLaunchRequest struct {
	Mode        WorkspaceMode
	CopyMode    WorkspaceCopyMode
	BaseRef     string
	WorkingDir  string
	ProjectRoot string
	WorkspaceID string
	SessionID   string
	Label       string
}
type WorkspaceSummary struct {
	ID          string
	ProjectRoot string
	WorkingDir  string
	Mode        WorkspaceMode
	State       string
	Active      bool
	Retained    bool
	Label       string
}
type WorkspaceReview struct {
	WorkspaceID string
	Digest      string
	Summary     string
	Files       []string
	Conflicts   []string
}
type WorkspaceHandoffError struct{ Request WorkspaceLaunchRequest }

func (e *WorkspaceHandoffError) Error() string {
	return "opening Code in workspace " + e.Request.WorkingDir
}

// CodeWorkspaceBackend is optional; remote backends do not implement it.
// PrepareWorkspaceLaunch creates/validates a destination, but never changes CWD.
// A caller exits tui.Run with WorkspaceHandoffError after successful preparation.
type CodeWorkspaceBackend interface {
	ListCodeWorkspaces(context.Context) ([]WorkspaceSummary, error)
	PrepareWorkspaceLaunch(context.Context, WorkspaceLaunchRequest) (WorkspaceLaunchRequest, error)
}

// Management is deliberately optional and independent of launch support.
type CodeWorkspaceManagementBackend interface {
	ReviewCodeWorkspace(context.Context, string) (WorkspaceReview, error)
	ApplyCodeWorkspace(context.Context, WorkspaceReview) error
	CreateCodeWorkspaceBranch(context.Context, string, string) error
	RemoveCodeWorkspace(context.Context, string) error
}
