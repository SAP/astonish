package client

import (
	"encoding/json"
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestBotOperationDecisionPublicBinding(t *testing.T) {
	q := BotOperationDecision{OperationID: uuid.New(), PayloadDigest: "digest", ExpectedRevision: 3, ExpiresAt: time.Now().Add(time.Hour), Approve: false}
	raw, err := json.Marshal(q)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err = json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 4 || fields["payload_digest"] != "digest" || fields["approve"] != false || fields["expected_revision"] != float64(3) {
		t.Fatalf("unexpected public decision %s", raw)
	}
}
