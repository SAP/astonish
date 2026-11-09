package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestEmployeeCreatedPeerIdentity(t *testing.T) {
	original := store.Bot{ID: uuid.New(), OwnerID: uuid.New(), Scope: store.BotScopePersonal, HomeSessionID: uuid.New()}
	oldPeer := original
	oldPeer.ID = uuid.New()
	peer := original
	peer.ID = uuid.New()
	peer.HomeSessionID = uuid.New()
	peer.Name = employeePeerTitle
	second := peer
	second.ID = uuid.New()
	second.HomeSessionID = uuid.New()
	before := map[uuid.UUID]bool{original.ID: true, oldPeer.ID: true}
	for _, test := range []struct {
		name    string
		bots    []store.Bot
		wantErr bool
	}{
		{"one-new-with-existing-peers", []store.Bot{original, oldPeer, peer}, false},
		{"no-new-peer", []store.Bot{original, oldPeer}, true},
		{"duplicate-peer", []store.Bot{original, peer, peer}, true},
		{"multiple-new-peers", []store.Bot{original, peer, second}, true},
		{"old-peer-is-not-new", []store.Bot{original, oldPeer}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"bots": test.bots})
			_, err := employeeCreatedPeer(string(raw), peer.ID.String(), original, before)
			if (err != nil) != test.wantErr {
				t.Fatal(err)
			}
		})
	}
	for _, mutate := range []func(*store.Bot){
		func(b *store.Bot) { b.ID = original.ID }, func(b *store.Bot) { b.OwnerID = uuid.New() }, func(b *store.Bot) { b.Scope = store.BotScopeTeam }, func(b *store.Bot) { b.HomeSessionID = original.HomeSessionID }, func(b *store.Bot) { b.Name = "foreign" },
	} {
		b := peer
		mutate(&b)
		raw, _ := json.Marshal(map[string]any{"bots": []store.Bot{original, b}})
		if _, err := employeeCreatedPeer(string(raw), peer.ID.String(), original, before); err == nil {
			t.Fatalf("accepted %+v", b)
		}
	}
}

func TestEmployeePeerTitleSourceOnce(t *testing.T) {
	peer := store.Bot{ID: uuid.New()}
	run := store.BotRun{ID: uuid.New(), BotID: peer.ID, Trigger: store.TriggerManual, Presentation: store.BotRunPresentationHomeInteractive, UserEventID: "title-event", HistoryCursor: 1}
	raw := admissionHistory(run.UserEventID, "user", employeePeerTitle)
	got, err := employeePeerTitleRun(raw, peer, []store.BotRun{run})
	if err != nil || got.ID != run.ID {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := employeePeerTitleRun(raw, peer, []store.BotRun{run, run}); err == nil {
		t.Fatal("duplicate title accepted")
	}
	if _, err := employeePeerTitleRun(admissionHistory(run.UserEventID, "user", "foreign"), peer, []store.BotRun{run}); err == nil {
		t.Fatal("wrong source accepted")
	}
	run.BotID = uuid.New()
	if _, err := employeePeerTitleRun(raw, peer, []store.BotRun{run}); err == nil {
		t.Fatal("foreign run accepted")
	}
}

func TestEmployeePeerCreateWiringSingleAttempt(t *testing.T) {
	raw, err := os.ReadFile("newemployee.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	start := strings.Index(source, `stage("employee-delete-preserves-peer")`)
	end := strings.Index(source, `peerPath :=`)
	if start < 0 || end < start {
		t.Fatal("peer stage missing")
	}
	stage := source[start:end]
	attempt := strings.Index(stage, `actions["peer-create"] = true`)
	checkpoint := strings.Index(stage[attempt:], "checkpoint()") + attempt
	click := strings.Index(stage, `"^Start conversation$").MustClick()`)
	wait := strings.Index(stage, "employeeWaitPeerHome")
	if attempt < 0 || checkpoint < attempt || click < checkpoint || wait < click || strings.Count(stage, `"^Start conversation$").MustClick()`) != 1 {
		t.Fatal("must persist attempt before exactly one create click, then wait")
	}
	for _, required := range []string{`if actions["peer-create"]`, "employeeCreatedPeer", `peerCapture("peer-title-history", historyPath, employeePeerSSE).titleRun(peerBot, rr.Runs)`, `boundedWait("preservation peer terminal"`} {
		if !strings.Contains(stage, required) {
			t.Fatal(required)
		}
	}
	for _, preserved := range []string{`"^Delete$").MustClick()`, `boundedWait("permanent deletion durable cleanup"`, `peerJSON("peer-"+label+"-after", path).Body != peerBefore[label]`, `string(peerVolumeAfter) != string(peerVolumeBefore)`} {
		if !strings.Contains(source, preserved) {
			t.Fatal("lost deletion assertion: " + preserved)
		}
	}
}
