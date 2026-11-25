package api

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
)

// Recovery saves accepted communication, never retries employee work or upgrades
// a partial outcome. Physical object keys are opaque, not run identities.
func recoverBotFileDelivery(ctx context.Context, req bot.RunRequest, resources *BotChatResources, work *store.BotWorkState, completion ChatCompletion, result *bot.RunResult) error {
	if work == nil || work.AgentWork == nil {
		return nil
	}
	// A configured recovery capability is not delivery intent. Ordinary answers
	// and schedule confirmations must not acquire a write requirement merely
	// because this runtime can save files. Explicit file delivery remains gated.
	if work.AgentWork.DeliveryIntent != store.BotDeliveryFileDeliverable {
		return nil
	}
	if err := botWorkBoundary(ctx, req); err != nil {
		return err
	}
	hasReceipt := func() bool {
		if resources == nil || resources.PublishedFiles == nil {
			return false
		}
		// This callback is bound by the factory to a private exact-run accumulator.
		// A caller/model artifact, even with a plausible key, is not a receipt.
		for _, trusted := range resources.PublishedFiles() {
			for _, a := range result.Report.Artifacts {
				if a.ContentRef != nil && a.ContentRef.Validate() == nil && a.ContentRef.Kind == store.BotContentArtifact && a.ContentRef.Scope.BotID == req.Bot.ID && a.ContentRef.Scope.OwnerID == req.Bot.OwnerID.String() && a.HomeID == req.Bot.Home.ID && a.SHA256 == a.ContentRef.SHA256 && a.SizeBytes == a.ContentRef.SizeBytes && reflect.DeepEqual(a, trusted) {
					return true
				}
			}
		}
		return false
	}
	if hasReceipt() {
		return nil
	}
	reason := ""
	switch {
	case work.AgentWork.DeliverableFormat != store.BotDeliverableMarkdown:
		reason = "format_not_markdown"
	case resources == nil || resources.RecoverFileDelivery == nil:
		reason = "recovery_unavailable"
	case completion.Blocked:
		reason = "completion_blocked"
	case completion.Err != nil && !botCanRetainReadOnlyFinal(completion, req, result.Report):
		reason = "completion_error"
	case !completion.Final:
		reason = "not_final"
	case completion.FinalEventID == "":
		reason = "final_event_missing"
	case strings.TrimSpace(completion.FinalText) == "":
		reason = "final_text_empty"
	case len(completion.pendingControls) != 0:
		reason = "pending_controls"
	case result.Report.EffectState == store.BotReportEffectsUncertain:
		reason = "effects_uncertain"
	}
	if reason != "" {
		return fmt.Errorf("%w: required file deliverable has no saved receipt (reason=%s)", store.ErrBotInvalid, reason)
	}
	artifacts, err := resources.RecoverFileDelivery(ctx, completion.FinalText)
	if err != nil {
		return fmt.Errorf("save file deliverable: %w", err)
	}
	result.Report.Artifacts = artifacts
	if !hasReceipt() {
		return fmt.Errorf("%w: recovery returned no saved receipt (reason=receipt_not_run_bound)", store.ErrBotInvalid)
	}
	if err := botWorkBoundary(ctx, req); err != nil {
		return err
	}
	result.Report.Summary = "The report is saved as a Markdown source file. Open the attached file for the complete answer."
	return nil
}
