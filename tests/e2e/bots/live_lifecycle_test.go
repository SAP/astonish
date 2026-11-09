//go:build e2e

package bots_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/tests/e2eboot"
	"github.com/google/uuid"
)

// TestBotsLiveLifecycle is a composed deployed acceptance, not a route mock or
// seeded active Bot. Explicit selection without dedicated infrastructure fails.
// The first slice is toolless real-provider execution; its retained artifact is
// an explicitly identified sandbox fixture, not claimed as model-produced proof.
func TestBotsLiveLifecycle(t *testing.T) {
	r := e2eboot.BootstrapLiveBotAcceptance(t)
	c := r.Client
	const suffix = "?scope=personal"
	var b store.Bot
	liveBotJSON(t, c.Post(t, "/api/studio/bots"+suffix, map[string]any{"name": "Live lifecycle", "idempotency_key": uuid.NewString(), "definition": map[string]string{"charter": "Respond concisely to the owner's lifecycle verification request. Do not call tools."}}), http.StatusCreated, &b)
	if b.Status != store.BotDraft || b.ID == uuid.Nil || b.HomeSessionID == uuid.Nil || b.Home.ID != uuid.Nil {
		t.Fatalf("new draft is not inactive/unprovisioned: %+v", b)
	}
	root := "/api/studio/bots/" + b.ID.String()
	request := func() map[string]any {
		return map[string]any{"version": b.Version, "idempotency_key": uuid.NewString(), "instructions": "Reply with exactly LIVE_LIFECYCLE_OK. Do not use tools."}
	}
	liveBotJSON(t, c.Post(t, root+"/runs"+suffix, request()), http.StatusForbidden, nil)
	b = r.RegisterHome(t, b)
	v := store.DefaultBotVersion()
	tokens := int64(12000)
	v.Budget.MaxRuntimeSeconds = 180
	v.Budget.MaxTokens = &tokens
	v.Delivery.Notify = store.NotifyNever
	liveBotJSON(t, c.Put(t, root+"/configuration"+suffix, store.BotConfigurationRequest{ExpectedRevision: b.Revision, ExpectedVersion: b.Version, Reviewed: true, Reason: "Reviewed bounded toolless live lifecycle", Autonomy: v.Autonomy, Budget: v.Budget, Delivery: v.Delivery}), http.StatusOK, &b)
	if b.Status != store.BotDraft {
		t.Fatalf("configuration activated draft: %s", b.Status)
	}
	r.PinModel(t, b)
	lifecycle := func(method, action string, want store.BotStatus) {
		t.Helper()
		body := map[string]any{"expected_revision": b.Revision, "reason": "Live lifecycle acceptance"}
		if method == http.MethodDelete {
			// Client.Delete has no body; send through the actual server with the same JWT.
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			req, err := http.NewRequestWithContext(r.Context, method, r.Harness.BaseURL+root+suffix, strings.NewReader(string(raw)))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+c.Token())
			req.Header.Set("X-Astonish-Team", c.TeamSlug())
			req.Header.Set("Content-Type", "application/json")
			resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
			if err != nil {
				t.Fatal(err)
			}
			liveBotJSON(t, resp, http.StatusOK, &b)
		} else {
			liveBotJSON(t, c.Post(t, root+action+suffix, body), http.StatusOK, &b)
		}
		if b.Status != want {
			t.Fatalf("%s: status=%s want=%s", action, b.Status, want)
		}
	}
	lifecycle(http.MethodPost, "/activate", store.BotActive)
	home, homeSession := b.Home, b.HomeSessionID
	var run store.BotRun
	liveBotJSON(t, c.Post(t, root+"/runs"+suffix, request()), http.StatusAccepted, &run)
	if run.ID == uuid.Nil || run.BotID != b.ID || run.SessionID == homeSession {
		t.Fatalf("run identity: %+v", run)
	}
	deadline, stop := context.WithTimeout(r.Context, 4*time.Minute)
	defer stop()
	for !run.Status.Terminal() {
		if run.Status == store.RunWaitingApproval || run.Status == store.RunInterrupted {
			t.Fatalf("unexpected nonterminal outcome: %+v", run)
		}
		select {
		case <-deadline.Done():
			t.Fatalf("worker did not complete run %s: %v", run.ID, deadline.Err())
		case <-time.After(250 * time.Millisecond):
		}
		liveBotJSON(t, c.Get(t, root+"/runs/"+run.ID.String()+suffix), http.StatusOK, &run)
	}
	if run.Status != store.RunSucceeded || !strings.Contains(run.Report.Summary, "LIVE_LIFECYCLE_OK") {
		t.Fatalf("real worker/model report: %+v", run)
	}
	// Stop only after HTTP observes the committed result; cleanup has already
	// destroyed the run pod. Offline reading must not recreate its physical session.
	r.Worker.Stop()
	const marker = "live-lifecycle-artifact"
	digest := sha256.Sum256([]byte(marker))
	artifact := store.BotArtifact{ID: uuid.New(), HomeID: home.ID, RelativePath: "lifecycle.txt", SHA256: hex.EncodeToString(digest[:])}
	readArtifact := func() {
		t.Helper()
		reader, err := r.Homes.ReadArtifact(r.Context, home, artifact)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(reader)
		reader.Close()
		if err != nil || string(data) != marker {
			t.Fatalf("offline home artifact: %q %v", data, err)
		}
	}
	readArtifact()
	liveBotJSON(t, c.Get(t, root+suffix), http.StatusOK, &b)
	lifecycle(http.MethodPost, "/pause", store.BotPaused)
	liveBotJSON(t, c.Post(t, root+"/runs"+suffix, request()), http.StatusForbidden, nil)
	var runs struct {
		Runs []store.BotRun `json:"runs"`
	}
	liveBotJSON(t, c.Get(t, root+"/runs"+suffix), http.StatusOK, &runs)
	if len(runs.Runs) != 1 || runs.Runs[0].ID != run.ID {
		t.Fatalf("rejected admission created work: %+v", runs)
	}
	// Restore is a deletion-recovery operation, not resume-from-pause.
	lifecycle(http.MethodDelete, "", store.BotDeleted)
	lifecycle(http.MethodPost, "/restore", store.BotPaused)
	if b.Home != home || b.HomeSessionID != homeSession {
		t.Fatal("restore replaced durable home")
	}
	readArtifact()
	liveBotJSON(t, c.Get(t, root+"/runs/"+run.ID.String()+suffix), http.StatusOK, &run)
	if run.Status != store.RunSucceeded || !strings.Contains(run.Report.Summary, "LIVE_LIFECYCLE_OK") {
		t.Fatal("restore lost completed report")
	}
	liveBotJSON(t, c.Post(t, root+"/runs"+suffix, request()), http.StatusForbidden, nil)
	t.Logf("real HTTP lifecycle, worker/model report and offline retained-home fixture verified: bot=%s run=%s", b.ID, run.ID)
}

func liveBotJSON(t *testing.T, response *http.Response, status int, target any) {
	t.Helper()
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != status {
		t.Fatalf("%s %s: HTTP %d want %d: %s", response.Request.Method, response.Request.URL.Path, response.StatusCode, status, data)
	}
	if target != nil {
		if err := json.Unmarshal(data, target); err != nil {
			t.Fatalf("decode live Bot response: %v: %s", err, data)
		}
	}
}
