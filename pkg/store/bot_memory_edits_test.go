package store

import (
	"errors"
	"github.com/google/uuid"
	"testing"
)

func TestBotMemoryReviewFingerprint(t *testing.T) {
	row := MemorySearchResult{ID: uuid.NewString(), SessionID: uuid.NewString(), Snippet: "source", Category: "people", CreatedBy: uuid.NewString()}
	fingerprint := MemoryContentFingerprint(row)
	row.Score = 0.9
	row.Scope = "personal"
	if MemoryContentFingerprint(row) != fingerprint {
		t.Fatal("ranking changed CAS")
	}
	row.Category = "facts"
	if MemoryContentFingerprint(row) == fingerprint {
		t.Fatal("category absent from CAS")
	}
	row.Category = "people"
	row.Snippet = "changed"
	if MemoryContentFingerprint(row) == fingerprint {
		t.Fatal("content absent from CAS")
	}
}
func TestBotMemoryReviewDuplicateSourceIdentity(t *testing.T) {
	id, sid := uuid.New(), uuid.NewString()
	source := BotMemoryReviewSource{ID: id.String(), SessionID: sid, ExpectedFingerprint: MemoryContentFingerprint(MemorySearchResult{})}
	alias := source
	alias.ID = id.URN()
	q := BotMemoryReview{ID: uuid.New(), ExpectedRevision: 1, Kind: BotMemoryReviewConsolidate, Reason: "reviewed", Confirmed: true, Edits: []BotMemoryReviewEdit{{SessionID: sid, Content: "merged", Sources: []BotMemoryReviewSource{source, alias}}}}
	if !errors.Is(q.Validate(), ErrBotInvalid) {
		t.Fatal("UUID aliases bypassed source uniqueness")
	}
}

func TestBotMemoryReviewValidation(t *testing.T) {
	q := BotMemoryReview{ID: uuid.New(), ExpectedRevision: 1, Kind: BotMemoryReviewDocument, Document: "MEMORY.md", Reason: "reviewed", Confirmed: true, Edits: []BotMemoryReviewEdit{{SessionID: uuid.NewString(), Content: "note"}}}
	if err := q.Validate(); err != nil {
		t.Fatal(err)
	}
	q.Confirmed = false
	if !errors.Is(q.Validate(), ErrBotForbidden) {
		t.Fatal("unconfirmed")
	}
	q.Confirmed = true
	q.Kind = BotMemoryReviewConsolidate
	q.Document = ""
	if !errors.Is(q.Validate(), ErrBotInvalid) {
		t.Fatal("merge without sources")
	}
	q.Kind = BotMemoryReviewDocument
	q.Edits[0].Sources = []BotMemoryReviewSource{{ID: uuid.NewString(), SessionID: q.Edits[0].SessionID}}
	if !errors.Is(q.Validate(), ErrBotInvalid) {
		t.Fatal("missing fingerprint")
	}
}
