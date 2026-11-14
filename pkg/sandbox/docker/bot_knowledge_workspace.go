package docker

import (
	"context"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
)

// SnapshotBotKnowledge returns complete actual retained Markdown, not summaries
// invented by a model. The upper layer redacts before retaining extraction input.
func (db *DockerBackend) SnapshotBotKnowledge(c context.Context, b *sandbox.BotHomeBinding) ([]store.BotKnowledgeFileSummary, error) {
	var result []store.BotKnowledgeFileSummary
	err := db.workspace(c, b, map[string]any{"op": "knowledge-snapshot"}, &result)
	return result, err
}

func validateKnowledgePublication(b *sandbox.BotHomeBinding, l store.BotKnowledgeExtractionLease, r store.BotKnowledgeExtraction) error {
	if b == nil || b.Validate() != nil || b.Backend != sandbox.BackendKindDocker || l.Validate(time.Now()) != nil || b.Scope.BotID != l.BotID.String() || r.ID != l.ExtractionID || r.Input.Source.BotID != l.BotID || r.Input.Validate() != nil || r.Decision == nil || r.Decision.Validate() != nil || r.Decision.Kind != store.BotKnowledgeDecisionChanges || r.DecisionDigest != store.BotKnowledgeDigest(*r.Decision) || r.Revision < 1 {
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

// PublishBotKnowledge deliberately requires a repository-held barrier in addition
// to the final callback: checking a transaction then releasing it before rename
// is unsafe. Missing trusted wiring fails closed, never falls back to host IO.
func (db *DockerBackend) PublishBotKnowledge(c context.Context, b *sandbox.BotHomeBinding, l store.BotKnowledgeExtractionLease, r store.BotKnowledgeExtraction, check func(context.Context) error) ([]store.BotKnowledgeFileReadback, error) {
	if err := validateKnowledgePublication(b, l, r); err != nil {
		return nil, err
	}
	barrier := sandbox.BotKnowledgeCommitBarrierFromContext(c)
	if check == nil || barrier == nil {
		return nil, store.ErrBotUnsupported
	}
	r = r.Clone()
	c = sandbox.WithBotHomeWorkspaceCommitGuard(c, func(live context.Context) error {
		if err := l.Validate(time.Now()); err != nil {
			return err
		}
		return check(live)
	})
	var result []store.BotKnowledgeFileReadback
	err := barrier(func() error {
		if err := c.Err(); err != nil {
			return err
		}
		return db.workspace(c, b, map[string]any{"op": "knowledge-publish", "record": r}, &result)
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (db *DockerBackend) VerifyBotKnowledge(c context.Context, b *sandbox.BotHomeBinding, l store.BotKnowledgeExtractionLease, r store.BotKnowledgeExtraction, readback []store.BotKnowledgeFileReadback) error {
	if err := validateKnowledgePublication(b, l, r); err != nil {
		return err
	}
	c = sandbox.WithBotHomeWorkspaceCommitGuard(c, func(context.Context) error { return l.Validate(time.Now()) })
	var actual []store.BotKnowledgeFileReadback
	if err := db.workspace(c, b, map[string]any{"op": "knowledge-verify", "record": r}, &actual); err != nil {
		return err
	}
	if !reflect.DeepEqual(actual, readback) {
		return store.ErrBotConflict
	}
	return nil
}
