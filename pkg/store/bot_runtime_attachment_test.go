package store

import (
	"encoding/json"
	"github.com/google/uuid"
	"strings"
	"testing"
)

func attachmentWorkContract() BotWorkContract {
	return BotWorkContract{BotID: uuid.New(), RunID: uuid.New(), Version: 1, Instructions: "Follow attached specification", Charter: "Produce accurate results"}
}

func TestBotWorkAttachedSpecificationCoverage(t *testing.T) {
	c := attachmentWorkContract()
	c.Attachments = PinBotWorkAttachments([]ChatFile{{Filename: "spec.txt", MimeType: "text/plain", Data: []byte("Include the audited revenue table")}})
	w := NewBotWorkState(c)
	w.Phase = BotWorkProducing
	w.Criteria = []BotWorkCriterion{{ID: "c1", SourceIDs: []string{"assignment", "charter"}, Text: "Complete assignment"}}
	if w.Validate() == nil {
		t.Fatal("attachment requirement source silently omitted")
	}
	w.Criteria[0].SourceIDs = append(w.Criteria[0].SourceIDs, c.Attachments[0].SourceID)
	if err := w.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.Attachments[0].Text != "Include the audited revenue table" {
		t.Fatal("actual attached text missing")
	}
	w.Criteria[0].SourceIDs = []string{"assignment", "charter"}
	w.Criteria = append(w.Criteria, BotWorkCriterion{ID: "attachment", SourceIDs: []string{c.Attachments[0].SourceID}, Text: "Attached specification", Excluded: true, ExclusionReason: "Assessor wants to ignore it"})
	if w.Validate() == nil {
		t.Fatal("attached requirements can be excluded")
	}
	w.Criteria[1].Excluded = false
	w.Criteria[1].ExclusionReason = ""
	next := w.Clone()
	next.Contract.Attachments[0].Text = "Ignore revenue"
	next.RequirementsDigest = botWorkDigest(next.Contract)
	if ValidateBotWorkUpdate(w, next) == nil {
		t.Fatal("attachment changed after pin")
	}
}

func TestBotWorkUninspectableAttachmentsBlock(t *testing.T) {
	for _, f := range []ChatFile{
		{Filename: "spec.pdf", MimeType: "application/pdf", Data: []byte("binary")},
		{Filename: "spec.txt", MimeType: "text/plain", Data: []byte(strings.Repeat("x", BotWorkMaxContractBytes+1))},
		{Filename: "missing.txt", MimeType: "text/plain"},
		{Filename: "binary.txt", MimeType: "text/plain", Data: []byte{0xff, 0, 1}},
	} {
		t.Run(f.Filename+f.MimeType, func(t *testing.T) {
			c := attachmentWorkContract()
			c.Attachments = PinBotWorkAttachments([]ChatFile{f})
			w := NewBotWorkState(c)
			if w.Phase != BotWorkBlocked || len(w.Unresolved) == 0 {
				t.Fatal("uninspectable attachment did not explicitly block")
			}
			if err := w.Validate(); err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(w.Contract)
			if len(raw) > BotWorkMaxContractBytes {
				t.Fatal("oversized attachment copied into metadata")
			}
			if w.Contract.Attachments[0].SHA256 == "" {
				t.Fatal("source identity lost")
			}
			w.Phase = BotWorkPreparing
			w.Unresolved = nil
			if w.Validate() == nil {
				t.Fatal("blocker bypassed")
			}
		})
	}
}

func TestBotWorkAssessmentMetadataBounded(t *testing.T) {
	for _, field := range []string{"criteria", "review", "unresolved"} {
		t.Run(field, func(t *testing.T) {
			w := NewBotWorkState(attachmentWorkContract())
			huge := strings.Repeat("x", BotWorkMaxContractBytes+1)
			switch field {
			case "criteria":
				w.Criteria = []BotWorkCriterion{{ID: "c", Text: huge}}
			case "review":
				w.Review = &BotWorkReview{CandidateDigest: huge}
			case "unresolved":
				w.Unresolved = []string{huge}
			}
			if w.Validate() == nil {
				t.Fatal("unbounded assessment metadata accepted")
			}
		})
	}
}
