package api

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
	"iter"
	"strings"
	"testing"
)

func TestBotArtifactPatchCorrectionBound(t *testing.T) {
	for _, alwaysInvalid := range []bool{false, true} {
		ctx, req, _ := botChatTestRequest(t)
		calls, checks := 0, 0
		llm := &agentCheckModel{run: func(_ context.Context, q *model.LLMRequest) iter.Seq2[*model.LLMResponse, error] {
			return func(yield func(*model.LLMResponse, error) bool) {
				calls++
				if calls > 2 {
					t.Fatal("extra correction call")
				}
				if calls == 2 && !strings.Contains(q.Config.SystemInstruction.Parts[0].Text, "EXACT ARTIFACT PATCH CORRECTION") {
					t.Fatal("missing exact-edit diagnostic")
				}
				yield(&model.LLMResponse{Content: genai.NewContentFromText(`{"edits":[{"old":"original","new":"qualified"}]}`, "model")}, nil)
			}
		}}
		var usage store.BotUsage
		var patch botArtifactTextPatch
		err := botWorkJSON(ctx, req, llm, botArtifactRepairPrompt, map[string]string{"artifact": "original"}, &patch, &usage, func(v any) error {
			checks++
			if len(patch.Edits) != 0 {
				t.Fatal("invalid output leaked")
			}
			if checks == 1 || alwaysInvalid {
				return errors.New("artifact patch missing edit")
			}
			return nil
		})
		if calls != 2 || checks != 2 || (err != nil) != alwaysInvalid {
			t.Fatalf("calls=%d checks=%d err=%v", calls, checks, err)
		}
		if alwaysInvalid && len(patch.Edits) != 0 {
			t.Fatal("invalid patch escaped")
		}
	}
}

func TestBotArtifactTextPatchSafety(t *testing.T) {
	for _, raw := range []string{`{"old":"x"}`, `{"old":"x","new":null}`, `{"old":"x","new":"y","extra":1}`} {
		var e botArtifactTextEdit
		if json.Unmarshal([]byte(raw), &e) == nil {
			t.Fatal("invalid typed edit accepted")
		}
	}
	original := "prefix\nunique claim\naaaa\n" + strings.Repeat("untouched diagram and detail\n", 2000)
	id, botID := uuid.New(), uuid.New()
	ref := &store.BotContentRef{Format: store.BotContentFormatVersion, Scope: store.BotContentScope{BotID: botID, OwnerID: uuid.NewString()}, Kind: store.BotContentArtifact, Key: "content-v1/artifact-1-patch", Revision: 1, SHA256: botAgentDigest([]byte(original)), SizeBytes: int64(len(original))}
	w := &store.BotWorkState{Contract: store.BotWorkContract{BotID: botID}, Candidate: &store.BotWorkCandidate{Revision: 1, Artifacts: []store.BotArtifact{{ID: id, ContentRef: ref, SHA256: ref.SHA256, SizeBytes: ref.SizeBytes}}, Bodies: map[string]string{id.String(): original}}}
	w.Candidate.Digest = w.Candidate.ContentDigest()
	for _, tc := range []struct {
		name  string
		edits []botArtifactTextEdit
		valid bool
	}{
		{"valid", []botArtifactTextEdit{{"unique claim", "qualified proposal"}}, true},
		{"missing", []botArtifactTextEdit{{"absent", "new"}}, false},
		{"ambiguous overlapping occurrences", []botArtifactTextEdit{{"aaa", "new"}}, false},
		{"noop", []botArtifactTextEdit{{"unique claim", "unique claim"}}, false},
		{"empty", []botArtifactTextEdit{{"", "new"}}, false},
		{"overlap", []botArtifactTextEdit{{"unique claim", "new"}, {"claim", "other"}}, false},
		{"sequential", []botArtifactTextEdit{{"unique claim", "invented"}, {"invented", "other"}}, false},
		{"oversize", []botArtifactTextEdit{{"unique claim", strings.Repeat("x", store.BotWorkMaxContractBytes)}}, false},
		{"too many", make([]botArtifactTextEdit, 9), false},
		{"none", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := applyBotArtifactTextPatch(w, botArtifactTextPatch{Edits: tc.edits})
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%t err=%v", tc.valid, err)
			}
			if tc.valid && got != strings.Replace(original, "unique claim", "qualified proposal", 1) {
				t.Fatal("outside patch changed")
			}
			if w.Candidate.Bodies[id.String()] != original || w.Candidate.Digest != w.Candidate.ContentDigest() {
				t.Fatal("original mutated")
			}
		})
	}
	bad := w.Clone()
	bad.Candidate.Bodies[id.String()] += "tampered"
	bad.Candidate.Digest = bad.Candidate.ContentDigest()
	if _, err := applyBotArtifactTextPatch(bad, botArtifactTextPatch{Edits: []botArtifactTextEdit{{"unique claim", "new"}}}); err == nil {
		t.Fatal("unbound source accepted")
	}
}
