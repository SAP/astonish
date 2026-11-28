package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/google/uuid"
)

// BotPreparedPlanStage is durable coordination, not permission to execute.
type BotPreparedPlanStage string

const (
	BotPlanPrepared         BotPreparedPlanStage = "prepared"
	BotPlanAwaitingApproval BotPreparedPlanStage = "awaiting_approval"
	BotPlanDispatchStarted  BotPreparedPlanStage = "dispatch_started"
	BotPlanSettled          BotPreparedPlanStage = "settled"
	BotPlanPublished        BotPreparedPlanStage = "published"
	BotPlanUncertain        BotPreparedPlanStage = "uncertain"
	BotPlanObsolete         BotPreparedPlanStage = "obsolete"
)

// BotPlanBase is the exact authoritative session snapshot being changed. The
// zero value denotes a session with no plan. It is checked inside the fence.
type BotPlanBase struct {
	PlanID   string `json:"plan_id"`
	Revision int64  `json:"revision"`
	Sequence int64  `json:"sequence"`
}

// BotPreparedPlanLeafKind identifies the independently authorized effect. An
// empty kind is a legacy mirror; it can never be advanced as a verifier.
type BotPreparedPlanLeafKind string

const (
	BotPlanLeafVerifier BotPreparedPlanLeafKind = "verifier"
	BotPlanLeafMirror   BotPreparedPlanLeafKind = "mirror"
)

// BotPreparedPlanLeafEvidence retains a completed leaf when advancing. All fields
// are authenticated by the private record's SQL-selected content hash.
type BotPreparedPlanLeafEvidence struct {
	LeafKind      BotPreparedPlanLeafKind
	Action        execution.ActionDescriptor
	ActionDigest  string
	ApprovalID    uuid.UUID
	DispatchID    uuid.UUID
	EngineRecord  json.RawMessage
	TrustedResult json.RawMessage
}

// BotPreparedPlan is private runtime storage. EngineRecord and TrustedResult
// are opaque JSON; neither transcript prose nor model arguments may create one.
// Action is the exact leaf descriptor, never the wrapper plan tool descriptor.
type BotPreparedPlan struct {
	LeafKind          BotPreparedPlanLeafKind       `json:",omitempty"`
	History           []BotPreparedPlanLeafEvidence `json:",omitempty"`
	BotID             uuid.UUID
	RunID             uuid.UUID
	SessionID         uuid.UUID
	Version           int64
	OperationID       uuid.UUID
	Revision          int64
	Base              BotPlanBase
	Stage             BotPreparedPlanStage
	Action            execution.ActionDescriptor
	ActionDigest      string
	ApprovalID        uuid.UUID
	DispatchID        uuid.UUID
	EngineRecord      json.RawMessage
	TrustedResult     json.RawMessage
	Snapshot          json.RawMessage
	PublicationDigest string
	EventID           string
}

// BotPreparedPlanChange is a revision-CAS transition. Attach requires a future
// ApprovalExpiresAt; start requires the bound approved ApprovalID and a unique
// DispatchID. Verifier settlement requires TrustedResult and updated EngineRecord,
// without Snapshot. Mirror settlement requires a valid future Snapshot. Publish
// accepts no new evidence and emits exactly the settled mirror Snapshot.
type BotPreparedPlanChange struct {
	OperationID       uuid.UUID
	ExpectedRevision  int64
	Stage             BotPreparedPlanStage
	ApprovalExpiresAt time.Time
	EngineRecord      json.RawMessage
	ApprovalID        uuid.UUID
	DispatchID        uuid.UUID
	TrustedResult     json.RawMessage
	Snapshot          json.RawMessage
}

// BotPreparedPlanAdvance atomically replaces a settled verifier with its mirror
// leaf. Base and settled EngineRecord are retained, not supplied anew. Retries
// conflict deterministically; reload the operation after a lost response.
type BotPreparedPlanAdvance struct {
	OperationID      uuid.UUID
	ExpectedRevision int64
	Action           execution.ActionDescriptor
	ActionDigest     string
}

// BotPreparedPlanStore is optional; BotStore implementations and mocks need not
// implement it. Every call requires fresh worker authority and a live lease.
// A dispatch_started/uncertain record must never be executed again. Settled
// records may be published without effects; identical publication is idempotent.
type BotPreparedPlanStore interface {
	PrepareBotPlan(context.Context, BotLease, BotPreparedPlan) (*BotPreparedPlan, error)
	LoadBotPlan(context.Context, BotLease, uuid.UUID) (*BotPreparedPlan, error)
	// LoadBotPlanByApproval locates current or historical approval bindings,
	// including consumed approvals. This is recovery evidence, NOT dispatch authority.
	LoadBotPlanByApproval(context.Context, BotLease, uuid.UUID) (*BotPreparedPlan, error)
	AdvanceBotPlan(context.Context, BotLease, BotPreparedPlanAdvance) (*BotPreparedPlan, error)
	ChangeBotPlan(context.Context, BotLease, BotPreparedPlanChange) (*BotPreparedPlan, error)
}
