package main

import (
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestEmployeeHomeVolumeRejectsMalformedIdentity(t *testing.T) {
	good := store.BotHomeBinding{ID: uuid.New(), BotID: uuid.New(), Scope: store.BotScopePersonal, Backend: "docker", StorageKey: "astonish-home-proof", Revision: 1}
	for name, mutate := range map[string]func(*store.BotHomeBinding){
		"missing-id":       func(b *store.BotHomeBinding) { b.ID = uuid.Nil },
		"wrong-bot":        func(b *store.BotHomeBinding) { b.BotID = uuid.New() },
		"nil-bot":          func(b *store.BotHomeBinding) { b.BotID = uuid.Nil },
		"wrong-scope":      func(b *store.BotHomeBinding) { b.Scope = store.BotScopeTeam },
		"wrong-backend":    func(b *store.BotHomeBinding) { b.Backend = "kubernetes" },
		"missing-revision": func(b *store.BotHomeBinding) { b.Revision = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			b := good
			mutate(&b)
			if _, err := employeeHomeVolume(employeeDiskJSON(t, b), good.BotID.String()); err == nil {
				t.Fatal("invalid identity accepted")
			}
		})
	}
	for _, key := range []string{"", "a", "-flag", "/path", "a/b", "a\\b", "a\nb", "a\rb", "a b", `{"backend":"docker","ref":"nested-home"}`, `"quoted-home"`} {
		t.Run("key-"+key, func(t *testing.T) {
			b := good
			b.StorageKey = key
			if _, err := employeeHomeVolume(employeeDiskJSON(t, b), good.BotID.String()); err == nil {
				t.Fatal("invalid volume accepted")
			}
		})
	}
	raw := employeeDiskJSON(t, map[string]any{"bots": []employeeDiskRow{{ID: good.BotID.String(), HomeBinding: "broken"}}})
	if _, err := employeeDiskHomeVolume([]byte(raw), "employee-peer-disk-before.json", good.BotID.String()); err == nil || !strings.Contains(err.Error(), "employee-peer-disk-before.json bots[0].home_binding") {
		t.Fatal("unlabeled disk error", err)
	}
}

func TestEmployeePeerPayloadTypesAndSources(t *testing.T) {
	peer := store.Bot{ID: uuid.New(), HomeSessionID: uuid.New()}
	run := store.BotRun{ID: uuid.New(), BotID: peer.ID, UserEventID: "exact-source", HistoryCursor: 9007199254740993, Trigger: store.TriggerManual, Presentation: store.BotRunPresentationHomeInteractive}
	sse := employeePeerPayload{Source: "employee-peer-title-history.sse", Path: employeePeerHistoryPath(peer), Status: 200, ContentType: string(employeePeerSSE), Format: employeePeerSSE, Body: admissionHistory(run.UserEventID, "user", employeePeerTitle)}
	got, err := sse.titleRun(peer, []store.BotRun{run})
	if err != nil || got.HistoryCursor != run.HistoryCursor {
		t.Fatal("lost source precision", err)
	}
	if err := sse.decode(&struct{}{}); err == nil {
		t.Fatal("SSE decoded as JSON")
	}
	jsonPayload := employeePeerPayload{Source: "employee-peer-runs.json", Path: "/runs", Status: 200, ContentType: "application/json; charset=utf-8", Format: employeePeerJSON, Body: `{"runs":[]}`}
	if err := jsonPayload.decode(&struct{}{}); err != nil {
		t.Fatal(err)
	}
	if _, err := jsonPayload.titleRun(peer, nil); err == nil {
		t.Fatal("JSON decoded as SSE")
	}
	for name, mutate := range map[string]func(*employeePeerPayload){
		"http-error":   func(p *employeePeerPayload) { p.Status = 404 },
		"wrong-type":   func(p *employeePeerPayload) { p.ContentType = "application/json" },
		"missing-type": func(p *employeePeerPayload) { p.ContentType = "" },
		"json-in-sse":  func(p *employeePeerPayload) { p.Body = `{"messages":[]}` },
		"bad-frame":    func(p *employeePeerPayload) { p.Body = "event: transcript\ndata: {bad}\n\n" },
	} {
		t.Run(name, func(t *testing.T) {
			p := sse
			mutate(&p)
			_, err := p.titleRun(peer, []store.BotRun{run})
			if err == nil || !strings.Contains(err.Error(), p.Source) || !strings.Contains(err.Error(), p.Path) {
				t.Fatalf("must label source/path: %v", err)
			}
		})
	}
	jsonPayload.Body = "agent output is not JSON"
	if err := jsonPayload.decode(&struct{}{}); err == nil || !strings.Contains(err.Error(), jsonPayload.Source) {
		t.Fatal("unlabeled JSON error", err)
	}
}
