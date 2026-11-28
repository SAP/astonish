package backend

import "context"

const CodePermissionSessionAllTools CodePermissionScope = "session_all_tools"

// CodeSessionPermissionsBackend changes only local session consent, never CLI or plan overrides.
// An empty session ID may create a saved Code session without starting a model turn.
type CodeSessionPermissionsBackend interface {
	SetCodeSessionPermission(context.Context, string, CodePermissionScope, bool) (CodePermissionsSnapshot, error)
}
