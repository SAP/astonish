package bot

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotAgentWorkDoesNotAuthorizeResume(t *testing.T) {
	w := store.NewBotAgentWorkState(store.BotWorkContract{BotID: uuid.New(), RunID: uuid.New(), Version: 1, Instructions: "Research"})
	if err := w.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, status := range []store.RunStatus{store.RunInterrupted, store.RunWaitingApproval, store.RunCancelled} {
		if ValidateRunTransition(status, store.RunQueued, RunTransitionGuard{}) == nil {
			t.Fatal("agent work bypassed continuation authority")
		}
	}
	// Optional service contracts must not add methods to the execution Runtime.
	if reflect.TypeOf((*Runtime)(nil)).Elem().NumMethod() != 3 {
		t.Fatal("execution interface expanded")
	}
	if reflect.TypeOf((*BotWorkServices)(nil)).Elem().NumMethod() != 6 {
		t.Fatal("work capability contract changed")
	}
}

func TestBotLifecycleTransitions(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		from  store.BotStatus
		event LifecycleEvent
		want  store.BotStatus
		allow bool
	}{
		{store.BotDraft, LifecycleActivate, store.BotActive, true},
		{store.BotActive, LifecyclePause, store.BotPaused, true},
		{store.BotActive, LifecycleStop, store.BotStopped, true},
		{store.BotPaused, LifecycleResume, store.BotActive, true},
		{store.BotStopped, LifecycleResume, store.BotActive, true},
		{store.BotDeleted, LifecycleRestore, store.BotPaused, true},
		{store.BotDraft, LifecycleResume, "", false},
		{store.BotPaused, LifecycleActivate, "", false},
		{store.BotDeleted, LifecycleResume, "", false},
		{store.BotActive, LifecycleRestore, "", false},
	}
	for _, tc := range cases {
		t.Run(string(tc.from)+"/"+string(tc.event), func(t *testing.T) {
			got, err := NextBotStatus(tc.from, tc.event, now, now.Add(time.Hour))
			if (err == nil) != tc.allow || tc.allow && got != tc.want {
				t.Fatalf("got %q %v", got, err)
			}
		})
	}
	for _, s := range []store.BotStatus{store.BotDraft, store.BotActive, store.BotPaused, store.BotStopped} {
		if got, err := NextBotStatus(s, LifecycleDelete, now, time.Time{}); err != nil || got != store.BotDeleted {
			t.Fatalf("delete %s: %s %v", s, got, err)
		}
	}
	if _, err := NextBotStatus(store.BotDeleted, LifecycleRestore, now, now); !errors.Is(err, store.ErrBotExpired) {
		t.Fatalf("retention expiry: %v", err)
	}
	if _, err := NextBotStatus("unknown", LifecycleDelete, now, now); err == nil {
		t.Fatal("unknown state accepted")
	}
}

func TestBotRunTransitions(t *testing.T) {
	states := []store.RunStatus{store.RunQueued, store.RunRunning, store.RunWaitingApproval, store.RunInterrupted, store.RunSucceeded, store.RunPartial, store.RunFailed, store.RunSkipped, store.RunCancelled}
	allowed := map[store.RunStatus][]store.RunStatus{
		store.RunQueued:          {store.RunRunning, store.RunSkipped, store.RunCancelled},
		store.RunRunning:         {store.RunWaitingApproval, store.RunInterrupted, store.RunSucceeded, store.RunPartial, store.RunFailed, store.RunSkipped, store.RunCancelled},
		store.RunWaitingApproval: {store.RunQueued, store.RunPartial, store.RunCancelled},
		store.RunInterrupted:     {store.RunQueued, store.RunPartial, store.RunCancelled},
	}
	for _, from := range states {
		for _, to := range states {
			want := false
			for _, s := range allowed[from] {
				if s == to {
					want = true
				}
			}
			if got := ValidateRunTransition(from, to, RunTransitionGuard{BotActive: true, Authorized: true, CheckpointSafe: true}); (got == nil) != want {
				t.Errorf("%s -> %s: %v", from, to, got)
			}
		}
	}
	for _, g := range []RunTransitionGuard{{}, {BotActive: true}, {BotActive: true, Authorized: true}} {
		if err := ValidateRunTransition(store.RunWaitingApproval, store.RunQueued, g); err == nil {
			t.Fatal("unapproved/unsafe resume")
		}
	}
}

func TestBotTodosAndEvidence(t *testing.T) {
	todo := store.BotTodo{ID: uuid.New(), Outcome: "Verify result", Status: store.BotTodoInProgress}
	if err := store.ValidateBotTodos([]store.BotTodo{todo}, true); err != nil {
		t.Fatal(err)
	}
	if err := store.ValidateBotTodos(nil, true); err == nil {
		t.Fatal("working without active item")
	}
	duplicate := todo
	duplicate.ID = uuid.New()
	if err := store.ValidateBotTodos([]store.BotTodo{todo, duplicate}, true); err == nil {
		t.Fatal("two active items")
	}
	todo.Status = store.BotTodoCompleted
	if err := store.ValidateBotTodos([]store.BotTodo{todo}, false); err == nil {
		t.Fatal("completed without evidence")
	}
	todo.Evidence = []store.BotEvidence{{Kind: store.BotEvidenceOwnerConfirmation, Summary: "Owner confirmed receipt", Source: "message:123", ObservedAt: time.Now(), ActorID: uuid.New()}}
	if err := store.ValidateBotTodos([]store.BotTodo{todo}, false); err != nil {
		t.Fatal(err)
	}
	todo.Status = store.BotTodoBlocked
	if err := store.ValidateBotTodos([]store.BotTodo{todo}, false); err == nil {
		t.Fatal("blocked without condition")
	}
}

func TestBotApprovalTransitions(t *testing.T) {
	now := time.Now().UTC()
	expires := now.Add(time.Minute)
	states := []store.ApprovalStatus{store.ApprovalPending, store.ApprovalApproved, store.ApprovalDenied, store.ApprovalExpired, store.ApprovalRevoked, store.ApprovalConsumed}
	for _, from := range states {
		for _, to := range states {
			want := from == store.ApprovalPending && (to == store.ApprovalApproved || to == store.ApprovalDenied || to == store.ApprovalRevoked) || from == store.ApprovalApproved && (to == store.ApprovalConsumed || to == store.ApprovalRevoked)
			if err := ValidateApprovalTransition(from, to, now, expires); (err == nil) != want {
				t.Errorf("%s -> %s: %v", from, to, err)
			}
		}
	}
	if err := ValidateApprovalTransition(store.ApprovalApproved, store.ApprovalConsumed, expires, expires); err == nil {
		t.Fatal("expired approval consumed")
	}
	if err := ValidateApprovalTransition(store.ApprovalPending, store.ApprovalExpired, expires, expires); err != nil {
		t.Fatal(err)
	}
}

func TestBotContractSerialization(t *testing.T) {
	v := store.DefaultBotVersion()
	v.BotID = uuid.New()
	if v.Autonomy.Level != execution.AutonomySupervised || v.Autonomy.MaxSpendPerRunMinor != 0 || len(v.Triggers) != 0 {
		t.Fatal("unsafe defaults")
	}
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var restored store.BotVersion
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(v, restored) {
		t.Fatal("version changed on restore")
	}
	if err := ValidateVersion(restored); err == nil {
		t.Fatal("empty charter accepted")
	}
	restored.Charter = "Observe fares; do not book."
	if err := ValidateVersion(restored); err != nil {
		t.Fatal(err)
	}
	restored.SchemaVersion++
	if err := ValidateVersion(restored); !errors.Is(err, store.ErrBotUnsupported) {
		t.Fatalf("unknown schema accepted: %v", err)
	}
}
