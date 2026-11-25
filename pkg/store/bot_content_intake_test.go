package store

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestBotContentIntakeScope(t *testing.T) {
	intake := BotContentScope{OrgID: "org", TeamID: "team", OwnerID: uuid.NewString(), IntakeID: uuid.New()}
	ref := BotContentRef{Format: BotContentFormatVersion, Scope: intake, Kind: BotContentIntake, Key: "staged-v1/intake", SHA256: strings.Repeat("0", 64), Revision: 1}
	if err := ref.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*BotContentRef){
		func(r *BotContentRef) { r.Scope.BotID = uuid.New() },
		func(r *BotContentRef) { r.Scope.OwnerID = "" },
		func(r *BotContentRef) { r.Scope.IntakeID = uuid.Nil },
		func(r *BotContentRef) { r.Kind = BotContentDocument },
		func(r *BotContentRef) { r.Kind = BotContentTemplate },
	} {
		bad := ref
		mutate(&bad)
		if bad.Validate() == nil {
			t.Fatalf("invalid ownership accepted: %+v", bad)
		}
	}
	personal := ref
	personal.Scope.OrgID = ""
	personal.Scope.TeamID = ""
	if err := personal.Validate(); err != nil {
		t.Fatal(err)
	}
	// Existing Bot/template scope namespace digests must not change for zero IntakeID.
	raw, err := json.Marshal(BotContentScope{OrgID: "org", TeamID: "team"})
	if err != nil || strings.Contains(string(raw), "intake_id") {
		t.Fatalf("zero intake changes namespace: %s %v", raw, err)
	}
}
