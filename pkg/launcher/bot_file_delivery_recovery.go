package launcher

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

// One local save/snapshot opportunity, shared across all consumers of resources.
// No model invocation, sandbox command, read of old work, or external action is
// possible here. Files supplies lease-fenced, confined IO; binding rechecks live
// authorization and the original budget before each mutation.
func (b *botFactoryBinding) fileDeliveryRecovery(scope *BotChatScope, receipts *BotFileReceipts) func(context.Context, string) ([]store.BotArtifact, error) {
	return b.fileDeliverySnapshot(scope, receipts, "")
}

func (b *botFactoryBinding) fileDeliveryRepair(scope *BotChatScope, receipts *BotFileReceipts) func(context.Context, string, int) ([]store.BotArtifact, error) {
	var mu sync.Mutex
	last := 0
	return func(ctx context.Context, source string, revision int) ([]store.BotArtifact, error) {
		mu.Lock()
		defer mu.Unlock()
		if receipts == nil || revision <= last || revision > store.BotWorkMaxRepairs {
			return nil, store.ErrBotConflict
		}
		last = revision // failed writes are never replayed
		before := receipts.Artifacts(b.req)
		all, err := b.fileDeliverySnapshot(scope, receipts, fmt.Sprintf("-repair-%d", revision))(ctx, source)
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		for _, a := range before {
			seen[a.ID.String()] = true
		}
		var out []store.BotArtifact
		for _, a := range all {
			if !seen[a.ID.String()] {
				out = append(out, a)
			}
		}
		return out, nil
	}
}

func (b *botFactoryBinding) fileDeliverySnapshot(scope *BotChatScope, receipts *BotFileReceipts, suffix string) func(context.Context, string) ([]store.BotArtifact, error) {
	var mu sync.Mutex
	attempted := false
	return func(ctx context.Context, source string) ([]store.BotArtifact, error) {
		mu.Lock()
		defer mu.Unlock()
		if attempted {
			return nil, store.ErrBotConflict
		}
		attempted = true
		if receipts == nil || len(source) == 0 || len(source) > botFileEditMaxBytes || !utf8.ValidString(source) {
			return nil, store.ErrBotInvalid
		}
		ctx, cancel := context.WithTimeout(b.bind(ctx), 10*time.Second)
		defer cancel()
		if err := b.check(ctx); err != nil {
			return nil, err
		}
		if b.progress != nil {
			if err := b.progress.boundary(ctx); err != nil {
				return nil, err
			}
		}
		p := "outputs/" + b.req.Run.ID.String() + "-report" + suffix + ".md"
		// Completion IO is not an escape from the reviewed tool policy. Only
		// already allowed confined writes are eligible; never request or consume
		// an approval (or replay an external action) during this recovery.
		authorize := func(name string, args map[string]any) error {
			if err := b.check(ctx); err != nil {
				return err
			}
			if b.req.Mandate != nil {
				if err := b.req.Mandate.Authorization.Authorize(execution.CapabilityToolExecute, name); err != nil {
					return err
				}
			}
			if b.req.Authorization != nil {
				if err := b.req.Authorization.AuthorizeTool(ctx, name); err != nil {
					return err
				}
			}
			if b.req.Policy != nil {
				raw, err := json.Marshal(args)
				if err != nil {
					return err
				}
				action, err := (BotFileActionClassifier{}).DescribeAction(ctx, name, raw)
				if err != nil {
					return err
				}
				if execution.EvaluateRunPolicy(ctx, action, time.Now()).Kind != execution.PolicyAllow {
					return store.ErrBotForbidden
				}
			} else if b.req.Authorization == nil {
				return store.ErrBotForbidden
			}
			return nil
		}
		publishArgs := map[string]any{"path": p, "title": "Report", "purpose": "Complete answer saved from retained evidence", "memory_summary": "Report saved at " + p}
		if err := authorize("write_file", map[string]any{"path": p, "content": source, "create_only": true}); err != nil {
			return nil, err
		}
		if err := authorize("publish_file", publishArgs); err != nil {
			return nil, err
		}
		// Charge the real granted operations, never an invented recovery tool.
		// Reserve both before any IO so an exhausted publication budget cannot
		// leave a misleading partially delivered answer.
		for _, name := range []string{"write_file", "publish_file"} {
			if err := b.req.Budget.ReserveTool(ctx, name); err != nil {
				return nil, err
			}
		}
		adapter := &botFileAdapter{request: b.req, scope: scope, receipts: receipts}
		files, err := adapter.files(ctx)
		if err != nil {
			return nil, err
		}
		entry, err := files.Write(ctx, b.req.Bot.ID, store.BotFileWrite{Path: p, Body: strings.NewReader(source), SizeBytes: int64(len(source)), CreateOnly: true})
		if err != nil {
			return nil, err
		}
		digest := fmt.Sprintf("%x", sha256.Sum256([]byte(source)))
		if entry == nil || entry.Path != p || entry.SizeBytes != int64(len(source)) || entry.SHA256 != digest {
			return nil, store.ErrBotConflict
		}
		stream, err := files.Open(ctx, b.req.Bot.ID, p, store.BotFileRange{})
		if err != nil {
			return nil, err
		}
		if stream == nil || stream.ReadCloser == nil {
			return nil, store.ErrBotConflict
		}
		hash := sha256.New()
		n, readErr := io.Copy(hash, io.LimitReader(stream, botFileEditMaxBytes+1))
		closeErr := stream.Close()
		if readErr != nil {
			return nil, readErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if n != int64(len(source)) || fmt.Sprintf("%x", hash.Sum(nil)) != digest {
			return nil, store.ErrBotConflict
		}
		if err := authorize("publish_file", publishArgs); err != nil {
			return nil, err
		}
		ref, err := files.PublishSnapshot(ctx, b.req.Bot.ID, p, "runs/"+b.req.Run.ID.String()+"/completion-report"+suffix, nil)
		if err != nil {
			return nil, err
		}
		if ref == nil || ref.Validate() != nil || ref.Kind != store.BotContentArtifact || ref.Scope.BotID != b.req.Bot.ID || ref.Scope.OwnerID != b.req.Bot.OwnerID.String() || ref.SHA256 != digest || ref.SizeBytes != n {
			return nil, store.ErrBotConflict
		}
		if err := b.check(ctx); err != nil {
			return nil, err
		}
		receipts.mu.Lock()
		receipts.receipts = append(receipts.receipts, BotPublishedFileReceipt{RunID: b.req.Run.ID, Path: p, Title: "Report", Purpose: "Complete answer saved from retained evidence", MemorySummary: "Report saved at " + p, Ref: *ref})
		receipts.mu.Unlock()
		return receipts.Artifacts(b.req), nil
	}
}
