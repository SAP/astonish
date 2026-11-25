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
