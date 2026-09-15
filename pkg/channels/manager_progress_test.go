package channels

import (
	"reflect"
	"testing"
)

type managerProgressSink struct {
	texts []string
}

func (s *managerProgressSink) Progress(text string) {
	s.texts = append(s.texts, text)
}

func TestHandleInbound_ProgressSinkReceivesIntermediateTurns(t *testing.T) {
	sink := &managerProgressSink{}

	emitProgress(sink, "activity 1")
	emitProgress(sink, "final answer")

	want := []string{"activity 1", "final answer"}
	if !reflect.DeepEqual(sink.texts, want) {
		t.Fatalf("progress texts = %v, want %v", sink.texts, want)
	}
}

func TestEmitProgress_NilSinkIsNoOp(t *testing.T) {
	emitProgress(nil, "ignored")
}
