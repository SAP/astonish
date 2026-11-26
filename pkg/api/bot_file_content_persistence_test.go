package api

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	adksession "google.golang.org/adk/session"
	"google.golang.org/genai"
)

// This is a real loopback HTTP + disposable SQLite acceptance test, not physical
// Docker acceptance. Readiness is controlled; no model or daemon is started.
// Every asserted content category is written and hydrated before SQL is scanned.
func TestBotFileContentPersistenceHTTP(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f := newSkillStorageHTTPFixture(t)
			markers := map[string]string{}
			for _, category := range []string{"charter", "persona", "goal", "criteria", "instructions", "attachment", "transcript", "tool_input", "tool_output", "state", "checkpoint", "report"} {
				markers[category] = "HTTP-DISK-" + category + "-\"quoted\"-\n-雪-PRIVATE"
			}
			encode := func(v any) string {
				t.Helper()
				raw, err := json.Marshal(v)
				if err != nil {
					t.Fatal(err)
				}
				return string(raw)
			}
			b := skillDecode[store.Bot](t, f.request("POST", "/api/studio/bots?scope="+string(scope), f.owner, encode(map[string]any{"name": "Disk-backed definition", "idempotency_key": "file-content-http", "definition": map[string]any{"charter": markers["charter"], "persona": map[string]any{"soul": markers["persona"], "timezone": "UTC"}}}), 201))
			ctx := f.context(f.owner)
			repo := f.repo(scope)
			goal := skillDecode[store.BotGoal](t, f.request("POST", skillPath(&b, "/goals"), f.owner, encode(map[string]any{"title": "Private goal", "description": markers["goal"], "completion_criteria": markers["criteria"]}), 201))
			service, err := execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindService, ClientID: "http-content-registration", Authentication: execution.AuthMethod("internal"), Surface: execution.SurfaceCode, OrgSlug: f.org.Slug, TeamSlug: f.team.Slug, Authenticated: true, Scopes: []string{entstore.BotHomeRegistrationScope}})
			if err != nil {
				t.Fatal(err)
			}
			if err := entstore.RegisterBotHomeBinding(service, repo, entstore.BotHomeRegistration{BotID: b.ID, ExpectedRevision: b.Revision, Home: store.BotHomeBinding{ID: uuid.New(), BotID: b.ID, Scope: scope, Backend: "controlled", StorageKey: uuid.NewString(), Revision: 1}}, func(context.Context, entstore.BotHomeRegistration) error { return nil }); err != nil {
				t.Fatal(err)
			}
			current, err := repo.Get(ctx, b.ID)
			if err != nil {
				t.Fatal(err)
			}
			active := current
			b = *active
			run := skillDecode[store.BotRun](t, f.request("POST", skillPath(&b, "/runs"), f.owner, encode(map[string]any{"version": b.Version, "idempotency_key": "content-run", "instructions": markers["instructions"], "attachments": []map[string]any{{"filename": "input.txt", "mimeType": "text/plain", "data": base64.StdEncoding.EncodeToString([]byte(markers["attachment"]))}}}), 202))
			worker := execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: b.ID, RunID: run.ID, Version: b.Version, ActorID: f.owner, OrgSlug: f.org.Slug, TeamSlug: f.team.Slug})
			lease, err := repo.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: run.ID, WorkerID: "http-content", ExpiresAt: time.Now().Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			sessions := botContentHTTPSessions(t, f, scope)
			session, err := sessions.Get(ctx, &adksession.GetRequest{SessionID: run.SessionID.String(), UserID: f.owner})
			if err != nil {
				t.Fatal(err)
			}
			event := &adksession.Event{
				ID: uuid.NewString(), Author: "assistant", Timestamp: time.Now().UTC(),
				LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{
					{Text: markers["transcript"]},
					{FunctionCall: &genai.FunctionCall{Name: "read_file", Args: map[string]any{"path": markers["tool_input"]}}},
					{FunctionResponse: &genai.FunctionResponse{Name: "read_file", Response: map[string]any{"content": markers["tool_output"]}}},
				}}},
				Actions: adksession.EventActions{StateDelta: map[string]any{"private_state": markers["state"]}},
			}
			if err := sessions.AppendEvent(ctx, session.Session, event); err != nil {
				t.Fatal(err)
			}
			checkpoint := json.RawMessage(encode(map[string]any{"private": markers["checkpoint"]}))
			work, err := repo.(store.BotWorkStore).LoadWork(worker, lease)
			if err != nil {
				t.Fatal(err)
			}
			if err := repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunFailed, Checkpoint: checkpoint, Report: store.BotReport{Summary: markers["report"], Work: store.BuildBotWorkProjection(work)}}); err != nil {
				t.Fatal(err)
			}
			queued := skillDecode[store.BotRun](t, f.request("POST", skillPath(&b, "/runs"), f.owner, encode(map[string]any{"version": b.Version, "idempotency_key": "queued-content-reopen", "instructions": markers["instructions"], "attachments": []ChatAttachment{{Filename: "input.txt", MimeType: "text/plain", Data: base64.StdEncoding.EncodeToString([]byte(markers["attachment"]))}}}), 202))
			read := func() []string {
				t.Helper()
				repo := f.repo(scope)
				input, err := repo.(store.BotRuntimeStore).LoadRunInput(ctx, b.ID, queued.ID)
				if err != nil {
					t.Fatal(err)
				}
				if input.Version.Charter != markers["charter"] || input.Version.Persona.Soul != markers["persona"] || input.Instructions != markers["instructions"] || len(input.Attachments) != 1 || string(input.Attachments[0].Data) != markers["attachment"] {
					t.Fatal("definition/run input did not hydrate all written bodies")
				}
				if len(input.Goals) != 1 || input.Goals[0].ID != goal.ID || input.Goals[0].Description != markers["goal"] || input.Goals[0].CompletionCriteria != markers["criteria"] {
					t.Fatal("goal body did not hydrate")
				}
				got := skillDecode[store.BotRun](t, f.request("GET", skillPath(&b, "/runs/"+run.ID.String()), f.owner, "", 200))
				if got.Report.Summary != markers["report"] || string(got.Checkpoint) != string(checkpoint) {
					t.Fatalf("run body did not hydrate: %+v", got)
				}
				session, err := botContentHTTPSessions(t, f, scope).Get(ctx, &adksession.GetRequest{SessionID: run.SessionID.String(), UserID: f.owner})
				if err != nil {
					t.Fatal(err)
				}
				state, err := session.Session.State().Get("private_state")
				if err != nil || state != markers["state"] {
					t.Fatalf("state hydration: %v %v", state, err)
				}
				frames := botContentHTTPReplay(t, f, skillPath(&b, "/sessions/"+run.SessionID.String()+"/events"), "")
				found := map[string]bool{}
				for _, frame := range frames {
					var got map[string]any
					if err := json.Unmarshal([]byte(frame.data), &got); err != nil {
						t.Fatal(err)
					}
					projection, _ := got["_projection"].(map[string]any)
					identity, _ := projection["event_id"].(string)
					if !strings.HasPrefix(identity, run.SessionID.String()+"/"+event.ID+"/") {
						continue
					}
					for _, private := range []string{"private_state", "bot_record_v2", "runtime_scope", "inline", markers["state"], markers["checkpoint"]} {
						encoded, _ := json.Marshal(private)
						if strings.Contains(frame.data, string(encoded[1:len(encoded)-1])) {
							t.Fatalf("HTTP public projection leaked private database field %q", private)
						}
					}
					found[frame.event] = true
					switch frame.event {
					case "text":
						if got["text"] != markers["transcript"] {
							t.Fatal("lost transcript text")
						}
					case "tool_call":
						args, _ := got["args"].(map[string]any)
						if got["name"] != "read_file" || args["path"] != markers["tool_input"] {
							t.Fatal("lost tool input")
						}
					case "tool_result":
						result, _ := json.Marshal(got["result"])
						marker, _ := json.Marshal(markers["tool_output"])
						if got["name"] != "read_file" || !strings.Contains(string(result), string(marker[1:len(marker)-1])) {
							t.Fatal("lost tool output")
						}
					}
				}
				if !found["text"] || !found["tool_call"] || !found["tool_result"] {
					t.Fatal("HTTP replay never returned all written public event parts")
				}
				cursor := frames[len(frames)-1].id
				delta := botContentHTTPReplay(t, f, skillPath(&b, "/sessions/"+run.SessionID.String()+"/events"), cursor)
				if len(delta) != 1 || delta[0].event != botReplayComplete || delta[0].id != cursor {
					t.Fatal("HTTP cursor replay duplicated events")
				}
				ids := make([]string, len(frames))
				for i, frame := range frames {
					ids[i] = frame.id + "/" + frame.event + "/" + frame.data
				}
				return ids
			}
			before := read()
			botContentHTTPAssertSQL(t, f, scope, markers)
			f.reopen()
			after := read()
			if !reflect.DeepEqual(before, after) {
				t.Fatal("close/reopen changed HTTP event bytes, identities or cursors")
			}
			botContentHTTPAssertSQL(t, f, scope, markers)
			other := store.BotScopePersonal
			if scope == store.BotScopePersonal {
				other = store.BotScopeTeam
			}
			f.request("GET", "/api/studio/bots/"+b.ID.String()+"?scope="+string(other), f.owner, "", 404)
			f.request("GET", skillPath(&b, "/sessions/"+run.SessionID.String()+"/events"), "", "", 401)
			t.Log("HTTP create, goal/run admission, event replay+cursor, close/reopen and nonempty SQL scan passed; physical Home backend not exercised")
		})
	}
}

func botContentHTTPSessions(t *testing.T, f *skillStorageHTTPFixture, scope store.BotScope) store.SessionStore {
	t.Helper()
	org, err := f.db.ForOrg(f.org.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if scope == store.BotScopePersonal {
		return org.ForUser(f.owner).Sessions()
	}
	return org.ForTeam(f.team.Slug).Sessions()
}

type botContentHTTPFrame struct{ id, event, data string }

func botContentHTTPReplay(t *testing.T, f *skillStorageHTTPFixture, path, after string) []botContentHTTPFrame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if after != "" {
		path += "&after=" + url.QueryEscape(after)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Test-Actor", f.owner)
	response, err := f.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("event stream status %d", response.StatusCode)
	}
	scan := bufio.NewScanner(response.Body)
	scan.Buffer(make([]byte, 4096), 2<<20)
	var result []botContentHTTPFrame
	var frame botContentHTTPFrame
	for scan.Scan() {
		line := scan.Text()
		if line == "" {
			result = append(result, frame)
			if frame.event == botReplayComplete {
				return result
			}
			frame = botContentHTTPFrame{}
			continue
		}
		if value, ok := strings.CutPrefix(line, "id: "); ok {
			frame.id = value
		}
		if value, ok := strings.CutPrefix(line, "event: "); ok {
			frame.event = value
		}
		if value, ok := strings.CutPrefix(line, "data: "); ok {
			frame.data = value
		}
	}
	t.Fatalf("stream ended without replay completion: %v", scan.Err())
	return nil
}

// Scan every column of every table in this test-owned scope (including any
// payload/index tables), while requiring populated rows for each exercised path.
// Recursively inspect JSON strings and base64, so nested envelopes cannot hide
// escaped or encoded bodies. The database is opened read-only, never via Ent.
func botContentHTTPAssertSQL(t *testing.T, f *skillStorageHTTPFixture, scope store.BotScope, markers map[string]string) {
	t.Helper()
	path := filepath.Join(f.cfg.DataDir, "orgs", f.org.Slug, "teams", f.team.Slug+".db")
	if scope == store.BotScopePersonal {
		path = filepath.Join(f.cfg.DataDir, "orgs", f.org.Slug, "personal", f.owner+".db")
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.QueryContext(t.Context(), "SELECT name FROM sqlite_master WHERE type='table'")
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, table)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	counts := map[string]int{}
	// Only these typed runtime envelopes may contain database-inline bodies.
	// Workspace definition/goal registries remain file-based; immutable run
	// snapshots legitimately include those bodies inside bot_runs.data.
	runtimeColumns := map[string]bool{"bot_runs.data": true, "bot_outbox.payload": true, "session_events.event_data": true, "sessions.metadata": true}
	runtimeMarkers := map[string]bool{"instructions": true, "attachment": true, "transcript": true, "tool_input": true, "tool_output": true, "state": true, "checkpoint": true, "report": true}
	found := map[string]bool{}
	var inspect func(string, string, int)
	inspect = func(where, value string, depth int) {
		if depth > 12 {
			t.Fatalf("SQL encoding nesting exceeds inspection bound at %s", where)
		}
		for category, marker := range markers {
			escaped, _ := json.Marshal(marker)
			for _, needle := range []string{marker, string(escaped[1 : len(escaped)-1]), base64.StdEncoding.EncodeToString([]byte(marker))} {
				if strings.Contains(value, needle) {
					if !runtimeColumns[where] || (!runtimeMarkers[category] && where != "bot_runs.data") {
						t.Fatalf("%s content leaked outside database runtime in SQL %s", category, where)
					}
					found[category] = true
				}
			}
		}
		var nested any
		if json.Unmarshal([]byte(value), &nested) == nil {
			var walk func(any)
			walk = func(v any) {
				switch x := v.(type) {
				case string:
					if x != value {
						inspect(where, x, depth+1)
					}
				case []any:
					for _, item := range x {
						walk(item)
					}
				case map[string]any:
					for key, item := range x {
						inspect(where, key, depth+1)
						walk(item)
					}
				}
			}
			walk(nested)
		}
		if decoded, err := base64.StdEncoding.DecodeString(value); err == nil && len(decoded) > 0 && string(decoded) != value {
			inspect(where, string(decoded), depth+1)
		}
	}
	for _, table := range tables {
		// The table allowlist comes exclusively from sqlite_master above in this
		// disposable, read-only database. Double quotes are escaped as identifiers;
		// keep every schema-listed table in the scan, including payload/index tables.
		rows, err := db.QueryContext(t.Context(), `SELECT * FROM "`+strings.ReplaceAll(table, `"`, `""`)+`"`) // #nosec G202 -- schema-listed table identifier, quoted and escaped; no request input.
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			counts[table]++
			values := make([]any, len(columns))
			ptrs := make([]any, len(columns))
			for i := range values {
				ptrs[i] = &values[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			for i, value := range values {
				var text string
				switch x := value.(type) {
				case []byte:
					text = string(x)
				case string:
					text = x
				default:
					continue
				}
				inspect(table+"."+columns[i], text, 0)
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}
	for _, table := range []string{"bots", "bot_versions", "bot_goals", "bot_runs", "sessions", "session_events"} {
		if counts[table] == 0 {
			t.Fatalf("SQL absence claim invalid: %s has no exercised rows", table)
		}
	}
	for category := range runtimeMarkers {
		if !found[category] {
			t.Fatalf("database-inline runtime contract not exercised: %s missing", category)
		}
	}
	t.Logf("%s raw SQL nonempty-row evidence: %s", scope, fmt.Sprint(counts))
}
