package entstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	adksession "google.golang.org/adk/session"
)

// The skill tables contain only scoped knowledge; replay metadata is private.
type botStoredSkill struct {
	Skill    store.BotSkill            `json:"skill"`
	Requests map[string]botSkillReplay `json:"requests,omitempty"`
}
type botSkillReplay struct {
	Hash  string              `json:"hash"`
	Skill *store.BotSkill     `json:"skill,omitempty"`
	Work  *store.BotWorkState `json:"work,omitempty"`
}
type botSkillDB interface {
	lockSkills(context.Context, uuid.UUID) error
	readSkill(context.Context, uuid.UUID, uuid.UUID) (*botStoredSkill, error)
	readSkills(context.Context, uuid.UUID, uuid.UUID, int, bool) ([]store.BotSkill, error)
	writeSkill(context.Context, botStoredSkill, int64) error
	readSkillRevision(context.Context, uuid.UUID, uuid.UUID, int64) (*store.BotSkillRevision, error)
	readSkillRevisions(context.Context, uuid.UUID, uuid.UUID, int64, int) ([]store.BotSkillRevision, error)
	insertSkillRevision(context.Context, store.BotSkillRevision) error
	skillQuota(context.Context, uuid.UUID) (int, int64, error)
	skillSourceEvent(context.Context, uuid.UUID, string) (*adksession.Event, error)
	purgeSkills(context.Context, uuid.UUID) error
}

func skillPage(p store.BotSkillPage) (int, uuid.UUID, error) {
	if err := p.Validate(); err != nil {
		return 0, uuid.Nil, err
	}
	n := p.Limit
	if n == 0 {
		n = 50
	}
	id := uuid.Nil
	if p.After != "" {
		var err error
		id, err = uuid.Parse(p.After)
		if err != nil {
			return 0, id, store.ErrBotInvalid
		}
	}
	return n, id, nil
}
func (r *botRepository) skillRead(ctx context.Context, id uuid.UUID) (botSkillDB, error) {
	if _, err := r.Get(ctx, id); err != nil {
		return nil, err
	}
	db, ok := r.db.(botSkillDB)
	if !ok {
		return nil, store.ErrBotUnsupported
	}
	return db, nil
}
func (r *botRepository) ListSkills(ctx context.Context, id store.BotID, p store.BotSkillPage) (*store.BotSkillList, error) {
	n, after, err := skillPage(p)
	if err != nil {
		return nil, err
	}
	db, err := r.skillRead(ctx, id)
	if err != nil {
		return nil, err
	}
	rows, err := db.readSkills(ctx, id, after, n+1, p.IncludeArchived)
	if err != nil {
		return nil, err
	}
	out := &store.BotSkillList{Skills: rows}
	if len(rows) > n {
		out.Skills = rows[:n]
		out.Next = rows[n-1].ID.String()
	}
	return out, nil
}
func (r *botRepository) GetSkill(ctx context.Context, b store.BotID, id uuid.UUID) (*store.BotSkill, error) {
	db, err := r.skillRead(ctx, b)
	if err != nil {
		return nil, err
	}
	s, err := db.readSkill(ctx, b, id)
	if err != nil {
		return nil, err
	}
	return &s.Skill, nil
}
func (r *botRepository) GetSkillRevision(ctx context.Context, b store.BotID, id uuid.UUID, v int64) (*store.BotSkillRevision, error) {
	if v < 1 {
		return nil, store.ErrBotInvalid
	}
	db, err := r.skillRead(ctx, b)
	if err != nil {
		return nil, err
	}
	if _, err = db.readSkill(ctx, b, id); err != nil {
		return nil, err
	}
	return db.readSkillRevision(ctx, b, id, v)
}
func (r *botRepository) ListSkillRevisions(ctx context.Context, b store.BotID, id uuid.UUID, p store.BotSkillPage) (*store.BotSkillRevisionList, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	n := p.Limit
	if n == 0 {
		n = 50
	}
	var after int64
	if p.After != "" {
		var err error
		after, err = strconv.ParseInt(p.After, 10, 64)
		if err != nil || after < 0 {
			return nil, store.ErrBotInvalid
		}
	}
	db, err := r.skillRead(ctx, b)
	if err != nil {
		return nil, err
	}
	if _, err = db.readSkill(ctx, b, id); err != nil {
		return nil, err
	}
	rows, err := db.readSkillRevisions(ctx, b, id, after, n+1)
	if err != nil {
		return nil, err
	}
	out := &store.BotSkillRevisionList{Revisions: rows}
	if len(rows) > n {
		out.Revisions = rows[:n]
		out.Next = strconv.FormatInt(rows[n-1].Revision, 10)
	}
	return out, nil
}
func (r *botRepository) ListSkillRuns(ctx context.Context, b store.BotID, id uuid.UUID, p store.BotSkillPage) (*store.BotSkillRunList, error) {
	n, after, err := skillPage(p)
	if err != nil {
		return nil, err
	}
	if _, err = r.GetSkill(ctx, b, id); err != nil {
		return nil, err
	}
	out := &store.BotSkillRunList{Runs: []store.BotRun{}}
	// Scan bounded batches and advance by canonical run IDs, not caller-supplied JSON.
	for {
		rows, err := r.db.ReadRuns(ctx, b, 100, after)
		if err != nil {
			return nil, err
		}
		for _, run := range rows {
			after = run.ID
			saved, err := r.db.ReadRun(ctx, b, run.ID)
			if err != nil {
				return nil, err
			}
			if saved.Work != nil && saved.Work.Contract.Skill != nil && saved.Work.Contract.Skill.SkillID == id {
				if len(out.Runs) == n {
					out.Next = out.Runs[n-1].ID.String()
					return out, nil
				}
				out.Runs = append(out.Runs, run)
			}
		}
		if len(rows) < 100 {
			return out, nil
		}
	}
}
func skillSHA(text string) string { d := sha256.Sum256([]byte(text)); return hex.EncodeToString(d[:]) }

// resolveSkillSource never treats an author role or a model-provided UUID as owner authority.
func resolveSkillSource(ctx context.Context, tx botDraftDB, p store.BotSkillProvenance, b uuid.UUID) error {
	if err := p.Validate(b); err != nil {
		return err
	}
	if p.Source == store.BotSkillSourceRunAdmission {
		// Original manual admission is authenticated before InsertRun. Resolve
		// immutable instructions, never the mutable work prompt or a role label.
		run, err := tx.ReadRun(ctx, b, p.RunID)
		if err != nil {
			return err
		}
		owner, err := tx.GetBot(ctx, b)
		if err != nil {
			return err
		}
		if run.SkillAdmissionOwner == nil || *run.SkillAdmissionOwner != owner.Bot.OwnerID || run.Run.Trigger != store.TriggerManual || run.Run.ContinuationReason != "" || run.Run.MandateRef != nil || run.Run.UserEventID != "" || run.Work == nil || run.Work.Protocol != store.BotWorkProtocolAgentLedV1 || run.Run.SessionID != p.SessionID {
			return store.ErrBotForbidden
		}
		if !run.Run.CreatedAt.Equal(p.RecordedAt) || skillSHA(run.Instructions) != p.SourceDigest || !strings.Contains(run.Instructions, p.Quote) {
			return store.ErrBotInvalid
		}
		return nil
	}
	if p.Level == store.BotSkillImported {
		return resolveImportedSkillSource(ctx, tx, p, b)
	}
	if p.Level == store.BotSkillProposed {
		return nil
	}
	if p.GoalID != nil && p.EventID == "" {
		g, err := tx.ReadGoal(ctx, b, *p.GoalID)
		if err != nil {
			return err
		}
		hash, err := botHash(g)
		if err != nil {
			return err
		}
		if g.Revision != p.GoalRevision || hash != p.SourceDigest || !strings.Contains(g.Title+"\n"+g.Description+"\n"+g.CompletionCriteria, p.Quote) {
			return store.ErrBotConflict
		}
		return nil
	}
	run, err := tx.ReadRun(ctx, b, p.RunID)
	if err != nil {
		return err
	}
	if run.Run.SessionID != p.SessionID {
		return store.ErrBotForbidden
	}
	event, err := tx.(botSkillDB).skillSourceEvent(ctx, p.SessionID, p.EventID)
	if err != nil {
		return err
	}
	if event.ID != p.EventID || event.Partial || event.Content == nil || !event.Timestamp.Equal(p.RecordedAt) {
		return store.ErrBotInvalid
	}
	var text strings.Builder
	for _, part := range event.Content.Parts {
		if part != nil {
			text.WriteString(part.Text)
		}
	}
	if p.AttachmentID != "" { // Only complete pinned textual attachments are accepted.
		if run.Work == nil {
			return store.ErrBotInvalid
		}
		found := false
		for _, a := range run.Work.Contract.Attachments {
			if a.SourceID == p.AttachmentID && a.Blocker == "" && a.Text != "" {
				text.Reset()
				text.WriteString(a.Text)
				found = true
				break
			}
		}
		if !found {
			return store.ErrBotInvalid
		}
	}
	if skillSHA(text.String()) != p.SourceDigest || (p.Quote != "" && !strings.Contains(text.String(), p.Quote)) {
		return store.ErrBotInvalid
	}
	if p.Level == store.BotSkillOwnerSpecified {
		canonical := run.Run.UserEventID
		if canonical == "" && run.Run.Intent != store.BotIntentRegular {
			canonical = "bot-work-input:" + run.Run.ID.String()
		}
		if canonical == "" || p.EventID != canonical || event.Author != "user" || event.Content.Role != "user" || p.AttachmentID == "" && text.String() != run.Instructions {
			return store.ErrBotForbidden
		}
	} else {
		if run.Work == nil || run.Work.Candidate == nil || event.Author == "user" || event.ID != run.Work.Candidate.EventID {
			return store.ErrBotForbidden
		}
		refs := map[string]bool{}
		for _, ref := range run.Work.Candidate.AvailableEvidenceRefs() {
			refs[ref] = true
		}
		if run.Work.Protocol == store.BotWorkProtocolAgentLedV1 && run.Work.AgentWork != nil {
			for _, evidence := range run.Work.AgentWork.Evidence {
				refs[evidence.Ref] = true
			}
		}
		for _, ref := range p.EvidenceRefs {
			if !refs[ref] {
				return store.ErrBotInvalid
			}
		}
	}
	return nil
}
func validateSkillRevisionSources(ctx context.Context, tx botDraftDB, v store.BotSkillRevision) error {
	if err := v.Validate(); err != nil {
		return err
	}
	for _, e := range v.Expectations {
		// A valid quote is not authority for unrelated model-authored text.
		if e.Provenance.Level == store.BotSkillOwnerSpecified && e.Text != e.Provenance.Quote {
			return store.ErrBotInvalid
		}
		if err := resolveSkillSource(ctx, tx, e.Provenance, v.BotID); err != nil {
			return err
		}
	}
	for _, s := range v.Sources {
		if err := resolveSkillSource(ctx, tx, s.Provenance, v.BotID); err != nil {
			return err
		}
	}
	for _, p := range v.Provenance {
		if err := resolveSkillSource(ctx, tx, p, v.BotID); err != nil {
			return err
		}
	}
	return nil
}
func (r *botRepository) skillMutation(ctx context.Context, tx botDraftDB, m store.BotSkillMutation, worker bool) (*botStoredRecord, error) {
	if _, isWorker := execution.BotRunIdentityFromContext(ctx); isWorker && !worker {
		return nil, store.ErrBotForbidden
	}
	if _, err := r.auth.authorize(ctx, uuid.Nil, true); err != nil {
		return nil, err
	}
	if m.BotID == uuid.Nil || strings.TrimSpace(m.IdempotencyKey) == "" || len(m.IdempotencyKey) > 200 {
		return nil, store.ErrBotInvalid
	}
	db, ok := tx.(botSkillDB)
	if !ok {
		return nil, store.ErrBotUnsupported
	}
	var rec *botStoredRecord
	var err error
	if worker {
		if m.Lease.BotID != m.BotID {
			return nil, store.ErrBotForbidden
		}
		if err = tx.(botTranscriptDB).fenceTranscript(ctx, m.Lease, r.clock()); err != nil {
			return nil, err
		}
		rec, err = r.checkLease(ctx, tx, m.Lease, r.clock())
	} else {
		if err = db.lockSkills(ctx, m.BotID); err != nil {
			return nil, err
		}
		rec, err = tx.GetBot(ctx, m.BotID)
	}
	if err != nil {
		return nil, err
	}
	actor, err := r.auth.authorize(ctx, rec.Bot.OwnerID, true)
	if err != nil {
		return nil, err
	}
	if m.ActorID != uuid.Nil && m.ActorID != actor {
		return nil, store.ErrBotForbidden
	}
	return rec, nil
}
func skillRequestHash(q any, m store.BotSkillMutation) (string, error) {
	// Lease expiry/fence are transport authority, not the identity of an operation.
	encoded, err := json.Marshal(q)
	if err != nil {
		return "", err
	}
	var raw map[string]json.RawMessage
	if err = json.Unmarshal(encoded, &raw); err != nil {
		return "", err
	}
	var mutation map[string]json.RawMessage
	if err = json.Unmarshal(raw["Mutation"], &mutation); err != nil {
		return "", err
	}
	delete(mutation, "Lease")
	delete(mutation, "RequestDigest")
	raw["Mutation"], err = json.Marshal(mutation)
	if err != nil {
		return "", err
	}
	hash, err := botHash(raw)
	if err != nil {
		return "", err
	}
	if m.RequestDigest != "" && m.RequestDigest != hash {
		return "", store.ErrBotInvalid
	}
	return hash, nil
}
func orientationResult(w *store.BotWorkState) *store.BotSkillOrientationResult {
	out := &store.BotSkillOrientationResult{Work: w.Clone()}
	if w.Contract.Skill != nil {
		out.Invocation = w.Contract.Skill.Clone()
	}
	if w.SkillState != nil && w.SkillState.Receipt != nil {
		receipt := *w.SkillState.Receipt
		out.Receipt = &receipt
	}
	return out
}
func (r *botRepository) OrientSkill(ctx context.Context, q store.BotSkillOrientation) (*store.BotSkillOrientationResult, error) {
	hash, err := skillRequestHash(q, q.Mutation)
	if err != nil {
		return nil, err
	}
	var out *store.BotSkillOrientationResult
	err = r.db.Transaction(ctx, func(tx botDraftDB) error {
		if _, err := r.skillMutation(ctx, tx, q.Mutation, true); err != nil {
			return err
		}
		db := tx.(botSkillDB)
		run, err := tx.ReadRun(ctx, q.Mutation.BotID, q.Mutation.Lease.RunID)
		if err != nil {
			return err
		}
		if prior, ok := run.SkillRequests[q.Mutation.IdempotencyKey]; ok {
			if prior.Hash != hash {
				return store.ErrBotConflict
			}
			out = orientationResult(prior.Work)
			return nil
		}
		w := run.Work.Clone()
		if w == nil {
			return store.ErrBotUnsupported
		}
		agentWorking := w.Protocol == store.BotWorkProtocolAgentLedV1 && w.Phase == store.BotWorkWorking
		if w.Revision != q.Mutation.ExpectedRevision || (!agentWorking && w.Phase != store.BotWorkPreparing) || w.Candidate != nil || w.Review != nil || w.Contract.Skill != nil || w.SkillState != nil && w.SkillState.Stage != store.BotSkillUnoriented {
			return store.ErrBotConflict
		}
		switch q.Kind {
		case store.BotSkillNoSkillNeeded:
			if q.SkillID != nil || q.Draft != nil {
				return store.ErrBotInvalid
			}
			w.SkillState = &store.BotSkillRunState{Stage: store.BotSkillOriented, NoSkillNeeded: true}
		case store.BotSkillNeedClarification:
			// No completed orientation: the worker must ask and re-enter after an answer.
			out = orientationResult(w)
			return nil
		case store.BotSkillUseExisting, store.BotSkillCreateDraft, store.BotSkillUpdateOwnerExpectations:
			var skill *botStoredSkill
			var revision *store.BotSkillRevision
			kind := store.BotSkillSelected
			if q.Kind == store.BotSkillCreateDraft {
				if q.Draft == nil || q.SkillID != nil || strings.TrimSpace(q.Key) == "" || len(q.Key) > 128 || strings.TrimSpace(q.Title) == "" || len(q.Title) > 512 {
					return store.ErrBotInvalid
				}
				revision = q.Draft.Clone()
				if revision.BotID != q.Mutation.BotID || revision.Revision != 1 {
					return store.ErrBotInvalid
				}
				if err := validateSkillRevisionSources(ctx, tx, *revision); err != nil {
					return err
				}
				count, size, err := db.skillQuota(ctx, q.Mutation.BotID)
				if err != nil {
					return err
				}
				raw, _ := json.Marshal(revision)
				if err = store.ValidateBotSkillQuota(count, size, int64(len(raw))); err != nil {
					return err
				}
				if q.GoalID != nil {
					if _, err = tx.ReadGoal(ctx, q.Mutation.BotID, *q.GoalID); err != nil {
						return err
					}
				}
				now := r.clock()
				skill = &botStoredSkill{Skill: store.BotSkill{ID: revision.SkillID, BotID: q.Mutation.BotID, Key: q.Key, Title: q.Title, GoalID: q.GoalID, Status: store.BotSkillDraft, Revision: 1, HeadRevision: 1, HeadDigest: revision.Digest, CreatedAt: now, UpdatedAt: now}}
				if err = db.writeSkill(ctx, *skill, 0); err != nil {
					return err
				}
				if err = db.insertSkillRevision(ctx, *revision); err != nil {
					return err
				}
				kind = store.BotSkillCreated
			} else {
				if q.SkillID == nil {
					return store.ErrBotInvalid
				}
				skill, err = db.readSkill(ctx, q.Mutation.BotID, *q.SkillID)
				if err != nil {
					return err
				}
				if skill.Skill.Status == store.BotSkillArchived {
					return store.ErrBotConflict
				}
				revision, err = db.readSkillRevision(ctx, q.Mutation.BotID, *q.SkillID, skill.Skill.HeadRevision)
				if err != nil {
					return err
				}
				if q.Kind == store.BotSkillUpdateOwnerExpectations {
					if q.Draft == nil || q.Mutation.Source.Level != store.BotSkillOwnerSpecified {
						return store.ErrBotInvalid
					}
					if err = resolveSkillSource(ctx, tx, q.Mutation.Source, q.Mutation.BotID); err != nil {
						return err
					}
					next := q.Draft.Clone()
					if next.SkillID != revision.SkillID || next.BotID != revision.BotID || next.ParentRevision != revision.Revision || next.ParentDigest != revision.Digest {
						return store.ErrBotConflict
					}
					if err = validateSkillRevisionSources(ctx, tx, *next); err != nil {
						return err
					}
					// This orientation is expectation-only, never a backdoor to alter learned methods.
					a, b := revision.Clone(), next.Clone()
					a.ID = b.ID
					a.Revision = b.Revision
					a.ParentRevision = b.ParentRevision
					a.ParentDigest = b.ParentDigest
					a.Digest = b.Digest
					a.Expectations = b.Expectations
					a.ChangeReason = b.ChangeReason
					a.Provenance = b.Provenance
					if !reflect.DeepEqual(a, b) {
						return store.ErrBotInvalid
					}
					if !revision.SameKnowledge(*next) {
						_, size, err := db.skillQuota(ctx, q.Mutation.BotID)
						if err != nil {
							return err
						}
						raw, _ := json.Marshal(next)
						if err = store.ValidateBotSkillQuota(0, size, int64(len(raw))); err != nil {
							return err
						}
						old := skill.Skill.Revision
						skill.Skill.Revision++
						skill.Skill.HeadRevision = next.Revision
						skill.Skill.HeadDigest = next.Digest
						skill.Skill.UpdatedAt = r.clock()
						if err = db.insertSkillRevision(ctx, *next); err != nil {
							return err
						}
						if err = db.writeSkill(ctx, *skill, old); err != nil {
							return err
						}
						revision = next
						kind = store.BotSkillUpdated
					}
				}
			}
			invocation := &store.BotSkillInvocation{Intent: q.Intent, SkillID: revision.SkillID, Revision: revision.Revision, Digest: revision.Digest, Snapshot: *revision.Clone(), AuthorityConstraintRef: q.AuthorityConstraintRef}
			if err = invocation.Validate(q.Mutation.BotID); err != nil {
				return err
			}
			w.Contract.Skill = invocation
			// Rehash only the additive pin; every other contract and work field is retained.
			w.RequirementsDigest = store.NewBotWorkState(w.Contract).RequirementsDigest
			w.SkillState = &store.BotSkillRunState{Stage: store.BotSkillOriented, Receipt: &store.BotSkillReceipt{SchemaVersion: store.BotSkillSchemaVersion, BotID: q.Mutation.BotID, RunID: run.Run.ID, SkillID: revision.SkillID, Kind: kind, UsedRevision: revision.Revision, Digest: revision.Digest}}
		default:
			return store.ErrBotInvalid
		}
		if agentWorking && w.SkillState != nil {
			selectedAt := r.clock()
			w.SkillState.SelectedAt = &selectedAt
			w.SkillState.SelectionRevision = w.Revision + 1
			w.SkillState.CreatedDuringWork = q.Kind == store.BotSkillCreateDraft
		}
		w.Revision++
		if err = w.Validate(); err != nil {
			return err
		}
		run.Work = w
		if run.SkillRequests == nil {
			run.SkillRequests = map[string]botSkillReplay{}
		}
		run.SkillRequests[q.Mutation.IdempotencyKey] = botSkillReplay{Hash: hash, Work: w.Clone()}
		if err = tx.(botWorkDB).updateWorkRun(ctx, *run, q.Mutation.Lease, r.clock()); err != nil {
			return err
		}
		out = orientationResult(w)
		return nil
	})
	return out, err
}

func (r *botRepository) StageSkillLearning(ctx context.Context, q store.BotSkillLearningRequest) (*store.BotWorkState, error) {
	hash, err := skillRequestHash(q, q.Mutation)
	if err != nil {
		return nil, err
	}
	var out *store.BotWorkState
	err = r.db.Transaction(ctx, func(tx botDraftDB) error {
		if _, err := r.skillMutation(ctx, tx, q.Mutation, true); err != nil {
			return err
		}
		run, err := tx.ReadRun(ctx, q.Mutation.BotID, q.Mutation.Lease.RunID)
		if err != nil {
			return err
		}
		if prior, ok := run.SkillRequests[q.Mutation.IdempotencyKey]; ok {
			if prior.Hash != hash {
				return store.ErrBotConflict
			}
			out = prior.Work.Clone()
			return nil
		}
		w := run.Work.Clone()
		if w == nil {
			return store.ErrBotUnsupported
		}
		if w.Revision != q.ExpectedWorkRevision || w.SkillState == nil || w.SkillState.Stage != store.BotSkillOriented || w.Contract.Skill == nil {
			return store.ErrBotConflict
		}
		if q.Proposal != nil {
			p := q.Proposal
			if w.Protocol != store.BotWorkProtocolAgentLedV1 || w.Phase != store.BotWorkWorking || p.CallID == "" || len(p.CallID) > 512 || len(p.Method) > store.BotSkillMaxMethodSteps || len(p.EvaluationRules) > store.BotSkillMaxMethodSteps || len(p.EvidenceRefs) > 25 {
				return store.ErrBotInvalid
			}
			raw, _ := json.Marshal(p)
			if len(raw) > store.BotSkillMaxRevisionBytes {
				return store.ErrBotInvalid
			}
			for _, ref := range p.EvidenceRefs {
				found := false
				for _, evidence := range w.AgentWork.Evidence {
					if evidence.Ref == ref {
						found = true
					}
				}
				if !found {
					return store.ErrBotForbidden
				}
			}
			w.SkillState.Proposal = &store.BotSkillProposal{}
			_ = json.Unmarshal(raw, w.SkillState.Proposal)
			w.Revision++
			if err := w.Validate(); err != nil {
				return err
			}
			run.Work = w
			if run.SkillRequests == nil {
				run.SkillRequests = map[string]botSkillReplay{}
			}
			run.SkillRequests[q.Mutation.IdempotencyKey] = botSkillReplay{Hash: hash, Work: w.Clone()}
			if err := tx.(botWorkDB).updateWorkRun(ctx, *run, q.Mutation.Lease, r.clock()); err != nil {
				return err
			}
			out = w.Clone()
			return nil
		}
		if q.Failure != "" {
			if q.Failure != store.BotSkillDeferred && q.Failure != store.BotSkillRejected {
				return store.ErrBotInvalid
			}
			if w.Phase != store.BotWorkAccepted && w.Phase != store.BotWorkBlocked && !(w.Protocol == store.BotWorkProtocolAgentLedV1 && w.Phase == store.BotWorkFinished) {
				return store.ErrBotConflict
			}
			w.SkillState.Stage = store.BotSkillLearningDeferred
			w.SkillState.Receipt = &store.BotSkillReceipt{SchemaVersion: store.BotSkillSchemaVersion, BotID: run.Run.BotID, RunID: run.Run.ID, SkillID: w.Contract.Skill.SkillID, UsedRevision: w.Contract.Skill.Revision, Kind: q.Failure, Reason: "Learning assessment or staging could not be verified; no learning was applied."}
			w.Revision++
			if err := w.Validate(); err != nil {
				return err
			}
			run.Work = w
			if run.SkillRequests == nil {
				run.SkillRequests = map[string]botSkillReplay{}
			}
			run.SkillRequests[q.Mutation.IdempotencyKey] = botSkillReplay{Hash: hash, Work: w.Clone()}
			if err := tx.(botWorkDB).updateWorkRun(ctx, *run, q.Mutation.Lease, r.clock()); err != nil {
				return err
			}
			out = w.Clone()
			return nil
		}
		skill, err := tx.(botSkillDB).readSkill(ctx, q.Mutation.BotID, q.Learning.SkillID)
		if err != nil {
			return err
		}
		if skill.Skill.Revision != q.Mutation.ExpectedRevision || skill.Skill.HeadRevision != q.Learning.ParentRevision || skill.Skill.HeadDigest != q.Learning.ParentDigest || skill.Skill.Status == store.BotSkillArchived {
			return store.ErrBotConflict
		}
		if w.Candidate == nil { // Feedback-only learning must cite a canonical owner source.
			if q.Mutation.Source.Level != store.BotSkillOwnerSpecified {
				return store.ErrBotInvalid
			}
			if err = resolveSkillSource(ctx, tx, q.Mutation.Source, q.Mutation.BotID); err != nil {
				return err
			}
			if q.Learning.CandidateDigest != q.Mutation.Source.SourceDigest || q.Learning.EvidenceDigest != q.Mutation.Source.SourceDigest {
				return store.ErrBotInvalid
			}
		} else {
			evidence, err := botHash(w.Candidate.AvailableEvidenceRefs())
			if err != nil {
				return err
			}
			if q.Learning.CandidateDigest != w.Candidate.Digest || q.Learning.EvidenceDigest != evidence {
				return store.ErrBotInvalid
			}
		}
		if !reflect.DeepEqual(q.Learning.Proposed.Expectations, w.Contract.Skill.Snapshot.Expectations) {
			if q.Mutation.Source.Level != store.BotSkillOwnerSpecified {
				return store.ErrBotForbidden
			}
			if err = resolveSkillSource(ctx, tx, q.Mutation.Source, q.Mutation.BotID); err != nil {
				return err
			}
		}
		if err = validateSkillRevisionSources(ctx, tx, q.Learning.Proposed); err != nil {
			return err
		}
		learning := q.Learning
		learning.Proposed = *q.Learning.Proposed.Clone()
		w.SkillState.Stage = store.BotSkillLearningStaged
		w.SkillState.Receipt = nil
		w.SkillState.Learning = &learning
		w.Revision++
		if err = w.Validate(); err != nil {
			return err
		}
		run.Work = w
		if run.SkillRequests == nil {
			run.SkillRequests = map[string]botSkillReplay{}
		}
		run.SkillRequests[q.Mutation.IdempotencyKey] = botSkillReplay{Hash: hash, Work: w.Clone()}
		if err = tx.(botWorkDB).updateWorkRun(ctx, *run, q.Mutation.Lease, r.clock()); err != nil {
			return err
		}
		out = w.Clone()
		return nil
	})
	return out, err
}
func (r *botRepository) ChangeSkillLifecycle(ctx context.Context, q store.BotSkillLifecycleRequest) (*store.BotSkill, error) {
	if strings.TrimSpace(q.Reason) == "" || len(q.Reason) > 4096 {
		return nil, store.ErrBotInvalid
	}
	hash, err := skillRequestHash(q, q.Mutation)
	if err != nil {
		return nil, err
	}
	var out *store.BotSkill
	err = r.db.Transaction(ctx, func(tx botDraftDB) error {
		worker := q.Mutation.Lease.RunID != uuid.Nil
		if _, err := r.skillMutation(ctx, tx, q.Mutation, worker); err != nil {
			return err
		}
		db := tx.(botSkillDB)
		s, err := db.readSkill(ctx, q.Mutation.BotID, q.SkillID)
		if err != nil {
			return err
		}
		if prior, ok := s.Requests[q.Mutation.IdempotencyKey]; ok {
			if prior.Hash != hash {
				return store.ErrBotConflict
			}
			v := *prior.Skill
			out = &v
			return nil
		}
		if s.Skill.Revision != q.Mutation.ExpectedRevision {
			return store.ErrBotConflict
		}
		verified := false
		if q.Status == store.BotSkillActive { // Activation requires an actual accepted, oriented run.
			if !worker {
				return store.ErrBotConflict
			}
			run, err := tx.ReadRun(ctx, q.Mutation.BotID, q.Mutation.Lease.RunID)
			if err != nil {
				return err
			}
			verified = run.Work != nil && run.Work.Phase == store.BotWorkAccepted && run.Work.Validate() == nil && run.Work.Contract.Skill != nil && run.Work.Contract.Skill.SkillID == q.SkillID && run.Work.Contract.Skill.Digest == s.Skill.HeadDigest
		}
		explicit := !worker
		if worker && q.Mutation.Source.Level == store.BotSkillOwnerSpecified {
			if err = resolveSkillSource(ctx, tx, q.Mutation.Source, q.Mutation.BotID); err != nil {
				return err
			}
			explicit = true
		}
		if err = store.ValidateBotSkillLifecycle(s.Skill.Status, q.Status, explicit, verified); err != nil {
			return err
		}
		old := s.Skill.Revision
		s.Skill.Status = q.Status
		s.Skill.Revision++
		s.Skill.UpdatedAt = r.clock()
		if len(s.Requests) >= store.BotSkillMaxRevisions {
			return store.ErrBotInvalid
		}
		if s.Requests == nil {
			s.Requests = map[string]botSkillReplay{}
		}
		v := s.Skill
		s.Requests[q.Mutation.IdempotencyKey] = botSkillReplay{Hash: hash, Skill: &v}
		if err = db.writeSkill(ctx, *s, old); err != nil {
			return err
		}
		out = &v
		return nil
	})
	return out, err
}
func (r *botRepository) ConvertLegacySkill(ctx context.Context, q store.BotSkillLegacyConversion) (*store.BotSkill, error) {
	return r.convertLegacySkill(ctx, q)
}
