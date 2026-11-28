package main

// Candidate-only CLI observer. The proxy forwards real responses, never fixtures;
// it refuses every product mutation and records neither credentials nor headers.
import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
	"github.com/google/uuid"
)

type agentTerminalExchange struct {
	Path   string `json:"path"`
	Status int    `json:"status"`
	Body   string `json:"body"`
}
type agentTerminalStream struct {
	io.ReadCloser
	pending string
	record  func(string)
}

func (s *agentTerminalStream) Read(p []byte) (int, error) {
	n, err := s.ReadCloser.Read(p)
	s.pending += string(p[:n])
	for {
		i := strings.Index(s.pending, "\n\n")
		if i < 0 {
			break
		}
		frame := s.pending[:i]
		s.pending = s.pending[i+2:]
		if strings.Contains(frame, "data:") {
			s.record(frame)
		}
	}
	return n, err
}

func agentTerminalCompare(passes []agentTerminalPass) error {
	if len(passes) != 2 {
		return fmt.Errorf("two independent CLI connections required")
	}
	canonical := func(pass agentTerminalPass) (map[string]string, error) {
		rows := map[string]string{}
		events := []string{}
		seen := map[string]bool{}
		for _, row := range pass.Exchanges {
			if row.Status != 200 {
				return nil, fmt.Errorf("CLI read failed")
			}
			if strings.Contains(row.Path, "/events") {
				if strings.Contains(row.Body, "event: transcript") && !seen[row.Body] {
					events = append(events, row.Body)
					seen[row.Body] = true
				}
				continue
			}
			var value any
			if json.Unmarshal([]byte(row.Body), &value) != nil {
				return nil, fmt.Errorf("CLI response is not JSON")
			}
			raw, _ := json.Marshal(value)
			rows[row.Path] = string(raw)
		}
		if len(events) == 0 {
			return nil, fmt.Errorf("CLI ordered transcript missing")
		}
		eventJSON, _ := json.Marshal(events)
		rows["ordered_transcript"] = string(eventJSON)
		snapshot, skills := false, false
		for path := range rows {
			snapshot = snapshot || strings.Contains(path, "/snapshot")
			skills = skills || strings.Contains(path, "/skills")
		}
		if !snapshot || !skills {
			return nil, fmt.Errorf("CLI did not read work and skills")
		}
		return rows, nil
	}
	first, err := canonical(passes[0])
	if err != nil {
		return err
	}
	second, err := canonical(passes[1])
	if err != nil {
		return err
	}
	a, _ := json.Marshal(first)
	b, _ := json.Marshal(second)
	if !bytes.Equal(a, b) {
		return fmt.Errorf("CLI reconnect changed persisted projection or skills")
	}
	return nil
}

type agentTerminalFrame struct {
	Encoding string `json:"encoding"`
	Data     string `json:"data"`
	Rows     uint16 `json:"rows"`
	Cols     uint16 `json:"cols"`
}

type agentTerminalPass struct {
	Frames           map[string]agentTerminalFrame `json:"frames"`
	Exchanges        []agentTerminalExchange       `json:"exchanges"`
	InitialExchanges []agentTerminalExchange       `json:"initial_exchanges,omitempty"`
}

func agentTerminalAllowed(method, path string) bool {
	if method == http.MethodGet {
		return strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/oauth/")
	}
	// Native password login exchanges the candidate token for CLI OAuth tokens.
	return method == http.MethodPost && (path == "/api/auth/login" || path == "/oauth/token" || path == "/api/auth/oauth/exchange")
}

func agentTerminalEnvironment(home string) []string {
	// Do not inherit normal user config, bearer tokens, provider keys or daemon env.
	env := []string{"HOME=" + home, "XDG_CONFIG_HOME=" + home, "XDG_DATA_HOME=" + home, "TERM=xterm-256color", "NO_COLOR=1", "SUDO_USER="}
	for _, key := range []string{"PATH", "TMPDIR", "LANG"} {
		if value := os.Getenv(key); value != "" {
			env = append(env, key+"="+value)
		}
	}
	return env
}

type agentTerminalPTY struct {
	cmd    *exec.Cmd
	file   *os.File
	mu     sync.Mutex
	output bytes.Buffer
	cols   uint16
	done   chan struct{}
}

func agentStartTerminal(binary string, env []string, args ...string) (*agentTerminalPTY, error) {
	cmd := exec.Command(binary, args...)
	cmd.Env = env
	file, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 120, Cols: 220})
	if err != nil {
		return nil, fmt.Errorf("candidate CLI PTY launch failed")
	}
	p := &agentTerminalPTY{cmd: cmd, file: file, cols: 220, done: make(chan struct{})}
	go func() {
		defer close(p.done)
		pending := ""
		buf := make([]byte, 8192)
		for {
			n, err := file.Read(buf)
			if n > 0 {
				p.mu.Lock()
				p.output.Write(buf[:n])
				p.mu.Unlock()
				pending += string(buf[:n])
				pending = agentTerminalRespond(file, pending)
			}
			if err != nil {
				return
			}
		}
	}()
	return p, nil
}

// Same startup query protocol as cmd/astonish/bots_pty_test.go, including split reads.
func agentTerminalRespond(w io.Writer, pending string) string {
	queries := [][2]string{{"\x1b]10;?\x1b\\", "\x1b]10;rgb:ffff/ffff/ffff\x1b\\"}, {"\x1b]11;?\x1b\\", "\x1b]11;rgb:0000/0000/0000\x1b\\"}, {"\x1b]10;?\a", "\x1b]10;rgb:ffff/ffff/ffff\a"}, {"\x1b]11;?\a", "\x1b]11;rgb:0000/0000/0000\a"}, {"\x1b[6n", "\x1b[1;1R"}, {"\x1b[c", "\x1b[?1;2c"}, {"\x1b[0c", "\x1b[?1;2c"}}
	for len(pending) > 0 {
		partial, matched := false, false
		for _, q := range queries {
			if strings.HasPrefix(pending, q[0]) {
				_, _ = io.WriteString(w, q[1])
				pending = pending[len(q[0]):]
				matched = true
				break
			}
			partial = partial || strings.HasPrefix(q[0], pending)
		}
		if matched {
			continue
		}
		if partial {
			break
		}
		pending = pending[1:]
	}
	return pending
}
func (p *agentTerminalPTY) text() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return ansi.Strip(p.output.String())
}
func (p *agentTerminalPTY) send(s string) error    { _, err := io.WriteString(p.file, s); return err }
func (p *agentTerminalPTY) wait(want string) error { return p.waitSince(want, 0) }

func (p *agentTerminalPTY) waitSince(want string, offset int) error {
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		text := ansi.Strip(string(p.output.Bytes()[offset:]))
		p.mu.Unlock()
		if strings.Contains(text, want) {
			return nil
		}
		select {
		case <-p.done:
			return fmt.Errorf("candidate CLI exited before required state")
		case <-time.After(30 * time.Millisecond):
		}
	}
	return fmt.Errorf("candidate CLI did not render required state")
}
func (p *agentTerminalPTY) close() {
	_ = p.cmd.Process.Kill()
	_ = p.file.Close()
	_ = p.cmd.Wait()
	<-p.done
}
func (p *agentTerminalPTY) rawFrame(cols uint16) agentTerminalFrame {
	p.mu.Lock()
	defer p.mu.Unlock()
	return agentTerminalFrame{Encoding: "base64", Data: base64.StdEncoding.EncodeToString(p.output.Bytes()), Rows: 120, Cols: cols}
}

func (p *agentTerminalPTY) frame(command, want string) (agentTerminalFrame, error) {
	offset := 0
	if command != "" {
		p.mu.Lock()
		offset = p.output.Len()
		p.mu.Unlock()
		if err := p.send(command); err != nil {
			return p.rawFrame(p.cols), err
		}
	}
	if err := p.waitSince(want, offset); err != nil {
		return p.rawFrame(p.cols), err
	}
	// Keep a fixed terminal size and the complete stream from process startup.
	// A resize-only repaint may use relative cursor movements and is not an
	// independently decodable screen. Never invent a cursor reset or remove
	// queries/styles merely to satisfy the conservative offline decoder.
	time.Sleep(250 * time.Millisecond)
	return p.rawFrame(p.cols), nil
}

func agentBindCLIHash(binary string, state map[string]any) (string, error) {
	file, err := os.Open(binary)
	if err != nil {
		return "", err
	}
	defer file.Close()
	h := sha256.New()
	if _, err = io.Copy(h, file); err != nil {
		return "", err
	}
	digest := fmt.Sprintf("%x", h.Sum(nil))
	state["agent_cli_sha256"] = digest
	return digest, nil
}

// Return a short literal visible wait target from the canonical publication.
// This is not screen evidence; only rawFrame supplies that evidence.
func agentTerminalAnswer(rows []agentTerminalExchange) string {
	var text string
	for _, row := range rows {
		if row.Status != 200 || !strings.Contains(row.Path, "/events") {
			continue
		}
		for _, line := range strings.Split(row.Body, "\n") {
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			var event struct {
				Invocation string `json:"invocationId"`
				Content    struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"content"`
			}
			if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &event) != nil || !strings.HasPrefix(event.Invocation, "bot-report:") {
				continue
			}
			text = "" // the most recent publication, never an earlier run's answer
			for _, part := range event.Content.Parts {
				text += part.Text
			}
		}
	}
	// A single word survives terminal wrapping/Markdown formatting. Full answer
	// visibility is independently checked by the strict screen decoder.
	for _, word := range strings.Fields(text) {
		if len(word) >= 5 && strings.Trim(word, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789") == "" {
			return word
		}
	}
	return ""
}

func agentTerminalLiveAnswer(rows []agentTerminalExchange) string {
	for i, row := range rows {
		if row.Status == http.StatusOK && strings.Contains(row.Path, "/events") && strings.Contains(row.Body, "event: replay_complete") {
			return agentTerminalAnswer(rows[i+1:])
		}
	}
	return ""
}

func agentObserveTerminal(root, base, bot string, state map[string]any) (map[string]string, error) {
	return agentObserveTerminalDuring(root, base, bot, state, nil)
}

// startWork must drive a real owned candidate journey (e.g. through Studio).
// It runs only after the first CLI has finished its initial replay, while that
// CLI remains connected. The observer itself never gets mutation permission.
// A nil callback retains diagnostic replay captures but cannot pass live parity.
func agentObserveTerminalDuring(root, base, bot string, state map[string]any, startWork func() error) (map[string]string, error) {
	target, err := url.Parse(base)
	if err != nil || target.Scheme != "http" || target.Hostname() != "127.0.0.1" || target.Port() == "" || target.Port() == "9393" || target.User != nil || target.Path != "" || target.RawQuery != "" || target.Fragment != "" || !strings.HasPrefix(filepath.Base(root), "astonish-chat-first-") {
		return nil, fmt.Errorf("terminal observer requires owned random-port candidate")
	}
	if _, err = uuid.Parse(bot); err != nil {
		return nil, fmt.Errorf("invalid owned Bot identity")
	}
	raw, err := os.ReadFile(filepath.Join(root, "credentials.json"))
	if err != nil {
		return nil, fmt.Errorf("candidate credentials unavailable")
	}
	var creds map[string]string
	if json.Unmarshal(raw, &creds) != nil || creds["email"] == "" || creds["password"] == "" {
		return nil, fmt.Errorf("candidate credentials invalid")
	}
	home, err := os.MkdirTemp(root, "agent-cli-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(home)
	binary := filepath.Join(home, "astonish")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, ".")
	if err = build.Run(); err != nil {
		return nil, fmt.Errorf("native candidate CLI build failed")
	}
	cliHash, err := agentBindCLIHash(binary, state)
	if err != nil {
		return nil, err
	}
	var mu sync.Mutex
	exchanges := []agentTerminalExchange{}
	blocked := 0
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ModifyResponse = func(r *http.Response) error {
		if r.Request.Method == http.MethodGet && strings.HasPrefix(r.Request.URL.Path, "/api/studio/bots/"+bot+"/") && strings.HasSuffix(r.Request.URL.Path, "/events") {
			path, status := r.Request.URL.RequestURI(), r.StatusCode
			r.Body = &agentTerminalStream{ReadCloser: r.Body, record: func(frame string) {
				mu.Lock()
				exchanges = append(exchanges, agentTerminalExchange{path, status, frame})
				mu.Unlock()
			}}
		}
		if r.Request.Method == http.MethodGet && (r.Request.URL.Path == "/api/studio/bots/"+bot+"/snapshot" || r.Request.URL.Path == "/api/studio/bots/"+bot+"/skills") {
			body, e := io.ReadAll(io.LimitReader(r.Body, 8<<20))
			_ = r.Body.Close()
			if e != nil {
				return e
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			mu.Lock()
			exchanges = append(exchanges, agentTerminalExchange{r.Request.URL.RequestURI(), r.StatusCode, string(body)})
			mu.Unlock()
		}
		return nil
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !agentTerminalAllowed(r.Method, r.URL.Path) {
			mu.Lock()
			blocked++
			mu.Unlock()
			http.Error(w, "read-only observer", 403)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	defer server.Close()
	env := agentTerminalEnvironment(home)
	login, err := agentStartTerminal(binary, env, "login", server.URL)
	if err != nil {
		return nil, err
	}
	// Never persist or return login output, even when authentication fails.
	err = func() error {
		defer login.close()
		if e := login.wait("Email:"); e != nil {
			return e
		}
		if e := login.send(creds["email"] + "\n"); e != nil {
			return e
		}
		if e := login.wait("Password:"); e != nil {
			return e
		}
		time.Sleep(150 * time.Millisecond)
		if e := login.send(creds["password"] + "\n"); e != nil {
			return e
		}
		return login.wait("Logged in")
	}()
	if err != nil {
		return nil, fmt.Errorf("native candidate CLI authentication failed")
	}
	passes := []agentTerminalPass{}
	var captureErr error
	for i := 0; i < 2; i++ {
		mu.Lock()
		exchanges = nil
		mu.Unlock()
		pass, e := func() (pass agentTerminalPass, err error) {
			pass.Frames = map[string]agentTerminalFrame{}
			defer func() {
				mu.Lock()
				pass.Exchanges = append([]agentTerminalExchange{}, exchanges...)
				mu.Unlock()
			}()
			p, e := agentStartTerminal(binary, env, "chat", "--bot", bot)
			if e != nil {
				return pass, e
			}
			defer p.close()
			defer func() {
				if err != nil {
					pass.Frames["failure"] = p.rawFrame(p.cols)
				}
			}()
			frames := pass.Frames
			if e = p.wait("/bots"); e != nil {
				return pass, e
			}
			if i == 0 && startWork != nil {
				deadline := time.Now().Add(25 * time.Second)
				replayed := false
				for time.Now().Before(deadline) {
					mu.Lock()
					for _, row := range exchanges {
						replayed = replayed || strings.Contains(row.Body, "event: replay_complete")
					}
					mu.Unlock()
					if replayed {
						break
					}
					time.Sleep(30 * time.Millisecond)
				}
				if !replayed {
					return pass, fmt.Errorf("CLI live observer did not finish initial replay")
				}
				// Retain the pre-work snapshot as diagnostic context, separate from
				// the final settled snapshot read by /bots status. Never relabel it
				// as the final projection or discard the initial replay events.
				mu.Lock()
				var retained []agentTerminalExchange
				for _, row := range exchanges {
					if strings.Contains(row.Path, "/snapshot") {
						pass.InitialExchanges = append(pass.InitialExchanges, row)
					} else {
						retained = append(retained, row)
					}
				}
				exchanges = retained
				mu.Unlock()
				if e := startWork(); e != nil {
					return pass, e
				}
			}
			// Wait for the report loaded through the CLI's own replay, not merely
			// its startup help. The API text is a wait target, never frame content.
			var answer string
			deadline := time.Now().Add(25 * time.Second)
			for time.Now().Before(deadline) {
				mu.Lock()
				answer = agentTerminalAnswer(exchanges)
				if i == 0 && startWork != nil {
					answer = agentTerminalLiveAnswer(exchanges)
				}
				mu.Unlock()
				if answer != "" {
					break
				}
				time.Sleep(30 * time.Millisecond)
			}
			if answer == "" {
				return pass, fmt.Errorf("CLI replay lacks canonical final publication")
			}
			frames["home"], e = p.frame("", answer)
			if e != nil {
				return pass, fmt.Errorf("CLI Home final publication not rendered: %w", e)
			}
			frames["status"], e = p.frame("/bots status\r", "Next run:")
			if e != nil {
				return pass, e
			}
			_ = p.send("\x1b")
			time.Sleep(250 * time.Millisecond)
			frames["skills"], e = p.frame("/bots skills\r", "Bot skills & history")
			if e != nil {
				return pass, e
			}
			_ = p.send("\x1b")
			time.Sleep(250 * time.Millisecond)
			frames["bots"], e = p.frame("/bots\r", "Bots")
			if e != nil {
				return pass, e
			}
			return pass, nil
		}()
		passes = append(passes, pass)
		if e != nil {
			captureErr = fmt.Errorf("CLI pass %d: %w", i+1, e)
			break
		}
	}
	mu.Lock()
	writes := blocked
	mu.Unlock()
	if writes != 0 {
		captureErr = fmt.Errorf("CLI attempted product mutation")
	}
	return agentWriteTerminalManifest(root, base, bot, cliHash, state, passes, writes, captureErr)
}

func agentWriteTerminalManifest(root, base, bot, cliHash string, state map[string]any, passes []agentTerminalPass, writes int, captureErr error) (map[string]string, error) {
	comparisonError := ""
	if captureErr != nil {
		comparisonError = captureErr.Error()
	} else if err := agentTerminalCompare(passes); err != nil {
		comparisonError = err.Error()
		captureErr = err
	}
	manifest := map[string]any{"comparison_error": comparisonError, "contract": "bot-agent-terminal-v1", "nonce": state["agent_nonce"], "candidate": state["binary_sha256"], "browser_candidate": state["browser_sha256"], "cli_sha256": cliHash, "bot_id": bot, "url": base, "passes": passes, "blocked_mutations": writes}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	name := "agent-terminal.json"
	if err = os.WriteFile(filepath.Join(root, name), raw, 0600); err != nil {
		return nil, err
	}
	return map[string]string{"Source": name, "SourceHash": fmt.Sprintf("%x", sha256.Sum256(raw))}, captureErr
}
