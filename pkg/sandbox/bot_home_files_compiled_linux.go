//go:build linux

package sandbox

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

// RunBotHomeFiles executes one internal retained-file operation. Roots are opened
// by trusted transport, never supplied by a model tool. It does not own the roots.
// Errors after binary framing are returned only as process exit errors, never
// appended to the body. Callers MUST join process exit even after consuming bytes.
func RunBotHomeFiles(home, private *os.File, input io.Reader, output io.Writer) (err error) {
	p := &compiledHomeFiles{home: home, private: private, in: bufio.NewReaderSize(input, 65537), out: output}
	defer func() {
		if err != nil && !p.streaming {
			code := BotHomeFileFailed
			if errors.Is(err, os.ErrNotExist) {
				code = BotHomeFileNotFound
			}
			if errors.Is(err, errHomeForbidden) || errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) || errors.Is(err, unix.EXDEV) || errors.Is(err, unix.EACCES) {
				code = BotHomeFileForbidden
			}
			err = errors.Join(err, p.emit(map[string]any{"error": BotHomeFileError{Code: code}}))
		}
	}()
	line, e := p.in.ReadSlice('\n')
	if e != nil || len(line) > 65536 {
		return errors.New("invalid file request frame")
	}
	if e = json.Unmarshal(line, &p.q); e != nil {
		return e
	}
	switch p.q.Op {
	case "list":
		return p.list()
	case "stat", "open":
		return p.read()
	case "write", "put", "snapshot":
		return p.write()
	default:
		return errors.New("invalid file operation")
	}
}

// OpenBotHomeFileRoots validates an existing scoped PVC attachment. It never
// allocates a Home; only an explicitly mutating operation may create the private
// child. The mount descriptor and expected owner are supplied by the backend.
// The caller owns returned descriptors; the input mount remains caller-owned.
func OpenBotHomeFileRoots(mount *os.File, owner string, needPrivate, createPrivate bool) (home, private *os.File, err error) {
	// Kubernetes persists the versioned reference verbatim in .owner. Validate
	// its digest without stripping the prefix from the exact on-disk comparison.
	digest := strings.TrimPrefix(owner, "k8s-home-v1:")
	if mount == nil || len(digest) != 64 || (createPrivate && !needPrivate) {
		return nil, nil, errHomeForbidden
	}
	if _, e := hex.DecodeString(digest); e != nil {
		return nil, nil, errHomeForbidden
	}
	f, err := homeFile(mount, ".owner")
	if err != nil {
		return nil, nil, err
	}
	s, err := homeStat(f, true)
	if err == nil && s.Size != int64(len(owner)+1) {
		err = errHomeForbidden
	}
	if err == nil {
		var raw []byte
		raw, err = io.ReadAll(io.LimitReader(f, 129))
		if err == nil && string(raw) != owner+"\n" {
			err = errHomeForbidden
		}
		if err == nil {
			err = homeUnchanged(f, s)
		}
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return nil, nil, err
	}
	home, err = homeOpen(mount, "data", unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, nil, err
	}
	if !needPrivate {
		return home, nil, nil
	}
	if createPrivate {
		err = unix.Mkdirat(int(mount.Fd()), "workspace-private", 0700)
		if errors.Is(err, unix.EEXIST) {
			err = nil
		}
		if err == nil {
			err = mount.Sync()
		}
	}
	if err == nil {
		private, err = homeOpen(mount, "workspace-private", unix.O_RDONLY|unix.O_DIRECTORY, 0)
	}
	if err != nil {
		_ = home.Close()
		return nil, nil, err
	}
	return home, private, nil
}

var errHomeForbidden = errors.New("forbidden retained path")

type homeFileRequest struct {
	Op         string  `json:"op"`
	Path       string  `json:"path"`
	After      string  `json:"after"`
	Limit      int     `json:"limit"`
	Private    bool    `json:"private"`
	Size       *int64  `json:"size"`
	SHA256     *string `json:"sha256"`
	Offset     int64   `json:"offset"`
	Length     int64   `json:"length"`
	Kind       string  `json:"kind"`
	Revision   int64   `json:"revision"`
	CreateOnly bool    `json:"create_only"`
	Expected   string  `json:"expected"`
	Readback   bool    `json:"readback"`
}
type compiledHomeFiles struct {
	home, private *os.File
	in            *bufio.Reader
	out           io.Writer
	q             homeFileRequest
	streaming     bool
}

func (p *compiledHomeFiles) emit(v any) error { return json.NewEncoder(p.out).Encode(v) }

// openat2 enforces mount identity (including bind mounts on the same device),
// no symlinks and beneath confinement atomically with lookup. Fail closed on
// kernels without this primitive rather than falling back to path-based opens.
func homeOpen(root *os.File, name string, flags int, mode uint32) (*os.File, error) {
	if root == nil {
		return nil, errHomeForbidden
	}
	fd, err := unix.Openat2(int(root.Fd()), name, &unix.OpenHow{Flags: uint64(flags | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK), Mode: uint64(mode), Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_XDEV})
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

type homeStagePrefix string

const (
	homeFileStagePrefix      homeStagePrefix = ".stage-"
	homeWorkspaceStagePrefix homeStagePrefix = ".tmp-"
)

// homeInternalStage recognizes only the exact names generated by the compiled
// writers, not arbitrary dotfiles or user names beginning with .stage-/.tmp-.
// These names are reserved in every path component. Cooperative input closure
// unwinds each writer's unlink defer. After SIGKILL, the next authorized file
// write reclaims exact .stage-* regular files in its destination directory under
// the existing exclusive directory flock. Reads never reclaim; .tmp-* workspace
// stages have a different lifetime/lock protocol and are not swept here.
func homeInternalStage(name string) bool {
	if suffix, ok := strings.CutPrefix(name, string(homeFileStagePrefix)); ok {
		if len(suffix) != 32 {
			return false
		}
		for _, c := range suffix {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				return false
			}
		}
		return true
	}
	if suffix, ok := strings.CutPrefix(name, string(homeWorkspaceStagePrefix)); ok {
		id, err := uuid.Parse(suffix)
		return err == nil && id.String() == suffix
	}
	return false
}

// homeReclaimFileStages requires the caller's exclusive destination-directory
// flock, held by every compiled file writer until its stage is gone. Thus no
// compatible active writer can own a stage here; no PID, age, receipt or editable
// content supplies authority. Never recurse or reclaim the workspace's .tmp-*.
func homeReclaimFileStages(d *os.File) error {
	scan, err := homeOpen(d, ".", unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return err
	}
	defer scan.Close()
	remaining := 8192
	changed := false
	for {
		names, e := scan.Readdirnames(128)
		if e != nil && e != io.EOF {
			return e
		}
		for _, name := range names {
			remaining--
			if remaining < 0 {
				return errors.New("directory quota")
			}
			if !strings.HasPrefix(name, string(homeFileStagePrefix)) || !homeInternalStage(name) {
				continue
			}
			f, err := homeOpen(d, name, unix.O_RDONLY, 0)
			if err != nil {
				return err
			}
			s, err := homeStat(f, true)
			if err == nil {
				var current unix.Stat_t
				err = unix.Fstatat(int(d.Fd()), name, &current, unix.AT_SYMLINK_NOFOLLOW)
				if err == nil && !homeIdentity(s, current) {
					err = errors.New("stage changed during recovery")
				}
			}
			if err == nil {
				err = unix.Unlinkat(int(d.Fd()), name, 0)
			}
			err = errors.Join(err, f.Close())
			if err != nil {
				return err
			}
			changed = true
		}
		if e == io.EOF {
			break
		}
	}
	if changed {
		return d.Sync()
	}
	return nil
}

func homeParts(s string, rootok bool) ([]string, error) {
	if s == "." && rootok {
		return nil, nil
	}
	if ValidateBotHomeArtifactPath(s) != nil || len(s) > 4096 {
		return nil, errHomeForbidden
	}
	for _, c := range s {
		if c < 32 || c == 127 {
			return nil, errHomeForbidden
		}
	}
	parts := strings.Split(s, "/")
	if len(parts) > 64 {
		return nil, errHomeForbidden
	}
	for _, part := range parts {
		if len(part) > 255 || homeInternalStage(part) {
			return nil, errHomeForbidden
		}
	}
	return parts, nil
}
func homeDir(root *os.File, parts []string, create bool) (*os.File, error) {
	d, err := homeOpen(root, ".", unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, err
	}
	for i, name := range parts {
		if create {
			if err = unix.Mkdirat(int(d.Fd()), name, 0700); err != nil && !errors.Is(err, unix.EEXIST) {
				d.Close()
				return nil, err
			}
			if err = d.Sync(); err != nil {
				d.Close()
				return nil, err
			}
		}
		// Resolve from the original root, not the previous descriptor: this also
		// detects mounts introduced above a previously opened parent.
		next, e := homeOpen(root, strings.Join(parts[:i+1], "/"), unix.O_RDONLY|unix.O_DIRECTORY, 0)
		d.Close()
		if e != nil {
			return nil, e
		}
		d = next
	}
	return d, nil
}
func homeStat(f *os.File, regular bool) (unix.Stat_t, error) {
	var s unix.Stat_t
	err := unix.Fstat(int(f.Fd()), &s)
	if err == nil && regular && (s.Mode&unix.S_IFMT != unix.S_IFREG || s.Nlink != 1) {
		err = errHomeForbidden
	}
	return s, err
}
func homeFile(root *os.File, name string) (*os.File, error) {
	if _, err := homeParts(name, false); err != nil {
		return nil, err
	}
	f, err := homeOpen(root, name, unix.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	if _, err = homeStat(f, true); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
func homeDigest(f *os.File) (string, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func homeIdentity(a, b unix.Stat_t) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino && a.Size == b.Size && a.Mtim == b.Mtim && a.Ctim == b.Ctim
}
func homeUnchanged(f *os.File, s unix.Stat_t) error {
	now, err := homeStat(f, true)
	if err != nil {
		return err
	}
	if !homeIdentity(s, now) {
		return errors.New("file changed")
	}
	return nil
}
func homeEntry(name string, s unix.Stat_t, digest string) map[string]any {
	kind := "file"
	if s.Mode&unix.S_IFMT == unix.S_IFDIR {
		kind = "directory"
	}
	preview := "download"
	if strings.Contains("|.txt|.md|.json|.yaml|.yml|.csv|.tsv|.py|.go|.js|.ts|.tsx|.jsx|.html|.css|.xml|.svg|.mmd|.mermaid|.sql|.sh|.toml|.ini|.log|", "|"+strings.ToLower(path.Ext(name))+"|") {
		preview = "text"
	}
	return map[string]any{"path": name, "kind": kind, "size_bytes": s.Size, "modified_at": time.Unix(s.Mtim.Sec, s.Mtim.Nsec).UTC().Format(time.RFC3339Nano), "freshness": fmt.Sprintf("%d:%d:%d", s.Mtim.Nano(), s.Ctim.Nano(), s.Size), "sha256": digest, "preview": preview}
}
func (p *compiledHomeFiles) list() error {
	parts, err := homeParts(p.q.Path, true)
	if err != nil {
		return err
	}
	d, err := homeDir(p.home, parts, false)
	if err != nil {
		return err
	}
	defer d.Close()
	n := p.q.Limit
	if n == 0 {
		n = 100
	}
	if n < 1 || n > 500 {
		return errors.New("limit")
	}
	type row struct {
		name  string
		entry map[string]any
	}
	rows := []row{}
	for {
		names, e := d.Readdirnames(128)
		if e != nil && e != io.EOF {
			return e
		}
		for _, name := range names {
			if name <= p.q.After {
				continue
			}
			if _, e := homeParts(name, false); e != nil {
				continue
			}
			f, e := homeOpen(d, name, unix.O_RDONLY, 0)
			if e != nil {
				continue
			}
			s, e := homeStat(f, false)
			f.Close()
			if e != nil || (s.Mode&unix.S_IFMT != unix.S_IFDIR && (s.Mode&unix.S_IFMT != unix.S_IFREG || s.Nlink != 1)) {
				continue
			}
			rows = append(rows, row{name, homeEntry(path.Join(p.q.Path, name), s, "")})
			sort.Slice(rows, func(i, j int) bool { return rows[i].name < rows[j].name })
			if len(rows) > n+1 {
				rows = rows[:n+1]
			}
		}
		if e == io.EOF {
			break
		}
	}
	entries := []map[string]any{}
	next := ""
	if len(rows) > n {
		next = rows[n-1].name
		rows = rows[:n]
	}
	for _, r := range rows {
		entries = append(entries, r.entry)
	}
	return p.emit(map[string]any{"entries": entries, "next": next})
}
func (p *compiledHomeFiles) read() error {
	root := p.home
	if p.q.Private {
		root = p.private
	}
	f, err := homeFile(root, p.q.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	s, err := homeStat(f, true)
	if err != nil {
		return err
	}
	digest, err := homeDigest(f)
	if err != nil {
		return err
	}
	if err = homeUnchanged(f, s); err != nil {
		return err
	}
	if p.q.SHA256 != nil && *p.q.SHA256 != digest || p.q.Size != nil && *p.q.Size != s.Size {
		return errors.New("content mismatch")
	}
	if p.q.Op == "stat" {
		return p.emit(homeEntry(p.q.Path, s, digest))
	}
	if p.q.Offset < 0 || p.q.Length < 0 || p.q.Offset > s.Size {
		return errors.New("range")
	}
	left := s.Size - p.q.Offset
	if p.q.Length > 0 && p.q.Length < left {
		left = p.q.Length
	}
	if _, err = f.Seek(p.q.Offset, io.SeekStart); err != nil {
		return err
	}
	p.streaming = true
	if err = p.emit(homeEntry(p.q.Path, s, digest)); err != nil {
		return err
	}
	if _, err = io.CopyN(p.out, f, left); err != nil {
		return err
	}
	return homeUnchanged(f, s)
}
func homeQuota(root *os.File, add int64) error {
	entries, files, bytes := 8192, 4096, int64(536870912)-add
	var walk func(*os.File) error
	walk = func(d *os.File) error {
		scan, err := homeOpen(d, ".", unix.O_RDONLY|unix.O_DIRECTORY, 0)
		if err != nil {
			return err
		}
		defer scan.Close()
		for {
			names, e := scan.Readdirnames(128)
			if e != nil && e != io.EOF {
				return e
			}
			for _, name := range names {
				entries--
				if entries < 0 {
					return errors.New("directory quota")
				}
				f, err := homeOpen(d, name, unix.O_RDONLY, 0)
				if err != nil {
					return err
				}
				s, err := homeStat(f, false)
				if err == nil {
					switch s.Mode & unix.S_IFMT {
					case unix.S_IFDIR:
						err = walk(f)
					case unix.S_IFREG:
						if s.Nlink != 1 {
							err = errHomeForbidden
						} else {
							files--
							bytes -= s.Size
						}
					default:
						err = errHomeForbidden
					}
				}
				f.Close()
				if err != nil {
					return err
				}
				if files < 0 || bytes < 0 {
					return errors.New("workspace quota")
				}
			}
			if e == io.EOF {
				break
			}
		}
		return nil
	}
	if err := walk(root); err != nil {
		return err
	}
	if entries < 2 || files < 1 || bytes < 0 {
		return errors.New("workspace quota")
	}
	return nil
}
func (p *compiledHomeFiles) write() error {
	immutable := p.q.Op != "write"
	root := p.home
	name := p.q.Path
	reserved := int64(0)
	if p.q.Size != nil {
		reserved = *p.q.Size
	}
	if reserved < 0 {
		return errors.New("size")
	}
	if p.q.Readback && (p.q.Op != "put" || reserved > 8388608) {
		return errors.New("readback bound")
	}
	var src *os.File
	var before unix.Stat_t
	if p.q.Op == "snapshot" {
		var err error
		src, err = homeFile(p.home, name)
		if err != nil {
			return err
		}
		defer src.Close()
		before, err = homeStat(src, true)
		if err != nil {
			return err
		}
		reserved = before.Size
	} else if p.q.Size == nil {
		return errors.New("size required")
	}
	if immutable {
		root = p.private
		if root == nil {
			return errHomeForbidden
		}
		kind := p.q.Kind
		if kind == "" {
			kind = "artifact"
		}
		switch kind {
		case "document", "memory", "task", "skill", "evidence", "checkpoint", "artifact":
		default:
			return errors.New("content identity")
		}
		rev := p.q.Revision
		if rev == 0 {
			rev = 1
		}
		if rev < 1 {
			return errors.New("content identity")
		}
		name = fmt.Sprintf("content-v1/%s-%d-%s", kind, rev, strings.ReplaceAll(uuid.NewString(), "-", ""))
		lock, err := homeOpen(root, ".workspace-lock", unix.O_RDWR|unix.O_CREAT, 0600)
		if err != nil {
			return err
		}
		defer lock.Close()
		if _, err = homeStat(lock, true); err != nil {
			return err
		}
		if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
			return err
		}
	}
	parts, err := homeParts(name, false)
	if err != nil {
		return err
	}
	d, err := homeDir(root, parts[:len(parts)-1], true)
	if err != nil {
		return err
	}
	defer d.Close()
	if err = unix.Flock(int(d.Fd()), unix.LOCK_EX); err != nil {
		return err
	}
	if err = homeReclaimFileStages(d); err != nil {
		return err
	}
	if immutable {
		if err = homeQuota(root, reserved); err != nil {
			return err
		}
	}
	tmp := string(homeFileStagePrefix) + strings.ReplaceAll(uuid.NewString(), "-", "")
	f, err := homeOpen(d, tmp, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	defer unix.Unlinkat(int(d.Fd()), tmp, 0)
	h := sha256.New()
	var reader io.Reader = p.in
	if src != nil {
		reader = src
	}
	size, err := io.CopyN(io.MultiWriter(f, h), reader, reserved)
	if err != nil {
		return err
	}
	if src != nil {
		if err = homeUnchanged(src, before); err != nil {
			return err
		}
	}
	digest := hex.EncodeToString(h.Sum(nil))
	if err = f.Sync(); err != nil {
		return err
	}
	base := parts[len(parts)-1]
	precondition := func() error {
		if immutable || p.q.CreateOnly {
			var st unix.Stat_t
			e := unix.Fstatat(int(d.Fd()), base, &st, unix.AT_SYMLINK_NOFOLLOW)
			if errors.Is(e, unix.ENOENT) {
				return nil
			}
			if e != nil {
				return e
			}
			return errors.New("conflict")
		}
		if len(p.q.Expected) != 64 {
			return errors.New("precondition required")
		}
		old, e := homeFile(root, name)
		if e != nil {
			return e
		}
		defer old.Close()
		s, e := homeStat(old, true)
		if e != nil {
			return e
		}
		got, e := homeDigest(old)
		if e != nil {
			return e
		}
		if got != p.q.Expected {
			return errors.New("conflict")
		}
		return homeUnchanged(old, s)
	}
	if err = precondition(); err != nil {
		return err
	}
	if err = p.emit(map[string]bool{"commit": true}); err != nil {
		return err
	}
	commit, err := p.in.ReadSlice('\n')
	if err != nil || string(commit) != "commit\n" {
		return errors.New("commit denied")
	}
	again, err := homeDir(root, parts[:len(parts)-1], false)
	if err != nil {
		return err
	}
	a, e := homeStat(again, false)
	again.Close()
	if e != nil {
		return e
	}
	b, e := homeStat(d, false)
	if e != nil {
		return e
	}
	if a.Dev != b.Dev || a.Ino != b.Ino {
		return errors.New("parent changed")
	}
	if err = precondition(); err != nil {
		return err
	}
	staged, err := homeOpen(d, tmp, unix.O_RDONLY, 0)
	if err != nil {
		return err
	}
	s, err := homeStat(staged, true)
	if err != nil {
		staged.Close()
		return err
	}
	got, err := homeDigest(staged)
	if err == nil {
		err = homeUnchanged(staged, s)
	}
	staged.Close()
	if err != nil {
		return err
	}
	if s.Size != size || got != digest {
		return errors.New("staged bytes changed")
	}
	flags := uint(0)
	if immutable || p.q.CreateOnly {
		flags = unix.RENAME_NOREPLACE
	}
	if err = unix.Renameat2(int(d.Fd()), tmp, int(d.Fd()), base, flags); err != nil {
		return err
	}
	if err = d.Sync(); err != nil {
		return err
	}
	stored, err := homeFile(root, name)
	if err != nil {
		return err
	}
	defer stored.Close()
	s, err = homeStat(stored, true)
	if err != nil {
		return err
	}
	got, err = homeDigest(stored)
	if err != nil {
		return err
	}
	if s.Size != size || got != digest {
		return errors.New("stored bytes changed")
	}
	if err = homeUnchanged(stored, s); err != nil {
		return err
	}
	if p.q.Readback {
		if _, err = stored.Seek(0, io.SeekStart); err != nil {
			return err
		}
		p.streaming = true
	}
	if err = p.emit(homeEntry(name, s, digest)); err != nil {
		return err
	}
	if p.q.Readback {
		if _, err = io.CopyN(p.out, stored, size); err != nil {
			return err
		}
		return homeUnchanged(stored, s)
	}
	return nil
}
