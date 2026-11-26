package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
)

type cancellableDispatchClient struct {
	stubClient
	started chan struct{}
}

func (c *cancellableDispatchClient) CallContext(ctx context.Context, _, _ string, _ map[string]any) (json.RawMessage, error) {
	close(c.started)
	<-ctx.Done()
	return nil, ctx.Err()
}
func TestRunNodeToolContextTransportAndObservation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client := &cancellableDispatchClient{started: make(chan struct{})}
	original := testTool{name: "custom"}
	done := make(chan error, 1)
	go func() {
		_, err := RunNodeTool(ctx, original, client, "owner", "caller", map[string]any{}, func(_, result map[string]any, err error) {
			if result != nil || !errors.Is(err, context.Canceled) {
				t.Errorf("observation=%v/%v", result, err)
			}
		})
		done <- err
	}()
	<-client.started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

type concurrentDispatchClient struct{ stubClient }

func (*concurrentDispatchClient) Call(_, _ string, args map[string]any) (json.RawMessage, error) {
	return json.Marshal(args)
}
func TestRunNodeToolConcurrentCallerIsolation(t *testing.T) {
	original := &argumentTestTool{testTool: testTool{name: "read_file"}, required: "path"}
	args := map[string]any{"file_path": "/a", "_caller": "untrusted"}
	client := &concurrentDispatchClient{}
	var wg sync.WaitGroup
	for _, caller := range []string{"first", "second", ""} {
		wg.Go(func() {
			result, err := RunNodeTool(context.Background(), original, client, "owner", caller, args, nil)
			if err != nil || result["path"] != "/a" {
				t.Errorf("result=%v err=%v", result, err)
			}
			if caller == "" {
				if _, present := result["_caller"]; present {
					t.Error("untrusted caller forwarded")
				}
			} else if result["_caller"] != caller {
				t.Error("caller crossed")
			}
		})
	}
	wg.Wait()
	if args["_caller"] != "untrusted" || args["path"] != nil {
		t.Fatal("input mutated")
	}
}

func TestNodeToolTransportReadinessBeforeDecoration(t *testing.T) {
	client := &argumentSpyClient{}
	pool := &argumentSpyPool{client: client}
	nt := NewNodeToolWithPool(&argumentTestTool{testTool: testTool{name: "read_file"}, required: "path"}, pool)
	decorated, observed := false, false
	decorate := func(c ToolNodeClient) ToolNodeClient {
		if c != client || !client.ready || client.called {
			t.Fatal("decorator ran before readiness or after call")
		}
		decorated = true
		return c
	}
	observe := func(args, result map[string]any, err error) {
		observed = true
		if !client.called || args["path"] != "/a" || result["ok"] != true || err != nil {
			t.Fatalf("bad observation: %v %v %v", args, result, err)
		}
	}
	ctx := testToolContext{Context: context.Background(), sessionID: "logical"}
	if _, err := nt.RunWithTransport(ctx, map[string]any{}, decorate, observe); err == nil || decorated || observed || pool.method != "" {
		t.Fatal("invalid arguments acquired/decorated transport")
	}
	if _, err := nt.RunWithTransport(ctx, map[string]any{"file_path": "/a"}, decorate, observe); err != nil || !decorated || !observed {
		t.Fatalf("transport hooks not called: %v", err)
	}
}

type failedReadyTransport struct {
	stubClient
	failure error
}

func (c *failedReadyTransport) EnsureReady(string) error { return c.failure }

type failedReadyTransportPool struct {
	spyPool
	client ToolNodeClient
}

func (p *failedReadyTransportPool) GetOrCreate(string) ToolNodeClient { return p.client }
func TestNodeToolTransportReadinessFailureNoHooks(t *testing.T) {
	failure := errors.New("readiness failure")
	nt := NewNodeToolWithPool(testTool{name: "custom"}, &failedReadyTransportPool{client: &failedReadyTransport{failure: failure}})
	_, err := nt.RunWithTransport(testToolContext{Context: context.Background(), sessionID: "logical"}, map[string]any{}, func(c ToolNodeClient) ToolNodeClient { t.Fatal("decorated failed readiness"); return c }, func(_, _ map[string]any, _ error) { t.Fatal("observed undispatched call") })
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
}
