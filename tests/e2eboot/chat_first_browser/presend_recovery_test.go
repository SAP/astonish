package main

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestEmployeePreSendRecovery(t *testing.T) {
	const home = "http://candidate/#/chat/bots/personal/owned"
	const message = "revise the document"
	ready := employeeComposerState{URL: home, Present: true, Connection: employeeConnectionLive}
	for _, tc := range []struct {
		name                   string
		initial                employeeComposerState
		afterFillAlert         bool
		recoverAfterFill       bool
		wantError              bool
		wantFills, wantReopens int
	}{
		{name: "ready", initial: ready, wantFills: 1},
		{name: "pre-fill-transient-reopens", initial: employeeComposerState{URL: home, Present: true, Connection: employeeConnectionReconnecting, Alert: employeeUnavailableAlert}, wantFills: 1, wantReopens: 1},
		{name: "post-fill-transient-recovers-without-navigation", initial: ready, afterFillAlert: true, recoverAfterFill: true, wantFills: 1},
		{name: "post-fill-timeout-preserves-draft", initial: ready, afterFillAlert: true, wantError: true, wantFills: 1},
		{name: "existing-draft-is-not-cleared", initial: employeeComposerState{URL: home, Present: true, Input: "unsent document", Connection: employeeConnectionReconnecting, Alert: employeeUnavailableAlert}, wantError: true},
		{name: "wrong-home-is-not-navigated", initial: employeeComposerState{URL: home + "other", Present: true}, wantError: true},
		{name: "access-alert-is-not-retried", initial: employeeComposerState{URL: home, Present: true, Alert: "Access denied"}, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := tc.initial
			now := time.Unix(0, 0)
			fills, reopens, waits, observations := 0, 0, 0, 0
			hooks := employeePreSendHooks{
				read: func() employeeComposerState { return state },
				fill: func(text string) {
					fills++
					state.Input = text
					if tc.afterFillAlert {
						state.Alert = employeeUnavailableAlert
						state.Connection = employeeConnectionReconnecting
					}
				},
				reopen: func() {
					reopens++
					if state.Input != "" {
						t.Fatal("draft lost by navigation")
					}
					state = ready
				},
				now: func() time.Time { return now },
				wait: func() {
					waits++
					now = now.Add(time.Second)
					if tc.recoverAfterFill && fills > 0 && waits >= 2 {
						state.Alert = ""
						state.Connection = employeeConnectionLive
					}
				},
				observe: func(employeePreSendObservation) { observations++ },
			}
			_, err := employeePrepareSend(home, message, 8*time.Second, hooks)
			if (err != nil) != tc.wantError || fills != tc.wantFills || reopens != tc.wantReopens {
				t.Fatalf("err=%v fills=%d reopens=%d", err, fills, reopens)
			}
			if observations == 0 {
				t.Fatal("missing diagnostics")
			}
			if tc.name == "post-fill-timeout-preserves-draft" && state.Input != message {
				t.Fatal("draft lost")
			}
		})
	}
}

func TestEmployeePreSendTimeoutAndNoActionReplay(t *testing.T) {
	const home = "owned"
	state := employeeComposerState{URL: home, Present: true, Connection: employeeConnectionLoading}
	now := time.Unix(0, 0)
	fills, reopens, clicks, attempted := 0, 0, 0, 0
	observations := []employeePreSendObservation{}
	hooks := employeePreSendHooks{read: func() employeeComposerState { return state }, fill: func(text string) { fills++; state.Input = text }, reopen: func() { reopens++ }, now: func() time.Time { return now }, wait: func() { now = now.Add(time.Second) }, observe: func(o employeePreSendObservation) { observations = append(observations, o) }}
	err := employeeSubmitOnce(home, "task", 8*time.Second, hooks, func() {}, func() { attempted++ }, func() error { clicks++; return errors.New("uncertain click") })
	if err == nil || !strings.Contains(err.Error(), "timeout") || fills != 0 || reopens > 1 || attempted != 0 || clicks != 0 || len(observations) < 2 {
		t.Fatalf("err=%v fills=%d reopens=%d attempted=%d clicks=%d trace=%v", err, fills, reopens, attempted, clicks, observations)
	}
	state.Connection = employeeConnectionLive
	err = employeeSubmitOnce(home, "task", 8*time.Second, hooks, func() {}, func() { attempted++ }, func() error { clicks++; return errors.New("uncertain click") })
	if err == nil || attempted != 1 || clicks != 1 || fills != 1 {
		t.Fatalf("uncertain action replayed: err=%v attempted=%d clicks=%d fills=%d", err, attempted, clicks, fills)
	}
}

func TestEmployeePreSendNeverRetypesLostDraft(t *testing.T) {
	now := time.Unix(0, 0)
	state := employeeComposerState{URL: "owned", Present: true, Connection: employeeConnectionLive}
	fills, reopens, attempted, clicks := 0, 0, 0, 0
	hooks := employeePreSendHooks{
		read:   func() employeeComposerState { return state },
		fill:   func(string) { fills++ /* Simulate disappearing/unmounted draft after input. */ },
		reopen: func() { reopens++ }, now: func() time.Time { return now },
		wait: func() { now = now.Add(time.Second) }, observe: func(employeePreSendObservation) {},
	}
	err := employeeSubmitOnce("owned", "task", time.Minute, hooks, func() {}, func() { attempted++ }, func() error { clicks++; return nil })
	if err == nil || fills != 1 || reopens != 0 || attempted != 0 || clicks != 0 {
		t.Fatalf("err=%v fills=%d reopens=%d attempted=%d clicks=%d", err, fills, reopens, attempted, clicks)
	}
}

func TestAgentGeneralDependentBlocked(t *testing.T) {
	var dependency agentGeneralSendDependency
	dependency.begin("document-seed")
	if blocker := dependency.blocker(employeeComposerState{}); !strings.Contains(blocker, "document-seed") {
		t.Fatal(blocker)
	}
	dependency.settled()
	if blocker := dependency.blocker(employeeComposerState{Input: "unsent revision"}); blocker == "" {
		t.Fatal("comparison allowed to overwrite draft")
	}
	if blocker := dependency.blocker(employeeComposerState{}); blocker != "" {
		t.Fatal(blocker)
	}
}
