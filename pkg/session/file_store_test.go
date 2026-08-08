package session

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	adkmodel "google.golang.org/adk/model"
	adksession "google.golang.org/adk/session"
	"google.golang.org/genai"
)

func newTestStore(t *testing.T) *FileStore {
	t.Helper()
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}
	return store
}

func createTestSession(t *testing.T, store *FileStore, appName, userID string) adksession.Session {
	t.Helper()
	resp, err := store.Create(context.Background(), &adksession.CreateRequest{
		AppName: appName,
		UserID:  userID,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	return resp.Session
}

func testEvent(id, author, text string) *adksession.Event {
	return &adksession.Event{
		ID:        id,
		Author:    author,
		Timestamp: time.Now(),
		Actions:   adksession.EventActions{},
		LLMResponse: adkmodel.LLMResponse{
			Content: genai.NewContentFromText(text, genai.RoleUser),
		},
	}
}

func TestFileStore_CreateAndGet(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	resp, err := store.Create(ctx, &adksession.CreateRequest{
		AppName: "myapp",
		UserID:  "user1",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	sess := resp.Session
	if sess.ID() == "" {
		t.Error("Session ID is empty")
	}
	if sess.AppName() != "myapp" {
		t.Errorf("AppName = %q, want %q", sess.AppName(), "myapp")
	}
	if sess.UserID() != "user1" {
		t.Errorf("UserID = %q, want %q", sess.UserID(), "user1")
	}

	// Get the session back
	getResp, err := store.Get(ctx, &adksession.GetRequest{
		AppName:   "myapp",
		UserID:    "user1",
		SessionID: sess.ID(),
	})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}

	got := getResp.Session
	if got.ID() != sess.ID() {
		t.Errorf("Get().ID = %q, want %q", got.ID(), sess.ID())
	}
}

func TestFileStore_CreateWithCustomID(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	resp, err := store.Create(ctx, &adksession.CreateRequest{
		AppName:   "myapp",
		UserID:    "user1",
		SessionID: "custom-id-123",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if resp.Session.ID() != "custom-id-123" {
		t.Errorf("ID = %q, want %q", resp.Session.ID(), "custom-id-123")
	}
}

func TestFileStore_CreateRequiresAppAndUser(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	tests := []struct {
		name    string
		appName string
		userID  string
	}{
		{"empty app", "", "user1"},
		{"empty user", "app1", ""},
		{"both empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := store.Create(ctx, &adksession.CreateRequest{
				AppName: tt.appName,
				UserID:  tt.userID,
			})
			if err == nil {
				t.Error("expected error for missing required fields, got nil")
			}
		})
	}
}

func TestFileStore_CreateDuplicate(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	_, err := store.Create(ctx, &adksession.CreateRequest{
		AppName:   "myapp",
		UserID:    "user1",
		SessionID: "dup-id",
	})
	if err != nil {
		t.Fatalf("first Create() error = %v", err)
	}

	_, err = store.Create(ctx, &adksession.CreateRequest{
		AppName:   "myapp",
		UserID:    "user1",
		SessionID: "dup-id",
	})
	if err == nil {
		t.Error("expected error for duplicate session ID, got nil")
	}
}

func TestFileStore_GetNotFound(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	_, err := store.Get(ctx, &adksession.GetRequest{
		AppName:   "myapp",
		UserID:    "user1",
		SessionID: "nonexistent",
	})
	if err == nil {
		t.Error("expected error for nonexistent session, got nil")
	}
}

func TestFileStore_GetWrongAppUser(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	sess := createTestSession(t, store, "app1", "user1")

	_, err := store.Get(ctx, &adksession.GetRequest{
		AppName:   "app2",
		UserID:    "user1",
		SessionID: sess.ID(),
	})
	if err == nil {
		t.Error("expected error for wrong app name, got nil")
	}
}

func TestFileStore_Delete(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	sess := createTestSession(t, store, "myapp", "user1")

	err := store.Delete(ctx, &adksession.DeleteRequest{
		AppName:   "myapp",
		UserID:    "user1",
		SessionID: sess.ID(),
	})
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	_, err = store.Get(ctx, &adksession.GetRequest{
		AppName:   "myapp",
		UserID:    "user1",
		SessionID: sess.ID(),
	})
	if err == nil {
		t.Error("expected error after Delete, got nil")
	}
}

func TestFileStore_List(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	createTestSession(t, store, "app1", "userA")
	createTestSession(t, store, "app1", "userB")
	createTestSession(t, store, "app2", "userA")

	// List all for app1
	resp, err := store.List(ctx, &adksession.ListRequest{AppName: "app1"})
	if err != nil {
		t.Fatalf("List(app1) error = %v", err)
	}
	if len(resp.Sessions) != 2 {
		t.Errorf("List(app1) len = %d, want 2", len(resp.Sessions))
	}

	// List app1, userA
	resp, err = store.List(ctx, &adksession.ListRequest{AppName: "app1", UserID: "userA"})
	if err != nil {
		t.Fatalf("List(app1, userA) error = %v", err)
	}
	if len(resp.Sessions) != 1 {
		t.Errorf("List(app1, userA) len = %d, want 1", len(resp.Sessions))
	}

	// List for app3 (no sessions)
	resp, err = store.List(ctx, &adksession.ListRequest{AppName: "app3"})
	if err != nil {
		t.Fatalf("List(app3) error = %v", err)
	}
	if len(resp.Sessions) != 0 {
		t.Errorf("List(app3) len = %d, want 0", len(resp.Sessions))
	}
}

func TestFileStore_AppendEvent(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	sess := createTestSession(t, store, "myapp", "user1")

	ev := testEvent("ev1", "user", "Hello agent")
	if err := store.AppendEvent(ctx, sess, ev); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}

	// Get session and check event
	getResp, err := store.Get(ctx, &adksession.GetRequest{
		AppName:   "myapp",
		UserID:    "user1",
		SessionID: sess.ID(),
	})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}

	eventCount := getResp.Session.Events().Len()
	if eventCount != 1 {
		t.Errorf("Events().Len() = %d, want 1", eventCount)
	}
}

func TestFileStore_AppendEventPartialSkipped(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	sess := createTestSession(t, store, "myapp", "user1")

	ev := testEvent("ev-partial", "model", "streaming chunk")
	ev.Partial = true

	if err := store.AppendEvent(ctx, sess, ev); err != nil {
		t.Fatalf("AppendEvent(partial) error = %v", err)
	}

	// No event should have been stored
	getResp, err := store.Get(ctx, &adksession.GetRequest{
		AppName:   "myapp",
		UserID:    "user1",
		SessionID: sess.ID(),
	})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}

	eventCount := getResp.Session.Events().Len()
	if eventCount != 0 {
		t.Errorf("Events().Len() = %d, want 0 (partial should be skipped)", eventCount)
	}
}

func TestFileStore_AppendEventNilErrors(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	sess := createTestSession(t, store, "myapp", "user1")

	// Nil event
	if err := store.AppendEvent(ctx, sess, nil); err == nil {
		t.Error("expected error for nil event, got nil")
	}

	// Nil session
	ev := testEvent("ev1", "user", "hello")
	if err := store.AppendEvent(ctx, nil, ev); err == nil {
		t.Error("expected error for nil session, got nil")
	}
}

func TestFileStore_AppendEventWithStateDelta(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	sess := createTestSession(t, store, "myapp", "user1")

	ev := testEvent("ev1", "model", "response")
	ev.Actions.StateDelta = map[string]any{
		"topic": "testing",
	}

	if err := store.AppendEvent(ctx, sess, ev); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}

	// Get and check state
	getResp, err := store.Get(ctx, &adksession.GetRequest{
		AppName:   "myapp",
		UserID:    "user1",
		SessionID: sess.ID(),
	})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}

	val, stateErr := getResp.Session.State().Get("topic")
	if stateErr != nil {
		t.Fatalf("State().Get(topic) error = %v", stateErr)
	}
	if v, ok := val.(string); !ok || v != "testing" {
		t.Errorf("State[topic] = %v, want %q", val, "testing")
	}
}

func TestFileStore_GetWithNumRecentEvents(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	sess := createTestSession(t, store, "myapp", "user1")

	for i := 0; i < 10; i++ {
		ev := testEvent("", "user", "msg")
		ev.Timestamp = time.Now().Add(time.Duration(i) * time.Millisecond)
		if err := store.AppendEvent(ctx, sess, ev); err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
	}

	getResp, err := store.Get(ctx, &adksession.GetRequest{
		AppName:         "myapp",
		UserID:          "user1",
		SessionID:       sess.ID(),
		NumRecentEvents: 3,
	})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}

	eventCount := getResp.Session.Events().Len()
	if eventCount != 3 {
		t.Errorf("Events().Len() = %d, want 3", eventCount)
	}
}

func TestFileStore_ResolveSessionID(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	resp, err := store.Create(ctx, &adksession.CreateRequest{
		AppName:   "myapp",
		UserID:    "user1",
		SessionID: "abc-123-def",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	// Exact match
	resolved, err := store.ResolveSessionID(resp.Session.ID())
	if err != nil {
		t.Fatalf("ResolveSessionID(exact) error = %v", err)
	}
	if resolved != "abc-123-def" {
		t.Errorf("resolved = %q, want %q", resolved, "abc-123-def")
	}

	// Prefix match
	resolved, err = store.ResolveSessionID("abc-123")
	if err != nil {
		t.Fatalf("ResolveSessionID(prefix) error = %v", err)
	}
	if resolved != "abc-123-def" {
		t.Errorf("resolved = %q, want %q", resolved, "abc-123-def")
	}

	// No match
	_, err = store.ResolveSessionID("zzz")
	if err == nil {
		t.Error("expected error for no match, got nil")
	}
}

func TestFileStore_ResolveSessionIDAmbiguous(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	for _, id := range []string{"abc-111", "abc-222"} {
		_, err := store.Create(ctx, &adksession.CreateRequest{
			AppName:   "myapp",
			UserID:    "user1",
			SessionID: id,
		})
		if err != nil {
			t.Fatalf("Create(%s) error = %v", id, err)
		}
	}

	_, err := store.ResolveSessionID("abc")
	if err == nil {
		t.Error("expected error for ambiguous prefix, got nil")
	}
}

func TestFileStore_SetSessionTitle(t *testing.T) {
	store := newTestStore(t)
	sess := createTestSession(t, store, "myapp", "user1")

	if err := store.SetSessionTitle(context.Background(), sess.ID(), "My Title"); err != nil {
		t.Fatalf("SetSessionTitle() error = %v", err)
	}

	meta, err := store.index.Get(sess.ID())
	if err != nil {
		t.Fatalf("index.Get() error = %v", err)
	}
	if meta.Title != "My Title" {
		t.Errorf("Title = %q, want %q", meta.Title, "My Title")
	}
}

func TestFileStore_PersistsToDisk(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	// Create store and session, append events
	store1, err := NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	resp, err := store1.Create(ctx, &adksession.CreateRequest{
		AppName:   "myapp",
		UserID:    "user1",
		SessionID: "persist-test",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	ev := testEvent("ev1", "user", "persisted message")
	if err := store1.AppendEvent(ctx, resp.Session, ev); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}

	// Create a new store pointing to the same directory (simulating restart)
	store2, err := NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	getResp, err := store2.Get(ctx, &adksession.GetRequest{
		AppName:   "myapp",
		UserID:    "user1",
		SessionID: "persist-test",
	})
	if err != nil {
		t.Fatalf("Get() on new store error = %v", err)
	}

	eventCount := getResp.Session.Events().Len()
	if eventCount != 1 {
		t.Errorf("Events().Len() = %d, want 1", eventCount)
	}
}

func TestFileStore_TempStateTrimmed(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	sess := createTestSession(t, store, "myapp", "user1")

	ev := testEvent("ev1", "model", "response")
	ev.Actions.StateDelta = map[string]any{
		"persistent_key":                       "stays",
		adksession.KeyPrefixTemp + "ephemeral": "should be trimmed",
	}

	if err := store.AppendEvent(ctx, sess, ev); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}

	getResp, err := store.Get(ctx, &adksession.GetRequest{
		AppName:   "myapp",
		UserID:    "user1",
		SessionID: sess.ID(),
	})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}

	state := getResp.Session.State()

	// Persistent key should be present
	val, stateErr := state.Get("persistent_key")
	if stateErr != nil {
		t.Errorf("State().Get(persistent_key) error = %v", stateErr)
	}
	if v, ok := val.(string); !ok || v != "stays" {
		t.Errorf("persistent_key = %v, want %q", val, "stays")
	}

	// Temp key should NOT be in state
	_, stateErr = state.Get(adksession.KeyPrefixTemp + "ephemeral")
	if stateErr == nil {
		t.Error("expected error for temp key after trim, got nil")
	}
}

func TestFileStore_CreateWithParentID(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Create parent session
	parentResp, err := store.Create(ctx, &adksession.CreateRequest{
		AppName:   "myapp",
		UserID:    "user1",
		SessionID: "parent-sess",
	})
	if err != nil {
		t.Fatalf("Create(parent) error = %v", err)
	}

	// Create child session using StateKeyParentID
	childResp, err := store.Create(ctx, &adksession.CreateRequest{
		AppName:   "myapp",
		UserID:    "user1",
		SessionID: "child-sess",
		State: map[string]any{
			StateKeyParentID: "parent-sess",
		},
	})
	if err != nil {
		t.Fatalf("Create(child) error = %v", err)
	}

	// Verify parent session exists
	if parentResp.Session.ID() != "parent-sess" {
		t.Errorf("parent ID = %q, want %q", parentResp.Session.ID(), "parent-sess")
	}

	// Verify child session exists
	if childResp.Session.ID() != "child-sess" {
		t.Errorf("child ID = %q, want %q", childResp.Session.ID(), "child-sess")
	}

	// Verify ParentID is set in the index
	meta, err := store.index.Get("child-sess")
	if err != nil {
		t.Fatalf("index.Get(child-sess) error = %v", err)
	}
	if meta.ParentID != "parent-sess" {
		t.Errorf("ParentID = %q, want %q", meta.ParentID, "parent-sess")
	}

	// Verify StateKeyParentID is NOT in the child session's state
	_, stateErr := childResp.Session.State().Get(StateKeyParentID)
	if stateErr == nil {
		t.Error("expected StateKeyParentID to be removed from state, but it's still present")
	}
}

func TestFileStore_ListExcludesSubSessions(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Create parent
	createTestSession(t, store, "myapp", "user1")

	// Create child via StateKeyParentID
	parentSess := createTestSession(t, store, "myapp", "user1")
	_, err := store.Create(ctx, &adksession.CreateRequest{
		AppName:   "myapp",
		UserID:    "user1",
		SessionID: "sub-child",
		State: map[string]any{
			StateKeyParentID: parentSess.ID(),
		},
	})
	if err != nil {
		t.Fatalf("Create(child) error = %v", err)
	}

	// List should exclude sub-sessions
	resp, err := store.List(ctx, &adksession.ListRequest{AppName: "myapp"})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	// Should return 2 top-level sessions, not 3
	if len(resp.Sessions) != 2 {
		t.Errorf("List() len = %d, want 2 (excluding sub-session)", len(resp.Sessions))
	}
}

func TestFileStore_DeleteCascadesChildren(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Create parent
	parentResp, err := store.Create(ctx, &adksession.CreateRequest{
		AppName:   "myapp",
		UserID:    "user1",
		SessionID: "parent-cascade",
	})
	if err != nil {
		t.Fatalf("Create(parent) error = %v", err)
	}

	// Create two children
	for _, childID := range []string{"child-cascade-1", "child-cascade-2"} {
		_, err := store.Create(ctx, &adksession.CreateRequest{
			AppName:   "myapp",
			UserID:    "user1",
			SessionID: childID,
			State: map[string]any{
				StateKeyParentID: "parent-cascade",
			},
		})
		if err != nil {
			t.Fatalf("Create(%s) error = %v", childID, err)
		}
	}

	// Create unrelated session
	_, err = store.Create(ctx, &adksession.CreateRequest{
		AppName:   "myapp",
		UserID:    "user1",
		SessionID: "unrelated-sess",
	})
	if err != nil {
		t.Fatalf("Create(unrelated) error = %v", err)
	}

	// Delete parent — should cascade children
	err = store.Delete(ctx, &adksession.DeleteRequest{
		AppName:   "myapp",
		UserID:    "user1",
		SessionID: parentResp.Session.ID(),
	})
	if err != nil {
		t.Fatalf("Delete(parent) error = %v", err)
	}

	// Parent and children should be gone
	for _, id := range []string{"parent-cascade", "child-cascade-1", "child-cascade-2"} {
		_, err := store.Get(ctx, &adksession.GetRequest{
			AppName:   "myapp",
			UserID:    "user1",
			SessionID: id,
		})
		if err == nil {
			t.Errorf("Get(%s) should return error after cascade delete, got nil", id)
		}
	}

	// Unrelated session should still exist
	_, err = store.Get(ctx, &adksession.GetRequest{
		AppName:   "myapp",
		UserID:    "user1",
		SessionID: "unrelated-sess",
	})
	if err != nil {
		t.Errorf("Get(unrelated) error = %v, want nil (should survive cascade)", err)
	}
}

func TestFileStore_TruncateEvents(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	sess := createTestSession(t, store, "myapp", "user1")

	for i, text := range []string{"msg0", "msg1", "msg2", "msg3"} {
		ev := testEvent("ev"+string(rune('0'+i)), "user", text)
		if err := store.AppendEvent(ctx, sess, ev); err != nil {
			t.Fatalf("AppendEvent(%d) error = %v", i, err)
		}
	}

	// Keep the first 2 events, discard the rest.
	kept, err := store.TruncateEvents("myapp", "user1", sess.ID(), 2)
	if err != nil {
		t.Fatalf("TruncateEvents() error = %v", err)
	}
	if kept != 2 {
		t.Errorf("TruncateEvents returned %d, want 2", kept)
	}

	getResp, err := store.Get(ctx, &adksession.GetRequest{
		AppName:   "myapp",
		UserID:    "user1",
		SessionID: sess.ID(),
	})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got := getResp.Session.Events().Len(); got != 2 {
		t.Errorf("Events().Len() after truncate = %d, want 2", got)
	}

	// The persisted transcript should reflect the truncation after reload.
	fresh, err := NewFileStore(store.BaseDir())
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}
	reloaded, err := fresh.Get(ctx, &adksession.GetRequest{
		AppName:   "myapp",
		UserID:    "user1",
		SessionID: sess.ID(),
	})
	if err != nil {
		t.Fatalf("Get(reloaded) error = %v", err)
	}
	if got := reloaded.Session.Events().Len(); got != 2 {
		t.Errorf("reloaded Events().Len() = %d, want 2 (transcript rewrite)", got)
	}

	// Truncating to the full (already shorter) length is a no-op.
	kept, err = store.TruncateEvents("myapp", "user1", sess.ID(), 5)
	if err != nil {
		t.Fatalf("TruncateEvents(over) error = %v", err)
	}
	if kept != 2 {
		t.Errorf("TruncateEvents(over) returned %d, want 2 (clamped)", kept)
	}

	// Truncating to 0 clears all events.
	if _, err := store.TruncateEvents("myapp", "user1", sess.ID(), 0); err != nil {
		t.Fatalf("TruncateEvents(0) error = %v", err)
	}
	getResp, _ = store.Get(ctx, &adksession.GetRequest{
		AppName:   "myapp",
		UserID:    "user1",
		SessionID: sess.ID(),
	})
	if got := getResp.Session.Events().Len(); got != 0 {
		t.Errorf("Events().Len() after truncate(0) = %d, want 0", got)
	}
}

func TestFileStore_LatestDescendantAndAncestorChain(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// root -> child -> tip
	root, err := store.Create(ctx, &adksession.CreateRequest{
		AppName: "app", UserID: "u", SessionID: "root",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Touch UpdatedAt order: create child then tip with slight delay via sequential creates.
	_, err = store.Create(ctx, &adksession.CreateRequest{
		AppName: "app", UserID: "u", SessionID: "child",
		State: map[string]any{StateKeyParentID: root.Session.ID()},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Create(ctx, &adksession.CreateRequest{
		AppName: "app", UserID: "u", SessionID: "tip",
		State: map[string]any{StateKeyParentID: "child"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if got := store.LatestDescendant("root"); got != "tip" {
		t.Fatalf("LatestDescendant(root)=%q want tip", got)
	}
	if got := store.LatestDescendant("child"); got != "tip" {
		t.Fatalf("LatestDescendant(child)=%q want tip", got)
	}
	if got := store.LatestDescendant("tip"); got != "tip" {
		t.Fatalf("LatestDescendant(tip)=%q want tip", got)
	}
	if got := store.LatestDescendant("missing"); got != "missing" {
		t.Fatalf("LatestDescendant(missing)=%q want missing", got)
	}

	chain := store.AncestorChain("tip")
	if len(chain) != 3 || chain[0] != "root" || chain[1] != "child" || chain[2] != "tip" {
		t.Fatalf("AncestorChain(tip)=%v want [root child tip]", chain)
	}
	chain = store.AncestorChain("root")
	if len(chain) != 1 || chain[0] != "root" {
		t.Fatalf("AncestorChain(root)=%v want [root]", chain)
	}
}

func TestFileStore_ArchiveAndReplaceEvents(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	resp, err := store.Create(ctx, &adksession.CreateRequest{AppName: "app", UserID: "u", SessionID: "active"})
	if err != nil {
		t.Fatal(err)
	}
	sess := resp.Session
	// Seed several events via the live session pointer (as ADK would).
	for i := 0; i < 10; i++ {
		ev := &adksession.Event{
			ID:     fmt.Sprintf("e%d", i),
			Author: "user",
			LLMResponse: adkmodel.LLMResponse{
				Content: genai.NewContentFromText(fmt.Sprintf("msg %d %s", i, strings.Repeat("x", 50)), genai.RoleUser),
			},
		}
		if err := store.AppendEvent(ctx, sess, ev); err != nil {
			t.Fatal(err)
		}
	}
	if sess.Events().Len() != 10 {
		t.Fatalf("live events = %d want 10", sess.Events().Len())
	}

	compacted := []*adksession.Event{
		{
			ID:     "c0",
			Author: "model",
			LLMResponse: adkmodel.LLMResponse{
				Content: genai.NewContentFromText("[Context Summary — 10 earlier messages compacted]\n\nSUMMARY", genai.RoleModel),
			},
		},
		{
			ID:     "c1",
			Author: "user",
			LLMResponse: adkmodel.LLMResponse{
				Content: genai.NewContentFromText("recent", genai.RoleUser),
			},
		},
	}
	archiveID, err := store.ArchiveAndReplaceEvents("app", "u", "active", compacted)
	if err != nil {
		t.Fatalf("ArchiveAndReplaceEvents: %v", err)
	}
	if archiveID == "" || archiveID == "active" {
		t.Fatalf("bad archive id %q", archiveID)
	}

	// Live ADK session pointer must see the rewrite immediately.
	if sess.Events().Len() != 2 {
		t.Fatalf("live session events after rewrite = %d want 2", sess.Events().Len())
	}

	// Archive has the full history.
	arch, err := store.Get(ctx, &adksession.GetRequest{AppName: "app", UserID: "u", SessionID: archiveID})
	if err != nil {
		t.Fatal(err)
	}
	if arch.Session.Events().Len() != 10 {
		t.Fatalf("archive events = %d want 10", arch.Session.Events().Len())
	}

	// Active is a child of the archive.
	meta, err := store.GetSessionMeta("active")
	if err != nil {
		t.Fatal(err)
	}
	if meta.ParentID != archiveID {
		t.Fatalf("active ParentID = %q want %q", meta.ParentID, archiveID)
	}
	if store.LatestDescendant(archiveID) != "active" {
		t.Fatalf("LatestDescendant(archive)=%q want active", store.LatestDescendant(archiveID))
	}
}
