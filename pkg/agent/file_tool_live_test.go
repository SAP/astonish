package agent

// This is a live behavioral harness, not a mocked sandbox unit test.
// Opt in explicitly:
//   ASTONISH_FILE_TOOL_LIVE=1 go test ./pkg/agent -run '^TestFileToolLiveNativeDispatch$' -count=1 -v -timeout=20m
// Optional ASTONISH_FILE_TOOL_LIVE_IMAGE selects an ALREADY LOCAL Linux image
// with /bin/sh, sleep, mkdir, printf, and cat (default: Astonish sandbox base).
// The harness builds the current checkout's binary for that image architecture.
// It never contacts a running Astonish daemon, mounts a session volume, pulls an
// image, or deletes resources other than its own random container and TempDir.
// The real bridge and NodeTool wrappers run in this test process; only their
// ToolNodeClient transport is test-owned, using Docker exec + real node NDJSON.
// This proves native dispatch and the shared artifact capture boundary, NOT the
// backend OverlayFS lifecycle, authenticated HTTP delivery, restore, or UI.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"

	"github.com/SAP/astonish/pkg/sandbox"
)

// Each call launches the actual current-revision astonish node command and
// verifies its ready message and correlated response. No tool result is mocked.
type fileLiveNodeClient struct {
	ctx                  context.Context
	container, sessionID string
	readyCalls, calls    int
}

func (c *fileLiveNodeClient) BindSession(string) {}
func (c *fileLiveNodeClient) EnsureReady(sessionID string) error {
	c.readyCalls++
	if sessionID != c.sessionID {
		return fmt.Errorf("unexpected session %q", sessionID)
	}
	out, err := exec.CommandContext(c.ctx, "docker", "inspect", "--format", "{{.State.Running}}", c.container).Output()
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(out)) != "true" {
		return fmt.Errorf("disposable sandbox is not running")
	}
	return nil
}
func (c *fileLiveNodeClient) Call(sessionID, name string, args map[string]interface{}) (json.RawMessage, error) {
	if sessionID != c.sessionID {
		return nil, fmt.Errorf("unexpected session %q", sessionID)
	}
	c.calls++
	id := strconv.Itoa(c.calls)
	request, err := json.Marshal(map[string]any{"id": id, "tool": name, "args": args})
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(c.ctx, "docker", "exec", "-i", c.container, "/live-bin/astonish", "node")
	cmd.Stdin = bytes.NewReader(append(request, '\n'))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("node process: %w: %s", err, stderr.String())
	}
	decoder := json.NewDecoder(bytes.NewReader(out))
	var ready struct {
		Ready bool `json:"ready"`
	}
	if err := decoder.Decode(&ready); err != nil || !ready.Ready {
		return nil, fmt.Errorf("missing node readiness: %s", out)
	}
	var response struct {
		ID     string          `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  string          `json:"error"`
	}
	if err := decoder.Decode(&response); err != nil {
		return nil, fmt.Errorf("decode node response: %w", err)
	}
	if response.ID != id {
		return nil, fmt.Errorf("uncorrelated node response: %q", response.ID)
	}
	if response.Error != "" {
		return nil, fmt.Errorf("%s", response.Error)
	}
	return response.Result, nil
}

// The descriptors duplicate only the file argument contracts: pkg/tools imports
// pkg/agent, so importing its constructors here creates a cycle. Local execution
// always fails; all actual file handlers are the production node's handlers.
type fileLiveWriteArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}
type fileLiveEditArgs struct {
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	Regex      bool   `json:"regex,omitempty"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}
type fileLiveReadArgs struct {
	Path  string `json:"path"`
	Force bool   `json:"force,omitempty"`
}
type fileLiveLoadArgs struct {
	Path  string `json:"path"`
	Title string `json:"title,omitempty"`
}

func fileLiveRejectLocal[T any](_ tool.Context, _ T) (map[string]any, error) {
	return nil, fmt.Errorf("live harness forbids host file execution")
}

func TestFileToolLiveNativeDispatch(t *testing.T) {
	if os.Getenv("ASTONISH_FILE_TOOL_LIVE") != "1" {
		t.Skip("opt in with ASTONISH_FILE_TOOL_LIVE=1; requires local Docker image and Go compiler")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Minute)
	defer cancel()
	command := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, args[0], args[1:]...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}
	image := os.Getenv("ASTONISH_FILE_TOOL_LIVE_IMAGE")
	if image == "" {
		image = "ghcr.io/sap/astonish-sandbox-base:latest"
	}
	arch := strings.TrimSpace(command("docker", "image", "inspect", "--format", "{{.Architecture}}", image))
	if arch != "amd64" && arch != "arm64" {
		t.Fatalf("unsupported image architecture %q", arch)
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	buildDir := t.TempDir()
	binary := filepath.Join(buildDir, "astonish")
	t.Logf("building current checkout node: GOOS=linux GOARCH=%s CGO_ENABLED=0 go build -o %s .", arch, binary)
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, ".")
	build.Dir = root
	build.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+arch, "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("current node build: %v\n%s", err, out)
	}
	if info, err := os.Stat(binary); err != nil || info.Size() == 0 {
		t.Fatalf("built binary missing/empty: %v", err)
	}

	id := uuid.NewString()
	name := "astonish-file-live-" + id
	// Register cleanup before creation; exact random name only, never prune.
	t.Cleanup(func() {
		cleanupCtx, done := context.WithTimeout(context.Background(), 30*time.Second)
		defer done()
		out, err := exec.CommandContext(cleanupCtx, "docker", "rm", "-f", name).CombinedOutput()
		if err != nil {
			t.Errorf("cleanup disposable container %s: %v: %s", name, err, out)
		}
	})
	command("docker", "run", "--pull=never", "-d", "--name", name,
		"--label", "astonish.acceptance=file-tool-live", "--network", "none",
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--read-only",
		"--tmpfs", "/tmp:rw,nosuid,nodev", "--tmpfs", "/root:rw,nosuid,nodev",
		"--tmpfs", "/live-bin:rw,exec,nosuid,nodev",
		"--workdir", "/tmp", "--entrypoint", "/bin/sh", image, "-c", "exec sleep 1200")
	// Copy through the Docker API: the daemon may be remote and cannot bind
	// mount the client's temporary build directory.
	binaryFile, err := os.Open(binary)
	if err != nil {
		t.Fatal(err)
	}
	copyBinary := exec.CommandContext(ctx, "docker", "exec", "-i", name, "/bin/sh", "-c", "cat > /live-bin/astonish && chmod 0555 /live-bin/astonish")
	copyBinary.Stdin = binaryFile
	copyOutput, copyErr := copyBinary.CombinedOutput()
	binaryFile.Close()
	if copyErr != nil {
		t.Fatalf("copy node into disposable tmpfs: %v: %s", copyErr, copyOutput)
	}
	t.Logf("isolated container: %s; image=%s; %s", name, image,
		strings.TrimSpace(command("docker", "inspect", "--format", "running={{.State.Running}} network={{.HostConfig.NetworkMode}} read_only={{.HostConfig.ReadonlyRootfs}}", name)))
	client := &fileLiveNodeClient{ctx: ctx, container: name, sessionID: id}
	pool := sandbox.NewSingleClientPool(client, nil)
	defer pool.Cleanup()
	write, err := functiontool.New(functiontool.Config{Name: "write_file", Description: "Write a file"}, fileLiveRejectLocal[fileLiveWriteArgs])
	if err != nil {
		t.Fatal(err)
	}
	edit, err := functiontool.New(functiontool.Config{Name: "edit_file", Description: "Edit a file"}, fileLiveRejectLocal[fileLiveEditArgs])
	if err != nil {
		t.Fatal(err)
	}
	read, err := functiontool.New(functiontool.Config{Name: "read_file", Description: "Read a file"}, fileLiveRejectLocal[fileLiveReadArgs])
	if err != nil {
		t.Fatal(err)
	}
	load, err := functiontool.New(functiontool.Config{Name: "load_file", Description: "Publish a file"}, fileLiveRejectLocal[fileLiveLoadArgs])
	if err != nil {
		t.Fatal(err)
	}
	wrapped := sandbox.WrapToolsWithPool([]tool.Tool{write, edit, read, load}, pool)
	for _, candidate := range wrapped {
		if _, ok := candidate.(*sandbox.NodeTool); !ok {
			t.Fatalf("%s has no production sandbox wrapper", candidate.Name())
		}
	}
	idx := syncTestToolIndex(t, &ToolGroup{Name: "core", Tools: wrapped})
	bridge, err := NewProgressiveToolBridge(idx)
	if err != nil {
		t.Fatal(err)
	}
	runner := bridge[1].(runnableDeferredTool)
	toolCtx := &minimalReadonlyContext{Context: ctx, sessionID: id}
	chat := &ChatAgent{ToolIndex: idx}
	// Use the same logical-call resolution + shared output finalizer used by
	// native callbacks. This is not a full ChatAgent/ChatRunner LLM loop.
	run := func(envelope map[string]any) (map[string]any, error) {
		logical, args := chat.effectiveToolCall(toolCtx, bridge[1], envelope)
		output, err := runner.Run(toolCtx, envelope)
		return chat.finalizeProgressiveToolOutput(logical, args, output, err), err
	}
	invoke := func(name string, args map[string]any) map[string]any {
		t.Helper()
		out, err := run(map[string]any{"name": name, "arguments": args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return out
	}
	base := "/tmp/astonish-file-live-" + id
	if _, err := os.Lstat(base); !os.IsNotExist(err) {
		t.Fatalf("host fixture path must not exist: %v", err)
	}
	t.Cleanup(func() {
		if _, err := os.Lstat(base); !os.IsNotExist(err) {
			t.Errorf("unexpected host filesystem mutation at %s: %v", base, err)
		}
	})
	writePath := base + "/hello.txt"
	_, err = run(map[string]any{"name": "write_file", "arguments": map[string]any{"unknown_path": writePath, "content": "hello\n"}})
	if err == nil || !strings.Contains(err.Error(), "path") {
		t.Fatalf("wrong-key write must identify path: %v", err)
	}
	for _, discovery := range []string{"describe_tools", "search_tools"} {
		_, err := run(map[string]any{"name": discovery, "arguments": map[string]any{"name": "write_file", "arguments": map[string]any{"file_path": writePath, "content": "must not write"}}})
		if err == nil {
			t.Fatalf("nested %s must be rejected", discovery)
		}
	}
	if client.readyCalls != 0 || client.calls != 0 {
		t.Fatalf("malformed/discovery calls reached sandbox: ready=%d calls=%d", client.readyCalls, client.calls)
	}
	if files := chat.DrainFiles(); len(files) != 0 {
		t.Fatalf("rejected calls published artifacts: %+v", files)
	}
	command("docker", "exec", name, "test", "!", "-e", base)
	for _, toolName := range []string{"write_file", "edit_file", "read_file", "load_file"} {
		described, err := bridge[0].(runnableDeferredTool).Run(toolCtx, map[string]any{"names": []string{toolName}})
		if err != nil {
			t.Fatal(err)
		}
		schemaJSON, err := json.Marshal(described)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(schemaJSON, []byte(`"path"`)) || bytes.Contains(schemaJSON, []byte("file_path")) || bytes.Contains(schemaJSON, []byte(`"delivery"`)) {
			t.Fatalf("unexpected %s discovery contract: %s", toolName, schemaJSON)
		}
	}
	t.Log("PASS malformed write/discovery: no readiness, node call, file, or artifact; each direct schema advertises path and no file_path or delivery")

	sequenceStart := client.calls
	invoke("write_file", map[string]any{"path": writePath, "content": "hello\n"})
	if got := command("docker", "exec", name, "cat", writePath); got != "hello\n" {
		t.Fatalf("sandbox bytes=%q", got)
	}
	if files := chat.DrainFiles(); len(files) != 1 || files[0].Path != writePath || files[0].ToolName != "write_file" {
		t.Fatalf("write artifacts=%+v", files)
	}
	const editedContent = "hello world!\n"
	edited := invoke("edit_file", map[string]any{"path": writePath, "old_string": "hello", "new_string": "hello world!"})
	if edited["success"] != true || edited["path"] != writePath || edited["replacements"] != float64(1) {
		t.Fatalf("edit result=%+v", edited)
	}
	if got := command("docker", "exec", name, "cat", writePath); got != editedContent {
		t.Fatalf("edited sandbox bytes=%q", got)
	}
	if files := chat.DrainFiles(); len(files) != 1 || files[0].Path != writePath || files[0].ToolName != "edit_file" {
		t.Fatalf("edit artifacts=%+v", files)
	}
	readBack := invoke("read_file", map[string]any{"path": writePath})
	if !strings.Contains(fmt.Sprint(readBack["content"]), "hello world!") {
		t.Fatalf("edited read content missing: %+v", readBack)
	}
	if files := chat.DrainFiles(); len(files) != 0 {
		t.Fatalf("edited read published: %+v", files)
	}
	published := invoke("load_file", map[string]any{"path": writePath})
	publishedJSON, err := json.Marshal(published)
	if err != nil {
		t.Fatal(err)
	}
	if published["path"] != writePath || published["size_bytes"] != float64(len(editedContent)) || published["total_lines"] != float64(1) {
		t.Fatalf("edited load metadata=%s", publishedJSON)
	}
	if published["content"] != nil || bytes.Contains(publishedJSON, []byte("hello world!")) {
		t.Fatalf("edited load leaked content: %s", publishedJSON)
	}
	if files := chat.DrainFiles(); len(files) != 1 || files[0].Path != writePath || files[0].ToolName != "load_file" {
		t.Fatalf("edited load artifacts=%+v", files)
	}
	if client.calls-sequenceStart != 4 {
		t.Fatalf("canonical sequence must dispatch exactly once per tool: calls=%d", client.calls-sequenceStart)
	}
	t.Logf("PASS canonical path write/edit/read/load on first attempt: hello -> hello world!; exact bytes, isolated artifacts, metadata=%s", publishedJSON)

	legacyPath := base + "/legacy.txt"
	invoke("write_file", map[string]any{"file_path": legacyPath, "content": "legacy compatibility\n"})
	if got := command("docker", "exec", name, "cat", legacyPath); got != "legacy compatibility\n" {
		t.Fatalf("legacy sandbox bytes=%q", got)
	}
	if files := chat.DrainFiles(); len(files) != 1 || files[0].Path != legacyPath || files[0].ToolName != "write_file" {
		t.Fatalf("legacy write artifacts=%+v", files)
	}
	t.Log("PASS legacy file_path write: exact sandbox bytes and one write_file artifact")
	// Seed a separate file without write_file so read cannot inherit an artifact.
	fixture := base + "/publish-only.txt"
	const fixtureContent = "sandbox-only-publication-fixture\nsecond line\n"
	command("docker", "exec", name, "/bin/sh", "-c", `printf '%s' "$1" > "$2"`, "fixture", fixtureContent, fixture)
	readResult := invoke("read_file", map[string]any{"path": fixture, "force": true})
	if !strings.Contains(fmt.Sprint(readResult["content"]), "sandbox-only-publication-fixture") {
		t.Fatalf("read content missing: %+v", readResult)
	}
	if files := chat.DrainFiles(); len(files) != 0 {
		t.Fatalf("read_file published: %+v", files)
	}
	loaded := invoke("load_file", map[string]any{"path": fixture, "title": "Live sandbox fixture"})
	metadata, _ := json.Marshal(loaded)
	if bytes.Contains(metadata, []byte("sandbox-only-publication-fixture")) || loaded["content"] != nil {
		t.Fatalf("load leaked content: %s", metadata)
	}
	if loaded["path"] != fixture || loaded["size_bytes"] != float64(len(fixtureContent)) || loaded["total_lines"] != float64(2) {
		t.Fatalf("load metadata=%s", metadata)
	}
	if files := chat.DrainFiles(); len(files) != 1 || files[0].Path != fixture || files[0].ToolName != "load_file" {
		t.Fatalf("load artifacts=%+v", files)
	}
	t.Logf("PASS sandbox write/read/load: exact bytes; read has no artifact; load metadata=%s and one load_file artifact", metadata)

	_, err = run(map[string]any{"name": "load_file", "arguments": map[string]any{"path": base + "/missing.txt"}})
	if err == nil {
		t.Fatal("missing load must fail")
	}
	if files := chat.DrainFiles(); len(files) != 0 {
		t.Fatalf("missing file published: %+v", files)
	}
	empty := base + "/empty.txt"
	invoke("write_file", map[string]any{"path": empty, "content": ""})
	chat.DrainFiles()
	emptyResult := invoke("load_file", map[string]any{"path": empty})
	if emptyResult["path"] != empty || emptyResult["total_lines"] != float64(0) {
		t.Fatalf("empty metadata=%+v", emptyResult)
	}
	if files := chat.DrainFiles(); len(files) != 1 || files[0].Path != empty {
		t.Fatalf("empty file artifacts=%+v", files)
	}
	if _, err := os.Lstat(base); !os.IsNotExist(err) {
		t.Fatalf("host path was written: %v", err)
	}
	t.Logf("PASS missing/empty publication states; host path absent; %d real NDJSON node calls; cleanup only %s", client.calls, name)
}
