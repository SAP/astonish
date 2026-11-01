package streaming

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestProgressContextIsolationAndSynchronousDelivery(t *testing.T) {
	var got []Progress
	parent := context.Background()
	ctx := WithProgress(parent, func(p Progress) { got = append(got, p) })
	Report(parent, Progress{Phase: PhaseThinking})
	Report(ctx, Progress{Phase: PhaseThinking})
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	Report(child, Progress{Phase: PhaseToolArguments})
	Report(WithProgress(ctx, nil), Progress{Phase: PhaseThinking})
	want := []Progress{{Phase: PhaseThinking}, {Phase: PhaseToolArguments}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	wantFields := map[string]reflect.Type{"Kind": reflect.TypeOf(Kind("")), "Phase": reflect.TypeOf(Phase("")), "Attempt": reflect.TypeOf(0), "MaxAttempts": reflect.TypeOf(0), "Elapsed": reflect.TypeOf(time.Duration(0)), "LastActivity": reflect.TypeOf(time.Duration(0))}
	typ := reflect.TypeOf(Progress{})
	if typ.NumField() != len(wantFields) {
		t.Fatal("unexpected payload field")
	}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if wantFields[f.Name] != f.Type {
			t.Fatalf("unsafe progress field: %s", f.Name)
		}
	}
}

func TestProgressTypedDelivery(t *testing.T) {
	for _, p := range []Progress{
		{Phase: PhaseThinking},
		{Kind: KindPhase, Phase: PhaseToolArguments},
		{Kind: KindHeartbeat, Phase: PhaseWaiting, Attempt: 1, MaxAttempts: 3, Elapsed: time.Second, LastActivity: time.Millisecond},
		{Kind: KindRecovery, Phase: PhaseRecovering, Attempt: 2, MaxAttempts: 3},
	} {
		var got Progress
		Report(WithProgress(context.Background(), func(value Progress) { got = value }), p)
		if got != p {
			t.Fatalf("got %+v want %+v", got, p)
		}
	}
}
