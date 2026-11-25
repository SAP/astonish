package daemon

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/email"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/sandbox/sessioncreds"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
)

const configuredServiceSecret = "test-owned-mail-secret"

type configuredServiceBackend struct {
	templateWiringBackend
	mu    sync.Mutex
	vault map[string][]byte
}

func (b *configuredServiceBackend) PushFile(ctx context.Context, sid, path string, r io.Reader, mode os.FileMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if path != sessioncreds.VaultPath || mode != sessioncreds.VaultFileMode || sid == "" {
		return store.ErrBotForbidden
	}
	raw, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.vault == nil {
		b.vault = map[string][]byte{}
	}
	b.vault[sid] = raw
	return nil
}

type configuredServicePool struct {
	configuredProtocolPool
	vaultBackend *configuredServiceBackend
}

func (p *configuredServicePool) GetOrCreate(string) sandbox.ToolNodeClient { return p }
func (p *configuredServicePool) GetOrCreateWithTemplate(string, string) sandbox.ToolNodeClient {
	return p
}
func (p *configuredServicePool) Call(s, n string, a map[string]any) (json.RawMessage, error) {
	return p.CallContext(context.Background(), s, n, a)
}
func (p *configuredServicePool) CallContext(ctx context.Context, s, n string, a map[string]any) (json.RawMessage, error) {
	if s == "" {
		return nil, store.ErrBotForbidden
	}
	if n == "filter_json" {
		if a["json_data"] != `{ "evidence": "configured-drill-result" }` && a["json_data"] != `{"evidence":"configured-drill-result"}` {
			return nil, store.ErrBotForbidden
		}
		p.mu.Lock()
		p.sessions = append(p.sessions, s)
		p.operations = append(p.operations, n)
		p.mu.Unlock()
		out, err := tools.ExecuteTool(ctx, n, a, s)
		if err != nil {
			return nil, err
		}
		return json.Marshal(out)
	}
	if n != email.NodeOperation {
		return nil, store.ErrBotForbidden
	}
	raw, err := json.Marshal(a)
	if err != nil {
		return nil, err
	}
	var req email.NodeRequest
	if err = json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	if req.Tool != "email_send" && req.Tool != tools.ScopedChatEmailPrefix+"email_send" {
		return nil, store.ErrBotForbidden
	}
	if req.Account.Credential != "mail" || req.Account.Address != "owner@example.com" || req.Arguments["body"] != "Reviewed configured email body" {
		return nil, store.ErrBotForbidden
	}
	p.vaultBackend.mu.Lock()
	data := append([]byte(nil), p.vaultBackend.vault[s]...)
	p.vaultBackend.mu.Unlock()
	cs, err := sessioncreds.Parse(data)
	if err != nil {
		return nil, err
	}
	if cred := cs.Get(ctx, "mail"); cred == nil || cred.Password != configuredServiceSecret || strings.Contains(string(data), "wrong-scope-secret") {
		return nil, store.ErrBotForbidden
	}
	p.mu.Lock()
	p.sessions = append(p.sessions, s)
	p.operations = append(p.operations, n)
	p.mu.Unlock()
	out, err := tools.ExecuteTool(store.WithCredentialStore(ctx, cs), n, a, s)
	if err != nil {
		return nil, err
	}
	return json.Marshal(out)
}

// Fully joined loopback SMTP transport, with exact envelope and scoped credential
// assertions. Each production operation owns/closes its own pkg/email client.
func configuredServiceSMTP(t *testing.T, calls func()) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
				r := bufio.NewReader(conn)
				fmt.Fprint(conn, "220 localhost ESMTP\r\n")
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					line = strings.TrimSpace(line)
					switch {
					case strings.HasPrefix(line, "EHLO"):
						fmt.Fprint(conn, "250-localhost\r\n250 AUTH PLAIN\r\n")
					case strings.HasPrefix(line, "AUTH PLAIN "):
						raw, e := base64.StdEncoding.DecodeString(strings.TrimPrefix(line, "AUTH PLAIN "))
						if e != nil || string(raw) != "\x00owner\x00"+configuredServiceSecret {
							t.Error("SMTP wrong scoped identity")
							fmt.Fprint(conn, "535 wrong identity\r\n")
							return
						}
						fmt.Fprint(conn, "235 authenticated\r\n")
					case strings.HasPrefix(line, "MAIL FROM:"):
						if line != "MAIL FROM:<owner@example.com>" {
							t.Errorf("wrong sender: %s", line)
						}
						fmt.Fprint(conn, "250 OK\r\n")
					case strings.HasPrefix(line, "RCPT TO:"):
						if line != "RCPT TO:<recipient@example.com>" {
							t.Errorf("wrong recipient: %s", line)
						}
						fmt.Fprint(conn, "250 OK\r\n")
					case line == "DATA":
						fmt.Fprint(conn, "354 continue\r\n")
						var body strings.Builder
						for {
							s, e := r.ReadString('\n')
							if e != nil {
								return
							}
							if s == ".\r\n" {
								break
							}
							body.WriteString(s)
						}
						if !strings.Contains(body.String(), "Reviewed configured email body") || !strings.Contains(body.String(), "Reviewed configured subject") {
							t.Error("wrong exact mail content")
						}
						calls()
						fmt.Fprint(conn, "250 queued\r\n")
					case line == "QUIT":
						fmt.Fprint(conn, "221 bye\r\n")
						return
					default:
						fmt.Fprint(conn, "500 unsupported\r\n")
					}
				}
			}()
		}
	}()
	t.Cleanup(func() { _ = l.Close(); wg.Wait() })
	return l.Addr().String()
}
