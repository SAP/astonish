package bot

import (
	"context"
	"testing"
)

func TestModelCallPurposeContext(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	ctx := WithModelCallPurpose(parent, ModelCallPurposeAssessment)
	if ModelCallPurposeFromContext(ctx) != ModelCallPurposeAssessment || ModelCallPurposeFromContext(parent) != ModelCallPurposeProducer {
		t.Fatal("purpose did not remain invocation-local")
	}
	cancel()
	if ctx.Err() != context.Canceled {
		t.Fatal("purpose lost cancellation")
	}
}
