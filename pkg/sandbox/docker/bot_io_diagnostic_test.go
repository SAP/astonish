package docker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// Opt-in diagnostic only. Clean timing uses the actual Docker executable;
// command counting is a separate, untimed phase on the same owned session.
func TestProductionBotIODiagnostic(t *testing.T) {
	if os.Getenv("ASTONISH_IO_DIAGNOSTIC") != "1" || os.Getenv("ASTONISH_TEST_DOCKER") != "1" {
		t.Skip("explicit Docker diagnostic opt-in required")
	}
	image, layers, template := os.Getenv("ASTONISH_SANDBOX_IMAGE"), os.Getenv("ASTONISH_BOT_HOME_TEST_LAYERS"), os.Getenv("ASTONISH_BOT_HOME_TEST_TEMPLATE")
	if image == "" || !filepath.IsAbs(layers) || template == "" {
		t.Fatal("explicit image, absolute layers, and provisioned Python template required")
	}
	root, err := os.MkdirTemp(filepath.Dir(layers), "session-io-diagnostic-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	st, err := sandbox.NewLocalSessionStore(filepath.Join(root, "registry"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := New(Config{Sessions: sandbox.NewSessionRegistryFromStore(st), SandboxImage: image, LayersDir: layers, UppersDir: filepath.Join(root, "uppers"), OverlayMode: "fuse"})
	if err != nil {
		t.Fatal(err)
	}
	runtime := db.cfg.ContainerRuntimePath
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Second)
	defer cancel()
	cold := time.Now()
	home, err := db.EnsureBotHome(ctx, sandbox.BotHomeScope{UserID: uuid.NewString(), BotID: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	t.Logf("owned_home=%s private=%s session=%s", home.Ref, workspaceVolume(home), id)
	defer func() {
		db.cfg.ContainerRuntimePath = runtime
		c, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if err := db.DestroySession(c, id); err != nil {
			t.Error(err)
			return
		}
		if err := db.DeleteBotHome(c, home); err != nil {
			t.Error(err)
			return
		}
		for _, v := range []string{home.Ref, workspaceVolume(home)} {
			raw, e := runDocker(c, runtime, "volume", "ls", "-q", "--filter=name="+v)
			if e != nil || strings.TrimSpace(string(raw)) != "" {
				t.Errorf("cleanup volume %s: %s %v", v, raw, e)
			}
		}
		raw, e := runDocker(c, runtime, "ps", "-aq", "--filter=name=^/"+containerName(id)+"$")
		if e != nil || strings.TrimSpace(string(raw)) != "" {
			t.Errorf("cleanup session: %s %v", raw, e)
		}
		t.Log("cleanup checked: exact owned session and home/private volumes absent")
	}()
	if err := db.BindBotHomeSession(home, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateSession(ctx, sandbox.SessionSpec{SessionID: id, Type: sandbox.SessionTypeChat, UserID: home.Scope.UserID, BotHome: home.Clone(), TemplateID: template, LayerChain: []string{sandbox.BaseTemplateID, template}}); err != nil {
		t.Fatal(err)
	}
	// Fail before collecting samples if the real session tool layer is not ready.
	if raw, err := runDocker(ctx, runtime, "exec", containerName(id), "chroot", "/sandbox/rootfs", "/usr/bin/python3", "-c", "print('session-python-ready')"); err != nil {
		t.Fatalf("session initialization: %s %v", raw, err)
	}
	t.Logf("cold_session_setup_ms=%.3f", float64(time.Since(cold))/1e6)
	body := bytes.Repeat([]byte("0123456789abcdef"), 4096)
	digest := sha256.Sum256(body)
	guard := func(f func() error) error { return f() }
	put := func(revision int64) (*store.BotContentRef, float64) {
		start := time.Now()
		ref, e := db.PutBotContent(ctx, home, store.BotContentDocument, int64(len(body)), bytes.NewReader(body), revision, guard)
		elapsed := float64(time.Since(start)) / 1e6
		if e != nil {
			t.Fatal(e)
		}
		return ref, elapsed
	}
	read := func(ref *store.BotContentRef) (float64, float64, float64) {
		start := time.Now()
		stream, e := db.OpenBotContent(ctx, home, *ref, store.BotFileRange{})
		opened := time.Now()
		if e != nil {
			t.Fatal(e)
		}
		raw, e := io.ReadAll(stream)
		ce := stream.Close()
		finished := time.Now()
		if e != nil || ce != nil {
			t.Fatalf("read/close: %v %v", e, ce)
		}
		if len(raw) != len(body) || sha256.Sum256(raw) != digest {
			t.Fatal("digest mismatch")
		}
		return float64(opened.Sub(start)) / 1e6, float64(finished.Sub(opened)) / 1e6, float64(time.Since(start)) / 1e6
	}
	stats := func(name string, x []float64) {
		sort.Float64s(x)
		p95 := (95*len(x)+99)/100 - 1
		t.Logf("%s n=%d median_ms=%.3f p95_ms=%.3f min_ms=%.3f max_ms=%.3f", name, len(x), (x[(len(x)-1)/2]+x[len(x)/2])/2, x[p95], x[0], x[len(x)-1])
	}
	ref, firstPut := put(1)
	o, r, e2e := read(ref)
	t.Logf("cold_fixture_put_ms=%.3f first_read_open_ms=%.3f consume_close_ms=%.3f verified_e2e_ms=%.3f sha256=%x bytes=%d", firstPut, o, r, e2e, digest, len(body))
	var opens, consumes, totals, verified []float64
	for i := range 20 {
		o, r, e := read(ref)
		opens = append(opens, o)
		consumes = append(consumes, r)
		totals = append(totals, o+r)
		verified = append(verified, e)
		t.Logf("warm_read iteration=%d open_ms=%.3f consume_close_ms=%.3f total_ms=%.3f verified_e2e_ms=%.3f", i, o, r, o+r, e)
	}
	stats("warm_read_open", opens)
	stats("warm_read_consume_close", consumes)
	stats("warm_read_total", totals)
	stats("warm_read_verified_e2e", verified)
	var writes, roundTrips []float64
	for i := range 20 {
		start := time.Now()
		written, elapsed := put(int64(i + 2))
		read(written) // every write independently read back and hashed; no byte cache
		roundTrip := float64(time.Since(start)) / 1e6
		writes = append(writes, elapsed)
		roundTrips = append(roundTrips, roundTrip)
		t.Logf("warm_write iteration=%d put_ms=%.3f verified_roundtrip_ms=%.3f", i, elapsed, roundTrip)
	}
	stats("warm_write_put", writes)
	stats("warm_write_verified_roundtrip", roundTrips)
	// No wrapper participates in the clean latency samples above.
	trace, spy := filepath.Join(root, "docker-trace"), filepath.Join(root, "docker-spy")
	script := "#!/bin/sh\ncase \"$1\" in container|volume|image) printf '%s %s\\n' \"$1\" \"$2\" ;; *) printf '%s\\n' \"$1\" ;; esac >> " + quoteShell(trace) + "\nexec " + quoteShell(runtime) + " \"$@\"\n"
	if err := os.WriteFile(spy, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	db.cfg.ContainerRuntimePath = spy
	for i := range 5 {
		written, _ := put(int64(i + 22))
		read(written)
	}
	db.cfg.ContainerRuntimePath = runtime
	raw, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		key := fields[0]
		if len(fields) > 1 && (key == "container" || key == "volume" || key == "image") {
			key += " " + fields[1]
		}
		counts[key]++
		if key == "container create" || key == "create" || key == "run" {
			t.Fatalf("warm helper creation: %s", line)
		}
	}
	t.Logf("UNTIMED_COUNTS five_write_read_pairs=%s warm_creates=0", fmt.Sprint(counts))
}
