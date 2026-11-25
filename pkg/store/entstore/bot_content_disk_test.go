package entstore

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

func TestBotContentDiskReopenAndScope(t *testing.T) {
	root := t.TempDir()
	scope := store.BotContentScope{OrgID: "org", TeamID: "team", OwnerID: uuid.NewString(), BotID: uuid.New()}
	disk, err := newBotDiskContent(root, scope)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.Repeat([]byte("retained\x00"), 2<<20)
	barrier := func(f func() error) error { return f() }
	ref, err := disk.Put(t.Context(), store.BotContentDocument, 1, int64(len(body)), bytes.NewReader(body), barrier)
	if err != nil {
		t.Fatal(err)
	}
	if err = disk.Close(); err != nil {
		t.Fatal(err)
	}
	disk, err = newBotDiskContent(root, scope)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	stream, err := disk.Open(t.Context(), *ref, store.BotFileRange{Offset: 11, Length: 200})
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(stream)
	if err != nil {
		t.Fatal(err)
	}
	if err = stream.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body[11:211]) {
		t.Fatal("range changed")
	}
	foreign := *ref
	foreign.Scope.BotID = uuid.New()
	if _, err = disk.Open(t.Context(), foreign, store.BotFileRange{}); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatal("foreign scope", err)
	}
	if err = disk.Remove(t.Context(), *ref, nil); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatal("unguarded deletion", err)
	}
	if err = disk.Remove(t.Context(), *ref, barrier); err != nil {
		t.Fatal(err)
	}
	if _, err = disk.Open(t.Context(), *ref, store.BotFileRange{}); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("deleted object accessible", err)
	}
}

func TestBotContentDiskRejectsUnsafeAndIncomplete(t *testing.T) {
	root := t.TempDir()
	scope := store.BotContentScope{OrgID: "org", TeamID: "team"}
	disk, err := newBotDiskContent(root, scope)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	barrier := func(f func() error) error { return f() }
	for _, mode := range []string{"short", "denied", "cancelled", "no-commit", "suppressed-error"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			size := int64(3)
			guard := barrier
			switch mode {
			case "short":
				size = 4
			case "denied":
				guard = func(func() error) error { return store.ErrBotForbidden }
			case "cancelled":
				guard = func(f func() error) error { cancel(); return f() }
			case "suppressed-error":
				guard = func(f func() error) error { cancel(); _ = f(); return nil }
			case "no-commit":
				guard = func(func() error) error { return nil }
			}
			if ref, e := disk.Put(ctx, store.BotContentTemplate, 1, size, strings.NewReader("abc"), guard); e == nil || ref != nil {
				t.Fatal("invalid commit accepted")
			}
		})
	}
	entries, err := os.ReadDir(filepath.Join(root, disk.namespace()))
	if err != nil || len(entries) != 0 {
		t.Fatal("failed staging left objects", entries, err)
	}
	ref, err := disk.Put(t.Context(), store.BotContentTemplate, 1, 3, strings.NewReader("abc"), barrier)
	if err != nil {
		t.Fatal(err)
	}
	name, err := diskContentName(*ref)
	if err != nil {
		t.Fatal(err)
	}
	physical := filepath.Join(root, disk.namespace(), name)
	for _, mode := range []string{"corrupt", "hardlink", "symlink", "fifo"} {
		t.Run(mode, func(t *testing.T) {
			if err := os.Remove(physical); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "corrupt":
				err = os.WriteFile(physical, []byte("bad"), 0600)
			case "hardlink":
				source := filepath.Join(t.TempDir(), "source")
				if err = os.WriteFile(source, []byte("abc"), 0600); err == nil {
					err = os.Link(source, physical)
				}
			case "symlink":
				err = os.Symlink("/etc/passwd", physical)
			case "fifo":
				err = unix.Mkfifo(physical, 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if s, e := disk.Open(t.Context(), *ref, store.BotFileRange{}); e == nil {
				s.Close()
				t.Fatal("unsafe object accepted")
			}
		})
	}
}

type diskBindFiles struct {
	store.BotFiles
	ref     store.BotContentRef
	body    []byte
	corrupt bool
}

func (f *diskBindFiles) PutContent(_ context.Context, _ store.BotID, _ store.BotContentKind, _ int64, body io.Reader, _ int64, barrier func(func() error) error) (*store.BotContentRef, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return nil, err
	}
	if err = barrier(func() error { f.body = data; return nil }); err != nil {
		return nil, err
	}
	copy := f.ref
	copy.Key = "content-v1/bound"
	return &copy, nil
}
func (f *diskBindFiles) OpenContent(_ context.Context, _ store.BotContentRef, _ store.BotFileRange) (*store.BotFileStream, error) {
	data := f.body
	if f.corrupt {
		data = []byte("corrupt")
	}
	return &store.BotFileStream{ReadCloser: io.NopCloser(bytes.NewReader(data))}, nil
}
func TestBotContentDiskBindChecksDestination(t *testing.T) {
	scope := store.BotContentScope{OrgID: "org", TeamID: "team", OwnerID: uuid.NewString(), BotID: uuid.New()}
	disk, err := newBotDiskContent(t.TempDir(), scope)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	barrier := func(f func() error) error { return f() }
	ref, err := disk.Put(t.Context(), store.BotContentDocument, 1, 3, strings.NewReader("abc"), barrier)
	if err != nil {
		t.Fatal(err)
	}
	files := &diskBindFiles{ref: *ref}
	bound, err := disk.Bind(t.Context(), *ref, files, barrier)
	if err != nil || bound.Key == ref.Key {
		t.Fatal("bind", bound, err)
	}
	files.corrupt = true
	if _, err = disk.Bind(t.Context(), *ref, files, barrier); !errors.Is(err, store.ErrBotConflict) {
		t.Fatal("corrupt destination accepted", err)
	}
	original, err := disk.Open(t.Context(), *ref, store.BotFileRange{})
	if err != nil {
		t.Fatal("staging removed before SQL selection", err)
	}
	original.Close()
}

func TestBotContentDiskRequiresDurableRoot(t *testing.T) {
	scope := store.BotContentScope{OrgID: "org", TeamID: "team"}
	root := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", "relative", filepath.Join(root, "absent"), link} {
		if d, e := newBotDiskContent(path, scope); e == nil {
			d.Close()
			t.Fatal("invalid root accepted", path)
		}
	}
}
