package entstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

const mandateHeadKey = "mandate_head"
const mandateUsageKey = "mandate_usage"
const mandateRecordKind store.BotActivityKind = "mandate_record"

type botMandateMetadataDB interface {
	saveMandateMetadata(context.Context, *botStoredRecord) error
}
type botMandateRuntime struct {
	repo   *botRepository
	policy store.BotMandatePolicyResolver
}

// NewBotMandateRuntime is a composition-root capability. It accepts only routed
// Ent repositories; neither a payload nor an arbitrary BotStore can mint it.
// Runtime verification uses separate scoped membership lookup, NOT owner impersonation.
func NewBotMandateRuntime(bots store.BotStore, policy store.BotMandatePolicyResolver) (store.BotMandateRuntime, error) {
	if policy == nil {
		return nil, store.ErrBotForbidden
	}
	var r *botRepository
	switch b := bots.(type) {
	case *personalBotStore:
		r = b.repository()
	case *teamBotStore:
		r = b.repository()
	default:
		return nil, store.ErrBotUnsupported
	}
	return &botMandateRuntime{repo: r, policy: policy}, nil
}
func mandateKey(ref store.BotMandateRef) string {
	return fmt.Sprintf("mandate:%s:%d", ref.ID, ref.Revision)
}
func mandateRevokedKey(ref store.BotMandateRef) string {
	return fmt.Sprintf("mandate-revoked:%s:%d", ref.ID, ref.Revision)
}
func readMandate(ctx context.Context, db botDraftDB, ref store.BotMandateRef) (*store.BotMandateRecord, error) {
	a, err := db.FindActivity(ctx, ref.BotID, mandateKey(ref))
	if err != nil {
		return nil, err
	}
	var m store.BotMandateRecord
	if a.Kind != mandateRecordKind || json.Unmarshal(a.Payload, &m) != nil || m.Mandate.Ref != ref {
		return nil, store.ErrBotInvalid
	}
	revoked, err := db.FindActivity(ctx, ref.BotID, mandateRevokedKey(ref))
	if err == nil {
		at := revoked.CreatedAt
		m.RevokedAt = &at
	} else if !errors.Is(err, store.ErrBotNotFound) {
		return nil, err
	}
	return &m, nil
}
func (r *botRepository) mandateScope(ctx context.Context, db botDraftDB, ref store.BotMandateRef) (*botStoredRecord, error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, store.ErrBotForbidden
	}
	if ref.Validate() != nil || ref.OrgSlug != r.auth.orgSlug || (r.auth.teamSlug != "" && ref.TeamSlug != r.auth.teamSlug) || (r.auth.scope == store.BotScopePersonal && ref.OwnerID != r.auth.userID) || r.auth.check == nil {
		return nil, store.ErrBotForbidden
	}
	rec, err := db.GetBot(ctx, ref.BotID)
	if err != nil {
		return nil, err
	}
	if rec.Bot.OwnerID.String() != ref.OwnerID || rec.Bot.DeletedAt != nil || rec.Bot.Status == store.BotDeleted {
		return nil, store.ErrBotForbidden
	}
	role, err := r.auth.check(ctx, ref.OwnerID)
	if err != nil {
		return nil, err
	}
	if role != "owner" && role != "admin" && role != "member" {
		return nil, store.ErrBotForbidden
	}
	return rec, nil
}
func checkMandatePolicy(m store.BotMandate, p store.BotMandatePolicy) error {
	if err := m.Limits.ValidateWithin(p.Limits); err != nil {
		return err
	}
	for _, s := range m.Scopes {
		if !slices.Contains(p.Scopes, s) {
			return store.ErrBotForbidden
		}
	}
	for _, c := range m.Constraints {
		for _, resource := range c.Resources {
			allowed := false
			for _, current := range p.Constraints {
				if current.Action == c.Action && slices.Contains(current.Resources, resource) {
					allowed = true
					break
				}
			}
			if !allowed {
				return store.ErrBotForbidden
			}
		}
	}
	return nil
}
func (s *botMandateRuntime) verify(ctx context.Context, db botDraftDB, ref store.BotMandateRef, purpose store.BotMandatePurpose) (*store.BotMandateRecord, *botStoredRecord, error) {
	rec, err := s.repo.mandateScope(ctx, db, ref)
	if err != nil {
		return nil, nil, err
	}
	m, err := readMandate(ctx, db, ref)
	if err != nil {
		return nil, nil, err
	}
	if m.Validate(s.repo.clock()) != nil || m.Mandate.Purpose != purpose || m.Provenance.Source != store.BotMandateSourceAuthenticatedUser || m.Provenance.ActorID != ref.OwnerID || m.Provenance.UserEventID == "" || m.Provenance.SourceDigest == "" {
		return nil, nil, store.ErrBotForbidden
	}
	if binding, e := readScheduledTask(ctx, db, ref); e == nil {
		if err := s.verifyScheduledTask(ctx, db, rec, m, binding); err != nil {
			return nil, nil, err
		}
		policy, err := s.policy(botPolicyContext(ctx, db), ref)
		if err != nil {
			return nil, nil, err
		}
		if err = checkMandatePolicy(m.Mandate, policy); err != nil {
			return nil, nil, err
		}
		return m, rec, nil
	} else if !errors.Is(e, store.ErrBotNotFound) {
		return nil, nil, e
	}
	sourceID, err := uuid.Parse(m.Provenance.RunID)
	if err != nil {
		return nil, nil, store.ErrBotForbidden
	}
	source, err := db.ReadRun(ctx, ref.BotID, sourceID)
	if err != nil {
		return nil, nil, err
	}
	if source.Run.MandateRef == nil || *source.Run.MandateRef != ref || source.Run.UserEventID != m.Provenance.UserEventID || source.Run.HistoryCursor < 1 {
		return nil, nil, store.ErrBotForbidden
	}
	digest, err := botHash(struct {
		EventID      string
		Instructions string
		Attachments  []store.ChatFile
	}{source.Run.UserEventID, source.Instructions, source.Attachments})
	if err != nil || digest != m.Provenance.SourceDigest {
		return nil, nil, store.ErrBotForbidden
	}
	if purpose == execution.BotMandatePurposeOperation && (source.Run.ID.String() != m.Provenance.RunID || source.Run.Intent != store.BotIntentConversation || source.Run.Presentation != store.BotRunPresentationHomeInteractive || source.Run.Version != rec.Bot.Version || source.Run.Trigger != store.TriggerManual) {
		return nil, nil, store.ErrBotForbidden
	}
	policy, err := s.policy(botPolicyContext(ctx, db), ref)
	if err != nil {
		return nil, nil, err
	}
	if err = checkMandatePolicy(m.Mandate, policy); err != nil {
		return nil, nil, err
	}
	return m, rec, nil
}
func (s *botMandateRuntime) VerifyBotMandate(ctx context.Context, ref store.BotMandateRef, purpose store.BotMandatePurpose) (store.BotMandate, error) {
	m, rec, err := s.verify(ctx, s.repo.db, ref, purpose)
	if err == nil && rec.Bot.Status == store.BotPaused && purpose != execution.BotMandatePurposeOperation {
		return store.BotMandate{}, store.ErrBotForbidden
	}
	if err != nil {
		return store.BotMandate{}, err
	}
	return m.Mandate.Clone(), nil
}
func (s *botMandateRuntime) ReadBotMandate(ctx context.Context, ref store.BotMandateRef) (*store.BotMandateRecord, error) {
	if _, err := s.repo.mandateScope(ctx, s.repo.db, ref); err != nil {
		return nil, err
	}
	return readMandate(ctx, s.repo.db, ref)
}
func (*botMandateRuntime) CreateBotMandate(context.Context, store.BotMandateRecord) error {
	return store.ErrBotForbidden
}
func (s *botMandateRuntime) RevokeBotMandate(ctx context.Context, ref store.BotMandateRef) error {
	return s.repo.db.Transaction(ctx, func(tx botDraftDB) error {
		rec, err := s.repo.mandateScope(ctx, tx, ref)
		if err != nil {
			return err
		}
		actor, err := s.repo.auth.authorize(ctx, rec.Bot.OwnerID, true)
		if err != nil {
			return err
		}
		if actor != rec.Bot.OwnerID || !verifiedMandateOwner(ctx, ref.OwnerID, s.repo.clock()) {
			return store.ErrBotForbidden
		}
		m, err := readMandate(ctx, tx, ref)
		if err != nil {
			return err
		}
		if m.RevokedAt == nil {
			if err = appendMandateRecord(ctx, tx, rec, mandateRevokedKey(ref), map[string]any{"ref": ref, "revoked": true}, s.repo.clock()); err != nil {
				return err
			}
		}
		return s.repo.cancelMandateOperationsTx(ctx, tx, rec, ref)
	})
}
func verifiedMandateOwner(ctx context.Context, owner string, now time.Time) bool {
	p, ok := execution.PrincipalFromContext(ctx)
	e, present := execution.AuthenticationEvidenceFromContext(ctx)
	return ok && present && p.Validate() == nil && p.Kind == execution.PrincipalKindUser && p.Subject == owner && e.Validate(now) == nil && (e.Method == execution.AuthMethodPlatformJWT || (e.Method == execution.AuthMethodLocalOS && e.LocalLoopback))
}
func appendMandateRecord(ctx context.Context, tx botDraftDB, rec *botStoredRecord, key string, value any, now time.Time) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err = tx.AdvanceSequence(ctx, rec.Bot.ID, rec.LastSequence); err != nil {
		return err
	}
	rec.LastSequence++
	if err = tx.InsertActivity(ctx, store.BotActivity{BotID: rec.Bot.ID, Sequence: rec.LastSequence, Kind: mandateRecordKind, Payload: data, CreatedAt: now}, key); err != nil {
		return err
	}
	// Only reserved typed operation records can publish public state. The event
	// and operation share this transaction and activity sequence.
	switch o := value.(type) {
	case store.BotOperation:
		return appendBotOperationViewTx(ctx, tx, rec, o, now)
	case *store.BotOperation:
		return appendBotOperationViewTx(ctx, tx, rec, *o, now)
	}
	return nil
}
func saveMandateMetadata(ctx context.Context, tx botDraftDB, rec *botStoredRecord) error {
	d, ok := tx.(botMandateMetadataDB)
	if !ok {
		return store.ErrBotUnsupported
	}
	return d.saveMandateMetadata(ctx, rec)
}

// issueMessageMandate runs only after the actual user event has been appended.
// Its record, private pin and head commit together with admission or not at all.
func (r *botRepository) issueMessageMandate(ctx context.Context, tx botDraftDB, rec *botStoredRecord, run *store.BotRun, q store.BotMessageRequest) error {
	resolve, enabled := store.BotMandateAdmissionFromContext(ctx)
	if !enabled {
		return nil
	}
	if !verifiedMandateOwner(ctx, rec.Bot.OwnerID.String(), r.clock()) || run.UserEventID == "" || run.HistoryCursor < 1 {
		return store.ErrBotForbidden
	}
	p, _ := execution.PrincipalFromContext(ctx)
	ref := store.BotMandateRef{ID: uuid.New(), BotID: rec.Bot.ID, OrgSlug: r.auth.orgSlug, TeamSlug: p.TeamSlug, OwnerID: rec.Bot.OwnerID.String(), Revision: 1}
	// Paused owner turns receive control-only authority. Old schedules are never upgraded.
	policy, err := resolve(botPolicyContext(ctx, tx), ref)
	if err != nil {
		return err
	}
	if policy.TTL <= 0 {
		return store.ErrBotInvalid
	}
	now := r.clock()
	m := store.BotMandate{Ref: ref, Purpose: execution.BotMandatePurposeTask, ExpiresAt: now.Add(policy.TTL), Scopes: policy.Scopes, Constraints: policy.Constraints, Limits: policy.Limits}.Clone()
	if rec.Bot.Status == store.BotPaused {
		m.Purpose = execution.BotMandatePurposeOperation
		m.Constraints = pausedControlConstraints(m.Constraints)
	}
	digest, err := botHash(struct {
		EventID      string
		Instructions string
		Attachments  []store.ChatFile
	}{run.UserEventID, q.Instructions, q.Attachments})
	if err != nil {
		return err
	}
	kind := store.BotInitiationKindDirectOwnerMessage
	if title, _ := ctx.Value(titleMandateAdmissionKey{}).(bool); title {
		kind = store.BotInitiationKindTitle
	}
	// Legacy schedule_command data is never an authority source.
	record := store.BotMandateRecord{Mandate: m, Provenance: store.BotMandateProvenance{InitiationKind: kind, Source: store.BotMandateSourceAuthenticatedUser, ActorID: ref.OwnerID, RequestID: q.IdempotencyKey, RecordedAt: now, UserEventID: run.UserEventID, SourceDigest: digest, RunID: run.ID.String()}}
	if err = record.Validate(now); err != nil {
		return err
	}
	if err = appendMandateRecord(ctx, tx, rec, mandateKey(ref), record, now); err != nil {
		return err
	}
	if rec.Metadata == nil {
		rec.Metadata = map[string]any{}
	}
	rec.Metadata[mandateHeadKey] = ref
	if err = saveMandateMetadata(ctx, tx, rec); err != nil {
		return err
	}
	run.MandateRef = &ref
	return nil
}

// pausedControlTool names exact control-plane tools, not task execution tools.
// Listing uses the run-bound own-Bot scheduler adapter; it cannot mutate jobs.
type pausedControlTool string

const (
	pausedControlCapture pausedControlTool = "propose_bot_control"
	pausedControlAsk     pausedControlTool = "ask_user"
	pausedControlList    pausedControlTool = "list_scheduled_jobs"
)

// Only exact control tools and non-task routine resources survive pause. This is
// an intersection with current policy, never an additional permission grant.
func pausedControlConstraints(in []execution.BotActionResourceConstraint) []execution.BotActionResourceConstraint {
	var out []execution.BotActionResourceConstraint
	for _, c := range in {
		if c.Action != execution.CapabilityToolExecute {
			continue
		}
		var resources []string
		for _, r := range c.Resources {
			switch pausedControlTool(r) {
			case pausedControlCapture, pausedControlAsk, pausedControlList:
				resources = append(resources, r)
				continue
			}
			for _, op := range []store.BotControlOperation{store.BotControlPriorityPause, store.BotControlResumeTasks, store.BotControlResponsibility, store.BotControlPersona, store.BotControlGoalCreate, store.BotControlGoalEdit, store.BotControlMemoryRemember, store.BotControlMemoryCorrect, store.BotControlMemoryForget} {
				if r == RoutineControlResource(op) {
					resources = append(resources, r)
				}
			}
		}
		if len(resources) > 0 {
			out = append(out, execution.BotActionResourceConstraint{Action: c.Action, Resources: resources})
		}
	}
	return out
}
