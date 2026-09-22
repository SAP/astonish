package pptxworker

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestThumbRunnerRejectsProtocolMismatch(t *testing.T) {
	t.Parallel()
	_, err := (ThumbRunner{}).Run(context.Background(), ThumbRequest{ProtocolVersion: 99, PPTXBase64: "aa"})
	if err == nil {
		t.Fatal("expected protocol error")
	}
}

func TestThumbRunnerRoundTrip(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not available")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "thumbs.mjs")
	stub := `let s=""; for await (const c of process.stdin) s+=c; const r=JSON.parse(s); process.stdout.write(JSON.stringify({protocolVersion:r.protocolVersion,slides:[{slideNumber:1,pngBase64:"aGVsbG8=",width:8,height:4}],warnings:["ok"]}));`
	if err := os.WriteFile(script, []byte(stub), 0o600); err != nil {
		t.Fatal(err)
	}
	resp, err := (ThumbRunner{WorkingDir: dir, ScriptPath: script, Timeout: 10 * time.Second}).
		Run(context.Background(), ThumbRequest{PPTXBase64: "AAAA"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Slides) != 1 || resp.Slides[0].SlideNumber != 1 {
		t.Fatalf("slides: %+v", resp.Slides)
	}
	raw, err := resp.Slides[0].DecodePNG()
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "hello" {
		t.Fatalf("png bytes %q", raw)
	}
	if len(resp.Warnings) != 1 {
		t.Fatalf("warnings: %v", resp.Warnings)
	}
	_ = json.Marshal
}
