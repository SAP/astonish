package k8s

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/SAP/astonish/pkg/sandbox"
)

func TestBotHomeWorkspaceProspectiveDirectoryQuota(t *testing.T) {
	root, q := workspaceFixture(t)
	private := filepath.Join(root, "workspace-private")
	if err := os.MkdirAll(private, 0700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8189; i++ {
		if err := os.Mkdir(filepath.Join(private, fmt.Sprintf("dir-%04d", i)), 0700); err != nil {
			t.Fatal(err)
		}
	}
	q["op"] = "write"
	q["path"] = "tasks/task/state/new"
	q["data"] = []byte("x")
	if workspaceLocal(t, root, q, nil) == nil {
		t.Fatal("prospective directory count accepted")
	}
	if _, err := os.Stat(filepath.Join(private, "tasks")); !os.IsNotExist(err) {
		t.Fatal("quota rejection left durable directory", err)
	}
}

func TestBotHomeWorkspaceProspectiveFileQuota(t *testing.T) {
	root, q := workspaceFixture(t)
	private := filepath.Join(root, "workspace-private")
	if err := os.MkdirAll(private, 0700); err != nil {
		t.Fatal(err)
	}
	// The lock is one file. Existing 4095 + prospective one must be rejected
	// before staging, even though their total byte size is zero.
	for i := 0; i < 4095; i++ {
		if err := os.WriteFile(filepath.Join(private, fmt.Sprintf("file-%04d", i)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	q["op"] = "write"
	q["path"] = "tasks/task/state/new"
	q["data"] = []byte("x")
	if workspaceLocal(t, root, q, nil) == nil {
		t.Fatal("prospective file count accepted")
	}
	if _, err := os.Stat(filepath.Join(private, "tasks/task/state/new")); !os.IsNotExist(err) {
		t.Fatal("quota rejection left durable file", err)
	}
}
func TestBotHomeWorkspaceRejectForgedOrdinaryHomeReceipt(t *testing.T) {
	root, q := workspaceFixture(t)
	r := sandbox.BotHomeWorkspacePublishRequest{TaskPath: "tasks/task", PublicationID: "forged", Files: []sandbox.BotHomeWorkspaceFile{{Path: "TASK.md", Data: []byte("forged")}}}
	q["op"] = "publish"
	q["request"] = r
	var receipt sandbox.BotHomeWorkspaceReceipt
	if e := workspaceLocal(t, root, q, &receipt); e != nil {
		t.Fatal(e)
	}
	// Simulate ordinary exec authoring all three formerly trusted objects.
	data := filepath.Join(root, "data")
	if e := os.MkdirAll(filepath.Join(data, ".receipts"), 0700); e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(receipt)
	if e := os.WriteFile(filepath.Join(data, ".receipts", "forged"), raw, 0600); e != nil {
		t.Fatal(e)
	}
	// Removing backend-only storage must make the ordinary forged receipt unusable.
	if e := os.RemoveAll(filepath.Join(root, "workspace-private")); e != nil {
		t.Fatal(e)
	}
	q["op"] = "resolve"
	q["path"] = "forged"
	if workspaceLocal(t, root, q, nil) == nil {
		t.Fatal("ordinary Home self-authored receipt accepted")
	}
}
