package k8s

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
)

func knowledgeFixture(h *sandbox.BotHomeBinding) (store.BotKnowledgeExtractionLease, store.BotKnowledgeExtraction) {
	id, botID := uuid.New(), uuid.MustParse(h.Scope.BotID)
	outcome := store.BotKnowledgeOutcome{Status: "succeeded", Answer: "Observed result"}
	input := store.BotKnowledgeExtractionInput{SchemaVersion: 1, Complete: true, OwnerMessage: "Investigate", Outcome: outcome, Source: store.BotKnowledgeExtractionSource{BotID: botID, RunID: uuid.New(), OutcomeDigest: store.BotKnowledgeDigest(outcome), Sequence: 1, Version: 1}}
	decision := store.BotKnowledgeExtractionDecision{SchemaVersion: 1, Kind: store.BotKnowledgeDecisionChanges, Reason: "Retain observations", Proposals: []store.BotKnowledgeProposal{{Kind: store.BotKnowledgeProposalKnowledge, Path: "knowledge/topic.md", Content: "# Observed knowledge — café\n"}, {Kind: store.BotKnowledgeProposalPreference, Path: "preferences/style.md", Content: "# Owner style\n"}}}
	return store.BotKnowledgeExtractionLease{BotID: botID, ExtractionID: id, WorkerID: "knowledge-test", Fence: 1, ExpiresAt: time.Now().Add(4 * time.Minute)}, store.BotKnowledgeExtraction{ID: id, Input: input, State: store.BotKnowledgeExtractionPrepared, Revision: 3, Decision: &decision, DecisionDigest: store.BotKnowledgeDigest(decision)}
}

func knowledgeBackend(t *testing.T, personal bool) (*K8sBackend, *sandbox.BotHomeBinding, *fake.Clientset) {
	t.Helper()
	b, old := homeBackend(t)
	scope := old.Scope
	scope.BotID = uuid.NewString()
	if personal {
		scope.OrgID, scope.TeamID = "", ""
	}
	h, err := b.EnsureBotHome(t.Context(), scope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.CreateSession(t.Context(), sandbox.SessionSpec{SessionID: "canonical", BotHome: h, TemplateID: sandbox.BaseTemplateID}); err != nil {
		t.Fatal(err)
	}
	client := b.client.(*fake.Clientset)
	pod, err := client.CoreV1().Pods(b.cfg.Namespace).Get(t.Context(), podNameForSession("canonical"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pod.UID = "physical-session"
	pod.Status.Phase = corev1.PodRunning
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: containerName, Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}
	if _, err = client.CoreV1().Pods(b.cfg.Namespace).Update(t.Context(), pod, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = b.BindBotHomeSession(h, "canonical"); err != nil {
		t.Fatal(err)
	}
	b.restConfig = &rest.Config{Host: "https://fake.invalid"}
	client.ClearActions()
	return b, h, client
}

func TestBotKnowledgeValidation(t *testing.T) {
	b, h, client := knowledgeBackend(t, false)
	l, r := knowledgeFixture(h)
	check := func(context.Context) error { return nil }
	if _, err := b.PublishBotKnowledge(t.Context(), h, l, r, check); !errors.Is(err, store.ErrBotUnsupported) {
		t.Fatalf("missing barrier: %v", err)
	}
	ctx := sandbox.WithBotKnowledgeCommitBarrier(t.Context(), func(f func() error) error { return f() })
	if _, err := b.PublishBotKnowledge(ctx, h, l, r, nil); !errors.Is(err, store.ErrBotUnsupported) {
		t.Fatalf("missing check: %v", err)
	}
	for _, state := range []store.BotKnowledgeExtractionState{store.BotKnowledgeExtractionHeld, store.BotKnowledgeExtractionAborted, store.BotKnowledgeExtractionExtracting} {
		bad := r.Clone()
		bad.State = state
		if err := validateKnowledgePublication(h, l, bad); !errors.Is(err, store.ErrBotConflict) {
			t.Fatalf("state %s: %v", state, err)
		}
	}
	for _, mode := range []string{"prior", "digest", "hidden", "utf8", "identity", "backend", "lease"} {
		t.Run(mode, func(t *testing.T) {
			bad, home, lease := r.Clone(), h.Clone(), l
			switch mode {
			case "prior":
				bad.Decision.Proposals[0].ExpectedDigest = strings.Repeat("a", 64)
			case "hidden":
				bad.Decision.Proposals[0].Path = "knowledge/.hidden.md"
			case "utf8":
				bad.Decision.Proposals[0].Content = string([]byte{0xff})
			case "identity":
				home.Scope.BotID = uuid.NewString()
			case "backend":
				home.Backend = sandbox.BackendKindDocker
			case "lease":
				lease.ExpiresAt = time.Now().Add(-time.Second)
			}
			bad.DecisionDigest = store.BotKnowledgeDigest(*bad.Decision)
			if mode == "digest" {
				bad.DecisionDigest = strings.Repeat("a", 64)
			}
			if _, err := b.PublishBotKnowledge(ctx, home, lease, bad, check); !errors.Is(err, store.ErrBotInvalid) {
				t.Fatalf("invalid %s: %v", mode, err)
			}
		})
	}
	if len(client.Actions()) != 0 {
		t.Fatal("invalid publication touched Kubernetes")
	}
}

func TestBotKnowledgeWarmCommitTransport(t *testing.T) {
	b, h, client := knowledgeBackend(t, false)
	l, r := knowledgeFixture(h)
	rows := []store.BotKnowledgeFileReadback{{Path: r.Decision.Proposals[0].Path, Digest: strings.Repeat("a", 64)}}
	held, checks := false, 0
	ctx := sandbox.WithBotKnowledgeCommitBarrier(t.Context(), func(f func() error) error { held = true; defer func() { held = false }(); return f() })
	b.execExecutorFactory = func(_ *rest.Config, _ string, u *url.URL) (remoteExecutor, error) {
		command := strings.Join(u.Query()["command"], " ")
		if !strings.Contains(u.Path, podNameForSession("canonical")) || !strings.Contains(command, "--internal-scoped-home-workspace") || !strings.Contains(command, h.Ref) || !strings.Contains(command, "physical-session") || strings.Contains(command, "python") {
			t.Fatal(u)
		}
		return homeReadExecutor(func(_ context.Context, o remotecommand.StreamOptions) error {
			in := bufio.NewReader(o.Stdin)
			line, err := in.ReadBytes('\n')
			if err != nil {
				return err
			}
			var q map[string]any
			if err = json.Unmarshal(line, &q); err != nil {
				return err
			}
			if q["binding"] != h.Scope.Key() || q["owner"] != h.Ref {
				t.Fatal("wrong scope")
			}
			if q["op"] == "knowledge-publish" {
				if !strings.HasSuffix(command, "private mutate") {
					t.Fatal("publish cannot initialize private root")
				}
				ready := make(chan error, 1)
				go func() {
					_, e := io.WriteString(o.Stdout, "WORKSPACE_READY\n")
					ready <- e
				}()
				line, err = in.ReadBytes('\n')
				readyErr := <-ready
				if readyErr != nil {
					return readyErr
				}
				if err != nil {
					return err
				}
				if string(line) != "commit\n" {
					return errors.New("commit denied")
				}
			} else if !strings.HasSuffix(command, "private read") {
				t.Fatal("read mutated root")
			}
			return json.NewEncoder(o.Stdout).Encode(rows)
		}), nil
	}
	got, err := b.PublishBotKnowledge(ctx, h, l, r, func(context.Context) error {
		if !held {
			return errors.New("barrier not held")
		}
		checks++
		return nil
	})
	if err != nil || !reflect.DeepEqual(got, rows) || checks != 1 {
		t.Fatalf("publish %v %+v checks=%d", err, got, checks)
	}
	if err = b.VerifyBotKnowledge(t.Context(), h, l, r, rows); err != nil {
		t.Fatal(err)
	}
	if err = b.VerifyBotKnowledge(t.Context(), h, l, r, nil); !errors.Is(err, store.ErrBotConflict) {
		t.Fatalf("readback mismatch %v", err)
	}
	if _, err = b.PublishBotKnowledge(ctx, h, l, r, func(context.Context) error { return errors.New("revoked") }); err == nil {
		t.Fatal("denied commit accepted")
	}
	for _, a := range client.Actions() {
		if a.GetVerb() != "get" {
			t.Fatalf("warm mutation %s %s", a.GetVerb(), a.GetResource().Resource)
		}
	}
}

// Actual compiled Linux CLI, with only Kubernetes transport and mount location
// substituted. This is not physical Kubernetes/CSI/SPDY evidence.
func TestBotKnowledgeCompiledFilesystem(t *testing.T) {
	binary := os.Getenv("ASTONISH_BOT_FILES_TEST_BINARY")
	if runtime.GOOS != "linux" || !filepath.IsAbs(binary) {
		t.Skip("requires Linux and ASTONISH_BOT_FILES_TEST_BINARY")
	}
	for _, personal := range []bool{false, true} {
		t.Run(map[bool]string{false: "team", true: "personal"}[personal], func(t *testing.T) {
			b, h, client := knowledgeBackend(t, personal)
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, "data"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, ".owner"), []byte(h.Ref+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			b.execExecutorFactory = func(_ *rest.Config, _ string, u *url.URL) (remoteExecutor, error) {
				args := append([]string(nil), u.Query()["command"]...)
				if len(args) != 3 || !strings.Contains(args[2], "--internal-scoped-home-workspace") || !strings.Contains(args[2], h.Ref) {
					t.Fatal("wrong dispatch")
				}
				args[2] = strings.ReplaceAll(args[2], mountBotHomeScope, root)
				args[2] = strings.ReplaceAll(args[2], "/usr/local/bin/astonish-host", binary)
				return homeReadExecutor(func(ctx context.Context, o remotecommand.StreamOptions) error {
					cmd := exec.CommandContext(ctx, args[0], args[1:]...)
					cmd.Env = append(os.Environ(), "ASTONISH_POD_UID=physical-session", "PATH=/nonexistent")
					input, writer, err := os.Pipe()
					if err != nil {
						return err
					}
					defer input.Close()
					cmd.Stdin, cmd.Stdout, cmd.Stderr = input, o.Stdout, o.Stderr
					copied := make(chan struct{})
					go func() { _, _ = io.Copy(writer, o.Stdin); _ = writer.Close(); close(copied) }()
					err = cmd.Run()
					_ = o.Stdin.(io.ReadCloser).Close()
					_ = writer.Close()
					<-copied
					return err
				}), nil
			}
			// Missing private roots fail closed and reads must not provision them.
			if _, err := b.SnapshotBotKnowledge(t.Context(), h); err == nil {
				t.Fatal("missing root accepted")
			}
			if _, err := os.Stat(filepath.Join(root, "workspace-private")); !os.IsNotExist(err) {
				t.Fatal("read provisioned root")
			}
			l, r := knowledgeFixture(h)
			held, checks := false, 0
			ctx := sandbox.WithBotKnowledgeCommitBarrier(t.Context(), func(f func() error) error { held = true; defer func() { held = false }(); return f() })
			check := func(context.Context) error {
				if !held {
					return errors.New("barrier not held")
				}
				checks++
				return nil
			}
			rows, err := b.PublishBotKnowledge(ctx, h, l, r, check)
			if err != nil {
				t.Fatal(err)
			}
			if err = b.VerifyBotKnowledge(t.Context(), h, l, r, rows); err != nil {
				t.Fatal(err)
			}
			snapshot, err := b.SnapshotBotKnowledge(t.Context(), h)
			if err != nil || len(snapshot) != 2 || snapshot[0].Summary != r.Decision.Proposals[0].Content {
				t.Fatalf("snapshot %+v %v", snapshot, err)
			}
			retry, err := b.PublishBotKnowledge(ctx, h, l, r, check)
			if err != nil || !reflect.DeepEqual(rows, retry) {
				t.Fatalf("exact retry %+v %v", retry, err)
			}
			staleL, stale := knowledgeFixture(h)
			if _, err = b.PublishBotKnowledge(ctx, h, staleL, stale, check); err == nil {
				t.Fatal("stale CAS accepted")
			}
			updateL, update := knowledgeFixture(h)
			update.Input.PriorFiles = snapshot
			for i := range update.Decision.Proposals {
				update.Decision.Proposals[i].ExpectedDigest = snapshot[i].Digest
				update.Decision.Proposals[i].Content += "Updated\n"
			}
			update.DecisionDigest = store.BotKnowledgeDigest(*update.Decision)
			if _, err = b.PublishBotKnowledge(ctx, h, updateL, update, func(context.Context) error { return errors.New("revoked") }); err == nil {
				t.Fatal("denied batch accepted")
			}
			after, err := b.SnapshotBotKnowledge(t.Context(), h)
			if err != nil || !reflect.DeepEqual(snapshot, after) {
				t.Fatalf("denial changed files %+v %v", after, err)
			}
			if _, err = b.PublishBotKnowledge(ctx, h, updateL, update, check); err != nil {
				t.Fatal(err)
			}
			if checks < 2 {
				t.Fatal("missing fresh commit checks")
			}
			if err = os.WriteFile(filepath.Join(root, ".owner"), []byte("foreign\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err = b.SnapshotBotKnowledge(t.Context(), h); err == nil {
				t.Fatal("foreign owner accepted")
			}
			for _, a := range client.Actions() {
				if a.GetVerb() != "get" {
					t.Fatalf("warm mutation %s %s", a.GetVerb(), a.GetResource().Resource)
				}
			}
		})
	}
}
