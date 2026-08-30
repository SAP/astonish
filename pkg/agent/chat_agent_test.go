package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
	"gopkg.in/yaml.v3"
)

func TestPreProviderRetrievalError(t *testing.T) {
	err := preProviderRetrievalError("knowledge search", 25*time.Millisecond, context.DeadlineExceeded)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error does not wrap deadline: %v", err)
	}
	if !strings.Contains(err.Error(), "pre-provider knowledge search timed out after 25ms") {
		t.Fatalf("unexpected timeout error: %v", err)
	}

	cause := errors.New("search unavailable")
	err = preProviderRetrievalError("tool index search", time.Second, cause)
	if !errors.Is(err, cause) || strings.Contains(err.Error(), "timed out") {
		t.Fatalf("unexpected retrieval error: %v", err)
	}
}

func TestEnqueueImagesFromContent(t *testing.T) {
	t.Parallel()
	ca := &ChatAgent{}
	ca.EnqueueImagesFromContent(&genai.Content{
		Role: "model",
		Parts: []*genai.Part{
			{Text: "caption"},
			{InlineData: &genai.Blob{MIMEType: "image/png", Data: []byte{1, 2, 3}}},
			{InlineData: &genai.Blob{MIMEType: "application/pdf", Data: []byte{4, 5}}},
			{InlineData: &genai.Blob{MIMEType: "image/jpeg", Data: []byte{6, 7}}},
			{InlineData: &genai.Blob{MIMEType: "image/jpg", Data: []byte{8}}},
		},
	})
	imgs := ca.DrainImages()
	if len(imgs) != 3 {
		t.Fatalf("expected 3 images, got %d", len(imgs))
	}
	if imgs[0].Format != "png" || string(imgs[0].Data) != "\x01\x02\x03" {
		t.Errorf("first image: %+v", imgs[0])
	}
	if imgs[1].Format != "jpeg" {
		t.Errorf("second format=%q, want jpeg", imgs[1].Format)
	}
	if imgs[2].Format != "jpeg" {
		t.Errorf("jpg should normalize to jpeg, got %q", imgs[2].Format)
	}
	if len(ca.DrainImages()) != 0 {
		t.Error("second drain should be empty")
	}
}

func TestEnqueueImagesFromContent_Nil(t *testing.T) {
	t.Parallel()
	ca := &ChatAgent{}
	ca.EnqueueImagesFromContent(nil)
	if len(ca.DrainImages()) != 0 {
		t.Error("expected no images from nil content")
	}
}

func TestExtractInputParams_NilTrace(t *testing.T) {
	ca := &ChatAgent{}
	params := ca.extractInputParams(context.Background(), "nodes: []", nil)
	if params != nil {
		t.Errorf("expected nil for nil trace, got %v", params)
	}
}

func TestExtractInputParams_EmptyYAML(t *testing.T) {
	ca := &ChatAgent{}
	trace := NewExecutionTrace("test")
	trace.Finalize()
	params := ca.extractInputParams(context.Background(), "", trace)
	if params != nil {
		t.Errorf("expected nil for empty YAML, got %v", params)
	}
}

func TestExtractInputParams_NoDistiller(t *testing.T) {
	ca := &ChatAgent{}
	trace := NewExecutionTrace("test")
	trace.Finalize()
	// No FlowDistiller set — should return nil gracefully
	params := ca.extractInputParams(context.Background(), `
nodes:
  - name: get_host
    type: input
    prompt: "Enter host:"
    output_model:
      host: str
`, trace)
	if params != nil {
		t.Errorf("expected nil when no distiller, got %v", params)
	}
}

func TestExtractInputParams_NoInputNodes(t *testing.T) {
	ca := &ChatAgent{
		FlowDistiller: &FlowDistiller{
			LLM: func(ctx context.Context, prompt string) (string, error) {
				t.Fatal("LLM should not be called when there are no input nodes")
				return "", nil
			},
		},
	}
	trace := NewExecutionTrace("test")
	trace.Finalize()
	params := ca.extractInputParams(context.Background(), `
nodes:
  - name: do_stuff
    type: llm
    prompt: "Do something"
`, trace)
	if params != nil {
		t.Errorf("expected nil for no input nodes, got %v", params)
	}
}

func TestExtractInputParams_ParsesLLMResponse(t *testing.T) {
	// Mock the LLM to return known parameter values keyed by node name.
	// The flow runner matches -p keys by node name, so extractInputParams
	// must use node names (get_connection_info, get_ssh_user), not field names.
	ca := &ChatAgent{
		FlowDistiller: &FlowDistiller{
			LLM: func(ctx context.Context, prompt string) (string, error) {
				return "get_connection_info=192.168.1.200\nget_ssh_user=root\n", nil
			},
		},
	}

	trace := NewExecutionTrace("show proxmox VMs")
	trace.RecordStep("shell_command", map[string]any{
		"command": `ssh root@192.168.1.200 "qm list"`,
	}, nil, nil)
	trace.Finalize()

	params := ca.extractInputParams(context.Background(), `
nodes:
  - name: get_connection_info
    type: input
    prompt: "Enter IP:"
    output_model:
      server_ip: str
  - name: get_ssh_user
    type: input
    prompt: "Enter user:"
    output_model:
      ssh_user: str
  - name: fetch
    type: llm
    prompt: "SSH as {ssh_user} to {server_ip}"
    tools: true
`, trace)

	if len(params) != 2 {
		t.Fatalf("expected 2 params, got %d: %v", len(params), params)
	}

	paramMap := make(map[string]string)
	for _, p := range params {
		parts := splitFirst(p, "=")
		paramMap[parts[0]] = parts[1]
	}

	if paramMap["get_connection_info"] != "192.168.1.200" {
		t.Errorf("expected get_connection_info=192.168.1.200, got %s", paramMap["get_connection_info"])
	}
	if paramMap["get_ssh_user"] != "root" {
		t.Errorf("expected get_ssh_user=root, got %s", paramMap["get_ssh_user"])
	}
}

func TestExtractInputParams_IgnoresInvalidLLMResponse(t *testing.T) {
	ca := &ChatAgent{
		FlowDistiller: &FlowDistiller{
			LLM: func(ctx context.Context, prompt string) (string, error) {
				// LLM returns some correct and some garbage lines
				return "get_host=10.0.0.1\n# comment\ngarbage line\nunknown_param=nope\n", nil
			},
		},
	}

	trace := NewExecutionTrace("test")
	trace.RecordStep("some_tool", map[string]any{"x": "y"}, nil, nil)
	trace.Finalize()

	params := ca.extractInputParams(context.Background(), `
nodes:
  - name: get_host
    type: input
    prompt: "Enter host:"
  - name: get_port
    type: input
    prompt: "Enter port:"
`, trace)

	if len(params) != 2 {
		t.Fatalf("expected 2 params, got %d: %v", len(params), params)
	}

	paramMap := make(map[string]string)
	for _, p := range params {
		parts := splitFirst(p, "=")
		paramMap[parts[0]] = parts[1]
	}

	if paramMap["get_host"] != "10.0.0.1" {
		t.Errorf("expected get_host=10.0.0.1, got %s", paramMap["get_host"])
	}
	// get_port was not in the LLM response — should fall back to <value>
	if paramMap["get_port"] != "<value>" {
		t.Errorf("expected get_port=<value>, got %s", paramMap["get_port"])
	}
}

func TestExtractInputParams_LLMPromptContainsTrace(t *testing.T) {
	// Verify that the prompt sent to the LLM includes the trace data and output_model fields
	var capturedPrompt string
	ca := &ChatAgent{
		FlowDistiller: &FlowDistiller{
			LLM: func(ctx context.Context, prompt string) (string, error) {
				capturedPrompt = prompt
				return "get_host=10.0.0.1\n", nil
			},
		},
	}

	trace := NewExecutionTrace("connect to my server")
	trace.RecordStep("shell_command", map[string]any{
		"command": "ssh root@10.0.0.1",
	}, nil, nil)
	trace.Finalize()

	ca.extractInputParams(context.Background(), `
nodes:
  - name: get_host
    type: input
    prompt: "Enter host:"
    output_model:
      server_ip: str
`, trace)

	// The prompt should contain the trace tool name and args
	if capturedPrompt == "" {
		t.Fatal("LLM was not called")
	}
	if !containsAll(capturedPrompt, "shell_command", "ssh root@10.0.0.1", "get_host", "connect to my server") {
		t.Errorf("LLM prompt missing expected content:\n%s", capturedPrompt)
	}
	// The prompt should include output_model field names as context
	if !containsAll(capturedPrompt, "server_ip") {
		t.Errorf("LLM prompt should include output_model fields:\n%s", capturedPrompt)
	}
	// The prompt should include conciseness guidance
	if !containsAll(capturedPrompt, "EXACT LITERAL", "NODE NAME") {
		t.Errorf("LLM prompt should include conciseness instructions:\n%s", capturedPrompt)
	}
}

func TestExtractInputParams_OutputModelMultipleFields(t *testing.T) {
	// When an input node has multiple output_model fields, extractInputParams
	// still produces one -p flag keyed by the node name. The output_model
	// field names appear in the LLM prompt as context but not as -p keys.
	var capturedPrompt string
	ca := &ChatAgent{
		FlowDistiller: &FlowDistiller{
			LLM: func(ctx context.Context, prompt string) (string, error) {
				capturedPrompt = prompt
				return "get_connection_info=root@192.168.1.200\n", nil
			},
		},
	}

	trace := NewExecutionTrace("show proxmox VMs")
	trace.RecordStep("shell_command", map[string]any{
		"command": `ssh root@192.168.1.200 "pvesh get /cluster/resources"`,
	}, nil, nil)
	trace.Finalize()

	params := ca.extractInputParams(context.Background(), `
nodes:
  - name: get_connection_info
    type: input
    prompt: "Enter SSH connection details:"
    output_model:
      ssh_user: str
      ssh_ip: str
  - name: fetch
    type: llm
    prompt: "SSH to {ssh_ip}"
    tools: true
`, trace)

	if len(params) != 1 {
		t.Fatalf("expected 1 param, got %d: %v", len(params), params)
	}
	if params[0] != "get_connection_info=root@192.168.1.200" {
		t.Errorf("expected get_connection_info=root@192.168.1.200, got %s", params[0])
	}
	// Prompt should mention the output_model fields as context
	if !containsAll(capturedPrompt, "ssh_user", "ssh_ip") {
		t.Errorf("LLM prompt should include output_model field names:\n%s", capturedPrompt)
	}
}

func TestExtractInputParams_FieldNameFallback(t *testing.T) {
	// When the LLM responds with output_model field names instead of node names,
	// extractInputParams should map them back to the parent node name.
	ca := &ChatAgent{
		FlowDistiller: &FlowDistiller{
			LLM: func(ctx context.Context, prompt string) (string, error) {
				// LLM responds with field names, not node names
				return "os_auth_url=https://identity.example.com/v3\nos_region_name=eu-west-1\n", nil
			},
		},
	}

	trace := NewExecutionTrace("list openstack VMs")
	trace.RecordStep("shell_command", map[string]any{
		"command": "openstack server list --os-auth-url https://identity.example.com/v3 --os-region-name eu-west-1",
	}, nil, nil)
	trace.Finalize()

	params := ca.extractInputParams(context.Background(), `
nodes:
  - name: get_auth_url
    type: input
    prompt: "Enter OpenStack Auth URL:"
    output_model:
      os_auth_url: str
  - name: get_region
    type: input
    prompt: "Enter region:"
    output_model:
      os_region_name: str
  - name: list_vms
    type: llm
    prompt: "List VMs at {os_auth_url} in {os_region_name}"
    tools: true
`, trace)

	if len(params) != 2 {
		t.Fatalf("expected 2 params, got %d: %v", len(params), params)
	}

	paramMap := make(map[string]string)
	for _, p := range params {
		parts := splitFirst(p, "=")
		paramMap[parts[0]] = parts[1]
	}

	// Field name "os_auth_url" should map back to node name "get_auth_url"
	if paramMap["get_auth_url"] != "https://identity.example.com/v3" {
		t.Errorf("expected get_auth_url=https://identity.example.com/v3, got %s", paramMap["get_auth_url"])
	}
	// Field name "os_region_name" should map back to node name "get_region"
	if paramMap["get_region"] != "eu-west-1" {
		t.Errorf("expected get_region=eu-west-1, got %s", paramMap["get_region"])
	}
}

func TestExtractInputParams_SkipsSecretNodes(t *testing.T) {
	// Input nodes that collect secrets (password, token, API key, secret)
	// must not appear in the suggested -p flags.
	ca := &ChatAgent{
		FlowDistiller: &FlowDistiller{
			LLM: func(ctx context.Context, prompt string) (string, error) {
				return "get_host=10.0.0.1\n", nil
			},
		},
	}
	trace := NewExecutionTrace("test")
	trace.RecordStep("shell_command", map[string]any{"command": "ssh 10.0.0.1"}, nil, nil)
	trace.Finalize()

	params := ca.extractInputParams(context.Background(), `
nodes:
  - name: get_host
    type: input
    prompt: "Enter host:"
    output_model:
      host: str
  - name: get_secret
    type: input
    prompt: "Enter your password:"
    output_model:
      user_password: str
  - name: get_token
    type: input
    prompt: "Enter API token:"
    output_model:
      api_token: str
  - name: get_cred_secret
    type: input
    prompt: "Enter the application credential secret:"
    output_model:
      app_credential_secret: str
`, trace)

	// Only get_host should be included — secret nodes are skipped
	if len(params) != 1 {
		t.Fatalf("expected 1 param (secrets filtered), got %d: %v", len(params), params)
	}
	if params[0] != "get_host=10.0.0.1" {
		t.Errorf("expected get_host=10.0.0.1, got %s", params[0])
	}
}

func TestFlowYAML_ParsesInputNodes(t *testing.T) {
	// Verify the YAML parsing correctly identifies input nodes
	yamlStr := `
nodes:
  - name: get_ip
    type: input
    prompt: "Enter IP:"
    output_model:
      ip: str
  - name: process
    type: llm
    prompt: "Process {ip}"
  - name: get_user
    type: input
    prompt: "Enter user:"
    output_model:
      user: str
`
	var flow flowYAML
	if err := yaml.Unmarshal([]byte(yamlStr), &flow); err != nil {
		t.Fatalf("failed to parse YAML: %v", err)
	}

	var inputNames []string
	for _, node := range flow.Nodes {
		if node.Type == "input" {
			inputNames = append(inputNames, node.Name)
		}
	}

	if len(inputNames) != 2 {
		t.Fatalf("expected 2 input nodes, got %d: %v", len(inputNames), inputNames)
	}
	if inputNames[0] != "get_ip" || inputNames[1] != "get_user" {
		t.Errorf("expected [get_ip, get_user], got %v", inputNames)
	}
}

// containsAll checks if s contains all of the given substrings.
func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		found := false
		for i := 0; i <= len(s)-len(sub); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// splitFirst splits s on the first occurrence of sep.
func splitFirst(s, sep string) [2]string {
	idx := len(sep)
	for i := 0; i < len(s)-len(sep)+1; i++ {
		if s[i:i+len(sep)] == sep {
			idx = i
			break
		}
	}
	return [2]string{s[:idx], s[idx+len(sep):]}
}

// --- thinkTagFilter streaming tests ---

// feedText is a test helper that calls Feed and returns only the text.
func feedText(f *thinkTagFilter, chunk string) string {
	text, _ := f.Feed(chunk)
	return text
}

func TestThinkTagFilter_SingleChunkComplete(t *testing.T) {
	// The entire think block arrives in one chunk.
	f := &thinkTagFilter{}
	got, stripped := f.Feed("<think>reasoning here</think>Hello!")
	if got != "Hello!" {
		t.Errorf("got %q, want %q", got, "Hello!")
	}
	if !stripped {
		t.Error("expected stripped=true")
	}
}

func TestThinkTagFilter_StreamedAcrossChunks(t *testing.T) {
	// Simulates the real streaming scenario: tags split across many small chunks.
	f := &thinkTagFilter{}
	chunks := []string{
		"<thi",
		"nk>",
		"The user said ",
		"\"Hi\".",
		" This is a simple",
		" greeting.",
		"</thi",
		"nk>",
		"Hello! ",
		"How can I help?",
	}
	var out strings.Builder
	for _, c := range chunks {
		out.WriteString(feedText(f, c))
	}
	want := "Hello! How can I help?"
	if out.String() != want {
		t.Errorf("got %q, want %q", out.String(), want)
	}
}

func TestThinkTagFilter_MultipleThinkBlocks(t *testing.T) {
	// Multiple think blocks interleaved with visible text.
	f := &thinkTagFilter{}
	chunks := []string{
		"<think>first thought</think>",
		"Visible 1",
		"<think>second thought</think>",
		" Visible 2",
	}
	var out strings.Builder
	for _, c := range chunks {
		out.WriteString(feedText(f, c))
	}
	want := "Visible 1 Visible 2"
	if out.String() != want {
		t.Errorf("got %q, want %q", out.String(), want)
	}
}

func TestThinkTagFilter_ThinkingTag(t *testing.T) {
	// <thinking> variant.
	f := &thinkTagFilter{}
	chunks := []string{
		"<thinking>deep reasoning</thinking>",
		"The answer is 42.",
	}
	var out strings.Builder
	for _, c := range chunks {
		out.WriteString(feedText(f, c))
	}
	want := "The answer is 42."
	if out.String() != want {
		t.Errorf("got %q, want %q", out.String(), want)
	}
}

func TestThinkTagFilter_ThinkingTagStreamed(t *testing.T) {
	// <thinking> tag split across chunks.
	f := &thinkTagFilter{}
	chunks := []string{
		"<thinkin",
		"g>",
		"reasoning",
		"</thinkin",
		"g>",
		"Result",
	}
	var out strings.Builder
	for _, c := range chunks {
		out.WriteString(feedText(f, c))
	}
	want := "Result"
	if out.String() != want {
		t.Errorf("got %q, want %q", out.String(), want)
	}
}

func TestThinkTagFilter_NoThinkTags(t *testing.T) {
	// Normal text without think tags passes through unchanged.
	f := &thinkTagFilter{}
	chunks := []string{"Hello ", "world! ", "How are you?"}
	var out strings.Builder
	for _, c := range chunks {
		out.WriteString(feedText(f, c))
	}
	want := "Hello world! How are you?"
	if out.String() != want {
		t.Errorf("got %q, want %q", out.String(), want)
	}
}

func TestThinkTagFilter_OnlyThinkContent(t *testing.T) {
	// Entire response is inside think tags — output should be empty.
	f := &thinkTagFilter{}
	chunks := []string{
		"<think>",
		"all reasoning, no answer",
		"</think>",
	}
	var out strings.Builder
	for _, c := range chunks {
		out.WriteString(feedText(f, c))
	}
	if out.String() != "" {
		t.Errorf("got %q, want empty", out.String())
	}
}

func TestThinkTagFilter_TagSplitAtEveryByte(t *testing.T) {
	// Worst case: every byte arrives as a separate chunk.
	input := "<think>hidden</think>visible"
	f := &thinkTagFilter{}
	var out strings.Builder
	for _, b := range []byte(input) {
		out.WriteString(feedText(f, string(b)))
	}
	want := "visible"
	if out.String() != want {
		t.Errorf("got %q, want %q", out.String(), want)
	}
}

func TestThinkTagFilter_TextBeforeThinkTag(t *testing.T) {
	// Text appears before the think tag.
	f := &thinkTagFilter{}
	chunks := []string{
		"Sure! ",
		"<think>let me reason</think>",
		"Here is the answer.",
	}
	var out strings.Builder
	for _, c := range chunks {
		out.WriteString(feedText(f, c))
	}
	want := "Sure! Here is the answer."
	if out.String() != want {
		t.Errorf("got %q, want %q", out.String(), want)
	}
}

func TestThinkTagFilter_NemotronExample(t *testing.T) {
	// Simulates the real Nemotron output from the issue.
	f := &thinkTagFilter{}
	chunks := []string{
		"<think>",
		"We need to produce a market report ",
		"for today's date.",
		"\n\n",
		"</think>",
		"\n",
		"Here's today's stock market recap",
	}
	var out strings.Builder
	for _, c := range chunks {
		out.WriteString(feedText(f, c))
	}
	want := "\nHere's today's stock market recap"
	if out.String() != want {
		t.Errorf("got %q, want %q", out.String(), want)
	}
}

func TestThinkTagFilter_PartialOpenTagNotConsumed(t *testing.T) {
	// A '<' that isn't part of a think tag should still be emitted.
	f := &thinkTagFilter{}
	got, stripped := f.Feed("a < b and c > d")
	if got != "a < b and c > d" {
		t.Errorf("got %q, want %q", got, "a < b and c > d")
	}
	if stripped {
		t.Error("expected stripped=false for text without think tags")
	}
}

func TestThinkTagFilter_AngleBracketInNormalText(t *testing.T) {
	// HTML-like content that isn't a think tag.
	f := &thinkTagFilter{}
	chunks := []string{"Use <b>", "bold</b>", " text"}
	var out strings.Builder
	for _, c := range chunks {
		out.WriteString(feedText(f, c))
	}
	want := "Use <b>bold</b> text"
	if out.String() != want {
		t.Errorf("got %q, want %q", out.String(), want)
	}
}

func TestFilterEventThinkContent_PreservesThoughtParts(t *testing.T) {
	// Parts with Thought=true should be preserved (not dropped) so they
	// remain in session history for providers like DeepSeek that require
	// reasoning_content to be sent back in subsequent requests.
	f := &thinkTagFilter{}
	event := &session.Event{}
	event.LLMResponse.Content = &genai.Content{
		Role: "model",
		Parts: []*genai.Part{
			{Text: "thinking internally", Thought: true},
			{Text: "visible answer"},
		},
	}
	filterEventThinkContent(f, event)
	if len(event.LLMResponse.Content.Parts) != 2 {
		t.Fatalf("expected 2 parts (thought preserved), got %d", len(event.LLMResponse.Content.Parts))
	}
	if !event.LLMResponse.Content.Parts[0].Thought {
		t.Error("first part should still be Thought=true")
	}
	if event.LLMResponse.Content.Parts[0].Text != "thinking internally" {
		t.Errorf("thought text: got %q, want %q", event.LLMResponse.Content.Parts[0].Text, "thinking internally")
	}
	if event.LLMResponse.Content.Parts[1].Text != "visible answer" {
		t.Errorf("content text: got %q, want %q", event.LLMResponse.Content.Parts[1].Text, "visible answer")
	}
}

func TestFilterEventThinkContent_NilEvent(t *testing.T) {
	// Should not panic on nil.
	f := &thinkTagFilter{}
	filterEventThinkContent(f, nil)
}

func TestFilterEventThinkContent_NilContent(t *testing.T) {
	// Should not panic when content is nil.
	f := &thinkTagFilter{}
	event := &session.Event{}
	filterEventThinkContent(f, event)
}

func TestFilterEventThinkContent_WhitespaceOnlyAfterStrip(t *testing.T) {
	// When think tags are stripped and only whitespace remains, the part
	// should be dropped entirely — not yielded as an empty message.
	f := &thinkTagFilter{}

	// Simulate streaming: first chunk opens think block
	event1 := &session.Event{}
	event1.LLMResponse.Content = &genai.Content{
		Role:  "model",
		Parts: []*genai.Part{{Text: "<think>reasoning"}},
	}
	filterEventThinkContent(f, event1)
	if len(event1.LLMResponse.Content.Parts) != 0 {
		t.Errorf("expected 0 parts during think block, got %d", len(event1.LLMResponse.Content.Parts))
	}

	// Second chunk closes think block with trailing whitespace
	event2 := &session.Event{}
	event2.LLMResponse.Content = &genai.Content{
		Role:  "model",
		Parts: []*genai.Part{{Text: "</think>\n\n"}},
	}
	filterEventThinkContent(f, event2)
	if len(event2.LLMResponse.Content.Parts) != 0 {
		t.Errorf("expected 0 parts for whitespace-only remnant, got %d", len(event2.LLMResponse.Content.Parts))
	}

	// Third chunk is real content — should pass through
	event3 := &session.Event{}
	event3.LLMResponse.Content = &genai.Content{
		Role:  "model",
		Parts: []*genai.Part{{Text: "Hello!"}},
	}
	filterEventThinkContent(f, event3)
	if len(event3.LLMResponse.Content.Parts) != 1 {
		t.Fatalf("expected 1 part for real content, got %d", len(event3.LLMResponse.Content.Parts))
	}
	if event3.LLMResponse.Content.Parts[0].Text != "Hello!" {
		t.Errorf("got %q, want %q", event3.LLMResponse.Content.Parts[0].Text, "Hello!")
	}
}

func TestFilterEventThinkContent_ToolCallPartPreserved(t *testing.T) {
	// Parts with FunctionCall should pass through even when adjacent to
	// whitespace-only text parts.
	f := &thinkTagFilter{}
	event := &session.Event{}
	event.LLMResponse.Content = &genai.Content{
		Role: "model",
		Parts: []*genai.Part{
			{Text: "\n\n"},
			{FunctionCall: &genai.FunctionCall{Name: "shell_command", Args: map[string]any{"command": "ls"}}},
		},
	}
	filterEventThinkContent(f, event)
	// Both parts should be preserved: the whitespace is legitimate (no think
	// tags were stripped) and the function call is a non-text part.
	if len(event.LLMResponse.Content.Parts) != 2 {
		t.Fatalf("expected 2 parts (whitespace + function call), got %d", len(event.LLMResponse.Content.Parts))
	}
	if event.LLMResponse.Content.Parts[0].Text != "\n\n" {
		t.Errorf("first part text = %q, want %q", event.LLMResponse.Content.Parts[0].Text, "\n\n")
	}
	if event.LLMResponse.Content.Parts[1].FunctionCall == nil {
		t.Error("expected function call part to be preserved")
	}
}

func TestFilterEventThinkContent_WhitespaceAfterThinkBeforeToolCall(t *testing.T) {
	// Whitespace-only text parts that are remnants of think-tag stripping
	// should be dropped, while the adjacent function call part is preserved.
	f := &thinkTagFilter{}

	// First event: open a think block
	event1 := &session.Event{}
	event1.LLMResponse.Content = &genai.Content{
		Role:  "model",
		Parts: []*genai.Part{{Text: "<think>reasoning</think>"}},
	}
	filterEventThinkContent(f, event1)

	// Second event: whitespace remnant + tool call
	event2 := &session.Event{}
	event2.LLMResponse.Content = &genai.Content{
		Role: "model",
		Parts: []*genai.Part{
			{Text: "\n\n"},
			{FunctionCall: &genai.FunctionCall{Name: "shell_command", Args: map[string]any{"command": "ls"}}},
		},
	}
	filterEventThinkContent(f, event2)
	// The whitespace is NOT a remnant here because f.inside is already false
	// after the first event fully closed the think block. The "\n\n" in event2
	// passes through Feed() without any stripping, so stripped=false and
	// the whitespace is preserved.
	if len(event2.LLMResponse.Content.Parts) != 2 {
		t.Fatalf("expected 2 parts, got %d", len(event2.LLMResponse.Content.Parts))
	}
}

func TestFilterEventThinkContent_WhitespacePreservedInNormalStreaming(t *testing.T) {
	// Whitespace-only chunks in normal streaming (no think tags at all)
	// must be preserved — they carry markdown formatting.
	f := &thinkTagFilter{}

	event := &session.Event{}
	event.LLMResponse.Content = &genai.Content{
		Role:  "model",
		Parts: []*genai.Part{{Text: "\n\n"}},
	}
	filterEventThinkContent(f, event)
	if len(event.LLMResponse.Content.Parts) != 1 {
		t.Fatalf("expected 1 part (whitespace preserved), got %d", len(event.LLMResponse.Content.Parts))
	}
	if event.LLMResponse.Content.Parts[0].Text != "\n\n" {
		t.Errorf("got %q, want %q", event.LLMResponse.Content.Parts[0].Text, "\n\n")
	}
}

// --- Unknown tool error recovery tests ---

func TestIsUnknownToolError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"unrelated error", fmt.Errorf("connection refused"), false},
		{"unknown tool error", fmt.Errorf("unknown tool: \"exec\""), true},
		{"unknown tool with context", fmt.Errorf("handleFunctionCalls: unknown tool: \"foobar\""), true},
		{"similar but different", fmt.Errorf("tool not found"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isUnknownToolError(tt.err)
			if got != tt.want {
				t.Errorf("isUnknownToolError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestBuildUnknownToolResponse_SingleCall(t *testing.T) {
	calls := []*genai.FunctionCall{
		{ID: "call_123", Name: "exec", Args: map[string]any{"command": "ls"}},
	}
	tools := mockTools("shell_command", "read_file", "write_file")

	ev := buildUnknownToolResponse(calls, tools, nil)

	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	if ev.LLMResponse.Content == nil {
		t.Fatal("expected non-nil content")
	}
	if ev.LLMResponse.Content.Role != "user" {
		t.Errorf("expected role 'user', got %q", ev.LLMResponse.Content.Role)
	}
	if len(ev.LLMResponse.Content.Parts) != 1 {
		t.Fatalf("expected 1 part, got %d", len(ev.LLMResponse.Content.Parts))
	}

	fr := ev.LLMResponse.Content.Parts[0].FunctionResponse
	if fr == nil {
		t.Fatal("expected FunctionResponse part")
	}
	if fr.ID != "call_123" {
		t.Errorf("expected ID 'call_123', got %q", fr.ID)
	}
	if fr.Name != "exec" {
		t.Errorf("expected Name 'exec', got %q", fr.Name)
	}

	errMsg, ok := fr.Response["error"].(string)
	if !ok {
		t.Fatal("expected 'error' string in response")
	}
	if !strings.Contains(errMsg, "Unknown tool \"exec\"") {
		t.Errorf("error message should mention the unknown tool, got: %s", errMsg)
	}
	if !strings.Contains(errMsg, "shell_command") {
		t.Errorf("error message should hint at available tools, got: %s", errMsg)
	}
}

func TestBuildUnknownToolResponse_MultipleCalls(t *testing.T) {
	calls := []*genai.FunctionCall{
		{ID: "call_1", Name: "exec", Args: map[string]any{"command": "ls"}},
		{ID: "call_2", Name: "run", Args: map[string]any{"script": "test.sh"}},
	}
	tools := mockTools("shell_command", "read_file")

	ev := buildUnknownToolResponse(calls, tools, nil)

	if len(ev.LLMResponse.Content.Parts) != 2 {
		t.Fatalf("expected 2 parts for 2 calls, got %d", len(ev.LLMResponse.Content.Parts))
	}

	// Verify each response matches its call
	for i, part := range ev.LLMResponse.Content.Parts {
		fr := part.FunctionResponse
		if fr == nil {
			t.Fatalf("part %d: expected FunctionResponse", i)
		}
		if fr.ID != calls[i].ID {
			t.Errorf("part %d: expected ID %q, got %q", i, calls[i].ID, fr.ID)
		}
		if fr.Name != calls[i].Name {
			t.Errorf("part %d: expected Name %q, got %q", i, calls[i].Name, fr.Name)
		}
	}
}

func TestBuildUnknownToolResponse_IncludesToolsetHint(t *testing.T) {
	calls := []*genai.FunctionCall{
		{ID: "call_1", Name: "exec"},
	}
	tools := mockTools("shell_command")

	// Create a mock toolset
	ts := &mockToolset{name: "mcp_server"}

	ev := buildUnknownToolResponse(calls, tools, []tool.Toolset{ts})

	fr := ev.LLMResponse.Content.Parts[0].FunctionResponse
	errMsg := fr.Response["error"].(string)
	if !strings.Contains(errMsg, "mcp_server.*") {
		t.Errorf("expected toolset hint 'mcp_server.*' in message, got: %s", errMsg)
	}
}

// mockToolset implements tool.Toolset for testing.
type mockToolset struct {
	name string
}

func (m *mockToolset) Name() string                                          { return m.name }
func (m *mockToolset) Tools(_ adkagent.ReadonlyContext) ([]tool.Tool, error) { return nil, nil }

func TestParseMCPToolRef(t *testing.T) {
	tests := []struct {
		in        string
		wantGroup string
		wantTool  string
		wantOK    bool
	}{
		{"send_email", "", "", false},
		{"mcp:email", "mcp:email", "", true},
		{"mcp:email/send_email", "mcp:email", "send_email", true},
		{"mcp:", "", "", false},
		{"mcp:/send", "", "", false},
	}
	for _, tt := range tests {
		g, tool, ok := parseMCPToolRef(tt.in)
		if ok != tt.wantOK || g != tt.wantGroup || tool != tt.wantTool {
			t.Errorf("parseMCPToolRef(%q) = (%q,%q,%v), want (%q,%q,%v)",
				tt.in, g, tool, ok, tt.wantGroup, tt.wantTool, tt.wantOK)
		}
	}
}
