package execution

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/google/uuid"
)

// AutonomyLevel is a saved setting, not authority inferred from conversation.
type AutonomyLevel string

const (
	AutonomyAsk        AutonomyLevel = "ask"
	AutonomySupervised AutonomyLevel = "supervised"
	AutonomyReversible AutonomyLevel = "autonomous-reversible"
	AutonomyScoped     AutonomyLevel = "autonomous-scoped"
)

// BotRunIdentity is installed by authenticated runtime adapters, never tool input.
// ActorID is the effective subject (personal) or service client (team).
type BotRunIdentity struct {
	BotID    uuid.UUID `json:"bot_id"`
	RunID    uuid.UUID `json:"run_id"`
	Version  int64     `json:"version"`
	ActorID  string    `json:"actor_id"`
	OrgSlug  string    `json:"org_slug"`
	TeamSlug string    `json:"team_slug"`
}

func (i BotRunIdentity) Validate() error {
	if i.BotID == uuid.Nil || i.RunID == uuid.Nil || i.Version < 1 || i.ActorID == "" || i.OrgSlug == "" || i.TeamSlug == "" {
		return errors.New("incomplete bot run identity")
	}
	return nil
}

type ActionEffect string

const (
	EffectUnknown         ActionEffect = "unknown"
	EffectRead            ActionEffect = "read"
	EffectReversibleWrite ActionEffect = "reversible_write"
	EffectSend            ActionEffect = "send"
	EffectSpend           ActionEffect = "spend"
	EffectDelete          ActionEffect = "delete"
	EffectPublish         ActionEffect = "publish"
	EffectSecurity        ActionEffect = "security"
	EffectSchedule        ActionEffect = "schedule"
)

// ActionDescriptor must be constructed by a trusted adapter. Tool names, HTTP
// methods and model-supplied claims are not evidence of an action's effects.
// Arguments contain credential REFERENCES only. Resolve secrets after approval.
// ConstraintsVerified means the adapter has mechanically checked effective paths,
// destination, effect and price against the policy, including symlink resolution.
type ActionDescriptor struct {
	Tool      string          `json:"tool"`
	Effect    ActionEffect    `json:"effect"`
	Arguments json.RawMessage `json:"arguments"`
	// TargetFingerprint is supplied only by trusted adapters over symbolic config.
	TargetFingerprint string `json:"target_fingerprint,omitempty"`
	// TargetSnapshot is trusted non-secret adapter evidence for resumable exact actions.
	TargetSnapshot      json.RawMessage `json:"target_snapshot,omitempty"`
	Recipient           string          `json:"recipient,omitempty"`
	Merchant            string          `json:"merchant,omitempty"`
	Currency            string          `json:"currency,omitempty"`
	SpendMinor          int64           `json:"spend_minor,omitempty"`
	ConstraintsVerified bool            `json:"-"`
}

// Digest binds exact content and destination with deterministic JSON object-key
// ordering. UseNumber prevents large integer identifiers losing precision.
func (a ActionDescriptor) Digest() (string, error) {
	if a.Tool == "" || !validEffect(a.Effect) || a.SpendMinor < 0 {
		return "", errors.New("invalid action descriptor")
	}
	d := json.NewDecoder(bytes.NewReader(a.Arguments))
	d.UseNumber()
	var value any
	if err := d.Decode(&value); err != nil {
		return "", fmt.Errorf("decode action arguments: %w", err)
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return "", errors.New("action arguments must contain exactly one JSON value")
	}
	object, ok := value.(map[string]any)
	if !ok {
		return "", errors.New("action arguments must be an object")
	}
	canonical, err := json.Marshal(object)
	if err != nil {
		return "", err
	}
	a.Arguments = canonical
	b, err := json.Marshal(a)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
func validEffect(e ActionEffect) bool {
	switch e {
	case EffectUnknown, EffectRead, EffectReversibleWrite, EffectSend, EffectSpend, EffectDelete, EffectPublish, EffectSecurity, EffectSchedule:
		return true
	}
	return false
}

type PolicyDecisionKind string

const (
	PolicyNotApplicable   PolicyDecisionKind = "not_applicable"
	PolicyAllow           PolicyDecisionKind = "allow"
	PolicyDeny            PolicyDecisionKind = "deny"
	PolicyRequestApproval PolicyDecisionKind = "request_approval"
	PolicyConsumeGrant    PolicyDecisionKind = "consume_grant"
)

type PolicyDecision struct {
	Kind         PolicyDecisionKind `json:"kind"`
	Reason       string             `json:"reason"`
	ActionDigest string             `json:"action_digest,omitempty"`
	GrantID      uuid.UUID          `json:"grant_id,omitempty"`
}

// ScopedGrant is a previously authenticated decision bound to one exact action.
// A policy match is NOT execution authority until the repository atomically
// consumes/reserves the grant under a live lease and current revocation state.
// General recurring grants must be materialized into exact-action grants by a
// mechanically constrained adapter; this layer does not infer send/spend scopes.
type ScopedGrant struct {
	ID           uuid.UUID      `json:"id"`
	Identity     BotRunIdentity `json:"identity"`
	ActionDigest string         `json:"action_digest"`
	ExpiresAt    time.Time      `json:"expires_at"`
	Revoked      bool           `json:"revoked"`
}

type RunPolicySpec struct {
	Identity     BotRunIdentity
	Level        AutonomyLevel
	AllowedTools []string
	DeniedTools  []string
	Grants       []ScopedGrant
}

// RunPolicy is immutable after construction: slices are copied and fields private.
// All comparisons use exact executable identities, not display names or prefixes.
type RunPolicy struct {
	identity BotRunIdentity
	level    AutonomyLevel
	allowed  []string
	denied   []string
	grants   []ScopedGrant
	child    bool
}

func NewRunPolicy(s RunPolicySpec) (*RunPolicy, error) {
	if err := s.Identity.Validate(); err != nil {
		return nil, err
	}
	switch s.Level {
	case AutonomyAsk, AutonomySupervised, AutonomyReversible, AutonomyScoped:
	default:
		return nil, errors.New("unknown autonomy level")
	}
	for _, name := range append(slices.Clone(s.AllowedTools), s.DeniedTools...) {
		if name == "" {
			return nil, errors.New("empty tool identity")
		}
	}
	for _, g := range s.Grants {
		digest, err := hex.DecodeString(g.ActionDigest)
		if g.ID == uuid.Nil || g.Identity != s.Identity || g.ExpiresAt.IsZero() || err != nil || len(digest) != sha256.Size {
			return nil, errors.New("invalid scoped grant")
		}
	}
	return &RunPolicy{identity: s.Identity, level: s.Level, allowed: slices.Clone(s.AllowedTools), denied: slices.Clone(s.DeniedTools), grants: slices.Clone(s.Grants)}, nil
}
func (p *RunPolicy) Identity() BotRunIdentity {
	if p == nil {
		return BotRunIdentity{}
	}
	return p.identity
}

// Narrow intersects an internal child allowlist, distinct from delegate_tasks'
// public preferences. Children never inherit action grants or send/spend rights.
func (p *RunPolicy) Narrow(tools []string) (*RunPolicy, error) {
	if p == nil {
		return nil, errors.New("missing parent policy")
	}
	var allowed []string
	for _, tool := range tools {
		if slices.Contains(p.allowed, tool) && !slices.Contains(p.denied, tool) {
			allowed = append(allowed, tool)
		}
	}
	return &RunPolicy{identity: p.identity, level: p.level, allowed: allowed, denied: slices.Clone(p.denied), child: true}, nil
}

type botRunIdentityKey struct{}
type runPolicyKey struct{}

func WithBotRunIdentity(ctx context.Context, identity BotRunIdentity) context.Context {
	return context.WithValue(ctx, botRunIdentityKey{}, identity)
}
func BotRunIdentityFromContext(ctx context.Context) (BotRunIdentity, bool) {
	if ctx == nil {
		return BotRunIdentity{}, false
	}
	i, ok := ctx.Value(botRunIdentityKey{}).(BotRunIdentity)
	return i, ok
}
func WithRunPolicy(ctx context.Context, policy *RunPolicy) context.Context {
	return context.WithValue(ctx, runPolicyKey{}, policy)
}
func RunPolicyFromContext(ctx context.Context) *RunPolicy {
	if ctx == nil {
		return nil
	}
	p, _ := ctx.Value(runPolicyKey{}).(*RunPolicy)
	return p
}

// EvaluateRunPolicy is pure. The execution guard must additionally check current
// lease, revocation, budget and consume a returned grant immediately before use.
func EvaluateRunPolicy(ctx context.Context, action ActionDescriptor, now time.Time) PolicyDecision {
	deny := func(reason string) PolicyDecision { return PolicyDecision{Kind: PolicyDeny, Reason: reason} }
	id, bot := BotRunIdentityFromContext(ctx)
	p := RunPolicyFromContext(ctx)
	if !bot {
		if p != nil {
			return deny("policy without bot identity")
		}
		return PolicyDecision{Kind: PolicyNotApplicable, Reason: "ordinary execution"}
	}
	if err := id.Validate(); err != nil {
		return deny("invalid bot identity")
	}
	principal, ok := PrincipalFromContext(ctx)
	if !ok || principal.Validate() != nil || principal.Kind == PrincipalKindLocal || principal.OrgSlug != id.OrgSlug || principal.TeamSlug != id.TeamSlug {
		return deny("missing or mismatched authenticated principal")
	}
	actor := principal.Subject
	if principal.Kind == PrincipalKindService {
		actor = principal.ClientID
	}
	if actor != id.ActorID {
		return deny("mismatched execution actor")
	}
	if p == nil || p.identity != id {
		return deny("missing or mismatched run policy")
	}
	if slices.Contains(p.denied, action.Tool) {
		return deny("tool outside policy: explicitly denied")
	}
	if !slices.Contains(p.allowed, action.Tool) {
		return deny(fmt.Sprintf("tool outside policy: %q is not in allowed_tools; needs a separately reviewed configuration proposal in the Bot conversation before a new task proposal. No exact-action approval was created; do not replay an applied task", action.Tool))
	}
	digest, err := action.Digest()
	if err != nil {
		return deny("invalid action")
	}
	if p.child && action.Effect != EffectRead && action.Effect != EffectReversibleWrite {
		return deny("child consequential action prohibited")
	}
	for _, grant := range p.grants {
		if grant.Identity == id && !grant.Revoked && grant.ActionDigest == digest && now.Before(grant.ExpiresAt) {
			return PolicyDecision{Kind: PolicyConsumeGrant, Reason: "consume exact-action approval before execution", ActionDigest: digest, GrantID: grant.ID}
		}
	}
	if action.ConstraintsVerified && (action.Effect == EffectRead || action.Effect == EffectReversibleWrite && p.level != AutonomyAsk) {
		return PolicyDecision{Kind: PolicyAllow, Reason: "verified action within policy", ActionDigest: digest}
	}
	return PolicyDecision{Kind: PolicyRequestApproval, Reason: "exact action requires approval", ActionDigest: digest}
}

// PolicyGuard is the mandatory execution boundary, not an interactive UI gate.
// Implementations enforce fresh authorization/fencing and atomic grant consumption.
type PolicyGuard interface {
	Authorize(context.Context, ActionDescriptor) (PolicyDecision, error)
}
type ApprovalRequester interface {
	RequestApproval(context.Context, BotRunIdentity, ActionDescriptor, string) (uuid.UUID, error)
}

type BudgetReservation struct {
	ID           uuid.UUID
	InputTokens  int64
	OutputTokens int64
	CostMicros   *int64
}
type BudgetUsage struct {
	InputTokens  int64
	OutputTokens int64
	CostMicros   *int64
}

// RunBudget is shared across parent, children and all auxiliary model calls.
// Settle must deduplicate cumulative streamed usage by reservation ID.
type RunBudget interface {
	ReserveModel(context.Context, BudgetReservation) (BudgetReservation, error)
	SettleModel(context.Context, uuid.UUID, BudgetUsage) error
	ReserveTool(context.Context, string) error
	Check(context.Context) error
}
