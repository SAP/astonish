package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/apps"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/fleet"
	"github.com/SAP/astonish/pkg/pdfgen"
	"github.com/SAP/astonish/pkg/sandbox"
	persistentsession "github.com/SAP/astonish/pkg/session"
	"github.com/SAP/astonish/pkg/store"
	"github.com/gorilla/mux"
	"google.golang.org/adk/session"
)

// validSessionID accepts all canonical session ID formats:
//   - UUIDs: "e89b12d3-a456-4266-1417-4000"
//   - Channel session keys: "telegram:direct:8484406081", "email:direct:alice@example.com"
//   - Prefixed IDs: "triage-abc12345", "test-abc12345"
//
// Blocks path traversal (no / or \) and shell metacharacters (no space, ;, |, &, $, etc.).
// Must start with an alphanumeric character (prevents leading - or . attacks).
var validSessionID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:@-]{0,127}$`)

// validAppName constrains the ?app= query parameter and StudioChatRequest.AppName.
// App names are used as directory path components in the file store and as query
// parameters in the Ent store, so they must be opaque, short identifiers.
// Allowed: lowercase ASCII letters, digits, hyphens, and underscores, 1-64 chars.
var validAppName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

// resolveSessionStore returns the session store that contains the given session.
// It checks the personal store first (private-first model), then falls back to
// the team store (for fleet sub-sessions and pre-migration data).
// Returns nil if the session is not found in any store.
func resolveSessionStore(svc *store.Services, sessionID string, requests ...*http.Request) store.SessionStore {
	if svc == nil {
		return nil
	}
	allowed := func(meta *store.SessionMeta) bool {
		// The request-less caller is the separately superadmin-gated cache
		// diagnostics endpoint. All ordinary session consumers pass a request.
		return len(requests) == 0 || sessionOwnerAllowed(requests[0], meta.UserID)
	}
	if svc.PersonalSessions != nil {
		if meta, err := svc.PersonalSessions.GetSessionMeta(context.TODO(), sessionID); err == nil && meta != nil && allowed(meta) {
			return svc.PersonalSessions
		}
	}
	if svc.Sessions != nil {
		if meta, err := svc.Sessions.GetSessionMeta(context.TODO(), sessionID); err == nil && meta != nil && allowed(meta) {
			return svc.Sessions
		}
	}
	return nil
}

// sessionOwnerAllowed prevents team members from accessing another user's
// session by guessing its ID. Platform administrators may inspect sessions for
// support and incident response; all other callers must be the recorded owner.
func sessionOwnerAllowed(r *http.Request, ownerID string) bool {
	if ownerID == "" {
		return false
	}
	if pu := GetPlatformUser(r); pu != nil && IsPlatformAdmin(pu) {
		return true
	}
	return ownerID == effectiveUserID(r)
}

func requireSessionOwner(w http.ResponseWriter, r *http.Request, ownerID string) bool {
	if sessionOwnerAllowed(r, ownerID) {
		return true
	}
	respondError(w, http.StatusNotFound, "Session not found")
	return false
}

func requireSessionIDOwner(w http.ResponseWriter, r *http.Request, sessionID string) bool {
	if sessionID == "" {
		respondError(w, http.StatusNotFound, "Session not found")
		return false
	}
	if svc := store.FromRequest(r); svc != nil {
		if ss := resolveSessionStore(svc, sessionID, r); ss != nil {
			meta, err := ss.GetSessionMeta(r.Context(), sessionID)
			if err != nil || meta == nil {
				respondError(w, http.StatusNotFound, "Session not found")
				return false
			}
			return requireSessionOwner(w, r, meta.UserID)
		}
		// Scoped storage misses must not fall through to daemon-local data.
		respondError(w, http.StatusNotFound, "Session not found")
		return false
	}
	if GetPlatformUser(r) != nil {
		respondError(w, http.StatusNotFound, "Session not found")
		return false
	}
	if fs := getFleetFileStore(); fs != nil {
		meta, err := fs.GetSessionMeta(sessionID)
		if err != nil || meta == nil {
			respondError(w, http.StatusNotFound, "Session not found")
			return false
		}
		return requireSessionOwner(w, r, meta.UserID)
	}
	respondError(w, http.StatusNotFound, "Session not found")
	return false
}

func StudioSessionsHandler(w http.ResponseWriter, r *http.Request) {
	userID := effectiveUserID(r)

	// Optional ?app= query parameter to filter sessions by app name.
	// Defaults to studioChatAppName for backward compatibility.
	appName := r.URL.Query().Get("app")
	if appName == "" {
		appName = studioChatAppName
	}
	if !validAppName.MatchString(appName) {
		respondError(w, http.StatusBadRequest, "invalid app name")
		return
	}

	// Platform mode: list from personal session store (private-first).
	// Sessions are always private — they don't change when switching teams.
	if svc := store.FromRequest(r); svc != nil && svc.PersonalSessions != nil {
		metas, err := svc.PersonalSessions.ListSessionMetas(r.Context(), appName, userID)
		if err != nil {
			respondError(w, http.StatusInternalServerError, err.Error())
			return
		}

		sort.Slice(metas, func(i, j int) bool {
			return metas[i].UpdatedAt.After(metas[j].UpdatedAt)
		})
		sessions := make([]StudioSessionResponse, 0, len(metas))
		for _, m := range metas {
			sessions = append(sessions, storeMetaToResponse(m))
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(sessions)
		return
	}

	// Personal mode: use ChatManager's file store.
	cm := GetChatManager()
	if err := cm.ensureReady(r.Context()); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	fs := cm.fileStore()
	if fs == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]StudioSessionResponse{})
		return
	}

	metas, err := fs.ListSessionMetas(appName, userID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Sort by updated_at descending (most recent first)
	sort.Slice(metas, func(i, j int) bool {
		return metas[i].UpdatedAt.After(metas[j].UpdatedAt)
	})

	sessions := make([]StudioSessionResponse, 0, len(metas))
	for _, m := range metas {
		sessions = append(sessions, StudioSessionResponse{
			ID:           m.ID,
			Title:        m.Title,
			CreatedAt:    m.CreatedAt.Format(time.RFC3339),
			UpdatedAt:    m.UpdatedAt.Format(time.RFC3339),
			MessageCount: m.MessageCount,
			FleetKey:     m.FleetKey,
			FleetName:    m.FleetName,
			IssueNumber:  m.IssueNumber,
			Repo:         m.Repo,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(sessions)
}

// StudioSessionHandler handles GET /api/studio/sessions/{id}.
func StudioSessionHandler(w http.ResponseWriter, r *http.Request) {
	sessionID := mux.Vars(r)["id"]
	userID := effectiveUserID(r)

	// Optional ?app= query parameter to filter by app name.
	// Defaults to studioChatAppName for backward compatibility.
	appName := r.URL.Query().Get("app")
	if appName == "" {
		appName = studioChatAppName
	}
	if !validAppName.MatchString(appName) {
		respondError(w, http.StatusBadRequest, "invalid app name")
		return
	}

	// Platform mode: try personal session store first, fall back to team
	// (for fleet sub-sessions or pre-migration data).
	svc := store.FromRequest(r)
	if svc != nil && (svc.PersonalSessions != nil || svc.Sessions != nil) {
		// Resolve which store has this session: personal first, then team.
		sessionStore := resolveSessionStore(svc, sessionID, r)
		if sessionStore == nil {
			respondError(w, http.StatusNotFound, "Session not found")
			return
		}

		meta, err := sessionStore.GetSessionMeta(r.Context(), sessionID)
		if err != nil {
			respondError(w, http.StatusNotFound, fmt.Sprintf("Session not found: %v", err))
			return
		}
		// Defense-in-depth: verify the session belongs to the requested app namespace.
		// GetSessionMeta looks up by globally-unique ID without appName scoping, so
		// we confirm the match here rather than leaking sessions across namespaces.
		if meta.AppName != appName {
			respondError(w, http.StatusNotFound, "Session not found")
			return
		}
		if !requireSessionOwner(w, r, meta.UserID) {
			return
		}

		// Fleet sessions: read transcript events from store
		if meta.FleetKey != "" {
			events, readErr := sessionStore.ReadTranscriptEvents(r.Context(), appName, userID, sessionID)
			var fleetMessages []FleetMessageSummary
			if readErr == nil && len(events) > 0 {
				fleetMessages = fleetEventsToMessages(events)
			}
			resp := StudioSessionDetailResponse{
				StudioSessionResponse: storeMetaToResponse(*meta),
				Messages:              []StudioMessage{},
				FleetMessages:         fleetMessages,
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
			return
		}

		// Regular session: get full session with events
		getResp, err := sessionStore.Get(r.Context(), &session.GetRequest{
			AppName:   appName,
			UserID:    userID,
			SessionID: sessionID,
		})
		if err != nil {
			respondError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to load session: %v", err))
			return
		}

		// Defense-in-depth: hydrate a Redactor from the request's credential
		// stores so that any secrets persisted before the redactor was active
		// are scrubbed before reaching the frontend.
		var redactor *credentials.Redactor
		if svc.PersonalCredentials != nil || svc.Credentials != nil {
			merged := store.NewMergedCredentialStore(svc.PersonalCredentials, svc.Credentials)
			redactor = credentials.NewRedactor()
			redactor.HydrateFromStore(merged)
		}
		messages := eventsToMessages(getResp.Session.Events(), redactor)
		artifacts := collectArtifacts(getResp.Session.Events())
		// Project persisted astonish-report fences onto matching artifacts so
		// the frontend can decide which markdown files to embed inline.
		artifacts = joinReportMarkers(artifacts, collectReportMarkers(getResp.Session.Events()))
		totalUsage := collectUsage(getResp.Session.Events())

		resp := StudioSessionDetailResponse{
			StudioSessionResponse: storeMetaToResponse(*meta),
			Messages:              messages,
			Artifacts:             artifacts,
			TotalUsage:            totalUsage,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
		return
	}

	// Personal mode: use ChatManager's file store.
	cm := GetChatManager()
	if err := cm.ensureReady(r.Context()); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	fs := cm.fileStore()
	if fs == nil {
		respondError(w, http.StatusInternalServerError, "File store not available")
		return
	}

	// Get session metadata
	meta, err := fs.GetSessionMeta(sessionID)
	if err != nil {
		respondError(w, http.StatusNotFound, fmt.Sprintf("Session not found: %v", err))
		return
	}
	// Defense-in-depth: verify the session belongs to the requested app namespace.
	if meta.AppName != appName {
		respondError(w, http.StatusNotFound, "Session not found")
		return
	}
	if !requireSessionOwner(w, r, meta.UserID) {
		return
	}

	// Fleet sessions: read transcript and return fleet-style messages
	if meta.FleetKey != "" {
		transcriptPath := filepath.Join(fs.BaseDir(), appName, userID, sessionID+".jsonl")
		transcript := persistentsession.NewTranscript(transcriptPath)
		events, readErr := transcript.ReadEvents()

		var fleetMessages []FleetMessageSummary
		if readErr == nil && len(events) > 0 {
			fleetMessages = fleetEventsToMessages(events)
		}

		resp := StudioSessionDetailResponse{
			StudioSessionResponse: StudioSessionResponse{
				ID:           meta.ID,
				Title:        meta.Title,
				CreatedAt:    meta.CreatedAt.Format(time.RFC3339),
				UpdatedAt:    meta.UpdatedAt.Format(time.RFC3339),
				MessageCount: meta.MessageCount,
				FleetKey:     meta.FleetKey,
				FleetName:    meta.FleetName,
				IssueNumber:  meta.IssueNumber,
				Repo:         meta.Repo,
			},
			Messages:      []StudioMessage{},
			FleetMessages: fleetMessages,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
		return
	}

	// Get session transcript
	getResp, err := fs.Get(r.Context(), &session.GetRequest{
		AppName:   appName,
		UserID:    userID,
		SessionID: sessionID,
	})
	if err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to load session: %v", err))
		return
	}

	// Transform ADK events into simplified messages
	var redactor *credentials.Redactor
	if cm.components != nil && cm.components.ChatAgent != nil {
		redactor = cm.components.ChatAgent.Redactor
	}
	messages := eventsToMessages(getResp.Session.Events(), redactor)

	// Collect file artifacts from write_file/edit_file tool calls
	artifacts := collectArtifacts(getResp.Session.Events())
	// Project persisted astonish-report fences onto matching artifacts so
	// the frontend can decide which markdown files to embed inline.
	artifacts = joinReportMarkers(artifacts, collectReportMarkers(getResp.Session.Events()))

	// Collect cumulative token usage from all LLM responses
	totalUsage := collectUsage(getResp.Session.Events())

	resp := StudioSessionDetailResponse{
		StudioSessionResponse: StudioSessionResponse{
			ID:           meta.ID,
			Title:        meta.Title,
			CreatedAt:    meta.CreatedAt.Format(time.RFC3339),
			UpdatedAt:    meta.UpdatedAt.Format(time.RFC3339),
			MessageCount: meta.MessageCount,
		},
		Messages:   messages,
		Artifacts:  artifacts,
		TotalUsage: totalUsage,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// StudioSessionTraceHandler handles GET /api/studio/sessions/{id}/trace.
// Returns a merged chronological trace of the session including all
// sub-agent tool calls and text events when recursive=true.
//
// Query params:
//
//	recursive=true   - include child sub-session events
//	tools_only=true  - only include tool_call and tool_result events
//	last_n=50        - only include the last N events
//	verbose=true     - include full args/results (no truncation)
func StudioSessionTraceHandler(w http.ResponseWriter, r *http.Request) {
	sessionID := mux.Vars(r)["id"]

	// Parse query parameters
	opts := persistentsession.TraceOpts{}
	if r.URL.Query().Get("tools_only") == "true" {
		opts.ToolsOnly = true
	}
	if r.URL.Query().Get("verbose") == "true" {
		opts.Verbose = true
	}
	if lastN := r.URL.Query().Get("last_n"); lastN != "" {
		if n, parseErr := strconv.Atoi(lastN); parseErr == nil && n > 0 {
			opts.LastN = n
		}
	}
	recursive := r.URL.Query().Get("recursive") == "true"

	// Platform mode: read from session store (PG).
	if svc := store.FromRequest(r); svc != nil {
		sessionStore := resolveSessionStore(svc, sessionID, r)
		if sessionStore == nil {
			respondError(w, http.StatusNotFound, "Session not found")
			return
		}

		meta, err := sessionStore.GetSessionMeta(r.Context(), sessionID)
		if err != nil || meta == nil {
			respondError(w, http.StatusNotFound, "Session not found")
			return
		}
		if !requireSessionOwner(w, r, meta.UserID) {
			return
		}

		events, err := sessionStore.ReadTranscriptEvents(r.Context(), meta.AppName, meta.UserID, sessionID)
		if err != nil {
			respondError(w, http.StatusInternalServerError, "Failed to read transcript: "+err.Error())
			return
		}

		entries, toolCalls, toolErrors := persistentsession.CollectTraceEntries(events, "", opts)

		if recursive {
			childEntries, childTC, childTE := collectChildEntriesFromStore(sessionStore, sessionID, opts, nil)
			entries = append(entries, childEntries...)
			toolCalls += childTC
			toolErrors += childTE
		}

		sort.Slice(entries, func(i, j int) bool {
			return entries[i].Timestamp < entries[j].Timestamp
		})

		if opts.LastN > 0 && len(entries) > opts.LastN {
			entries = entries[len(entries)-opts.LastN:]
		}

		output := persistentsession.TraceJSON{
			SessionID: meta.ID,
			App:       meta.AppName,
			User:      meta.UserID,
			Events:    entries,
			Summary: persistentsession.TraceSummary{
				TotalEvents: len(events),
				ToolCalls:   toolCalls,
				Errors:      toolErrors,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(output)
		return
	}

	// Personal mode: read from file store.
	fileStore := getFleetFileStore()
	if fileStore == nil {
		respondError(w, http.StatusServiceUnavailable, "Session storage not available")
		return
	}

	meta, err := fileStore.GetSessionMeta(sessionID)
	if err != nil || meta == nil {
		respondError(w, http.StatusNotFound, "Session not found")
		return
	}
	if !requireSessionOwner(w, r, meta.UserID) {
		return
	}

	events, err := fileStore.ReadTranscriptEvents(meta.AppName, meta.UserID, sessionID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to read transcript: "+err.Error())
		return
	}

	entries, toolCalls, toolErrors := persistentsession.CollectTraceEntries(events, "", opts)

	if recursive {
		sessDir := fileStore.BaseDir()
		index := fileStore.Index()
		childEntries, childTC, childTE := persistentsession.CollectChildSessionEntries(sessDir, sessionID, index, opts)
		entries = append(entries, childEntries...)
		toolCalls += childTC
		toolErrors += childTE
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Timestamp < entries[j].Timestamp
	})

	if opts.LastN > 0 && len(entries) > opts.LastN {
		entries = entries[len(entries)-opts.LastN:]
	}

	output := persistentsession.TraceJSON{
		SessionID: meta.ID,
		App:       meta.AppName,
		User:      meta.UserID,
		Events:    entries,
		Summary: persistentsession.TraceSummary{
			TotalEvents: len(events),
			ToolCalls:   toolCalls,
			Errors:      toolErrors,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(output)
}

// StudioSubtaskEventsHandler handles GET /api/studio/sessions/{id}/subtask-events.
// Returns the full tool call/result history for a specific sub-agent task,
// loaded from the child session. Used by the frontend TaskPlanPanel for
// lazy-loading detail when a user expands a completed task after page refresh.
//
// Query params:
//
//	task_name=<name>  - required, the sub-task name to load events for
func StudioSubtaskEventsHandler(w http.ResponseWriter, r *http.Request) {
	sessionID := mux.Vars(r)["id"]
	if !requireSessionIDOwner(w, r, sessionID) {
		return
	}
	taskName := r.URL.Query().Get("task_name")
	childID := r.URL.Query().Get("child_session_id")
	batchID, taskID := r.URL.Query().Get("batch_id"), r.URL.Query().Get("task_id")
	attempt := 0
	if raw := r.URL.Query().Get("attempt"); raw != "" {
		var err error
		attempt, err = strconv.Atoi(raw)
		if err != nil || attempt < 1 {
			respondError(w, http.StatusBadRequest, "invalid attempt")
			return
		}
	}
	if childID == "" && (batchID != "" || taskID != "" || attempt != 0) && (batchID == "" || taskID == "" || attempt == 0) {
		respondError(w, http.StatusBadRequest, "provide batch_id, task_id and attempt together")
		return
	}
	selectedIdentities := make(map[string]map[string]any)
	matchesState := func(svc session.Service, id string) bool {
		got, err := svc.Get(r.Context(), &session.GetRequest{AppName: studioChatAppName, UserID: effectiveUserID(r), SessionID: id})
		if err != nil || got == nil || got.Session == nil {
			return false
		}
		state := got.Session.State()
		// Parent ownership is established by ListChildren(sessionID), whose
		// implementations query persisted parent_id. Do not require the same link
		// to be duplicated in ADK state: platform restores can legitimately omit it.
		// A child ID is already authorized by its parent link and owner. Treat the
		// child's persisted tuple as authoritative because restored cards may retain
		// an older provider call ID in their optional identity hints.
		if childID == "" {
			for key, want := range map[string]string{"batch_id": batchID, "task_id": taskID} {
				if want != "" {
					v, _ := state.Get(key)
					if v != want {
						return false
					}
				}
			}
			if attempt > 0 {
				v, _ := state.Get("attempt")
				if toInt(v) != attempt {
					return false
				}
			}
		}
		identity := map[string]any{}
		for _, key := range []string{"batch_id", "task_id", "attempt"} {
			identity[key], _ = state.Get(key)
		}
		selectedIdentities[id] = identity
		return true
	}
	if childID != "" && !validSessionID.MatchString(childID) {
		respondError(w, http.StatusBadRequest, "invalid child_session_id")
		return
	}
	if taskName == "" && childID == "" && taskID == "" {
		respondError(w, http.StatusBadRequest, "task_name query parameter is required")
		return
	}

	userID := effectiveUserID(r)

	type subtaskEventItem struct {
		BatchID        string `json:"batch_id,omitempty"`
		TaskID         string `json:"task_id,omitempty"`
		Attempt        int    `json:"attempt,omitempty"`
		CallID         string `json:"call_id,omitempty"`
		ChildSessionID string `json:"child_session_id,omitempty"`
		Sequence       int    `json:"sequence,omitempty"`
		Type           string `json:"type"`
		ToolName       string `json:"tool_name,omitempty"`
		ToolArgs       any    `json:"tool_args,omitempty"`
		ToolResult     any    `json:"tool_result,omitempty"`
		Text           string `json:"text,omitempty"`
	}

	type subtaskEventsResponse struct {
		Events []subtaskEventItem `json:"events"`
	}

	// Hydrate a redactor for subtask event scrubbing (platform mode).
	var subtaskRedactor *credentials.Redactor
	if svc := store.FromRequest(r); svc != nil {
		if svc.PersonalCredentials != nil || svc.Credentials != nil {
			merged := store.NewMergedCredentialStore(svc.PersonalCredentials, svc.Credentials)
			subtaskRedactor = credentials.NewRedactor()
			subtaskRedactor.HydrateFromStore(merged)
		}
	}

	// extractChildToolEvents parses ADK session events from a child session
	// and returns subtask event items (tool calls, results, and text).
	extractChildToolEvents := func(events []*session.Event) []subtaskEventItem {
		var items []subtaskEventItem
		uiTools := newUIToolTracker()
		for _, evt := range events {
			if evt.Content == nil {
				continue
			}
			for _, part := range evt.Content.Parts {
				if part.FunctionCall != nil {
					identity := uiTools.call(part.FunctionCall)
					// Skip internal tools that don't add value for display
					name := identity.name
					if name == "search_tools" || name == "announce_plan" || name == "update_plan" {
						continue
					}
					args := identity.args
					if subtaskRedactor != nil && args != nil {
						args = subtaskRedactor.RedactMap(args)
					}
					items = append(items, subtaskEventItem{
						Type: "task_tool_call", CallID: part.FunctionCall.ID,
						ToolName: name,
						ToolArgs: args,
					})
				}
				if part.FunctionResponse != nil {
					name := uiTools.response(part.FunctionResponse)
					if name == "search_tools" || name == "announce_plan" || name == "update_plan" {
						continue
					}
					resp := part.FunctionResponse.Response
					if subtaskRedactor != nil && resp != nil {
						resp = subtaskRedactor.RedactMap(resp)
					}
					items = append(items, subtaskEventItem{
						Type: "task_tool_result", CallID: part.FunctionResponse.ID,
						ToolName:   name,
						ToolResult: summarizeToolResult(resp),
					})
				}
				if part.Text != "" && evt.Content.Role == "model" {
					text := part.Text
					if subtaskRedactor != nil {
						text = subtaskRedactor.Redact(text)
					}
					items = append(items, subtaskEventItem{
						Type: "task_text",
						Text: text,
					})
				}
			}
		}
		for i := range items {
			items[i].ChildSessionID = childID
			items[i].BatchID = strVal(selectedIdentities[childID]["batch_id"])
			items[i].TaskID = strVal(selectedIdentities[childID]["task_id"])
			items[i].Attempt = toInt(selectedIdentities[childID]["attempt"])
			items[i].Sequence = i + 1
		}
		return items
	}

	// Platform mode: resolve session store and load child sessions.
	if svc := store.FromRequest(r); svc != nil {
		sessionStore := resolveSessionStore(svc, sessionID, r)
		if sessionStore == nil {
			respondError(w, http.StatusNotFound, "Session not found")
			return
		}

		children, err := sessionStore.ListChildren(r.Context(), sessionID)
		if err != nil {
			respondError(w, http.StatusInternalServerError, "Failed to list child sessions: "+err.Error())
			return
		}

		// Find the last child session matching the task name (last = successful retry).
		// Child sessions have title set to the task name.
		var matchedChild *store.SessionMeta
		for i := len(children) - 1; i >= 0; i-- {
			if (childID == "" || children[i].ID == childID) && (taskName == "" || children[i].Title == taskName) {
				if !requireSessionOwner(w, r, children[i].UserID) {
					return
				}
				if !matchesState(sessionStore, children[i].ID) {
					continue
				}
				if matchedChild != nil {
					respondError(w, http.StatusConflict, "ambiguous task; provide child_session_id")
					return
				}
				matchedChild = &children[i]
			}
		}

		if matchedChild == nil {
			if childID != "" || taskID != "" {
				respondError(w, http.StatusNotFound, "Subtask not found")
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(subtaskEventsResponse{Events: []subtaskEventItem{}})
			return
		}

		childID = matchedChild.ID
		events, err := sessionStore.ReadTranscriptEvents(r.Context(), studioChatAppName, userID, matchedChild.ID)
		if err != nil {
			slog.Warn("failed to read child session events", "child_id", matchedChild.ID, "error", err)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(subtaskEventsResponse{Events: []subtaskEventItem{}})
			return
		}

		items := extractChildToolEvents(events)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(subtaskEventsResponse{Events: items})
		return
	}

	// Personal mode: use ChatManager's file store.
	cm := GetChatManager()
	if err := cm.ensureReady(r.Context()); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	fs := cm.fileStore()
	if fs == nil {
		respondError(w, http.StatusInternalServerError, "File store not available")
		return
	}

	children, err := fs.ListChildren(sessionID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to list child sessions: "+err.Error())
		return
	}

	// Find the last child session matching the task name.
	var matchedChildID string
	for i := len(children) - 1; i >= 0; i-- {
		if (childID == "" || children[i].ID == childID) && (taskName == "" || children[i].Title == taskName) {
			if !requireSessionOwner(w, r, children[i].UserID) {
				return
			}
			if !matchesState(fs, children[i].ID) {
				continue
			}
			if matchedChildID != "" {
				respondError(w, http.StatusConflict, "ambiguous task; provide child_session_id")
				return
			}
			matchedChildID = children[i].ID
		}
	}

	if matchedChildID == "" {
		if childID != "" || taskID != "" {
			respondError(w, http.StatusNotFound, "Subtask not found")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(subtaskEventsResponse{Events: []subtaskEventItem{}})
		return
	}

	childID = matchedChildID
	transcriptPath := filepath.Join(fs.BaseDir(), studioChatAppName, userID, matchedChildID+".jsonl")
	transcript := persistentsession.NewTranscript(transcriptPath)
	events, err := transcript.ReadEvents()
	if err != nil {
		slog.Warn("failed to read child session transcript", "child_id", matchedChildID, "error", err)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(subtaskEventsResponse{Events: []subtaskEventItem{}})
		return
	}

	items := extractChildToolEvents(events)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(subtaskEventsResponse{Events: items})
}

// StudioDeleteSessionHandler handles DELETE /api/studio/sessions/{id}.
func StudioDeleteSessionHandler(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("panic in StudioDeleteSessionHandler", "recover", rec)
			respondError(w, http.StatusInternalServerError, "internal error during session deletion")
		}
	}()

	sessionID := mux.Vars(r)["id"]
	if !validSessionID.MatchString(sessionID) {
		respondError(w, http.StatusBadRequest, "invalid session ID")
		return
	}
	userID := effectiveUserID(r)

	// Optional ?app= query parameter to filter by app name.
	// Defaults to studioChatAppName for backward compatibility.
	appName := r.URL.Query().Get("app")
	if appName == "" {
		appName = studioChatAppName
	}
	if !validAppName.MatchString(appName) {
		respondError(w, http.StatusBadRequest, "invalid app name")
		return
	}

	// Load app config once for backend-agnostic sandbox cleanup.
	appCfg, _ := config.LoadAppConfig()

	// Platform mode: try personal session store first, fall back to team.
	svc := store.FromRequest(r)
	if svc != nil && (svc.PersonalSessions != nil || svc.Sessions != nil) {
		// Resolve which store has this session: personal first, then team.
		sessionStore := resolveSessionStore(svc, sessionID, r)
		if sessionStore == nil {
			// Session not found in any store — treat as already deleted.
			w.WriteHeader(http.StatusNoContent)
			return
		}

		// Defense-in-depth: verify the session belongs to the requested app
		// namespace BEFORE any destructive action (fleet stop, workspace cleanup).
		// Fail closed: if GetSessionMeta errors, treat as not found rather than
		// skipping the namespace check.
		meta, metaErr := sessionStore.GetSessionMeta(r.Context(), sessionID)
		if metaErr != nil || meta == nil {
			respondError(w, http.StatusNotFound, "Session not found")
			return
		}
		if meta.AppName != appName {
			respondError(w, http.StatusNotFound, "Session not found")
			return
		}
		if !requireSessionOwner(w, r, meta.UserID) {
			return
		}

		// Namespace and ownership verified — now safe to stop any active fleet session.
		registry := getFleetSessionRegistry()
		if fs := registry.Get(sessionID); fs != nil {
			fs.Stop()
			fs.Cleanup()
			registry.Unregister(sessionID)
		}

		if meta.WorkspaceDir != "" {
			if cleanErr := fleet.CleanupSessionWorkspace(meta.WorkspaceDir); cleanErr != nil {
				slog.Warn("could not clean up workspace", "component", "fleet", "workspace", meta.WorkspaceDir, "error", cleanErr)
			}
		}

		err := sessionStore.Delete(r.Context(), &session.DeleteRequest{
			AppName:   appName,
			UserID:    userID,
			SessionID: sessionID,
		})
		if err != nil {
			respondError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to delete session: %v", err))
			return
		}
		sandbox.TryDestroySession(appCfg, sessionID, buildPGSessionRegistry(r.Context()))
		// Clean up session-scoped app databases
		if svc.AppStateSQL != nil {
			prefix := "session_" + apps.Slugify(sessionID) + "_"
			if err := svc.AppStateSQL.DropSchemasWithPrefix(r.Context(), prefix); err != nil {
				slog.Debug("failed to drop session app schemas", "sessionId", sessionID, "error", err)
			}
		}
		// Clean up session-scoped slide decks
		if svc.PersonalDocs != nil {
			if err := svc.PersonalDocs.DeleteDecksBySessionID(r.Context(), sessionID); err != nil {
				slog.Debug("failed to delete session decks", "sessionId", sessionID, "error", err)
			}
		}
		if svc.Docs != nil {
			if err := svc.Docs.DeleteDecksBySessionID(r.Context(), sessionID); err != nil {
				slog.Debug("failed to delete session team decks", "sessionId", sessionID, "error", err)
			}
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Personal mode: use ChatManager's file store.
	cm := GetChatManager()
	if err := cm.ensureReady(r.Context()); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Defense-in-depth: verify the session belongs to the requested app
	// namespace via the file store metadata BEFORE any destructive action.
	// Fail closed: if GetSessionMeta errors, treat as not found rather than
	// skipping the namespace check (which would orphan fleet sandboxes/workspaces).
	if fileStore := getFleetFileStore(); fileStore != nil {
		meta, metaErr := fileStore.GetSessionMeta(sessionID)
		if metaErr != nil || meta == nil {
			respondError(w, http.StatusNotFound, "Session not found")
			return
		}
		if meta.AppName != appName {
			respondError(w, http.StatusNotFound, "Session not found")
			return
		}
		if !requireSessionOwner(w, r, meta.UserID) {
			return
		}
		// Namespace and ownership verified — now safe to stop any active fleet session.
		registry := getFleetSessionRegistry()
		if fs := registry.Get(sessionID); fs != nil {
			fs.Stop()
			fs.Cleanup()
			registry.Unregister(sessionID)
		}
		if meta.WorkspaceDir != "" {
			if cleanErr := fleet.CleanupSessionWorkspace(meta.WorkspaceDir); cleanErr != nil {
				slog.Warn("could not clean up workspace", "component", "fleet", "workspace", meta.WorkspaceDir, "error", cleanErr)
			}
		}
	}

	sessionService := cm.components.SessionService
	err := sessionService.Delete(r.Context(), &session.DeleteRequest{
		AppName:   appName,
		UserID:    userID,
		SessionID: sessionID,
	})
	if err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to delete session: %v", err))
		return
	}

	// Best-effort: destroy sandbox container if one exists for this session
	sandbox.TryDestroySession(appCfg, sessionID, buildPGSessionRegistry(r.Context()))

	// Best-effort: clean up session-scoped app state databases
	if svc := store.FromRequest(r); svc != nil && svc.AppStateSQL != nil {
		prefix := "session_" + apps.Slugify(sessionID) + "_"
		if err := svc.AppStateSQL.DropSchemasWithPrefix(r.Context(), prefix); err != nil {
			slog.Debug("failed to drop session app schemas", "sessionId", sessionID, "error", err)
		}
	}

	// Best-effort: clean up session-scoped slide decks
	if svc := store.FromRequest(r); svc != nil {
		if svc.PersonalDocs != nil {
			if err := svc.PersonalDocs.DeleteDecksBySessionID(r.Context(), sessionID); err != nil {
				slog.Debug("failed to delete session decks", "sessionId", sessionID, "error", err)
			}
		}
		if svc.Docs != nil {
			if err := svc.Docs.DeleteDecksBySessionID(r.Context(), sessionID); err != nil {
				slog.Debug("failed to delete session team decks", "sessionId", sessionID, "error", err)
			}
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

// StudioStopHandler handles POST /api/studio/sessions/{id}/stop.
// Stops both the background chat runner and any active SSE stream.
func StudioStopHandler(w http.ResponseWriter, r *http.Request) {
	sessionID := mux.Vars(r)["id"]
	if !requireSessionIDOwner(w, r, sessionID) {
		return
	}
	cm := GetChatManager()
	cm.cancelStream(sessionID)

	// Also stop the background runner if one exists
	registry := getChatRunnerRegistry()
	registry.Stop(sessionID)

	w.WriteHeader(http.StatusNoContent)
}

// validateArtifactPath sanitizes and validates a user-provided file path for
// artifact access. It ensures the path is absolute after cleaning and rejects
// paths that attempt directory traversal. Returns the cleaned path or an error.
func validateArtifactPath(rawPath string) (string, error) {
	// Reject traversal attempts before cleaning
	if strings.Contains(rawPath, "..") {
		return "", fmt.Errorf("path contains illegal traversal")
	}
	cleaned := filepath.Clean(rawPath)
	// Resolve relative paths against the process working directory
	if !filepath.IsAbs(cleaned) {
		abs, err := filepath.Abs(cleaned)
		if err != nil {
			return "", fmt.Errorf("cannot resolve relative path: %w", err)
		}
		cleaned = abs
	}
	return cleaned, nil
}

// StudioArtifactDownloadHandler serves file artifacts for download.
// Uses the same three-tier fallback strategy as StudioArtifactContentHandler
// so downloads work even when the original file no longer exists on the host.
//
// GET /api/studio/artifacts?path=<absolute_path>&session=<sessionID>
func StudioArtifactDownloadHandler(w http.ResponseWriter, r *http.Request) {
	filePath := r.URL.Query().Get("path")
	if filePath == "" {
		respondError(w, http.StatusBadRequest, "missing 'path' query parameter")
		return
	}

	sessionID := r.URL.Query().Get("session")
	cleanPath, err := validateArtifactPath(filePath)
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid path")
		return
	}
	if (sessionID != "" || store.FromRequest(r) != nil || GetPlatformUser(r) != nil) && !artifactSessionVisible(w, r, sessionID) {
		return
	}
	fileName := filepath.Base(cleanPath)

	// Tier 1: Try serving directly from host filesystem
	if content, ok := readLocalArtifact(r, cleanPath); ok {
		serveArtifactDownload(w, fileName, content)
		return
	}

	// Tier 2: Try reading from sandbox container
	if sessionID != "" {
		if content, ok := readFromSandboxContainer(r, sessionID, cleanPath); ok {
			serveArtifactDownload(w, fileName, content)
			return
		}
	}

	// Tier 3: Fall back to reading from persisted session events
	if sessionID != "" {
		// 3a: File-based session store (personal mode)
		cm := GetChatManager()
		if fs := cm.fileStore(); fs != nil && store.FromRequest(r) == nil && GetPlatformUser(r) == nil {
			if content, ok := readArtifactContentFromSession(fs, effectiveUserID(r), sessionID, cleanPath); ok {
				serveArtifactDownload(w, fileName, []byte(content))
				return
			}
		}

		// 3b: Platform mode — read from PG session store
		if svc := store.FromRequest(r); svc != nil {
			if ss := resolveSessionStore(svc, sessionID, r); ss != nil {
				if content, ok := readArtifactContentFromSessionStore(ss, studioChatAppName, effectiveUserID(r), sessionID, cleanPath); ok {
					serveArtifactDownload(w, fileName, []byte(content))
					return
				}
			}
		}
	}

	respondError(w, http.StatusNotFound, "file not found")
}

// readLocalArtifact preserves trusted local-code access. Remote/platform
// requests may only use their authorized sandbox or persisted session events.
func readLocalArtifact(r *http.Request, path string) ([]byte, bool) {
	if store.FromRequest(r) != nil || GetPlatformUser(r) != nil {
		return nil, false
	}
	content, err := os.ReadFile(path)
	return content, err == nil
}

// serveArtifactDownload keeps active documents as attachments. Only known
// passive image formats and audio/video are eligible for inline media playback.
func serveArtifactDownload(w http.ResponseWriter, fileName string, content []byte) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	// Detect content type from extension
	ct := mime.TypeByExtension(filepath.Ext(fileName))
	if ct == "" {
		ct = http.DetectContentType(content)
	}
	disposition := "attachment"
	mediaType, _, _ := mime.ParseMediaType(ct)
	switch mediaType {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/avif", "image/bmp", "image/x-icon", "image/vnd.microsoft.icon":
		disposition = "inline"
	default:
		if strings.HasPrefix(mediaType, "video/") || strings.HasPrefix(mediaType, "audio/") {
			disposition = "inline"
		}
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", fmt.Sprintf("%s; filename=%q", disposition, fileName))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
	w.Write(content)
}

// StudioArtifactContentHandler returns file content as plain text for the
// in-browser file viewer. Uses a three-tier lookup strategy:
//  1. Read from host filesystem (works for non-sandbox sessions where file still exists)
//  2. Read from the active sandbox backend, resuming stopped sessions before PullFile
//  3. Reconstruct the latest known content from persisted write_file/edit_file events
//
// GET /api/studio/artifacts/content?path=<path>&session=<sessionID>
func StudioArtifactContentHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	filePath := r.URL.Query().Get("path")
	if filePath == "" {
		respondError(w, http.StatusBadRequest, "missing 'path' query parameter")
		return
	}

	sessionID := r.URL.Query().Get("session")
	cleanPath, err := validateArtifactPath(filePath)
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid path")
		return
	}
	if (sessionID != "" || store.FromRequest(r) != nil || GetPlatformUser(r) != nil) && !artifactSessionVisible(w, r, sessionID) {
		return
	}

	// Tier 1: Try reading from host filesystem
	if content, ok := readLocalArtifact(r, cleanPath); ok { // #nosec G304 -- path validated by validateArtifactPath
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write(content)
		return
	}

	// Tier 2: Try reading from sandbox container (if sandbox is enabled and session is provided)
	if sessionID != "" {
		if content, ok := readFromSandboxContainer(r, sessionID, cleanPath); ok {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Write(content)
			return
		}
	}

	// Tier 3: Fall back to reading content from persisted session events
	if sessionID != "" {
		// 3a: File-based session store (personal mode)
		cm := GetChatManager()
		if fs := cm.fileStore(); fs != nil && store.FromRequest(r) == nil && GetPlatformUser(r) == nil {
			if content, ok := readArtifactContentFromSession(fs, effectiveUserID(r), sessionID, cleanPath); ok {
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				fmt.Fprint(w, content)
				return
			}
		}

		// 3b: Platform mode — read from PG session store
		if svc := store.FromRequest(r); svc != nil {
			if ss := resolveSessionStore(svc, sessionID, r); ss != nil {
				if content, ok := readArtifactContentFromSessionStore(ss, studioChatAppName, effectiveUserID(r), sessionID, cleanPath); ok {
					w.Header().Set("Content-Type", "text/plain; charset=utf-8")
					fmt.Fprint(w, content)
					return
				}
			}
		}
	}

	respondError(w, http.StatusNotFound, "file not found")
}

func artifactSessionVisible(w http.ResponseWriter, r *http.Request, sessionID string) bool {
	if !requireSessionIDOwner(w, r, sessionID) {
		return false
	}
	reg := sandboxRegistryFromRequest(r)
	var err error
	if reg == nil {
		reg, err = sandboxSessionRegistryForRequest(r)
		if err != nil {
			respondError(w, http.StatusNotFound, "container not found")
			return false
		}
	}
	if _, ok := lookupSandboxSession(reg, sessionID); !ok {
		// No sandbox row: persisted transcript reconstruction must still verify
		// that the requested session belongs to the caller.
		return requireSessionIDOwner(w, r, sessionID)
	}
	return authorizeSandboxSession(w, r, reg, sessionID) != nil
}

// readFromSandboxContainer reads live files through a backend whose file access
// is confined to the addressed session's injected registry. The ownership and
// tenant checks happen before this function is called.
func readFromSandboxContainer(r *http.Request, sessionID, filePath string) ([]byte, bool) {
	if sessionID == "" || filePath == "" {
		return nil, false
	}

	if backend, cleanup, err := sandboxBackendForAuthorizedRequest(r); err == nil && backend != nil {
		if cleanup != nil {
			defer cleanup()
		}
		if content, ok := pullAuthorizedArtifactFromSandboxBackend(r.Context(), backend, sessionID, filePath); ok {
			return content, true
		}
	} else if err != nil {
		slog.Debug("sandbox backend unavailable for artifact read", "session", sessionID, "path", filePath, "error", err)
	}
	return nil, false
}

func pullAuthorizedArtifactFromSandboxBackend(ctx context.Context, backend sandbox.Backend, sessionID, filePath string) ([]byte, bool) {
	confined, ok := backend.(sandbox.ConfinedArtifactReader)
	if !ok || !confined.ArtifactReadsConfined() {
		return nil, false
	}
	if content, ok := pullArtifactFromSandboxBackend(ctx, backend, sessionID, filePath); ok {
		return content, true
	}
	reader, err := confined.PullArtifact(ctx, sessionID, filePath)
	if err != nil {
		return nil, false
	}
	content, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil {
		return nil, false
	}
	return content, true
}

func pullArtifactFromSandboxBackend(ctx context.Context, backend sandbox.Backend, sessionID, filePath string) ([]byte, bool) {
	if store.FromContext(ctx) != nil || PlatformUserFromContext(ctx) != nil {
		confined, ok := backend.(sandbox.ConfinedArtifactReader)
		if !ok || !confined.ArtifactReadsConfined() {
			return nil, false
		}
	}
	state, stateErr := backend.SessionState(ctx, sessionID)
	if stateErr != nil {
		slog.Debug("failed to query artifact sandbox state", "session", sessionID, "path", filePath, "error", stateErr)
		return nil, false
	}
	if state == sandbox.SessionStateGone {
		return nil, false
	}
	if state == sandbox.SessionStateStopped {
		if err := backend.StartSession(ctx, sessionID); err != nil {
			slog.Debug("failed to resume sandbox for artifact read", "session", sessionID, "path", filePath, "error", err)
			return nil, false
		}
		if err := backend.WaitForSessionReady(ctx, sessionID); err != nil {
			slog.Debug("sandbox did not become ready for artifact read", "session", sessionID, "path", filePath, "error", err)
			return nil, false
		}
	}

	reader, pullErr := backend.PullFile(ctx, sessionID, filePath)
	if pullErr != nil {
		slog.Debug("failed to pull artifact from sandbox backend", "session", sessionID, "path", filePath, "error", pullErr)
		return nil, false
	}
	content, readErr := io.ReadAll(reader)
	// Streaming backends report remote command and extraction failures on
	// Close. Do not publish partial bytes before that result is known.
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil {
		slog.Debug("failed to read artifact content from sandbox backend", "session", sessionID, "path", filePath, "error", readErr, "close_error", closeErr)
		return nil, false
	}
	return content, true
}

// StudioArtifactPDFHandler reads a markdown artifact and returns it as a PDF.
// Uses the same three-tier fallback as StudioArtifactDownloadHandler to locate
// the source markdown, then converts it to PDF using the pdfgen package.
//
// GET /api/studio/artifacts/pdf?path=<path>&session=<sessionID>
func StudioArtifactPDFHandler(w http.ResponseWriter, r *http.Request) {
	filePath := r.URL.Query().Get("path")
	if filePath == "" {
		respondError(w, http.StatusBadRequest, "missing 'path' query parameter")
		return
	}

	sessionID := r.URL.Query().Get("session")
	cleanPath, err := validateArtifactPath(filePath)
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid path")
		return
	}
	if (sessionID != "" || store.FromRequest(r) != nil || GetPlatformUser(r) != nil) && !artifactSessionVisible(w, r, sessionID) {
		return
	}

	// Read the source markdown content using the same 3-tier fallback.
	var mdContent []byte

	// Tier 1: Try reading from host filesystem
	if content, ok := readLocalArtifact(r, cleanPath); ok {
		mdContent = content
	}

	// Tier 2: Try reading from sandbox container
	if mdContent == nil && sessionID != "" {
		if content, ok := readFromSandboxContainer(r, sessionID, cleanPath); ok {
			mdContent = content
		}
	}

	// Tier 3: Fall back to reading from persisted session events
	if mdContent == nil && sessionID != "" {
		// 3a: File-based session store (personal mode)
		cm := GetChatManager()
		if fs := cm.fileStore(); fs != nil && store.FromRequest(r) == nil && GetPlatformUser(r) == nil {
			if content, ok := readArtifactContentFromSession(fs, effectiveUserID(r), sessionID, cleanPath); ok {
				mdContent = []byte(content)
			}
		}

		// 3b: Platform mode — read from PG session store
		if mdContent == nil {
			if svc := store.FromRequest(r); svc != nil {
				if ss := resolveSessionStore(svc, sessionID, r); ss != nil {
					if content, ok := readArtifactContentFromSessionStore(ss, studioChatAppName, effectiveUserID(r), sessionID, cleanPath); ok {
						mdContent = []byte(content)
					}
				}
			}
		}
	}

	if mdContent == nil {
		respondError(w, http.StatusNotFound, "file not found")
		return
	}

	// Convert markdown to PDF using headless Chrome inside the session container.
	slog.Info("PDF: starting Chrome PDF generation", "path", cleanPath, "sessionID", sessionID)

	// Use a timeout so the request doesn't hang indefinitely if Chrome is stuck.
	type pdfResult struct {
		data []byte
		err  error
	}
	ch := make(chan pdfResult, 1)
	go func() {
		slog.Info("PDF: getting browser manager", "sessionID", sessionID)
		browserMgr := GetPDFBrowserManager(sessionID)
		slog.Info("PDF: browser manager obtained, calling ConvertMarkdownToPDFChrome")
		data, err := pdfgen.ConvertMarkdownToPDFChrome(mdContent, browserMgr)
		slog.Info("PDF: ConvertMarkdownToPDFChrome returned", "err", err, "size", len(data))
		ch <- pdfResult{data, err}
	}()

	var pdfData []byte
	select {
	case result := <-ch:
		if result.err != nil {
			slog.Error("PDF generation failed", "path", cleanPath, "error", result.err)
			respondError(w, http.StatusInternalServerError, fmt.Sprintf("PDF generation failed: %v", result.err))
			return
		}
		pdfData = result.data
	case <-time.After(3 * time.Minute):
		slog.Error("PDF generation timed out", "path", cleanPath, "sessionID", sessionID)
		respondError(w, http.StatusGatewayTimeout, "PDF generation timed out")
		return
	}
	slog.Info("PDF generated via Chrome", "path", cleanPath, "size", len(pdfData))

	// Serve as downloadable PDF
	baseName := filepath.Base(cleanPath)
	ext := filepath.Ext(baseName)
	pdfName := baseName[:len(baseName)-len(ext)] + ".pdf"

	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", pdfName))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(pdfData)))
	w.Write(pdfData)
}
