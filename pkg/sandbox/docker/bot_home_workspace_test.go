package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/google/uuid"
)

func TestBotHomeWorkspace(t *testing.T) {
	if os.Getenv("ASTONISH_TEST_TASK_WORKSPACE") != "1" {
		t.Skip("explicit physical Docker acceptance required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	image := os.Getenv("ASTONISH_SANDBOX_IMAGE")
	if image == "" {
		t.Fatal("explicit local image required")
	}
	raw, e := exec.CommandContext(ctx, "docker", "image", "inspect", "--format", "{{.Id}}", image).Output()
	if e != nil {
		t.Fatal(e)
	}
	db := &DockerBackend{cfg: Config{ContainerRuntimePath: "docker", SandboxImage: strings.TrimSpace(string(raw))}}
	home, e := db.EnsureBotHome(ctx, sandbox.BotHomeScope{OrgID: "task-workspace-test", TeamID: "owned", UserID: uuid.NewString(), BotID: uuid.NewString()})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		c, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		if e := db.DeleteBotHome(c, home); e != nil {
			t.Error(e)
		}
	})
	t.Cleanup(func() {
		c, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		_, err := runDocker(c, "docker", "volume", "rm", workspaceVolume(home))
		if err != nil {
			t.Error(err)
		}
	})
	files := []sandbox.BotHomeWorkspaceFile{{Path: "TASK.md", Data: []byte("# Task\nInspect retained state\n")}, {Path: "scripts/helper.py", Data: []byte("import sqlite3,socket\ntry:\n socket.create_connection(('1.1.1.1',443),timeout=.1)\n raise RuntimeError('network available')\nexcept OSError: pass\nc=sqlite3.connect('state.sqlite'); c.execute('create table if not exists counts(n integer)'); c.execute('insert into counts values(1)'); c.commit(); print(c.execute('select count(*) from counts').fetchone()[0]); c.close()\n"), Executable: true}}
	q := sandbox.BotHomeWorkspacePublishRequest{PublicationID: uuid.NewString(), TaskPath: "tasks/" + uuid.NewString(), Files: files}
	receipt, e := db.PublishBotHomeWorkspace(ctx, home, q)
	if e != nil {
		t.Fatal(e)
	}
	if receipt.ReceiptRef == "" || len(receipt.Entries) != 2 {
		t.Fatalf("receipt %+v", receipt)
	}
	retry, e := db.PublishBotHomeWorkspace(ctx, home, q)
	if e != nil || retry.ReceiptRef != receipt.ReceiptRef {
		t.Fatalf("durable retry %v", e)
	}
	script := sandbox.BotHomeWorkspaceScriptRequest{TaskPath: q.TaskPath, PublicationID: q.PublicationID, ScriptPath: files[1].Path, Digest: files[1].Digest()}
	result, e := db.RunBotHomeWorkspaceScript(ctx, home, script)
	if e != nil || strings.TrimSpace(result.Stdout) != "1" {
		t.Fatalf("helper %+v %v", result, e)
	}
	result, e = db.RunBotHomeWorkspaceScript(ctx, home, script)
	if e != nil || strings.TrimSpace(result.Stdout) != "2" {
		t.Fatalf("SQLite continuity %+v %v", result, e)
	}
	sqlite, e := db.ReadBotHomeWorkspace(ctx, home, q.TaskPath+"/state/state.sqlite", 1<<20)
	if e != nil || !strings.HasPrefix(string(sqlite), "SQLite format 3") {
		t.Fatalf("physical SQLite readback %v", e)
	}
	whole, e := db.SnapshotBotHomeWorkspace(ctx, home, "workspace", sandbox.MaxBotHomeWorkspaceBundleBytes)
	if e != nil {
		t.Fatal(e)
	}
	retained := map[string][]byte{}
	for _, file := range whole.Files {
		retained[file.Path] = file.Data
	}
	if !bytes.Equal(retained[q.TaskPath+"/state/state.sqlite"], sqlite) || !bytes.Equal(retained[q.TaskPath+"/.revisions/"+q.PublicationID+"/TASK.md"], files[0].Data) || len(retained[".receipts/"+q.PublicationID]) == 0 || len(retained[q.TaskPath+"/.head"]) == 0 {
		t.Fatal("whole snapshot omitted mutable state or private immutable metadata")
	}
	if _, err := db.SnapshotBotHomeWorkspace(ctx, home, "workspace", 1); err == nil {
		t.Fatal("truncated whole workspace snapshot accepted")
	}
	// New backend / new helper container: no in-memory receipts or temporary bytes.
	db = &DockerBackend{cfg: db.cfg}
	got, e := db.ReadBotHomeWorkspace(ctx, home, q.TaskPath+"/TASK.md", 1024)
	if e != nil || string(got) != string(files[0].Data) {
		t.Fatalf("reread %s %v", got, e)
	}
	resolved, e := db.ResolveBotHomeWorkspaceReceipt(ctx, home, receipt.ReceiptRef)
	if e != nil || !reflect.DeepEqual(receipt, resolved) {
		t.Fatalf("receipt resolve %+v %v", resolved, e)
	}
	snap, e := db.SnapshotBotHomeWorkspace(ctx, home, q.TaskPath, 4096)
	if e != nil || len(snap.Files) != 2 {
		t.Fatalf("snapshot %v", e)
	}
	rows, next, e := db.ListBotHomeWorkspace(ctx, home, q.TaskPath, "", 1)
	if e != nil || len(rows) != 1 || next == "" {
		t.Fatalf("page %v %s %v", rows, next, e)
	}
	rows, next, e = db.ListBotHomeWorkspace(ctx, home, q.TaskPath, next, 1)
	if e != nil || len(rows) != 1 || next != "" {
		t.Fatalf("last page %v %s %v", rows, next, e)
	}
	state := sandbox.BotHomeWorkspaceFile{Path: q.TaskPath + "/state/checkpoint.json", Data: []byte(`{"count":1}`)}
	if e = db.WriteBotHomeWorkspace(ctx, home, state); e != nil {
		t.Fatal(e)
	}
	got, e = db.ReadBotHomeWorkspace(ctx, home, state.Path, 1024)
	if e != nil || string(got) != string(state.Data) {
		t.Fatal(e)
	}
	// A script holds the same lock through execution, inventory and commit.
	staged := make(chan struct{})
	release := make(chan struct{})
	scriptDone := make(chan error, 1)
	guarded := sandbox.WithBotHomeWorkspaceCommitGuard(ctx, func(context.Context) error {
		close(staged)
		<-release
		return errors.New("script lease revoked after inventory")
	})
	go func() { _, err := db.RunBotHomeWorkspaceScript(guarded, home, script); scriptDone <- err }()
	select {
	case <-staged:
	case <-ctx.Done():
		t.Fatal("script did not reach staged guard", ctx.Err())
	}
	for _, op := range []string{"read", "write", "snapshot"} {
		blocked, stop := context.WithTimeout(ctx, 200*time.Millisecond)
		var err error
		switch op {
		case "read":
			_, err = db.ReadBotHomeWorkspace(blocked, home, state.Path, 1024)
		case "write":
			err = db.WriteBotHomeWorkspace(blocked, home, sandbox.BotHomeWorkspaceFile{Path: state.Path, Data: []byte("blocked")})
		case "snapshot":
			_, err = db.SnapshotBotHomeWorkspace(blocked, home, q.TaskPath, 4096)
		}
		stop()
		if err == nil {
			close(release)
			t.Fatalf("%s bypassed script storage lock", op)
		}
	}
	close(release)
	if err := <-scriptDone; err == nil {
		t.Fatal("revoked script published")
	}
	if b, err := db.ReadBotHomeWorkspace(ctx, home, state.Path, 1024); err != nil || string(b) != string(state.Data) {
		t.Fatal("guarded script changed state", err)
	}

	for _, p := range []string{q.TaskPath + "/TASK.md", q.TaskPath + "/scripts/helper.py", "../foreign", "/root/secret"} {
		if e = db.WriteBotHomeWorkspace(ctx, home, sandbox.BotHomeWorkspaceFile{Path: p, Data: []byte("tampered")}); e == nil {
			t.Fatalf("unconfined write %s", p)
		}
	}
	if _, e = db.ReadBotHomeWorkspace(ctx, home, q.TaskPath+"/TASK.md", 1); e == nil {
		t.Fatal("truncated read accepted")
	}
	foreign := *home
	foreign.Scope.BotID = uuid.NewString()
	if _, e = db.ResolveBotHomeWorkspaceReceipt(ctx, &foreign, receipt.ReceiptRef); e == nil {
		t.Fatal("foreign binding accepted")
	}
	stale := q
	stale.PublicationID = uuid.NewString()
	if _, e = db.PublishBotHomeWorkspace(ctx, home, stale); e == nil {
		t.Fatal("stale head accepted")
	}
	cancelled, stop := context.WithCancel(ctx)
	stop()
	if e = db.WriteBotHomeWorkspace(cancelled, home, state); e == nil {
		t.Fatal("cancelled writer accepted")
	}
	// Crash after immutable rename/receipt but before head selection; exact retry
	// reuses bytes and edited retry must never rewrite the immutable candidate.
	crash := sandbox.BotHomeWorkspacePublishRequest{PublicationID: uuid.NewString(), TaskPath: "tasks/" + uuid.NewString(), Files: files}
	input, err := json.Marshal(map[string]any{"op": "publish", "request": crash, "binding": home.Scope.Key()})
	if err != nil {
		t.Fatal(err)
	}
	program := strings.Replace(botWorkspaceProgram, " write(p+'/.head',json.dumps(result).encode())", " raise RuntimeError('injected pre-head crash')", 1)
	program = strings.Replace(program, "def ready():\n", "def ready():\n return\n", 1)
	cmd := exec.CommandContext(ctx, "docker", "container", "run", "--rm", "--interactive", "--pull=never", "--network=none", "--read-only", "--mount=type=volume,source="+workspaceVolume(home)+",target="+botHomeMount+",volume-nocopy", "--entrypoint=python3", BotWorkspaceHelperImage, "-I", "-c", program)
	cmd.Stdin = bytes.NewReader(append(input, '\n'))
	if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "injected pre-head crash") {
		t.Fatalf("fault injection %s %v", out, err)
	}
	edited := crash
	edited.Files = []sandbox.BotHomeWorkspaceFile{{Path: "TASK.md", Data: []byte("edited")}}
	if _, e = db.PublishBotHomeWorkspace(ctx, home, edited); e == nil {
		t.Fatal("edited immutable retry accepted")
	}
	if _, e = db.PublishBotHomeWorkspace(ctx, home, crash); e != nil {
		t.Fatalf("pre-head recovery %v", e)
	}
	// Ordinary Home authoring does not change private receipts or revision bytes.
	tamper := exec.CommandContext(ctx, "docker", "run", "--rm", "--pull=never", "--network=none", "--mount=type=volume,source="+home.Ref+",target=/home", "--entrypoint=python3", BotWorkspaceHelperImage, "-I", "-c", "import os;os.makedirs('/home/.receipts',exist_ok=True);open('/home/.receipts/"+receipt.ReceiptRef+"','w').write('{}')")
	if out, err := tamper.CombinedOutput(); err != nil {
		t.Fatalf("ordinary tamper %s %v", out, err)
	}
	if r, err := db.ResolveBotHomeWorkspaceReceipt(ctx, home, receipt.ReceiptRef); err != nil || r.Digest != receipt.Digest {
		t.Fatalf("forged ordinary receipt affected provenance: %v", err)
	}
	forged := *receipt
	forged.PublicationID = uuid.NewString()
	forged.ReceiptRef = forged.PublicationID
	forgery, _ := json.Marshal(forged)
	forge := exec.CommandContext(ctx, "docker", "run", "--rm", "--interactive", "--pull=never", "--network=none", "--mount=type=volume,source="+home.Ref+",target=/home", "--entrypoint=python3", BotWorkspaceHelperImage, "-I", "-c", "import os,sys;os.makedirs('/home/.receipts',exist_ok=True);open('/home/.receipts/"+forged.ReceiptRef+"','w').write(sys.stdin.read())")
	forge.Stdin = bytes.NewReader(forgery)
	if out, err := forge.CombinedOutput(); err != nil {
		t.Fatalf("ordinary forgery %s %v", out, err)
	}
	if _, err := db.ResolveBotHomeWorkspaceReceipt(ctx, home, forged.ReceiptRef); err == nil {
		t.Fatal("self-authored ordinary receipt accepted")
	}
	// Staged writes re-check revocation at the last publication barrier.
	denied := sandbox.WithBotHomeWorkspaceCommitGuard(ctx, func(context.Context) error { return errors.New("revoked after staging") })
	if err := db.WriteBotHomeWorkspace(denied, home, sandbox.BotHomeWorkspaceFile{Path: state.Path, Data: []byte("late")}); err == nil {
		t.Fatal("revoked staged write published")
	}
	if b, err := db.ReadBotHomeWorkspace(ctx, home, state.Path, 1024); err != nil || string(b) != string(state.Data) {
		t.Fatal("revoked writer changed state", err)
	}
	for _, mode := range []string{"failure", "aggregate", "links", "timeout", "cancel"} {
		code := "open('checkpoint.json','w').write('changed');raise RuntimeError('fail')"
		if mode == "aggregate" {
			code = "for i in range(200):\n open(str(i),'wb').write(b'x'*1048576)"
		}
		if mode == "links" {
			code = "import os;os.symlink('/tmp/other','bad')"
		}
		if mode == "cancel" || mode == "timeout" {
			code = "import time;open('checkpoint.json','w').write('changed');time.sleep(20)"
		}
		bad := sandbox.BotHomeWorkspacePublishRequest{TaskPath: q.TaskPath, PublicationID: uuid.NewString(), ExpectedDigest: receipt.Digest, Files: []sandbox.BotHomeWorkspaceFile{{Path: "TASK.md", Data: files[0].Data}, {Path: "scripts/bad.py", Data: []byte(code), Executable: true}}}
		r, err := db.PublishBotHomeWorkspace(ctx, home, bad)
		if err != nil {
			t.Fatal(err)
		}
		receipt = r
		runctx := ctx
		stop := func() {}
		if mode == "cancel" {
			runctx, stop = context.WithTimeout(ctx, 200*time.Millisecond)
		}
		_, err = db.RunBotHomeWorkspaceScript(runctx, home, sandbox.BotHomeWorkspaceScriptRequest{TaskPath: q.TaskPath, PublicationID: bad.PublicationID, ScriptPath: "scripts/bad.py", Digest: bad.Files[1].Digest()})
		stop()
		if err == nil {
			t.Fatalf("%s script accepted", mode)
		}
		b, err := db.ReadBotHomeWorkspace(ctx, home, state.Path, 1024)
		if err != nil || string(b) != string(state.Data) {
			t.Fatalf("%s durable state changed %s %v", mode, b, err)
		}
	}
	t.Logf("physical Docker volume %s image %s: retained TASK.md/helper/state, receipt readback, pagination, quotas, foreign paths, cancellation, immutable pre-head recovery", home.Ref, db.cfg.SandboxImage)
}

func TestBotHomeWorkspaceRejectInputs(t *testing.T) {
	db := &DockerBackend{}
	if _, e := db.PublishBotHomeWorkspace(t.Context(), nil, sandbox.BotHomeWorkspacePublishRequest{}); e == nil {
		t.Fatal("empty publication")
	}
	if _, e := db.ReadBotHomeWorkspace(t.Context(), nil, "../x", 1); e == nil {
		t.Fatal("traversal")
	}
	if e := db.WriteBotHomeWorkspace(t.Context(), nil, sandbox.BotHomeWorkspaceFile{Path: "x", Executable: true}); e == nil {
		t.Fatal("executable mutation")
	}
	if _, _, e := db.ListBotHomeWorkspace(t.Context(), nil, "x", "", 101); e == nil {
		t.Fatal("page limit")
	}
	if _, e := db.SnapshotBotHomeWorkspace(t.Context(), nil, "x", 0); e == nil {
		t.Fatal("snapshot limit")
	}
	if _, e := db.ResolveBotHomeWorkspaceReceipt(t.Context(), nil, "x"); e == nil {
		t.Fatal("missing binding")
	}
}
