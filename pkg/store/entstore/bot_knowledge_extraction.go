package entstore

import (
	"context"
	"errors"
	"fmt"
	personalbotrun "github.com/SAP/astonish/ent/personal/botrun"
	teambotrun "github.com/SAP/astonish/ent/team/botrun"
	"math"
	"reflect"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type knowledgeInputSanitizerKey struct{}
type knowledgeInputSanitizer func(context.Context, store.BotKnowledgeExtractionInput) (store.BotKnowledgeExtractionInput, string, error)

func WithBotKnowledgeInputSanitizer(c context.Context, f func(context.Context, store.BotKnowledgeExtractionInput) (store.BotKnowledgeExtractionInput, string, error)) context.Context {
	return context.WithValue(c, knowledgeInputSanitizerKey{}, knowledgeInputSanitizer(f))
}

func knowledgeRepository(s store.BotStore) (*botRepository, error) {
	switch x := s.(type) {
	case *botRepository:
		return x, nil
	case *personalBotStore:
		return x.repository(), nil
	case *teamBotStore:
		return x.repository(), nil
	}
	return nil, store.ErrBotUnsupported
}

// ReadBotKnowledgeTurnInput reads issuer-owned evidence for a running turn;
// LoadRunInput is deliberately admission-only and must not be reused after claim.
func ReadBotKnowledgeTurnInput(c context.Context, s store.BotStore, lease store.BotLease) (*store.BotRunInput, error) {
	r, err := knowledgeRepository(s)
	if err != nil {
		return nil, err
	}
	var out store.BotRunInput
	err = r.db.Transaction(c, func(tx botDraftDB) error {
		root, err := tx.GetBot(c, lease.BotID)
		if err != nil {
			return err
		}
		saved, err := tx.ReadRun(c, lease.BotID, lease.RunID)
		if err != nil {
			return err
		}
		if err = r.authorizeWorker(c, lease.BotID, lease.RunID, saved.Run.Version, root.Bot.OwnerID); err != nil {
			return err
		}
		// Commit retries may read terminal evidence, but cannot mint extraction authority.
		if saved.Run.Lease != lease {
			return store.ErrBotConflict
		}
		out = store.BotRunInput{Bot: root.Bot, Run: saved.Run, Work: saved.Work.Clone(), Instructions: saved.Instructions, Attachments: saved.Attachments}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// PublishBotKnowledgeExtraction serializes fresh authority, physical CAS and
// receipt transitions under the repository root lock. No terminal run lease is used.
func PublishBotKnowledgeExtraction(c context.Context, s store.BotStore, l store.BotKnowledgeExtractionLease, revision int64, publish func(context.Context, store.BotKnowledgeExtraction, func(context.Context) error) ([]store.BotKnowledgeFileReadback, error), verify func(context.Context, store.BotKnowledgeExtractionLease, store.BotKnowledgeExtraction, []store.BotKnowledgeFileReadback) error) error {
	r, err := knowledgeRepository(s)
	if err != nil {
		return err
	}
	if publish == nil || verify == nil {
		return store.ErrBotUnsupported
	}
	return r.db.Transaction(c, func(tx botDraftDB) error {
		v, err := r.knowledgeLease(c, tx, l)
		if err != nil {
			return err
		}
		if v.Record.Revision != revision || (v.Record.State != store.BotKnowledgeExtractionPrepared && v.Record.State != store.BotKnowledgeExtractionFilesReady) {
			return store.ErrBotConflict
		}
		check := func(ctx context.Context) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if l.Validate(r.clock()) != nil {
				return store.ErrBotExpired
			}
			_, err := r.auth.authorize(ctx, v.ActorID, true)
			return err
		}
		readback, err := publish(c, v.Record.Clone(), check)
		if err != nil {
			return err
		}
		if err := check(c); err != nil {
			return err
		}
		if err := verify(c, l, v.Record.Clone(), readback); err != nil {
			return err
		}
		for _, to := range []store.BotKnowledgeExtractionState{store.BotKnowledgeExtractionFilesReady, store.BotKnowledgeExtractionCommitted} {
			if v.Record.State == to {
				continue
			}
			old := v.Record.Revision
			q := store.BotKnowledgeExtractionTransition{Lease: l, ExpectedRevision: old, From: v.Record.State, To: to, Readback: readback}
			if err := q.Validate(v.Record, r.clock()); err != nil {
				return err
			}
			if to == store.BotKnowledgeExtractionCommitted {
				if err := tx.(botKnowledgeDB).commitKnowledgeHeads(c, v.Record); err != nil {
					return err
				}
			}
			v.Record.State = to
			v.Record.Revision++
			if to == store.BotKnowledgeExtractionCommitted {
				v.Lease.ExpiresAt = r.clock().UTC().Truncate(time.Microsecond)
			}
			if err := tx.(botKnowledgeDB).saveKnowledge(c, *v, old); err != nil {
				return err
			}
		}
		return c.Err()
	})
}

// RecordBotKnowledgeUsage persists the cumulative shared meter without modifying
// the terminal lease. Record outstanding holds BEFORE generation, never refund a crash.
func RecordBotKnowledgeUsage(c context.Context, s store.BotStore, source store.BotKnowledgeExtractionSource, lease store.BotKnowledgeExtractionLease, usage store.BotUsage) error {
	r, err := knowledgeRepository(s)
	if err != nil {
		return err
	}
	return r.db.Transaction(c, func(tx botDraftDB) error {
		v, err := r.knowledgeLease(c, tx, lease)
		if err != nil {
			return err
		}
		if v.Record.Input.Source != source {
			return store.ErrBotConflict
		}
		if v.Record.State != store.BotKnowledgeExtractionExtracting {
			return store.ErrBotConflict
		}
		saved, err := tx.ReadRun(c, source.BotID, source.RunID)
		if err != nil {
			return err
		}
		old := saved.Run.Usage
		if v.UsageBase == nil {
			base := old
			v.UsageBase = &base
			if err := tx.(botKnowledgeDB).saveKnowledge(c, *v, v.Record.Revision); err != nil {
				return err
			}
		}
		base := *v.UsageBase
		for _, pair := range [][2]int64{{usage.InputTokens, base.InputTokens}, {usage.OutputTokens, base.OutputTokens}, {usage.ToolCalls, base.ToolCalls}, {usage.RuntimeNanos, base.RuntimeNanos}} {
			if pair[0] < 0 || pair[1] < 0 || pair[0] > math.MaxInt64-pair[1] {
				return store.ErrBotInvalid
			}
		}
		if usage.CostMicros != nil && (*usage.CostMicros < 0 || (base.CostMicros != nil && (*base.CostMicros < 0 || *usage.CostMicros > math.MaxInt64-*base.CostMicros))) {
			return store.ErrBotInvalid
		}
		usage.InputTokens += base.InputTokens
		usage.OutputTokens += base.OutputTokens
		usage.ToolCalls += base.ToolCalls
		usage.RuntimeNanos += base.RuntimeNanos
		if usage.CostMicros != nil && base.CostMicros != nil {
			cost := *usage.CostMicros + *base.CostMicros
			usage.CostMicros = &cost
		} else {
			usage.CostMicros = nil
		}
		if !saved.Run.Status.Terminal() || usage.InputTokens < base.InputTokens || usage.OutputTokens < base.OutputTokens || usage.ToolCalls < base.ToolCalls || usage.RuntimeNanos < base.RuntimeNanos {
			return store.ErrBotConflict
		}
		saved.Run.Usage = usage
		var codec *botContentCodec
		switch d := tx.(type) {
		case personalBotDB:
			codec = d.contentCodec()
		case teamBotDB:
			codec = d.contentCodec()
		default:
			return store.ErrBotUnsupported
		}
		data, err := codec.encodeRun(c, *saved)
		if err != nil {
			return err
		}
		var n int
		switch d := tx.(type) {
		case personalBotDB:
			n, err = d.client.BotRun.Update().Where(personalbotrun.IDEQ(source.RunID), personalbotrun.BotIDEQ(source.BotID)).SetData(data).Save(c)
		case teamBotDB:
			n, err = d.client.BotRun.Update().Where(teambotrun.IDEQ(source.RunID), teambotrun.BotIDEQ(source.BotID)).SetData(data).Save(c)
		default:
			return store.ErrBotUnsupported
		}
		if err != nil {
			return err
		}
		if n != 1 {
			return store.ErrBotConflict
		}
		return c.Err()
	})
}

type knowledgeEnvelope struct {
	UsageBase *store.BotUsage                   `json:"usage_base,omitempty"`
	Record    store.BotKnowledgeExtraction      `json:"record"`
	Lease     store.BotKnowledgeExtractionLease `json:"lease"`
	ActorID   uuid.UUID                         `json:"actor_id"`
	RootFence int64                             `json:"root_fence"`
}
type botKnowledgeDB interface {
	lockKnowledgeRoot(context.Context, uuid.UUID) error
	readKnowledge(context.Context, uuid.UUID, uuid.UUID) (*knowledgeEnvelope, error)
	findKnowledge(context.Context, store.BotKnowledgeExtractionSource) (*knowledgeEnvelope, error)
	saveKnowledge(context.Context, knowledgeEnvelope, int64) error
	commitKnowledgeHeads(context.Context, store.BotKnowledgeExtraction) error
}

// WithBotKnowledgePublicationVerifier is trusted composition wiring. The verifier
// must resolve actual retained bytes and original expected file digests at this
// exact extraction fence; HTTP/model supplied readback is never a receipt.
func WithBotKnowledgePublicationVerifier(ctx context.Context, verify func(context.Context, store.BotKnowledgeExtractionLease, store.BotKnowledgeExtraction, []store.BotKnowledgeFileReadback) error) context.Context {
	return context.WithValue(ctx, knowledgeVerifierKey{}, verify)
}

type knowledgeVerifierKey struct{}
type knowledgeSourceVerifierKey struct{}

// WithBotKnowledgeSourceVerifier binds the complete redacted history, tools,
// review and prior-file snapshot to issuer-owned durable evidence. The callback
// must never accept model/caller claims as canonical evidence.
func WithBotKnowledgeSourceVerifier(ctx context.Context, verify func(context.Context, store.BotKnowledgeExtractionInput) error) context.Context {
	return context.WithValue(ctx, knowledgeSourceVerifierKey{}, verify)
}

func (r *botRepository) knowledgeAuthority(c context.Context, tx botDraftDB, b uuid.UUID) (*botStoredRecord, uuid.UUID, error) {
	if err := c.Err(); err != nil {
		return nil, uuid.Nil, err
	}
	d, ok := tx.(botKnowledgeDB)
	if !ok {
		return nil, uuid.Nil, store.ErrBotUnsupported
	}
	if err := d.lockKnowledgeRoot(c, b); err != nil {
		return nil, uuid.Nil, err
	}
	root, err := tx.GetBot(c, b)
	if err != nil {
		return nil, uuid.Nil, err
	}
	actor, err := r.auth.authorize(c, root.Bot.OwnerID, true)
	if err != nil {
		return nil, uuid.Nil, err
	}
	if root.Metadata["restore_reconciliation_required"] == true {
		return nil, uuid.Nil, store.ErrBotConflict
	}
	// Draft Bots can execute owner-admitted interactive turns. Learning those
	// turns does not activate the Bot or authorize scheduled work.
	if root.Bot.Status != store.BotActive && root.Bot.Status != store.BotPaused && root.Bot.Status != store.BotDraft {
		return nil, uuid.Nil, store.ErrBotConflict
	}
	return root, actor, nil
}

// enqueueBotKnowledgeExtractionTx is the atomic CommitRun hook. Call after the
// canonical result activity has been inserted, using the redacted complete turn.
// It never claims a run lease or calls a model.
func (r *botRepository) enqueueBotKnowledgeExtractionTx(c context.Context, tx botDraftDB, input store.BotKnowledgeExtractionInput) (*store.BotKnowledgeExtraction, error) {
	if input.Source.Validate() != nil {
		return nil, store.ErrBotInvalid
	}
	root, actor, err := r.knowledgeAuthority(c, tx, input.Source.BotID)
	if err != nil {
		return nil, err
	}
	db := tx.(botKnowledgeDB)
	if existing, e := db.findKnowledge(c, input.Source); e == nil {
		if !reflect.DeepEqual(existing.Record.Input, input) {
			return nil, store.ErrBotConflict
		}
		out := existing.Record.Clone()
		return &out, nil
	} else if !errors.Is(e, store.ErrBotNotFound) {
		return nil, e
	}
	run, err := tx.ReadRun(c, input.Source.BotID, input.Source.RunID)
	if err != nil {
		return nil, err
	}
	if !run.Run.Status.Terminal() || run.Run.FinishedAt == nil || run.CommitHash == "" || run.Run.Version != input.Source.Version || string(run.Run.Status) != input.Outcome.Status || run.Instructions != input.OwnerMessage || run.Run.Report.Summary != input.Outcome.Answer {
		return nil, fmt.Errorf("knowledge canonical run terminal=%v finished=%v commit=%v version=%v status=%v instructions=%v answer=%v: %w", run.Run.Status.Terminal(), run.Run.FinishedAt != nil, run.CommitHash != "", run.Run.Version == input.Source.Version, string(run.Run.Status) == input.Outcome.Status, run.Instructions == input.OwnerMessage, run.Run.Report.Summary == input.Outcome.Answer, store.ErrBotConflict)
	}
	activity, err := tx.FindActivity(c, input.Source.BotID, "result:"+run.Run.ID.String()+":"+knowledgeFenceString(run.Run.Lease.Fence))
	if err != nil {
		return nil, err
	}
	if activity.Sequence != input.Source.Sequence || activity.RunID == nil || *activity.RunID != input.Source.RunID || activity.Kind != store.BotActivityReport {
		return nil, fmt.Errorf("knowledge activity sequence=%v run=%v kind=%v: %w", activity.Sequence == input.Source.Sequence, activity.RunID != nil && *activity.RunID == input.Source.RunID, activity.Kind == store.BotActivityReport, store.ErrBotConflict)
	}
	// Validate issuer evidence before applying the trusted retention policy.
	verify, ok := c.Value(knowledgeSourceVerifierKey{}).(func(context.Context, store.BotKnowledgeExtractionInput) error)
	if !ok || verify == nil {
		return nil, store.ErrBotUnsupported
	}
	if err = verify(c, input.Clone()); err != nil {
		return nil, err
	}
	reason := ""
	if sanitize, ok := c.Value(knowledgeInputSanitizerKey{}).(knowledgeInputSanitizer); ok {
		input, reason, err = sanitize(c, input.Clone())
		if err != nil {
			return nil, err
		}
	}
	if reason == "" {
		if err := input.Validate(); err != nil {
			return nil, err
		}
	}
	d := tx.(botKnowledgeDB)
	old, err := d.findKnowledge(c, input.Source)
	if err == nil {
		if !reflect.DeepEqual(old.Record.Input, input) {
			return nil, store.ErrBotConflict
		}
		v := old.Record.Clone()
		return &v, nil
	}
	if !errors.Is(err, store.ErrBotNotFound) {
		return nil, err
	}
	v := knowledgeEnvelope{Record: store.BotKnowledgeExtraction{ID: uuid.New(), Input: input.Clone(), State: store.BotKnowledgeExtractionPending, Revision: 1}, ActorID: actor, RootFence: root.Fence}
	if reason != "" {
		v.Record.State = store.BotKnowledgeExtractionHeld
		v.Record.HoldReason = reason
	}
	if err = d.saveKnowledge(c, v, 0); err != nil {
		return nil, err
	}
	out := v.Record.Clone()
	return &out, nil
}
func (r *botRepository) EnqueueBotKnowledgeExtraction(c context.Context, input store.BotKnowledgeExtractionInput) (out *store.BotKnowledgeExtraction, err error) {
	err = r.db.Transaction(c, func(tx botDraftDB) error {
		var e error
		out, e = r.enqueueBotKnowledgeExtractionTx(c, tx, input)
		return e
	})
	return
}
func (r *botRepository) ReadBotKnowledgeExtraction(c context.Context, s store.BotKnowledgeExtractionSource) (*store.BotKnowledgeExtraction, error) {
	if s.Validate() != nil {
		return nil, store.ErrBotInvalid
	}
	if _, e := r.Get(c, s.BotID); e != nil {
		return nil, e
	}
	d, ok := r.db.(botKnowledgeDB)
	if !ok {
		return nil, store.ErrBotUnsupported
	}
	v, e := d.findKnowledge(c, s)
	if e != nil {
		return nil, e
	}
	out := v.Record.Clone()
	return &out, nil
}
func (r *botRepository) ClaimBotKnowledgeExtraction(c context.Context, s store.BotKnowledgeExtractionSource, worker string, ttl time.Duration) (lease store.BotKnowledgeExtractionLease, err error) {
	if s.Validate() != nil || strings.TrimSpace(worker) == "" || ttl <= 0 || ttl > 5*time.Minute {
		return lease, store.ErrBotInvalid
	}
	uncertain := false
	err = r.db.Transaction(c, func(tx botDraftDB) error {
		root, actor, e := r.knowledgeAuthority(c, tx, s.BotID)
		if e != nil {
			return e
		}
		d := tx.(botKnowledgeDB)
		v, e := d.findKnowledge(c, s)
		if e != nil {
			return e
		}
		if actor != v.ActorID {
			return store.ErrBotForbidden
		}
		now := r.clock()
		if v.Lease.Fence > 0 && now.Before(v.Lease.ExpiresAt) && !(v.Lease.WorkerID == worker && (v.Record.State == store.BotKnowledgeExtractionPrepared || v.Record.State == store.BotKnowledgeExtractionFilesReady)) {
			return store.ErrBotConflict
		}
		if v.Record.State == store.BotKnowledgeExtractionExtracting {
			old := v.Record.Revision
			v.Record.State = store.BotKnowledgeExtractionHeld
			v.Record.HoldReason = "model_uncertain"
			v.Record.Revision++
			v.Lease.ExpiresAt = now.UTC().Truncate(time.Microsecond)
			uncertain = true
			return d.saveKnowledge(c, *v, old)
		}
		switch v.Record.State {
		case store.BotKnowledgeExtractionPending, store.BotKnowledgeExtractionPrepared, store.BotKnowledgeExtractionFilesReady, store.BotKnowledgeExtractionHeld:
		default:
			return store.ErrBotConflict
		}
		// Canonicalize before storing typed timestamps and JSON: PostgreSQL
		// retains microseconds, while exact lease comparisons protect fences.
		// Uncertain Held may only be claimed to abort, never to regenerate.
		lease = store.BotKnowledgeExtractionLease{BotID: s.BotID, ExtractionID: v.Record.ID, WorkerID: worker, Fence: v.Lease.Fence + 1, ExpiresAt: now.Add(ttl).UTC().Truncate(time.Microsecond)}
		v.Lease = lease
		v.RootFence = root.Fence
		return d.saveKnowledge(c, *v, v.Record.Revision)
	})
	if err == nil && uncertain {
		err = store.ErrBotConflict
	}
	return
}
func (r *botRepository) knowledgeLease(c context.Context, tx botDraftDB, l store.BotKnowledgeExtractionLease) (*knowledgeEnvelope, error) {
	root, actor, e := r.knowledgeAuthority(c, tx, l.BotID)
	if e != nil {
		return nil, e
	}
	v, e := tx.(botKnowledgeDB).readKnowledge(c, l.BotID, l.ExtractionID)
	if e != nil {
		return nil, e
	}
	if actor != v.ActorID {
		return nil, store.ErrBotForbidden
	}
	if l.Validate(r.clock()) != nil {
		return nil, store.ErrBotExpired
	}
	if v.Lease.BotID != l.BotID || v.Lease.ExtractionID != l.ExtractionID || v.Lease.WorkerID != l.WorkerID || v.Lease.Fence != l.Fence || !v.Lease.ExpiresAt.Equal(l.ExpiresAt) || root.Fence != v.RootFence {
		return nil, store.ErrBotConflict
	}
	return v, nil
}
func (r *botRepository) BeginBotKnowledgeExtraction(c context.Context, l store.BotKnowledgeExtractionLease, revision int64) (out *store.BotKnowledgeExtraction, started bool, err error) {
	err = r.db.Transaction(c, func(tx botDraftDB) error {
		v, e := r.knowledgeLease(c, tx, l)
		if e != nil {
			return e
		}
		if v.Record.State != store.BotKnowledgeExtractionPending {
			out = &v.Record
			return nil
		}
		if v.Record.Revision != revision {
			return store.ErrBotConflict
		}
		v.Record.AttemptID = uuid.New()
		v.Record.State = store.BotKnowledgeExtractionExtracting
		v.Record.Revision++
		if e = tx.(botKnowledgeDB).saveKnowledge(c, *v, revision); e != nil {
			return e
		}
		out = &v.Record
		started = true
		return nil
	})
	return
}
func (r *botRepository) TransitionBotKnowledgeExtraction(c context.Context, q store.BotKnowledgeExtractionTransition) (out *store.BotKnowledgeExtraction, err error) {
	err = r.db.Transaction(c, func(tx botDraftDB) error {
		v, e := r.knowledgeLease(c, tx, q.Lease)
		if e != nil {
			return e
		}
		if e = q.Validate(v.Record, r.clock()); e != nil {
			return e
		}
		if q.To == store.BotKnowledgeExtractionFilesReady || q.To == store.BotKnowledgeExtractionCommitted {
			verify, ok := c.Value(knowledgeVerifierKey{}).(func(context.Context, store.BotKnowledgeExtractionLease, store.BotKnowledgeExtraction, []store.BotKnowledgeFileReadback) error)
			if !ok || verify == nil {
				return store.ErrBotUnsupported
			}
			if e = verify(c, q.Lease, v.Record.Clone(), q.Readback); e != nil {
				return e
			}
		}
		if q.Decision != nil {
			decision := q.Decision.Clone()
			v.Record.Decision = &decision
			v.Record.DecisionDigest = q.DecisionDigest
		}
		if q.To == store.BotKnowledgeExtractionCommitted {
			if e = tx.(botKnowledgeDB).commitKnowledgeHeads(c, v.Record); e != nil {
				return e
			}
		}
		v.Record.State = q.To
		v.Record.Revision++
		v.Record.HoldReason = q.Reason
		if q.To == store.BotKnowledgeExtractionCommitted || q.To == store.BotKnowledgeExtractionNoChange || q.To == store.BotKnowledgeExtractionHeld || q.To == store.BotKnowledgeExtractionAborted {
			v.Lease.ExpiresAt = r.clock().UTC().Truncate(time.Microsecond)
		}
		if e = tx.(botKnowledgeDB).saveKnowledge(c, *v, q.ExpectedRevision); e != nil {
			return e
		}
		out = &v.Record
		return c.Err()
	})
	return
}
