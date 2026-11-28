package store

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestMemoryAttributionCompatibility(t *testing.T) {
	legacy := map[string]any{"unrelated": "retained"}
	if a, err := DecodeMemoryAttribution(legacy); err != nil || a != nil {
		t.Fatalf("legacy: %v %v", a, err)
	}
	a := &MemoryAttribution{BotID: uuid.New(), GoalID: uuid.New(), RunID: uuid.New(), Source: "receipt:1", ObservedAt: time.Now().UTC(), Status: MemoryKnowledgeInferred}
	metadata, err := EncodeMemoryAttribution(legacy, a)
	if err != nil {
		t.Fatal(err)
	}
	if len(legacy) != 1 {
		t.Fatal("mutated caller metadata")
	}
	raw, _ := json.Marshal(metadata)
	var restored map[string]any
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	got, err := DecodeMemoryAttribution(restored)
	if err != nil || !reflect.DeepEqual(got, a) || restored["unrelated"] != "retained" {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	superseded, err := SupersedeMemoryAttribution(restored, "correction:1")
	if err != nil {
		t.Fatal(err)
	}
	got, err = DecodeMemoryAttribution(superseded)
	if err != nil || got.Status != MemoryKnowledgeInferred || got.SupersededBy != "correction:1" || got.Source != a.Source {
		t.Fatalf("supersede: %+v %v", got, err)
	}
	original, _ := DecodeMemoryAttribution(restored)
	if original.SupersededBy != "" {
		t.Fatal("mutated original provenance")
	}
	if _, err := EncodeMemoryAttribution(metadata, nil); !errors.Is(err, ErrBotInvalid) {
		t.Fatalf("raw envelope accepted: %v", err)
	}
}

func TestMemoryAttributionValidation(t *testing.T) {
	a := MemoryAttribution{BotID: uuid.New(), Source: "source", ObservedAt: time.Now().UTC(), Status: MemoryKnowledgeVerified}
	if err := a.Validate(); !errors.Is(err, ErrBotInvalid) {
		t.Fatalf("verified without evidence: %v", err)
	}
	a.Evidence = &BotEvidence{Kind: BotEvidenceInference, Source: "source", Summary: "guess", ActorID: uuid.New(), ObservedAt: a.ObservedAt}
	if err := a.Validate(); !errors.Is(err, ErrBotInvalid) {
		t.Fatalf("inference promoted: %v", err)
	}
	a.Evidence.Kind = BotEvidenceObservation
	if err := a.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []any{nil, 123, "{}", "not JSON"} {
		if _, err := DecodeMemoryAttribution(map[string]any{memoryAttributionMetadataKey: bad}); !errors.Is(err, ErrBotInvalid) {
			t.Fatalf("corrupt provenance %v: %v", bad, err)
		}
	}
}
