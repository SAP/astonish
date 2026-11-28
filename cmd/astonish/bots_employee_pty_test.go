package astonish

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
	"github.com/google/uuid"
	adksession "google.golang.org/adk/session"
	"google.golang.org/genai"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// employeePTYCommittedHome uses the real scoped repository writer and immutable
// publication reader. The transport fixture cannot grant Employee speech authority.
func employeePTYCommittedHome(t *testing.T) (context.Context, store.BotStore, store.Bot, []*adksession.Event, store.BotFilePublication) {
	ctx := context.Background()
	dir := t.TempDir()
	_, db, err := entstore.NewPlatformServices(ctx, entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	org := &store.Organization{ID: uuid.NewString(), Slug: "pty", Name: "PTY", Status: "active"}
	if err = db.Organizations().Create(ctx, org); err != nil {
		t.Fatal(err)
	}
	if err = db.ProvisionOrg(ctx, org.ID, org.Slug); err != nil {
		t.Fatal(err)
	}
	ods, err := db.ForOrg(org.Slug)
	if err != nil {
		t.Fatal(err)
	}
	team := &store.Team{ID: uuid.NewString(), Slug: "team", Name: "Team", SchemaName: "team_team"}
	if err = ods.Teams().CreateTeam(ctx, team); err != nil {
		t.Fatal(err)
	}
	owner := uuid.NewString()
	if err = db.Users().Create(ctx, &store.User{ID: owner, Email: owner + "@example.invalid", DisplayName: "Owner", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if err = db.Organizations().AddMember(ctx, owner, org.ID, "member"); err != nil {
		t.Fatal(err)
	}
	if err = ods.Teams().AddMember(ctx, &store.TeamMembership{UserID: owner, TeamID: team.ID, Role: "member"}); err != nil {
		t.Fatal(err)
	}
	ctx, err = execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodOAuth, Surface: execution.SurfaceTerminal, Subject: owner, OrgSlug: org.Slug, TeamSlug: team.Slug, Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx = store.WithTenantContext(ctx, &store.TenantContext{UserID: owner, OrgSlug: org.Slug, TeamSlug: team.Slug})
	personal := ods.ForUser(owner)
	repo := personal.Bots()
	version := store.DefaultBotVersion()
	version.Charter = "Report the controlled result to the owner."
	b, err := repo.Create(ctx, store.BotCreateRequest{Name: "Employee", OwnerID: uuid.MustParse(owner), Version: version, IdempotencyKey: "pty"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindService, ClientID: "pty-home", Authentication: execution.AuthMethod("internal"), Surface: execution.SurfaceCode, Actor: "pty-home", OrgSlug: org.Slug, TeamSlug: team.Slug, Authenticated: true, Scopes: []string{entstore.BotHomeRegistrationScope}})
	if err != nil {
		t.Fatal(err)
	}
	home := store.BotHomeBinding{ID: uuid.New(), BotID: b.ID, Scope: store.BotScopePersonal, Backend: "controlled", StorageKey: "pty-home", Revision: 1}
	if err = entstore.RegisterBotHomeBinding(service, repo, entstore.BotHomeRegistration{BotID: b.ID, ExpectedRevision: b.Revision, Home: home}, func(context.Context, entstore.BotHomeRegistration) error { return nil }); err != nil {
		t.Fatal(err)
	}
	b, err = repo.Get(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	var rows []*adksession.Event
	var files store.BotFilePublication
	for i, text := range []string{"I need the destination before proceeding.", "The schedule is enabled and verified."} {
		run, e := repo.RequestRun(ctx, store.BotRunRequest{BotID: b.ID, Version: b.Version, Intent: store.BotIntentRegular, Trigger: store.TriggerManual, IdempotencyKey: fmt.Sprintf("pty-%d", i), Instructions: "Weekly request"})
		if e != nil {
			t.Fatal(e)
		}
		worker := execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: b.ID, RunID: run.ID, Version: b.Version, ActorID: owner, OrgSlug: org.Slug, TeamSlug: team.Slug})
		lease, e := repo.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: run.ID, WorkerID: "pty", ExpiresAt: time.Now().Add(time.Minute)})
		if e != nil {
			t.Fatal(e)
		}
		workStore := repo.(store.BotWorkStore)
		work, e := workStore.LoadWork(worker, lease)
		if e != nil {
			t.Fatal(e)
		}
		next := work.Clone()
		next.Phase = store.BotWorkFinished
		next.AgentWork.Disposition = store.AgentWorkAnswered
		next.Candidate = &store.BotWorkCandidate{Revision: 1, EventID: uuid.NewString(), Text: text}
		if i == 1 {
			body := "IMMUTABLE proof preview"
			digest := sha256.Sum256([]byte(body))
			artifactID := uuid.New()
			staging, err := db.BotContentStaging(ctx, store.BotContentScope{OrgID: org.Slug, TeamID: team.Slug, OwnerID: owner, BotID: b.ID})
			if err != nil {
				t.Fatal(err)
			}
			ref, err := staging.Put(ctx, store.BotContentArtifact, 1, int64(len(body)), strings.NewReader(body), func(commit func() error) error { return commit() })
			_ = staging.Close()
			if err != nil || ref == nil || ref.SHA256 != hex.EncodeToString(digest[:]) {
				t.Fatalf("persisted artifact: %+v %v", ref, err)
			}
			next.Candidate.Artifacts = []store.BotArtifact{{ID: artifactID, HomeID: b.Home.ID, RelativePath: "proof.md", SHA256: ref.SHA256, SizeBytes: ref.SizeBytes, MediaType: "text/markdown", ContentRef: ref}}
			next.Candidate.Bodies = map[string]string{artifactID.String(): body}
		}
		next.Candidate.Digest = next.Candidate.ContentDigest()
		// Deterministic model assessment evidence is passed through the production
		// work validator and CommitRun; speech provenance is never constructed here.
		if next.EmployeeReviewPolicy == store.BotEmployeeReviewEvidenceBoundV1 {
			next.Criteria = []store.BotWorkCriterion{{ID: "pty-result", SourceIDs: []string{"assignment", "charter"}, Text: "Report the controlled result"}}
			next.Review = &store.BotWorkReview{CandidateDigest: next.Candidate.Digest, Criteria: []store.BotCriterionAssessment{{CriterionID: "pty-result", Verdict: store.BotCriterionSatisfied, Method: store.BotAssessmentModel, EvidenceRefs: []string{"candidate:" + next.Candidate.Digest}, Explanation: "Scripted provider result"}}}
		}
		if e := next.Validate(); e != nil {
			t.Fatalf("candidate work invalid: %+v: %v", next, e)
		}
		if e := store.ValidateBotWorkUpdate(work, next); e != nil {
			t.Fatalf("candidate transition invalid: old=%+v next=%+v: %v", work, next, e)
		}
		work, e = workStore.SaveWork(worker, store.BotWorkUpdate{Lease: lease, ExpectedRevision: work.Revision, State: *next})
		if e != nil {
			t.Fatal(e)
		}
		report := store.BotReport{Summary: text, Work: store.BuildBotWorkProjection(work), Artifacts: next.Candidate.Artifacts}
		if e = repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: report}); e != nil {
			t.Fatal(e)
		}
		pub, e := repo.(store.BotPublicationStore).PublishReport(ctx, store.BotPublicationRequest{BotID: b.ID, RunID: run.ID})
		if e != nil {
			t.Fatal(e)
		}
		verified, e := repo.(store.BotPublicationReader).ReadBotPublication(ctx, b.ID, run.ID, pub.ReportSequence)
		if e != nil || verified == nil || verified.Text != text || !verified.EmployeeSpeech || verified.ReportSequence != pub.ReportSequence || verified.SourceSessionID != run.SessionID || verified.ReportDigest == "" {
			t.Fatalf("committed Employee publication: %+v %v", verified, e)
		}
		if i == 1 {
			f, e := repo.(store.BotFilePublicationStore).ReadFilePublication(ctx, b.ID, run.ID, pub.ReportSequence)
			if e != nil {
				t.Fatal(e)
			}
			files = *f
		}
	}
	// Publish a terminal partial result through the same scoped writer. No candidate
	// means there is no speech authority; the durable Home event is neutral state.
	partialReceipt, err := repo.(store.BotMessageStore).AdmitMessage(ctx, store.BotMessageRequest{BotID: b.ID, Version: b.Version, IdempotencyKey: "pty-neutral", Instructions: "Unverified follow-up"})
	if err != nil {
		t.Fatal(err)
	}
	partial := partialReceipt.Run
	if err != nil {
		t.Fatal(err)
	}
	partialWorker := execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: b.ID, RunID: partial.ID, Version: b.Version, ActorID: owner, OrgSlug: org.Slug, TeamSlug: team.Slug})
	partialLease, err := repo.ClaimRun(partialWorker, store.BotClaimRequest{BotID: b.ID, RunID: partial.ID, WorkerID: "pty-neutral", ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	partialWork, err := repo.(store.BotWorkStore).LoadWork(partialWorker, partialLease)
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.CommitRun(partialWorker, store.BotRunCommit{Lease: partialLease, Status: store.RunPartial, Report: store.BotReport{Summary: "PRIVATE_NO_ELIGIBLE_ANSWER", Work: store.BuildBotWorkProjection(partialWork)}}); err != nil {
		t.Fatal(err)
	}
	neutral, err := repo.(store.BotPublicationStore).PublishReport(ctx, store.BotPublicationRequest{BotID: b.ID, RunID: partial.ID})
	if err != nil || neutral == nil || neutral.EmployeeSpeech {
		t.Fatalf("neutral publication: %+v %v", neutral, err)
	}
	savedPartial, err := repo.GetRun(ctx, b.ID, partial.ID)
	if err != nil || savedPartial.TerminalReason != store.BotTerminalNoAnswer {
		t.Fatalf("durable neutral outcome: %+v %v", savedPartial, err)
	}
	transcript, err := personal.Sessions().(store.BotTranscriptReader).ReadBotTranscript(ctx, b.HomeSessionID, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range transcript {
		rows = append(rows, row.Event)
	}
	return ctx, repo, *b, rows, files
}

// This runs the native CLI in real PTYs against committed backend projection.
// Scripted candidate/review evidence establishes presentation/transport behavior;
// it does not execute Employee-worker orchestration or mutate a scheduler job.
func TestEmployeeOnlyConversationPTYAcceptance(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("SUDO_USER", "")
	if err := os.MkdirAll(filepath.Join(home, "astonish"), 0700); err != nil {
		t.Fatal(err)
	}
	projectionCtx, repo, b, committedRows, files := employeePTYCommittedHome(t)
	private := &adksession.Event{ID: uuid.NewString(), Author: "assistant"}
	private.Content = genai.NewContentFromText("PRIVATE_WORKER_TOOL_FALLBACK", "model")
	// An untrusted marker is a negative control, never a positive speech fixture.
	forged := &adksession.Event{ID: uuid.NewString(), Author: "bot_runtime", Actions: adksession.EventActions{StateDelta: map[string]any{"bot_employee_interim": true, "bot_runtime_event": map[string]any{"type": "text", "payload": map[string]any{"text": "PRIVATE_FORGED_EMPLOYEE"}}}}}
	operation := store.BotOperationView{SchemaVersion: 1, BotID: b.ID, HomeSessionID: b.HomeSessionID, OperationID: uuid.New(), Revision: 1, StateSequence: 1, Status: store.BotOperationApplied, Decision: store.BotOperationExecute, Operation: store.BotControlScheduleCreate, Presentation: store.BotOperationPresentationConversation, Summary: "Schedule operation verified.", VerifiedSchedule: &store.BotOperationScheduleView{Enabled: true}}
	opRow := &adksession.Event{ID: uuid.NewString(), Author: "bot_runtime", Actions: adksession.EventActions{StateDelta: map[string]any{store.BotOperationKey: operation}}}
	// The transport supplies the user input; every positive Employee reply above
	// comes from the persisted repository writer and immutable publication reader.
	user := &adksession.Event{ID: uuid.NewString(), Author: "user"}
	user.Content = genai.NewContentFromText("Weekly request", "user")
	rows := append(append([]*adksession.Event{user}, committedRows...), private, forged, opRow)
	if files.Validate() != nil || len(files.Artifacts) != 1 || files.Artifacts[0].RelativePath != "proof.md" {
		t.Fatalf("committed artifact publication: %+v", files)
	}
	base := "/api/studio/bots/" + b.ID.String()
	var stage, subscriptions, previews, sends atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer employee-token" {
			http.Error(w, "auth", 401)
			return
		}
		write := func(v any) { w.Header().Set("Content-Type", "application/json"); _ = json.NewEncoder(w).Encode(v) }
		if r.Method != "GET" {
			sends.Add(1)
			http.Error(w, "PRIVATE_SEND_DIAGNOSTIC", 503)
			return
		}
		switch {
		case r.URL.Path == base:
			write(b)
		case r.URL.Path == base+"/snapshot":
			write(store.BotSnapshot{Bot: b, Operations: []store.BotOperationView{operation}})
		case r.URL.Path == base+"/files":
			write(store.BotFilePage{Entries: []store.BotFileEntry{}})
		case strings.HasSuffix(r.URL.Path, "/preview"):
			previews.Add(1)
			write(client.BotFilePreview{Content: "IMMUTABLE proof preview"})
		case strings.HasSuffix(r.URL.Path, "/events"):
			n := subscriptions.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			projector := api.SessionEventProjector{BotID: b.ID, HomeSessionID: b.HomeSessionID, SessionID: b.HomeSessionID, Repository: repo}
			emit := func(rows []*adksession.Event) {
				for i, row := range rows {
					projection, err := projector.ProjectBot(projectionCtx, store.BotTranscriptEvent{Cursor: int64(i + 1), Event: row}, nil)
					if err != nil {
						t.Errorf("projection event=%s author=%s state=%+v: %v", row.ID, row.Author, row.Actions.StateDelta, err)
						return
					}
					frames, err := projection.BotFrames(int64(i))
					if err != nil {
						t.Errorf("frames: %v", err)
						return
					}
					for _, f := range frames {
						fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", fmt.Sprintf("%s:%d", b.HomeSessionID, f.Cursor), f.Type, f.Data)
					}
				}
				w.(http.Flusher).Flush()
			}
			if stage.Load() > 0 {
				emit(rows)
			}
			fmt.Fprint(w, "event: replay_complete\ndata: {}\n\n")
			w.(http.Flusher).Flush()
			if n == 1 {
				for stage.Load() == 0 {
					select {
					case <-r.Context().Done():
						return
					case <-time.After(20 * time.Millisecond):
					}
				}
				emit(rows)
				return // real SSE reconnect must preserve identity, count and order
			}
			<-r.Context().Done()
		default:
			write(map[string]any{})
		}
	}))
	defer server.Close()
	if err := client.SaveRemoteConfig(&client.RemoteConfig{URL: server.URL}); err != nil {
		t.Fatal(err)
	}
	tokens, err := client.NewTokenStore()
	if err != nil {
		t.Fatal(err)
	}
	if err = tokens.Save(&client.Tokens{AccessToken: "employee-token"}); err != nil {
		t.Fatal(err)
	}
	for reload := 0; reload < 2; reload++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestBotCLIProcess$", "--", "chat", "--bot", b.ID.String())
		cmd.Env = append(os.Environ(), "ASTONISH_BOT_CLI_CHILD=1", "TERM=xterm-256color", "NO_COLOR=1")
		pt, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 48, Cols: 150})
		if err != nil {
			t.Fatal(err)
		}
		var mu sync.Mutex
		var output strings.Builder
		done := make(chan struct{})
		go func() {
			defer close(done)
			var terminal botPTYTerminal
			buf := make([]byte, 8192)
			for {
				n, e := pt.Read(buf)
				if n > 0 {
					_ = terminal.respond(pt, buf[:n])
					mu.Lock()
					output.Write(buf[:n])
					mu.Unlock()
				}
				if e != nil {
					return
				}
			}
		}()
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = pt.Close() })
		read := func() string { mu.Lock(); defer mu.Unlock(); return ansi.Strip(output.String()) }
		wait := func(text string) {
			t.Helper()
			deadline := time.Now().Add(12 * time.Second)
			for time.Now().Before(deadline) {
				if strings.Contains(read(), text) {
					return
				}
				time.Sleep(20 * time.Millisecond)
			}
			t.Fatalf("missing %q: %s", text, read())
		}
		wait("Employee")
		if reload == 0 {
			time.Sleep(150 * time.Millisecond)
			if strings.Contains(read(), "I need the destination") {
				t.Fatal("empty Home invented speech")
			}
			stage.Store(1)
		}
		wait("The schedule is enabled and verified.")
		wait("Partially completed — no Employee answer available")
		wait("/bots files → p: published files")
		if reload == 0 {
			deadline := time.Now().Add(10 * time.Second)
			for subscriptions.Load() < 2 && time.Now().Before(deadline) {
				time.Sleep(20 * time.Millisecond)
			}
			if subscriptions.Load() < 2 {
				t.Fatal("SSE did not reconnect")
			}
		}
		time.Sleep(150 * time.Millisecond)
		mu.Lock()
		start := output.Len()
		mu.Unlock()
		_ = pty.Setsize(pt, &pty.Winsize{Rows: 48, Cols: uint16(151 + reload)})
		time.Sleep(250 * time.Millisecond)
		mu.Lock()
		frame := ansi.Strip(output.String()[start:])
		mu.Unlock()
		if strings.Count(frame, "I need the destination before proceeding.") != 1 || strings.Count(frame, "The schedule is enabled and verified.") != 1 || strings.Index(frame, "Weekly request") > strings.Index(frame, "I need the destination") || strings.Index(frame, "I need the destination") > strings.Index(frame, "The schedule is enabled") {
			t.Fatalf("voice/order/count: %s", frame)
		}
		if strings.Contains(read(), "PRIVATE_") {
			t.Fatal("private worker speech leaked")
		}
		if _, err := io.WriteString(pt, "/bots files\r"); err != nil {
			t.Fatal(err)
		}
		wait("No files.")
		_, _ = io.WriteString(pt, "p")
		time.Sleep(200 * time.Millisecond)
		wait("proof.md")
		_, _ = io.WriteString(pt, "\r")
		wait("IMMUTABLE proof preview")
		_, _ = io.WriteString(pt, "\x1b")
		time.Sleep(100 * time.Millisecond)
		_, _ = io.WriteString(pt, "\x1b")
		time.Sleep(100 * time.Millisecond)
		_, _ = io.WriteString(pt, "test send failure\r")
		deadline := time.Now().Add(8 * time.Second)
		for sends.Load() < int32(reload+1) && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		wait("Latest outcome unavailable.")
		if strings.Contains(read(), "PRIVATE_SEND_DIAGNOSTIC") || strings.Contains(read(), "I can’t confirm") {
			t.Fatal("client failure impersonated Employee")
		}
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = pt.Close()
		<-done
	}
	if previews.Load() < 2 || sends.Load() < 2 {
		t.Fatalf("preview/send paths not exercised: %d/%d", previews.Load(), sends.Load())
	}
}

func TestBotEmployeePTYAcceptance(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("SUDO_USER", "")
	if err := os.MkdirAll(filepath.Join(home, "astonish"), 0700); err != nil {
		t.Fatal(err)
	}
	bot := store.Bot{ID: uuid.New(), HomeSessionID: uuid.New(), Name: "Employee", Scope: store.BotScopePersonal, Status: store.BotActive, Version: 1, Revision: 1}
	op := store.BotOperationView{SchemaVersion: 1, BotID: bot.ID, HomeSessionID: bot.HomeSessionID, OperationID: uuid.New(), Revision: 1, StateSequence: 1, Status: store.BotOperationPending, Decision: store.BotOperationExecute, Summary: "Employee change queued."}
	exception := op
	exception.OperationID = uuid.New()
	exception.Status = store.BotOperationBlocked
	exception.Decision = store.BotOperationRequireAuthorization
	exception.Summary = "Employee exception needs review."
	exception.Exception = &store.BotOperationExceptionView{OperationID: exception.OperationID, PayloadDigest: "exact-digest", ExpiresAt: time.Now().Add(time.Hour)}
	base := "/api/studio/bots/" + bot.ID.String()
	var stage, decisions atomic.Int32
	current := func() []store.BotOperationView {
		a := op
		if stage.Load() > 0 {
			a.Status = store.BotOperationApplied
			a.Revision = 2
			a.StateSequence = 2
			a.Summary = "Employee change verified."
			a.VerifiedSchedule = &store.BotOperationScheduleView{Enabled: true}
		}
		out := []store.BotOperationView{a}
		if stage.Load() > 1 {
			e := exception
			if decisions.Load() > 0 {
				e.Status = store.BotOperationCancelled
				e.Decision = store.BotOperationDeny
				e.Revision = 2
				e.StateSequence = 2
				e.Exception = nil
				e.Summary = "Employee exception denied."
			}
			out = append(out, e)
		}
		return out
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		write := func(v any) { w.Header().Set("Content-Type", "application/json"); _ = json.NewEncoder(w).Encode(v) }
		if r.Header.Get("Authorization") != "Bearer employee-token" {
			http.Error(w, "unauthorized", 401)
			return
		}
		if r.Method == "POST" {
			if r.URL.Path != base+"/operations/"+exception.OperationID.String()+"/decision" {
				t.Errorf("unexpected mutation %s", r.URL)
				http.Error(w, "unexpected", 400)
				return
			}
			var q map[string]any
			_ = json.NewDecoder(r.Body).Decode(&q)
			if len(q) != 4 || q["payload_digest"] != "exact-digest" || q["expected_revision"] != float64(1) || q["approve"] != false {
				t.Errorf("unbound decision %#v", q)
			}
			decisions.Add(1)
			write(current()[1])
			return
		}
		switch r.URL.Path {
		case "/api/studio/sessions":
			write(map[string]any{"sessions": []any{}})
		case "/api/settings/providers/effective":
			write(map[string]any{})
		case "/api/studio/bots":
			write(map[string]any{"bots": []store.Bot{bot}})
		case base:
			write(bot)
		case base + "/snapshot":
			write(store.BotSnapshot{Bot: bot, Operations: current()})
		case base + "/sessions/" + bot.HomeSessionID.String() + "/events":
			w.Header().Set("Content-Type", "text/event-stream")
			seq := 0
			emit := func() {
				for _, v := range current() {
					seq++
					row := adksession.Event{Author: "bot_runtime", Actions: adksession.EventActions{StateDelta: map[string]any{store.BotOperationKey: v}}}
					raw, _ := json.Marshal(row)
					fmt.Fprintf(w, "id: %s:%d\nevent: transcript\ndata: %s\n\n", bot.HomeSessionID, seq, raw)
				}
				w.(http.Flusher).Flush()
			}
			emit()
			fmt.Fprintf(w, "event: replay_complete\ndata: {}\n\n")
			w.(http.Flusher).Flush()
			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-r.Context().Done():
					return
				case <-ticker.C:
					emit()
				}
			}
		default:
			t.Errorf("unexpected endpoint %s", r.URL)
			http.Error(w, "unexpected", 404)
		}
	}))
	defer server.Close()
	if err := client.SaveRemoteConfig(&client.RemoteConfig{URL: server.URL}); err != nil {
		t.Fatal(err)
	}
	tokens, err := client.NewTokenStore()
	if err != nil {
		t.Fatal(err)
	}
	if err = tokens.Save(&client.Tokens{AccessToken: "employee-token"}); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestBotCLIProcess$", "--", "chat")
	cmd.Env = append(os.Environ(), "ASTONISH_BOT_CLI_CHILD=1", "TERM=xterm-256color", "NO_COLOR=1")
	pt, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 45, Cols: 140})
	if err != nil {
		t.Fatal(err)
	}
	defer pt.Close()
	defer cmd.Process.Kill()
	var mu sync.Mutex
	var output strings.Builder
	go func() {
		var terminal botPTYTerminal
		buf := make([]byte, 8192)
		for {
			n, e := pt.Read(buf)
			if n > 0 {
				_ = terminal.respond(pt, buf[:n])
				mu.Lock()
				output.Write(buf[:n])
				mu.Unlock()
			}
			if e != nil {
				return
			}
		}
	}()
	start := 0
	read := func() string { mu.Lock(); defer mu.Unlock(); return ansi.Strip(output.String()[start:]) }
	mark := func() { mu.Lock(); start = output.Len(); mu.Unlock() }
	send := func(s string) {
		mark()
		if _, e := io.WriteString(pt, s); e != nil {
			t.Fatal(e)
		}
	}
	wait := func(s string) {
		t.Helper()
		until := time.Now().Add(10 * time.Second)
		for time.Now().Before(until) {
			if strings.Contains(read(), s) {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("missing %q: %s", s, read())
	}
	frame := func(cols uint16) string {
		time.Sleep(150 * time.Millisecond)
		mark()
		_ = pty.Setsize(pt, &pty.Winsize{Rows: 45, Cols: cols})
		time.Sleep(200 * time.Millisecond)
		return read()
	}
	wait("platform")
	send("/bots\r")
	wait("Employee [active]")
	send("\r")
	wait("Employee change queued.")
	if s := frame(141); strings.Contains(s, "approve once") || strings.Contains(s, "Confirm approved") {
		t.Fatalf("routine review: %s", s)
	}
	stage.Store(1)
	wait("Employee change verified.")
	s := frame(142)
	if strings.Count(s, "Employee change verified.") != 1 || strings.Contains(s, "Employee change queued.") || !strings.Contains(s, "Schedule enabled.") {
		t.Fatalf("receipt mismatch %s", s)
	}
	stage.Store(2)
	wait("Employee exception needs review.")
	send("/bots exceptions\r")
	wait("[Cancel]")
	send("\r")
	time.Sleep(150 * time.Millisecond)
	if decisions.Load() != 0 {
		t.Fatal("default enter mutated")
	}
	send("/bots exceptions\r")
	wait("[Cancel]")
	send("y")
	wait("Confirm approved")
	send("\x1b")
	time.Sleep(200 * time.Millisecond)
	if decisions.Load() != 0 {
		t.Fatal("escape mutated")
	}
	send("/bots exceptions\r")
	wait("[Cancel]")
	send("n")
	wait("Confirm denied")
	send("\r")
	wait("No pending authorization exceptions.")
	send("\x1b")
	time.Sleep(200 * time.Millisecond)
	send("/bots home\r")
	wait("Employee exception denied.")
	s = frame(143)
	if strings.Count(s, "Employee exception denied.") != 1 || strings.Contains(s, "Authorization exception:") {
		t.Fatalf("reload resurrected exception %s", s)
	}
	send("/bots exceptions\r")
	wait("No pending authorization exceptions.")
	send("n\r")
	time.Sleep(150 * time.Millisecond)
	if decisions.Load() != 1 {
		t.Fatalf("repeated decision %d", decisions.Load())
	}
}
