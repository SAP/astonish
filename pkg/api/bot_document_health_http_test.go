package api

import (
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"reflect"
	"strings"
	"testing"

	botdomain "github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/memory"
	"github.com/SAP/astonish/pkg/store"
)

func checkReviewedMemoryHTTP(t *testing.T, request func(*testing.T, string, string, string, string, int) []byte, suffix, owner, reader, scope string) {
	t.Helper()
	var b store.Bot
	if err := json.Unmarshal(request(t, "POST", "/api/studio/bots"+suffix, owner, fmt.Sprintf(`{"name":"Reviewed memory","idempotency_key":%q,"definition":{"charter":"Observe"}}`, uuid.NewString()), 201), &b); err != nil {
		t.Fatal(err)
	}
	path := "/api/studio/bots/" + b.ID.String()
	q := store.BotMemoryReview{ID: uuid.New(), ExpectedRevision: 1, Reason: "explicit owner review", Confirmed: true, Edits: []store.BotMemoryReviewEdit{{SessionID: b.HomeSessionID.String(), Content: "Alice note", Category: "people"}, {SessionID: b.HomeSessionID.String(), Content: "Alice second note", Category: "people"}}}
	encode := func() string {
		data, err := json.Marshal(q)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	ingest := path + "/documents/people/INDEX.md/ingest" + suffix
	request(t, "POST", ingest, "", encode(), 401)
	if scope == "team" {
		request(t, "POST", ingest, reader, encode(), 403)
	}
	q.Confirmed = false
	request(t, "POST", ingest, owner, encode(), 403)
	q.Confirmed = true
	// Structured requests cannot carry identity, evidence or permissions.
	for _, field := range []string{`"actor_id":"forged"`, `"permissions":["admin"]`, `"attribution":{"status":"verified"}`} {
		request(t, "POST", ingest, owner, strings.TrimSuffix(encode(), "}")+","+field+"}", 400)
	}
	q.Kind = store.BotMemoryReviewConsolidate
	request(t, "POST", ingest, owner, encode(), 400)
	q.Kind = ""
	q.Document = "MEMORY.md"
	request(t, "POST", ingest, owner, encode(), 400)
	q.Document = ""
	first := request(t, "POST", ingest, owner, encode(), 200)
	if replay := request(t, "POST", ingest, owner, encode(), 200); string(first) != string(replay) {
		t.Fatal("receipt replay changed")
	}
	var projection botdomain.DocumentProjection
	if err := json.Unmarshal(request(t, "GET", path+"/documents"+suffix, owner, "", 200), &projection); err != nil {
		t.Fatal(err)
	}
	var mappings []store.BotMemoryReviewSource
	for _, doc := range projection.Documents {
		if doc.Name == botdomain.DocumentPeople {
			mappings = doc.RecordMappings
		}
	}
	if len(mappings) != 2 {
		t.Fatal("missing ingest mappings")
	}
	q = store.BotMemoryReview{ID: uuid.New(), ExpectedRevision: 2, Reason: "merge reviewed", Confirmed: true, Edits: []store.BotMemoryReviewEdit{{SessionID: b.HomeSessionID.String(), Sources: mappings, Content: "Alice consolidated", Category: "people"}}}
	apply := path + "/memory-health/consolidate" + suffix
	stale := q.Edits[0].Sources[0].ExpectedFingerprint
	q.Edits[0].Sources[0].ExpectedFingerprint = strings.Repeat("0", 64)
	request(t, "POST", apply, owner, encode(), 409)
	q.Edits[0].Sources[0].ExpectedFingerprint = stale
	var receipt store.BotMemoryReviewReceipt
	if err := json.Unmarshal(request(t, "POST", apply, owner, encode(), 200), &receipt); err != nil {
		t.Fatal(err)
	}
	request(t, "POST", apply, owner, encode(), 200)
	if len(receipt.Sources) != 2 || receipt.ActorID.String() != owner || receipt.Revision != 3 {
		t.Fatalf("receipt %+v", receipt)
	}
	for _, source := range receipt.Sources {
		var saved store.MemorySearchResult
		if err := json.Unmarshal(request(t, "GET", path+"/memory/"+source.ID+suffix, owner, "", 200), &saved); err != nil {
			t.Fatal(err)
		}
		if saved.Snippet != source.Snippet || saved.Attribution == nil || saved.Attribution.SupersededBy == "" {
			t.Fatal("source lost")
		}
	}
	q.ID = uuid.New()
	request(t, "POST", apply, owner, encode(), 409)
	var activities struct {
		Activity []store.BotActivity `json:"activity"`
	}
	json.Unmarshal(request(t, "GET", path+"/activity"+suffix, owner, "", 200), &activities)
	count := 0
	for _, event := range activities.Activity {
		if event.Kind == store.BotActivityMemoryReview {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("%d receipts", count)
	}
	var merged store.MemorySearchResult
	if err := json.Unmarshal(request(t, "GET", path+"/memory/"+receipt.ReplacementIDs[0]+suffix, owner, "", 200), &merged); err != nil {
		t.Fatal(err)
	}
	q = store.BotMemoryReview{ID: uuid.New(), ExpectedRevision: 3, Reason: "review document replacement", Confirmed: true, Edits: []store.BotMemoryReviewEdit{{SessionID: b.HomeSessionID.String(), Content: "reviewed replacement", Sources: []store.BotMemoryReviewSource{{ID: merged.ID, SessionID: merged.SessionID, ExpectedFingerprint: store.MemoryContentFingerprint(merged)}}}}}
	memoryIngest := path + "/documents/MEMORY.md/ingest" + suffix
	request(t, "POST", memoryIngest, owner, encode(), 200)
	q.ID = uuid.New()
	q.ExpectedRevision = 4
	request(t, "POST", memoryIngest, owner, encode(), 409)
	q.Edits[0].Sources = nil
	q.Edits[0].Content = "dated observation"
	var noteReceipt store.BotMemoryReviewReceipt
	if err := json.Unmarshal(request(t, "POST", path+"/documents/notes/2020-02-03.md/ingest"+suffix, owner, encode(), 200), &noteReceipt); err != nil {
		t.Fatal(err)
	}
	var note store.MemorySearchResult
	if err := json.Unmarshal(request(t, "GET", path+"/memory/"+noteReceipt.ReplacementIDs[0]+suffix, owner, "", 200), &note); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(note.CreatedAt, "2020-02-03") || note.Attribution.Status != store.MemoryKnowledgeInferred {
		t.Fatalf("dated note: %+v", note)
	}
	var after store.Bot
	json.Unmarshal(request(t, "GET", path+suffix, owner, "", 200), &after)
	if after.Version != b.Version || after.OwnerID != b.OwnerID || after.Status != b.Status || !reflect.DeepEqual(after.HomeSessionID, b.HomeSessionID) {
		t.Fatal("review widened authority")
	}
}

func TestBotHealthKnownIDs(t *testing.T) {
	ids := botHealthKnownIDs([]string{"own", "foreign", "missing"}, map[string]bool{"own": true})
	if len(ids) != 1 || ids[0] != "own" {
		t.Fatalf("unverified reference promoted: %v", ids)
	}
}

// Runs inside TestBotPhase2HTTPAcceptance's real HTTP server, production routes,
// tenant middleware and SQLite fixture for both personal and team scopes.
func checkBotDocumentHealthHTTP(t *testing.T, request func(*testing.T, string, string, string, string, int) []byte, path, suffix, owner, reader, scope string, row store.MemorySearchResult, projection botdomain.DocumentProjection) {
	checkReviewedMemoryHTTP(t, request, suffix, owner, reader, scope)
	t.Helper()
	docs := map[botdomain.DocumentName]botdomain.Document{}
	for _, doc := range projection.Documents {
		docs[doc.Name] = doc
		if strings.Contains(doc.Content, "quasar quasar quasar foreign") {
			t.Fatal("foreign document evidence")
		}
	}
	for _, name := range []botdomain.DocumentName{botdomain.DocumentMemory, botdomain.DocumentPeople, botdomain.DocumentGroups} {
		if !docs[name].ReadOnly || docs[name].SourceVersion == "" || docs[name].WriteUnsupportedReason == "" {
			t.Fatalf("missing mapped document %s", name)
		}
		request(t, "PUT", path+"/documents/"+string(name)+suffix, owner, `{"expected_revision":1,"content":"replace","reason":"reviewed","confirmed":true}`, 503)
	}
	if !strings.Contains(docs[botdomain.DocumentMemory].Content, row.ID) || !strings.Contains(docs[botdomain.DocumentMemory].Content, owner) {
		t.Fatal("missing source attribution")
	}
	healthPath := path + "/memory-health"
	request(t, "GET", healthPath+suffix, "", "", 401)
	request(t, "GET", healthPath+suffix+"&limit=2001", owner, "", 400)
	var result struct {
		Scope          string                   `json:"scope"`
		Snapshot       botdomain.MemorySnapshot `json:"snapshot"`
		ReviewOnly     bool                     `json:"review_only"`
		ApplySupported bool                     `json:"apply_supported"`
		Health         MemoryHealthResponse     `json:"health"`
	}
	data := request(t, "GET", healthPath+suffix+"&limit=1", owner, "", 200)
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if result.Scope != scope || !result.ReviewOnly || !result.ApplySupported || len(result.Snapshot.Records) != 1 || result.Snapshot.Records[0].ID != row.ID || result.Snapshot.Records[0].CreatedBy != owner || result.Snapshot.Truncated {
		t.Fatalf("health boundary %s", data)
	}
	for _, rec := range result.Health.Recommendations {
		if rec.TargetScope != scope || rec.Card.Scope != scope || rec.Card.Status != memory.ScenarioCardStatusDraft {
			t.Fatalf("promotion %+v", rec)
		}
	}
	request(t, "POST", healthPath+"/consolidate"+suffix, owner, `{"confirmed":true}`, 400)
	var after struct {
		Memories []store.MemorySearchResult `json:"memories"`
	}
	if err := json.Unmarshal(request(t, "GET", path+"/memory"+suffix, owner, "", 200), &after); err != nil {
		t.Fatal(err)
	}
	if len(after.Memories) != 1 || after.Memories[0].ID != row.ID || after.Memories[0].Snippet != "updated fact" {
		t.Fatal("health changed evidence")
	}
	if scope == "team" {
		request(t, "GET", healthPath+suffix, reader, "", 200)
	} else {
		request(t, "GET", healthPath+suffix, reader, "", 404)
	}
}
