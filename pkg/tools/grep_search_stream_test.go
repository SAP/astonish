package tools

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/tools/ripgrep"
)

func grepStreamFixture(t *testing.T, content string) string {
	t.Helper()
	if _, err := ripgrep.ResolvePath(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "input.txt")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestGrepSearchStreamPipeCap(t *testing.T) {
	// The JSON output is much larger than an OS pipe, despite a tiny read cap.
	path := grepStreamFixture(t, strings.Repeat("needle\n", 100000))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	matches, reason, err := tryRipgrep(ctx, GrepSearchArgs{Pattern: "needle"}, path, 100000, 128)
	if err != nil {
		t.Fatalf("byte cap must succeed without waiting for deadline: %v (elapsed %s)", err, time.Since(start))
	}
	if reason == "" || len(matches) != 0 {
		t.Fatalf("expected explicit cap before first complete match: matches=%v reason=%q", matches, reason)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("capped search did not stop promptly: %s", time.Since(start))
	}
}

func TestGrepSearchStreamByteCapMetadata(t *testing.T) {
	path := grepStreamFixture(t, "needle\n")
	result, err := GrepSearch(nil, GrepSearchArgs{Pattern: "needle", SearchPath: path, HeadLimit: 128})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Capped || result.TruncatedReason == "" || result.Total != 0 {
		t.Fatalf("incomplete record must be explicitly capped, not silently lost: %+v", result)
	}
}

func TestGrepSearchStreamLargeJSONRecord(t *testing.T) {
	line := "needle" + strings.Repeat("x", 256*1024)
	path := grepStreamFixture(t, line+"\n")
	result, err := GrepSearch(nil, GrepSearchArgs{Pattern: "needle", SearchPath: path})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || result.Matches[0].Content != line || result.Capped {
		t.Fatalf("record beyond Scanner's 64KiB limit must survive: total=%d capped=%v", result.Total, result.Capped)
	}
}

func TestGrepSearchStreamOversizedJSONRecordCap(t *testing.T) {
	path := grepStreamFixture(t, "needle"+strings.Repeat("x", 1024*1024)+"\n")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result, err := GrepSearch(&toolContextStub{Context: ctx}, GrepSearchArgs{Pattern: "needle", SearchPath: path, HeadLimit: 32 * 1024})
	if err != nil || !result.Capped || result.TruncatedReason == "" || result.Total != 0 {
		t.Fatalf("oversized record must stop at the budget with an explicit cap: total=%d capped=%v reason=%q err=%v", result.Total, result.Capped, result.TruncatedReason, err)
	}
}

func TestGrepSearchStreamCallerCancellation(t *testing.T) {
	path := grepStreamFixture(t, "needle\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := GrepSearch(&toolContextStub{Context: ctx}, GrepSearchArgs{Pattern: "needle", SearchPath: path})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("caller cancellation must be preserved: %v", err)
	}
}

func TestGrepSearchStreamContextDoesNotConsumeMatchLimit(t *testing.T) {
	path := grepStreamFixture(t, "before\nneedle\nafter\n")
	result, err := GrepSearch(nil, GrepSearchArgs{Pattern: "needle", SearchPath: path, Context: 1, MaxResults: 2})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 3 || result.Capped {
		t.Fatalf("context is free and must not falsely trigger the match cap: %+v", result)
	}
}

func TestGrepSearchStreamExitDistinctions(t *testing.T) {
	path := grepStreamFixture(t, "needle\n")
	for _, tc := range []struct {
		name    string
		args    GrepSearchArgs
		wantErr string
	}{
		{"no matches", GrepSearchArgs{Pattern: "absent"}, ""},
		{"invalid regex", GrepSearchArgs{Pattern: "[", Regex: true}, "invalid regex"},
		{"invalid type", GrepSearchArgs{Pattern: "needle", Type: "astonish_nonexistent_type"}, "exit 2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.args.SearchPath = path
			result, err := GrepSearch(nil, tc.args)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want %q, got %v", tc.wantErr, err)
				}
			} else if err != nil || result.Total != 0 || result.Capped {
				t.Fatalf("no-match must be uncapped success: %+v, %v", result, err)
			}
		})
	}
}

// errorReader verifies I/O failures are not mistaken for a capped/empty search.
type grepStreamErrorReader struct{}

func (grepStreamErrorReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestParseRipgrepStreamBoundaries(t *testing.T) {
	const record = `{"type":"match","data":{"path":{"text":"a"},"lines":{"text":"needle\n"},"line_number":1}}` + "\n"
	for _, tc := range []struct {
		name   string
		input  string
		limit  int
		want   int
		capped bool
		bad    bool
	}{
		{"exact boundary", record, len(record), 1, false, false},
		{"complete prefix", record + record, len(record) + 8, 1, true, false},
		{"oversized record", record, 8, 0, true, false},
		{"malformed", "{bad}\n", 1024, 0, false, true},
		{"truncated final record", record + `{"type":`, 1024, 0, false, true},
		{"no final newline", strings.TrimSuffix(record, "\n"), len(record), 1, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			matches, reason, err := parseRipgrepOutput(strings.NewReader(tc.input), 50, tc.limit)
			if (err != nil) != tc.bad || len(matches) != tc.want || (reason != "") != tc.capped {
				t.Fatalf("matches=%v reason=%q err=%v", matches, reason, err)
			}
		})
	}
	if _, _, err := parseRipgrepOutput(grepStreamErrorReader{}, 50, 1024); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("read error lost: %v", err)
	}
}

func TestGrepSearchStreamRunningCancellation(t *testing.T) {
	if _, err := exec.LookPath("mkfifo"); err != nil {
		t.Skip("requires Unix FIFO support")
	}
	if _, err := ripgrep.ResolvePath(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"caller cancel", "caller deadline", "tool timeout"} {
		t.Run(name, func(t *testing.T) {
			// An explicit FIFO makes real rg wait for input, not a simulated
			// process or a timing-dependent traversal of the local machine.
			path := filepath.Join(t.TempDir(), "input.fifo")
			if out, err := exec.Command("mkfifo", path).CombinedOutput(); err != nil {
				t.Fatalf("mkfifo: %s: %v", out, err)
			}
			var ctx context.Context
			var cancel context.CancelFunc
			want := error(context.Canceled)
			switch name {
			case "caller cancel":
				ctx, cancel = context.WithCancel(context.Background())
				timer := time.AfterFunc(100*time.Millisecond, cancel)
				defer timer.Stop()
			case "caller deadline":
				ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
				want = context.DeadlineExceeded
			case "tool timeout":
				ctx, cancel = context.WithTimeoutCause(context.Background(), 100*time.Millisecond, errGrepTimeout)
				want = errGrepTimeout
			}
			defer cancel()
			start := time.Now()
			var err error
			if name == "tool timeout" {
				_, _, err = tryRipgrep(ctx, GrepSearchArgs{Pattern: "needle"}, path, 50, 1024)
			} else {
				_, err = GrepSearch(&toolContextStub{Context: ctx}, GrepSearchArgs{Pattern: "needle", SearchPath: path})
			}
			if !errors.Is(err, want) || time.Since(start) > time.Second {
				t.Fatalf("want prompt %v; got %v in %s", want, err, time.Since(start))
			}
		})
	}
}

func TestGrepSearchStreamResultLimitStopsProducer(t *testing.T) {
	path := grepStreamFixture(t, "before\n"+strings.Repeat("needle\n", 100000))
	result, err := GrepSearch(nil, GrepSearchArgs{Pattern: "needle", SearchPath: path, Context: 1, MaxResults: 1})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 2 || !result.Capped || result.TruncatedReason != "result limit reached (1)" || result.Matches[0].Kind != "context" || result.Matches[1].Kind != "match" {
		t.Fatalf("match cap must preserve the existing context prefix: %+v", result)
	}
}
