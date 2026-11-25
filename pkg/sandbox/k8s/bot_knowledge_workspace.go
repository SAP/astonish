package k8s

import (
	"context"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
)

// SnapshotBotKnowledge returns actual retained Markdown through the confined
// workspace transport. The caller owns authorization and extraction redaction.
func (b *K8sBackend) SnapshotBotKnowledge(ctx context.Context, h *sandbox.BotHomeBinding) ([]store.BotKnowledgeFileSummary, error) {
	var result []store.BotKnowledgeFileSummary
	err := b.workspace(ctx, h, map[string]any{"op": "knowledge-snapshot"}, &result)
	return result, err
}

// Keep the publication contract identical to the Docker retained-home adapter.
func validateKnowledgePublication(h *sandbox.BotHomeBinding, l store.BotKnowledgeExtractionLease, r store.BotKnowledgeExtraction) error {
	if h == nil || h.Validate() != nil || h.Backend != sandbox.BackendKindK8s || l.Validate(time.Now()) != nil || h.Scope.BotID != l.BotID.String() || r.ID != l.ExtractionID || r.Input.Source.BotID != l.BotID || r.Input.Validate() != nil || r.Decision == nil || r.Decision.Validate() != nil || r.Decision.Kind != store.BotKnowledgeDecisionChanges || r.DecisionDigest != store.BotKnowledgeDigest(*r.Decision) || r.Revision < 1 {
		return store.ErrBotInvalid
	}
	if r.State != store.BotKnowledgeExtractionPrepared && r.State != store.BotKnowledgeExtractionFilesReady {
		return store.ErrBotConflict
	}
	prior := map[string]string{}
	for _, f := range r.Input.PriorFiles {
		prior[f.Path] = f.Digest
	}
	for _, p := range r.Decision.Proposals {
		if !utf8.ValidString(p.Content) || prior[p.Path] != p.ExpectedDigest {
			return store.ErrBotInvalid
		}
		for _, segment := range strings.Split(p.Path, "/") {
			if strings.HasPrefix(segment, ".") {
				return store.ErrBotInvalid
			}
		}
	}
	return nil
}

// PublishBotKnowledge requires both the repository-held barrier and a fresh
// authority check at the compiled runtime's staged commit handshake. No host IO
// or model-callable tool is used, and ambiguous results are never replayed here.
func (b *K8sBackend) PublishBotKnowledge(ctx context.Context, h *sandbox.BotHomeBinding, l store.BotKnowledgeExtractionLease, r store.BotKnowledgeExtraction, check func(context.Context) error) ([]store.BotKnowledgeFileReadback, error) {
	if err := validateKnowledgePublication(h, l, r); err != nil {
		return nil, err
	}
	barrier := sandbox.BotKnowledgeCommitBarrierFromContext(ctx)
	if check == nil || barrier == nil {
		return nil, store.ErrBotUnsupported
	}
	r = r.Clone()
	ctx = sandbox.WithBotHomeWorkspaceCommitGuard(ctx, func(live context.Context) error {
		if err := l.Validate(time.Now()); err != nil {
			return err
		}
		return check(live)
	})
	var result []store.BotKnowledgeFileReadback
	err := barrier(func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return b.workspace(ctx, h, map[string]any{"op": "knowledge-publish", "record": r}, &result)
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (b *K8sBackend) VerifyBotKnowledge(ctx context.Context, h *sandbox.BotHomeBinding, l store.BotKnowledgeExtractionLease, r store.BotKnowledgeExtraction, readback []store.BotKnowledgeFileReadback) error {
	if err := validateKnowledgePublication(h, l, r); err != nil {
		return err
	}
	ctx = sandbox.WithBotHomeWorkspaceCommitGuard(ctx, func(context.Context) error { return l.Validate(time.Now()) })
	var actual []store.BotKnowledgeFileReadback
	if err := b.workspace(ctx, h, map[string]any{"op": "knowledge-verify", "record": r}, &actual); err != nil {
		return err
	}
	if !reflect.DeepEqual(actual, readback) {
		return store.ErrBotConflict
	}
	return nil
}
