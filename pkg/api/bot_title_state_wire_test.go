package api

import (
	"context"
	"encoding/json"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/session"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBotTitleStartFailsClosed(t *testing.T) {
	owner := uuid.NewString()
	ctx, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner, OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx = store.WithTenantContext(ctx, &store.TenantContext{UserID: owner, OrgSlug: "org", TeamSlug: "team"})
	repo := &minimalBotCreateStore{}
	ctx = store.WithServices(ctx, &store.Services{PersonalBots: repo})
	w := httptest.NewRecorder()
	BotCreateHandler(w, httptest.NewRequest("POST", "/api/studio/bots", strings.NewReader(`{"name":"Title","idempotency_key":"key","start_from_title":true}`)).WithContext(ctx))
	if w.Code != 503 || repo.request.Name != "" {
		t.Fatalf("silently fell back: %d %+v", w.Code, repo.request)
	}
}

func TestBotRunStateWireValidation(t *testing.T) {
	b := store.Bot{ID: uuid.New(), HomeSessionID: uuid.New()}
	run := store.BotRun{ID: uuid.New(), BotID: b.ID, SessionID: uuid.New(), Presentation: store.BotRunPresentationHomeInteractive, Status: store.RunQueued}
	event, err := store.NewBotRunStateEvent(b, run, 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := marshalBotTranscriptEvent(event, b.ID, b.HomeSessionID)
	if err != nil {
		t.Fatal(err)
	}
	original, _ := json.Marshal(event)
	if string(raw) != string(original) {
		t.Fatal("state was translated")
	}
	if _, err = marshalBotTranscriptEvent(event, uuid.New(), b.HomeSessionID); err == nil {
		t.Fatal("cross-bot state accepted")
	}
	bad := *event
	bad.Actions = session.EventActions{StateDelta: map[string]any{store.BotRunStateKey: map[string]any{"schema_version": 1, "status": "bogus"}}}
	if _, err = marshalBotTranscriptEvent(&bad, b.ID, b.HomeSessionID); err == nil {
		t.Fatal("invalid known payload accepted")
	}
	bad.Actions.StateDelta[store.BotRunStateKey] = map[string]any{"schema_version": 2, "future": "opaque"}
	if _, err = marshalBotTranscriptEvent(&bad, b.ID, b.HomeSessionID); err != nil {
		t.Fatal("future payload not preserved", err)
	}
}
