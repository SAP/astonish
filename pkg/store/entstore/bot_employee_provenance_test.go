package entstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"iter"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

type employeeCommitModel struct{}

func (employeeCommitModel) Name() string { return "owner-test" }
func (employeeCommitModel) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		var input struct {
			SourceDigest string `json:"source_digest"`
			TextDigest   string `json:"text_digest"`
			Facts        struct {
				SourceDigest string `json:"source_digest"`
			} `json:"facts"`
		}
		_ = json.Unmarshal([]byte(req.Contents[0].Parts[0].Text), &input)
		out := map[string]any{"source_digest": input.SourceDigest, "outcome": "incomplete", "text": "I couldn't complete this work because an operational failure stopped it. Please review the run diagnostics before continuing."}
		if strings.Contains(req.Config.SystemInstruction.Parts[0].Text, "BOT_EMPLOYEE_MESSAGE_REVIEW") {
			out = map[string]any{"source_digest": input.Facts.SourceDigest, "text_digest": input.TextDigest, "outcome": "incomplete", "approved": true}
		}
		raw, _ := json.Marshal(out)
		yield(&model.LLMResponse{Content: genai.NewContentFromText(string(raw), "model")}, nil)
	}
}

func TestEmployeeModelCommitRequiresPrivateReview(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, owner, worker, run, lease, _ := botRunFixture(t, scope)
			report := store.BotReport{Summary: "private failure"}
			m := store.NewBotEmployeeFallback(store.RunFailed, report, "")
			m.Origin, m.Text = store.BotEmployeeMessageModel, "I sent the payment successfully."
			sum := sha256.Sum256([]byte(m.Text))
			m.TextDigest = hex.EncodeToString(sum[:])
			m.ReviewedTextDigest, m.ReviewedSourceDigest = m.TextDigest, m.SourceDigest
			report.EmployeeMessage = &m
			commit := store.BotRunCommit{Lease: lease, Status: store.RunFailed, Report: report}
			if err := repo.CommitRun(worker, commit); !errors.Is(err, store.ErrBotInvalid) {
				t.Fatalf("forged hashes committed: %v", err)
			}
			m = bot.ComposeEmployeeMessage(worker, store.RunFailed, report, nil, bot.EmployeeMessageOptions{LLM: employeeCommitModel{}, Boundary: func(context.Context) error { return nil }})
			if m.Origin != store.BotEmployeeMessageModel {
				t.Fatal("composer did not review")
			}
			report.EmployeeMessage = &m
			report.FinalizeEmployeeMessage(store.RunFailed, nil)
			commit.Report = report
			raw, _ := json.Marshal(commit)
			var roundtrip store.BotRunCommit
			if err := json.Unmarshal(raw, &roundtrip); err != nil {
				t.Fatal(err)
			}
			if err := repo.CommitRun(worker, roundtrip); !errors.Is(err, store.ErrBotInvalid) {
				t.Fatalf("public roundtrip committed: %v", err)
			}
			if err := repo.CommitRun(worker, commit); err != nil {
				t.Fatal("trusted composer commit", err)
			}
			if err := repo.CommitRun(worker, commit); err != nil {
				t.Fatal("trusted retry", err)
			}
			if err := repo.CommitRun(worker, roundtrip); !errors.Is(err, store.ErrBotInvalid) {
				t.Fatalf("public retry committed: %v", err)
			}
			saved, err := repo.GetRun(owner, run.BotID, run.ID)
			if err != nil || saved.Report.ValidateEmployeeMessageIntegrity(store.RunFailed) != nil {
				t.Fatal("persisted integrity", err)
			}
			pub, err := repo.PublishReport(owner, store.BotPublicationRequest{BotID: run.BotID, RunID: run.ID})
			if err != nil || pub.Text != m.Text {
				t.Fatalf("trusted publication: %+v %v", pub, err)
			}
		})
	}
}
