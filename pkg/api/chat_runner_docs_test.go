package api

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

type chatRunnerDocsStore struct{}

func (*chatRunnerDocsStore) CreateDeck(context.Context, *store.DeckManifest) error { return nil }
func (*chatRunnerDocsStore) GetDeck(context.Context, string) (*store.DeckManifest, error) {
	return nil, store.ErrDocsNotFound
}
func (*chatRunnerDocsStore) ListDecks(context.Context) ([]*store.DeckManifest, error) {
	return nil, nil
}
func (*chatRunnerDocsStore) ListDecksLite(context.Context) ([]*store.DeckManifest, error) {
	return nil, nil
}
func (*chatRunnerDocsStore) UpdateDeck(context.Context, *store.DeckManifest) error { return nil }
func (*chatRunnerDocsStore) DeleteDeck(context.Context, string) error              { return nil }
func (*chatRunnerDocsStore) UpsertSlide(context.Context, *store.SlideContent) error {
	return nil
}
func (*chatRunnerDocsStore) GetSlide(context.Context, string, string) (*store.SlideContent, error) {
	return nil, store.ErrDocsNotFound
}
func (*chatRunnerDocsStore) ListSlides(context.Context, string) ([]*store.SlideContent, error) {
	return nil, nil
}
func (*chatRunnerDocsStore) DeleteSlide(context.Context, string, string) error { return nil }
func (*chatRunnerDocsStore) ReorderSlides(context.Context, string, []string) error {
	return nil
}
func (*chatRunnerDocsStore) DeleteDecksBySessionID(context.Context, string) error { return nil }
func (*chatRunnerDocsStore) SaveDeckVersion(context.Context, *store.DeckVersionSnapshot) error {
	return nil
}
func (*chatRunnerDocsStore) ListDeckVersions(context.Context, string) ([]*store.DeckVersionSnapshot, error) {
	return nil, nil
}
func (*chatRunnerDocsStore) GetDeckVersion(context.Context, string, int) (*store.DeckVersionSnapshot, error) {
	return nil, store.ErrDocsNotFound
}

func TestRequestDocsStoresInjectedIntoChatRunner(t *testing.T) {
	personal := &chatRunnerDocsStore{}
	team := &chatRunnerDocsStore{}
	svc := &store.Services{PersonalDocs: personal, Docs: team}
	req := httptest.NewRequest("POST", "/api/studio/chat", nil)
	req = req.WithContext(store.WithServices(req.Context(), svc))
	runner := newChatRunner("session", "user", false)

	gotSvc := injectRequestDocsStores(runner, req)

	if gotSvc != svc {
		t.Fatal("request services were not returned for reuse")
	}
	got := store.FromContext(runner.Context())
	if got == nil {
		t.Fatal("runner context has no services")
	}
	if got.PersonalDocs != personal {
		t.Fatal("request personal docs store was not injected")
	}
	if got.Docs != team {
		t.Fatal("request team docs store was not injected")
	}
}

func TestRequestDocsStoresInjectNilServicesIsNoOp(t *testing.T) {
	runner := newChatRunner("session", "user", false)
	req := httptest.NewRequest("POST", "/api/studio/chat", nil)

	if got := injectRequestDocsStores(runner, req); got != nil {
		t.Fatalf("expected nil request services, got %#v", got)
	}
	if got := store.FromContext(runner.Context()); got != nil {
		t.Fatalf("nil request services changed runner context: %#v", got)
	}
}

func TestChatRunnerInjectDocsStoresPreservesServicesWithoutMutatingSharedValue(t *testing.T) {
	originalPersonal := &chatRunnerDocsStore{}
	originalTeam := &chatRunnerDocsStore{}
	personal := &chatRunnerDocsStore{}
	team := &chatRunnerDocsStore{}
	shared := &store.Services{
		Mode:         store.ModePlatform,
		PersonalDocs: originalPersonal,
		Docs:         originalTeam,
	}

	runner := newChatRunner("session", "user", false)
	runner.ctx = store.WithServices(runner.ctx, shared)
	runner.InjectDocsStores(personal, team)

	got := store.FromContext(runner.Context())
	if got == nil {
		t.Fatal("runner context has no services")
	}
	if got.PersonalDocs != personal {
		t.Fatal("personal docs store was not injected")
	}
	if got.Docs != team {
		t.Fatal("team docs store was not injected")
	}
	if got.Mode != store.ModePlatform {
		t.Fatalf("unrelated service field was not preserved: Mode = %q", got.Mode)
	}
	if got == shared {
		t.Fatal("runner context retained the shared Services pointer")
	}
	if shared.PersonalDocs != originalPersonal || shared.Docs != originalTeam {
		t.Fatal("original shared Services value was mutated")
	}
}

func TestChatRunnerInjectDocsStoresCreatesServices(t *testing.T) {
	personal := &chatRunnerDocsStore{}
	team := &chatRunnerDocsStore{}
	runner := newChatRunner("session", "user", false)

	runner.InjectDocsStores(personal, team)

	got := store.FromContext(runner.Context())
	if got == nil {
		t.Fatal("runner context has no services")
	}
	if got.PersonalDocs != personal {
		t.Fatal("personal docs store was not injected")
	}
	if got.Docs != team {
		t.Fatal("team docs store was not injected")
	}
}

func TestChatRunnerInjectSlideTemplateStoresPopulatesServices(t *testing.T) {
	platStore := &store.MemorySlideTemplateStore{}
	orgStore := &store.MemorySlideTemplateStore{}

	runner := newChatRunner("session", "user", false)
	runner.InjectDocsStores(&chatRunnerDocsStore{}, &chatRunnerDocsStore{})
	runner.InjectSlideTemplateStores(platStore, orgStore)

	got := store.FromContext(runner.Context())
	if got == nil {
		t.Fatal("runner context has no services")
	}
	if got.PlatformSlideTemplates != platStore {
		t.Fatal("platform slide template store was not injected")
	}
	if got.OrgSlideTemplates != orgStore {
		t.Fatal("org slide template store was not injected")
	}
	if got.PersonalDocs == nil {
		t.Fatal("InjectSlideTemplateStores lost previously-injected PersonalDocs")
	}
}

func TestInjectRequestDocsStoresCarriesSlideTemplateStores(t *testing.T) {
	platStore := &store.MemorySlideTemplateStore{}
	orgStore := &store.MemorySlideTemplateStore{}
	svc := &store.Services{
		PersonalDocs:           &chatRunnerDocsStore{},
		Docs:                   &chatRunnerDocsStore{},
		PlatformSlideTemplates: platStore,
		OrgSlideTemplates:      orgStore,
	}

	r := httptest.NewRequest("POST", "/api/studio/chat", nil)
	r = r.WithContext(store.WithServices(r.Context(), svc))

	runner := newChatRunner("session", "user", false)
	injectRequestDocsStores(runner, r)

	got := store.FromContext(runner.Context())
	if got == nil {
		t.Fatal("runner context has no services after injectRequestDocsStores")
	}
	if got.PlatformSlideTemplates != platStore {
		t.Fatal("platform slide template store was not carried into runner context")
	}
	if got.OrgSlideTemplates != orgStore {
		t.Fatal("org slide template store was not carried into runner context")
	}
}
