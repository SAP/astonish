package client

import (
	"context"
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

func TestBotFilesDestinationLinksAndConcurrentCreation(t *testing.T) {
	for _, kind := range []string{"symlink", "dangling-symlink", "hardlink", "concurrent-hardlink"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			target, dest := filepath.Join(dir, "existing"), filepath.Join(dir, "saved")
			if err := os.WriteFile(target, []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			create := func() {
				var err error
				switch kind {
				case "symlink":
					err = os.Symlink(target, dest)
				case "dangling-symlink":
					err = os.Symlink(filepath.Join(dir, "missing"), dest)
				default:
					err = os.Link(target, dest)
				}
				if err != nil {
					t.Error(err)
				}
			}
			if kind != "concurrent-hardlink" {
				create()
			}
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if kind == "concurrent-hardlink" {
					create()
				} else {
					t.Error("existing destination fetched")
				}
				_, _ = w.Write([]byte("replacement"))
			}))
			defer s.Close()
			b := Bots{Client: NewWithConfig(s.URL), Scope: store.BotScopePersonal}
			if err := b.DownloadBotFile(context.Background(), BotFileRef{BotID: uuid.New(), Path: "file"}, dest); err == nil {
				t.Fatal("overwrote link")
			}
			got, err := os.ReadFile(target)
			if err != nil || string(got) != "keep" {
				t.Fatal("target changed", err)
			}
			if _, err := os.Lstat(dest); err != nil {
				t.Fatal("destination removed", err)
			}
			partial, _ := filepath.Glob(filepath.Join(dir, ".astonish-download-*"))
			if len(partial) != 0 {
				t.Fatal(partial)
			}
		})
	}
}

func TestBotFilesAbortAfterPartialCreatedKeepsUnrelatedFile(t *testing.T) {
	started := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("partial", 8192)))
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	defer s.Close()
	dir := t.TempDir()
	unrelated := filepath.Join(dir, ".astonish-download-not-ours")
	if err := os.WriteFile(unrelated, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	b := Bots{Client: NewWithConfig(s.URL), Scope: store.BotScopePersonal}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- b.DownloadBotFile(ctx, BotFileRef{BotID: uuid.New()}, filepath.Join(dir, "save")) }()
	<-started
	deadline := time.Now().Add(3 * time.Second)
	partialCreated := false
	for time.Now().Before(deadline) {
		files, _ := os.ReadDir(dir)
		for _, f := range files {
			if f.Name() != filepath.Base(unrelated) {
				info, _ := f.Info()
				if info.Size() > 0 {
					partialCreated = true
				}
			}
		}
		if partialCreated {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if !partialCreated {
		t.Fatal("did not observe partial write")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("abort blocked")
	}
	files, _ := os.ReadDir(dir)
	if len(files) != 1 || files[0].Name() != filepath.Base(unrelated) {
		t.Fatal("removed unrelated file or leaked partial", files)
	}
}

func TestBotFilesListAndDownloadPreserveStatus(t *testing.T) {
	for _, status := range []int{403, 404, 409, 410, 415, 501, 503} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "unavailable", status) }))
		b := Bots{Client: NewWithConfig(s.URL), Scope: store.BotScopePersonal}
		_, listErr := b.ListBotFiles(context.Background(), uuid.New(), "", "")
		downloadErr := b.DownloadBotFile(context.Background(), BotFileRef{BotID: uuid.New()}, filepath.Join(t.TempDir(), "save"))
		for _, err := range []error{listErr, downloadErr} {
			var response *BotResponseError
			if !errors.As(err, &response) || response.StatusCode != status {
				t.Fatalf("status %d lost: %v", status, err)
			}
		}
		s.Close()
	}
}
