package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// This is real per-call node execution, not a mocked transport; it intentionally
// does not certify session admission, SQL barriers, provisioning or cold restore.
func TestCompiledKnowledgeNodeDockerNoPython(t *testing.T) {
	binary := os.Getenv("ASTONISH_TEST_WORKSPACE_BINARY")
	if binary == "" {
		t.Skip("explicit freshly built Linux binary required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	name := "astonish-knowledge-node-" + uuid.NewString()
	run := func(args ...string) []byte {
		t.Helper()
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %v: %s %v", args, out, err)
		}
		return out
	}
	run("container", "create", "--pull=never", "--name="+name, "--network=none", "debian:bookworm-slim", "/bin/sleep", "120")
	defer exec.Command("docker", "container", "rm", "-f", name).Run()
	run("cp", binary, name+":/usr/local/bin/astonish-host")
	run("container", "start", name)
	run("exec", name, "/bin/sh", "-ec", "chmod 0755 /usr/local/bin/astonish-host; mkdir -p "+botPrivateMount+" "+botHomeMount+"; ! command -v python3; ! command -v python")
	db := &DockerBackend{cfg: Config{ContainerRuntimePath: "docker"}}
	checks := 0
	live := sandbox.WithBotHomeWorkspaceCommitGuard(ctx, func(context.Context) error { checks++; return nil })
	call := func(op string, record any, out any) error {
		if !sandbox.IsCompiledWorkspaceOperation(op) {
			return errors.New("knowledge routed to Python")
		}
		raw, err := json.Marshal(map[string]any{"op": op, "binding": "test-binding", "record": record})
		if err != nil {
			return err
		}
		cmd := db.botCompiledWorkspaceCommand(live, name)
		input, approval, err := os.Pipe()
		if err != nil {
			return err
		}
		defer input.Close()
		defer approval.Close()
		cmd.Stdin = input
		var result, stderr bytes.Buffer
		control := &sandbox.WorkspaceCommitStream{Context: live, Output: &result, Approve: approval}
		cmd.Stdout, cmd.Stderr = control, &stderr
		written := make(chan error, 1)
		go func() { _, err := approval.Write(append(raw, '\n')); written <- err }()
		err = cmd.Run()
		input.Close()
		approval.Close()
		writeErr := <-written
		if err != nil {
			return fmt.Errorf("%w: %s", err, stderr.String())
		}
		if writeErr != nil {
			return writeErr
		}
		if err = control.Finish(); err != nil {
			return err
		}
		return json.Unmarshal(result.Bytes(), out)
	}
	_, r := knowledgeFixture(&sandbox.BotHomeBinding{Scope: sandbox.BotHomeScope{BotID: uuid.NewString()}})
	var rows []store.BotKnowledgeFileSummary
	if err := call("knowledge-snapshot", nil, &rows); err != nil || len(rows) != 0 {
		t.Fatalf("empty snapshot: %v %+v", err, rows)
	}
	var receipt, retry []store.BotKnowledgeFileReadback
	if err := call("knowledge-publish", r, &receipt); err != nil {
		t.Fatal(err)
	}
	if err := call("knowledge-verify", r, &retry); err != nil || !reflect.DeepEqual(receipt, retry) {
		t.Fatalf("verify: %v", err)
	}
	if err := call("knowledge-publish", r, &retry); err != nil || !reflect.DeepEqual(receipt, retry) {
		t.Fatalf("retry: %v", err)
	}
	if err := call("knowledge-snapshot", nil, &rows); err != nil || len(rows) != 2 || rows[0].Summary != r.Decision.Proposals[0].Content {
		t.Fatalf("snapshot: %v %+v", err, rows)
	}
	if checks != 3 {
		t.Fatalf("host barriers: %d", checks)
	}
	r.ID = uuid.New()
	for i := range r.Decision.Proposals {
		r.Decision.Proposals[i].ExpectedDigest = rows[i].Digest
		r.Decision.Proposals[i].Content = "# replaced\n"
	}
	if err := call("knowledge-publish", r, &receipt); err != nil {
		t.Fatal(err)
	}
	if err := call("knowledge-verify", r, &retry); err != nil {
		t.Fatal(err)
	}
	t.Log("real compiled node: empty snapshot, UTF-8 Markdown batch publish, exact retry, directory exchange, verify and fresh host commit barriers without Python")
}

func knowledgeFixture(b *sandbox.BotHomeBinding) (store.BotKnowledgeExtractionLease, store.BotKnowledgeExtraction) {
	id := uuid.New()
	botID := uuid.MustParse(b.Scope.BotID)
	outcome := store.BotKnowledgeOutcome{Status: "succeeded", Answer: "Observed result"}
	input := store.BotKnowledgeExtractionInput{SchemaVersion: 1, Complete: true, OwnerMessage: "Investigate", Outcome: outcome, Source: store.BotKnowledgeExtractionSource{BotID: botID, RunID: uuid.New(), OutcomeDigest: store.BotKnowledgeDigest(outcome), Sequence: 1, Version: 1}}
	decision := store.BotKnowledgeExtractionDecision{SchemaVersion: 1, Kind: store.BotKnowledgeDecisionChanges, Reason: "Retain observations", Proposals: []store.BotKnowledgeProposal{{Kind: store.BotKnowledgeProposalKnowledge, Path: "knowledge/topic.md", Content: "# Observed knowledge\n"}, {Kind: store.BotKnowledgeProposalPreference, Path: "preferences/style.md", Content: "# Owner style\n"}}}
	return store.BotKnowledgeExtractionLease{BotID: botID, ExtractionID: id, WorkerID: "knowledge-test", Fence: 1, ExpiresAt: time.Now().Add(4 * time.Minute)}, store.BotKnowledgeExtraction{ID: id, Input: input, State: store.BotKnowledgeExtractionPrepared, Revision: 3, Decision: &decision, DecisionDigest: store.BotKnowledgeDigest(decision)}
}

func TestBotKnowledgeRequiresRepositoryBarrier(t *testing.T) {
	b := &sandbox.BotHomeBinding{Scope: sandbox.BotHomeScope{OrgID: "knowledge-test", TeamID: "owned", UserID: uuid.NewString(), BotID: uuid.NewString()}, Backend: sandbox.BackendKindDocker}
	b.Ref = botHomeVolume(b.Scope)
	l, r := knowledgeFixture(b)
	db := &DockerBackend{}
	if _, err := db.PublishBotKnowledge(t.Context(), b, l, r, func(context.Context) error { return nil }); !errors.Is(err, store.ErrBotUnsupported) {
		t.Fatalf("missing atomic barrier: %v", err)
	}
	for _, state := range []store.BotKnowledgeExtractionState{store.BotKnowledgeExtractionHeld, store.BotKnowledgeExtractionAborted, store.BotKnowledgeExtractionExtracting} {
		bad := r
		bad.State = state
		if err := validateKnowledgePublication(b, l, bad); err == nil {
			t.Fatalf("accepted %s", state)
		}
	}
	bad := r.Clone()
	bad.Decision.Proposals[0].ExpectedDigest = strings.Repeat("a", 64)
	bad.DecisionDigest = store.BotKnowledgeDigest(*bad.Decision)
	if err := validateKnowledgePublication(b, l, bad); err == nil {
		t.Fatal("unbound prior CAS")
	}
}

func TestBotKnowledgeDockerPhysical(t *testing.T) {
	if os.Getenv("ASTONISH_TEST_TASK_WORKSPACE") != "1" {
		t.Skip("explicit owned-volume acceptance required")
	}
	image := os.Getenv("ASTONISH_SANDBOX_IMAGE")
	if image == "" {
		t.Fatal("explicit local image required")
	}
	c, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	_, err := exec.CommandContext(c, "docker", "image", "inspect", "--format", "{{.Id}}", image).Output()
	if err != nil {
		t.Fatal(err)
	}
	db := newBotRetainedSessionFixture(t, c, "docker")
	b, err := db.EnsureBotHome(c, sandbox.BotHomeScope{OrgID: "knowledge-physical-test", TeamID: "owned", UserID: uuid.NewString(), BotID: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		live, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if err := db.DeleteBotHome(live, b); err != nil {
			t.Error(err)
		}
		// DeleteBotHome owns cleanup of both exact labelled volumes.
	})
	id := bindBotRetainedSessionFixture(t, c, db, b)
	l, r := knowledgeFixture(b)
	held := false
	live := sandbox.WithBotKnowledgeCommitBarrier(c, func(commit func() error) error { held = true; defer func() { held = false }(); return commit() })
	check := func(context.Context) error {
		if !held {
			return errors.New("repository lock not held")
		}
		return nil
	}
	rows, err := db.SnapshotBotKnowledge(c, b)
	if err != nil || len(rows) != 0 {
		t.Fatalf("empty snapshot %v %v", rows, err)
	}
	readback, err := db.PublishBotKnowledge(live, b, l, r, check)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.VerifyBotKnowledge(c, b, l, r, readback); err != nil {
		t.Fatal(err)
	}
	// A new backend recovers the exact authorized session and retained bytes.
	db = &DockerBackend{cfg: db.cfg}
	if err := db.BindBotHomeSession(b, id); err != nil {
		t.Fatal(err)
	}
	rows, err = db.SnapshotBotKnowledge(c, b)
	if err != nil || len(rows) != 2 || rows[0].Summary != r.Decision.Proposals[0].Content {
		t.Fatalf("actual byte snapshot %+v %v", rows, err)
	}
	retry, err := db.PublishBotKnowledge(live, b, l, r, check)
	if err != nil || !reflect.DeepEqual(retry, readback) {
		t.Fatalf("exact retry %v", err)
	}
	edited := r.Clone()
	edited.Decision.Proposals[0].Content = "# Edited retry\n"
	edited.DecisionDigest = store.BotKnowledgeDigest(*edited.Decision)
	if _, err := db.PublishBotKnowledge(live, b, l, edited, check); err == nil {
		t.Fatal("edited retry accepted")
	}
	staleL, stale := knowledgeFixture(b)
	if _, err := db.PublishBotKnowledge(live, b, staleL, stale, check); err == nil {
		t.Fatal("stale CAS accepted")
	}
	updateL, update := knowledgeFixture(b)
	update.Input.PriorFiles = rows
	update.Decision.Proposals[0].ExpectedDigest = rows[0].Digest
	update.Decision.Proposals[1].ExpectedDigest = rows[1].Digest
	update.Decision.Proposals[0].Content = "# Updated knowledge\n"
	update.Decision.Proposals[1].Content = "# Updated preference\n"
	update.DecisionDigest = store.BotKnowledgeDigest(*update.Decision)
	if _, err := db.PublishBotKnowledge(live, b, updateL, update, func(context.Context) error { return errors.New("revoked after staging") }); err == nil {
		t.Fatal("revoked batch committed")
	}
	after, err := db.SnapshotBotKnowledge(c, b)
	if err != nil || !reflect.DeepEqual(rows, after) {
		t.Fatalf("partial denied batch: %v", err)
	}
	cancelled, stop := context.WithCancel(live)
	stop()
	if _, err := db.PublishBotKnowledge(cancelled, b, updateL, update, check); err == nil {
		t.Fatal("cancelled commit")
	}
	stagedCancel, cancelStaged := context.WithCancel(live)
	if _, err := db.PublishBotKnowledge(stagedCancel, b, updateL, update, func(context.Context) error { cancelStaged(); return nil }); err == nil {
		t.Fatal("cancellation at staged approval committed")
	}
	after, err = db.SnapshotBotKnowledge(c, b)
	if err != nil || !reflect.DeepEqual(rows, after) {
		t.Fatalf("late cancelled batch changed bytes: %v", err)
	}
	oversizeL, oversize := knowledgeFixture(b)
	oversize.Input.PriorFiles = rows
	oversize.Decision.Proposals[0].ExpectedDigest = rows[0].Digest
	oversize.Decision.Proposals[1].ExpectedDigest = rows[1].Digest
	oversize.Decision.Proposals[0].Content = strings.Repeat("x", store.BotKnowledgeExtractionMaxOutputBytes)
	oversize.DecisionDigest = store.BotKnowledgeDigest(*oversize.Decision)
	if _, err := db.PublishBotKnowledge(live, b, oversizeL, oversize, check); err == nil {
		t.Fatal("unbounded proposal accepted")
	}
	if _, err := db.PublishBotKnowledge(live, b, updateL, update, check); err != nil {
		t.Fatalf("atomic update %v", err)
	}
	foreign := *b
	foreign.Scope.BotID = uuid.NewString()
	if _, err := db.SnapshotBotKnowledge(c, &foreign); err == nil {
		t.Fatal("foreign binding read")
	}
	// Only this exact fresh owned private volume is used for hostile filesystem fixtures.
	for _, attack := range []string{"os.symlink('/etc/passwd',p)", "os.link('/mnt/astonish-bot-home/.workspace-lock',p)", "os.mkfifo(p)", "os.mkdir(p)"} {
		program := "import os; p='/mnt/astonish-bot-home/.knowledge/knowledge/topic.md';os.unlink(p);" + attack
		cmd := exec.CommandContext(c, "docker", "run", "--rm", "--pull=never", "--network=none", "--read-only", "--mount=type=volume,source="+workspaceVolume(b)+",target="+botHomeMount+",volume-nocopy", "--entrypoint=python3", BotWorkspaceHelperImage, "-I", "-c", program)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("owned attack %s %v", out, err)
		}
		if _, err := db.SnapshotBotKnowledge(c, b); err == nil {
			t.Fatalf("accepted special path %s", attack)
		}
		cleanup := "import os; p='/mnt/astonish-bot-home/.knowledge/knowledge/topic.md'; (os.rmdir if os.path.isdir(p) and not os.path.islink(p) else os.unlink)(p);open(p,'w').write('# Updated knowledge\\n')"
		cmd = exec.CommandContext(c, "docker", "run", "--rm", "--pull=never", "--network=none", "--read-only", "--mount=type=volume,source="+workspaceVolume(b)+",target="+botHomeMount+",volume-nocopy", "--entrypoint=python3", BotWorkspaceHelperImage, "-I", "-c", cleanup)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("owned cleanup %s %v", out, err)
		}
	}
}
