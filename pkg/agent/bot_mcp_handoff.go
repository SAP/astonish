package agent

import "google.golang.org/adk/tool"

// BotBoundMCPInvocation authorizes an invocation and transfers its exact classified
// evidence to the lazy boundary. It does not manufacture authorization evidence
// from an assumed earlier check. Already-authorized dispatchers use the private
// evidence-carrying path instead.
func BotBoundMCPInvocation(ctx tool.Context, identity string, args any) (tool.Context, error) {
	return authorizeBotInvocation(ctx, identity, args)
}
