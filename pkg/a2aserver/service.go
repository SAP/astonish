// Package a2aserver owns the inbound A2A protocol lifecycle independently of Channels.
package a2aserver

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/SAP/astonish/pkg/a2a"
	"github.com/SAP/astonish/pkg/channels"
	"github.com/google/uuid"
)

// DispatchFunc executes a normalized inbound message and receives complete turns.
type DispatchFunc func(context.Context, channels.InboundMessage, func(context.Context, channels.OutboundMessage) error) error

// Identity identifies the OAuth principal submitting an A2A task. The API layer
// will construct it from canonical Astonish OAuth claims in the next phase.
type Identity struct {
	AgentID string
	UserID  string
	OrgID   string
}

// Config supplies the task and execution dependencies for Service.
type Config struct {
	TaskStore    a2a.TaskStore
	PushNotifier *a2a.PushNotifier
	Dispatcher   DispatchFunc
	BaseURL      string
	Logger       *log.Logger
}

// Service owns task correlation, task state, and protocol-specific delivery.
type Service struct {
	store     a2a.TaskStore
	dispatch  DispatchFunc
	push      *a2a.PushNotifier
	baseURL   string
	logger    *log.Logger
	waitersMu sync.Mutex
	waiters   map[string]chan channels.OutboundMessage
}

// New creates an endpoint-owned A2A service.
func New(cfg Config) (*Service, error) {
	if cfg.TaskStore == nil {
		return nil, fmt.Errorf("a2a task store is required")
	}
	if cfg.Dispatcher == nil {
		return nil, fmt.Errorf("a2a dispatcher is required")
	}
	if cfg.Logger == nil {
		cfg.Logger = log.Default()
	}
	if cfg.PushNotifier == nil {
		cfg.PushNotifier = a2a.NewPushNotifier(cfg.Logger)
	}
	return &Service{store: cfg.TaskStore, dispatch: cfg.Dispatcher, push: cfg.PushNotifier, baseURL: cfg.BaseURL, logger: cfg.Logger, waiters: make(map[string]chan channels.OutboundMessage)}, nil
}

// SendMessage creates, dispatches, and optionally waits for an A2A task.
func (s *Service) SendMessage(ctx context.Context, identity Identity, params a2a.SendMessageParams) (*a2a.Task, error) {
	if identity.AgentID == "" {
		return nil, fmt.Errorf("a2a agent identity is required")
	}
	contextID := ""
	if params.Configuration != nil {
		contextID = params.Configuration.ContextID
	}
	if contextID == "" {
		contextID = uuid.NewString()
	}
	task := s.store.Create(identity.AgentID, contextID)
	_ = s.store.UpdateState(task.ID, a2a.TaskStateWorking, &params.Message)
	inbound := channels.InboundMessage{
		ID: task.ID, ChannelID: "a2a", SenderID: identity.UserID, SenderName: identity.AgentID,
		ChatID: contextID, ChatType: channels.ChatTypeDirect,
		ThreadID: SessionKey(identity.AgentID, identity.UserID, contextID),
		Text:     NormalizePartsToText(params.Message.Parts), Timestamp: time.Now(), Raw: params.Message,
	}
	if identity.OrgID != "" {
		inbound.RoutingHint = &channels.RoutingHint{OrgSlug: identity.OrgID}
	}
	if params.Configuration != nil && params.Configuration.ReturnImmediately {
		go s.dispatchAsync(ctx, task.ID, inbound)
		return s.store.Get(task.ID)
	}
	waiter := s.register(task.ID)
	defer s.unregister(task.ID)
	if err := s.dispatch(ctx, inbound, func(replyCtx context.Context, msg channels.OutboundMessage) error {
		return s.reply(replyCtx, task.ID, msg)
	}); err != nil {
		s.fail(task.ID, err)
		return s.store.Get(task.ID)
	}
	select {
	case reply := <-waiter:
		s.complete(task.ID, reply)
	case <-ctx.Done():
		_ = s.store.UpdateState(task.ID, a2a.TaskStateCanceled, nil)
	case <-time.After(5 * time.Minute):
		s.fail(task.ID, fmt.Errorf("request timed out"))
	}
	return s.store.Get(task.ID)
}

func (s *Service) dispatchAsync(ctx context.Context, taskID string, inbound channels.InboundMessage) {
	if err := s.dispatch(ctx, inbound, func(replyCtx context.Context, msg channels.OutboundMessage) error {
		return s.reply(replyCtx, taskID, msg)
	}); err != nil {
		s.fail(taskID, err)
	}
}

func (s *Service) register(taskID string) <-chan channels.OutboundMessage {
	ch := make(chan channels.OutboundMessage, 16)
	s.waitersMu.Lock()
	s.waiters[taskID] = ch
	s.waitersMu.Unlock()
	return ch
}
func (s *Service) unregister(taskID string) {
	s.waitersMu.Lock()
	delete(s.waiters, taskID)
	s.waitersMu.Unlock()
}

func (s *Service) reply(ctx context.Context, taskID string, msg channels.OutboundMessage) error {
	s.waitersMu.Lock()
	waiter := s.waiters[taskID]
	s.waitersMu.Unlock()
	if waiter != nil {
		select {
		case waiter <- msg:
		default:
		}
	}
	s.complete(taskID, msg)
	return nil
}
func (s *Service) complete(taskID string, msg channels.OutboundMessage) {
	response := &a2a.Message{Role: "agent", Parts: []a2a.Part{a2a.TextPart{Text: msg.Text}}}
	if task, err := s.store.Get(taskID); err == nil && !task.Status.State.IsTerminal() {
		_ = s.store.UpdateState(taskID, a2a.TaskStateCompleted, response)
		_ = s.store.AddArtifact(taskID, a2a.Artifact{Name: "response", Parts: response.Parts, LastChunk: true})
	}
	if cfg := s.store.GetPushConfig(taskID); cfg != nil {
		go func() {
			if err := s.push.NotifyStatusUpdate(cfg, a2a.TaskStatusUpdateEvent{TaskID: taskID, Status: a2a.TaskStatus{State: a2a.TaskStateCompleted, Timestamp: time.Now(), Message: response}}); err != nil {
				s.logger.Printf("[a2a] push notification failed for task %s: %v", taskID, err)
			}
		}()
	}
}
func (s *Service) fail(taskID string, err error) {
	_ = s.store.UpdateState(taskID, a2a.TaskStateFailed, &a2a.Message{Role: "agent", Parts: []a2a.Part{a2a.TextPart{Text: err.Error()}}})
}

// GetTask returns a task only when the identity owns it.
func (s *Service) GetTask(agentID, taskID string) (*a2a.Task, error) {
	task, err := s.store.Get(taskID)
	if err != nil || task.AgentID != agentID {
		return nil, fmt.Errorf("task %s not found", taskID)
	}
	return task, nil
}

// CancelTask cancels an owned task.
func (s *Service) CancelTask(agentID, taskID string) error {
	if _, err := s.GetTask(agentID, taskID); err != nil {
		return err
	}
	return s.store.Cancel(taskID)
}

// TaskStore exposes protocol push configuration operations to the route layer.
func (s *Service) TaskStore() a2a.TaskStore { return s.store }

// BaseURL returns the public endpoint base used in the agent card.
func (s *Service) BaseURL() string { return s.baseURL }

// PushNotifier exposes the existing notifier to the route layer.
func (s *Service) PushNotifier() *a2a.PushNotifier { return s.push }

// SessionKey scopes continuity to the user where available, otherwise to the agent.
func SessionKey(agentID, userID, contextID string) string {
	if userID != "" {
		return fmt.Sprintf("a2a:direct:%s:%s", userID, contextID)
	}
	return fmt.Sprintf("a2a:direct:%s:%s", agentID, contextID)
}

// NormalizePartsToText turns structured protocol parts into agent input text.
func NormalizePartsToText(parts []a2a.Part) string {
	var texts []string
	for _, p := range parts {
		switch v := p.(type) {
		case a2a.TextPart:
			if v.Text != "" {
				texts = append(texts, v.Text)
			}
		case a2a.DataPart:
			texts = append(texts, fmt.Sprintf("[data: %v]", v.Data))
		case a2a.FilePart:
			if v.Name != "" {
				texts = append(texts, fmt.Sprintf("[file: %s]", v.Name))
			}
		}
	}
	return strings.Join(texts, "\n")
}
