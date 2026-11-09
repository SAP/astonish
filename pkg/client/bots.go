package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// Bots is a scoped, authenticated control-plane client, never an execution runtime.
type Bots struct {
	Client *Client
	Scope  store.BotScope
}

func (b Bots) path(suffix string) string {
	return "/api/studio/bots" + suffix + "?scope=" + url.QueryEscape(string(b.Scope))
}
func (b Bots) request(ctx context.Context, method, path string, body, dst any) error {
	if b.Scope != store.BotScopePersonal && b.Scope != store.BotScopeTeam {
		return fmt.Errorf("invalid Bot scope")
	}
	resp, err := b.Client.doBotRequest(ctx, method, path, body, false)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return parseErrorResponse(resp)
	}
	if dst != nil {
		return json.NewDecoder(resp.Body).Decode(dst)
	}
	return nil
}
func (c *Client) doBotRequest(ctx context.Context, method, path string, body any, stream bool) (*http.Response, error) {
	for attempt := 0; attempt < 2; attempt++ {
		req, err := c.buildRequest(method, path, body)
		if err != nil {
			return nil, err
		}
		req = req.WithContext(ctx)
		hc := c.httpClient
		if stream {
			req.Header.Set("Accept", "text/event-stream")
			hc = &http.Client{Transport: c.httpClient.Transport}
		}
		resp, err := hc.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusUnauthorized || attempt == 1 {
			return resp, nil
		}
		resp.Body.Close()
		if err = c.refresh(); err != nil {
			return nil, fmt.Errorf("session expired: %w (run 'astonish login')", err)
		}
	}
	return nil, fmt.Errorf("authentication failed")
}
func (b Bots) List(ctx context.Context) ([]store.Bot, error) {
	out := []store.Bot{}
	after := ""
	for {
		var page struct {
			Bots []store.Bot `json:"bots"`
		}
		err := b.request(ctx, "GET", b.path("")+"&limit=200&after="+url.QueryEscape(after), nil, &page)
		if err != nil {
			return nil, err
		}
		out = append(out, page.Bots...)
		if len(page.Bots) < 200 {
			return out, nil
		}
		next := page.Bots[len(page.Bots)-1].ID.String()
		if next == after {
			return nil, fmt.Errorf("Bot pagination did not advance")
		}
		after = next
	}
}
func (b Bots) Get(ctx context.Context, id string) (*store.Bot, error) {
	var v store.Bot
	err := b.request(ctx, "GET", b.path("/"+url.PathEscape(id)), nil, &v)
	return &v, err
}

// Snapshot returns the scoped authoritative read projection and cancels HTTP
// transport work when the caller detaches or supersedes the request.
func (b Bots) Snapshot(ctx context.Context, id string) (*store.BotSnapshot, error) {
	var v store.BotSnapshot
	err := b.request(ctx, "GET", b.path("/"+url.PathEscape(id)+"/snapshot"), nil, &v)
	return &v, err
}

// Resolve accepts an exact UUID or an unambiguous exact name across all pages.
func (b Bots) Resolve(ctx context.Context, ref string) (*store.Bot, error) {
	if id, err := uuid.Parse(ref); err == nil {
		return b.Get(ctx, id.String())
	}
	list, err := b.List(ctx)
	if err != nil {
		return nil, err
	}
	var found *store.Bot
	var candidates []string
	for i := range list {
		if list[i].Name == ref {
			found = &list[i]
			candidates = append(candidates, fmt.Sprintf("%s (scope=%s, status=%s)", list[i].ID, b.Scope, list[i].Status))
		}
	}
	if len(candidates) > 1 {
		return nil, fmt.Errorf("ambiguous Bot name %q; use an exact UUID; candidates: %s", ref, strings.Join(candidates, "; "))
	}
	if found == nil {
		return nil, fmt.Errorf("Bot %q not found", ref)
	}
	return found, nil
}

type BotDraft struct {
	StartFromTitle bool               `json:"start_from_title,omitempty"`
	Name           string             `json:"name"`
	IdempotencyKey string             `json:"idempotency_key"`
	Definition     BotDraftDefinition `json:"definition"`
}
type BotDraftDefinition struct {
	Charter string            `json:"charter"`
	Persona *store.BotPersona `json:"persona,omitempty"`
}

func (b Bots) Create(ctx context.Context, q BotDraft) (*store.Bot, error) {
	var v store.Bot
	err := b.request(ctx, "POST", b.path(""), q, &v)
	return &v, err
}

type BotLifecycle string

const (
	BotPause   BotLifecycle = "pause"
	BotResume  BotLifecycle = "activate"
	BotStop    BotLifecycle = "stop"
	BotDelete  BotLifecycle = "delete"
	BotRestore BotLifecycle = "restore"
)

func (b Bots) Lifecycle(ctx context.Context, id string, action BotLifecycle, revision int64, reason string) (*store.Bot, error) {
	method, suffix := "POST", "/"+url.PathEscape(id)+"/"+string(action)
	switch action {
	case BotPause, BotResume, BotStop, BotRestore:
	case BotDelete:
		method = "DELETE"
		suffix = "/" + url.PathEscape(id)
	default:
		return nil, fmt.Errorf("unsupported Bot action")
	}
	if revision < 1 || strings.TrimSpace(reason) == "" {
		return nil, fmt.Errorf("reviewed --revision and --reason required")
	}
	var v store.Bot
	err := b.request(ctx, method, b.path(suffix), struct {
		Revision int64  `json:"expected_revision"`
		Reason   string `json:"reason"`
	}{revision, reason}, &v)
	return &v, err
}
func (b Bots) Run(ctx context.Context, id string, version int64, key, instructions string) (*store.BotRun, error) {
	return b.submitRun(ctx, id, "runs", version, key, instructions)
}

// TestRun explicitly queues one reviewed draft test without activating the Bot.
func (b Bots) TestRun(ctx context.Context, id string, version int64, key, instructions string) (*store.BotRun, error) {
	return b.submitRun(ctx, id, "test-run", version, key, instructions)
}

// Conversation queues a tools-disabled turn for a paused Bot, never activation.
func (b Bots) Conversation(ctx context.Context, id string, version int64, key, instructions string) (*store.BotRun, error) {
	return b.submitRun(ctx, id, "conversation", version, key, instructions)
}

func (b Bots) submitRun(ctx context.Context, id, endpoint string, version int64, key, instructions string) (*store.BotRun, error) {
	var v store.BotRun
	err := b.request(ctx, "POST", b.path("/"+url.PathEscape(id)+"/"+endpoint), struct {
		Version      int64  `json:"version"`
		Key          string `json:"idempotency_key"`
		Instructions string `json:"instructions"`
	}{version, key, instructions}, &v)
	return &v, err
}
func (b Bots) Runs(ctx context.Context, id string) ([]store.BotRun, error) {
	out := []store.BotRun{}
	after := ""
	for {
		var v struct {
			Runs []store.BotRun `json:"runs"`
		}
		if err := b.request(ctx, "GET", b.path("/"+url.PathEscape(id)+"/runs")+"&limit=200&after="+url.QueryEscape(after), nil, &v); err != nil {
			return nil, err
		}
		out = append(out, v.Runs...)
		if len(v.Runs) < 200 {
			return out, nil
		}
		next := v.Runs[len(v.Runs)-1].ID.String()
		if next == after {
			return nil, fmt.Errorf("run pagination did not advance")
		}
		after = next
	}
}

// Goals returns the complete goal collection; this endpoint is not paginated.
func (b Bots) Goals(ctx context.Context, id string) ([]store.BotGoal, error) {
	var v struct {
		Goals []store.BotGoal `json:"goals"`
	}
	err := b.request(ctx, "GET", b.path("/"+url.PathEscape(id)+"/goals"), nil, &v)
	if err != nil {
		return nil, err
	}
	if v.Goals == nil {
		v.Goals = []store.BotGoal{}
	}
	return v.Goals, nil
}
func (b Bots) Approvals(ctx context.Context, id string) ([]store.BotApproval, error) {
	out := []store.BotApproval{}
	after := ""
	for {
		var v struct {
			Approvals []store.BotApproval `json:"approvals"`
		}
		err := b.request(ctx, "GET", b.path("/"+url.PathEscape(id)+"/approvals")+"&limit=200&after="+url.QueryEscape(after), nil, &v)
		if err != nil {
			return nil, err
		}
		out = append(out, v.Approvals...)
		if len(v.Approvals) < 200 {
			return out, nil
		}
		next := v.Approvals[len(v.Approvals)-1].ID.String()
		if next == after {
			return nil, fmt.Errorf("approval pagination did not advance")
		}
		after = next
	}
}

type BotDecision struct {
	ActionDigest string                        `json:"action_digest"`
	Status       store.ApprovalStatus          `json:"status"`
	Response     *store.BotInteractionResponse `json:"response,omitempty"`
}

func (b Bots) Decide(ctx context.Context, id, approvalID string, q BotDecision) (*store.BotApproval, error) {
	if q.ActionDigest == "" || (q.Status != store.ApprovalApproved && q.Status != store.ApprovalDenied) {
		return nil, fmt.Errorf("exact digest and approve/deny decision required")
	}
	var v store.BotApproval
	err := b.request(ctx, "POST", b.path("/"+url.PathEscape(id)+"/approvals/"+url.PathEscape(approvalID)+"/decision"), q, &v)
	return &v, err
}
func (b Bots) ResumeRun(ctx context.Context, id, runID, approvalID string) error {
	return b.request(ctx, "POST", b.path("/"+url.PathEscape(id)+"/runs/"+url.PathEscape(runID)+"/resume"), struct {
		ApprovalID string `json:"approval_id"`
	}{approvalID}, nil)
}

const (
	BotTranscriptEvent = "transcript"
	BotReplayComplete  = "replay_complete"
)

// Watch replays persisted events and follows deltas. Cursor advances only after
// the consumer accepts a frame. Detaching cancels HTTP reads, not daemon work.
// follow=false returns after replay_complete; no finite-snapshot cursor is used.
func (b Bots) Watch(ctx context.Context, id, sessionID, after string, follow bool, consume func(SSEEvent) error) error {
	if b.Scope != store.BotScopePersonal && b.Scope != store.BotScopeTeam {
		return fmt.Errorf("invalid Bot scope")
	}
	cursor, err := botCursor(sessionID, after)
	if err != nil {
		return err
	}
	for {
		path := b.path("/"+url.PathEscape(id)+"/sessions/"+url.PathEscape(sessionID)+"/events") + "&after=" + url.QueryEscape(after)
		resp, err := b.Client.doBotRequest(ctx, "GET", path, nil, true)
		if err != nil {
			return err
		}
		if resp.StatusCode >= 400 {
			err = parseErrorResponse(resp)
			resp.Body.Close()
			return err
		}
		stream := NewSSEStream(resp.Body)
		for {
			ev, e := stream.Next()
			if e != nil {
				err = e
				break
			}
			if ev.Type != BotTranscriptEvent && ev.Type != BotReplayComplete {
				continue
			}
			n, e := botCursor(sessionID, ev.ID)
			if e != nil {
				stream.Close()
				return e
			}
			if ev.Type == BotReplayComplete {
				if n < cursor {
					stream.Close()
					return fmt.Errorf("Bot replay cursor regressed")
				}
				if !follow {
					stream.Close()
					return nil
				}
				continue
			}
			if n <= cursor {
				continue
			}
			if e = consume(*ev); e != nil {
				stream.Close()
				return e
			}
			cursor = n
			after = ev.ID
		}
		stream.Close()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !follow {
			return err
		}
		if err != io.EOF {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}
func botCursor(session, raw string) (int64, error) {
	if raw == "" {
		return 0, nil
	}
	prefix := session + ":"
	if !strings.HasPrefix(raw, prefix) {
		return 0, fmt.Errorf("cursor belongs to another Bot session")
	}
	n, err := strconv.ParseInt(strings.TrimPrefix(raw, prefix), 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid Bot cursor")
	}
	return n, nil
}
