package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/scheduler"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/genai"
)

// Called under completionMu. Only current tool responses consume the server
// ledger; neither model text nor serialized result maps can mint attestations.
func (cr *ChatRunner) observeBotToolOutcome(role string, response *genai.FunctionResponse) {
	if _, ok := execution.BotRunIdentityFromContext(cr.ctx); !ok || role == "model" {
		return
	}
	cr.completion.toolResponses++
	// Capture the actual returned source/delegate content, not just outcome claims.
	// Oversized content is explicitly unverified and blocks work acceptance.
	if raw, err := json.Marshal(response.Response); err == nil {
		if len(raw) > store.BotWorkMaxCandidateBytes {
			cr.completion.Report.Unverified = append(cr.completion.Report.Unverified, "Required tool response content exceeds assessment limit.")
		} else {
			if cr.completion.reportBodies == nil {
				cr.completion.reportBodies = make(map[string]string)
			}
			cr.completion.reportBodies["tool-response:"+response.ID] = string(raw)
		}
	}
	outcome, ok := bot.TakeToolOutcome(cr.ctx, response.ID)
	if capture, _ := cr.ctx.Value(botApprovalCaptureKey{}).(*botApprovalCapture); capture != nil {
		// Consume only identity-bound, guard-captured nonexecution. An existing
		// ledger outcome (especially a failed effect) always remains authoritative.
		if capture.takeNonexecution(response.ID, response.Name) && !ok {
			return
		}
	}
	if !ok || !outcome.Verified {
		cr.completion.Report.Unverified = append(cr.completion.Report.Unverified, fmt.Sprintf("Tool %s returned without independently verified outcome metadata.", response.Name))
		if ok {
			cr.completion.Report.Unverified = append(cr.completion.Report.Unverified, outcome.Unverified...)
		}
		return
	}
	report := &cr.completion.Report
	report.Unverified = append(report.Unverified, outcome.Unverified...)
	if capture := outcome.RoutineCapture; capture != nil {
		identity, _ := execution.BotRunIdentityFromContext(cr.ctx)
		operationID, _ := response.Response["operation_id"].(string)
		status := fmt.Sprint(response.Response["status"])
		decision := fmt.Sprint(response.Response["decision"])
		if !capture.ValidFor(identity) || operationID != capture.OperationID.String() || status != string(store.BotOperationPending) || decision != string(store.BotOperationExecute) || outcome.ScheduleCapture != nil || len(outcome.Changed) != 0 || len(outcome.Artifacts) != 0 || outcome.NextRunAt != nil {
			report.Unverified = append(report.Unverified, "Routine enqueue receipt did not match the current run and pending tool response.")
			return
		}
		cr.completion.pendingControls = append(cr.completion.pendingControls, botWorkPendingControl{OperationID: capture.OperationID.String(), Operation: capture.Operation, Acknowledgment: capture.Acknowledgment(), Payload: json.RawMessage(capture.PayloadJSON)})
		report.Unchanged = append(report.Unchanged, capture.Observation())
		if report.Summary != "" {
			report.Summary += "\n\n"
		}
		report.Summary += capture.Acknowledgment()
		return
	}
	if capture := outcome.ScheduleCapture; capture != nil {
		identity, _ := execution.BotRunIdentityFromContext(cr.ctx)
		operationID, _ := response.Response["operation_id"].(string)
		status, _ := response.Response["status"].(string)
		if !capture.ValidFor(identity) || operationID != capture.OperationID.String() || status != string(store.BotOperationPending) || len(outcome.Changed) != 0 || len(outcome.Artifacts) != 0 || outcome.NextRunAt != nil {
			report.Unverified = append(report.Unverified, "Schedule enqueue receipt did not match the current run and pending tool response.")
			return
		}
		// This observation is of a pending record, not a changed schedule. The
		// server acknowledgment, not premature model 'Done', owns the summary.
		cr.completion.pendingControls = append(cr.completion.pendingControls, botWorkPendingControl{OperationID: capture.OperationID.String(), Operation: capture.Operation, Acknowledgment: capture.Acknowledgment(), Payload: json.RawMessage(capture.PayloadJSON)})
		report.Unchanged = append(report.Unchanged, capture.Observation())
		if report.Summary != "" {
			report.Summary += "\n\n"
		}
		report.Summary += capture.Acknowledgment()
		return
	}
	appendObservations := func(dst *[]store.BotEvidence, observations []store.BotEvidence) {
		for _, e := range observations {
			if !bot.ValidObservation(e) {
				report.Unverified = append(report.Unverified, "Tool outcome contained incomplete observation evidence.")
				continue
			}
			duplicate := false
			for _, prior := range *dst {
				if prior == e {
					duplicate = true
					break
				}
			}
			if !duplicate {
				*dst = append(*dst, e)
			}
		}
	}
	appendObservations(&report.Changed, outcome.Changed)
	appendObservations(&report.Unchanged, outcome.Unchanged)
	for _, artifact := range outcome.Artifacts {
		a := artifact.Artifact
		digest := sha256.Sum256([]byte(artifact.Body))
		if a.ID == uuid.Nil || a.HomeID == uuid.Nil || a.RelativePath == "" || path.IsAbs(a.RelativePath) || path.Clean(a.RelativePath) != a.RelativePath || a.RelativePath == ".." || strings.HasPrefix(a.RelativePath, "../") || strings.Contains(a.RelativePath, "\\") || a.SHA256 != hex.EncodeToString(digest[:]) {
			report.Unverified = append(report.Unverified, "Tool report artifact metadata or digest could not be verified.")
			continue
		}
		report.Artifacts = append(report.Artifacts, a)
		if cr.completion.reportBodies == nil {
			cr.completion.reportBodies = make(map[string]string)
		}
		cr.completion.reportBodies[a.RelativePath] = artifact.Body
	}
	if outcome.NextRunAt != nil && !outcome.NextRunAt.IsZero() {
		next := outcome.NextRunAt.UTC()
		report.NextRunAt = &next
	}
}

func botCompletionReport(completion ChatCompletion, req bot.RunRequest) store.BotReport {
	report := completion.Report
	// Do not retain artifacts (or use their bodies) from another durable home.
	artifacts := report.Artifacts
	report.Artifacts = nil
	bodies := make(map[string]string)
	for _, a := range artifacts {
		if a.HomeID != req.Bot.Home.ID || a.HomeID == uuid.Nil {
			report.Unverified = append(report.Unverified, "Report artifact does not belong to this Bot home.")
			continue
		}
		report.Artifacts = append(report.Artifacts, a)
		bodies[a.RelativePath] = completion.reportBodies[a.RelativePath]
	}
	if report.Summary == "" {
		report.Summary = scheduler.ResolveAdaptiveReportBody(completion.FinalText, bodies)
	}
	if completion.Usage.ToolCalls > completion.toolResponses {
		report.Unverified = append(report.Unverified, "Some tool calls have no observed response.")
	}
	return report
}
