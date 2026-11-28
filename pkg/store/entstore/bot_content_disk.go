package entstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

// prepareBotContentDirectory is startup-only provisioning inside the explicitly
// configured persistent DataDir. Reads never invoke it.
func prepareBotContentDirectory(directory string) error {
	if !filepath.IsAbs(directory) {
		return store.ErrBotUnsupported
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer root.Close()
	if err = root.Mkdir("bot-content", 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := root.Lstat("bot-content")
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return store.ErrBotForbidden
	}
	file, err := root.Open(".")
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

// BotContentStaging opens an explicitly provisioned directory in the configured
// control-plane DataDir. Missing storage is an error, never a temporary fallback.
func (s *Store) BotContentStaging(ctx context.Context, scope store.BotContentScope) (store.BotContentStaging, error) {
	if scope.Validate() != nil || s == nil || !filepath.IsAbs(s.dataDir) {
		return nil, store.ErrBotUnsupported
	}
	p, ok := execution.PrincipalFromContext(ctx)
	if !ok || p.Validate() != nil || p.Kind != execution.PrincipalKindUser || p.OrgSlug != scope.OrgID || p.TeamSlug != scope.TeamID || (scope.OwnerID != "" && scope.OwnerID != p.Subject) {
		return nil, store.ErrBotForbidden
	}
	user, err := s.Users().GetByID(ctx, p.Subject)
	if err != nil || user == nil || user.Status != "active" {
		return nil, store.ErrBotForbidden
	}
	org, err := s.Organizations().GetBySlug(ctx, p.OrgSlug)
	if err != nil || org == nil {
		return nil, store.ErrBotForbidden
	}
	role, err := s.Organizations().GetMemberRole(ctx, p.Subject, org.ID)
	if err != nil || (role != "owner" && role != "admin" && role != "member") {
		return nil, store.ErrBotForbidden
	}
	data, err := s.ForOrg(p.OrgSlug)
	if err != nil {
		return nil, err
	}
	team, err := data.Teams().GetTeamBySlug(ctx, p.TeamSlug)
	if err != nil || team == nil {
		return nil, store.ErrBotForbidden
	}
	role, err = data.Teams().GetMemberRole(ctx, p.Subject, team.ID)
	if err != nil || (role != "owner" && role != "admin" && role != "member") {
		return nil, store.ErrBotForbidden
	}
	disk, err := newBotDiskContent(filepath.Join(s.dataDir, "bot-content"), scope)
	if err != nil {
		return nil, err
	}
	disk.authorize = func(live context.Context) error {
		current, err := s.BotContentStaging(live, scope)
		if err != nil {
			return err
		}
		return current.Close()
	}
	return disk, nil
}

// botDiskContent is a trusted, scope-bound control-plane repository for content
// created before Home registration and for team-owned templates. The configured
// durable root is mandatory; no temporary-directory or SQL fallback exists.
// It is never mounted into a Bot runtime or exposed by the working-file tree.
type botDiskContent struct {
	authorize func(context.Context) error
	root      *os.Root
	scope     store.BotContentScope
}

func newBotDiskContent(directory string, scope store.BotContentScope) (*botDiskContent, error) {
	if !filepath.IsAbs(directory) || scope.Validate() != nil {
		return nil, store.ErrBotInvalid
	}
	// The caller supplies a provisioned control-plane directory. Constructor and
	// reads do not create it, and a symlink is not a configured durable root.
	info, err := os.Lstat(directory)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, store.ErrBotInvalid
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	return &botDiskContent{root: root, scope: scope}, nil
}

func (d *botDiskContent) check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if d.authorize != nil {
		return d.authorize(ctx)
	}
	return nil
}

func (d *botDiskContent) Close() error { return d.root.Close() }
func (d *botDiskContent) namespace() string {
	raw, _ := json.Marshal(d.scope)
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

func (d *botDiskContent) scopeRoot(create bool) (*os.Root, error) {
	name := d.namespace()
	if create {
		if err := d.root.Mkdir(name, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, err
		}
	}
	info, err := d.root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, store.ErrBotForbidden
	}
	child, err := d.root.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	opened, err := child.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		child.Close()
		return nil, store.ErrBotConflict
	}
	return child, nil
}

func diskContentName(ref store.BotContentRef) (string, error) {
	name := strings.TrimPrefix(ref.Key, "staged-v1/")
	prefix := string(ref.Kind) + "-" + strconv.FormatInt(ref.Revision, 10) + "-"
	if name == ref.Key || !strings.HasPrefix(name, prefix) {
		return "", store.ErrBotInvalid
	}
	id := strings.TrimPrefix(name, prefix)
	parsed, err := uuid.Parse(id)
	if err != nil || parsed.String() != id {
		return "", store.ErrBotInvalid
	}
	return name, nil
}

// Put stages immutable bytes in the same filesystem and exposes the reference
// only after a trusted commit barrier, fsync and readback verification.
func (d *botDiskContent) Put(ctx context.Context, kind store.BotContentKind, revision, size int64, body io.Reader, barrier func(func() error) error) (*store.BotContentRef, error) {
	if err := d.check(ctx); err != nil {
		return nil, err
	}
	if body == nil || barrier == nil {
		return nil, store.ErrBotForbidden
	}
	ref := store.BotContentRef{Format: store.BotContentFormatVersion, Scope: d.scope, Kind: kind, Revision: revision, SizeBytes: size, SHA256: strings.Repeat("0", 64), Key: "staged-v1/" + string(kind) + "-" + strconv.FormatInt(revision, 10) + "-" + uuid.NewString()}
	if err := ref.Validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Cover the stream and barrier even outside SQL. Nonblocking contention
	// avoids inversion with maintenance's root SQL write fence.
	publication, err := d.lockPublication(ctx)
	if err != nil {
		return nil, err
	}
	defer publication.Close()
	root, err := d.scopeRoot(true)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	name, err := diskContentName(ref)
	if err != nil {
		return nil, err
	}
	temp := ".stage-" + uuid.NewString()
	file, err := root.OpenFile(temp, os.O_RDWR|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	defer root.Remove(temp)
	hash := sha256.New()
	reader := &botContentContextReader{ctx: ctx, reader: body}
	if _, err = io.CopyN(io.MultiWriter(file, hash), reader, size); err != nil {
		return nil, err
	}
	ref.SHA256 = hex.EncodeToString(hash.Sum(nil))
	if err = file.Sync(); err != nil {
		return nil, err
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	verify := sha256.New()
	if _, err = io.Copy(verify, &botContentContextReader{ctx: ctx, reader: file}); err != nil {
		return nil, err
	}
	if hex.EncodeToString(verify.Sum(nil)) != ref.SHA256 {
		return nil, store.ErrBotConflict
	}
	called := false
	var commitErr error
	err = barrier(func() (result error) {
		defer func() { commitErr = errors.Join(commitErr, result) }()
		if called {
			return store.ErrBotConflict
		}
		called = true
		if err := ctx.Err(); err != nil {
			return err
		}
		// Hard-link publication is atomic and refuses an existing object. After
		// unlinking our stage the final immutable object has exactly one link.
		if err := root.Link(temp, name); err != nil {
			return err
		}
		if err := root.Remove(temp); err != nil {
			return err
		}
		dir, err := root.Open(".")
		if err != nil {
			return err
		}
		defer dir.Close()
		return dir.Sync()
	})
	if err = errors.Join(err, commitErr); err != nil {
		return nil, err
	}
	if !called {
		return nil, store.ErrBotForbidden
	}
	return &ref, nil
}

type botContentContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *botContentContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

type botDiskContentStream struct {
	reader io.Reader
	file   *os.File
	stop   func() bool
	once   sync.Once
	err    error
}

func (s *botDiskContentStream) Read(p []byte) (int, error) { return s.reader.Read(p) }
func (s *botDiskContentStream) Close() error {
	s.once.Do(func() { s.stop(); s.err = s.file.Close() })
	return s.err
}

func (d *botDiskContent) Open(ctx context.Context, ref store.BotContentRef, span store.BotFileRange) (*store.BotFileStream, error) {
	if err := d.check(ctx); err != nil {
		return nil, err
	}
	if ref.Scope != d.scope {
		return nil, store.ErrBotForbidden
	}
	if ref.Validate() != nil || span.Validate() != nil {
		return nil, store.ErrBotInvalid
	}
	name, err := diskContentName(ref)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := d.scopeRoot(false)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	file, err := root.OpenFile(name, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	var stat unix.Stat_t
	if err = unix.Fstat(int(file.Fd()), &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || stat.Size != ref.SizeBytes {
		file.Close()
		return nil, store.ErrBotConflict
	}
	stream := &botDiskContentStream{file: file}
	stream.stop = context.AfterFunc(ctx, func() { _ = file.Close() })
	fail := func(e error) (*store.BotFileStream, error) { _ = stream.Close(); return nil, e }
	hash := sha256.New()
	if _, err = io.Copy(hash, &botContentContextReader{ctx: ctx, reader: file}); err != nil {
		return fail(err)
	}
	if hex.EncodeToString(hash.Sum(nil)) != ref.SHA256 {
		return fail(store.ErrBotConflict)
	}
	if span.Offset > ref.SizeBytes {
		return fail(store.ErrBotInvalid)
	}
	length := ref.SizeBytes - span.Offset
	if span.Length > 0 && span.Length < length {
		length = span.Length
	}
	stream.reader = &botContentContextReader{ctx: ctx, reader: io.NewSectionReader(file, span.Offset, length)}
	return &store.BotFileStream{ReadCloser: stream, Entry: store.BotFileEntry{Path: ref.Key, Kind: store.BotFileRegular, SizeBytes: ref.SizeBytes, SHA256: ref.SHA256, Preview: store.BotFilePreviewDownload}}, nil
}

// Bind copies staged Bot-owned content into the already registered retained
// Home. It never removes the staging object: the caller must commit replacement
// references first, then explicitly authorize cleanup. Team templates stay here.
func (d *botDiskContent) Bind(ctx context.Context, ref store.BotContentRef, files store.BotFiles, barrier func(func() error) error) (*store.BotContentRef, error) {
	if files == nil || ref.Scope.BotID == uuid.Nil || ref.Scope != d.scope || barrier == nil {
		return nil, store.ErrBotForbidden
	}
	stream, err := d.Open(ctx, ref, store.BotFileRange{})
	if err != nil {
		return nil, err
	}
	next, putErr := files.PutContent(ctx, ref.Scope.BotID, ref.Kind, ref.SizeBytes, stream, ref.Revision, barrier)
	closeErr := stream.Close()
	if err = errors.Join(putErr, closeErr); err != nil {
		return nil, err
	}
	if next == nil || next.Validate() != nil || next.Scope != ref.Scope || next.Kind != ref.Kind || next.Revision != ref.Revision || next.SizeBytes != ref.SizeBytes || next.SHA256 != ref.SHA256 {
		return nil, store.ErrBotConflict
	}
	// Resolve the trusted destination once, before allowing SQL to select it.
	copied, err := files.OpenContent(ctx, *next, store.BotFileRange{})
	if err != nil {
		return nil, err
	}
	hash := sha256.New()
	n, readErr := io.Copy(hash, copied)
	closeErr = copied.Close()
	if err = errors.Join(readErr, closeErr); err != nil {
		return nil, err
	}
	if n != ref.SizeBytes || hex.EncodeToString(hash.Sum(nil)) != ref.SHA256 {
		return nil, store.ErrBotConflict
	}
	return next, nil
}

// Remove requires explicit trusted ownership/reference-closure authorization.
// It never enumerates another namespace or deletes a whole control-plane root.
func (d *botDiskContent) Remove(ctx context.Context, ref store.BotContentRef, barrier func(func() error) error) error {
	if err := d.check(ctx); err != nil {
		return err
	}
	if barrier == nil || ref.Scope != d.scope {
		return store.ErrBotForbidden
	}
	if ref.Validate() != nil {
		return store.ErrBotInvalid
	}
	name, err := diskContentName(ref)
	if err != nil {
		return err
	}
	root, err := d.scopeRoot(false)
	if err != nil {
		return err
	}
	defer root.Close()
	return barrier(func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		dir, err := root.Open(".")
		if err != nil {
			return err
		}
		defer dir.Close()
		return dir.Sync()
	})
}
