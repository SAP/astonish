package launcher

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/tools"
)

// A separate test binary gives each node the production process-global manager
// namespace. Do not add process-ID ownership filtering here: foreign-ID refusal
// must come from the real dispatcher/manager, exactly as with separate nodes.
// This is disposable local transport evidence, NOT a container or node CLI test.
const processParityCommand = `printf 'ready\n'; while IFS= read -r line; do printf 'ack:%s\n' "$line"; done`
const processParityHelperEnv = "ASTONISH_PROCESS_PARITY_HELPER"

type processParityRequest struct {
	Session, Name string
	Args          map[string]any
	WaitFor       string
}
type processParityResponse struct {
	Result json.RawMessage
	Error  string
}

type processParityNode struct {
	sandbox.ToolNodeClient
	session, root string
	cmd           *exec.Cmd
	input         io.WriteCloser
	output        *json.Decoder
	calls         []string
	stopped       bool
}

func TestChatRuntimeProcessNodeHelper(t *testing.T) {
	if os.Getenv(processParityHelperEnv) != "1" {
		return
	}
	input, output := os.NewFile(3, "requests"), os.NewFile(4, "responses")
	defer input.Close()
	defer output.Close()
	root, owner := os.Getenv("ASTONISH_PROCESS_PARITY_ROOT"), os.Getenv("ASTONISH_PROCESS_PARITY_SESSION")
	if root == "" || owner == "" {
		t.Fatal("helper requires disposable scope")
	}
	// All manager entries belong exclusively to this helper; cleanup never reaches
	// the daemon's or test runner's process manager.
	defer func() {
		tools.CleanupProcessManager()
		for _, sess := range tools.GetProcessManager().List() {
			if sess.IsRunning() {
				t.Errorf("owned process %s survived cleanup", sess.ID)
			}
		}
	}()
	dec, enc := json.NewDecoder(bufio.NewReader(input)), json.NewEncoder(output)
	for {
		var req processParityRequest
		if err := dec.Decode(&req); err != nil {
			if err != io.EOF {
				t.Error(err)
			}
			return
		}
		response := processParityResponse{}
		execute := func() (any, error) {
			if req.Session != owner {
				return nil, fmt.Errorf("foreign node session")
			}
			if req.WaitFor != "" {
				id, _ := req.Args["session_id"].(string)
				sess := tools.GetProcessManager().Get(id)
				if sess == nil {
					return nil, fmt.Errorf("missing owned process")
				}
				deadline := time.Now().Add(10 * time.Second)
				for !strings.Contains(string(sess.Output.Bytes()), req.WaitFor) {
					if !sess.IsRunning() || time.Now().After(deadline) {
						return nil, fmt.Errorf("process handshake missing %q", req.WaitFor)
					}
					runtime.Gosched()
				}
				return map[string]any{"ready": true}, nil
			}
			switch req.Name {
			case "shell_command":
				interactive := req.Args["command"] == processParityCommand && req.Args["background"] == true
				leaseProbe := req.Args["command"] == "printf services-parity-ok" && req.Args["background"] != true
				if (!interactive && !leaseProbe) || req.Args["working_dir"] != root {
					return nil, fmt.Errorf("unsafe fixture command")
				}
			case "write_file", "read_file", "edit_file":
				if req.Args["path"] != filepath.Join(root, "parity.txt") {
					return nil, fmt.Errorf("unsafe fixture path")
				}
			case "process_read", "process_write", "process_list", "process_kill":
				// Deliberately no caller/process ID check: exercise actual semantics.
			default:
				return nil, fmt.Errorf("unexpected fixture operation %q", req.Name)
			}
			return tools.ExecuteTool(context.Background(), req.Name, req.Args, owner)
		}
		result, err := execute()
		if err != nil {
			response.Error = err.Error()
		} else {
			response.Result, err = json.Marshal(result)
			if err != nil {
				response.Error = err.Error()
			}
		}
		if err := enc.Encode(response); err != nil {
			t.Fatal(err)
		}
	}
}

func newProcessParityNode(t *testing.T, session, root string) *processParityNode {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestChatRuntimeProcessNodeHelper$", "-test.timeout=85s")
	cmd.Env = append(os.Environ(), processParityHelperEnv+"=1", "ASTONISH_PROCESS_PARITY_ROOT="+root, "ASTONISH_PROCESS_PARITY_SESSION="+session)
	cmd.ExtraFiles = []*os.File{inR, outW}
	logs := tools.NewRingBuffer(16 * 1024)
	cmd.Stdout, cmd.Stderr = logs, logs
	if err := cmd.Start(); err != nil {
		cancel()
		inR.Close()
		inW.Close()
		outR.Close()
		outW.Close()
		t.Fatal(err)
	}
	inR.Close()
	outW.Close()
	n := &processParityNode{session: session, root: root, cmd: cmd, input: inW, output: json.NewDecoder(outR)}
	t.Cleanup(func() {
		n.stopped = true
		inW.Close() // EOF invokes helper-owned manager cleanup and joins every child.
		if err := cmd.Wait(); err != nil {
			t.Errorf("node helper cleanup/reap: %v; bounded logs: %s", err, logs.Bytes())
		}
		outR.Close()
		cancel()
	})
	return n
}
func (n *processParityNode) BindSession(string) {}
func (n *processParityNode) EnsureReady(id string) error {
	if id != n.session || n.stopped {
		return fmt.Errorf("unavailable node")
	}
	return nil
}
func (n *processParityNode) exchange(req processParityRequest) (json.RawMessage, error) {
	if n.stopped {
		return nil, fmt.Errorf("node transport unavailable")
	}
	if err := json.NewEncoder(n.input).Encode(req); err != nil {
		return nil, err
	}
	var out processParityResponse
	if err := n.output.Decode(&out); err != nil {
		return nil, err
	}
	if out.Error != "" {
		return nil, fmt.Errorf("node: %s", out.Error)
	}
	return out.Result, nil
}
func (n *processParityNode) Call(id, name string, args map[string]any) (json.RawMessage, error) {
	n.calls = append(n.calls, name)
	return n.exchange(processParityRequest{Session: id, Name: name, Args: args})
}
func (n *processParityNode) handshake(t *testing.T, id, want string) {
	t.Helper()
	if _, err := n.exchange(processParityRequest{Session: n.session, Args: map[string]any{"session_id": id}, WaitFor: want}); err != nil {
		t.Fatal(err)
	}
}
