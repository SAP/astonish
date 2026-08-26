package pptxworker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunnerRejectsProtocolMismatch(t *testing.T) {
	t.Parallel()
	_, err := (Runner{}).Run(context.Background(), Request{ProtocolVersion: 99})
	if err == nil {
		t.Fatal("expected protocol error")
	}
}

func TestRunnerRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	script := filepath.Join(dir, "worker.mjs")
	if err := os.WriteFile(script, []byte(`let s=""; for await (const c of process.stdin) s+=c; const r=JSON.parse(s); process.stdout.write(JSON.stringify({protocolVersion:r.protocolVersion,pptxBase64:"UEs=",native:1,vector:0,raster:0,unsupported:0}));`), 0o600); err != nil {
		t.Fatal(err)
	}
	response, err := (Runner{WorkingDir: dir, ScriptPath: script, Timeout: 5 * time.Second}).Run(context.Background(), Request{ProtocolVersion: ProtocolVersion, Scene: []byte(`{"slides":[]}`)})
	if err != nil {
		t.Fatal(err)
	}
	if response.PPTXBase64 != "UEs=" || response.Native != 1 {
		t.Fatalf("unexpected response: %+v", response)
	}
}
