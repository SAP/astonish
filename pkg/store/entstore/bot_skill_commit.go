package entstore

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"strconv"
	"strings"

	"github.com/SAP/astonish/pkg/store"
)

// commitBotSkill runs inside CommitRun's lease-fenced transaction. Semantic
// conflicts produce a durable receipt, never discard an accepted deliverable.
// Database errors still roll the entire publication back.
func (r *botRepository) commitBotSkill(ctx context.Context, tx botDraftDB, saved *botStoredRun, q *store.BotRunCommit) error {
	w := saved.Work
	if w == nil {
		return nil
	}
	// Only the durable, lease-fenced skill state may supply a receipt. Validate
	// its invocation/run binding before copying it into publication data.
	if err := w.Validate(); err != nil {
		return err
	}
	if w.Contract.Skill == nil || w.SkillState == nil {
		return nil
	}
	state := w.SkillState
	agent := w.Protocol == store.BotWorkProtocolAgentLedV1
	completed := q.Status == store.RunSucceeded && (w.Phase == store.BotWorkAccepted || agent && w.Phase == store.BotWorkFinished && (w.AgentWork.Disposition == store.AgentWorkAnswered || w.AgentWork.Disposition == store.AgentWorkComplete))
	used := !agent
	if agent && !state.CreatedDuringWork && state.SelectedAt != nil {
		// Resolve the actual captured event, not the time evidence was bound to
		// the journal. Binding an older response after selection is not use.
		for _, evidence := range w.AgentWork.Evidence {
			event, err := tx.(botSkillDB).skillSourceEvent(ctx, saved.Run.SessionID, evidence.EventID)
			if err != nil {
				return err
			}
			if event.Timestamp.After(*state.SelectedAt) && !event.Partial && event.Content != nil {
				for _, part := range event.Content.Parts {
					if part != nil && part.FunctionResponse != nil && part.FunctionResponse.ID == evidence.CallID {
						used = true
					}
				}
			}
		}
	}
	if state.Receipt != nil {
		receipt := *state.Receipt
		q.Report.Skill = &receipt
	}
	if completed && used {
		// Successful use activates a draft independently of whether it learned a URL.
		db, ok := tx.(botSkillDB)
		if !ok {
			return store.ErrBotUnsupported
		}
		head, err := db.readSkill(ctx, saved.Run.BotID, w.Contract.Skill.SkillID)
		if err != nil {
			return err
		}
		if head.Skill.Status == store.BotSkillDraft && head.Skill.HeadDigest == w.Contract.Skill.Digest {
			prior := head.Skill.Revision
			head.Skill.Status = store.BotSkillActive
			head.Skill.Revision++
			head.Skill.UpdatedAt = r.clock()
			if err := db.writeSkill(ctx, *head, prior); err != nil {
				return err
			}
		}
	}
	if state.Stage != store.BotSkillLearningStaged || !q.Status.Terminal() {
		return nil
	}
	inv := w.Contract.Skill
	receipt := &store.BotSkillReceipt{SchemaVersion: store.BotSkillSchemaVersion, BotID: saved.Run.BotID, RunID: saved.Run.ID, SkillID: inv.SkillID, UsedRevision: inv.Revision}
	finish := func(kind store.BotSkillReceiptKind, reason string) error {
		receipt.Kind, receipt.Reason = kind, reason
		state.Stage = store.BotSkillLearningDeferred
		if kind == store.BotSkillUpdated || kind == store.BotSkillUnchanged {
			state.Stage = store.BotSkillLearningCommitted
		}
		state.Receipt = receipt
		w.Revision++
		if err := w.Validate(); err != nil {
			return err
		}
		q.Report.Skill = receipt
		if q.Report.Quality != nil {
			quality := *q.Report.Quality
			quality.WorkRevision = w.Revision
			q.Report.Quality = &quality
		}
		return nil
	}
	if q.Status == store.RunCancelled || q.Status == store.RunInterrupted {
		return finish(store.BotSkillDeferred, "Run did not complete; learning was not applied.")
	}
	l := state.Learning
	if l == nil || w.Candidate == nil || (!agent && w.Review == nil) {
		return finish(store.BotSkillDeferred, "No assessed execution evidence; learning was not applied.")
	}
	if l.CandidateDigest != w.Candidate.Digest {
		return finish(store.BotSkillRejected, "Learning refers to a different candidate.")
	}
	evidence, err := botHash(w.Candidate.AvailableEvidenceRefs())
	if err != nil {
		return err
	}
	if evidence != l.EvidenceDigest {
		return finish(store.BotSkillRejected, "Learning evidence changed.")
	}
	// Advisory method/output/evaluation knowledge may evolve; owner authority cannot.
	old, next := inv.Snapshot, l.Proposed
	if !reflect.DeepEqual(old.Expectations, next.Expectations) || old.Responsibility != next.Responsibility {
		return finish(store.BotSkillRejected, "Learning cannot reinterpret owner expectations.")
	}
	if err = validateSkillRevisionSources(ctx, tx, next); err != nil {
		if errors.Is(err, store.ErrBotInvalid) || errors.Is(err, store.ErrBotForbidden) || errors.Is(err, store.ErrBotConflict) || errors.Is(err, store.ErrBotNotFound) {
			return finish(store.BotSkillRejected, "Canonical learning sources could not be verified.")
		}
		return err
	}
	methodChanged := !reflect.DeepEqual(old.Method, next.Method) || old.OutputFormat != next.OutputFormat || !reflect.DeepEqual(old.EvaluationRules, next.EvaluationRules)
	if agent && methodChanged {
		p := state.Proposal
		if !p.HasEvidence(w.AgentWork.Evidence) {
			return finish(store.BotSkillRejected, "Method learning requires evidence supplied by the method proposal.")
		}
		// Explicit refs may support only the exact proposed changes, not a
		// different replacement smuggled into terminal staging.
		if (!reflect.DeepEqual(old.Method, next.Method) && !reflect.DeepEqual(p.Method, next.Method)) || (old.OutputFormat != next.OutputFormat && p.OutputFormat != next.OutputFormat) || (!reflect.DeepEqual(old.EvaluationRules, next.EvaluationRules) && !reflect.DeepEqual(p.EvaluationRules, next.EvaluationRules)) {
			return finish(store.BotSkillRejected, "Method learning differs from the evidence-bound proposal.")
		}
	}
	if methodChanged && !completed {
		return finish(store.BotSkillDeferred, "Procedural learning requires an accepted execution.")
	}
	if old.SameKnowledge(next) {
		receipt.CommittedRevision, receipt.Digest = old.Revision, old.Digest
		return finish(store.BotSkillUnchanged, "No knowledge changed.")
	}
	// A candidate citation or LLM verdict alone cannot establish a learned method.
	observed := false
	for _, p := range next.Provenance {
		if p.Level == store.BotSkillObserved && p.RunID == saved.Run.ID {
			for _, ref := range p.EvidenceRefs {
				for i, e := range w.Candidate.Observations {
					if ref == fmtSkillObservation(i) && e.Kind == store.BotEvidenceObservation && e.ValidateCompletion() == nil {
						observed = true
					}
				}
			}
		}
	}
	if agent {
		for _, source := range next.Sources {
			if source.Provenance.RunID == saved.Run.ID && source.Observation != nil {
				observed = true
			}
		}
		for _, p := range next.Provenance {
			if p.RunID == saved.Run.ID && p.Level == store.BotSkillObserved {
				for _, ref := range p.EvidenceRefs {
					for _, e := range w.AgentWork.Evidence {
						if e.Ref == ref {
							observed = true
						}
					}
				}
			}
		}
	}
	if !observed {
		return finish(store.BotSkillDeferred, "No actual execution observation supports learning.")
	}
	db, ok := tx.(botSkillDB)
	if !ok {
		return store.ErrBotUnsupported
	}
	head, err := db.readSkill(ctx, saved.Run.BotID, inv.SkillID)
	if err != nil {
		return err
	}
	if head.Skill.Status == store.BotSkillArchived || head.Skill.HeadRevision != l.ParentRevision || head.Skill.HeadDigest != l.ParentDigest {
		return finish(store.BotSkillConflict, "The skill changed after this run pinned its revision.")
	}
	// Content safety is checked independently of the runtime's request-local secret
	// redactor. Credentials in URL userinfo/query/fragment are never reusable sources.
	for _, s := range next.Sources {
		previous := false
		for _, p := range old.Sources {
			if reflect.DeepEqual(p, s) {
				previous = true
				break
			}
		}
		if previous {
			continue
		}
		matched := false
		for i, observation := range w.Candidate.Observations {
			if observation.Source == s.URL && observation.Kind == store.BotEvidenceObservation && observation.ValidateCompletion() == nil && s.LastObservedAt != nil && observation.ObservedAt.Equal(*s.LastObservedAt) && s.Provenance.RunID == saved.Run.ID && s.Provenance.EventID == w.Candidate.EventID && s.Provenance.Level == store.BotSkillObserved {
				for _, ref := range s.Provenance.EvidenceRefs {
					if ref == fmtSkillObservation(i) {
						matched = true
					}
				}
			}
		}
		if agent && s.Observation != nil {
			for _, fact := range w.AgentWork.SourceAccess {
				if fact.OriginalURL == s.URL && fact.CallID == s.Observation.CallID && fact.Status == s.Observation.Status && fact.HTTPStatus == s.Observation.HTTPStatus && fact.Truncated == s.Observation.Truncated && s.LastObservedAt != nil && fact.ObservedAt.Equal(*s.LastObservedAt) && s.Provenance.RunID == saved.Run.ID && s.Provenance.EventID == w.Candidate.EventID && s.Provenance.Level == store.BotSkillObserved && len(s.Provenance.EvidenceRefs) == 1 && s.Provenance.EvidenceRefs[0] == fact.EvidenceRef {
					matched = true
				}
			}
		}
		if !matched {
			return finish(store.BotSkillRejected, "Source learning is not backed by its exact access observation.")
		}
		if !safeSkillCommitURL(s.URL) {
			return finish(store.BotSkillRejected, "Sensitive source URL was not retained.")
		}
	}
	raw, _ := json.Marshal(next)
	_, size, err := db.skillQuota(ctx, saved.Run.BotID)
	if err != nil {
		return err
	}
	if err = store.ValidateBotSkillQuota(0, size, int64(len(raw))); err != nil {
		return finish(store.BotSkillDeferred, "Skill storage limit reached.")
	}
	prior := head.Skill.Revision
	head.Skill.Revision++
	head.Skill.HeadRevision = next.Revision
	head.Skill.HeadDigest = next.Digest
	head.Skill.UpdatedAt = r.clock()
	if completed && used {
		head.Skill.Status = store.BotSkillActive
	}
	if err = db.insertSkillRevision(ctx, next); err != nil {
		return err
	}
	if err = db.writeSkill(ctx, *head, prior); err != nil {
		return err
	}
	receipt.CommittedRevision, receipt.Digest = next.Revision, next.Digest
	return finish(store.BotSkillUpdated, "")
}

func fmtSkillObservation(i int) string { return "observation:" + strconv.Itoa(i) }
func safeSkillCommitURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Fragment != "" || u.RawQuery != "" {
		return false
	}
	return (u.Scheme == "https" || u.Scheme == "http") && u.Hostname() != "" && !strings.Contains(strings.ToLower(raw), "{{credential:")
}
