package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// BotFileRef is a remote identity, never a host path. A nonzero ArtifactID
// selects immutable published bytes rather than the mutable working file.
type BotFileRef struct {
	BotID          uuid.UUID
	HomeID         uuid.UUID
	HomeSessionID  uuid.UUID
	Path           string
	RunID          uuid.UUID
	ReportSequence int64
	ArtifactID     uuid.UUID
	Freshness      string
	SHA256         string
}
type BotFilePreview struct {
	Entry     store.BotFileEntry `json:"entry"`
	Content   string             `json:"content"`
	Truncated bool               `json:"truncated"`
}

func (b Bots) filePath(ref BotFileRef, action string) (string, error) {
	if ref.BotID == uuid.Nil || (b.Scope != store.BotScopePersonal && b.Scope != store.BotScopeTeam) {
		return "", store.ErrBotInvalid
	}
	suffix := "/" + ref.BotID.String() + "/files/" + action
	if ref.ArtifactID != uuid.Nil {
		if ref.RunID == uuid.Nil || ref.ReportSequence < 1 {
			return "", store.ErrBotInvalid
		}
		suffix = fmt.Sprintf("/%s/runs/%s/reports/%d/artifacts/%s/%s", ref.BotID, ref.RunID, ref.ReportSequence, ref.ArtifactID, action)
	}
	return b.path(suffix) + "&path=" + url.QueryEscape(ref.Path), nil
}
func (b Bots) ListBotFiles(ctx context.Context, id uuid.UUID, path, cursor string) (*store.BotFilePage, error) {
	var page store.BotFilePage
	err := b.request(ctx, "GET", b.path("/"+id.String()+"/files")+"&path="+url.QueryEscape(path)+"&cursor="+url.QueryEscape(cursor)+"&limit=100", nil, &page)
	return &page, err
}
func (b Bots) ReadBotFilePreview(ctx context.Context, ref BotFileRef) (*BotFilePreview, error) {
	path, err := b.filePath(ref, "preview")
	if err != nil {
		return nil, err
	}
	resp, err := b.Client.doBotRequest(ctx, "GET", path, nil, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &BotResponseError{StatusCode: resp.StatusCode, Cause: parseErrorResponse(resp)}
	}
	// JSON may escape every byte, but no response may force an unbounded allocation.
	data, err := io.ReadAll(io.LimitReader(resp.Body, 6*store.BotFilePreviewMaxBytes+65537))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > 6*store.BotFilePreviewMaxBytes+65536 {
		return nil, fmt.Errorf("preview response too large")
	}
	var p BotFilePreview
	if err = json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	if int64(len(p.Content)) > store.BotFilePreviewMaxBytes {
		return nil, fmt.Errorf("preview too large")
	}
	if ref.Freshness != "" && p.Entry.Freshness != ref.Freshness {
		return nil, store.ErrBotConflict
	}
	return &p, nil
}

// DownloadBotFile streams to a private sibling temporary file. Linking the fully
// synced file into place is an atomic no-replace commit (unlike os.Rename, which
// silently overwrites on Unix). Cancellation/error only removes our own partial.
func (b Bots) DownloadBotFile(ctx context.Context, ref BotFileRef, destination string) error {
	if destination == "" {
		return fmt.Errorf("choose a destination")
	}
	if _, err := os.Lstat(destination); err == nil {
		return fmt.Errorf("destination exists; choose another name")
	} else if !os.IsNotExist(err) {
		return err
	}
	// Mutable files must match the reviewed listing before and after streaming.
	checkFreshness := func() error {
		if ref.ArtifactID != uuid.Nil || ref.Freshness == "" {
			return nil
		}
		statPath, e := b.filePath(ref, "stat")
		if e != nil {
			return e
		}
		var entry store.BotFileEntry
		if e = b.request(ctx, "GET", statPath, nil, &entry); e != nil {
			return e
		}
		if entry.Freshness != ref.Freshness {
			return store.ErrBotConflict
		}
		return nil
	}
	if err := checkFreshness(); err != nil {
		return err
	}
	path, err := b.filePath(ref, "download")
	if err != nil {
		return err
	}
	resp, err := b.Client.doBotRequest(ctx, "GET", path, nil, false)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return &BotResponseError{StatusCode: resp.StatusCode, Cause: parseErrorResponse(resp)}
	}
	f, err := os.CreateTemp(filepath.Dir(destination), ".astonish-download-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	hash := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, hash), resp.Body)
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if ref.SHA256 != "" && hex.EncodeToString(hash.Sum(nil)) != ref.SHA256 {
		return fmt.Errorf("download digest changed")
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = checkFreshness(); err != nil {
		return err
	}
	return os.Link(f.Name(), destination)
}
