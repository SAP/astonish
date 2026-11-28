package store

import (
	"github.com/SAP/astonish/pkg/execution"
	"testing"
	"time"
)

func TestBotMandateProvenanceAndCopies(t *testing.T) {
	p := BotMandateProvenance{Source: BotMandateSource("model"), ActorID: "u", RequestID: "r", RecordedAt: time.Now()}
	if p.Validate() == nil {
		t.Fatal("model provenance accepted")
	}
	p.Source = BotMandateSourceAuthenticatedUser
	if p.Validate() != nil {
		t.Fatal("protected adapter provenance rejected")
	}
	now := time.Now()
	r := BotMandateRecord{Provenance: p, RevokedAt: &now, Mandate: BotMandate{Scopes: []string{"chat"}, Constraints: []execution.BotActionResourceConstraint{{Action: execution.CapabilityChat, Resources: []string{"bot:self"}}}}}
	c := r.Clone()
	c.Mandate.Scopes[0] = "tool:execute"
	c.Mandate.Constraints[0].Resources[0] = "other"
	*c.RevokedAt = now.Add(time.Hour)
	if r.Mandate.Scopes[0] != "chat" || r.Mandate.Constraints[0].Resources[0] != "bot:self" || !r.RevokedAt.Equal(now) {
		t.Fatal("record aliases mutable source")
	}
}
