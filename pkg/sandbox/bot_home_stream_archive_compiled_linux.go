//go:build linux

package sandbox

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

// RunBotHomeArchive executes the retained archive v1 protocol on caller-owned
// roots. The transport must join the terminal process error, even after all body
// bytes or an acknowledgement have arrived. admissionHeld is selected only by
// trusted transport holding the existing workspace admission lock through exit;
// otherwise this operation acquires that same lock itself. It is not a request
// field and this operation is never registered as a model tool.
func RunBotHomeArchive(home, private *os.File, input io.Reader, output io.Writer, admissionHeld bool) error {
	p := &compiledHomeArchive{home: home, private: private, in: bufio.NewReaderSize(input, 65536), out: output}
	// Permit a manifest plus its bounded request envelope, without buffering bodies.
	var line []byte
	for {
		fragment, more, err := p.in.ReadLine()
		if err != nil {
			return err
		}
		if len(line)+len(fragment) > MaxBotHomeArchiveManifestBytes+1024 {
			return errors.New("archive request bound")
		}
		line = append(line, fragment...)
		if !more {
			break
		}
	}
	if err := json.Unmarshal(line, &p.q); err != nil {
		return err
	}
	if !archiveDigest(p.q.Binding) {
		return errors.New("archive binding")
	}
	if p.q.Op != archiveExport && p.q.Op != archiveRestore {
		return errors.New("archive operation")
	}
	if p.q.Op == archiveRestore {
		if err := p.q.Manifest.Validate(); err != nil {
			return err
		}
	}
	for _, root := range []*os.File{home, private} {
		if root == nil {
			return errHomeForbidden
		}
		s, err := homeStat(root, false)
		if err != nil {
			return err
		}
		if s.Mode&unix.S_IFMT != unix.S_IFDIR {
			return errHomeForbidden
		}
	}
	lockFlags := unix.O_RDWR
	if !admissionHeld {
		lockFlags |= unix.O_CREAT
	}
	lockMode := uint32(0)
	if !admissionHeld {
		lockMode = 0600
	}
	lock, err := homeOpen(private, ".workspace-lock", lockFlags, lockMode)
	if err != nil {
		return err
	}
	defer lock.Close()
	if _, err = homeStat(lock, true); err != nil {
		return err
	}
	if !admissionHeld {
		if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
			return err
		}
		defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	}
	if p.q.Op == archiveExport {
		return p.export()
	}
	return p.restore()
}

type archiveOperation string

const (
	archiveExport  archiveOperation = "export"
	archiveRestore archiveOperation = "restore"
)

type compiledHomeArchive struct {
	home, private *os.File
	in            *bufio.Reader
	out           io.Writer
	q             struct {
		Op       archiveOperation        `json:"op"`
		Binding  string                  `json:"binding"`
		Manifest *BotHomeArchiveManifest `json:"manifest"`
	}
}

func (p *compiledHomeArchive) root(root BotHomeArchiveRoot) *os.File {
	if root == BotHomeArchiveHome {
		return p.home
	}
	return p.private
}
func (p *compiledHomeArchive) emit(v any) error { return json.NewEncoder(p.out).Encode(v) }
func (p *compiledHomeArchive) approved() error {
	var b [7]byte
	if _, err := io.ReadFull(p.in, b[:]); err != nil {
		return err
	}
	if string(b[:]) != "commit\n" {
		return errors.New("archive commit denied")
	}
	return nil
}
func archiveSafe(root BotHomeArchiveRoot, name string) ([]string, error) {
	if err := BotHomeArchivePath(root, name); err != nil {
		return nil, err
	}
	return homeParts(name, false)
}

// Hash exactly the statted size, not an unbounded file that may be growing.
func archiveFileDigest(f *os.File, s unix.Stat_t) (string, error) {
	h := sha256.New()
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	if _, err := io.CopyN(h, f, s.Size); err != nil {
		return "", err
	}
	if err := homeUnchanged(f, s); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func (p *compiledHomeArchive) inventory() (*BotHomeArchiveManifest, error) {
	m := &BotHomeArchiveManifest{Version: 1, Binding: p.q.Binding, Entries: []BotHomeArchiveEntry{}}
	var total, privateBytes int64
	privateEntries, privateFiles := 0, 0
	var walk func(BotHomeArchiveRoot, string) error
	walk = func(root BotHomeArchiveRoot, prefix string) error {
		name := strings.TrimSuffix(prefix, "/")
		if name == "" {
			name = "."
		}
		d, err := homeOpen(p.root(root), name, unix.O_RDONLY|unix.O_DIRECTORY, 0)
		if err != nil {
			return err
		}
		defer d.Close()
		for {
			names, e := d.Readdirnames(128)
			if e != nil && e != io.EOF {
				return e
			}
			for _, name := range names {
				if root == BotHomeArchivePrivate && prefix == "" && name == ".workspace-lock" {
					continue
				}
				if root == BotHomeArchivePrivate && prefix == "" && name == ".archive-state" {
					f, err := homeFile(p.private, name)
					if err != nil {
						return err
					}
					s, err := homeStat(f, true)
					if err == nil && s.Size != int64(len("restoring\nverified\n")) {
						err = errors.New("incomplete archive target")
					}
					if err == nil {
						var b []byte
						b, err = io.ReadAll(io.LimitReader(f, 64))
						if err == nil && string(b) != "restoring\nverified\n" {
							err = errors.New("incomplete archive target")
						}
						if err == nil {
							err = homeUnchanged(f, s)
						}
					}
					err = errors.Join(err, f.Close())
					if err != nil {
						return err
					}
					continue
				}
				path := prefix + name
				if _, err := archiveSafe(root, path); err != nil {
					return err
				}
				f, err := homeOpen(p.root(root), path, unix.O_RDONLY, 0)
				if err != nil {
					return err
				}
				s, err := homeStat(f, false)
				directory := s.Mode&unix.S_IFMT == unix.S_IFDIR
				if err == nil && !directory {
					_, err = homeStat(f, true)
				}
				row := BotHomeArchiveEntry{Root: root, Path: path, Mode: s.Mode & 0777, Directory: directory}
				if !directory {
					row.Size = s.Size
				}
				total += row.Size
				if root == BotHomeArchivePrivate {
					privateEntries++
					privateBytes += row.Size
					if !directory {
						privateFiles++
					}
				}
				if len(m.Entries) >= MaxBotHomeArchiveEntries || row.Size < 0 || total > MaxBotHomeArchiveBytes || privateEntries > 8192 || privateFiles > 4096 || privateBytes > 536870912 {
					err = errors.New("archive quota")
				}
				if err == nil && !directory {
					row.SHA256, err = archiveFileDigest(f, s)
				}
				err = errors.Join(err, f.Close())
				if err != nil {
					return err
				}
				m.Entries = append(m.Entries, row)
				if directory {
					if err := walk(root, path+"/"); err != nil {
						return err
					}
				}
			}
			if e == io.EOF {
				break
			}
		}
		return nil
	}
	if err := walk(BotHomeArchiveHome, ""); err != nil {
		return nil, err
	}
	if err := walk(BotHomeArchivePrivate, ""); err != nil {
		return nil, err
	}
	sort.Slice(m.Entries, func(i, j int) bool {
		a, b := m.Entries[i], m.Entries[j]
		return string(a.Root)+"/"+a.Path < string(b.Root)+"/"+b.Path
	})
	return m, m.Validate()
}
func (p *compiledHomeArchive) export() error {
	m, err := p.inventory()
	if err != nil {
		return err
	}
	if err = p.emit(m); err != nil {
		return err
	}
	if err = p.approved(); err != nil {
		return err
	}
	for _, e := range m.Entries {
		if e.Directory {
			continue
		}
		if err = p.exportFile(e); err != nil {
			return err
		}
	}
	after, err := p.inventory()
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(m, after) {
		return errors.New("archive inventory changed")
	}
	return nil
}
func (p *compiledHomeArchive) exportFile(e BotHomeArchiveEntry) (err error) {
	f, err := homeFile(p.root(e.Root), e.Path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	s, err := homeStat(f, true)
	if err != nil {
		return err
	}
	if s.Size != e.Size || s.Mode&0777 != e.Mode {
		return errors.New("changed archive read")
	}
	h := sha256.New()
	if _, err = io.CopyN(io.MultiWriter(p.out, h), f, e.Size); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != e.SHA256 {
		return errors.New("changed archive read")
	}
	return homeUnchanged(f, s)
}
func archiveEmpty(root *os.File, allowLock bool) error {
	d, err := homeOpen(root, ".", unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return err
	}
	defer d.Close()
	names, err := d.Readdirnames(2)
	if err != nil && err != io.EOF {
		return err
	}
	for _, n := range names {
		if !allowLock || n != ".workspace-lock" {
			return errors.New("restore target not fresh")
		}
	}
	return nil
}
func (p *compiledHomeArchive) restore() (err error) {
	if err = archiveEmpty(p.home, false); err != nil {
		return err
	}
	if err = archiveEmpty(p.private, true); err != nil {
		return err
	}
	state, err := homeOpen(p.private, ".archive-state", unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, state.Close()) }()
	if _, err = io.WriteString(state, "restoring\n"); err != nil {
		return err
	}
	if err = state.Sync(); err != nil {
		return err
	}
	if err = p.private.Sync(); err != nil {
		return err
	}
	if err = p.emit(map[string]bool{"ready": true}); err != nil {
		return err
	}
	for _, e := range p.q.Manifest.Entries {
		if err = p.restoreEntry(e); err != nil {
			return err
		}
	}
	if err = p.emit(map[string]bool{"commit": true}); err != nil {
		return err
	}
	if err = p.approved(); err != nil {
		return err
	}
	for i := len(p.q.Manifest.Entries) - 1; i >= 0; i-- {
		e := p.q.Manifest.Entries[i]
		if !e.Directory {
			continue
		}
		d, e2 := homeOpen(p.root(e.Root), e.Path, unix.O_RDONLY|unix.O_DIRECTORY, 0)
		if e2 != nil {
			return e2
		}
		err = unix.Fchmod(int(d.Fd()), e.Mode)
		if err == nil {
			err = d.Sync()
		}
		err = errors.Join(err, d.Close())
		if err != nil {
			return err
		}
	}
	if _, err = io.WriteString(state, "verified\n"); err != nil {
		return err
	}
	if err = state.Sync(); err != nil {
		return err
	}
	return p.emit(map[string]bool{"restored": true})
}
func (p *compiledHomeArchive) restoreEntry(e BotHomeArchiveEntry) (err error) {
	parts, err := archiveSafe(e.Root, e.Path)
	if err != nil {
		return err
	}
	d, err := homeDir(p.root(e.Root), parts[:len(parts)-1], false)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, d.Close()) }()
	name := parts[len(parts)-1]
	if e.Directory {
		if err = unix.Mkdirat(int(d.Fd()), name, 0700); err != nil {
			return err
		}
		return d.Sync()
	}
	f, err := homeOpen(d, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL, 0600)
	if err != nil {
		return err
	}
	err = func() error {
		if _, err := homeStat(f, true); err != nil {
			return err
		}
		h := sha256.New()
		if _, err := io.CopyN(io.MultiWriter(f, h), p.in, e.Size); err != nil {
			return err
		}
		if hex.EncodeToString(h.Sum(nil)) != e.SHA256 {
			return errors.New("restore digest mismatch")
		}
		if err := unix.Fchmod(int(f.Fd()), e.Mode); err != nil {
			return err
		}
		return f.Sync()
	}()
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	verify, err := homeFile(p.root(e.Root), e.Path)
	if err != nil {
		return err
	}
	s, err := homeStat(verify, true)
	if err == nil && s.Size != e.Size {
		err = errors.New("staged restore size mismatch")
	}
	if err == nil {
		var digest string
		digest, err = archiveFileDigest(verify, s)
		if err == nil && digest != e.SHA256 {
			err = errors.New("staged restore digest mismatch")
		}
	}
	err = errors.Join(err, verify.Close())
	if err != nil {
		return err
	}
	return d.Sync()
}
