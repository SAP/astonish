package daemon

import (
	"context"
	"encoding/json"
	"iter"
	"log"
	"os"
	"reflect"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
)

const botModelObservationMarker = "[bot-model-observation-v1] "

type botModelObservationPhase string
type botModelObservationOutcome string

const (
	botModelResolved    botModelObservationPhase   = "resolved"
	botModelStarted     botModelObservationPhase   = "generation-start"
	botModelEnded       botModelObservationPhase   = "generation-end"
	botModelComplete    botModelObservationOutcome = "complete"
	botModelError       botModelObservationOutcome = "error"
	botModelCancelled   botModelObservationOutcome = "cancelled"
	botModelStopped     botModelObservationOutcome = "consumer-stopped"
	botModelInterrupted botModelObservationOutcome = "interrupted"
)

// Only identity metadata belongs here. Never add request/response payloads, raw
// errors, provider configuration, credentials, or authorization/lease tokens.
type botModelObservation struct {
	Boundary     string                     `json:"observation_boundary"`
	WireVerified bool                       `json:"wire_verified"`
	At           time.Time                  `json:"at"`
	PID          int                        `json:"pid"`
	WorkerID     string                     `json:"worker_id"`
	AdapterID    uuid.UUID                  `json:"adapter_id"`
	BotID        uuid.UUID                  `json:"bot_id"`
	RunID        uuid.UUID                  `json:"run_id"`
	SessionID    uuid.UUID                  `json:"session_id"`
	OwnerID      uuid.UUID                  `json:"owner_id"`
	Scope        string                     `json:"scope"`
	Provider     string                     `json:"provider"`
	Model        string                     `json:"model"`
	AdapterType  string                     `json:"adapter_type"`
	AdapterName  string                     `json:"adapter_name"`
	Phase        botModelObservationPhase   `json:"phase"`
	CallID       uuid.UUID                  `json:"call_id,omitempty"`
	Ordinal      uint64                     `json:"ordinal,omitempty"`
	Stream       bool                       `json:"stream"`
	RequestModel string                     `json:"request_model"`
	Responses    int                        `json:"responses"`
	Outcome      botModelObservationOutcome `json:"outcome,omitempty"`
}

type botObservedModel struct {
	inner    model.LLM
	identity botModelObservation
	calls    atomic.Uint64
}

func botObservationLabel(s string) string {
	if len(s) > 256 {
		return "<invalid-metadata>"
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return "<invalid-metadata>"
		}
	}
	return s
}

// Observation must never become an execution gate. Even a broken log writer or
// an unexpected Name implementation leaves the original model available.
func observeBotResolvedModel(r bot.RunRequest, workerID, providerName, modelName string, llm model.LLM) (observed model.LLM) {
	observed = llm
	defer func() { _ = recover() }()
	if llm == nil {
		return
	}
	value := reflect.ValueOf(llm)
	switch value.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		if value.IsNil() {
			return
		}
	}
	id, err := uuid.NewRandom()
	if err != nil {
		return
	}
	record := botModelObservation{
		Boundary: "native-worker-adapter", PID: os.Getpid(), WorkerID: botObservationLabel(workerID), AdapterID: id,
		BotID: r.Bot.ID, RunID: r.Run.ID, SessionID: r.Run.SessionID, OwnerID: r.Bot.OwnerID, Scope: string(r.Bot.Scope),
		Provider: botObservationLabel(providerName), Model: botObservationLabel(modelName),
		AdapterType: reflect.TypeOf(llm).String(), AdapterName: botObservationLabel(llm.Name()), Phase: botModelResolved,
	}
	emitBotModelObservation(record)
	observed = &botObservedModel{inner: llm, identity: record}
	return
}

func emitBotModelObservation(record botModelObservation) {
	defer func() { _ = recover() }()
	record.At = time.Now().UTC()
	raw, err := json.Marshal(record)
	if err == nil {
		log.Printf("%s%s", botModelObservationMarker, raw)
	}
}

func (m *botObservedModel) Name() string { return m.inner.Name() }

func (m *botObservedModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	sequence := m.inner.GenerateContent(ctx, req, stream)
	return func(yield func(*model.LLMResponse, error) bool) {
		record := m.identity
		id, err := uuid.NewRandom()
		if err != nil {
			sequence(yield)
			return
		}
		record.CallID, record.Ordinal, record.Stream = id, m.calls.Add(1), stream
		if req != nil {
			record.RequestModel = botObservationLabel(req.Model)
		}
		record.Phase = botModelStarted
		emitBotModelObservation(record)
		record.Outcome = botModelInterrupted
		defer func() { record.Phase = botModelEnded; emitBotModelObservation(record) }()
		hadError, stopped := false, false
		sequence(func(response *model.LLMResponse, err error) bool {
			if response != nil {
				record.Responses++
			}
			if err != nil {
				hadError = true
			}
			more := yield(response, err)
			if !more {
				stopped = true
			}
			return more
		})
		switch {
		case ctx.Err() != nil:
			record.Outcome = botModelCancelled
		case hadError:
			record.Outcome = botModelError
		case stopped:
			record.Outcome = botModelStopped
		default:
			record.Outcome = botModelComplete
		}
	}
}
