package agent

import (
	"context"
	"sync"
	"testing"
)

func TestConversationStateRetainsDistillAcrossAttempts(t *testing.T) {
	owner := &ChatAgent{}
	first := &ChatAgent{}
	first.ShareConversationState(owner)
	trace := &ExecutionTrace{UserRequest: "retained task"}
	first.conversationState().traceMu.Lock()
	first.traceHistory["session"] = []*ExecutionTrace{trace}
	first.pendingDistill["session"] = &distillPreview{Description: "preview", Traces: []*ExecutionTrace{trace}}
	first.pendingDistillReview["session"] = &DistillReview{YAML: "review", Traces: []*ExecutionTrace{trace}, LastDryRunOutput: "retained output"}
	first.traceMu.Unlock()
	second := &ChatAgent{}
	second.ShareConversationState(owner)
	if !second.HasPendingDistill("session") || second.GetPendingDistillReview("session").LastDryRunOutput != "retained output" || second.HasPendingDistill("other") {
		t.Fatal("distillation continuity/isolation lost")
	}
	second.CancelPendingDistill("session")
	if owner.HasPendingDistill("session") {
		t.Fatal("preview cancellation lost")
	}
	owner.ForgetConversationState("session")
	if second.HasPendingDistillReview("session") || len(second.traceHistory["session"]) != 0 {
		t.Fatal("deleted conversation retained")
	}
}

func TestConversationStateDistillUsesCurrentAttemptRunner(t *testing.T) {
	owner, first, second := &ChatAgent{}, &ChatAgent{}, &ChatAgent{}
	first.ShareConversationState(owner)
	first.conversationState().traceMu.Lock()
	first.pendingDistillReview["session"] = &DistillReview{YAML: "retained flow"}
	first.traceMu.Unlock()
	first.FlowRunner = func(context.Context, string, map[string]string) (*DryRunExecResult, error) {
		t.Fatal("stale execution callback reused")
		return nil, nil
	}
	second.ShareConversationState(owner)
	second.FlowRunner = func(ctx context.Context, yaml string, params map[string]string) (*DryRunExecResult, error) {
		if yaml != "retained flow" {
			t.Fatalf("lost review: %q", yaml)
		}
		return &DryRunExecResult{Success: true, Output: "current attempt"}, nil
	}
	result, err := second.DryRunDistilledFlow(t.Context(), "session")
	if err != nil || result == nil || !result.Success || owner.GetPendingDistillReview("session").LastDryRunOutput != "current attempt" {
		t.Fatalf("distill continuation: %+v %v", result, err)
	}
	owner.ForgetConversationState("session")
	if _, err := second.DryRunDistilledFlow(t.Context(), "session"); err == nil {
		t.Fatal("deleted review executed")
	}
}

func TestConversationStateDeletionFencesReview(t *testing.T) {
	c := &ChatAgent{}
	c.conversationState().pendingDistill["s"] = &distillPreview{Traces: []*ExecutionTrace{{UserRequest: "task"}}}
	started, release := make(chan struct{}), make(chan struct{})
	c.FlowDistiller = &FlowDistiller{LLM: func(context.Context, string) (string, error) {
		close(started)
		<-release
		return "```yaml\nname: task\nnodes: []\n```", nil
	}}
	done := make(chan error, 1)
	go func() {
		_, err := c.DistillToReview(context.Background(), DistillSession{SessionID: "s"}, func(string) {})
		done <- err
	}()
	<-started
	c.ForgetConversationState("s")
	close(release)
	if err := <-done; err == nil {
		t.Error("deleted in-flight review reported success")
	}
	if c.HasPendingDistillReview("s") {
		t.Fatal("deleted review republished")
	}
}

func TestConversationStateDeletionFencesMutation(t *testing.T) {
	for _, operation := range []string{"modify", "dry-run"} {
		t.Run(operation, func(t *testing.T) {
			c := &ChatAgent{}
			c.conversationState().pendingDistillReview["s"] = &DistillReview{YAML: "name: task\nnodes: []"}
			started, release := make(chan struct{}), make(chan struct{})
			c.FlowDistiller = &FlowDistiller{LLM: func(context.Context, string) (string, error) {
				close(started)
				<-release
				return "```yaml\nname: changed\nnodes: []\n```", nil
			}}
			c.FlowRunner = func(context.Context, string, map[string]string) (*DryRunExecResult, error) {
				close(started)
				<-release
				return &DryRunExecResult{Success: true, Output: "late"}, nil
			}
			done := make(chan error, 1)
			go func() {
				var err error
				if operation == "modify" {
					_, err = c.ModifyDistillReview(context.Background(), "s", "change")
				} else {
					_, err = c.DryRunDistilledFlow(context.Background(), "s")
				}
				done <- err
			}()
			<-started
			c.ForgetConversationState("s")
			close(release)
			if err := <-done; err == nil {
				t.Fatal("deleted mutation reported success")
			}
			if c.HasPendingDistillReview("s") {
				t.Fatal("deleted mutation republished")
			}
		})
	}
}

func TestConversationStateTraceDeletionFence(t *testing.T) {
	owner, attempt := &ChatAgent{}, &ChatAgent{}
	attempt.ShareConversationState(owner)
	for range 25 {
		attempt.recordConversationTrace("s", &ExecutionTrace{UserRequest: "task"})
	}
	if len(owner.traceHistory["s"]) != 20 {
		t.Fatal("trace retention bound lost")
	}
	owner.ForgetConversationState("s")
	attempt.recordConversationTrace("s", &ExecutionTrace{UserRequest: "late"})
	if len(owner.traceHistory["s"]) != 0 {
		t.Fatal("late trace republished after deletion")
	}
}

func TestConversationStateResetFencesReview(t *testing.T) {
	owner, attempt := &ChatAgent{}, &ChatAgent{}
	attempt.ShareConversationState(owner)
	attempt.conversationState().pendingDistill["s"] = &distillPreview{Traces: []*ExecutionTrace{{UserRequest: "task"}}}
	started, release := make(chan struct{}), make(chan struct{})
	attempt.FlowDistiller = &FlowDistiller{LLM: func(context.Context, string) (string, error) {
		close(started)
		<-release
		return "```yaml\nname: task\nnodes: []\n```", nil
	}}
	done := make(chan error, 1)
	go func() {
		_, err := attempt.DistillToReview(context.Background(), DistillSession{SessionID: "s"}, func(string) {})
		done <- err
	}()
	<-started
	owner.CloseConversationState()
	close(release)
	if err := <-done; err == nil {
		t.Fatal("reset in-flight review reported success")
	}
	attempt.recordConversationTrace("previously-unseen", &ExecutionTrace{UserRequest: "late"})
	if owner.HasPendingDistillReview("s") || len(owner.traceHistory) != 0 {
		t.Fatal("reset state accepted late mutation")
	}
}

func TestConversationStateTutorialDeletionFence(t *testing.T) {
	for _, reset := range []bool{false, true} {
		t.Run(map[bool]string{false: "delete", true: "reset"}[reset], func(t *testing.T) {
			owner, attempt := &ChatAgent{}, &ChatAgent{}
			attempt.ShareConversationState(owner)
			attempt.SetPendingTutorialBlueprint("session", &TutorialBlueprintPending{Title: "before"})
			attempt.MarkTutorialBlueprintApproved("session")
			if reset {
				owner.CloseConversationState()
			} else {
				owner.ForgetConversationState("session")
			}
			attempt.SetPendingTutorialBlueprint("session", &TutorialBlueprintPending{Title: "late"})
			attempt.MarkTutorialBlueprintApproved("session")
			if owner.HasPendingTutorialBlueprint("session") || owner.HasTutorialBlueprintApproved("session") {
				t.Fatal("deleted tutorial state republished")
			}
			if reset {
				attempt.SetPendingTutorialBlueprint("unseen", &TutorialBlueprintPending{Title: "late"})
				attempt.MarkTutorialBlueprintApproved("unseen")
				if owner.HasPendingTutorialBlueprint("unseen") || owner.HasTutorialBlueprintApproved("unseen") {
					t.Fatal("closed state accepted unseen tutorial")
				}
			}
		})
	}
}

func TestConversationStateZeroValueConcurrentAccess(t *testing.T) {
	c := &ChatAgent{}
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			c.HasPendingDistill("session")
			c.HasPendingDistillReview("session")
			c.SetPendingTutorialBlueprint("session", &TutorialBlueprintPending{YAML: "draft"})
			c.HasTutorialBlueprintApproved("session")
		})
	}
	wg.Wait()
}
