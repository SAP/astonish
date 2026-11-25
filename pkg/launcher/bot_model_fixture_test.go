package launcher

import (
	"context"
	"errors"
	"iter"
	"reflect"
	"testing"

	"google.golang.org/adk/model"
)

// botFactoryModelFixture exposes Bot-specific state for fixture setup without
// making the production model's outer wrapper part of the test contract. Model
// calls still go through the exact installed chain, including live authority.
type botFactoryModelFixture struct {
	*botFactoryLLM
	installed model.LLM
}

func (f botFactoryModelFixture) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return f.installed.GenerateContent(ctx, req, stream)
}

type botFixtureGuardProbe struct {
	model.LLM
	calls int
	err   error
}

func (g *botFixtureGuardProbe) GenerateContent(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		g.calls++
		yield(nil, g.err)
	}
}

func TestBotFactoryModelFixturePreservesInstalledGuard(t *testing.T) {
	binding := &botFactoryBinding{}
	inner := &botFactoryLLM{binding: binding}
	denied := errors.New("outer authority denied")
	guard := &botFixtureGuardProbe{LLM: inner, err: denied}
	fixture := botFactoryModelForTest(t, guard)
	if fixture.binding != binding {
		t.Fatal("fixture did not retain the actual Bot binding")
	}
	fixture.researchOnly = true
	if !inner.researchOnly {
		t.Fatal("fixture state setup changed a copy")
	}
	responses := 0
	for response, err := range fixture.GenerateContent(context.Background(), &model.LLMRequest{}, false) {
		responses++
		if response != nil || !errors.Is(err, denied) {
			t.Fatalf("outer guard was bypassed: response=%v err=%v", response, err)
		}
	}
	if guard.calls != 1 || responses != 1 {
		t.Fatalf("guard calls=%d responses=%d", guard.calls, responses)
	}
}

func botFactoryModelForTest(t *testing.T, installed model.LLM) botFactoryModelFixture {
	t.Helper()
	current := installed
	for depth := 0; depth < 16 && current != nil; depth++ {
		if botModel, ok := current.(*botFactoryLLM); ok {
			return botFactoryModelFixture{botFactoryLLM: botModel, installed: installed}
		}
		// Composition wrappers embed the public model.LLM interface. Inspect
		// only that exported delegate; do not add a production unwrap/bypass API.
		value := reflect.ValueOf(current)
		if value.Kind() != reflect.Pointer || value.IsNil() {
			break
		}
		value = value.Elem()
		if value.Kind() != reflect.Struct {
			break
		}
		delegate := value.FieldByName("LLM")
		if !delegate.IsValid() || !delegate.CanInterface() {
			break
		}
		current, _ = delegate.Interface().(model.LLM)
	}
	t.Fatalf("installed model %T has no Bot factory delegate", installed)
	return botFactoryModelFixture{}
}
