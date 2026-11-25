package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

// botPublicationReader resolves a previously committed publication; reads must
// never invoke PublishReport or look at a newer run's mutable report.
type botPublicationReader interface {
	ReadBotPublication(context.Context, store.BotID, store.RunID, int64) (*store.BotPublication, error)
}

func registerBotFileRoutes(r *mux.Router) {
	root := "/api/studio/bots/{botID}"
	r.HandleFunc(root+"/files", BotFilesHandler).Methods(http.MethodGet)
	for _, base := range []string{root + "/files", root + "/runs/{runID}/reports/{reportSequence}/artifacts/{artifactID}"} {
		r.HandleFunc(base+"/stat", BotFileStatHandler).Methods(http.MethodGet)
		r.HandleFunc(base+"/preview", BotFilePreviewHandler).Methods(http.MethodGet)
		r.HandleFunc(base+"/download", BotFileDownloadHandler).Methods(http.MethodGet, http.MethodHead)
		r.HandleFunc(base+"/download-ticket", BotFileDownloadTicketHandler).Methods(http.MethodPost)
	}
}

func botFileError(w http.ResponseWriter, err error) {
	if errors.Is(err, fs.ErrNotExist) {
		err = store.ErrBotNotFound
	}
	if errors.Is(err, fs.ErrPermission) {
		err = store.ErrBotForbidden
	}
	botHTTPError(w, err)
}
func botFileHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
}
func botFileService(w http.ResponseWriter, r *http.Request) (store.BotFiles, store.BotStore, *store.Bot, bool) {
	botFileHeaders(w)
	repo, _, ok := botRequestStore(w, r)
	if !ok {
		return nil, nil, nil, false
	}
	id, ok := botPathID(w, r)
	if !ok {
		return nil, nil, nil, false
	}
	b, err := repo.Get(r.Context(), id)
	if err == nil && (b == nil || b.ID != id) {
		err = store.ErrBotNotFound
	}
	requestedScope := store.BotScope(r.URL.Query().Get("scope"))
	if requestedScope == "" {
		requestedScope = store.BotScopePersonal
	}
	if err == nil && b.Scope != requestedScope {
		err = store.ErrBotForbidden
	}
	if err == nil && b.PermanentDelete {
		http.Error(w, "Bot content unavailable", http.StatusGone)
		return nil, nil, nil, false
	}
	if err != nil {
		botFileError(w, err)
		return nil, nil, nil, false
	}
	svc := store.FromRequest(r)
	if svc == nil || svc.BotFilesFactory == nil {
		botFileError(w, store.ErrBotUnsupported)
		return nil, nil, nil, false
	}
	files, err := svc.BotFilesFactory(r.Context(), svc, b.Scope)
	if err == nil && files == nil {
		err = store.ErrBotUnsupported
	}
	if err != nil {
		botFileError(w, err)
		return nil, nil, nil, false
	}
	return files, repo, b, true
}

func botPublicFilePath(p string, root bool) bool {
	if root && p == "" {
		return true
	}
	if sandbox.ValidateBotHomeArtifactPath(p) != nil {
		return false
	}
	// Home is a separate retained mount; descriptor walks reject links and mount
	// escapes. Dotfiles are not inherently private, but credential/runtime trees
	// and writer staging objects must remain inaccessible even by direct URL.
	parts := strings.Split(p, "/")
	for i, part := range parts {
		switch part {
		case ".ssh", ".aws", ".azure", ".kube", ".docker", ".gnupg", ".pki", ".netrc", ".npmrc", ".pypirc", ".git-credentials", ".git", ".env", ".bash_history", ".zsh_history", ".python_history":
			return false
		}
		if strings.HasPrefix(part, ".env.") || part == ".astonish" || strings.HasPrefix(part, ".astonish-") || strings.HasPrefix(part, ".stage-") || strings.HasPrefix(part, ".tmp-") {
			return false
		}
		if part == ".config" && i+1 < len(parts) {
			switch parts[i+1] {
			case "astonish", "gcloud", "gh", "op", "rclone":
				return false
			}
		}
		if (part == ".cache" || part == ".local") && i+1 < len(parts) {
			rest := strings.Join(parts[i+1:], "/")
			if rest == "astonish" || strings.HasPrefix(rest, "astonish/") || rest == "share/astonish" || strings.HasPrefix(rest, "share/astonish/") || rest == "state/astonish" || strings.HasPrefix(rest, "state/astonish/") {
				return false
			}
		}
	}
	return !strings.ContainsFunc(p, func(r rune) bool { return r < 32 || r == 127 })
}

func botHiddenFilePath(p string) bool {
	for _, part := range strings.Split(p, "/") {
		if strings.HasPrefix(part, ".") {
			return true
		}
	}
	return false
}
func botFilePreviewKind(p string) store.BotFilePreviewKind {
	switch strings.ToLower(path.Ext(p)) {
	case ".txt", ".md", ".markdown", ".csv", ".tsv", ".json", ".yaml", ".yml", ".toml", ".xml", ".html", ".htm", ".svg", ".css", ".js", ".ts", ".tsx", ".jsx", ".py", ".go", ".rs", ".java", ".c", ".h", ".cpp", ".sh", ".sql", ".mmd", ".mermaid", ".drawio":
		return store.BotFilePreviewText
	default:
		return store.BotFilePreviewDownload
	}
}
func botSafeFileEntry(e store.BotFileEntry) store.BotFileEntry {
	e.Preview = botFilePreviewKind(e.Path)
	if e.Kind != store.BotFileRegular {
		e.Preview = store.BotFilePreviewDownload
	}
	return e
}
func BotFilesHandler(w http.ResponseWriter, r *http.Request) {
	files, _, b, ok := botFileService(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	p := q.Get("path")
	showHidden := q.Get("show_hidden") == "true"
	if value := q.Get("show_hidden"); value != "" && value != "true" && value != "false" {
		botFileError(w, store.ErrBotInvalid)
		return
	}
	limit := 100
	if q.Get("limit") != "" {
		n, e := strconv.Atoi(q.Get("limit"))
		if e != nil || n < 1 || n > 200 {
			botFileError(w, store.ErrBotInvalid)
			return
		}
		limit = n
	}
	if !botPublicFilePath(p, true) || (!showHidden && botHiddenFilePath(p)) || len(q.Get("cursor")) > 4096 {
		botFileError(w, store.ErrBotInvalid)
		return
	}
	page, err := files.List(r.Context(), b.ID, p, q.Get("cursor"), limit)
	if err != nil {
		botFileError(w, err)
		return
	}
	if page == nil {
		botFileError(w, store.ErrBotNotFound)
		return
	}
	out := store.BotFilePage{Entries: []store.BotFileEntry{}, Next: page.Next}
	for _, e := range page.Entries {
		if botPublicFilePath(e.Path, false) && (showHidden || !botHiddenFilePath(e.Path)) && (e.Kind == store.BotFileRegular || e.Kind == store.BotFileDirectory) {
			out.Entries = append(out.Entries, botSafeFileEntry(e))
		}
	}
	botWriteJSON(w, 200, out)
}

type botFileObject struct {
	entry store.BotFileEntry
	open  func(store.BotFileRange) (*store.BotFileStream, error)
}

func botResolveFile(w http.ResponseWriter, r *http.Request) (*botFileObject, bool) {
	files, repo, b, ok := botFileService(w, r)
	if !ok {
		return nil, false
	}
	v := mux.Vars(r)
	if v["artifactID"] != "" {
		aid, e1 := uuid.Parse(v["artifactID"])
		rid, e2 := uuid.Parse(v["runID"])
		seq, e3 := strconv.ParseInt(v["reportSequence"], 10, 64)
		if e1 != nil || e2 != nil || e3 != nil || seq < 1 {
			botFileError(w, store.ErrBotInvalid)
			return nil, false
		}
		reader, ok := repo.(botPublicationReader)
		if !ok {
			botFileError(w, store.ErrBotUnsupported)
			return nil, false
		}
		pub, err := reader.ReadBotPublication(r.Context(), b.ID, rid, seq)
		if err != nil {
			botFileError(w, err)
			return nil, false
		}
		if pub == nil || pub.BotID != b.ID || pub.RunID != rid || pub.ReportSequence != seq || pub.HomeSessionID != b.HomeSessionID || pub.Suppressed || pub.EventID == "" {
			botFileError(w, store.ErrBotNotFound)
			return nil, false
		}
		for _, a := range pub.Artifacts {
			if a.ID != aid {
				continue
			}
			ref := a.ContentRef
			tc := store.TenantContextFrom(r.Context())
			if ref == nil || ref.Validate() != nil || ref.Kind != store.BotContentArtifact || ref.Scope.BotID != b.ID || ref.Scope.OwnerID != b.OwnerID.String() || tc == nil || ref.Scope.OrgID != tc.OrgSlug || ref.Scope.TeamID != tc.TeamSlug || a.HomeID != b.Home.ID || a.SHA256 != ref.SHA256 || a.SizeBytes != ref.SizeBytes || !botPublicFilePath(a.RelativePath, false) {
				botFileError(w, store.ErrBotNotFound)
				return nil, false
			}
			exact := *ref
			e := botSafeFileEntry(store.BotFileEntry{Path: a.RelativePath, Kind: store.BotFileRegular, SizeBytes: ref.SizeBytes, SHA256: ref.SHA256, Freshness: ref.SHA256})
			return &botFileObject{entry: e, open: func(br store.BotFileRange) (*store.BotFileStream, error) {
				return files.OpenContent(r.Context(), exact, br)
			}}, true
		}
		botFileError(w, store.ErrBotNotFound)
		return nil, false
	}
	p := r.URL.Query().Get("path")
	if !botPublicFilePath(p, false) {
		botFileError(w, store.ErrBotInvalid)
		return nil, false
	}
	e, err := files.Stat(r.Context(), b.ID, p)
	if err != nil {
		botFileError(w, err)
		return nil, false
	}
	if e == nil || e.Path != p || e.SizeBytes < 0 || (e.Kind != store.BotFileRegular && e.Kind != store.BotFileDirectory) {
		botFileError(w, store.ErrBotNotFound)
		return nil, false
	}
	return &botFileObject{entry: botSafeFileEntry(*e), open: func(br store.BotFileRange) (*store.BotFileStream, error) { return files.Open(r.Context(), b.ID, p, br) }}, true
}
func BotFileStatHandler(w http.ResponseWriter, r *http.Request) {
	o, ok := botResolveFile(w, r)
	if ok {
		botWriteJSON(w, 200, o.entry)
	}
}

// botFilePreviewRedactor uses only the Bot's request-scoped credential stores.
// Team previews deliberately exclude the viewer's personal store, so every
// authorized viewer sees the same redaction. Never fall back to daemon secrets.
func botFilePreviewRedactor(r *http.Request) (*credentials.Redactor, error) {
	svc := store.FromRequest(r)
	if svc == nil {
		return nil, store.ErrBotUnsupported
	}
	stores := []store.CredentialStore{svc.Credentials}
	if store.BotScope(r.URL.Query().Get("scope")) != store.BotScopeTeam {
		stores = append(stores, svc.PersonalCredentials)
	}
	redactor := credentials.NewRedactor()
	available := false
	for _, cs := range stores {
		if cs == nil {
			continue
		}
		available = true
		if err := cs.Reload(r.Context()); err != nil {
			return nil, store.ErrBotUnsupported
		}
		// Hydrate each store separately: personal shadowing must not erase a
		// team's secret signatures from retained output.
		redactor.HydrateFromStore(cs)
	}
	if !available || r.Context().Err() != nil {
		return nil, store.ErrBotUnsupported
	}
	return redactor, nil
}

func BotFilePreviewHandler(w http.ResponseWriter, r *http.Request) {
	o, ok := botResolveFile(w, r)
	if !ok {
		return
	}
	if o.entry.Kind != store.BotFileRegular || o.entry.Preview != store.BotFilePreviewText {
		http.Error(w, "Download required", http.StatusUnsupportedMediaType)
		return
	}
	redactor, err := botFilePreviewRedactor(r)
	if err != nil {
		botFileError(w, err)
		return
	}
	truncated := o.entry.SizeBytes > store.BotFilePreviewMaxBytes
	previewBytes := o.entry.SizeBytes
	br := store.BotFileRange{}
	if truncated {
		previewBytes = store.BotFilePreviewMaxBytes
		br.Length = previewBytes
	}
	stream, err := o.open(br)
	if err != nil {
		botFileError(w, err)
		return
	}
	if stream == nil || stream.ReadCloser == nil {
		botFileError(w, store.ErrBotNotFound)
		return
	}
	defer botOwnFileStream(r.Context(), stream)()
	data, err := io.ReadAll(io.LimitReader(stream, store.BotFilePreviewMaxBytes+1))
	closeErr := stream.Close()
	if err != nil || closeErr != nil {
		botFileError(w, store.ErrBotConflict)
		return
	}
	if int64(len(data)) != previewBytes || stream.Entry.SizeBytes != o.entry.SizeBytes || (o.entry.Freshness != "" && stream.Entry.Freshness != o.entry.Freshness && o.entry.SHA256 == "") {
		botFileError(w, store.ErrBotConflict)
		return
	}
	// A bounded window may end inside a valid UTF-8 rune. Trim only that
	// incomplete final rune; invalid bytes anywhere else still require download.
	if truncated {
		start := len(data) - 1
		for start > 0 && !utf8.RuneStart(data[start]) {
			start--
		}
		if start >= 0 && !utf8.FullRune(data[start:]) {
			data = data[:start]
		}
	}
	if !utf8.Valid(data) || strings.IndexByte(string(data), 0) >= 0 {
		http.Error(w, "Download required", http.StatusUnsupportedMediaType)
		return
	}
	// HTML/SVG are JSON source text only, never executable document responses.
	botWriteJSON(w, 200, struct {
		Entry     store.BotFileEntry `json:"entry"`
		Content   string             `json:"content"`
		Truncated bool               `json:"truncated"`
	}{o.entry, redactor.Redact(string(data)), truncated})
}

func botDownloadRange(value string, size int64) (store.BotFileRange, bool, error) {
	if value == "" {
		return store.BotFileRange{}, false, nil
	}
	bad := func() (store.BotFileRange, bool, error) { return store.BotFileRange{}, false, store.ErrBotInvalid }
	if !strings.HasPrefix(value, "bytes=") || strings.Contains(value, ",") || size == 0 {
		return bad()
	}
	parts := strings.Split(strings.TrimPrefix(value, "bytes="), "-")
	if len(parts) != 2 {
		return bad()
	}
	if parts[0] == "" {
		n, e := strconv.ParseInt(parts[1], 10, 64)
		if e != nil || n <= 0 {
			return bad()
		}
		if n > size {
			n = size
		}
		return store.BotFileRange{Offset: size - n, Length: n}, true, nil
	}
	start, e := strconv.ParseInt(parts[0], 10, 64)
	if e != nil || start < 0 || start >= size {
		return bad()
	}
	end := size - 1
	if parts[1] != "" {
		end, e = strconv.ParseInt(parts[1], 10, 64)
		if e != nil || end < start {
			return bad()
		}
		if end >= size {
			end = size - 1
		}
	}
	return store.BotFileRange{Offset: start, Length: end - start + 1}, true, nil
}
func BotFileDownloadHandler(w http.ResponseWriter, r *http.Request) {
	// Normal authenticated API clients may stream directly. Browser navigation
	// uses the additional exact-object ticket, never a bearer in the URL.
	if r.Header.Get("Sec-Fetch-Mode") == "navigate" && !botFileTicketAllowed(r) {
		http.Error(w, "Download ticket required", http.StatusForbidden)
		return
	}
	o, ok := botResolveFile(w, r)
	if !ok {
		return
	}
	if o.entry.Kind != store.BotFileRegular {
		botFileError(w, store.ErrBotInvalid)
		return
	}
	br, partial, err := botDownloadRange(r.Header.Get("Range"), o.entry.SizeBytes)
	if err != nil {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", o.entry.SizeBytes))
		http.Error(w, "Range not satisfiable", 416)
		return
	}
	size := o.entry.SizeBytes
	if partial {
		size = br.Length
	}
	var stream *store.BotFileStream
	if r.Method != http.MethodHead {
		stream, err = o.open(br)
		if err != nil {
			botFileError(w, err)
			return
		}
		if stream == nil || stream.ReadCloser == nil {
			botFileError(w, store.ErrBotNotFound)
			return
		}
		defer botOwnFileStream(r.Context(), stream)()
		if stream.Entry.SizeBytes != o.entry.SizeBytes || (o.entry.Freshness != "" && stream.Entry.Freshness != o.entry.Freshness && o.entry.SHA256 == "") {
			botFileError(w, store.ErrBotConflict)
			return
		}
		if size == 0 {
			if err := botCopyDownload(io.Discard, stream, 0); err != nil {
				botFileError(w, store.ErrBotConflict)
				return
			}
			stream = nil
		}
	}
	name := strings.Map(func(c rune) rune {
		if c < 32 || c == 127 || c == '/' || c == '\\' {
			return '_'
		}
		return c
	}, path.Base(o.entry.Path))
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	if partial {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", br.Offset, br.Offset+size-1, o.entry.SizeBytes))
		w.WriteHeader(206)
	} else {
		w.WriteHeader(200)
	}
	if stream == nil {
		return
	}
	// Hold the final byte until EOF and Close are confirmed. A transport can
	// deliver the promised byte count and still fail terminal validation; sending
	// that last byte first would let Content-Length clients accept corruption.
	if err := botCopyDownload(w, stream, size); err != nil {
		panic(http.ErrAbortHandler)
	}
}

type botOnceFileCloser struct {
	io.ReadCloser
	once sync.Once
	err  error
}

func (c *botOnceFileCloser) Close() error {
	c.once.Do(func() { c.err = c.ReadCloser.Close() })
	return c.err
}

// Own cancellation independently of a backend's read implementation. Close is
// exactly once, and the cancellation callback is joined before returning.
func botOwnFileStream(ctx context.Context, stream *store.BotFileStream) func() {
	stream.ReadCloser = &botOnceFileCloser{ReadCloser: stream.ReadCloser}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = stream.Close(); close(done) })
	return func() {
		if !stop() {
			<-done
		}
		_ = stream.Close()
	}
}

func botCopyDownload(w io.Writer, stream *store.BotFileStream, size int64) error {
	if size == 0 {
		var extra [1]byte
		n, err := stream.Read(extra[:])
		if n != 0 || err != io.EOF {
			return store.ErrBotConflict
		}
		return stream.Close()
	}
	if n, err := io.CopyN(w, stream, size-1); err != nil || n != size-1 {
		return store.ErrBotConflict
	}
	var last [1]byte
	if _, err := io.ReadFull(stream, last[:]); err != nil {
		return err
	}
	var extra [1]byte
	if n, err := stream.Read(extra[:]); n != 0 || err != io.EOF {
		return store.ErrBotConflict
	}
	if err := stream.Close(); err != nil {
		return err
	}
	_, err := w.Write(last[:])
	return err
}
