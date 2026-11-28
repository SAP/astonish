package api

import "testing"

// Keep source intake in the named conversational acceptance gate even though its
// source is an ordinary Chat/job/template rather than an existing Bot run.
func TestBotConversationalControlHTTPAcceptance(t *testing.T) {
	t.Run("source-proposals", TestBotSourceIntakeProposalHTTPAcceptance)
	t.Run("adaptive-source", TestBotSourceIntakeAdaptiveHTTP)
	t.Run("template-source", TestBotSourceIntakeTemplateHTTP)
}
