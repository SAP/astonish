package email

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2/imapclient"
)

func TestReadMessageScopedPeek(t *testing.T) {
	for _, markRead := range []bool{false, true} {
		t.Run(fmt.Sprint(markRead), func(t *testing.T) {
			clientConn, serverConn := net.Pipe()
			clientConn.SetDeadline(time.Now().Add(5 * time.Second))
			serverConn.SetDeadline(time.Now().Add(5 * time.Second))
			defer serverConn.Close()
			fetch := make(chan string, 1)
			go func() {
				defer serverConn.Close()
				fmt.Fprint(serverConn, "* OK [CAPABILITY IMAP4rev1] fixture\r\n")
				reader := bufio.NewReader(serverConn)
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						return
					}
					fields := strings.Fields(line)
					if len(fields) < 2 {
						return
					}
					tag := fields[0]
					switch {
					case strings.Contains(line, "NOOP"):
						fmt.Fprintf(serverConn, "%s OK done\r\n", tag)
					case strings.Contains(line, "SELECT"):
						fmt.Fprintf(serverConn, "* 1 EXISTS\r\n* FLAGS (\\Seen)\r\n* OK [UIDVALIDITY 1] valid\r\n%s OK [READ-WRITE] selected\r\n", tag)
					case strings.Contains(line, "FETCH"):
						fetch <- line
						body := "From: sender@example.com\r\nTo: owner@example.com\r\nSubject: Controlled\r\n\r\nscoped content\r\n"
						fmt.Fprintf(serverConn, "* 1 FETCH (UID 42 FLAGS () BODY[] {%d}\r\n%s)\r\n%s OK done\r\n", len(body), body, tag)
					default:
						fmt.Fprintf(serverConn, "%s BAD unsupported\r\n", tag)
					}
				}
			}()
			conn := imapclient.New(clientConn, nil)
			defer conn.Close()
			cfg := DefaultConfig()
			cfg.MarkRead = markRead
			client := &IMAPSMTPClient{cfg: cfg, imapConn: conn, connected: true}
			result, err := client.ReadMessage(context.Background(), "42")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(result.Body, "scoped content") {
				t.Fatalf("missing content: %+v", result)
			}
			line := <-fetch
			if strings.Contains(line, "BODY.PEEK[]") == markRead {
				t.Fatalf("incorrect read side effect markRead=%v: %s", markRead, line)
			}
		})
	}
}
