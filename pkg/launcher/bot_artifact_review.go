package launcher

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"reflect"

	"github.com/SAP/astonish/pkg/store"
)

// No agent-selected path is opened. The exact receipt must be in this factory's
// private run accumulator, then immutable storage rechecks current owner access.
func (b *botFactoryBinding) artifactReviewReader(scope *BotChatScope, receipts *BotFileReceipts) func(context.Context, store.BotArtifact) ([]byte, error) {
	return func(ctx context.Context, artifact store.BotArtifact) ([]byte, error) {
		ctx = b.bind(ctx)
		if err := b.check(ctx); err != nil {
			return nil, err
		}
		trusted := false
		for _, a := range receipts.Artifacts(b.req) {
			if reflect.DeepEqual(a, artifact) {
				trusted = true
				break
			}
		}
		if !trusted || artifact.ContentRef == nil {
			return nil, store.ErrBotForbidden
		}
		if artifact.SizeBytes < 0 || artifact.SizeBytes > store.BotWorkMaxCandidateBytes {
			return nil, store.ErrBotInvalid
		}
		adapter := &botFileAdapter{request: b.req, scope: scope, receipts: receipts}
		files, err := adapter.files(ctx)
		if err != nil {
			return nil, err
		}
		stream, err := files.OpenContent(ctx, *artifact.ContentRef, store.BotFileRange{})
		if err != nil {
			return nil, err
		}
		if stream == nil || stream.ReadCloser == nil {
			return nil, store.ErrBotInvalid
		}
		body, err := io.ReadAll(io.LimitReader(stream, artifact.SizeBytes+1))
		closeErr := stream.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if int64(len(body)) != artifact.SizeBytes || fmt.Sprintf("%x", sha256.Sum256(body)) != artifact.SHA256 {
			return nil, store.ErrBotInvalid
		}
		return body, nil
	}
}
