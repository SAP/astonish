package api

import (
	"encoding/json"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"strings"
	"testing"
)

func TestBotProposalRoutesStrictHTTP(t *testing.T) {
	f := newReviewedHTTPFixture(t)
	for _, scope := range []string{"personal", "team"} {
		t.Run(scope, func(t *testing.T) {
			suffix := "?scope=" + scope
			var b store.Bot
			if err := json.Unmarshal(f.request("POST", "/api/studio/bots"+suffix, `{"name":"Proposal routes","idempotency_key":"proposal-routes","definition":{"charter":"Observe"}}`, 201), &b); err != nil {
				t.Fatal(err)
			}
			path := "/api/studio/bots/" + b.ID.String() + "/proposals"
			if got := string(f.request("GET", path+suffix, nil, 200)); !strings.Contains(got, `"proposals":[]`) {
				t.Fatalf("empty proposals: %s", got)
			}
			decision := path + "/" + uuid.NewString() + "/decision" + suffix
			for _, body := range []string{`null`, `{}`, `{"digest":"saved","decision":"apply"}`, `{"digest":"saved","decision":"confirm","actor_id":"forged"}`, `{"digest":"saved","decision":"reject","payload":{}}`, `{"digest":"saved","decision":"confirm"} {}`} {
				f.request("POST", decision, body, 400)
			}
			f.request("POST", path+"/not-an-id/decision"+suffix, `{"digest":"saved","decision":"confirm"}`, 400)
			f.request("POST", decision, `{"digest":"saved","decision":"confirm"}`, 404)
			f.request("POST", decision, `{"digest":"saved","decision":"reject"}`, 404)
		})
	}
}
