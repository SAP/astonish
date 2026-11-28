package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"golang.org/x/time/rate"
)

const botWebhookIngressPath = "/api/bot-webhooks/events"

type botWebhookBackend interface {
	RegisterBotWebhook(context.Context, uuid.UUID, uuid.UUID, store.BotScope) (string, error)
	AdmitBotWebhook(context.Context, string, store.BotExternalEvent) (*store.BotRun, error)
}

func isBotWebhookIngress(r *http.Request) bool {
	return r.Method == http.MethodPost && r.URL.Path == botWebhookIngressPath
}

func registerBotWebhookRoutes(router *mux.Router, backend store.PlatformBackend) {
	// One bounded limiter per server, not an attacker-keyed unbounded map.
	limiter := rate.NewLimiter(10, 20)
	router.HandleFunc(botWebhookIngressPath, func(w http.ResponseWriter, r *http.Request) {
		if !limiter.Allow() {
			http.Error(w, "Webhook rate limit", http.StatusTooManyRequests)
			return
		}
		if len(r.Header.Get("X-Astonish-Signature")) != 64 {
			http.Error(w, "Invalid webhook", http.StatusUnauthorized)
			return
		}
		b, ok := backend.(botWebhookBackend)
		if !ok {
			botHTTPError(w, store.ErrBotUnsupported)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
		r.Body = http.MaxBytesReader(w, r.Body, 96*1024)
		var q struct {
			BindingID string                 `json:"binding_id"`
			Event     store.BotExternalEvent `json:"event"`
		}
		if !botDecodeRequest(w, r, &q) {
			return
		}
		q.Event.Signature = r.Header.Get("X-Astonish-Signature")
		run, err := b.AdmitBotWebhook(ctx, q.BindingID, q.Event)
		if err != nil {
			http.Error(w, "Webhook rejected", http.StatusUnauthorized)
			return
		}
		// No scoped run details, instructions, ownership or credentials leave ingress.
		botWriteJSON(w, http.StatusAccepted, map[string]any{"run_id": run.ID})
	}).Methods(http.MethodPost)
	router.HandleFunc("/api/studio/bots/{botID}/triggers/{triggerID}/webhook-binding", func(w http.ResponseWriter, r *http.Request) {
		repo, _, ok := botRequestStore(w, r)
		if !ok {
			return
		}
		id, ok := botPathID(w, r)
		if !ok {
			return
		}
		tid, ok := botRuntimePathID(w, r, "triggerID")
		if !ok {
			return
		}
		bot, err := repo.Get(r.Context(), id)
		if err != nil {
			botHTTPError(w, err)
			return
		}
		b, ok := backend.(botWebhookBackend)
		if !ok {
			botHTTPError(w, store.ErrBotUnsupported)
			return
		}
		binding, err := b.RegisterBotWebhook(r.Context(), id, tid, bot.Scope)
		if err != nil {
			botHTTPError(w, err)
			return
		}
		botWriteJSON(w, http.StatusCreated, map[string]string{"binding_id": binding, "path": strings.TrimSpace(botWebhookIngressPath)})
	}).Methods(http.MethodPost)
}
