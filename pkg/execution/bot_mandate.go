package execution

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"slices"
	"strings"
	"time"
)

// BotRuntimeMode distinguishes saved legacy limits from explicit progress policy.
type BotRuntimeMode string

const (
	BotRuntimeModeLegacy          BotRuntimeMode = "legacy"
	BotRuntimeModeProgressV1      BotRuntimeMode = "progress_v1"
	BotRuntimePolicySchemaVersion                = 1
	BotRuntimePolicyMaxBytes                     = 1024
)

// BotRuntimePolicy is optional. Nil preserves legacy semantics. A present policy
// must be complete: decoding or validation never supplies defaults. Cadence is a
// soft evaluation boundary, not a kill deadline. Nil MaxActiveSeconds explicitly
// means no whole-assignment active-duration cap, never unlimited spending.
type BotRuntimePolicy struct {
	SchemaVersion             int            `json:"schema_version" yaml:"schema_version"`
	Mode                      BotRuntimeMode `json:"mode" yaml:"mode"`
	EvaluationIntervalSeconds int64          `json:"evaluation_interval_seconds,omitempty" yaml:"evaluation_interval_seconds,omitempty"`
	MaxActiveSeconds          *int64         `json:"max_active_seconds,omitempty" yaml:"max_active_seconds,omitempty"`
	ModelCallTimeoutSeconds   int64          `json:"model_call_timeout_seconds,omitempty" yaml:"model_call_timeout_seconds,omitempty"`
	ToolCallTimeoutSeconds    int64          `json:"tool_call_timeout_seconds,omitempty" yaml:"tool_call_timeout_seconds,omitempty"`
}

// NewBotProgressRuntimePolicy is only for explicitly authorized NEW configuration.
func NewBotProgressRuntimePolicy() *BotRuntimePolicy {
	return &BotRuntimePolicy{SchemaVersion: BotRuntimePolicySchemaVersion, Mode: BotRuntimeModeProgressV1, EvaluationIntervalSeconds: 300, ModelCallTimeoutSeconds: 120, ToolCallTimeoutSeconds: 120}
}
func (p *BotRuntimePolicy) EffectiveMode() BotRuntimeMode {
	if p == nil {
		return BotRuntimeModeLegacy
	}
	return p.Mode
}
func (p *BotRuntimePolicy) Clone() *BotRuntimePolicy {
	if p == nil {
		return nil
	}
	out := *p
	if p.MaxActiveSeconds != nil {
		v := *p.MaxActiveSeconds
		out.MaxActiveSeconds = &v
	}
	return &out
}
func (p *BotRuntimePolicy) Validate() error {
	if p == nil {
		return nil
	}
	if p.SchemaVersion != BotRuntimePolicySchemaVersion {
		return errors.New("unknown bot runtime policy version")
	}
	switch p.Mode {
	case BotRuntimeModeLegacy:
		if p.EvaluationIntervalSeconds != 0 || p.MaxActiveSeconds != nil || p.ModelCallTimeoutSeconds != 0 || p.ToolCallTimeoutSeconds != 0 {
			return errors.New("legacy runtime policy contains progress limits")
		}
	case BotRuntimeModeProgressV1:
		values := []int64{p.EvaluationIntervalSeconds, p.ModelCallTimeoutSeconds, p.ToolCallTimeoutSeconds}
		if p.MaxActiveSeconds != nil {
			values = append(values, *p.MaxActiveSeconds)
		}
		for _, value := range values {
			if value <= 0 || value > int64((1<<63-1)/time.Second) {
				return errors.New("runtime policy seconds must be positive and duration-safe")
			}
		}
	default:
		return errors.New("unknown bot runtime mode")
	}
	data, err := json.Marshal(p)
	if err != nil || len(data) > BotRuntimePolicyMaxBytes {
		return errors.New("runtime policy exceeds serialized limit")
	}
	return nil
}

// ValidateWithin checks runtime authority only. Nil/legacy ceilings never opt a
// request into progress. An interval ceiling is a maximum time between reviews.
func (p *BotRuntimePolicy) ValidateWithin(ceiling *BotRuntimePolicy) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if err := ceiling.Validate(); err != nil {
		return err
	}
	if p.EffectiveMode() == BotRuntimeModeLegacy {
		return nil
	}
	if ceiling.EffectiveMode() != BotRuntimeModeProgressV1 {
		return errors.New("progress runtime not allowed by trusted policy")
	}
	if p.EvaluationIntervalSeconds > ceiling.EvaluationIntervalSeconds || p.ModelCallTimeoutSeconds > ceiling.ModelCallTimeoutSeconds || p.ToolCallTimeoutSeconds > ceiling.ToolCallTimeoutSeconds {
		return errors.New("runtime policy exceeds operator ceiling")
	}
	if ceiling.MaxActiveSeconds != nil && (p.MaxActiveSeconds == nil || *p.MaxActiveSeconds > *ceiling.MaxActiveSeconds) {
		return errors.New("runtime policy exceeds active duration ceiling")
	}
	return nil
}

// ValidateWithinRuntimePolicy replaces ONLY the legacy duration comparison when
// both sides explicitly opt into progress. All finite fields still validate;
// cumulative and per-run resource limits retain their original comparisons.
func (l BotMandateLimits) ValidateWithinRuntimePolicy(c BotMandateLimits, requested, allowed *BotRuntimePolicy) error {
	if err := requested.ValidateWithin(allowed); err != nil {
		return err
	}
	if err := l.Validate(); err != nil {
		return err
	}
	if err := c.Validate(); err != nil {
		return err
	}
	if requested.EffectiveMode() == BotRuntimeModeProgressV1 {
		l.MaxRuntimeSeconds = c.MaxRuntimeSeconds
	}
	return l.ValidateWithin(c)
}

// BotMandateRef pins the complete tenant, owner, bot and immutable revision.
// Possessing a reference is not authority.
type BotMandateRef struct {
	ID       uuid.UUID `json:"id"`
	BotID    uuid.UUID `json:"bot_id"`
	OrgSlug  string    `json:"org_slug"`
	TeamSlug string    `json:"team_slug"`
	OwnerID  string    `json:"owner_id"`
	Revision int64     `json:"revision"`
}

func (r BotMandateRef) Validate() error {
	if r.ID == uuid.Nil || r.BotID == uuid.Nil || strings.TrimSpace(r.OrgSlug) == "" || strings.TrimSpace(r.TeamSlug) == "" || strings.TrimSpace(r.OwnerID) == "" || r.Revision < 1 {
		return errors.New("invalid mandate reference")
	}
	return nil
}

type BotMandatePurpose string

const (
	BotMandatePurposeSchedule  BotMandatePurpose = "schedule"
	BotMandatePurposeTask      BotMandatePurpose = "task"
	BotMandatePurposeOperation BotMandatePurpose = "operation"
)

func (p BotMandatePurpose) Valid() bool {
	return p == BotMandatePurposeSchedule || p == BotMandatePurposeTask || p == BotMandatePurposeOperation
}

// Resources are exact resource identifiers; wildcards never grant access.
type BotActionResourceConstraint struct {
	Action    Capability `json:"action"`
	Resources []string   `json:"resources"`
}

// All limits are finite; zero never means unlimited. Spend is integer micros.
type BotMandateLimits struct {
	MinScheduleIntervalSeconds int64 `json:"min_schedule_interval_seconds" yaml:"min_schedule_interval_seconds"`
	MaxSchedules               int64 `json:"max_schedules" yaml:"max_schedules"`
	MaxDailyRuns               int64 `json:"max_daily_runs" yaml:"max_daily_runs"`
	MaxConcurrentRuns          int64 `json:"max_concurrent_runs" yaml:"max_concurrent_runs"`
	MaxRuntimeSeconds          int64 `json:"max_runtime_seconds" yaml:"max_runtime_seconds"`
	MaxToolCalls               int64 `json:"max_tool_calls" yaml:"max_tool_calls"`
	MaxTokens                  int64 `json:"max_tokens" yaml:"max_tokens"`
	MaxTotalToolCalls          int64 `json:"max_total_tool_calls" yaml:"max_total_tool_calls"`
	MaxTotalTokens             int64 `json:"max_total_tokens" yaml:"max_total_tokens"`
	MaxSpendMicros             int64 `json:"max_spend_micros" yaml:"max_spend_micros"`
	AllowExternalDelivery      bool  `json:"allow_external_delivery" yaml:"allow_external_delivery"`
	AllowDestructiveActions    bool  `json:"allow_destructive_actions" yaml:"allow_destructive_actions"`
}

func (l BotMandateLimits) Validate() error {
	for _, v := range []int64{l.MinScheduleIntervalSeconds, l.MaxSchedules, l.MaxDailyRuns, l.MaxConcurrentRuns, l.MaxRuntimeSeconds, l.MaxToolCalls, l.MaxTokens, l.MaxTotalToolCalls, l.MaxTotalTokens} {
		if v <= 0 {
			return errors.New("mandate limits must be positive and finite")
		}
	}
	if l.MaxSpendMicros < 0 {
		return errors.New("negative spend limit")
	}
	if l.MaxToolCalls > l.MaxTotalToolCalls || l.MaxTokens > l.MaxTotalTokens {
		return errors.New("per-run limit exceeds aggregate")
	}
	return nil
}
func (l BotMandateLimits) ValidateWithin(c BotMandateLimits) error {
	if err := l.Validate(); err != nil {
		return err
	}
	if err := c.Validate(); err != nil {
		return err
	}
	a := []int64{l.MaxSchedules, l.MaxDailyRuns, l.MaxConcurrentRuns, l.MaxRuntimeSeconds, l.MaxToolCalls, l.MaxTokens, l.MaxTotalToolCalls, l.MaxTotalTokens, l.MaxSpendMicros}
	b := []int64{c.MaxSchedules, c.MaxDailyRuns, c.MaxConcurrentRuns, c.MaxRuntimeSeconds, c.MaxToolCalls, c.MaxTokens, c.MaxTotalToolCalls, c.MaxTotalTokens, c.MaxSpendMicros}
	for i := range a {
		if a[i] > b[i] {
			return errors.New("mandate exceeds current operator ceiling")
		}
	}
	if l.MinScheduleIntervalSeconds < c.MinScheduleIntervalSeconds || l.AllowExternalDelivery && !c.AllowExternalDelivery || l.AllowDestructiveActions && !c.AllowDestructiveActions {
		return errors.New("mandate exceeds current operator policy")
	}
	return nil
}

type BotMandate struct {
	RuntimePolicy  *BotRuntimePolicy             `json:"runtime_policy,omitempty"`
	Ref            BotMandateRef                 `json:"ref"`
	Purpose        BotMandatePurpose             `json:"purpose"`
	ExpiresAt      time.Time                     `json:"expires_at"`
	Scopes         []string                      `json:"scopes"`
	Constraints    []BotActionResourceConstraint `json:"constraints"`
	PublicResearch *BotPublicResearchScope       `json:"public_research,omitempty"`
	Limits         BotMandateLimits              `json:"limits"`
}

func (m BotMandate) Clone() BotMandate {
	m.RuntimePolicy = m.RuntimePolicy.Clone()
	if m.PublicResearch != nil {
		v := m.PublicResearch.Clone()
		m.PublicResearch = &v
	}
	m.Scopes = slices.Clone(m.Scopes)
	m.Constraints = slices.Clone(m.Constraints)
	for i := range m.Constraints {
		m.Constraints[i].Resources = slices.Clone(m.Constraints[i].Resources)
	}
	return m
}
func (m BotMandate) Validate(now time.Time) error {
	if err := m.RuntimePolicy.Validate(); err != nil {
		return err
	}
	if err := m.Ref.Validate(); err != nil {
		return err
	}
	if !m.Purpose.Valid() || !m.ExpiresAt.After(now) || len(m.Scopes) == 0 || len(m.Constraints) == 0 {
		return errors.New("invalid mandate purpose, expiry or constraints")
	}
	if m.PublicResearch != nil {
		if err := m.PublicResearch.Validate(); err != nil {
			return err
		}
	}
	if err := m.Limits.Validate(); err != nil {
		return err
	}
	for _, s := range m.Scopes {
		if !validMandateCapability(Capability(s)) {
			return errors.New("unknown mandate scope")
		}
	}
	for _, c := range m.Constraints {
		if !validMandateCapability(c.Action) || !slices.Contains(m.Scopes, string(c.Action)) || len(c.Resources) == 0 {
			return errors.New("invalid mandate constraint")
		}
		for _, r := range c.Resources {
			if strings.TrimSpace(r) == "" || strings.Contains(r, "*") {
				return errors.New("exact mandate resource required")
			}
		}
	}
	return nil
}
func validMandateCapability(c Capability) bool {
	switch c {
	case CapabilityChat, CapabilityA2A, CapabilityMemoryRead, CapabilityMemoryWrite, CapabilityIntegration, CapabilityToolExecute:
		return true
	}
	return false
}

// BotMandateVerifier is injected ONLY by the trusted composition root. Its
// implementation must read current durable revision/revocation, membership,
// source provenance and operator ceilings. Model output is not a verifier.
type BotMandateVerifier interface {
	VerifyBotMandate(context.Context, BotMandateRef, BotMandatePurpose) (BotMandate, error)
}

// VerifiedBotMandateAuthorization has no exported state and cannot be restored
// from JSON, principal fields, a caller ceiling or a turn registry.
type VerifiedBotMandateAuthorization struct{ mandate *BotMandate }

func VerifyBotMandateAuthorization(ctx context.Context, v BotMandateVerifier, r BotMandateRef, p BotMandatePurpose) (VerifiedBotMandateAuthorization, error) {
	if ctx == nil || v == nil {
		return VerifiedBotMandateAuthorization{}, errors.New("trusted verifier required")
	}
	if err := ctx.Err(); err != nil {
		return VerifiedBotMandateAuthorization{}, err
	}
	if err := r.Validate(); err != nil {
		return VerifiedBotMandateAuthorization{}, err
	}
	if !p.Valid() {
		return VerifiedBotMandateAuthorization{}, errors.New("invalid purpose")
	}
	m, err := v.VerifyBotMandate(ctx, r, p)
	if err != nil {
		return VerifiedBotMandateAuthorization{}, err
	}
	if m.Ref != r || m.Purpose != p {
		return VerifiedBotMandateAuthorization{}, errors.New("mandate binding mismatch")
	}
	if err = m.Validate(time.Now()); err != nil {
		return VerifiedBotMandateAuthorization{}, err
	}
	if err = ctx.Err(); err != nil {
		return VerifiedBotMandateAuthorization{}, err
	}
	m = m.Clone()
	return VerifiedBotMandateAuthorization{mandate: &m}, nil
}
func (a VerifiedBotMandateAuthorization) Ref() BotMandateRef {
	if a.mandate == nil {
		return BotMandateRef{}
	}
	return a.mandate.Ref
}
func (a VerifiedBotMandateAuthorization) Snapshot() BotMandate {
	if a.mandate == nil {
		return BotMandate{}
	}
	return a.mandate.Clone()
}
func (a VerifiedBotMandateAuthorization) Principal(s Surface) Principal {
	if a.mandate == nil {
		return Principal{}
	}
	m := a.mandate
	return Principal{Kind: PrincipalKindUser, Authentication: AuthMethodBotMandate, Surface: s, Subject: m.Ref.OwnerID, Actor: m.Ref.BotID.String(), OrgSlug: m.Ref.OrgSlug, TeamSlug: m.Ref.TeamSlug, Authenticated: true, Scopes: slices.Clone(m.Scopes), mandate: a.mandate}
}
func (a VerifiedBotMandateAuthorization) Authorize(action Capability, resource string) error {
	if a.mandate == nil || a.mandate.Validate(time.Now()) != nil {
		return errors.New("verified mandate required")
	}
	for _, c := range a.mandate.Constraints {
		if c.Action == action && slices.Contains(c.Resources, resource) {
			return nil
		}
	}
	return errors.New("mandate action/resource denied")
}
func (p Principal) validateMandate() error {
	m := p.mandate
	if m == nil || m.Validate(time.Now()) != nil || p.Kind != PrincipalKindUser || p.Subject != m.Ref.OwnerID || p.Actor != m.Ref.BotID.String() || p.OrgSlug != m.Ref.OrgSlug || p.TeamSlug != m.Ref.TeamSlug || len(p.Roles) != 0 || !slices.Equal(p.Scopes, m.Scopes) {
		return errors.New("unverified or modified mandate principal")
	}
	return nil
}
