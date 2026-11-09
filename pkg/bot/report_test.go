package bot

import (
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"strings"
	"testing"
	"time"
)

func TestRenderReport(t *testing.T) {
	next := time.Now().UTC()
	approval := uuid.New()
	run := store.BotRun{ID: uuid.New(), Status: store.RunPartial, Report: store.BotReport{Summary: "Observed a partial result", Changed: []store.BotEvidence{{Summary: "changed fact", Source: "source A"}}, Unchanged: []store.BotEvidence{{Summary: "stable fact"}}, Unverified: []string{"unverified fact"}, Artifacts: []store.BotArtifact{{Title: "Artifact", RelativePath: "results/a.txt", SHA256: "digest"}}, NeedsApproval: []uuid.UUID{approval}, NextRunAt: &next}}
	text := RenderReport(run)
	for _, want := range []string{"partial", "changed fact", "source A", "stable fact", "unverified fact", "results/a.txt", "digest", approval.String(), next.Format(time.RFC3339), run.ID.String()} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in %s", want, text)
		}
	}
}
