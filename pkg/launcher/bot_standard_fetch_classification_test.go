package launcher

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/SAP/astonish/pkg/execution"
)

// Ordinary production catalogue is intentionally not the mandated retrieval
// adapter: identical names cannot mint routine-read authority in a continuation.
func TestBotStandardFetchCannotAcquireMandatedReadClassification(t *testing.T) {
	f := newBotFactoryFixture(t)
	catalog, err := BotCatalogueTools(&fileOutcomeNode{}, f.req.Run.SessionID.String())
	if err != nil {
		t.Fatal(err)
	}
	var standard botRunnableTool
	for _, candidate := range catalog {
		if candidate.Name() == "web_fetch" {
			standard = candidate.(botRunnableTool)
		}
	}
	if standard == nil {
		t.Fatal("ordinary production fetch missing")
	}
	classifier := WithBotMandatedReadClassifier(BotCatalogueClassifier{}, catalog)
	raw := json.RawMessage(`{"url":"https://example.com/source"}`)
	action, err := classifier.DescribeAction(context.Background(), standard.Name(), raw)
	if err != nil || action.Effect != execution.EffectUnknown || action.ConstraintsVerified {
		t.Fatalf("standard fetch gained read authority: %+v %v", action, err)
	}
}
