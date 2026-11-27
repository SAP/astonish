package entstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
	"time"

	personalrun "github.com/SAP/astonish/ent/personal/botrun"
	teamrun "github.com/SAP/astonish/ent/team/botrun"

	botdomain "github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	adksession "google.golang.org/adk/session"
	"google.golang.org/genai"
)

var _ store.BotEmployeePresentationStore = (*personalBotStore)(nil)
var _ store.BotEmployeePresentationStore = (*teamBotStore)(nil)

func (s *personalBotStore) ClaimEmployeePresentation(c context.Context, q store.BotEmployeePresentationClaim) (*store.BotEmployeePresentation, error) {
	return s.repository().ClaimEmployeePresentation(c, q)
}
func (s *teamBotStore) ClaimEmployeePresentation(c context.Context, q store.BotEmployeePresentationClaim) (*store.BotEmployeePresentation, error) {
	return s.repository().ClaimEmployeePresentation(c, q)
}
func (s *personalBotStore) ReadEmployeePresentation(c context.Context, q store.BotEmployeePresentationRequest) (*store.BotEmployeePresentation, error) {
	return s.repository().ReadEmployeePresentation(c, q)
}
func (s *teamBotStore) ReadEmployeePresentation(c context.Context, q store.BotEmployeePresentationRequest) (*store.BotEmployeePresentation, error) {
	return s.repository().ReadEmployeePresentation(c, q)
}
func (s *personalBotStore) ReserveEmployeePresentationModel(c context.Context, q store.BotEmployeePresentationModelReservation) (*store.BotEmployeePresentation, error) {
	return s.repository().ReserveEmployeePresentationModel(c, q)
}
func (s *teamBotStore) ReserveEmployeePresentationModel(c context.Context, q store.BotEmployeePresentationModelReservation) (*store.BotEmployeePresentation, error) {
	return s.repository().ReserveEmployeePresentationModel(c, q)
}
func (s *personalBotStore) SettleEmployeePresentationModel(c context.Context, q store.BotEmployeePresentationModelSettlement) (*store.BotEmployeePresentation, error) {
	return s.repository().SettleEmployeePresentationModel(c, q)
}
func (s *teamBotStore) SettleEmployeePresentationModel(c context.Context, q store.BotEmployeePresentationModelSettlement) (*store.BotEmployeePresentation, error) {
	return s.repository().SettleEmployeePresentationModel(c, q)
}
func (s *personalBotStore) CompleteEmployeePresentation(c context.Context, q store.BotEmployeePresentationCompletion) (*store.BotEmployeePresentation, error) {
	return s.repository().CompleteEmployeePresentation(c, q)
}
func (s *teamBotStore) CompleteEmployeePresentation(c context.Context, q store.BotEmployeePresentationCompletion) (*store.BotEmployeePresentation, error) {
	return s.repository().CompleteEmployeePresentation(c, q)
}

func (s *personalBotStore) PrepareEmployeePresentation(c context.Context, q store.BotEmployeePresentationRequest) (*store.BotEmployeePresentation, error) {
	return s.repository().PrepareEmployeePresentation(c, q)
}
func (s *teamBotStore) PrepareEmployeePresentation(c context.Context, q store.BotEmployeePresentationRequest) (*store.BotEmployeePresentation, error) {
	return s.repository().PrepareEmployeePresentation(c, q)
}

func presentationAdd(a, b int64) int64 {
	if a < 0 || b < 0 || b > math.MaxInt64-a {
		return math.MaxInt64
	}
	return a + b
}
func presentationTokens(u store.BotUsage) int64 {
	return presentationAdd(u.InputTokens, u.OutputTokens)
}
func presentationLedger(rec *botStoredRecord, saved *botStoredRun) (mandateUsage, mandateRunUsage, error) {
	var usage mandateUsage
	raw, err := json.Marshal(rec.Metadata[mandateUsageKey])
	if err != nil {
		return usage, mandateRunUsage{}, err
	}
	if err = json.Unmarshal(raw, &usage); err != nil {
		return usage, mandateRunUsage{}, err
	}
	id := saved.Run.ID.String()
	current, ok := usage.Active[id]
	if !ok {
		current, ok = usage.Released[id]
	}
	if !ok && saved.ProgressQuota != nil {
		current, ok = *saved.ProgressQuota, true
	}
	if saved.Run.MandateRef == nil {
		return usage, current, store.ErrBotForbidden
	}
	if !ok {
		// Admission can fail before a quota ledger exists. This is accounting
		// metadata only, not a new run reservation or execution grant.
		current = mandateRunUsage{Ref: *saved.Run.MandateRef, Tokens: presentationTokens(saved.Run.Usage)}
	}
	if current.Ref != *saved.Run.MandateRef {
		return usage, current, store.ErrBotForbidden
	}
	return usage, current, nil
}

// Prepare never accepts a budget, pin or operation list from its caller.
func (r *botRepository) PrepareEmployeePresentation(ctx context.Context, q store.BotEmployeePresentationRequest) (*store.BotEmployeePresentation, error) {
	var result *store.BotEmployeePresentation
	err := r.db.Transaction(ctx, func(tx botDB) error {
		rec, err := r.publicationOwner(ctx, tx, q.BotID)
		if err != nil {
			return err
		}
		saved, err := tx.ReadRun(ctx, q.BotID, q.RunID)
		if err != nil {
			return err
		}
		if saved.EmployeePresentation != nil {
			var e error
			rec, saved, e = r.presentationSource(ctx, tx, q)
			if e != nil {
				return e
			}
			if e = saveEmployeePresentation(ctx, tx, rec, saved); e != nil {
				return e
			}
			result = saved.EmployeePresentation
			return nil
		}
		run := saved.Run
		if !run.Status.Terminal() || rec.ActiveRunID != nil || run.MandateRef == nil || run.Provider == "" || run.Model == "" || run.UserEventID == "" {
			return store.ErrBotForbidden
		}
		m, err := readMandate(ctx, tx, *run.MandateRef)
		if err != nil {
			return err
		}
		_, charged, err := presentationLedger(rec, saved)
		if err != nil {
			return err
		}
		expires := m.Mandate.ExpiresAt
		if run.CallerCeiling != nil && run.CallerCeiling.ExpiresAt.Before(expires) {
			expires = run.CallerCeiling.ExpiresAt
		}
		p := &store.BotEmployeePresentation{SchemaVersion: store.BotEmployeePresentationVersion, State: store.BotEmployeePresentationPending, BotID: run.BotID, RunID: run.ID, BotVersion: run.Version, OwnerID: rec.Bot.OwnerID, OrgSlug: r.auth.orgSlug, TeamSlug: run.MandateRef.TeamSlug, HomeSessionID: rec.Bot.HomeSessionID, SourceSessionID: run.SessionID, UserEventID: run.UserEventID, AuthorityExpiresAt: expires, Provider: run.Provider, Model: run.Model, ReportSequence: run.StateSequence, ReportDigest: run.Report.EmployeeMessageSourceDigest(run.Status), Usage: run.Usage}
		if charged.Tokens > presentationTokens(p.Usage) {
			p.Usage.InputTokens = charged.Tokens
			p.Usage.OutputTokens = 0
		}
		for key := range rec.Metadata {
			if !strings.HasPrefix(key, "mandate_operation_head:") {
				continue
			}
			id, e := uuid.Parse(strings.TrimPrefix(key, "mandate_operation_head:"))
			if e != nil {
				return e
			}
			op, e := readMandateOperation(ctx, tx, rec, *run.MandateRef, id)
			if errors.Is(e, store.ErrBotInvalid) {
				continue
			}
			if e != nil {
				return e
			}
			if op.Provenance == nil || op.Provenance.SourceRunID != run.ID {
				continue
			}
			if e = verifyPresentationOperation(ctx, tx, rec, saved, op); e != nil {
				return e
			}
			digest, e := botHash(op)
			if e != nil {
				return e
			}
			p.Operations = append(p.Operations, store.BotEmployeeOperationIdentity{OperationID: id, Revision: op.Revision, ReceiptDigest: digest})
		}
		sort.Slice(p.Operations, func(i, j int) bool {
			return p.Operations[i].OperationID.String() < p.Operations[j].OperationID.String()
		})
		for _, charge := range charged.Models {
			if !charge.Settled {
				p.State = store.BotEmployeePresentationUnavailable
				p.UnavailableReason = store.BotEmployeeReasonInterrupted
			}
		}
		// A cancelled source must not resume work, but a coordinator-owned,
		// definitive schedule outcome may still be communicated with the original
		// tools-disabled claim. Expiry, revocation and unsettled calls still deny it.
		if run.Status == store.RunCancelled && (!saved.EmployeeScheduleCoordinator || len(p.Operations) == 0) {
			p.State = store.BotEmployeePresentationUnavailable
			p.UnavailableReason = store.BotEmployeeReasonOwnerCancelled
		}
		if run.Status == store.RunInterrupted {
			p.State = store.BotEmployeePresentationUnavailable
			p.UnavailableReason = store.BotEmployeeReasonInterrupted
		}
		if !r.clock().Before(expires) {
			p.State = store.BotEmployeePresentationUnavailable
			p.UnavailableReason = store.BotEmployeeReasonExpired
		}
		if m.RevokedAt != nil {
			p.State = store.BotEmployeePresentationUnavailable
			p.UnavailableReason = store.BotEmployeeReasonForbidden
		}
		saved.EmployeePresentation = p
		if err = saveEmployeePresentation(ctx, tx, rec, saved); err != nil {
			return err
		}
		result = p
		return nil
	})
	return result, err
}

// The original source envelope is the sole authority for communication. A current
// Bot version is deliberately not an executable grant for this terminal stage.
func (r *botRepository) presentationSource(ctx context.Context, tx botDB, q store.BotEmployeePresentationRequest) (*botStoredRecord, *botStoredRun, error) {
	rec, err := r.publicationOwner(ctx, tx, q.BotID)
	if err != nil {
		return nil, nil, err
	}
	saved, err := tx.ReadRun(ctx, q.BotID, q.RunID)
	if err != nil {
		return nil, nil, err
	}
	p := saved.EmployeePresentation
	if p == nil || p.ValidateBinding(saved.Run) != nil || !saved.Run.Status.Terminal() || p.OwnerID != rec.Bot.OwnerID || p.HomeSessionID != rec.Bot.HomeSessionID || p.OrgSlug != r.auth.orgSlug || (r.auth.teamSlug != "" && p.TeamSlug != r.auth.teamSlug) {
		return nil, nil, store.ErrBotForbidden
	}
	if p.ClaimToken != "" && (q.ClaimToken == "" || p.ClaimToken != q.ClaimToken) {
		return nil, nil, store.ErrBotConflict
	}
	now := r.clock() // Caller timestamps never extend original authority.
	if rec.Bot.Status == store.BotDeleted || rec.Bot.PermanentDelete {
		return nil, nil, store.ErrBotNotFound
	}
	if p.State == store.BotEmployeePresentationCompleted || p.State == store.BotEmployeePresentationUnavailable {
		return rec, saved, nil
	}
	if !now.Before(p.AuthorityExpiresAt) {
		p.State, p.UnavailableReason = store.BotEmployeePresentationUnavailable, store.BotEmployeeReasonExpired
	}
	if saved.Run.Status == store.RunInterrupted || saved.Run.Status == store.RunCancelled && (!saved.EmployeeScheduleCoordinator || len(p.Operations) == 0) {
		p.State, p.UnavailableReason = store.BotEmployeePresentationUnavailable, store.BotEmployeeReasonInterrupted
	}
	if saved.Run.MandateRef == nil {
		return nil, nil, store.ErrBotForbidden
	}
	m, err := readMandate(ctx, tx, *saved.Run.MandateRef)
	if err != nil {
		return nil, nil, err
	}
	if m.RevokedAt != nil || m.Validate(now) != nil || m.Mandate.Ref.OwnerID != p.OwnerID.String() {
		p.State, p.UnavailableReason = store.BotEmployeePresentationUnavailable, store.BotEmployeeReasonForbidden
	}
	if p.State == store.BotEmployeePresentationUnavailable {
		return rec, saved, nil
	}
	for _, identity := range p.Operations {
		op, err := readMandateOperation(ctx, tx, rec, *saved.Run.MandateRef, identity.OperationID)
		if err != nil {
			return nil, nil, err
		}
		digest, err := botHash(op)
		if err != nil || op.Revision != identity.Revision || digest != identity.ReceiptDigest || verifyPresentationOperation(ctx, tx, rec, saved, op) != nil {
			return nil, nil, store.ErrBotConflict
		}
	}
	if p.ClaimToken != "" && (q.ClaimToken == "" || p.ClaimToken != q.ClaimToken) {
		return nil, nil, store.ErrBotConflict
	}
	return rec, saved, nil
}

// Presentation calls retain cumulative usage accounting without execution quotas.
// They neither occupy a tool run nor charge/reset daily runs.
func chargeEmployeePresentation(ctx context.Context, tx botDB, rec *botStoredRecord, saved *botStoredRun, before store.BotEmployeePresentation) error {
	p := saved.EmployeePresentation
	if len(p.Turns) == 0 || reflect.DeepEqual(before.Turns, p.Turns) {
		return nil
	}
	usage, current, err := presentationLedger(rec, saved)
	if err != nil {
		return err
	}
	turn := p.Turns[0]
	key := "employee-presentation:" + turn.ID
	if current.Models == nil {
		current.Models = map[string]mandateModelCharge{}
	}
	if len(before.Turns) == 0 {
		for _, charge := range current.Models {
			if !charge.Settled {
				return store.ErrBotConflict
			}
		}
		tokens := presentationTokens(turn.Reserved)
		if _, ok := current.Models[key]; ok {
			return store.ErrBotConflict
		}
		current.Models[key] = mandateModelCharge{Tokens: tokens}
		current.Tokens = presentationAdd(current.Tokens, tokens)
		usage.Tokens = presentationAdd(usage.Tokens, tokens)
	} else if before.Turns[0].State != store.BotEmployeeModelTurnSettled && turn.State == store.BotEmployeeModelTurnSettled {
		charge, ok := current.Models[key]
		if !ok || charge.Settled || charge.Tokens > current.Tokens || charge.Tokens > usage.Tokens {
			return store.ErrBotConflict
		}
		tokens := presentationTokens(turn.Usage)
		current.Tokens = presentationAdd(current.Tokens-charge.Tokens, tokens)
		usage.Tokens = presentationAdd(usage.Tokens-charge.Tokens, tokens)
		current.Models[key] = mandateModelCharge{Tokens: tokens, Settled: true}
	}
	id := saved.Run.ID.String()
	if _, active := usage.Active[id]; active {
		usage.Active[id] = current
	} else {
		if usage.Released == nil {
			usage.Released = map[string]mandateRunUsage{}
		}
		usage.Released[id] = current
	}
	if saved.ProgressQuota != nil {
		*saved.ProgressQuota = current
	}
	rec.Metadata[mandateUsageKey] = usage
	return saveMandateMetadata(ctx, tx, rec)
}

// Sequence-CAS serializes private envelope writes with lifecycle/revocation.
func saveEmployeePresentation(ctx context.Context, tx botDB, rec *botStoredRecord, saved *botStoredRun) error {
	if err := saved.EmployeePresentation.ValidateBinding(saved.Run); err != nil {
		return err
	}
	if err := tx.AdvanceSequence(ctx, rec.Bot.ID, rec.LastSequence); err != nil {
		return err
	}
	data, err := encodeBotRunData(ctx, tx, *saved)
	if err != nil {
		return err
	}
	var n int
	switch d := tx.(type) {
	case personalBotDB:
		n, err = d.client.BotRun.Update().Where(personalrun.IDEQ(saved.Run.ID), personalrun.BotIDEQ(rec.Bot.ID), personalrun.StatusEQ(personalrun.Status(saved.Run.Status))).SetData(data).Save(ctx)
	case teamBotDB:
		n, err = d.client.BotRun.Update().Where(teamrun.IDEQ(saved.Run.ID), teamrun.BotIDEQ(rec.Bot.ID), teamrun.StatusEQ(teamrun.Status(saved.Run.Status))).SetData(data).Save(ctx)
	default:
		return store.ErrBotUnsupported
	}
	if err != nil {
		return err
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	return nil
}
func (r *botRepository) mutateEmployeePresentation(ctx context.Context, q store.BotEmployeePresentationRequest, mutate func(*store.BotEmployeePresentation) error) (*store.BotEmployeePresentation, error) {
	var result *store.BotEmployeePresentation
	err := r.db.Transaction(ctx, func(tx botDB) error {
		rec, saved, err := r.presentationSource(ctx, tx, q)
		if err != nil {
			return err
		}
		before := *saved.EmployeePresentation
		before.Turns = append([]store.BotEmployeeModelTurn(nil), before.Turns...)
		if saved.EmployeePresentation.State == store.BotEmployeePresentationUnavailable {
			if err = saveEmployeePresentation(ctx, tx, rec, saved); err != nil {
				return err
			}
			result = saved.EmployeePresentation
			return nil
		}
		if err = mutate(saved.EmployeePresentation); err != nil {
			return err
		}
		if err = chargeEmployeePresentation(ctx, tx, rec, saved, before); err != nil {
			return err
		}
		if err = saveEmployeePresentation(ctx, tx, rec, saved); err != nil {
			return err
		}
		result = saved.EmployeePresentation
		return nil
	})
	return result, err
}
func (r *botRepository) ReadEmployeePresentation(ctx context.Context, q store.BotEmployeePresentationRequest) (*store.BotEmployeePresentation, error) {
	return r.mutateEmployeePresentation(ctx, q, func(*store.BotEmployeePresentation) error { return nil })
}
func (r *botRepository) ClaimEmployeePresentation(ctx context.Context, q store.BotEmployeePresentationClaim) (*store.BotEmployeePresentation, error) {
	return r.mutateEmployeePresentation(ctx, q.BotEmployeePresentationRequest, func(p *store.BotEmployeePresentation) error {
		if q.ExpectedReportSequence != p.ReportSequence || q.ExpectedReportDigest != p.ReportDigest || q.ClaimToken == "" {
			return store.ErrBotConflict
		}
		if p.State == store.BotEmployeePresentationPending {
			p.State = store.BotEmployeePresentationClaimed
			p.ClaimToken = q.ClaimToken
		}
		return nil
	})
}
func (r *botRepository) ReserveEmployeePresentationModel(ctx context.Context, q store.BotEmployeePresentationModelReservation) (*store.BotEmployeePresentation, error) {
	return r.mutateEmployeePresentation(ctx, q.BotEmployeePresentationRequest, func(p *store.BotEmployeePresentation) error {
		if p.State != store.BotEmployeePresentationClaimed || q.TurnID == "" || q.DispatchToken == "" || len(p.Turns) != 0 || q.Reserved.ToolCalls != 0 || !presentationUsageValid(q.Reserved) {
			return store.ErrBotConflict
		}
		p.DispatchToken = q.DispatchToken
		p.Turns = append(p.Turns, store.BotEmployeeModelTurn{ID: q.TurnID, DispatchToken: q.DispatchToken, State: store.BotEmployeeModelTurnStarted, StartedAt: r.clock(), Reserved: q.Reserved})
		return nil
	})
}
func presentationUsageValid(u store.BotUsage) bool {
	return u.InputTokens >= 0 && u.OutputTokens >= 0 && u.RuntimeNanos >= 0 && u.ToolCalls == 0 && (u.CostMicros == nil || *u.CostMicros >= 0)
}
func (r *botRepository) SettleEmployeePresentationModel(ctx context.Context, q store.BotEmployeePresentationModelSettlement) (*store.BotEmployeePresentation, error) {
	return r.mutateEmployeePresentation(ctx, q.BotEmployeePresentationRequest, func(p *store.BotEmployeePresentation) error {
		if p.State != store.BotEmployeePresentationClaimed || len(p.Turns) != 1 || !presentationUsageValid(q.Usage) {
			return store.ErrBotConflict
		}
		turn := &p.Turns[0]
		if turn.ID != q.TurnID || turn.DispatchToken != q.DispatchToken || p.DispatchToken != q.DispatchToken {
			return store.ErrBotConflict
		}
		if turn.State == store.BotEmployeeModelTurnSettled {
			if !reflect.DeepEqual(turn.Usage, q.Usage) {
				return store.ErrBotConflict
			}
			return nil
		}
		turn.State = store.BotEmployeeModelTurnSettled
		turn.SettledAt = r.clock()
		turn.Usage = q.Usage
		p.Usage.InputTokens = presentationAdd(p.Usage.InputTokens, q.Usage.InputTokens)
		p.Usage.OutputTokens = presentationAdd(p.Usage.OutputTokens, q.Usage.OutputTokens)
		p.Usage.RuntimeNanos = presentationAdd(p.Usage.RuntimeNanos, q.Usage.RuntimeNanos)
		if p.Usage.CostMicros == nil || q.Usage.CostMicros == nil {
			p.Usage.CostMicros = nil
		} else {
			value := presentationAdd(*p.Usage.CostMicros, *q.Usage.CostMicros)
			p.Usage.CostMicros = &value
		}
		return nil
	})
}
func (r *botRepository) CompleteEmployeePresentation(ctx context.Context, q store.BotEmployeePresentationCompletion) (*store.BotEmployeePresentation, error) {
	return r.mutateEmployeePresentation(ctx, q.BotEmployeePresentationRequest, func(p *store.BotEmployeePresentation) error {
		if p.State == store.BotEmployeePresentationCompleted {
			if p.FinalText != q.Text || p.DispatchToken != q.DispatchToken {
				return store.ErrBotConflict
			}
			return nil
		}
		if p.State == store.BotEmployeePresentationUnavailable {
			if p.UnavailableReason != q.UnavailableReason {
				return store.ErrBotConflict
			}
			return nil
		}
		if p.State != store.BotEmployeePresentationClaimed {
			return store.ErrBotConflict
		}
		if q.UnavailableReason != "" {
			if q.Text != "" {
				return store.ErrBotInvalid
			}
			p.State = store.BotEmployeePresentationUnavailable
			p.UnavailableReason = q.UnavailableReason
			return nil
		}
		if len(p.Turns) != 1 || p.Turns[0].State != store.BotEmployeeModelTurnSettled || p.DispatchToken != q.DispatchToken {
			return store.ErrBotConflict
		}
		if !botdomain.EmployeePresentationTextValid(q.Text) {
			p.State = store.BotEmployeePresentationUnavailable
			p.UnavailableReason = store.BotEmployeeReasonFormat
			return nil
		}
		p.State = store.BotEmployeePresentationCompleted
		p.FinalText = q.Text
		p.FinalTextDigest = store.BotEmployeeTextDigest(q.Text)
		return nil
	})
}

var _ store.BotPublicationStore = (*personalBotStore)(nil)
var _ store.BotPublicationStore = (*teamBotStore)(nil)

func (s *personalBotStore) PublishReport(c context.Context, q store.BotPublicationRequest) (*store.BotPublication, error) {
	return s.repository().PublishReport(c, q)
}
func (s *teamBotStore) PublishReport(c context.Context, q store.BotPublicationRequest) (*store.BotPublication, error) {
	return s.repository().PublishReport(c, q)
}
func (s *personalBotStore) ClaimNotification(c context.Context, q store.BotNotificationClaim) (*store.BotNotification, bool, error) {
	return s.repository().ClaimNotification(c, q)
}
func (s *teamBotStore) ClaimNotification(c context.Context, q store.BotNotificationClaim) (*store.BotNotification, bool, error) {
	return s.repository().ClaimNotification(c, q)
}
func (s *personalBotStore) FinishNotification(c context.Context, q store.BotNotificationFinish) error {
	return s.repository().FinishNotification(c, q)
}
func (s *teamBotStore) FinishNotification(c context.Context, q store.BotNotificationFinish) error {
	return s.repository().FinishNotification(c, q)
}

type botPublicationDB interface {
	AppendHomeReport(context.Context, store.Bot, *adksession.Event) error
}

func publicationKey(run uuid.UUID, sequence int64) string {
	return fmt.Sprintf("home-report:%s:%d", run, sequence)
}
func notificationKey(run, binding uuid.UUID, sequence int64, state string) string {
	return fmt.Sprintf("notification:%s:%d:%s:%s", run, sequence, binding, state)
}

// committedPublicationReport selects only committed outcome intents, never the
// publication receipts that share the report activity kind. Explicit sequences
// allow replay of earlier outcomes after the mutable run has resumed.
func committedPublicationReport(ctx context.Context, db botDB, bot, run uuid.UUID, sequence int64) (store.BotRun, int64, error) {
	saved, err := db.ReadRun(ctx, bot, run)
	if err != nil {
		return store.BotRun{}, 0, err
	}
	var selected store.BotRun
	var selectedSequence int64
	after := int64(0)
	if sequence > 0 {
		after = sequence - 1
	}
	for {
		limit := 200
		if sequence > 0 {
			// Do not hydrate unrelated later evidence for an exact report replay.
			limit = 1
		}
		activities, err := db.ReadActivity(ctx, bot, after, limit)
		if err != nil {
			return selected, 0, err
		}
		for _, a := range activities {
			after = a.Sequence
			if sequence > 0 && a.Sequence > sequence {
				return selected, selectedSequence, store.ErrBotNotFound
			}
			if a.Kind != store.BotActivityReport || a.RunID == nil || *a.RunID != run {
				continue
			}
			var outcome struct {
				Intent store.BotOutboxKind `json:"intent"`
				Status store.RunStatus     `json:"status"`
				Report store.BotReport     `json:"report"`
			}
			if err := json.Unmarshal(a.Payload, &outcome); err != nil {
				return selected, 0, err
			}
			if outcome.Intent != store.BotOutboxPublish {
				continue
			}
			selected = saved.Run
			selected.Status, selected.Report = outcome.Status, outcome.Report
			selected.StateSequence = a.Sequence
			if snapshot, ok := saved.EmployeeOutcomes[a.Sequence]; ok {
				x := snapshot.Run
				if x.BotID != bot || x.ID != run || x.StateSequence != a.Sequence || x.Status != outcome.Status || x.Report.EmployeeMessageSourceDigest(x.Status) != outcome.Report.EmployeeMessageSourceDigest(outcome.Status) {
					return selected, 0, store.ErrBotInvalid
				}
				// Report digests do not cover Employee wording: check that separately.
				expected, actual := x.Report.EmployeeMessage, outcome.Report.EmployeeMessage
				if (expected == nil) != (actual == nil) || expected != nil && (expected.Text != actual.Text || expected.SourceDigest != actual.SourceDigest || expected.Origin != actual.Origin) {
					return selected, 0, store.ErrBotInvalid
				}
				selected = x
			}
			selectedSequence = a.Sequence
			if sequence > 0 {
				return selected, selectedSequence, nil
			}
		}
		if sequence > 0 || len(activities) < limit {
			break
		}
	}
	if selectedSequence == 0 {
		return selected, 0, store.ErrBotNotFound
	}
	return selected, selectedSequence, nil
}

func (r *botRepository) publicationOwner(ctx context.Context, db botDB, id uuid.UUID) (*botStoredRecord, error) {
	if _, err := r.auth.authorize(ctx, uuid.Nil, true); err != nil {
		return nil, err
	}
	rec, err := db.GetBot(ctx, id)
	if err != nil {
		return nil, err
	}
	actor, err := r.auth.authorize(ctx, rec.Bot.OwnerID, true)
	if err != nil {
		return nil, err
	}
	if actor != rec.Bot.OwnerID || rec.Bot.Scope != r.auth.scope {
		return nil, store.ErrBotForbidden
	}
	return rec, nil
}
func publicationTime(now time.Time, r *botRepository) time.Time {
	if now.IsZero() {
		return r.clock()
	}
	return now.UTC()
}
func insertPublicationActivity(ctx context.Context, db botDB, rec *botStoredRecord, run uuid.UUID, kind store.BotActivityKind, key string, payload any, now time.Time) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if err := db.AdvanceSequence(ctx, rec.Bot.ID, rec.LastSequence); err != nil {
		return err
	}
	rec.LastSequence++
	return db.InsertActivity(ctx, store.BotActivity{BotID: rec.Bot.ID, RunID: &run, Sequence: rec.LastSequence, Kind: kind, Payload: data, CreatedAt: now}, key)
}
func readPublication(ctx context.Context, db botDB, bot, run uuid.UUID, sequence int64) (*store.BotPublication, error) {
	if sequence == 0 {
		_, latest, err := committedPublicationReport(ctx, db, bot, run, 0)
		if err != nil {
			return nil, err
		}
		sequence = latest
	}
	a, err := db.FindActivity(ctx, bot, publicationKey(run, sequence))
	if err != nil {
		return nil, err
	}
	var p store.BotPublication
	if err := json.Unmarshal(a.Payload, &p); err != nil {
		return nil, err
	}
	if p.EmployeeSpeech {
		source, exact, err := committedPublicationReport(ctx, db, bot, run, sequence)
		if err != nil {
			return nil, err
		}
		stored, err := db.ReadRun(ctx, bot, run)
		if err != nil {
			return nil, err
		}
		var presentation *store.BotEmployeePresentation
		if x := stored.EmployeePresentation; x != nil && x.ReportSequence == sequence {
			presentation = x
		}
		text, speech := store.EmployeeSpeech(source, stored.EmployeeOutcomes[sequence].Speech, presentation)
		owner, err := db.GetBot(ctx, bot)
		if err != nil {
			return nil, err
		}
		if !speech || exact != sequence || p.BotID != bot || p.RunID != run || p.ReportSequence != sequence || p.Version != source.Version || p.HomeSessionID != owner.Bot.HomeSessionID || p.SourceSessionID != source.SessionID || p.SourceUserEventID != source.UserEventID || p.ReportDigest != source.Report.EmployeeMessageSourceDigest(source.Status) || p.EventID != store.BotPublicationEventID(run, sequence) || p.Text != text {
			return nil, store.ErrBotInvalid
		}
	}
	return &p, nil
}

// A neutral publication may advance its own event sequence, not the immutable
// source sequence bound by the private presentation. AppendRunState rewrites the
// source sequence, so use a detached state event for presentation-backed outcomes.
func appendEmployeeTerminalPublication(ctx context.Context, tx botDB, rec *botStoredRecord, source store.BotRun, at time.Time) error {
	saved, err := tx.ReadRun(ctx, source.BotID, source.ID)
	if err != nil {
		return err
	}
	if saved.EmployeePresentation == nil {
		return appendNeutralTerminalPublication(ctx, tx, rec, source, at)
	}
	if source.Status != store.RunPartial && source.Status != store.RunFailed {
		return nil
	}
	if saved.Run.Status != source.Status || saved.Run.SessionID != source.SessionID {
		return nil
	}
	saved.Run.TerminalReason = store.BotTerminalNoAnswer
	if err := saveEmployeePresentation(ctx, tx, rec, saved); err != nil {
		return err
	}
	event, err := store.NewBotRunStateEvent(rec.Bot, saved.Run, rec.LastSequence, at)
	if err != nil {
		return err
	}
	switch d := tx.(type) {
	case personalBotDB:
		if err = d.appendRunTranscript(ctx, rec.Bot, saved.Run, event); err == nil {
			queueBotRunStateNotification(ctx, d.afterCommit, event.Actions.StateDelta[store.BotRunStateKey].(store.BotRunState))
		}
	case teamBotDB:
		if err = d.appendRunTranscript(ctx, rec.Bot, saved.Run, event); err == nil {
			queueBotRunStateNotification(ctx, d.afterCommit, event.Actions.StateDelta[store.BotRunStateKey].(store.BotRunState))
		}
	default:
		return store.ErrBotUnsupported
	}
	return err
}

func (r *botRepository) PublishReport(ctx context.Context, q store.BotPublicationRequest) (*store.BotPublication, error) {
	if q.BotID == uuid.Nil || q.RunID == uuid.Nil {
		return nil, store.ErrBotInvalid
	}
	var result *store.BotPublication
	err := r.db.Transaction(ctx, func(tx botDB) error {
		rec, err := r.publicationOwner(ctx, tx, q.BotID)
		if err != nil {
			return err
		}
		prior, err := readPublication(ctx, tx, q.BotID, q.RunID, q.ReportSequence)
		if err == nil {
			result = prior
			for i := range result.Notifications {
				n, err := readNotification(ctx, tx, result, result.Notifications[i].Target.ID)
				if err != nil {
					return err
				}
				result.Notifications[i] = *n
			}
			return nil
		}
		if !errors.Is(err, store.ErrBotNotFound) {
			return err
		}
		if rec.Bot.Status == store.BotDeleted {
			return store.ErrBotConflict
		}
		savedRun, sequence, err := committedPublicationReport(ctx, tx, q.BotID, q.RunID, q.ReportSequence)
		if err != nil {
			return err
		}
		saved := struct{ Run store.BotRun }{savedRun}
		if !saved.Run.Status.Terminal() && saved.Run.Status != store.RunWaitingApproval && saved.Run.Status != store.RunInterrupted {
			return store.ErrBotConflict
		}
		if strings.TrimSpace(saved.Run.Report.Summary) == "" {
			return store.ErrBotConflict
		}
		version, err := tx.ReadVersion(ctx, q.BotID, saved.Run.Version)
		if err != nil {
			return err
		}
		adapter, ok := tx.(botPublicationDB)
		if !ok {
			return store.ErrBotUnsupported
		}
		now := publicationTime(q.Now, r)
		stored, err := tx.ReadRun(ctx, q.BotID, q.RunID)
		if err != nil {
			return err
		}
		outcome, boundOutcome := stored.EmployeeOutcomes[sequence]
		presentation := stored.EmployeePresentation
		if presentation != nil && presentation.ReportSequence != sequence {
			presentation = nil
		}
		// The durable coordinator marker precedes preparation of the private final
		// envelope. A restart in that window must not fall back to source prose.
		// Do not create a suppression receipt yet: a later completed presentation
		// must still be able to publish, with the original verified artifacts.
		if boundOutcome && outcome.ScheduleCoordinator && presentation == nil {
			return store.ErrBotConflict
		}
		if presentation != nil {
			// Never attach a later private continuation to an older committed receipt.
			if presentation.ValidateBinding(saved.Run) != nil || presentation.ReportSequence != sequence || presentation.ReportDigest != saved.Run.Report.EmployeeMessageSourceDigest(saved.Run.Status) {
				return store.ErrBotConflict
			}
			if presentation.State == store.BotEmployeePresentationPending || presentation.State == store.BotEmployeePresentationClaimed {
				return store.ErrBotConflict
			}
			for _, identity := range presentation.Operations {
				if stored.Run.MandateRef == nil {
					return store.ErrBotForbidden
				}
				op, err := readMandateOperation(ctx, tx, rec, *stored.Run.MandateRef, identity.OperationID)
				if err != nil {
					return err
				}
				digest, err := botHash(op)
				if err != nil || op.Revision != identity.Revision || digest != identity.ReceiptDigest || verifyPresentationOperation(ctx, tx, rec, stored, op) != nil {
					return store.ErrBotConflict
				}
			}
		}
		text, speech := store.EmployeeSpeech(saved.Run, outcome.Speech, presentation)
		p := &store.BotPublication{EmployeeSpeech: speech, SourceSessionID: saved.Run.SessionID, SourceUserEventID: saved.Run.UserEventID, ReportDigest: saved.Run.Report.EmployeeMessageSourceDigest(saved.Run.Status), Artifacts: append([]store.BotArtifact(nil), saved.Run.Report.Artifacts...), ReportSequence: sequence, BotID: q.BotID, RunID: q.RunID, Version: saved.Run.Version, HomeSessionID: rec.Bot.HomeSessionID, EventID: store.BotPublicationEventID(q.RunID, sequence), Text: text, PublishedAt: now}
		// Artifact eligibility is independent of speech. Private/fallback outcomes
		// retain a committed receipt and can append a file-only delivery event.
		if !speech && len(p.Artifacts) == 0 {
			if err := insertPublicationActivity(ctx, tx, rec, q.RunID, store.BotActivityReport, publicationKey(q.RunID, sequence), p, now); err != nil {
				return err
			}
			if err := appendEmployeeTerminalPublication(ctx, tx, rec, saved.Run, now); err != nil {
				return err
			}
			result = p
			return nil
		}
		// A committed schedule receipt is a distinct source-bound Home event, not
		// the model's conversational reply, and never authorizes delivery.
		scheduleReceipt, err := authorizedSchedulePublication(ctx, tx, rec, q.RunID, sequence)
		if err != nil {
			return err
		}
		if saved.Run.Report.EmployeeMessage != nil && saved.Run.Report.EmployeeMessage.Origin == store.BotEmployeeMessageOperation && !scheduleReceipt {
			return store.ErrBotInvalid
		}
		evaluation := botdomain.EvaluateNotification(version.Delivery, saved.Run.Report, now, false)
		if !speech || saved.Run.ControlOnly() || scheduleReceipt {
			evaluation.Decision = store.NotificationSkip
			evaluation.Reason = "Owner conversation does not authorize external notifications"
		}
		seen := map[uuid.UUID]bool{}
		targets := map[string]bool{}
		for _, binding := range version.Delivery.Bindings {
			target := binding.Channel + "\x00" + binding.ChatID + "\x00" + binding.ThreadID
			if binding.ID == uuid.Nil || strings.TrimSpace(binding.Channel) == "" || strings.TrimSpace(binding.ChatID) == "" || seen[binding.ID] || targets[target] {
				continue
			}
			seen[binding.ID] = true
			targets[target] = true
			state := store.BotNotificationPending
			if evaluation.Decision == store.NotificationSkip {
				state = store.BotNotificationSkipped
			}
			if evaluation.Decision == store.NotificationDefer {
				state = store.BotNotificationDeferred
			}
			p.Notifications = append(p.Notifications, store.BotNotification{ReportSequence: sequence, BotID: q.BotID, RunID: q.RunID, Target: binding, State: state, Decision: evaluation.Decision, Reason: evaluation.Reason, AvailableAt: evaluation.AvailableAt, Text: p.Text})
		}
		event := &adksession.Event{ID: p.EventID, InvocationID: "bot-report:" + q.RunID.String(), Author: "assistant", Timestamp: now, LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: p.Text}}}}}
		envelope, err := p.FilePublication()
		if err != nil {
			return err
		}
		if envelope != nil {
			if envelope.HomeID != rec.Bot.Home.ID {
				return store.ErrBotInvalid
			}
			event.Actions.StateDelta = map[string]any{store.BotFilePublicationKey: envelope}
		}
		if event.Actions.StateDelta == nil {
			event.Actions.StateDelta = map[string]any{}
		}
		event.Actions.StateDelta[store.BotPublicationKey] = map[string]any{"bot_id": p.BotID, "run_id": p.RunID, "report_sequence": p.ReportSequence, "event_id": p.EventID}
		if err := adapter.AppendHomeReport(ctx, rec.Bot, event); err != nil {
			return err
		}
		if err := insertPublicationActivity(ctx, tx, rec, q.RunID, store.BotActivityReport, publicationKey(q.RunID, sequence), p, now); err != nil {
			return err
		}
		if !speech {
			if err := appendEmployeeTerminalPublication(ctx, tx, rec, saved.Run, now); err != nil {
				return err
			}
		}
		result = p
		return nil
	})
	return result, err
}

// questionOnlyWaitingPublication uses committed approval identities, never prose
// or the mutable run's current status, so replay after an answer stays silent.
func questionOnlyWaitingPublication(ctx context.Context, db botDB, run store.BotRun) (bool, error) {
	r := run.Report
	if run.Status != store.RunWaitingApproval || len(r.NeedsApproval) == 0 || r.Failure != nil || len(r.Changed) != 0 || len(r.Artifacts) != 0 || len(r.Unverified) != 0 || r.EmployeeMessage == nil || r.EmployeeMessage.Origin != store.BotEmployeeMessageFallback {
		return false, nil
	}
	for _, id := range r.NeedsApproval {
		a, err := db.ReadApproval(ctx, run.BotID, id)
		if errors.Is(err, store.ErrBotNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if a.RunID != run.ID || a.Action.Tool != "ask_user" || a.Interaction == nil || a.Interaction.Validate() != nil {
			return false, nil
		}
	}
	return true, nil
}

func readNotification(ctx context.Context, db botDB, p *store.BotPublication, binding uuid.UUID) (*store.BotNotification, error) {
	var original *store.BotNotification
	for i := range p.Notifications {
		if p.Notifications[i].Target.ID == binding {
			n := p.Notifications[i]
			original = &n
			break
		}
	}
	if original == nil {
		return nil, store.ErrBotNotFound
	}
	for _, state := range []string{"delivered", "claim", "skip"} {
		a, err := db.FindActivity(ctx, p.BotID, notificationKey(p.RunID, binding, p.ReportSequence, state))
		if errors.Is(err, store.ErrBotNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var n store.BotNotification
		if err := json.Unmarshal(a.Payload, &n); err != nil {
			return nil, err
		}
		return &n, nil
	}
	return original, nil
}

func (r *botRepository) ClaimNotification(ctx context.Context, q store.BotNotificationClaim) (*store.BotNotification, bool, error) {
	var result *store.BotNotification
	send := false
	err := r.db.Transaction(ctx, func(tx botDB) error {
		rec, err := r.publicationOwner(ctx, tx, q.BotID)
		if err != nil {
			return err
		}
		p, err := readPublication(ctx, tx, q.BotID, q.RunID, q.ReportSequence)
		if err != nil {
			return err
		}
		n, err := readNotification(ctx, tx, p, q.BindingID)
		if err != nil {
			return err
		}
		result = n
		if n.State != store.BotNotificationPending && n.State != store.BotNotificationDeferred {
			return nil
		}
		now := publicationTime(q.Now, r)
		if now.Before(n.AvailableAt) {
			return nil
		}
		skip := func(reason string) error {
			n.State = store.BotNotificationSkipped
			n.Decision = store.NotificationSkip
			n.Reason = reason
			return insertPublicationActivity(ctx, tx, rec, q.RunID, store.BotActivityNotification, notificationKey(q.RunID, q.BindingID, p.ReportSequence, "skip"), n, now)
		}
		if rec.Bot.Status != store.BotActive || rec.Bot.Version != p.Version {
			return skip("bot inactive or approved version changed")
		}
		savedRun, _, err := committedPublicationReport(ctx, tx, q.BotID, q.RunID, p.ReportSequence)
		if err != nil {
			return err
		}
		saved := struct{ Run store.BotRun }{savedRun}
		if saved.Run.ControlOnly() {
			return skip("Owner conversation does not authorize external notifications")
		}
		version, err := tx.ReadVersion(ctx, q.BotID, p.Version)
		if err != nil {
			return err
		}
		matched := false
		for _, binding := range version.Delivery.Bindings {
			if binding == n.Target {
				matched = true
			}
		}
		if !matched {
			return skip("channel binding changed")
		}
		if err := r.authorizeDeliveryIdentity(ctx, rec.Bot.OwnerID, n.Target); err != nil {
			return skip("notification destination authority revoked or unproven")
		}
		// Goal-scoped notifications are obsolete once the goal completes, even if a
		// reminder was deferred while it was still active.
		for _, trigger := range version.Triggers {
			if trigger.ID == saved.Run.TriggerID && trigger.GoalID != nil {
				goal, err := tx.ReadGoal(ctx, q.BotID, *trigger.GoalID)
				if errors.Is(err, store.ErrBotNotFound) {
					return skip("goal no longer exists")
				}
				if err != nil {
					return err
				}
				if goal.Status != store.BotGoalActive {
					return skip("goal completed")
				}
			}
		}
		evaluation := botdomain.EvaluateNotification(version.Delivery, saved.Run.Report, now, true)
		if evaluation.Decision != store.NotificationSend {
			return skip(evaluation.Reason)
		}
		n.State = store.BotNotificationUncertain
		n.Decision = store.NotificationSend
		n.Reason = "delivery claimed; awaiting positive transport receipt"
		n.AttemptID = uuid.New()
		if err := insertPublicationActivity(ctx, tx, rec, q.RunID, store.BotActivityNotification, notificationKey(q.RunID, q.BindingID, p.ReportSequence, "claim"), n, now); err != nil {
			return err
		}
		send = true
		return nil
	})
	if err != nil {
		send = false
	}
	return result, send, err
}

func (r *botRepository) FinishNotification(ctx context.Context, q store.BotNotificationFinish) error {
	if q.AttemptID == uuid.Nil || q.Delivered && strings.TrimSpace(q.Receipt) == "" {
		return store.ErrBotInvalid
	}
	return r.db.Transaction(ctx, func(tx botDB) error {
		rec, err := r.publicationOwner(ctx, tx, q.BotID)
		if err != nil {
			return err
		}
		p, err := readPublication(ctx, tx, q.BotID, q.RunID, q.ReportSequence)
		if err != nil {
			return err
		}
		n, err := readNotification(ctx, tx, p, q.BindingID)
		if err != nil {
			return err
		}
		if n.AttemptID != q.AttemptID {
			return store.ErrBotConflict
		}
		if n.State == store.BotNotificationDelivered {
			if q.Delivered && n.Receipt == q.Receipt {
				return nil
			}
			return store.ErrBotConflict
		}
		if n.State != store.BotNotificationUncertain {
			return store.ErrBotConflict
		}
		if !q.Delivered {
			return nil
		}
		n.State = store.BotNotificationDelivered
		n.Receipt = q.Receipt
		n.Reason = "transport confirmed delivery"
		return insertPublicationActivity(ctx, tx, rec, q.RunID, store.BotActivityNotification, notificationKey(q.RunID, q.BindingID, p.ReportSequence, "delivered"), n, publicationTime(q.Now, r))
	})
}
