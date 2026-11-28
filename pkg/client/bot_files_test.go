package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotFilesStreamingAndNoOverwrite(t *testing.T) {
	data := bytes.Repeat([]byte{0, 255, 42, 1}, (17<<20)/4)
	sum := sha256.Sum256(data)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Query().Get("scope") != "team" || r.Header.Get("Authorization") != "Bearer token" {
			t.Error("scope/auth/method")
		}
		w.Write(data)
	}))
	defer s.Close()
	c := NewWithConfig(s.URL)
	c.cached = &Tokens{AccessToken: "token"}
	b := Bots{Client: c, Scope: store.BotScopeTeam}
	ref := BotFileRef{BotID: uuid.New(), Path: "large.bin", SHA256: hex.EncodeToString(sum[:])}
	dest := filepath.Join(t.TempDir(), "saved.bin")
	if err := b.DownloadBotFile(context.Background(), ref, dest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("bytes differ", err)
	}
	if err := b.DownloadBotFile(context.Background(), ref, dest); err == nil {
		t.Fatal("overwrite allowed")
	}
	files, _ := filepath.Glob(filepath.Join(filepath.Dir(dest), ".astonish-download-*"))
	if len(files) != 0 {
		t.Fatal(files)
	}
}
func TestBotFilesPreviewBoundAndStatus(t *testing.T) {
	for _, status := range []int{200, 403, 409, 501} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				json.NewEncoder(w).Encode(BotFilePreview{Content: strings.Repeat("x", int(store.BotFilePreviewMaxBytes)+1)})
			}))
			defer s.Close()
			b := Bots{Client: NewWithConfig(s.URL), Scope: store.BotScopePersonal}
			_, err := b.ReadBotFilePreview(context.Background(), BotFileRef{BotID: uuid.New(), Path: "MEMORY.md"})
			if err == nil {
				t.Fatal("accepted oversized/error")
			}
			if status != 200 {
				var response *BotResponseError
				if !errors.As(err, &response) || response.StatusCode != status {
					t.Fatal(err)
				}
			}
		})
	}
}
func TestBotFilesCancelledRemovesOnlyPartial(t *testing.T) {
	started := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("partial"))
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	defer s.Close()
	b := Bots{Client: NewWithConfig(s.URL), Scope: store.BotScopePersonal}
	dir := t.TempDir()
	dest := filepath.Join(dir, "cancel.bin")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.DownloadBotFile(ctx, BotFileRef{BotID: uuid.New(), Path: "large.bin"}, dest) }()
	<-started
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled success")
		}
	case <-time.After(time.Second):
		t.Fatal("did not cancel")
	}
	files, _ := os.ReadDir(dir)
	if len(files) != 0 {
		t.Fatal(files)
	}
}
