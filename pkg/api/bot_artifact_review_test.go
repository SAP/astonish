package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/SAP/astonish/pkg/bot"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

func TestBotArtifactReviewCompleteBoundedCoverage(t *testing.T) {
	body := strings.Repeat("design detail and verification\n", 817)
	w := &store.BotWorkState{Candidate: &store.BotWorkCandidate{Text: "Saved report", Artifacts: []store.BotArtifact{{}}, Bodies: map[string]string{(store.BotArtifact{}).ID.String(): body}}}
	in := botContentInput(w)
	if len(in.Items) > botWorkContentMaxItems {
		t.Fatalf("complete report cannot reach reviewer: %d items", len(in.Items))
	}
	var joined strings.Builder
	for _, item := range in.Items {
		if strings.HasPrefix(item.ID, "body:1:") {
			joined.WriteString(item.Text)
		}
	}
	if joined.String() != body {
		t.Fatal("artifact bytes lost during review segmentation")
	}
}

func hydrateCapturedReviewArtifact(t *testing.T, w *store.BotWorkState) {
	t.Helper()
	raw, err := os.ReadFile(os.Getenv("ASTONISH_BOT_REAL_REVIEW_ARTIFACT"))
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 44779 || fmt.Sprintf("%x", sha256.Sum256(raw)) != "e4d5778d062adb54220986bae153a645ae134500fcc364c20dd1b1163ec24807" {
		t.Fatal("exact captured artifact changed")
	}
	if len(w.Candidate.Artifacts) != 1 {
		t.Fatal("captured receipt missing")
	}
	receipt := w.Candidate.Artifacts[0]
	resources := &BotChatResources{ArtifactBody: func(_ context.Context, a store.BotArtifact) ([]byte, error) {
		if !reflect.DeepEqual(a, receipt) {
			return nil, store.ErrBotForbidden
		}
		return raw, nil
	}}
	req := bot.RunRequest{Bot: store.Bot{ID: w.Contract.BotID}, Run: store.BotRun{ID: w.Contract.RunID}}
	for _, mode := range []string{"foreign-run", "corrupt"} {
		bad := w.Clone()
		badReq := req
		r := *resources
		if mode == "foreign-run" {
			badReq.Run.ID = [16]byte{}
		} else {
			r.ArtifactBody = func(context.Context, store.BotArtifact) ([]byte, error) { return []byte("corrupt"), nil }
		}
		if botEmployeeCaptureArtifacts(context.Background(), badReq, &r, bad) == nil {
			t.Fatal("accepted " + mode)
		}
	}
	for _, mode := range []string{"missing", "corrupt"} {
		bad := w.Clone()
		bad.Candidate.Bodies[receipt.ID.String()] = "corrupt"
		if mode == "missing" {
			delete(bad.Candidate.Bodies, receipt.ID.String())
		}
		probe := &capturedReviewEntryProbe{}
		_, _, err := reviewBotEmployeeCandidate(context.Background(), req, probe, bad, &store.BotUsage{})
		if err == nil || probe.called {
			t.Fatal("unbound artifact reached reviewer: " + mode)
		}
	}
	before := w.Candidate.Digest
	if err := botEmployeeCaptureArtifacts(context.Background(), req, resources, w); err != nil {
		t.Fatal(err)
	}
	// This is the same pre-save digest transition as production evidence capture.
	w.Candidate.Digest = w.Candidate.ContentDigest()
	in := botContentInput(w)
	if len(in.Items) > botWorkContentMaxItems {
		t.Fatal("unbounded coverage")
	}
	var joined strings.Builder
	for _, item := range in.Items {
		if strings.HasPrefix(item.ID, "body:1:") {
			joined.WriteString(item.Text)
		}
	}
	if joined.String() != string(raw) {
		t.Fatal("captured report content omitted")
	}
	t.Logf("artifact=%s bytes=%d items=%d original_digest=%s hydrated_digest=%s", receipt.SHA256, len(raw), len(in.Items), before, w.Candidate.Digest)
}

func TestBotArtifactReviewHydrationSafety(t *testing.T) {
	ctx, req, _ := botChatTestRequest(t)
	body := "complete immutable document\n"
	ref := store.BotContentRef{Format: store.BotContentFormatVersion, Scope: store.BotContentScope{BotID: req.Bot.ID, OwnerID: req.Bot.OwnerID.String()}, Kind: store.BotContentArtifact, Key: "content-v1/artifact-1-review", Revision: 1, SizeBytes: int64(len(body)), SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(body)))}
	a := store.BotArtifact{ID: req.Run.ID, HomeID: req.Bot.Home.ID, RelativePath: "report.md", ContentRef: &ref, SHA256: ref.SHA256, SizeBytes: ref.SizeBytes}
	for _, mode := range []string{"complete", "corrupt", "missing-reader", "binary", "foreign-run", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			w := &store.BotWorkState{Contract: store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID}, Candidate: &store.BotWorkCandidate{Text: "Saved", Artifacts: []store.BotArtifact{a}}}
			resources := &BotChatResources{ArtifactBody: func(context.Context, store.BotArtifact) ([]byte, error) { return []byte(body), nil }}
			switch mode {
			case "corrupt":
				resources.ArtifactBody = func(context.Context, store.BotArtifact) ([]byte, error) { return []byte("corrupt"), nil }
			case "missing-reader":
				resources.ArtifactBody = nil
			case "binary":
				w.Candidate.Artifacts[0].RelativePath = "report.bin"
			case "foreign-run":
				w.Contract.RunID = [16]byte{}
			case "oversize":
				w.Candidate.Text = strings.Repeat("x", store.BotWorkMaxCandidateBytes)
			}
			w.AgentWork = &store.BotAgentWork{}
			err := botEmployeeCaptureEvidence(ctx, req, resources, w)
			if mode == "complete" {
				if w.Candidate.Digest != w.Candidate.ContentDigest() {
					t.Fatal("hydration not bound before save")
				}
				if err != nil || w.Candidate.Bodies[a.ID.String()] != body {
					t.Fatalf("hydrate: %v", err)
				}
			} else if err == nil {
				t.Fatal("accepted " + mode)
			}
		})
	}
}

func TestBotCapturedArtifactReviewCoverage(t *testing.T) {
	if os.Getenv("ASTONISH_BOT_REAL_REVIEW_ARTIFACT") == "" {
		t.Skip("captured artifact required")
	}
	raw, err := os.ReadFile(os.Getenv("ASTONISH_BOT_REAL_REVIEW_WORK"))
	if err != nil {
		t.Fatal(err)
	}
	var w store.BotWorkState
	if err := json.Unmarshal(raw, &w); err != nil {
		t.Fatal(err)
	}
	if w.Candidate.Digest != w.Candidate.ContentDigest() {
		t.Fatal("saved digest mismatch")
	}
	hydrateCapturedReviewArtifact(t, &w)
}
