//go:build linux

package sandbox

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

const workspaceRestoreMarker = ".workspace-restore-incomplete"

// RunBotHomeWorkspace executes one bounded private-workspace request. The caller
// supplies an already validated root descriptor and owns its lifetime. It shares
// the on-disk lock and receipt format with legacy archive/scratch operations.
func RunBotHomeWorkspace(root *os.File, input io.Reader, output io.Writer) error {
	p := &compiledWorkspace{root: root, in: bufio.NewReader(input), out: output}
	line, err := workspaceFrame(p.in, 24<<20)
	return p.run(line, err)
}

type workspaceRequest struct {
	Publication string                         `json:"publication"`
	Script      string                         `json:"script"`
	Digest      string                         `json:"digest"`
	Op          string                         `json:"op"`
	Binding     string                         `json:"binding"`
	Path        string                         `json:"path"`
	Limit       int64                          `json:"limit"`
	After       string                         `json:"after"`
	Data        []byte                         `json:"data"`
	Record      json.RawMessage                `json:"record"`
	Snapshot    *BotHomeWorkspaceSnapshot      `json:"snapshot"`
	Request     BotHomeWorkspacePublishRequest `json:"request"`
}
type compiledWorkspace struct {
	root     *os.File
	in       *bufio.Reader
	out      io.Writer
	q        workspaceRequest
	lock     *os.File
	lockMode int
}

func (p *compiledWorkspace) run(line []byte, err error) error {
	if err != nil || len(line) > 24<<20 {
		return errors.New("invalid workspace request frame")
	}
	if err = json.Unmarshal(line, &p.q); err != nil {
		return err
	}
	if p.q.Binding == "" {
		return errors.New("workspace binding required")
	}
	// An interrupted restore is never a selectable workspace. Only external
	// recovery may purge this disposable target; no request retries/adopts it.
	marker, markerErr := homeOpen(p.root, workspaceRestoreMarker, unix.O_RDONLY, 0)
	if markerErr == nil {
		marker.Close()
		return errors.New("workspace restore incomplete; discard target")
	}
	if !errors.Is(markerErr, os.ErrNotExist) {
		return markerErr
	}
	// The directory flock is lifetime-only: shared writers may reenter during
	// authorization. Recovery needs a nonblocking exclusive hold, never waits
	// behind a host callback, and still takes the separate data-authority lock.
	var lifetime *os.File
	recoverStages := false
	if p.q.Op == "publish" || p.q.Op == "write" || p.q.Op == "restore" || p.q.Op == workspaceKnowledgePublish || p.q.Op == workspaceScript {
		lifetime, err = homeOpen(p.root, ".", unix.O_RDONLY|unix.O_DIRECTORY, 0)
		if err != nil {
			return err
		}
		defer lifetime.Close()
		err = unix.Flock(int(lifetime.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			recoverStages = true
		} else if errors.Is(err, unix.EWOULDBLOCK) {
			err = unix.Flock(int(lifetime.Fd()), unix.LOCK_SH)
		}
		if err != nil {
			return err
		}
		defer unix.Flock(int(lifetime.Fd()), unix.LOCK_UN)
	}
	lockFlags := unix.O_RDWR
	if p.q.Op == "publish" || p.q.Op == "write" || p.q.Op == "restore" || p.q.Op == workspaceKnowledgePublish {
		lockFlags |= unix.O_CREAT
	}
	lock, err := homeOpen(p.root, ".workspace-lock", lockFlags, func() uint32 {
		if lockFlags&unix.O_CREAT != 0 {
			return 0600
		}
		return 0
	}())
	if err != nil {
		// An uninitialized knowledge store is empty; reads must not create its lock.
		if p.q.Op == workspaceKnowledgeSnapshot && errors.Is(err, os.ErrNotExist) {
			d, e := homeOpen(p.root, ".knowledge", unix.O_RDONLY|unix.O_DIRECTORY, 0)
			if errors.Is(e, os.ErrNotExist) {
				return json.NewEncoder(p.out).Encode([]any{})
			}
			if d != nil {
				d.Close()
			}
		}
		return err
	}
	defer lock.Close()
	if _, err = homeStat(lock, true); err != nil {
		return err
	}
	p.lock, p.lockMode = lock, unix.LOCK_EX
	switch p.q.Op {
	case "resolve", "read", "snapshot", "list", workspaceKnowledgeSnapshot, workspaceKnowledgeVerify:
		p.lockMode = unix.LOCK_SH
	}
	if err = unix.Flock(int(lock.Fd()), p.lockMode); err != nil {
		return err
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	marker, markerErr = homeOpen(p.root, workspaceRestoreMarker, unix.O_RDONLY, 0)
	if markerErr == nil {
		marker.Close()
		return errors.New("workspace restore incomplete; discard target")
	}
	if !errors.Is(markerErr, os.ErrNotExist) {
		return markerErr
	}
	if recoverStages {
		if err = workspaceReclaimStages(p.root); err != nil {
			return err
		}
	}
	if lifetime != nil {
		// Downgrade before any host exchange; nested compatible writers take
		// shared lifetime holds, and therefore skip recovery instead of deadlocking.
		if err = unix.Flock(int(lifetime.Fd()), unix.LOCK_SH); err != nil {
			return err
		}
	}
	var result any
	switch p.q.Op {
	case workspaceScript:
		result, err = p.scriptOperation()
	case workspaceKnowledgeSnapshot, workspaceKnowledgePublish, workspaceKnowledgeVerify:
		result, err = p.knowledgeOperation()
	case "restore":
		result, err = p.restore()
	case "publish":
		result, err = p.publish()
	case "resolve":
		if !workspacePublicReference(p.q.Path) {
			return errHomeForbidden
		}
		var h *BotHomeWorkspaceReceipt
		h, err = p.receipt(".receipts/" + p.q.Path)
		if err == nil && h.ReceiptRef != p.q.Path {
			err = errHomeForbidden
		}
		if err == nil {
			_, err = p.snapshot(h)
		}
		result = h
	case "read":
		var name string
		name, err = p.physical(p.q.Path)
		if err == nil {
			result, err = p.read(name, p.q.Limit)
		}
	case "write":
		err = p.writeState()
	case "snapshot":
		result, err = p.snapshotRequest()
	case "list":
		if p.q.Limit < 1 || p.q.Limit > 100 {
			return errors.New("invalid list limit")
		}
		var h *BotHomeWorkspaceReceipt
		h, err = p.head(p.q.Path)
		if err == nil {
			_, err = p.snapshot(h)
		}
		if err == nil {
			rows := make([]BotHomeWorkspaceEntry, 0)
			next := ""
			for _, e := range h.Entries {
				if e.Path > p.q.After {
					rows = append(rows, e)
				}
			}
			if len(rows) > int(p.q.Limit) {
				rows = rows[:p.q.Limit]
				next = rows[len(rows)-1].Path
			}
			result = map[string]any{"entries": rows, "next": next}
		}
	default:
		return errors.New("unsupported compiled workspace operation")
	}
	if err != nil {
		return err
	}
	if err = json.NewEncoder(p.out).Encode(result); err != nil {
		return err
	}
	if p.q.Op == "restore" {
		if err = unix.Unlinkat(int(p.root.Fd()), workspaceRestoreMarker, 0); err != nil {
			return err
		}
		if err = p.root.Sync(); err != nil {
			// Best-effort reinstate on an ambiguous final durability failure.
			_ = p.write(workspaceRestoreMarker, []byte("restore durability uncertain\n"), false)
			return err
		}
	}
	return nil
}

// workspaceFrame bounds each newline-delimited frame independently: script
// results may be 192 MiB while requests remain 24 MiB and approval is tiny.
func workspaceFrame(in *bufio.Reader, limit int) ([]byte, error) {
	var frame []byte
	for {
		part, err := in.ReadSlice('\n')
		if len(part) > limit-len(frame) {
			return nil, errors.New("workspace frame quota")
		}
		frame = append(frame, part...)
		if err == bufio.ErrBufferFull {
			continue
		}
		return frame, err
	}
}

func (p *compiledWorkspace) scriptState(name string) ([]BotHomeWorkspaceFile, error) {
	rows, _, err := p.inventory(name)
	if err != nil {
		return nil, err
	}
	files := make([]BotHomeWorkspaceFile, 0, len(rows))
	var total int64
	for _, row := range rows {
		rel := strings.TrimPrefix(row.path, name+"/")
		total += row.size
		if !workspacePublicPath(rel) || row.size > 1<<20 || total > 128<<20 {
			return nil, errors.New("script state quota or path")
		}
		data, e := p.read(row.path, 1<<20)
		if e != nil {
			return nil, e
		}
		files = append(files, BotHomeWorkspaceFile{Path: rel, Data: data})
	}
	return files, nil
}

// scriptOperation is trusted storage only. WORKSPACE_SCRIPT delegates to the
// host's isolated scratch runner; no workload runs here or receives private fds.
func (p *compiledWorkspace) scriptOperation() (result any, err error) {
	q := p.q
	if !workspaceTask(q.Path) || !workspacePublicPath(q.Script) || !strings.HasPrefix(q.Script, "scripts/") || !strings.HasSuffix(q.Script, ".py") {
		return nil, errHomeForbidden
	}
	if _, err = uuid.Parse(q.Publication); err != nil {
		return nil, err
	}
	digest, e := hex.DecodeString(q.Digest)
	if e != nil || len(digest) != 32 {
		return nil, errors.New("script digest")
	}
	receiptName := ".receipts/" + q.Publication
	receiptBytes, err := p.read(receiptName, 1<<20)
	if err != nil {
		return nil, err
	}
	h, err := p.receipt(receiptName)
	if err != nil {
		return nil, err
	}
	if h.TaskPath != q.Path || h.PublicationID != q.Publication || h.ReceiptRef != q.Publication {
		return nil, errors.New("script receipt identity")
	}
	revision, err := p.snapshot(h)
	if err != nil {
		return nil, err
	}
	var script *BotHomeWorkspaceFile
	for i := range revision {
		if revision[i].Path == q.Script {
			script = &revision[i]
		}
	}
	if script == nil || !script.Executable || script.Digest() != q.Digest {
		return nil, errors.New("script digest or executable")
	}
	task, err := homeOpen(p.root, q.Path, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, err
	}
	defer task.Close()
	stateName := q.Path + "/state"
	previous, err := homeOpen(p.root, stateName, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	old := make([]BotHomeWorkspaceFile, 0)
	if previous != nil {
		defer previous.Close()
		old, err = p.scriptState(stateName)
		if err != nil {
			return nil, err
		}
	}
	if err = p.quota(0, 0, nil); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(map[string]any{"script": script, "files": old})
	if err != nil {
		return nil, err
	}
	if len(payload)+len("WORKSPACE_SCRIPT \n") > 192<<20 {
		return nil, errors.New("script request quota")
	}
	var frame []byte
	err = p.hostExchange(func() error {
		if _, e := io.WriteString(p.out, "WORKSPACE_SCRIPT "+string(payload)+"\n"); e != nil {
			return e
		}
		var e error
		frame, e = workspaceFrame(p.in, 192<<20)
		return e
	})
	if err != nil {
		return nil, err
	}
	var update struct {
		ExitCode *int                    `json:"exit_code"`
		Stdout   string                  `json:"stdout"`
		Stderr   string                  `json:"stderr"`
		Files    *[]BotHomeWorkspaceFile `json:"files"`
	}
	if err = json.Unmarshal(frame, &update); err != nil {
		return nil, err
	}
	if update.ExitCode == nil || *update.ExitCode != 0 {
		return nil, errors.New("script failed; state unchanged")
	}
	if update.Files == nil || len(*update.Files) > 4096 || len(update.Stdout) > 65536 || len(update.Stderr) > 4096 {
		return nil, errors.New("script output quota")
	}
	fresh := *update.Files
	seen := map[string]bool{}
	var total int64
	stage := "state-stage-" + uuid.NewString()
	stagePath := q.Path + "/" + stage
	paths := []string{stagePath + "/placeholder"}
	for i := range fresh {
		f := &fresh[i]
		if !workspacePublicPath(f.Path) || seen[f.Path] || len(f.Data) > 1<<20 {
			return nil, errors.New("script state path or asset quota")
		}
		seen[f.Path] = true
		f.Executable = false
		total += int64(len(f.Data))
		paths = append(paths, stagePath+"/"+f.Path)
	}
	if total > 128<<20 {
		return nil, errors.New("script state quota")
	}
	sort.Slice(fresh, func(i, j int) bool { return fresh[i].Path < fresh[j].Path })
	if err = p.quota(total, len(fresh), paths); err != nil {
		return nil, err
	}
	if err = unix.Mkdirat(int(task.Fd()), stage, 0700); err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, workspaceRemoveTree(task, stage)) }()
	staged, err := homeOpen(task, stage, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, err
	}
	defer staged.Close()
	for _, f := range fresh {
		if err = p.write(stagePath+"/"+f.Path, f.Data, false); err != nil {
			return nil, err
		}
	}
	if err = staged.Sync(); err != nil {
		return nil, err
	}
	checkDirectory := func(name string, want *os.File) error {
		now, e := homeOpen(p.root, name, unix.O_RDONLY|unix.O_DIRECTORY, 0)
		if e != nil {
			return e
		}
		defer now.Close()
		return workspaceSameDirectory(want, now)
	}
	checkStage := func() error {
		if e := checkDirectory(q.Path, task); e != nil {
			return e
		}
		if e := checkDirectory(stagePath, staged); e != nil {
			return e
		}
		actual, e := p.scriptState(stagePath)
		if e != nil {
			return e
		}
		if !reflect.DeepEqual(fresh, actual) {
			return errors.New("script staged bytes changed")
		}
		return nil
	}
	if err = checkStage(); err != nil {
		return nil, err
	}
	if err = p.ready(); err != nil {
		return nil, err
	}
	if err = checkStage(); err != nil {
		return nil, err
	}
	// Revalidate exact input receipt, revision, state, quotas and restore fence
	// after the host permission barrier, before selecting the new generation.
	marker, markerErr := homeOpen(p.root, workspaceRestoreMarker, unix.O_RDONLY, 0)
	if marker != nil {
		marker.Close()
		return nil, errors.New("workspace restore incomplete")
	}
	if !errors.Is(markerErr, os.ErrNotExist) {
		return nil, markerErr
	}
	currentReceipt, e := p.read(receiptName, 1<<20)
	if e != nil {
		return nil, e
	}
	if !bytes.Equal(currentReceipt, receiptBytes) {
		return nil, errors.New("script receipt changed")
	}
	if _, err = p.snapshot(h); err != nil {
		return nil, err
	}
	flags := uint(unix.RENAME_NOREPLACE)
	if previous != nil {
		if err = checkDirectory(stateName, previous); err != nil {
			return nil, err
		}
		current, e := p.scriptState(stateName)
		if e != nil {
			return nil, e
		}
		if !reflect.DeepEqual(old, current) {
			return nil, errors.New("script state changed")
		}
		flags = unix.RENAME_EXCHANGE
	} else {
		current, e := homeOpen(p.root, stateName, unix.O_RDONLY|unix.O_DIRECTORY, 0)
		if current != nil {
			current.Close()
			return nil, errors.New("script state appeared")
		}
		if !errors.Is(e, os.ErrNotExist) {
			return nil, e
		}
	}
	if err = p.quota(0, 0, nil); err != nil {
		return nil, err
	}
	if err = unix.Renameat2(int(task.Fd()), stage, int(task.Fd()), "state", flags); err != nil {
		return nil, err
	}
	if err = task.Sync(); err != nil {
		return nil, err
	}
	actual, err := p.scriptState(stateName)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(fresh, actual) {
		return nil, errors.New("script committed bytes changed")
	}
	return map[string]any{"exit_code": 0, "stdout": update.Stdout, "stderr": update.Stderr}, nil
}

func (p *compiledWorkspace) restore() (any, error) {
	s := p.q.Snapshot
	if err := ValidateLegacyWorkspaceSnapshot(s); err != nil {
		return nil, err
	}
	if err := archiveEmpty(p.root, true); err != nil {
		return nil, err
	}
	content := make(map[string][]byte, len(s.Files))
	receipts := make(map[string]*BotHomeWorkspaceReceipt)
	paths := make([]string, 0, len(s.Files)+1)
	var total int64
	for _, f := range s.Files {
		a, err := homeParts(f.Path, false)
		if err != nil || workspaceRevisionStage(f.Path) || strings.Contains(f.Path, "/.revision-stage-") || a[0] == workspaceRestoreMarker || a[0] == ".workspace-lock" {
			return nil, errors.New("invalid restore path")
		}
		data := f.Data
		if a[0] == ".receipts" || a[len(a)-1] == ".head" {
			var h BotHomeWorkspaceReceipt
			if err := json.Unmarshal(data, &h); err != nil {
				return nil, err
			}
			if h.BindingKey != s.Receipt.BindingKey {
				return nil, errors.New("foreign archive receipt")
			}
			h.BindingKey = p.q.Binding
			if err := p.validate(&h); err != nil {
				return nil, err
			}
			if f.Path != ".receipts/"+h.ReceiptRef && f.Path != h.TaskPath+"/.head" {
				return nil, errors.New("restore receipt path mismatch")
			}
			// Change only the binding value. Preserve field order, whitespace,
			// executable defaults and unknown fields in legacy receipt bytes.
			data, err = workspaceRemapReceipt(data, p.q.Binding)
			if err != nil {
				return nil, err
			}
			receipts[f.Path] = &h
		}
		content[f.Path] = data
		paths = append(paths, f.Path)
		total += int64(len(data))
	}
	for name, h := range receipts {
		prefix := h.TaskPath + "/.revisions/" + h.PublicationID + "/"
		count := 0
		for path := range content {
			if strings.HasPrefix(path, prefix) {
				count++
			}
		}
		if count != len(h.Entries) {
			return nil, errors.New("restore revision inventory")
		}
		for _, e := range h.Entries {
			b, ok := content[prefix+e.Path]
			if !ok || int64(len(b)) != e.SizeBytes || workspaceHash(b) != e.Digest {
				return nil, errors.New("restore revision digest")
			}
		}
		if strings.HasSuffix(name, "/.head") && !reflect.DeepEqual(h, receipts[".receipts/"+h.ReceiptRef]) {
			return nil, errors.New("restore head receipt mismatch")
		}
	}
	paths = append(paths, workspaceRestoreMarker)
	if err := p.quota(total+64, len(content)+1, paths); err != nil {
		return nil, err
	}
	if err := p.ready(); err != nil {
		return nil, err
	}
	if err := archiveEmpty(p.root, true); err != nil {
		return nil, err
	}
	marker, err := homeOpen(p.root, workspaceRestoreMarker, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL, 0600)
	if err != nil {
		return nil, err
	}
	_, err = io.WriteString(marker, "restore incomplete; discard target\n")
	if err == nil {
		err = marker.Sync()
	}
	err = errors.Join(err, marker.Close())
	if err == nil {
		err = p.root.Sync()
	}
	if err != nil {
		return nil, err
	}
	// Marker remains on every failure, including the final host fence. Neither
	// receipts nor heads can be consumed by this protocol while it is present.
	for _, f := range s.Files {
		if err = p.write(f.Path, content[f.Path], false); err != nil {
			return nil, err
		}
	}
	if err = p.ready(); err != nil {
		return nil, err
	}
	rows, _, err := p.inventory(".")
	if err != nil {
		return nil, err
	}
	if len(rows) != len(content)+2 {
		return nil, errors.New("restore inventory changed")
	}
	for name, expected := range content {
		actual, err := p.read(name, MaxBotHomeWorkspaceStateBytes)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(actual, expected) {
			return nil, errors.New("restored bytes changed")
		}
	}
	return map[string]int{"restored": len(content)}, nil
}

// workspaceRemapReceipt retains all original JSON bytes except the single
// binding value. Duplicate binding keys are rejected rather than interpreted.
func workspaceRemapReceipt(raw []byte, binding string) ([]byte, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	if token, err := d.Token(); err != nil || token != json.Delim('{') {
		return nil, errors.New("restore receipt object required")
	}
	start, end := -1, -1
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return nil, err
		}
		before := int(d.InputOffset())
		var value json.RawMessage
		if err = d.Decode(&value); err != nil {
			return nil, err
		}
		if key == "binding_key" {
			if start != -1 {
				return nil, errors.New("duplicate archive binding")
			}
			end = int(d.InputOffset())
			start = end - len(value)
			if start < before {
				return nil, errors.New("invalid archive binding offset")
			}
		}
	}
	if start == -1 {
		return nil, errors.New("archive binding missing")
	}
	value, err := json.Marshal(binding)
	if err != nil {
		return nil, err
	}
	out := append([]byte(nil), raw[:start]...)
	out = append(out, value...)
	return append(out, raw[end:]...), nil
}

func workspacePublicReference(s string) bool {
	a, e := homeParts(s, false)
	return e == nil && len(a) == 1 && !strings.HasPrefix(s, ".")
}
func workspaceTask(s string) bool {
	a, e := homeParts(s, false)
	return e == nil && len(a) == 2 && a[0] == "tasks" && !strings.HasPrefix(a[1], ".")
}
func workspacePublicPath(s string) bool {
	a, e := homeParts(s, false)
	if e != nil {
		return false
	}
	for _, n := range a {
		if strings.HasPrefix(n, ".") {
			return false
		}
	}
	return true
}
func workspaceHash(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// Maps retain Python sort_keys field order and explicit false executable values.
func workspaceEntriesDigest(entries []BotHomeWorkspaceEntry) string {
	return workspaceCanonicalDigest(entries, false)
}
func workspaceCanonicalDigest(entries []BotHomeWorkspaceEntry, ascii bool) string {
	rows := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, map[string]any{"path": e.Path, "size_bytes": e.SizeBytes, "digest": e.Digest, "executable": e.Executable})
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(rows)
	// encoding/json escapes U+2028/U+2029 even with EscapeHTML disabled, unlike
	// Python ensure_ascii=False. Canonical manifests must preserve those bytes.
	raw := bytes.TrimSuffix(b.Bytes(), []byte("\n"))
	raw = bytes.ReplaceAll(raw, []byte(`\u2028`), []byte("\u2028"))
	raw = bytes.ReplaceAll(raw, []byte(`\u2029`), []byte("\u2029"))
	if ascii {
		var escaped strings.Builder
		for _, r := range string(raw) {
			if r < 128 {
				escaped.WriteRune(r)
			} else if r <= 0xffff {
				fmt.Fprintf(&escaped, `\u%04x`, r)
			} else {
				r -= 0x10000
				fmt.Fprintf(&escaped, `\u%04x\u%04x`, 0xd800+(r>>10), 0xdc00+(r&0x3ff))
			}
		}
		raw = []byte(escaped.String())
	}
	return workspaceHash(raw)
}
func (p *compiledWorkspace) validate(h *BotHomeWorkspaceReceipt) error {
	if !workspaceTask(h.TaskPath) || !workspacePublicReference(h.PublicationID) || h.ReceiptRef != h.PublicationID || h.BindingKey != p.q.Binding || len(h.Entries) < 1 || len(h.Entries) > 64 {
		return errors.New("workspace receipt identity")
	}
	var total int64
	last := ""
	for _, e := range h.Entries {
		digest, err := hex.DecodeString(e.Digest)
		if !workspacePublicPath(e.Path) || e.Path <= last || e.SizeBytes < 0 || e.SizeBytes > 1<<20 || err != nil || len(digest) != 32 || hex.EncodeToString(digest) != e.Digest {
			return errors.New("workspace manifest entry")
		}
		last = e.Path
		total += e.SizeBytes
	}
	if total > 16<<20 || workspaceEntriesDigest(h.Entries) != h.Digest {
		return errors.New("workspace manifest digest")
	}
	return nil
}
func (p *compiledWorkspace) read(name string, limit int64) ([]byte, error) {
	if limit < 1 || limit > 128<<20 {
		return nil, errors.New("workspace read limit")
	}
	f, err := homeFile(p.root, name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	s, err := homeStat(f, true)
	if err != nil {
		return nil, err
	}
	if s.Size > limit {
		return nil, errors.New("workspace file too large")
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) != s.Size {
		return nil, errors.New("incomplete workspace read")
	}
	if err = homeUnchanged(f, s); err != nil {
		return nil, err
	}
	return b, nil
}
func (p *compiledWorkspace) receipt(name string) (*BotHomeWorkspaceReceipt, error) {
	b, err := p.read(name, 1<<20)
	if err != nil {
		return nil, err
	}
	var h BotHomeWorkspaceReceipt
	if err = json.Unmarshal(b, &h); err != nil {
		return nil, err
	}
	if err = p.validate(&h); err != nil {
		return nil, err
	}
	return &h, nil
}
func (p *compiledWorkspace) head(name string) (*BotHomeWorkspaceReceipt, error) {
	if !workspaceTask(name) {
		return nil, errHomeForbidden
	}
	h, err := p.receipt(name + "/.head")
	if err == nil && h.TaskPath != name {
		err = errHomeForbidden
	}
	return h, err
}

type workspaceRow struct {
	path string
	size int64
}

func (p *compiledWorkspace) inventory(name string) ([]workspaceRow, int, error) {
	return p.inventoryChecked(name, false)
}

// Physical inventories include reserved staging names for accounting and commit
// fencing; public request paths still use homeParts and cannot address them.
func workspaceInventoryPath(name string) error {
	parts := strings.Split(name, "/")
	for i, part := range parts {
		if homeInternalStage(part) {
			parts[i] = "reserved-stage"
		}
	}
	_, err := homeParts(strings.Join(parts, "/"), false)
	return err
}

func (p *compiledWorkspace) inventoryChecked(name string, knowledge bool) ([]workspaceRow, int, error) {
	rows := make([]workspaceRow, 0)
	nodes := 0
	var walk func(string) error
	walk = func(name string) error {
		d, err := homeOpen(p.root, name, unix.O_RDONLY|unix.O_DIRECTORY, 0)
		if err != nil {
			return err
		}
		defer d.Close()
		for {
			entries, err := d.ReadDir(128)
			if err != nil && err != io.EOF {
				return err
			}
			for _, e := range entries {
				nodes++
				if nodes > 8192 {
					return errors.New("workspace directory quota")
				}
				child := e.Name()
				if name != "." {
					child = name + "/" + child
				}
				if err := workspaceInventoryPath(child); err != nil {
					return err
				}
				f, err := homeOpen(p.root, child, unix.O_RDONLY, 0)
				if err != nil {
					return err
				}
				s, err := homeStat(f, false)
				f.Close()
				if err != nil {
					return err
				}
				switch s.Mode & unix.S_IFMT {
				case unix.S_IFDIR:
					if knowledge && strings.HasSuffix(child, ".md") {
						return errors.New("Markdown directory substitution")
					}
					if err = walk(child); err != nil {
						return err
					}
				case unix.S_IFREG:
					if s.Nlink != 1 {
						return errHomeForbidden
					}
					rows = append(rows, workspaceRow{child, s.Size})
					if len(rows) > 4096 {
						return errors.New("workspace file quota")
					}
				default:
					return errHomeForbidden
				}
			}
			if err == io.EOF {
				break
			}
		}
		return nil
	}
	err := walk(name)
	sort.Slice(rows, func(i, j int) bool { return rows[i].path < rows[j].path })
	return rows, nodes, err
}
func (p *compiledWorkspace) quota(add int64, count int, paths []string) error {
	rows, nodes, err := p.inventory(".")
	if err != nil {
		return err
	}
	var total int64
	for _, r := range rows {
		total += r.size
	}
	prospective := map[string]bool{}
	for _, name := range paths {
		a, err := homeParts(name, false)
		if err != nil {
			return err
		}
		for i := 1; i < len(a); i++ {
			prefix := strings.Join(a[:i], "/")
			if prospective[prefix] {
				continue
			}
			d, e := homeOpen(p.root, prefix, unix.O_RDONLY|unix.O_DIRECTORY, 0)
			if errors.Is(e, os.ErrNotExist) {
				prospective[prefix] = true
			} else if e != nil {
				return e
			} else {
				d.Close()
			}
		}
	}
	if total+add > 512<<20 || len(rows)+count > 4096 || nodes+len(prospective)+count > 8192 {
		return errors.New("workspace quota")
	}
	return nil
}

// workspaceFenceIdentity deliberately excludes access time: authorization reads
// may update atime but must not change namespace identity or file content. ctime
// and mtime are the same kernel-maintained content fence used by homeIdentity.
type workspaceFenceIdentity struct {
	Dev, Ino, Nlink uint64
	Mode            uint32
	Size            int64
	Mtime, Ctime    unix.Timespec
}

// fenceSnapshot includes directories (even empty ones), private content, stages,
// and the legacy lock itself. It is bounded by the existing workspace quotas.
// No read is served from this snapshot; it is only an optimistic commit fence.
func (p *compiledWorkspace) fenceSnapshot() (map[string]workspaceFenceIdentity, error) {
	result := make(map[string]workspaceFenceIdentity)
	var size int64
	files := 0
	var walk func(string) error
	walk = func(name string) error {
		f, err := homeOpen(p.root, name, unix.O_RDONLY, 0)
		if err != nil {
			return err
		}
		defer f.Close()
		s, err := homeStat(f, false)
		if err != nil {
			return err
		}
		result[name] = workspaceFenceIdentity{s.Dev, s.Ino, uint64(s.Nlink), s.Mode, s.Size, s.Mtim, s.Ctim}
		if len(result) > 8193 {
			return errors.New("workspace directory quota")
		}
		switch s.Mode & unix.S_IFMT {
		case unix.S_IFREG:
			if s.Nlink != 1 {
				return errHomeForbidden
			}
			files++
			size += s.Size
			if files > 4096 || size > 512<<20 {
				return errors.New("workspace quota")
			}
		case unix.S_IFDIR:
			for {
				entries, e := f.ReadDir(128)
				if e != nil && e != io.EOF {
					return e
				}
				for _, entry := range entries {
					child := entry.Name()
					if name != "." {
						child = name + "/" + child
					}
					if e := workspaceInventoryPath(child); e != nil {
						return e
					}
					if e := walk(child); e != nil {
						return e
					}
				}
				if e == io.EOF {
					break
				}
			}
		default:
			return errHomeForbidden
		}
		return nil
	}
	err := walk(".")
	return result, err
}

// hostExchange releases only the existing legacy flock, not the confinement or
// commit checks. Readers use LOCK_SH; all namespace mutations still use LOCK_EX.
// A callback may therefore read or run another writer without lock inversion.
// Any intervening mutation (including a legacy writer or private-file insertion)
// rejects this operation rather than merging stale inputs or losing its update.
// Reacquire even on denial so deferred stage cleanup remains serialized.
// This covers every READY (publish twice, write, knowledge publish/retry/verify,
// restore twice, script) as well as the scratch callback. Existing operation-
// specific staged-byte, CAS, parent, quota and restore checks remain mandatory.
func (p *compiledWorkspace) hostExchange(exchange func() error) (err error) {
	before, err := p.fenceSnapshot()
	if err != nil {
		return err
	}
	if err = unix.Flock(int(p.lock.Fd()), unix.LOCK_UN); err != nil {
		return err
	}
	defer func() {
		lockErr := unix.Flock(int(p.lock.Fd()), p.lockMode)
		err = errors.Join(err, lockErr)
		if err != nil {
			return
		}
		after, e := p.fenceSnapshot()
		if e != nil {
			err = e
			return
		}
		if !reflect.DeepEqual(before, after) {
			err = errors.New("workspace changed during host authorization")
		}
	}()
	return exchange()
}

func (p *compiledWorkspace) ready() error {
	return p.hostExchange(func() error {
		if _, err := io.WriteString(p.out, "WORKSPACE_READY\n"); err != nil {
			return err
		}
		line, err := workspaceFrame(p.in, 16)
		if err != nil {
			return err
		}
		if string(line) != "commit\n" {
			return errors.New("workspace commit rejected")
		}
		return nil
	})
}
func (p *compiledWorkspace) write(name string, b []byte, guard bool) error {
	a, err := homeParts(name, false)
	if err != nil {
		return err
	}
	d, err := homeDir(p.root, a[:len(a)-1], true)
	if err != nil {
		return err
	}
	defer d.Close()
	n := a[len(a)-1]
	var old *unix.Stat_t
	existing, err := homeFile(p.root, name)
	if err == nil {
		s, e := homeStat(existing, true)
		existing.Close()
		if e != nil {
			return e
		}
		old = &s
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temp := string(homeWorkspaceStagePrefix) + uuid.NewString()
	f, err := homeOpen(d, temp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer unix.Unlinkat(int(d.Fd()), temp, 0)
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if guard {
		if err = p.ready(); err != nil {
			return err
		}
	}
	// Re-open the ancestor and recheck destination after the host fence.
	current, err := homeDir(p.root, a[:len(a)-1], false)
	if err != nil {
		return err
	}
	defer current.Close()
	if err = workspaceSameDirectory(d, current); err != nil {
		return err
	}
	// The control-plane fence may block long enough for the retained tree to
	// change. Revalidate the exact staged bytes, not only the destination.
	staged, err := homeOpen(d, temp, unix.O_RDONLY, 0)
	if err != nil {
		return err
	}
	ss, err := homeStat(staged, true)
	if err == nil && ss.Size != int64(len(b)) {
		err = errors.New("workspace staged size changed")
	}
	if err == nil {
		var actual []byte
		actual, err = io.ReadAll(io.LimitReader(staged, int64(len(b))+1))
		if err == nil && !bytes.Equal(actual, b) {
			err = errors.New("workspace staged bytes changed")
		}
		if err == nil {
			err = homeUnchanged(staged, ss)
		}
	}
	err = errors.Join(err, staged.Close())
	if err != nil {
		return err
	}
	existing, err = homeFile(p.root, name)
	if old == nil {
		if err == nil {
			existing.Close()
			return errors.New("workspace destination appeared")
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		err = unix.Renameat2(int(d.Fd()), temp, int(d.Fd()), n, unix.RENAME_NOREPLACE)
	} else {
		if err != nil {
			return err
		}
		s, e := homeStat(existing, true)
		existing.Close()
		if e != nil {
			return e
		}
		if !homeIdentity(*old, s) {
			return errors.New("workspace destination changed")
		}
		err = unix.Renameat(int(d.Fd()), temp, int(d.Fd()), n)
	}
	if err != nil {
		return err
	}
	return d.Sync()
}
func workspaceSameDirectory(a, b *os.File) error {
	as, err := homeStat(a, false)
	if err != nil {
		return err
	}
	bs, err := homeStat(b, false)
	if err != nil {
		return err
	}
	if as.Dev != bs.Dev || as.Ino != bs.Ino {
		return errHomeForbidden
	}
	return nil
}

func (p *compiledWorkspace) snapshot(h *BotHomeWorkspaceReceipt) ([]BotHomeWorkspaceFile, error) {
	return p.snapshotRevision(h, h.PublicationID)
}
func (p *compiledWorkspace) snapshotRevision(h *BotHomeWorkspaceReceipt, revision string) ([]BotHomeWorkspaceFile, error) {
	if err := p.validate(h); err != nil {
		return nil, err
	}
	prefix := h.TaskPath + "/.revisions/" + revision + "/"
	rows, _, err := p.inventory(strings.TrimSuffix(prefix, "/"))
	if err != nil {
		return nil, err
	}
	if len(rows) != len(h.Entries) {
		return nil, errors.New("workspace undeclared file")
	}
	files := make([]BotHomeWorkspaceFile, 0, len(rows))
	for i, e := range h.Entries {
		if rows[i].path != prefix+e.Path {
			return nil, errors.New("workspace inventory mismatch")
		}
		b, err := p.read(prefix+e.Path, 1<<20)
		if err != nil {
			return nil, err
		}
		if int64(len(b)) != e.SizeBytes || workspaceHash(b) != e.Digest {
			return nil, errors.New("workspace byte digest mismatch")
		}
		files = append(files, BotHomeWorkspaceFile{Path: e.Path, Data: b, Executable: e.Executable})
	}
	return files, nil
}
func (p *compiledWorkspace) publish() (*BotHomeWorkspaceReceipt, error) {
	q := p.q.Request
	if err := q.Validate(); err != nil {
		return nil, err
	}
	h := &BotHomeWorkspaceReceipt{ReceiptRef: q.PublicationID, PublicationID: q.PublicationID, TaskPath: q.TaskPath, BindingKey: p.q.Binding, Entries: make([]BotHomeWorkspaceEntry, 0, len(q.Files))}
	for _, f := range q.Files {
		h.Entries = append(h.Entries, BotHomeWorkspaceEntry{Path: f.Path, SizeBytes: int64(len(f.Data)), Digest: f.Digest(), Executable: f.Executable})
	}
	sort.Slice(h.Entries, func(i, j int) bool { return h.Entries[i].Path < h.Entries[j].Path })
	h.Digest = workspaceEntriesDigest(h.Entries)
	if err := p.validate(h); err != nil {
		return nil, err
	}
	old, err := p.head(q.TaskPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if old != nil && old.PublicationID == q.PublicationID {
		if _, err = p.snapshot(old); err != nil {
			return nil, err
		}
		if !reflect.DeepEqual(old, h) {
			return nil, errors.New("edited workspace retry")
		}
		return old, nil
	}
	expected := ""
	if old != nil {
		expected = old.Digest
	}
	if expected != q.ExpectedDigest {
		return nil, errors.New("workspace head CAS")
	}
	// Publication references are globally unique within the private workspace.
	prior, e := p.receipt(".receipts/" + q.PublicationID)
	if e == nil && !reflect.DeepEqual(prior, h) {
		return nil, errors.New("workspace receipt collision")
	}
	if e != nil && !errors.Is(e, os.ErrNotExist) {
		return nil, e
	}
	stage := ".revision-stage-" + uuid.NewString()
	base := q.TaskPath + "/.revisions"
	paths := []string{".receipts/" + q.PublicationID, q.TaskPath + "/.head"}
	var size int64
	for _, f := range q.Files {
		paths = append(paths, base+"/"+stage+"/"+f.Path)
		size += int64(len(f.Data))
	}
	if err = p.quota(size+(2<<20), len(q.Files)+2, paths); err != nil {
		return nil, err
	}
	a, _ := homeParts(base, false)
	d, err := homeDir(p.root, a, true)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	dest, e := homeOpen(p.root, base+"/"+q.PublicationID, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if e == nil {
		dest.Close()
	} else if !errors.Is(e, os.ErrNotExist) {
		return nil, e
	} else {
		if err = unix.Mkdirat(int(d.Fd()), stage, 0700); err != nil {
			return nil, err
		}
		defer workspaceRemoveTree(d, stage)
		if err = d.Sync(); err != nil {
			return nil, err
		}
		for _, f := range q.Files {
			if err = p.write(base+"/"+stage+"/"+f.Path, f.Data, false); err != nil {
				return nil, err
			}
		}
		if _, err = p.snapshotRevision(h, stage); err != nil {
			return nil, err
		}
		if err = p.ready(); err != nil {
			return nil, err
		}
		current, err := homeDir(p.root, a, false)
		if err != nil {
			return nil, err
		}
		err = workspaceSameDirectory(d, current)
		current.Close()
		if err != nil {
			return nil, err
		}
		if _, err = p.snapshotRevision(h, stage); err != nil {
			return nil, err
		}
		if err = unix.Renameat2(int(d.Fd()), stage, int(d.Fd()), q.PublicationID, unix.RENAME_NOREPLACE); err != nil {
			return nil, err
		}
		if err = d.Sync(); err != nil {
			return nil, err
		}
	}
	if _, err = p.snapshot(h); err != nil {
		return nil, err
	}
	if err = p.ready(); err != nil {
		return nil, err
	}
	if _, err = p.snapshot(h); err != nil {
		return nil, err
	}
	// Recheck the logical CAS after the second host fence as well.
	currentHead, err := p.head(q.TaskPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if !reflect.DeepEqual(currentHead, old) {
		return nil, errors.New("workspace head changed at commit")
	}
	raw, err := json.Marshal(h)
	if err != nil {
		return nil, err
	}
	if err = p.write(".receipts/"+q.PublicationID, raw, false); err != nil {
		return nil, err
	}
	if err = p.write(q.TaskPath+"/.head", raw, false); err != nil {
		return nil, err
	}
	return h, nil
}

// knowledgePath mirrors the durable Markdown namespace, not a model tool path.
func workspaceKnowledgePath(name string) bool {
	a, err := homeParts(name, false)
	if err != nil || len(name) > 240 || len(a) < 2 || !strings.HasSuffix(name, ".md") || !workspacePublicPath(name) {
		return false
	}
	switch a[0] {
	case "knowledge", "preferences", "procedures", "failures":
		return true
	}
	return false
}

type workspaceKnowledgeProposal struct {
	Path           string `json:"path"`
	Content        string `json:"content"`
	ExpectedDigest string `json:"expected_digest"`
}
type workspaceKnowledgeRecord struct {
	ID    string `json:"id"`
	Input struct {
		Source json.RawMessage `json:"source"`
	} `json:"input"`
	Decision struct {
		Proposals json.RawMessage `json:"proposals"`
	} `json:"decision"`
	DecisionDigest string `json:"decision_digest"`
}

// Keep receipt identity as JSON values: nil vs [] and every proposal/source field
// are significant to legacy exact retries. Formatting/escaping are not identity.
func workspaceKnowledgeJSONEqual(a, b []byte) bool {
	var x, y any
	decode := func(raw []byte, out *any) error {
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		return d.Decode(out)
	}
	return json.Valid(a) && json.Valid(b) && decode(a, &x) == nil && decode(b, &y) == nil && reflect.DeepEqual(x, y)
}
func (p *compiledWorkspace) knowledgeTree(name string) (map[string][]byte, error) {
	d, err := homeOpen(p.root, name, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	rows, _, err := p.inventoryChecked(name, true)
	if err != nil {
		return nil, fmt.Errorf("knowledge inventory: %v", err)
	}
	files := make(map[string][]byte, len(rows))
	var retained int64
	for _, row := range rows {
		retained += row.size
		if row.size < 0 || row.size > 1<<20 || retained > 512<<20 {
			return nil, errors.New("knowledge retained quota")
		}
	}
	var total int64
	count := 0
	for _, row := range rows {
		rel := strings.TrimPrefix(row.path, name+"/")
		b, err := p.read(row.path, 1<<20)
		if err != nil {
			return nil, fmt.Errorf("knowledge read: %v", err)
		}
		if !strings.HasPrefix(rel, ".publications/") {
			if !workspaceKnowledgePath(rel) {
				return nil, errors.New("knowledge path")
			}
			if !utf8.Valid(b) {
				return nil, errors.New("knowledge UTF-8")
			}
			total += int64(len(b))
			count++
			if count > 64 || total > 1<<20 {
				return nil, errors.New("knowledge snapshot quota")
			}
		}
		files[rel] = b
	}
	current, err := homeOpen(p.root, name, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, fmt.Errorf("knowledge directory changed: %v", err)
	}
	err = workspaceSameDirectory(d, current)
	current.Close()
	if err != nil {
		return nil, err
	}
	return files, nil
}

// Cleanup only this operation's stage, walking confined descriptors. Never follow
// a substituted symlink or recursively remove through an absolute retained path.
func workspaceRemoveTree(parent *os.File, name string) error {
	d, err := homeOpen(parent, name, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer d.Close()
	entries, err := d.ReadDir(-1)
	if err != nil {
		return err
	}
	if len(entries) > 8192 {
		return errors.New("workspace directory quota")
	}
	for _, e := range entries {
		if e.IsDir() {
			err = workspaceRemoveTree(d, e.Name())
		} else {
			err = unix.Unlinkat(int(d.Fd()), e.Name(), 0)
		}
		if err != nil {
			return err
		}
	}
	current, err := homeOpen(parent, name, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return err
	}
	err = workspaceSameDirectory(d, current)
	current.Close()
	if err != nil {
		return err
	}
	return unix.Unlinkat(int(parent.Fd()), name, unix.AT_REMOVEDIR)
}

func (p *compiledWorkspace) knowledgeOperation() (result any, err error) {
	old, err := p.knowledgeTree(".knowledge")
	if errors.Is(err, os.ErrNotExist) {
		old = map[string][]byte{}
		err = nil
	}
	if err != nil {
		return nil, err
	}
	if p.q.Op == workspaceKnowledgeSnapshot {
		names := make([]string, 0, len(old))
		for name := range old {
			if !strings.HasPrefix(name, ".publications/") {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		rows := make([]map[string]string, 0, len(names))
		for _, name := range names {
			rows = append(rows, map[string]string{"path": name, "digest": workspaceHash(old[name]), "summary": string(old[name])})
		}
		return rows, nil
	}
	var r workspaceKnowledgeRecord
	if !utf8.Valid(p.q.Record) {
		return nil, errors.New("knowledge UTF-8")
	}
	if err = json.Unmarshal(p.q.Record, &r); err != nil {
		return nil, err
	}
	if !workspacePublicReference(r.ID) {
		return nil, errors.New("knowledge publication")
	}
	var props []workspaceKnowledgeProposal
	if err = json.Unmarshal(r.Decision.Proposals, &props); err != nil {
		return nil, err
	}
	if len(props) == 0 || len(props) > 64 || !json.Valid(r.Input.Source) {
		return nil, errors.New("knowledge proposals")
	}
	identity, err := json.Marshal(map[string]any{"binding": p.q.Binding, "id": r.ID, "source": r.Input.Source, "decision_digest": r.DecisionDigest, "proposals": r.Decision.Proposals})
	if err != nil {
		return nil, err
	}
	if len(identity) > 1<<20 {
		return nil, errors.New("knowledge receipt quota")
	}
	expected := make([]map[string]string, 0, len(props))
	seen := map[string]bool{}
	for _, prop := range props {
		if !workspaceKnowledgePath(prop.Path) || seen[prop.Path] {
			return nil, errors.New("knowledge path")
		}
		seen[prop.Path] = true
		expected = append(expected, map[string]string{"path": prop.Path, "digest": workspaceHash([]byte(prop.Content))})
	}
	receipt := ".publications/" + r.ID
	if prior, ok := old[receipt]; ok {
		if !workspaceKnowledgeJSONEqual(prior, identity) {
			return nil, errors.New("edited knowledge retry")
		}
		for _, prop := range props {
			if b, ok := old[prop.Path]; !ok || !bytes.Equal(b, []byte(prop.Content)) {
				return nil, errors.New("knowledge readback mismatch")
			}
		}
		if err = p.ready(); err != nil {
			return nil, err
		}
		current, e := p.knowledgeTree(".knowledge")
		if e != nil {
			return nil, e
		}
		if !reflect.DeepEqual(old, current) {
			return nil, errors.New("knowledge readback mismatch")
		}
		return expected, nil
	}
	if p.q.Op == workspaceKnowledgeVerify {
		return nil, errors.New("knowledge receipt missing")
	}
	staged := make(map[string][]byte, len(old)+len(props)+1)
	for name, b := range old {
		staged[name] = b
	}
	for _, prop := range props {
		digest := ""
		if b, ok := staged[prop.Path]; ok {
			digest = workspaceHash(b)
		}
		if digest != prop.ExpectedDigest {
			return nil, errors.New("knowledge CAS")
		}
		staged[prop.Path] = []byte(prop.Content)
	}
	staged[receipt] = identity
	var size, contentSize int64
	count := 0
	stage := ".knowledge-stage-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	paths := make([]string, 0, len(staged))
	for name, b := range staged {
		size += int64(len(b))
		paths = append(paths, stage+"/"+name)
		if !strings.HasPrefix(name, ".publications/") {
			contentSize += int64(len(b))
			count++
		}
	}
	if count > 64 || contentSize > 1<<20 {
		return nil, errors.New("knowledge quota")
	}
	if err = p.quota(size, len(staged), paths); err != nil {
		return nil, err
	}
	previous, e := homeOpen(p.root, ".knowledge", unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if e != nil && !errors.Is(e, os.ErrNotExist) {
		return nil, e
	}
	if previous != nil {
		defer previous.Close()
	}
	if err = unix.Mkdirat(int(p.root.Fd()), stage, 0700); err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, workspaceRemoveTree(p.root, stage)) }()
	stageDir, err := homeOpen(p.root, stage, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, err
	}
	defer stageDir.Close()
	for name, b := range staged {
		if err = p.write(stage+"/"+name, b, false); err != nil {
			return nil, err
		}
	}
	checkStage := func() error {
		current, e := homeOpen(p.root, stage, unix.O_RDONLY|unix.O_DIRECTORY, 0)
		if e != nil {
			return e
		}
		defer current.Close()
		if e = workspaceSameDirectory(stageDir, current); e != nil {
			return e
		}
		actual, e := p.knowledgeTree(stage)
		if e != nil {
			return e
		}
		if !reflect.DeepEqual(staged, actual) {
			return errors.New("knowledge staging readback")
		}
		return nil
	}
	if err = checkStage(); err != nil {
		return nil, err
	}
	if err = p.ready(); err != nil {
		return nil, err
	}
	if err = checkStage(); err != nil {
		return nil, err
	}
	current, e := homeOpen(p.root, ".knowledge", unix.O_RDONLY|unix.O_DIRECTORY, 0)
	flags := uint(unix.RENAME_NOREPLACE)
	if previous == nil {
		if current != nil {
			current.Close()
			return nil, errors.New("knowledge destination appeared")
		}
		if !errors.Is(e, os.ErrNotExist) {
			return nil, e
		}
	} else {
		if e != nil {
			return nil, e
		}
		e = workspaceSameDirectory(previous, current)
		current.Close()
		if e != nil {
			return nil, e
		}
		actual, e := p.knowledgeTree(".knowledge")
		if e != nil {
			return nil, e
		}
		if !reflect.DeepEqual(old, actual) {
			return nil, errors.New("knowledge CAS")
		}
		flags = unix.RENAME_EXCHANGE
	}
	if err = unix.Renameat2(int(p.root.Fd()), stage, int(p.root.Fd()), ".knowledge", flags); err != nil {
		return nil, err
	}
	if err = p.root.Sync(); err != nil {
		return nil, err
	}
	actual, err := p.knowledgeTree(".knowledge")
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(staged, actual) {
		return nil, errors.New("knowledge committed readback")
	}
	return expected, nil
}

func (p *compiledWorkspace) physical(name string) (string, error) {
	a, err := homeParts(name, false)
	if err != nil {
		return "", err
	}
	if len(a) >= 5 && a[0] == "tasks" && a[2] == ".revisions" && workspacePublicPath(strings.Join(append(append([]string{}, a[:2]...), a[3:]...), "/")) {
		h, err := p.receipt(".receipts/" + a[3])
		if err != nil {
			return "", err
		}
		if _, err = p.snapshot(h); err != nil {
			return "", err
		}
		if h.TaskPath != strings.Join(a[:2], "/") {
			return "", errHomeForbidden
		}
		for _, e := range h.Entries {
			if e.Path == strings.Join(a[4:], "/") {
				return name, nil
			}
		}
		return "", errHomeForbidden
	}
	if !workspacePublicPath(name) {
		return "", errHomeForbidden
	}
	if len(a) >= 3 && a[0] == "tasks" && a[2] != "state" && a[2] != "outputs" && a[2] != "drafts" {
		h, err := p.head(strings.Join(a[:2], "/"))
		if err != nil {
			return "", err
		}
		if _, err = p.snapshot(h); err != nil {
			return "", err
		}
		for _, e := range h.Entries {
			if e.Path == strings.Join(a[2:], "/") {
				return h.TaskPath + "/.revisions/" + h.PublicationID + "/" + e.Path, nil
			}
		}
		return "", errHomeForbidden
	}
	return name, nil
}
func (p *compiledWorkspace) writeState() error {
	a, err := homeParts(p.q.Path, false)
	if err != nil {
		return err
	}
	if len(a) < 4 || a[0] != "tasks" || (a[2] != "state" && a[2] != "outputs" && a[2] != "drafts") || !workspacePublicPath(p.q.Path) {
		return errHomeForbidden
	}
	if len(p.q.Data) > 1<<20 {
		return errors.New("workspace asset quota")
	}
	if err = p.quota(int64(len(p.q.Data)), 1, []string{p.q.Path}); err != nil {
		return err
	}
	rows, _, err := p.inventory(strings.Join(a[:3], "/"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	total := int64(len(p.q.Data))
	for _, r := range rows {
		if r.path != p.q.Path {
			total += r.size
		}
	}
	if total > 128<<20 {
		return errors.New("workspace state quota")
	}
	return p.write(p.q.Path, p.q.Data, true)
}
func (p *compiledWorkspace) snapshotRequest() (*BotHomeWorkspaceSnapshot, error) {
	if p.q.Limit < 1 || p.q.Limit > 16<<20 {
		return nil, errors.New("workspace snapshot limit")
	}
	if p.q.Path != "workspace" {
		h, err := p.head(p.q.Path)
		if err != nil {
			return nil, err
		}
		files, err := p.snapshot(h)
		if err != nil {
			return nil, err
		}
		var total int64
		for _, e := range h.Entries {
			total += e.SizeBytes
		}
		if total > p.q.Limit {
			return nil, errors.New("workspace snapshot limit")
		}
		return &BotHomeWorkspaceSnapshot{Files: files, Receipt: *h}, nil
	}
	rows, _, err := p.inventory(".")
	if err != nil {
		return nil, err
	}
	result := &BotHomeWorkspaceSnapshot{Files: []BotHomeWorkspaceFile{}, Receipt: BotHomeWorkspaceReceipt{BindingKey: p.q.Binding, TaskPath: "workspace", Entries: []BotHomeWorkspaceEntry{}}}
	var total int64
	for _, r := range rows {
		if r.path == ".workspace-lock" {
			continue
		}
		total += r.size
		if total > p.q.Limit {
			return nil, fmt.Errorf("workspace snapshot limit")
		}
		b, err := p.read(r.path, 128<<20)
		if err != nil {
			return nil, err
		}
		result.Files = append(result.Files, BotHomeWorkspaceFile{Path: r.path, Data: b})
		result.Receipt.Entries = append(result.Receipt.Entries, BotHomeWorkspaceEntry{Path: r.path, SizeBytes: int64(len(b)), Digest: workspaceHash(b)})
	}
	result.Receipt.Digest = workspaceCanonicalDigest(result.Receipt.Entries, true)
	return result, nil
}
