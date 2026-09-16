package channels

import (
	"context"
	"testing"
)

type recordingProgressSink struct {
	texts []string
}

func (s *recordingProgressSink) Progress(text string) {
	s.texts = append(s.texts, text)
}

func TestProgressSinkContext(t *testing.T) {
	if got := ProgressSinkFromContext(context.Background()); got != nil {
		t.Fatalf("ProgressSinkFromContext(background) = %T, want nil", got)
	}

	sink := &recordingProgressSink{}
	ctx := WithProgressSink(context.Background(), sink)
	got := ProgressSinkFromContext(ctx)
	if got != sink {
		t.Fatalf("ProgressSinkFromContext(ctx) = %T, want original sink", got)
	}

	got.Progress("working")
	if len(sink.texts) != 1 || sink.texts[0] != "working" {
		t.Fatalf("sink texts = %v, want [working]", sink.texts)
	}
}
