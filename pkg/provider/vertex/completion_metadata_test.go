package vertex

import (
	"fmt"
	"google.golang.org/genai"
	"strings"
	"testing"
)

func TestTerminationMetadata(t *testing.T) {
	for _, reason := range []genai.FinishReason{genai.FinishReasonStop, genai.FinishReasonMaxTokens, genai.FinishReasonSafety, genai.FinishReasonOther, "", "NEW_REASON"} {
		for _, part := range []string{`{"text":"answer"}`, `{"functionCall":{"name":"read_file","args":{}}}`} {
			t.Run(fmt.Sprintf("%s/%s", reason, part), func(t *testing.T) {
				body := fmt.Sprintf(`{"candidates":[{"content":{"role":"model","parts":[%s]},"finishReason":%q}]}`, part, reason)
				r, err := ParseResponse([]byte(body))
				if err != nil {
					t.Fatal(err)
				}
				complete := reason == genai.FinishReasonStop
				if r.FinishReason != reason || r.TurnComplete != complete {
					t.Fatalf("response=%+v", r)
				}
				final, failed := false, false
				for r, err := range ParseStream(strings.NewReader("data: " + body + "\n\n")) {
					if err != nil {
						failed = true
						continue
					}
					if r.Partial {
						if r.TurnComplete {
							t.Fatal("partial marked complete")
						}
						continue
					}
					final = true
					if r.FinishReason != reason || r.TurnComplete != complete {
						t.Fatalf("stream response=%+v", r)
					}
				}
				if final != complete || failed == complete {
					t.Fatalf("final=%v failed=%v complete=%v", final, failed, complete)
				}
			})
		}
	}
}
