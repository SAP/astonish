package sandbox

import "context"

// Optional execution capability. Inputs identify a retained immutable revision;
// callers still authorize the run lease, selected registry revision and budget.
// No network, credentials, package installation or host fallback is permitted.
type BotHomeWorkspaceScriptRequest struct {
	TaskPath      string
	PublicationID string
	ScriptPath    string
	Digest        string
	BeforeCommit  BotHomeWorkspaceCommitBarrier `json:"-"`
}
type BotHomeWorkspaceScriptResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
}
type BotHomeWorkspaceScriptBackend interface {
	RunBotHomeWorkspaceScript(context.Context, *BotHomeBinding, BotHomeWorkspaceScriptRequest) (*BotHomeWorkspaceScriptResult, error)
}
