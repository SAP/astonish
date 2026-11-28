package agent

import (
	"slices"
	"strings"
)

// PlanSnapshot is the durable, authoritative execution view. Revision changes
// only when a definition is accepted; Sequence also changes for runtime events.
type PlanSnapshot struct {
	PlanID          string                `json:"plan_id"`
	Revision        int64                 `json:"revision"`
	Sequence        int64                 `json:"sequence"`
	Goal            string                `json:"goal"`
	Document        PlanDocumentInfo      `json:"document"`
	Steps           []PlanStepInfo        `json:"steps"`
	PendingRevision *PlanRevisionProposal `json:"pending_revision,omitempty"`
}

// IsFullyAccepted requires Results and every phase complete. Neither lifecycle
// labels, failed phases, nor checked boxes without Results establish success.
func (s PlanSnapshot) IsFullyAccepted() bool {
	if len(s.Steps) == 0 || strings.TrimSpace(s.Document.Results) == "" {
		return false
	}
	for _, step := range s.Steps {
		if step.Status != "complete" {
			return false
		}
	}
	return true
}

// Clone returns an independent value, including nested file and proposal slices.
func (s PlanSnapshot) Clone() PlanSnapshot {
	s.Document = s.Document.Clone()
	s.Steps = clonePlanStepInfos(s.Steps)
	s.PendingRevision = s.PendingRevision.Clone()
	return s
}

func clonePlanStepInfos(steps []PlanStepInfo) []PlanStepInfo {
	steps = slices.Clone(steps)
	for i := range steps {
		steps[i].Files = slices.Clone(steps[i].Files)
	}
	return steps
}

// Clone makes document metadata safe to publish outside the plan lock.
func (d PlanDocumentInfo) Clone() PlanDocumentInfo {
	d.PendingRevision = d.PendingRevision.Clone()
	return d
}

// RenderMarkdown snapshots the complete plan, including statuses and recorded
// evidence, for non-filesystem session backends.
func (p *PlanState) RenderMarkdown() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	goal, steps := p.snapshotLocked()
	return renderPlanMarkdownWithDoc(goal, p.doc, steps)
}
