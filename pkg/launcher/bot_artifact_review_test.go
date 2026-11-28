package launcher

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type artifactReviewFiles struct {
	store.BotFiles
	body  string
	ref   store.BotContentRef
	reads int
}

func (f *artifactReviewFiles) OpenContent(_ context.Context, ref store.BotContentRef, _ store.BotFileRange) (*store.BotFileStream, error) {
	f.reads++
	if ref != f.ref {
		return nil, store.ErrBotForbidden
	}
	return &store.BotFileStream{ReadCloser: io.NopCloser(strings.NewReader(f.body))}, nil
}
func TestBotArtifactReviewTrustedSnapshot(t *testing.T) {
	f := newBotFactoryFixture(t)
	got, err := f.construct(t)
	if err != nil {
		t.Fatal(err)
	}
	defer got.Cleanup()
	if got.ArtifactBody == nil {
		t.Fatal("production reader unwired")
	}
	binding := botFactoryModelForTest(t, got.Agent.LLM).binding
	body := "complete immutable design\n"
	ref := store.BotContentRef{Format: store.BotContentFormatVersion, Scope: store.BotContentScope{BotID: f.req.Bot.ID, OwnerID: f.req.Bot.OwnerID.String()}, Kind: store.BotContentArtifact, Key: "content-v1/artifact-1-review", SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(body))), SizeBytes: int64(len(body)), Revision: 1}
	files := &artifactReviewFiles{body: body, ref: ref}
	scope := &BotChatScope{Services: &store.Services{BotFilesFactory: func(context.Context, *store.Services, store.BotScope) (store.BotFiles, error) { return files, nil }}}
	receipts := &BotFileReceipts{receipts: []BotPublishedFileReceipt{{RunID: f.req.Run.ID, Path: "outputs/report.md", Ref: ref}}}
	reader := binding.artifactReviewReader(scope, receipts)
	a := receipts.Artifacts(f.req)[0]
	data, err := reader(f.ctx, a)
	if err != nil || string(data) != body {
		t.Fatalf("read: %v", err)
	}
	for _, mode := range []string{"foreign-run", "foreign-owner", "different-path", "corrupt"} {
		t.Run(mode, func(t *testing.T) {
			bad := a
			r := *a.ContentRef
			bad.ContentRef = &r
			before := files.reads
			switch mode {
			case "foreign-run":
				bad.ID = uuid.New()
			case "foreign-owner":
				r.Scope.OwnerID = uuid.NewString()
			case "different-path":
				bad.RelativePath = "elsewhere.md"
			case "corrupt":
				files.body = "corrupt"
			}
			if _, err := reader(f.ctx, bad); err == nil {
				t.Fatal("accepted untrusted bytes")
			}
			if mode != "corrupt" && files.reads != before {
				t.Fatal("untrusted reference reached storage")
			}
		})
	}
}
