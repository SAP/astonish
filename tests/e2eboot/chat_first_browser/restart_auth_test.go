package main

import (
	"errors"
	"reflect"
	"testing"
)

func TestRestartAuthenticatesInSameRunAndPreservesResume(t *testing.T) {
	for _, resumed := range []bool{false, true} {
		cp := smokeCheckpoint{Restarted: resumed, Receipts: map[string]smokeReceipt{"prior": {Detail: "unchanged"}}}
		var events []string
		err := restartAndAuthenticate(&cp, func() error { events = append(events, "restart"); return nil }, func() { events = append(events, "persist") }, func() error { events = append(events, "authenticate"); return nil })
		want := []string{"restart", "persist", "authenticate"}
		if resumed {
			want = []string{"authenticate"}
		}
		if err != nil || !reflect.DeepEqual(events, want) || !cp.Restarted || cp.Receipts["prior"].Detail != "unchanged" {
			t.Fatalf("events=%v cp=%+v err=%v", events, cp, err)
		}
	}
}

func TestRestartAuthenticationFailureDoesNotReplayRestart(t *testing.T) {
	cp := smokeCheckpoint{}
	restarts := 0
	restart := func() error { restarts++; return nil }
	if err := restartAndAuthenticate(&cp, restart, func() {}, func() error { return errors.New("denied") }); err == nil {
		t.Fatal("authentication failure ignored")
	}
	if err := restartAndAuthenticate(&cp, restart, func() {}, func() error { return nil }); err != nil || restarts != 1 {
		t.Fatalf("restarts=%d err=%v", restarts, err)
	}
}

func TestAuthenticationRedactsCredentials(t *testing.T) {
	err := redactedAuthentication(func() { panic("email=private@example.com password=secret") })
	if err == nil || err.Error() != "browser authentication failed" {
		t.Fatalf("unsafe diagnostic: %v", err)
	}
	if err := redactedAuthentication(func() {}); err != nil {
		t.Fatal(err)
	}
}

func TestRestartFailureDoesNotAuthenticate(t *testing.T) {
	cp := smokeCheckpoint{}
	err := restartAndAuthenticate(&cp, func() error { return errors.New("restart failed") }, func() { t.Fatal("persisted failed restart") }, func() error { t.Fatal("authenticated after failed restart"); return nil })
	if err == nil || cp.Restarted {
		t.Fatal("restart failure ignored")
	}
}
