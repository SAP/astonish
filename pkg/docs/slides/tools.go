package slides

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/SAP/astonish/pkg/docs/slides/themes"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

const (
	ActionDeckCreated  = "deck_created"
	ActionDeckViewed   = "deck_viewed"
	ActionSlideWritten = "slide_written"
)

// CreateDeckArgs defines the create_deck tool input.
type CreateDeckArgs struct {
	Slug        string            `json:"slug" jsonschema:"Stable URL-safe deck slug."`
	Title       string            `json:"title" jsonschema:"Human-readable deck title."`
	Description string            `json:"description,omitempty" jsonschema:"Optional short deck description."`
	Theme       map[string]string `json:"theme,omitempty" jsonschema:"Optional ASD theme token overrides."`
	Template    string            `json:"template,omitempty" jsonschema:"Optional template name from slide_templates. Seeds a coherent theme + assets; reuse its title/section/content archetypes as starting markup."`
}

// DeckResult is the common deck payload returned by slide tools.
type DeckResult struct {
	Deck       *store.DeckManifest   `json:"deck"`
	Slides     []*store.SlideContent `json:"slides,omitempty"`
	SlideCount int                   `json:"slideCount"`
	Archetypes []themes.Archetype    `json:"archetypes,omitempty"`
}

// WriteSlideArgs defines the write_slide tool input.
type WriteSlideArgs struct {
	DeckSlug string `json:"deck_slug" jsonschema:"Slug of the deck to update."`
	Position int    `json:"position" jsonschema:"Zero-based slide position. Writing an occupied position replaces that slide."`
	Markup   string `json:"markup" jsonschema:"One complete validated ast-slide element using ASD v1 markup. Geometry x/y/w/h uses integer logical pixels on a fixed 1920x1080 canvas, never percentages or a 0-100 coordinate system. ast-text is plain text: use size, weight, font-token, and color-token attributes instead of Markdown markers."`
	Notes    string `json:"notes,omitempty" jsonschema:"Optional speaker notes. Use this field instead of embedding ast-notes in markup."`
}

// WriteSlideResult reports the persisted slide and all validation diagnostics.
type WriteSlideResult struct {
	Deck        *store.DeckManifest `json:"deck"`
	Slide       *store.SlideContent `json:"slide"`
	SlideCount  int                 `json:"slideCount"`
	Diagnostics []Diagnostic        `json:"diagnostics,omitempty"`
}

// GetDeckArgs defines the get_deck tool input.
type GetDeckArgs struct {
	Slug string `json:"slug" jsonschema:"Deck slug to read."`
}

// ListDecksArgs defines the list_decks tool input.
type ListDecksArgs struct{}

// ListDecksResult contains the user's private decks.
type ListDecksResult struct {
	Decks []*store.DeckManifest `json:"decks"`
}

// ListTemplatesArgs defines the slide_templates tool input.
type ListTemplatesArgs struct{}

// ListTemplatesResult contains the available slide templates (built-in + saved).
type ListTemplatesResult struct {
	Templates []themes.Template `json:"templates"`
}

// ValidateDeckArgs defines the validate_deck tool input.
type ValidateDeckArgs struct {
	Slug string `json:"slug" jsonschema:"Deck slug to validate."`
}

// DeckDiagnostic associates one ASD diagnostic with its persisted slide.
type DeckDiagnostic struct {
	SlideID    string     `json:"slideId"`
	Position   int        `json:"position"`
	Diagnostic Diagnostic `json:"diagnostic"`
}

// ValidateDeckResult reports whether every persisted slide is valid ASD v1.
type ValidateDeckResult struct {
	Deck        *store.DeckManifest `json:"deck"`
	SlideCount  int                 `json:"slideCount"`
	Valid       bool                `json:"valid"`
	Diagnostics []DeckDiagnostic    `json:"diagnostics,omitempty"`
}

func personalService(ctx context.Context) (Service, error) {
	svc := store.FromContext(ctx)
	if svc == nil || svc.PersonalDocs == nil {
		return Service{}, fmt.Errorf("personal docs store unavailable")
	}
	return Service{Store: svc.PersonalDocs}, nil
}

func createDeck(ctx context.Context, args CreateDeckArgs) (DeckResult, error) {
	svc, err := personalService(ctx)
	if err != nil {
		return DeckResult{}, err
	}
	slug := strings.TrimSpace(args.Slug)
	title := strings.TrimSpace(args.Title)
	description := strings.TrimSpace(args.Description)

	if name := strings.TrimSpace(args.Template); name != "" {
		tmpl, ok := themes.LookupTemplate(name)
		if !ok {
			scoped, listErr := svc.ListTemplates(ctx)
			if listErr != nil {
				return DeckResult{}, fmt.Errorf("resolve template %q: %w", name, listErr)
			}
			for _, t := range scoped {
				if t.Name == name {
					tmpl, ok = t, true
					break
				}
			}
		}
		if !ok {
			return DeckResult{}, fmt.Errorf("unknown template %q", name)
		}
		// Merge tokens: template tokens first, explicit args.Theme overlays win.
		merged := make(map[string]string, len(tmpl.Tokens)+len(args.Theme))
		for k, v := range tmpl.Tokens {
			merged[k] = v
		}
		for k, v := range args.Theme {
			merged[k] = v
		}
		deck, err := svc.CreateDeckWithAssets(ctx, slug, title, description, merged, tmpl.Assets)
		if err != nil {
			return DeckResult{}, err
		}
		return DeckResult{Deck: deck, Archetypes: tmpl.Archetypes}, nil
	}

	deck, err := svc.CreateDeck(ctx, slug, title, description, args.Theme)
	if err != nil {
		return DeckResult{}, err
	}
	return DeckResult{Deck: deck}, nil
}

func writeSlide(ctx context.Context, args WriteSlideArgs) (WriteSlideResult, error) {
	if args.Position < 0 {
		return WriteSlideResult{}, fmt.Errorf("position must be zero or greater")
	}
	svc, err := personalService(ctx)
	if err != nil {
		return WriteSlideResult{}, err
	}
	slide, diagnostics, err := svc.WriteSlide(ctx, strings.TrimSpace(args.DeckSlug), args.Position, args.Markup, args.Notes)
	if err != nil {
		return WriteSlideResult{Diagnostics: diagnostics}, err
	}
	deck, slides, err := svc.Deck(ctx, args.DeckSlug)
	if err != nil {
		return WriteSlideResult{}, fmt.Errorf("reload deck: %w", err)
	}
	return WriteSlideResult{Deck: deck, Slide: slide, SlideCount: len(slides), Diagnostics: diagnostics}, nil
}

func getDeck(ctx context.Context, args GetDeckArgs) (DeckResult, error) {
	svc, err := personalService(ctx)
	if err != nil {
		return DeckResult{}, err
	}
	deck, slides, err := svc.Deck(ctx, strings.TrimSpace(args.Slug))
	if err != nil {
		return DeckResult{}, err
	}
	sort.Slice(slides, func(i, j int) bool { return slides[i].Position < slides[j].Position })
	return DeckResult{Deck: deck, Slides: slides, SlideCount: len(slides)}, nil
}

func listDecks(ctx context.Context, _ ListDecksArgs) (ListDecksResult, error) {
	svc, err := personalService(ctx)
	if err != nil {
		return ListDecksResult{}, err
	}
	decks, err := svc.ListDecks(ctx)
	if err != nil {
		return ListDecksResult{}, fmt.Errorf("list decks: %w", err)
	}
	return ListDecksResult{Decks: decks}, nil
}

func listTemplates(ctx context.Context, _ ListTemplatesArgs) (ListTemplatesResult, error) {
	svc, err := personalService(ctx)
	if err != nil {
		return ListTemplatesResult{}, err
	}
	out := themes.ListTemplates()
	seen := make(map[string]bool, len(out))
	for _, t := range out {
		seen[t.Name] = true
	}
	scoped, err := svc.ListTemplates(ctx)
	if err != nil {
		return ListTemplatesResult{}, fmt.Errorf("list templates: %w", err)
	}
	for _, t := range scoped {
		if seen[t.Name] {
			continue
		}
		seen[t.Name] = true
		out = append(out, t)
	}
	return ListTemplatesResult{Templates: out}, nil
}

func validateDeck(ctx context.Context, args ValidateDeckArgs) (ValidateDeckResult, error) {
	deckResult, err := getDeck(ctx, GetDeckArgs{Slug: args.Slug})
	if err != nil {
		return ValidateDeckResult{}, err
	}
	result := ValidateDeckResult{Deck: deckResult.Deck, SlideCount: deckResult.SlideCount, Valid: true}
	for _, persisted := range deckResult.Slides {
		_, diagnostics, parseErr := ParseSlide(persisted.Content)
		if parseErr != nil {
			result.Valid = false
			result.Diagnostics = append(result.Diagnostics, DeckDiagnostic{
				SlideID: persisted.ID, Position: persisted.Position,
				Diagnostic: Diagnostic{Severity: "error", Code: "invalid_markup", Message: parseErr.Error()},
			})
			continue
		}
		for _, diagnostic := range diagnostics {
			if diagnostic.Severity == "error" {
				result.Valid = false
			}
			result.Diagnostics = append(result.Diagnostics, DeckDiagnostic{SlideID: persisted.ID, Position: persisted.Position, Diagnostic: diagnostic})
		}
	}
	return result, nil
}

// GetTools returns the chat tools for authoring and inspecting private slide decks.
func GetTools() ([]tool.Tool, error) {
	specs := []struct {
		name        string
		description string
		newTool     func() (tool.Tool, error)
	}{
		{"create_deck", "Create a new private Astonish Slides deck before writing slides. Pass an optional template (see slide_templates) to seed a coherent theme, assets, and starting archetypes.", func() (tool.Tool, error) {
			return functiontool.New(functiontool.Config{Name: "create_deck", Description: "Create a new private Astonish Slides deck before writing slides. Pass an optional template (see slide_templates) to seed a coherent theme, assets, and starting archetypes."}, func(ctx tool.Context, args CreateDeckArgs) (DeckResult, error) {
				return createDeck(ctx, args)
			})
		}},
		{"write_slide", "Validate and write one complete ASD v1 ast-slide at a zero-based position in a private deck.", func() (tool.Tool, error) {
			return functiontool.New(functiontool.Config{Name: "write_slide", Description: "Validate and write one complete ASD v1 ast-slide at a zero-based position in a private deck."}, func(ctx tool.Context, args WriteSlideArgs) (WriteSlideResult, error) {
				return writeSlide(ctx, args)
			})
		}},
		{"get_deck", "Read a private slide deck and its ordered canonical ASD slide markup.", func() (tool.Tool, error) {
			return functiontool.New(functiontool.Config{Name: "get_deck", Description: "Read a private slide deck and its ordered canonical ASD slide markup."}, func(ctx tool.Context, args GetDeckArgs) (DeckResult, error) {
				return getDeck(ctx, args)
			})
		}},
		{"list_decks", "List private Astonish Slides decks available to the current user.", func() (tool.Tool, error) {
			return functiontool.New(functiontool.Config{Name: "list_decks", Description: "List private Astonish Slides decks available to the current user."}, func(ctx tool.Context, args ListDecksArgs) (ListDecksResult, error) {
				return listDecks(ctx, args)
			})
		}},
		{"slide_templates", "List available slide templates (built-in + saved) to seed a styled deck via create_deck.", func() (tool.Tool, error) {
			return functiontool.New(functiontool.Config{Name: "slide_templates", Description: "List available slide templates (built-in + saved) to seed a styled deck via create_deck."}, func(ctx tool.Context, args ListTemplatesArgs) (ListTemplatesResult, error) {
				return listTemplates(ctx, args)
			})
		}},
		{"validate_deck", "Validate every persisted slide in a private deck and return structured ASD diagnostics.", func() (tool.Tool, error) {
			return functiontool.New(functiontool.Config{Name: "validate_deck", Description: "Validate every persisted slide in a private deck and return structured ASD diagnostics."}, func(ctx tool.Context, args ValidateDeckArgs) (ValidateDeckResult, error) {
				return validateDeck(ctx, args)
			})
		}},
	}

	out := make([]tool.Tool, 0, len(specs))
	for _, spec := range specs {
		t, err := spec.newTool()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", spec.name, err)
		}
		out = append(out, t)
	}
	return out, nil
}
