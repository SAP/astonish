package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/google/uuid"

	"google.golang.org/adk/model"
	adksession "google.golang.org/adk/session"
	"google.golang.org/genai"

	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/gorilla/mux"
)

// Strict opt-in: missing built SPA or browser fails an explicitly requested run.
// Uses the real scoped Bot handlers and SQLite, not fetch/SSE/React mocks.
// Authentication/settings are controlled by the shared browser fixture. No
// daemon worker or external provider is claimed by this workspace acceptance.
func TestBotWorkspaceBrowserAcceptance(t *testing.T) {
	if os.Getenv("ASTONISH_MEMORY_BROWSER_ACCEPTANCE") != "1" && os.Getenv("ASTONISH_BOT_BROWSER_ACCEPTANCE") != "1" {
		t.Skip("set ASTONISH_MEMORY_BROWSER_ACCEPTANCE=1 after building web")
	}
	var pkg struct {
		Version string `json:"version"`
	}
	data, err := os.ReadFile("../../web/package.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &pkg); err != nil {
		t.Fatal(err)
	}
	dist, err := filepath.Abs(filepath.Join("../../web/dist", pkg.Version))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(dist, "index.html")); err != nil {
		t.Fatalf("build Studio SPA: %v", err)
	}
	bin := os.Getenv("ASTONISH_BOT_BROWSER_PATH")
	if bin == "" {
		var found bool
		bin, found = launcher.LookPath()
		if !found {
			t.Fatal("set ASTONISH_BOT_BROWSER_PATH to installed Chromium")
		}
	}
	env := setupMemoryHTTPAcceptance(t, &MockLLM{}, nil)
	// Controlled readiness only: test admission, not a daemon/home deployment.
	env.Store.SetBotActivationReadiness(func(_ context.Context, b store.Bot, v store.BotVersion) error {
		if b.OwnerID.String() != env.UserID || b.Scope != store.BotScopePersonal || v.Autonomy.CanContactPeople {
			return fmt.Errorf("unexpected browser fixture activation")
		}
		return nil
	})
	bots := mux.NewRouter()
	registerBotRoutes(bots)
	env.Router.PathPrefix("/api/studio/bots").Handler(entstore.TenantMiddleware(env.Store)(bots))
	page := newStreamBrowserPage(t, env, bin, dist).CancelTimeout().Timeout(3 * time.Minute)
	// The browser writes only an opaque pointer locally; all form values must
	// survive through the real scoped HTTP handler and SQLite repository.
	intakeRoundTrip := func(mode store.BotIntakeMode, fields map[string]string) {
		t.Helper()
		t.Logf("starting %s intake close/reload/resume/discard", mode)
		getJSON := func(path string, want int, target any) {
			t.Helper()
			response, err := env.Client.Get(env.URL + path)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != want {
				t.Fatalf("%s: status %d, want %d", path, response.StatusCode, want)
			}
			if target != nil {
				if err := json.NewDecoder(response.Body).Decode(target); err != nil {
					t.Fatal(err)
				}
			}
		}
		var beforeBots struct {
			Bots []store.Bot `json:"bots"`
		}
		getJSON("/api/studio/bots?scope=personal", http.StatusOK, &beforeBots)
		for label, value := range fields {
			page.MustElement(`[aria-label="` + label + `"]`).MustInput(value)
		}
		t.Logf("%s partial fields entered", mode)
		page.MustElement(`[aria-label="Bot name"]`).MustType(input.Escape)
		page.Timeout(15 * time.Second).MustWait(`() => !document.querySelector(':is([aria-label="Review Chat promotion"], [aria-label="Create a Bot draft"])') || !!document.querySelector(':is([aria-label="Review Chat promotion"], [aria-label="Create a Bot draft"]) [role="alert"]')`)
		if alert := page.MustEval(`() => document.querySelector(':is([aria-label="Review Chat promotion"], [aria-label="Create a Bot draft"]) [role="alert"]')?.textContent || ''`).Str(); alert != "" {
			t.Fatalf("Escape failed to save %s intake: %s", mode, alert)
		}
		page.MustWait(`() => !document.querySelector(':is([aria-label="Review Chat promotion"], [aria-label="Create a Bot draft"])')`)
		t.Logf("%s Escape closed", mode)
		page.MustElementR("button", "^Resume intake$").MustVisible()
		pointerJSON := page.MustEval(`() => {
			const keys = Object.keys(localStorage).filter(k => k.startsWith('astonish:bot-intake:'));
			if (keys.length !== 1) throw new Error('Expected one scoped intake pointer');
			return localStorage.getItem(keys[0]);
		}`).Str()
		var pointer map[string]string
		if err := json.Unmarshal([]byte(pointerJSON), &pointer); err != nil {
			t.Fatal(err)
		}
		if len(pointer) != 2 || pointer["scope"] != "personal" {
			t.Fatalf("local storage contains more than an opaque intake pointer: %v", pointer)
		}
		intakePath := "/api/studio/bots/intake/" + pointer["id"] + "?scope=personal"
		var saved store.BotUnfinishedIntake
		getJSON(intakePath, http.StatusOK, &saved)
		if saved.OwnerID.String() != env.UserID || saved.Scope != store.BotScopePersonal || saved.Content == nil || saved.Content.Mode != mode || saved.Revision < 1 {
			t.Fatalf("wrong scoped persisted intake: %+v", saved)
		}
		encoded, err := json.Marshal(saved.Content)
		if err != nil {
			t.Fatal(err)
		}
		for label, value := range fields {
			if !strings.Contains(string(encoded), value) {
				t.Fatalf("SQLite intake lost %s", label)
			}
		}
		t.Logf("%s persisted content checked; reloading", mode)
		page.MustReload()
		page.MustElementR("button", "^Resume intake$").MustClick()
		t.Logf("%s Resume clicked", mode)
		page.MustWait(`() => !document.body.innerText.includes('Restoring unfinished intake…')`)
		for label, value := range fields {
			t.Logf("checking restored %s field %s", mode, label)
			if got := page.Timeout(10 * time.Second).MustElement(`[aria-label="` + label + `"]`).MustProperty("value").Str(); got != value {
				t.Fatalf("resumed %s = %q, want %q", label, got, value)
			}
		}
		if !page.MustElementR("button", "^Review draft$").MustProperty("disabled").Bool() {
			t.Fatal("partial intake unexpectedly ready for creation review")
		}
		if mode == store.BotIntakeChat && (saved.Content.SourceID == nil || !strings.HasSuffix(page.MustEval(`() => location.hash`).Str(), saved.Content.SourceID.String())) {
			t.Fatal("persisted promotion lost its source Chat selection")
		}
		page.MustElementR("button", "^Cancel intake$").MustClick()
		page.MustElement(`[aria-label="Discard confirmation"]`).MustVisible()
		var retained store.BotUnfinishedIntake
		getJSON(intakePath, http.StatusOK, &retained)
		if !reflect.DeepEqual(saved, retained) {
			t.Fatal("Cancel changed intake before explicit discard")
		}
		page.MustElementR("button", "^Discard intake$").MustClick()
		page.MustWait(`() => !document.querySelector(':is([aria-label="Review Chat promotion"], [aria-label="Create a Bot draft"])')`)
		getJSON(intakePath, http.StatusNotFound, nil)
		page.MustReload()
		page.MustElementR("button", "^New Bot$").MustVisible()
		if page.MustEval(`() => Object.keys(localStorage).some(k => k.startsWith('astonish:bot-intake:')) || [...document.querySelectorAll('button')].some(b => b.textContent === 'Resume intake')`).Bool() {
			t.Fatal("discarded intake reappeared after reload")
		}
		getJSON(intakePath, http.StatusNotFound, nil)
		var afterBots struct {
			Bots []store.Bot `json:"bots"`
		}
		getJSON("/api/studio/bots?scope=personal", http.StatusOK, &afterBots)
		if !reflect.DeepEqual(beforeBots, afterBots) {
			t.Fatal("unfinished intake close/resume/discard created or changed a Bot")
		}
		t.Logf("%s intake: Escape saved scoped SQLite content; reload/Resume restored exact partial fields; Cancel retained then explicit discard removed persisted copy; no Bot created", mode)
	}
	page.MustElementR("button", "^New Bot$").MustClick()
	page.MustElementR("button", "^Use a template or existing schedule$").MustClick()
	intakeRoundTrip(store.BotIntakeBlank, map[string]string{
		"Bot name": "Unfinished browser draft", "Bot language": "English",
		"Bot identity": "Only an unfinished identity", "Bot soul": "Ask before acting",
		"Bot user preferences": "Prefer source links",
	})
	page.MustElement(`[data-testid="chat-input"]`).MustInput("Keep this ordinary Chat draft")
	page.MustElementR("button", "^New Bot$").MustClick()
	page.MustElement(`[aria-label="Bot name"]`).MustInput("Browser watcher")
	page.MustElementR("button", "^Start conversation$").MustClick()
	page.MustElement(`[aria-label="Bot workspace"]`).MustVisible()
	page.MustElementR("p", "draft · Private").MustVisible()
	page.MustElementR("p", "Bot transcript: live").MustVisible()
	if page.MustElement(`[aria-label="Bot workspace"] [data-testid="chat-input"]`).MustProperty("disabled").Bool() {
		t.Fatal("draft conversation unavailable")
	}
	botHash := page.MustEval(`() => location.hash`).Str()
	// Seed a durable execution-owner transcript event, then prove the actual
	// open SSE and shared reducer paint it once and restore it after navigation.
	response, err := env.Client.Get(env.URL + "/api/studio/bots/" + strings.Split(botHash, "/")[4] + "?scope=personal")
	if err != nil {
		t.Fatal(err)
	}
	var stored store.Bot
	err = json.NewDecoder(response.Body).Decode(&stored)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err = env.Services.PersonalSessions.AppendFleetEvent(context.Background(), stored.HomeSessionID.String(), &adksession.Event{ID: "browser-durable-text", Author: "agent", Timestamp: time.Now(), LLMResponse: model.LLMResponse{Content: genai.NewContentFromText("Persisted Bot browser evidence.", "model"), TurnComplete: true}}); err != nil {
		t.Fatal(err)
	}
	page.MustElementR("p", "^Persisted Bot browser evidence\\.$").MustVisible()
	page.MustElementR("button", "^Details$").MustClick()
	for _, panel := range []string{"Runs & activity", "Goals", "Schedule & triggers", "Memory & charter"} {
		page.MustElementR("summary", "^"+panel+"$").MustClick()
		switch panel {
		case "Goals":
			page.MustElementR("p", "^No goals yet\\. Add an observable outcome below\\.$").MustVisible()
			page.MustElementR("label", "^Goal title$").MustElement("input").MustInput("Browser persisted goal")
			page.MustElementR("label", "^Completion criteria$").MustElement("textarea").MustInput("Verify a public source")
			page.MustElementR("button", "^Save goal$").MustClick()
			page.MustElementR("h3", "^Browser persisted goal · active$").MustVisible()
		case "Memory & charter":
			page.MustElementR("h2", "^Memory Health$").MustVisible()
			page.MustElementR("p", "Evaluated: .* · Scope: personal").MustVisible()
			page.MustElementR("p", "^No memory records yet\\.$").MustVisible()
			page.MustElementR("p", "^Version 1 · revision").MustVisible()
		}
	}
	t.Log("real scoped Goals save and empty Memory Health/documents rendered through Details")
	page.MustEval(`() => { location.hash='/chat' }`)
	page.MustWait(`() => !document.querySelector('[aria-label="Bot workspace"]')`)
	if got := page.MustElement(`[data-testid="chat-input"]`).MustProperty("value").Str(); got != "Keep this ordinary Chat draft" {
		t.Fatalf("lost Chat draft: %q", got)
	}
	page.MustEval(`hash => { location.hash=hash }`, botHash)
	page.MustElementR("button", "^Details$").MustClick()
	page.MustElementR("button", "^Review activation$").MustClick()
	page.MustElement(`[aria-label="Lifecycle reason"]`).MustInput("Reviewed supervised policy")
	page.MustElementR("button", "^Confirm activate$").MustClick()
	page.MustElementR("p", "active · Private").MustVisible()
	page.MustElementR("button", "^Close$").MustClick()
	page.MustElement(`[aria-label="Bot workspace"] [data-testid="chat-input"]`).MustInput("Check official source")
	page.MustElement(`[aria-label="Bot workspace"] [data-testid="send-button"]`).MustClick()
	page.MustWait(`() => document.querySelector('[data-testid="chat-input"]').value === ''`)
	if page.MustEval(`() => location.hash`).Str() != botHash {
		t.Fatal("active send left home")
	}
	page.MustReload()
	page.MustElementR("p", "active · Private").MustVisible()
	if page.MustEval(`() => location.hash`).Str() != botHash {
		t.Fatal("home route lost on reload")
	}
	// Exercise durable decisions through the real inbox/API. The pending action
	// is a controlled execution-owner fixture, not a fabricated DOM response.
	org, err := env.Store.ForOrg(env.OrgSlug)
	if err != nil {
		t.Fatal(err)
	}
	repo := org.ForUser(env.UserID).Bots()
	owner, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: env.UserID, OrgSlug: env.OrgSlug, TeamSlug: env.TeamSlug, Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	runs, err := repo.ListRuns(owner, stored.ID, store.BotPage{})
	if err != nil || len(runs) != 1 {
		t.Fatalf("active send runs: %+v %v", runs, err)
	}
	runID := runs[0].ID
	worker := execution.WithBotRunIdentity(owner, execution.BotRunIdentity{BotID: stored.ID, RunID: runID, Version: stored.Version, ActorID: env.UserID, OrgSlug: env.OrgSlug, TeamSlug: env.TeamSlug})
	lease, err := repo.ClaimRun(worker, store.BotClaimRequest{BotID: stored.ID, RunID: runID, WorkerID: "browser-controlled-owner", ExpiresAt: time.Now().UTC().Add(5 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	action := execution.ActionDescriptor{Tool: "send", Effect: execution.EffectSend, Recipient: "review@example.invalid", Arguments: json.RawMessage(`{"body":"Reviewed browser action only"}`)}
	digest, err := action.Digest()
	if err != nil {
		t.Fatal(err)
	}
	approval, err := repo.RequestApproval(worker, lease, store.BotApproval{BotID: stored.ID, RunID: runID, Version: stored.Version, Action: action, ActionDigest: digest, ExpiresAt: time.Now().UTC().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	run, err := repo.GetRun(owner, stored.ID, runID)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := json.Marshal(bot.ApprovalCheckpoint{Format: bot.ApprovalCheckpointFormat, ApprovalID: approval.ID, BotID: stored.ID, RunID: runID, SessionID: run.SessionID, Version: stored.Version, ActionDigest: digest, Fence: lease.Fence})
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunWaitingApproval, Checkpoint: checkpoint, Report: store.BotReport{Summary: "Waiting for browser review", NeedsApproval: []store.RunID{approval.ID}}}); err != nil {
		t.Fatal(err)
	}
	page.MustReload()
	page.MustElementR("button", "^Details$").MustClick()
	page.MustElementR("button", "^Pause$").MustClick()
	page.MustElement(`[aria-label="Lifecycle reason"]`).MustInput("Pause before reviewing exact action")
	page.MustElementR("button", "^Confirm pause$").MustClick()
	page.MustElementR("p", "paused · Private").MustVisible()
	page.MustElementR("summary", "^Review inbox$").MustClick()
	page.MustElementR(`[role="dialog"] [aria-label="Bot review"] pre`, "review@example.invalid").MustVisible()
	page.MustElementR(`[role="dialog"] [aria-label="Bot review"] pre`, "Reviewed browser action only").MustVisible()
	page.MustElementR(`[role="dialog"] [aria-label="Bot review"] button`, "^Approve & continue$").MustClick()
	page.MustElementR(`[role="dialog"] [aria-label="Bot review"] p`, "Approved; continuation blocked by pause").MustVisible()
	page.MustReload()
	page.MustElementR("button", "^Details$").MustClick()
	page.MustElementR("summary", "^Review inbox$").MustClick()
	page.MustElementR(`[role="dialog"] [aria-label="Bot review"] p`, "^approved$").MustVisible()
	page.MustElementR(`[role="dialog"] [aria-label="Bot review"] pre`, "review@example.invalid").MustVisible()
	page.MustElementR(`[role="dialog"] [aria-label="Bot review"] pre`, "Reviewed browser action only").MustVisible()
	decisions, err := repo.ListApprovals(owner, stored.ID, store.BotPage{})
	if err != nil || len(decisions) != 1 || decisions[0].Status != store.ApprovalApproved || decisions[0].ActionDigest != digest {
		t.Fatalf("decision not persisted exactly: %+v %v", decisions, err)
	}
	run, err = repo.GetRun(owner, stored.ID, runID)
	if err != nil || run.Status != store.RunWaitingApproval {
		t.Fatalf("paused decision must not resume: %+v %v", run, err)
	}
	if !page.MustElementR(`[role="dialog"] button`, "^Continue approved action$").MustProperty("disabled").Bool() {
		t.Fatal("paused continuation enabled")
	}
	t.Log("approved exact action survived reload with continuation blocked by pause")
	page.MustElementR("summary", "^Goals$").MustClick()
	page.MustElementR("h3", "^Browser persisted goal · active$").MustVisible()
	page.MustElementR("p", "paused · Private").MustVisible()
	page.MustElementR("button", "^Delete Bot$").MustClick()
	page.MustElement(`[aria-label="Lifecycle reason"]`).MustInput("Retain read-only history")
	page.MustElementR("button", "^Confirm delete$").MustClick()
	page.MustElementR("p", "deleted · Private").MustVisible()
	page.MustElementR("button", "^Close$").MustClick()
	if !page.MustElement(`[aria-label="Bot workspace"] [data-testid="chat-input"]`).MustProperty("disabled").Bool() {
		t.Fatal("deleted composer can execute")
	}
	// Promote a real persisted Chat, selecting only reviewed text. Compare the
	// complete stored metadata and transcript before/after, not just the title.
	sourceID := uuid.NewString()
	if err = env.Services.PersonalSessions.AddSessionMeta(context.Background(), store.SessionMeta{ID: sourceID, AppName: studioChatAppName, UserID: env.UserID, Title: "Original browser Chat", Purpose: store.SessionPurposeChat, CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err = env.Services.PersonalSessions.AppendFleetEvent(context.Background(), sourceID, &adksession.Event{ID: "browser-source-event", Author: "agent", Timestamp: time.Now(), LLMResponse: model.LLMResponse{Content: genai.NewContentFromText("Original Chat evidence stays here.", "model"), TurnComplete: true}}); err != nil {
		t.Fatal(err)
	}
	before, err := env.Services.PersonalSessions.GetSessionMeta(context.Background(), sourceID)
	if err != nil {
		t.Fatal(err)
	}
	beforeEvents, err := env.Services.PersonalSessions.ReadTranscriptEvents(context.Background(), "", env.UserID, sourceID)
	if err != nil || len(beforeEvents) != 1 {
		t.Fatalf("source transcript fixture: %v %v", beforeEvents, err)
	}
	page.MustNavigate("about:blank")
	page.MustNavigate(env.URL + "/#/chat/" + sourceID)
	page.MustWait(`() => document.readyState === 'complete'`)
	page.MustWait(`() => document.body.innerText.includes('Original Chat evidence stays here.')`)
	page.MustElementR("button", "^Promote Chat to Bot draft$").MustClick()
	page.MustElementR("button", "^Add memory$").MustClick()
	page.MustElementR("button", "^Add goal$").MustClick()
	intakeRoundTrip(store.BotIntakeChat, map[string]string{
		"Bot name": "Unfinished browser promotion", "Carryover summary": "Partial selected summary",
		"Memory content 1": "Partial selected fact", "Goal title 1": "Unfinished goal without criteria",
	})
	page.MustElementR("button", "^Promote Chat to Bot draft$").MustClick()
	page.MustElement(`[aria-label="Bot name"]`).MustInput("Promoted browser watcher")
	page.MustElement(`[aria-label="Bot charter"]`).MustInput("Watch selected public changes only")
	page.MustElement(`[aria-label="Carryover summary"]`).MustInput("Reviewed browser summary")
	page.MustElementR("button", "^Add memory$").MustClick()
	page.MustElement(`[aria-label="Memory content 1"]`).MustInput("Selected browser fact")
	page.MustElementR("button", "^Add goal$").MustClick()
	page.MustElement(`[aria-label="Goal title 1"]`).MustInput("Reviewed browser goal")
	page.MustElement(`[aria-label="Goal criteria 1"]`).MustInput("Report an official change with a source")
	page.MustElementR("button", "^Review draft$").MustClick()
	page.MustElement(`[aria-label="Reviewed carryover"]`).MustVisible()
	if !page.MustElementR("button", "^Save reviewed draft$").MustProperty("disabled").Bool() {
		t.Fatal("unreviewed promotion enabled")
	}
	page.MustElement(`:is([aria-label="Review Chat promotion"], [aria-label="Create a Bot draft"]) input[type="checkbox"]`).MustClick()
	page.MustElementR("button", "^Save reviewed draft$").MustClick()
	page.MustWait(`() => !!(document.querySelector('[aria-label="Bot workspace"]') || document.querySelector(':is([aria-label="Review Chat promotion"], [aria-label="Create a Bot draft"]) [role="alert"]'))`)
	if alert := page.MustEval(`() => document.querySelector(':is([aria-label="Review Chat promotion"], [aria-label="Create a Bot draft"]) [role="alert"]')?.textContent || ''`).Str(); alert != "" {
		after, metaErr := env.Services.PersonalSessions.GetSessionMeta(context.Background(), sourceID)
		afterEvents, transcriptErr := env.Services.PersonalSessions.ReadTranscriptEvents(context.Background(), "", env.UserID, sourceID)
		if metaErr != nil || transcriptErr != nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(beforeEvents, afterEvents) {
			t.Fatalf("rejected promotion changed source Chat: metadata=%v transcript=%v", metaErr, transcriptErr)
		}
		_, intakeSupported := repo.(store.BotIntakeStore)
		t.Fatalf("reviewed promotion failed through real scoped API: %s (repository %T implements BotIntakeStore=%t; source Chat remains unchanged)", alert, repo, intakeSupported)
	}
	page.MustElement(`[aria-label="Bot workspace"]`).MustVisible()
	page.MustElementR("p", "draft · Private").MustVisible()
	promotedHash := page.MustEval(`() => location.hash`).Str()
	if promotedHash == botHash {
		t.Fatal("promotion reused unrelated Bot")
	}
	page.MustElementR("button", "^Details$").MustClick()
	page.MustElementR("summary", "^Goals$").MustClick()
	page.MustElementR("h3", "^Reviewed browser goal · active$").MustVisible()
	page.MustElementR("p", "^Completion criteria: Report an official change with a source$").MustVisible()
	page.MustElementR("summary", "^Memory & charter$").MustClick()
	page.MustElementR("h2", "^Memory Health$").MustVisible()
	page.MustElementR("pre", "Selected browser fact").MustVisible()
	page.MustElementR("pre", "Reviewed browser summary").MustVisible()
	page.MustElementR("p", "Evaluated: .* · Scope: personal").MustVisible()
	if page.MustEval(`() => document.body.innerText.includes('Original Chat evidence stays here.')`).Bool() {
		t.Fatal("unselected transcript copied into Bot")
	}
	after, err := env.Services.PersonalSessions.GetSessionMeta(context.Background(), sourceID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("promotion changed source metadata: before=%+v after=%+v err=%v", before, after, err)
	}
	afterEvents, err := env.Services.PersonalSessions.ReadTranscriptEvents(context.Background(), "", env.UserID, sourceID)
	if err != nil || !reflect.DeepEqual(beforeEvents, afterEvents) {
		t.Fatalf("promotion changed source transcript: before=%+v after=%+v err=%v", beforeEvents, afterEvents, err)
	}
	page.MustNavigate("about:blank")
	page.MustNavigate(env.URL + "/#/chat/" + sourceID)
	page.MustWait(`() => document.readyState === 'complete'`)
	page.MustWait(`() => document.body.innerText.includes('Original Chat evidence stays here.')`)
	page.MustNavigate(env.URL + "/" + promotedHash)
	page.MustElementR("p", "draft · Private").MustVisible()
	page.MustElementR("button", "^Details$").MustClick()
	page.MustElementR("summary", "^Goals$").MustClick()
	page.MustElementR("h3", "^Reviewed browser goal · active$").MustVisible()
	t.Log("PASS built SPA: minimal private draft → live scoped transcript → Details goals/memory → preserved Chat draft → explicit activation → stable home send/reload → exact approve-and-continue persisted/reloaded → pause → deleted read-only history → reviewed Chat promotion → real goals/documents/Memory Health → unchanged source Chat → restored promoted goal (controlled pending-action fixture; no scheduled worker result claimed)")
}
