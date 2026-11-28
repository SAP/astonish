package tui

import (
	"strings"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/tui/events"
)

type planPhaseState string

const (
	planPhasePending  planPhaseState = "pending"
	planPhaseRunning  planPhaseState = "running"
	planPhaseComplete planPhaseState = "complete"
	planPhaseFailed   planPhaseState = "failed"
	planPhaseUnknown  planPhaseState = "unknown"
)

type planProgressState string

const (
	planProgressUnavailable              planProgressState = "unavailable"
	planProgressAwaitingApproval         planProgressState = "awaiting-approval"
	planProgressChangesRequested         planProgressState = "changes-requested"
	planProgressDeclined                 planProgressState = "declined"
	planProgressActive                   planProgressState = "active"
	planProgressIdleIncomplete           planProgressState = "idle-incomplete"
	planProgressReviewPaused             planProgressState = "review-paused"
	planProgressFailed                   planProgressState = "failed"
	planProgressAwaitingCompletionRecord planProgressState = "awaiting-completion-record"
	planProgressCompleted                planProgressState = "completed"
)

type planStepSummary struct {
	Total, Complete, Running, Pending, Failed, Unknown int
}

func phaseState(status string) planPhaseState {
	switch planPhaseState(status) {
	case "", planPhasePending:
		return planPhasePending
	case planPhaseRunning:
		return planPhaseRunning
	case planPhaseComplete:
		return planPhaseComplete
	case planPhaseFailed:
		return planPhaseFailed
	default:
		return planPhaseUnknown
	}
}

// summarizePlanSteps is shared by the scrolling card and persistent progress.
// Only completed phases count as completed work; failures are never "done".
func summarizePlanSteps(steps []agent.PlanStepInfo) planStepSummary {
	s := planStepSummary{Total: len(steps)}
	for _, step := range steps {
		switch phaseState(step.Status) {
		case planPhasePending:
			s.Pending++
		case planPhaseRunning:
			s.Running++
		case planPhaseComplete:
			s.Complete++
		case planPhaseFailed:
			s.Failed++
		case planPhaseUnknown:
			s.Unknown++
		}
	}
	return s
}

type planPhaseView struct {
	Name, Description, Summary, ParallelGroup string
	State                                     planPhaseState
	Current                                   bool // Every recorded running phase, including concurrent phases.
	Live                                      bool // Presentation only; false for idle/restored transcripts.
}

type planProgressView struct {
	Present            bool
	Available          bool
	PlanID, Goal       string
	Revision, Sequence int64
	State              planProgressState
	Counts             planStepSummary
	Percent            int
	PercentAvailable   bool
	Phases             []planPhaseView
	ReviewPending      string
}

// latestPlanProgress projects only the last plan in transcript order. Sequence
// numbers belong to an identity, not to the transcript. The reducer has already
// rejected stale snapshots; a malformed latest plan must not reveal an older one.
func latestPlanProgress(items []events.Item) planProgressView {
	for i := len(items) - 1; i >= 0; i-- {
		it := items[i]
		if it.Kind != events.ItemPlan {
			continue
		}
		v := planProgressView{Present: true, State: planProgressUnavailable}
		var doc agent.PlanDocumentInfo
		var steps []agent.PlanStepInfo
		if s := it.PlanSnapshot; s != nil {
			v.PlanID, v.Revision, v.Sequence, v.Goal = s.PlanID, s.Revision, s.Sequence, s.Goal
			doc, steps = s.Document, s.Steps
		} else {
			var err error
			doc, v.Goal, steps, err = agent.ParsePlanDocument(it.Content)
			if err != nil {
				return v
			}
			v.PlanID, v.Revision, v.Sequence = doc.PlanID, doc.Revision, doc.Sequence
		}
		v.Available = true
		v.Counts = summarizePlanSteps(steps)
		v.PercentAvailable = v.Counts.Total > 0
		if v.PercentAvailable {
			v.Percent = 100 * v.Counts.Complete / v.Counts.Total
		}
		for _, step := range steps {
			state := phaseState(step.Status)
			v.Phases = append(v.Phases, planPhaseView{Name: step.Name, Description: step.Description,
				Summary: step.Summary, ParallelGroup: step.ParallelGroup, State: state, Current: state == planPhaseRunning})
		}
		v.ReviewPending = doc.CodeReviewPending
		switch {
		case it.PlanStatus == events.PlanDeclined:
			v.State = planProgressDeclined
		case it.PlanStatus == events.PlanChangesRequested:
			v.State = planProgressChangesRequested
		case it.PlanStatus == events.PlanPending && doc.Lifecycle == "":
			v.State = planProgressAwaitingApproval
		case strings.TrimSpace(doc.Results) != "":
			v.State = planProgressCompleted
		case doc.CodeReviewPending != "":
			v.State = planProgressReviewPaused
		case v.Counts.Failed > 0:
			v.State = planProgressFailed
		case v.Counts.Running > 0:
			v.State = planProgressActive
		case v.PercentAvailable && v.Counts.Complete == v.Counts.Total:
			v.State = planProgressAwaitingCompletionRecord
		default:
			v.State = planProgressIdleIncomplete
		}
		return v
	}
	return planProgressView{}
}

// withStreaming adds ephemeral presentation without changing the recorded phase
// state or progress counts. Restored running phases are current but not workers.
func (v planProgressView) withStreaming(streaming bool) planProgressView {
	v.Phases = append([]planPhaseView(nil), v.Phases...)
	for i := range v.Phases {
		v.Phases[i].Live = streaming && v.Phases[i].Current && (v.State == planProgressActive || v.State == planProgressFailed)
	}
	if !streaming && v.State == planProgressActive {
		v.State = planProgressIdleIncomplete
	}
	return v
}
