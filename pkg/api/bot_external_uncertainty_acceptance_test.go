package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"github.com/google/uuid"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

// This is deliberately a controlled composition, NOT a new production adapter,
// mandate grant, human UI approval, or durable approval-store acceptance. The
// existing Chat runtime and shared HTTP implementation execute the actual wire
// requests. Only the owned sink knows whether the first write committed.
const externalAcceptanceTool = "http_request"

type externalAcceptanceClassifier struct {
	writeURL, receiptURL string
	identity             execution.BotRunIdentity
}

func (c externalAcceptanceClassifier) DescribeAction(ctx context.Context, name string, raw json.RawMessage) (execution.ActionDescriptor, error) {
	id, ok := execution.BotRunIdentityFromContext(ctx)
	if !ok || id != c.identity {
		return execution.ActionDescriptor{}, store.ErrBotForbidden
	}
	var args tools.HttpRequestArgs
	if name != externalAcceptanceTool || json.Unmarshal(raw, &args) != nil {
		return execution.ActionDescriptor{}, store.ErrBotForbidden
	}
	effect := execution.EffectSend
	switch {
	case args.Method == http.MethodPost && args.URL == c.writeURL:
	case args.Method == http.MethodGet && args.URL == c.receiptURL:
		effect = execution.EffectRead
	default:
		return execution.ActionDescriptor{}, store.ErrBotForbidden
	}
	return execution.ActionDescriptor{Tool: name, Arguments: raw, Effect: effect, ConstraintsVerified: true, TargetFingerprint: args.URL}, nil
}

type externalAcceptanceProviderReceipt struct {
	ProviderKey   string `json:"provider_key"`
	PayloadSHA256 string `json:"payload_sha256"`
	EffectCount   int    `json:"effect_count"`
}

func TestBotExternalUncertaintyLoopbackAcceptance(t *testing.T) {
	// HttpRequest clones this transport. Pin every dial to the one owned listener,
	// disable proxies, and restore globals. No DNS or non-loopback network is used.
	// Do not parallelize this test with other process-global HTTP-policy tests.
	tools.SetAllowPrivateNetworks(true)
	defer tools.SetAllowPrivateNetworks(false)
	for _, mode := range []string{"approve", "deny", "wrong_scope", "wrong_target"} {
		t.Run(mode, func(t *testing.T) {
			key := uuid.NewString()
			body := "one bounded acceptance effect"
			sum := sha256.Sum256([]byte(body))
			want := externalAcceptanceProviderReceipt{ProviderKey: key, PayloadSHA256: hex.EncodeToString(sum[:]), EffectCount: 1}
			receiptPath := filepath.Join(t.TempDir(), "provider-receipt.json")
			var mu sync.Mutex
			var posts, gets, effects atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/effects":
					posts.Add(1)
					raw, err := io.ReadAll(io.LimitReader(r.Body, 1024))
					if err != nil || string(raw) != body || r.Header.Get("Idempotency-Key") != key {
						http.Error(w, "wrong exact resource", http.StatusForbidden)
						return
					}
					if _, err := os.Stat(receiptPath); errors.Is(err, os.ErrNotExist) {
						encoded, _ := json.Marshal(want)
						if err := os.WriteFile(receiptPath, encoded, 0600); err != nil {
							t.Error(err)
							w.WriteHeader(http.StatusInternalServerError)
							return
						}
						effects.Add(1)
					} else if err != nil {
						t.Error(err)
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					// Receipt is written before losing the response: no status or body
					// is sent. This is not a synthetic successful tool return.
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = conn.Close()
				case r.Method == http.MethodGet && r.URL.Path == "/receipts/"+key:
					gets.Add(1)
					raw, err := os.ReadFile(receiptPath)
					if err != nil {
						http.NotFound(w, r)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write(raw)
				default:
					http.Error(w, "outside owned resource", http.StatusForbidden)
				}
			}))
			defer srv.Close()
			endpoint, err := url.Parse(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			original := http.DefaultTransport
			transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				if address != endpoint.Host {
					return nil, fmt.Errorf("non-fixture dial rejected")
				}
				return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, address)
			}}
			http.DefaultTransport = transport
			defer func() { http.DefaultTransport = original; transport.CloseIdleConnections() }()

			ctx, req, ss := botChatTestRequest(t)
			policy, err := execution.NewRunPolicy(execution.RunPolicySpec{Identity: req.Policy.Identity(), Level: execution.AutonomySupervised, AllowedTools: []string{externalAcceptanceTool}})
			if err != nil {
				t.Fatal(err)
			}
			req.Policy = policy
			toolLimit, tokenLimit := int64(8), int64(4096)
			req.Budget, err = bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 30, MaxToolCalls: &toolLimit, MaxTokens: &tokenLimit}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			classifier := externalAcceptanceClassifier{writeURL: srv.URL + "/effects", receiptURL: srv.URL + "/receipts/" + key, identity: req.Policy.Identity()}
			ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Classifier: classifier, Guard: resumeTestGuard{}})
			approval := &resumeTestStore{}
			ctx = bot.WithApprovalStore(ctx, approval)
			var cleaned atomic.Int64
			var lostResponse atomic.Bool
			action, err := functiontool.New(functiontool.Config{Name: externalAcceptanceTool, Description: "Owned bounded loopback acceptance sink"}, func(c tool.Context, args tools.HttpRequestArgs) (map[string]any, error) {
				// The real dispatcher already authorizes/consumes the exact grant.
				// This adapter adds a transport bound, not a second authorization.
				if _, err := classifier.DescribeAction(c, externalAcceptanceTool, mustExternalJSON(t, args)); err != nil {
					return nil, err
				}
				out, err := tools.HttpRequest(c, args)
				if err != nil {
					if args.Method != http.MethodPost {
						return nil, err
					}
					lostResponse.Store(true)
					message := "External write outcome uncertain; reconcile provider key " + key + "; never replay the write."
					bot.RecordToolOutcome(c, c.FunctionCallID(), bot.ToolOutcome{Unverified: []string{message}})
					return map[string]any{"status": "uncertain", "provider_key": key, "error": err.Error()}, nil
				}
				var receipt externalAcceptanceProviderReceipt
				if args.Method != http.MethodGet || out.StatusCode != http.StatusOK || json.Unmarshal([]byte(out.Body), &receipt) != nil || receipt != want {
					return nil, errors.New("provider receipt did not match exact key and payload")
				}
				bot.RecordToolOutcome(c, c.FunctionCallID(), bot.ToolOutcome{Verified: true, Changed: []store.BotEvidence{{Kind: store.BotEvidenceObservation, Summary: "Provider key confirms exactly one committed effect", Source: classifier.receiptURL, ActorID: req.Bot.OwnerID, ObservedAt: time.Now()}}})
				return map[string]any{"receipt": receipt}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			args := map[string]any{"url": classifier.writeURL, "method": http.MethodPost, "body": body, "headers": map[string]any{"Idempotency-Key": key}, "timeout": 2, "max_bytes": 1024}
			adapter, err := NewBotChatRuntimeAdapter(botChatTestFactory(t, ss, NewMockLLM(ToolCallTurn(externalAcceptanceTool, args)), []tool.Tool{action}, &cleaned))
			if err != nil {
				t.Fatal(err)
			}
			sink := func(context.Context, bot.RuntimeEvent) error { return nil }
			waiting, err := adapter.Start(ctx, req, sink)
			if err != nil || waiting.Status != store.RunWaitingApproval || posts.Load() != 0 || approval.a == nil {
				t.Fatalf("missing exact approval/no-effect barrier: %+v %v posts=%d", waiting, err, posts.Load())
			}
			if approval.a.Action.TargetFingerprint != classifier.writeURL || !strings.Contains(string(approval.a.Action.Arguments), key) {
				t.Fatal("approval lost provider binding")
			}
			req.Run.Checkpoint = waiting.Checkpoint
			req.Run.Lease.Fence++
			approval.a.Status = store.ApprovalApproved // controlled decision, NOT claimed as human/UI proof
			switch mode {
			case "deny":
				approval.a.Status = store.ApprovalDenied
			case "wrong_scope":
				req.Principal.TeamSlug = "other-team"
			case "wrong_target":
				changed := classifier
				changed.writeURL += "/unapproved"
				ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Classifier: changed, Guard: resumeTestGuard{}})
			}
			adapter, err = NewBotChatRuntimeAdapter(botChatTestFactory(t, ss, NewMockLLM(TextTurn("The write outcome is uncertain. Reconcile by provider key; do not replay.")), []tool.Tool{action}, &cleaned))
			if err != nil {
				t.Fatal(err)
			}
			result, err := adapter.Resume(ctx, req, sink)
			if mode != "approve" {
				if err == nil || posts.Load() != 0 || effects.Load() != 0 || approval.consumed != 0 {
					t.Fatalf("denial/scope failure executed: %+v %v posts=%d effects=%d", result, err, posts.Load(), effects.Load())
				}
				if _, statErr := os.Stat(receiptPath); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("denied provider receipt exists: %v", statErr)
				}
				t.Logf("EXTERNAL_UNCERTAINTY_RECEIPT %s", mustExternalJSON(t, map[string]any{"schema": "employee-external-uncertainty/v1", "scenario": mode, "passed": true, "post_attempts": posts.Load(), "effect_count": effects.Load(), "approval_consumptions": approval.consumed, "production_scope_blocked": true}))
				return
			}
			if err == nil || result.Failure == nil || !result.Failure.UncertainEffect || result.Status != store.RunPartial || len(result.Report.Unverified) == 0 || len(result.Report.Changed) != 0 || !lostResponse.Load() || posts.Load() != 1 || effects.Load() != 1 || approval.consumed != 1 {
				t.Fatalf("lost-response uncertainty not preserved: %+v %v posts=%d effects=%d consumed=%d", result, err, posts.Load(), effects.Load(), approval.consumed)
			}
			if !strings.Contains(strings.Join(result.Report.Unverified, " "), key) {
				t.Fatal("uncertain report lost reconciliation key")
			}
			for range 2 {
				if _, err := adapter.Resume(ctx, req, sink); err == nil {
					t.Fatal("consumed action resumed")
				}
			}
			// Reconciliation is a separate read-only runtime invocation; it must
			// not mutate the consumed approval or erase the original partial report.
			for range 2 {
				readCtx, readReq, readSS := ctx, req, ss
				readReq.Run.Checkpoint = nil
				readReq.Policy, err = execution.NewRunPolicy(execution.RunPolicySpec{Identity: readReq.Policy.Identity(), Level: execution.AutonomySupervised, AllowedTools: []string{externalAcceptanceTool}})
				if err != nil {
					t.Fatal(err)
				}
				readCtx = agent.WithBotPolicyRuntime(readCtx, agent.BotPolicyRuntime{Classifier: classifier, Guard: resumeTestGuard{}})
				readArgs := map[string]any{"url": classifier.receiptURL, "method": http.MethodGet, "timeout": 2, "max_bytes": 1024}
				reader, err := NewBotChatRuntimeAdapter(botChatTestFactory(t, readSS, NewMockLLM(ToolCallTurn(externalAcceptanceTool, readArgs), TextTurn("Provider receipt confirms one effect.")), []tool.Tool{action}, &cleaned))
				if err != nil {
					t.Fatal(err)
				}
				reconciled, err := reader.Start(readCtx, readReq, sink)
				if err != nil || reconciled.Status != store.RunSucceeded || len(reconciled.Report.Changed) != 1 || len(reconciled.Report.Unverified) != 0 {
					t.Fatalf("provider-key reconciliation: %+v %v", reconciled, err)
				}
			}
			if posts.Load() != 1 || gets.Load() != 2 || effects.Load() != 1 || approval.consumed != 1 {
				t.Fatal("reconciliation blindly replayed write")
			}
			t.Logf("EXTERNAL_UNCERTAINTY_RECEIPT %s", mustExternalJSON(t, map[string]any{"schema": "employee-external-uncertainty/v1", "scenario": mode, "passed": true, "provider_key": key, "payload_sha256": want.PayloadSHA256, "post_attempts": posts.Load(), "reconciliation_gets": gets.Load(), "effect_count": effects.Load(), "approval_consumptions": approval.consumed, "uncertain_report": result.Report, "run_status": result.Status, "uncertain_effect": result.Failure.UncertainEffect, "replay_rejections": 2, "production_scope_blocked": true, "human_ui_verified": false, "approval_store": "controlled_in_memory", "transport": "owned_loopback_shared_http"}))
		})
	}
}

func mustExternalJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
