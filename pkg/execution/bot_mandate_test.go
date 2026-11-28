package execution

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestBotRuntimePolicyValidationAndLegacy(t *testing.T) {
	var absent *BotRuntimePolicy
	if absent.Validate() != nil || absent.EffectiveMode() != BotRuntimeModeLegacy || absent.Clone() != nil {
		t.Fatal("absent policy changed legacy semantics")
	}
	p := NewBotProgressRuntimePolicy()
	if p.Validate() != nil || p.EvaluationIntervalSeconds != 300 || p.ModelCallTimeoutSeconds != 0 || p.GenerationHealth == nil || p.GenerationHealth.IdleTimeoutSeconds != 120 || p.GenerationHealth.CheckIntervalSeconds != 10 || p.ToolCallTimeoutSeconds != 120 || p.MaxActiveSeconds != nil {
		t.Fatal("new policy defaults")
	}
	for name, mutate := range map[string]func(*BotRuntimePolicy){
		"unknown mode":     func(p *BotRuntimePolicy) { p.Mode = "unknown" },
		"unknown version":  func(p *BotRuntimePolicy) { p.SchemaVersion++ },
		"zero cadence":     func(p *BotRuntimePolicy) { p.EvaluationIntervalSeconds = 0 },
		"negative timeout": func(p *BotRuntimePolicy) { p.ModelCallTimeoutSeconds = -1 },
		"overflow":         func(p *BotRuntimePolicy) { p.ToolCallTimeoutSeconds = 1<<63 - 1 },
		"zero hard cap":    func(p *BotRuntimePolicy) { p.MaxActiveSeconds = new(int64) },
		"legacy mixture":   func(p *BotRuntimePolicy) { p.Mode = BotRuntimeModeLegacy },
	} {
		t.Run(name, func(t *testing.T) {
			q := p.Clone()
			mutate(q)
			if q.Validate() == nil {
				t.Fatal("malformed policy accepted")
			}
		})
	}
	var old BotMandate
	if err := json.Unmarshal([]byte(`{"limits":{"max_runtime_seconds":300}}`), &old); err != nil || old.RuntimePolicy != nil {
		t.Fatal("old JSON upgraded")
	}
	var zero BotRuntimePolicy
	if zero.Validate() == nil {
		t.Fatal("present zero policy silently defaulted")
	}
}

func TestBotRuntimePolicyCeilingAndClone(t *testing.T) {
	m := testMandate()
	ceiling := m.Limits
	m.Limits.MaxRuntimeSeconds = 900
	if m.Limits.ValidateWithin(ceiling) != nil {
		t.Fatal("historical runtime metadata enforced")
	}
	p := NewBotProgressRuntimePolicy()
	if m.Limits.ValidateWithinRuntimePolicy(ceiling, p, nil) == nil {
		t.Fatal("progress allowed without trusted opt-in")
	}
	if err := m.Limits.ValidateWithinRuntimePolicy(ceiling, p, p); err != nil {
		t.Fatal(err)
	}
	m.Limits.MaxTokens++
	if m.Limits.ValidateWithinRuntimePolicy(ceiling, p, p) != nil {
		t.Fatal("historical token metadata enforced")
	}
	m.Limits = ceiling
	cap := int64(600)
	bounded := p.Clone()
	bounded.MaxActiveSeconds = &cap
	if p.ValidateWithin(bounded) != nil {
		t.Fatal("historical active duration metadata enforced")
	}
	if bounded.ValidateWithin(p) != nil {
		t.Fatal("bounded request denied")
	}
	m.RuntimePolicy = bounded
	copy := m.Clone()
	*copy.RuntimePolicy.MaxActiveSeconds = 1
	if *m.RuntimePolicy.MaxActiveSeconds != 600 {
		t.Fatal("policy pointer aliases clone")
	}
	a, err := VerifyBotMandateAuthorization(context.Background(), mandateVerifier{m}, m.Ref, m.Purpose)
	if err != nil {
		t.Fatal(err)
	}
	*m.RuntimePolicy.MaxActiveSeconds = 2
	snapshot := a.Snapshot()
	*snapshot.RuntimePolicy.MaxActiveSeconds = 3
	if *a.Snapshot().RuntimePolicy.MaxActiveSeconds != 600 {
		t.Fatal("sealed authority policy aliased")
	}
}

func TestBotMandateRawPrincipalRejected(t *testing.T) {
	p := Principal{Kind: PrincipalKindUser, Authentication: AuthMethodBotMandate, Surface: SurfaceStudio, Subject: "u", OrgSlug: "o", TeamSlug: "t", Authenticated: true, Scopes: []string{"chat"}}
	if p.Validate() == nil {
		t.Fatal("forged mandate principal accepted")
	}
	if (CapabilityAuthorizer{}).Authorize(p, CapabilityChat) == nil {
		t.Fatal("raw principal authorized")
	}
}
func testMandate() BotMandate {
	return BotMandate{Ref: BotMandateRef{ID: uuid.New(), BotID: uuid.New(), OrgSlug: "o", TeamSlug: "t", OwnerID: "u", Revision: 1}, Purpose: BotMandatePurposeSchedule, ExpiresAt: time.Now().Add(time.Hour), Scopes: []string{"chat"}, Constraints: []BotActionResourceConstraint{{Action: CapabilityChat, Resources: []string{"bot:self"}}}, Limits: BotMandateLimits{MinScheduleIntervalSeconds: 60, MaxSchedules: 8, MaxDailyRuns: 1440, MaxConcurrentRuns: 1, MaxRuntimeSeconds: 300, MaxToolCalls: 100, MaxTokens: 100000, MaxTotalToolCalls: 144000, MaxTotalTokens: 144000000}}
}

type mandateVerifier struct{ m BotMandate }

func (v mandateVerifier) VerifyBotMandate(context.Context, BotMandateRef, BotMandatePurpose) (BotMandate, error) {
	return v.m, nil
}
func TestBotMandateVerifiedScopeAndCopy(t *testing.T) {
	m := testMandate()
	a, err := VerifyBotMandateAuthorization(context.Background(), mandateVerifier{m}, m.Ref, m.Purpose)
	if err != nil {
		t.Fatal(err)
	}
	m.Scopes[0] = "tool:execute"
	m.Constraints[0].Resources[0] = "*"
	p := a.Principal(SurfaceStudio)
	if err = (CapabilityAuthorizer{}).Authorize(p, CapabilityChat); err != nil {
		t.Fatal(err)
	}
	if (CapabilityAuthorizer{}).Authorize(p, CapabilityToolExecute) == nil {
		t.Fatal("scope bypass")
	}
	p.Subject = "other"
	if p.Validate() == nil {
		t.Fatal("identity changed")
	}
	// Persist the authorization as part of a normal JSON document. Its sealed
	// state must neither leak into that document nor survive restoration.
	type persisted struct {
		Authorization VerifiedBotMandateAuthorization `json:"authorization"`
	}
	b, err := json.Marshal(persisted{Authorization: a})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"authorization":{}}` {
		t.Fatalf("JSON leaked sealed authorization: %s", b)
	}
	var restored persisted
	if err = json.Unmarshal(b, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Authorization.Principal(SurfaceStudio).Validate() == nil {
		t.Fatal("JSON restored authority")
	}
}
func TestBotDelegationAdmissionLimitsAndHistoricalTokens(t *testing.T) {
	m := testMandate()
	if m.Validate(time.Now()) != nil {
		t.Fatal("valid mandate")
	}
	m.Limits.MaxTokens = 0
	if m.Validate(time.Now()) != nil {
		t.Fatal("zero historical tokens rejected")
	}
}
func TestBotMandateBindingsAndResources(t *testing.T) {
	m := testMandate()
	for name, change := range map[string]func(*BotMandate){"tenant": func(x *BotMandate) { x.Ref.TeamSlug = "other" }, "revision": func(x *BotMandate) { x.Ref.Revision++ }, "purpose": func(x *BotMandate) { x.Purpose = BotMandatePurposeTask }, "expired": func(x *BotMandate) { x.ExpiresAt = time.Now().Add(-time.Second) }, "wildcard": func(x *BotMandate) { x.Constraints[0].Resources[0] = "*" }, "negative aggregate": func(x *BotMandate) { x.Limits.MaxTotalTokens = -1 }} {
		t.Run(name, func(t *testing.T) {
			bad := m.Clone()
			change(&bad)
			if _, err := VerifyBotMandateAuthorization(context.Background(), mandateVerifier{bad}, m.Ref, m.Purpose); err == nil {
				t.Fatal("invalid grant accepted")
			}
		})
	}
	a, err := VerifyBotMandateAuthorization(context.Background(), mandateVerifier{m}, m.Ref, m.Purpose)
	if err != nil {
		t.Fatal(err)
	}
	if a.Authorize(CapabilityChat, "bot:self") != nil || a.Authorize(CapabilityChat, "other") == nil || a.Authorize(CapabilityToolExecute, "bot:self") == nil {
		t.Fatal("resource boundary")
	}
	snapshot := a.Snapshot()
	snapshot.Constraints[0].Resources[0] = "other"
	if a.Authorize(CapabilityChat, "bot:self") != nil {
		t.Fatal("snapshot aliases grant")
	}
	p := a.Principal(SurfaceStudio)
	p.Authentication = AuthMethodPlatformJWT
	if p.Validate() == nil {
		t.Fatal("auth method bypass")
	}
	ctx, _ := WithPrincipal(context.Background(), a.Principal(SurfaceStudio))
	if _, err := WithAuthenticationEvidence(ctx, AuthenticationEvidence{Method: AuthMethodBotMandate, ExpiresAt: m.ExpiresAt}); err == nil {
		t.Fatal("mandate entered turn authentication")
	}
	c := BotCallerCeiling{Principal: a.Principal(SurfaceStudio)}
	if c.Clone().Principal.Validate() == nil {
		t.Fatal("ceiling retained live grant")
	}
}

func TestBotCallerCeilingDeepCopy(t *testing.T) {
	c := BotCallerCeiling{Principal: Principal{Scopes: []string{"chat"}, Roles: []string{"user"}}}
	d := c.Clone()
	d.Principal.Scopes[0] = "tool:execute"
	if c.Principal.Scopes[0] != "chat" {
		t.Fatal("alias")
	}
}
