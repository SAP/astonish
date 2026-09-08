package tools

import (
	"fmt"

	"github.com/go-rod/rod/lib/proto"
	"github.com/SAP/astonish/pkg/browser"
	"google.golang.org/adk/tool"
)

// --- browser_highlight ---

type BrowserHighlightArgs struct {
	Ref        string `json:"ref,omitempty" jsonschema:"Element ref from a snapshot (preferred)"`
	Selector   string `json:"selector,omitempty" jsonschema:"CSS selector when ref is unavailable"`
	Label      string `json:"label,omitempty" jsonschema:"Optional caption shown above the highlight"`
	Color      string `json:"color,omitempty" jsonschema:"Highlight color (CSS). Default #FF6B2C"`
	DurationMs int    `json:"duration_ms,omitempty" jsonschema:"Auto-clear after N ms; 0 keeps until browser_clear_highlights"`
}

type BrowserHighlightResult struct {
	Success     bool   `json:"success"`
	HighlightID string `json:"highlight_id,omitempty"`
}

func BrowserHighlight(mgr *browser.Manager, refs *browser.RefMap) func(tool.Context, BrowserHighlightArgs) (BrowserHighlightResult, error) {
	return func(_ tool.Context, args BrowserHighlightArgs) (BrowserHighlightResult, error) {
		if args.Ref == "" && args.Selector == "" {
			return BrowserHighlightResult{}, fmt.Errorf("ref or selector is required")
		}
		if args.Ref != "" {
			pg, err := mgr.CurrentPage()
			if err != nil {
				return BrowserHighlightResult{}, err
			}
			el, err := refs.ResolveElement(pg, args.Ref)
			if err != nil {
				return BrowserHighlightResult{}, err
			}
			el = el.Timeout(elementInteractionTimeout)
			id, err := mgr.HighlightElement(el, args.Label, args.Color, args.DurationMs)
			if err != nil {
				return BrowserHighlightResult{}, err
			}
			return BrowserHighlightResult{Success: true, HighlightID: id}, nil
		}
		id, err := mgr.HighlightSelector(args.Selector, args.Label, args.Color, args.DurationMs)
		if err != nil {
			return BrowserHighlightResult{}, err
		}
		return BrowserHighlightResult{Success: true, HighlightID: id}, nil
	}
}

// --- browser_clear_highlights ---

type BrowserClearHighlightsArgs struct{}

type BrowserClearHighlightsResult struct {
	Success bool `json:"success"`
}

func BrowserClearHighlights(mgr *browser.Manager) func(tool.Context, BrowserClearHighlightsArgs) (BrowserClearHighlightsResult, error) {
	return func(_ tool.Context, _ BrowserClearHighlightsArgs) (BrowserClearHighlightsResult, error) {
		if err := mgr.ClearHighlights(); err != nil {
			return BrowserClearHighlightsResult{}, err
		}
		return BrowserClearHighlightsResult{Success: true}, nil
	}
}

// --- browser_move_cursor ---

type BrowserMoveCursorArgs struct {
	Ref        string   `json:"ref,omitempty" jsonschema:"Element ref to move the cursor to (center)"`
	Selector   string   `json:"selector,omitempty" jsonschema:"CSS selector when ref is unavailable"`
	X          *float64 `json:"x,omitempty" jsonschema:"Absolute X in CSS pixels (with y)"`
	Y          *float64 `json:"y,omitempty" jsonschema:"Absolute Y in CSS pixels (with x)"`
	Steps      int      `json:"steps,omitempty" jsonschema:"Number of intermediate animation frames (default 12). Higher values produce smoother motion when combined with duration_ms. Recommended: 40-80 for visible animations, 12 for instant moves. Controls visual smoothness only — speed is set by duration_ms."`
	DurationMs int      `json:"duration_ms,omitempty" jsonschema:"Total movement duration in milliseconds. The cursor glides from its current position to the target with natural ease-in-out acceleration. ALWAYS set this for visible cursor animations — use 0 only for instant repositioning when the user should not see the cursor travel. Recommended by distance: short moves (<100px) 300-500ms, medium (100-400px) 600-1000ms, long (>400px) 1000-1500ms, dramatic/very long 1500-2500ms. Pair with steps=40-80 for extra smoothness on longer moves."`
}

type BrowserMoveCursorResult struct {
	Success bool    `json:"success"`
	X       float64 `json:"x,omitempty"`
	Y       float64 `json:"y,omitempty"`
}

func BrowserMoveCursor(mgr *browser.Manager, refs *browser.RefMap) func(tool.Context, BrowserMoveCursorArgs) (BrowserMoveCursorResult, error) {
	return func(_ tool.Context, args BrowserMoveCursorArgs) (BrowserMoveCursorResult, error) {
		pg, err := mgr.CurrentPage()
		if err != nil {
			return BrowserMoveCursorResult{}, err
		}

		var pt proto.Point
		switch {
		case args.Ref != "":
			el, err := refs.ResolveElement(pg, args.Ref)
			if err != nil {
				return BrowserMoveCursorResult{}, err
			}
			el = el.Timeout(elementInteractionTimeout)
			center, err := el.Interactable()
			if err != nil {
				return BrowserMoveCursorResult{}, fmt.Errorf("element not interactable: %w", err)
			}
			pt = *center
		case args.Selector != "":
			el, err := pg.Element(args.Selector)
			if err != nil {
				return BrowserMoveCursorResult{}, fmt.Errorf("selector: %w", err)
			}
			el = el.Timeout(elementInteractionTimeout)
			center, err := el.Interactable()
			if err != nil {
				return BrowserMoveCursorResult{}, fmt.Errorf("element not interactable: %w", err)
			}
			pt = *center
		case args.X != nil && args.Y != nil:
			pt = proto.NewPoint(*args.X, *args.Y)
		default:
			return BrowserMoveCursorResult{}, fmt.Errorf("ref, selector, or x+y is required")
		}

		steps := args.Steps
		if steps <= 0 {
			steps = 12
		}
		if err := mgr.MoveMouseAnimated(pg, pt, steps, args.DurationMs); err != nil {
			return BrowserMoveCursorResult{}, err
		}
		return BrowserMoveCursorResult{Success: true, X: pt.X, Y: pt.Y}, nil
	}
}

// --- browser_fullscreen ---

type BrowserFullscreenArgs struct {
	Enabled bool `json:"enabled" jsonschema:"true to enter fullscreen, false to restore normal window"`
}

type BrowserFullscreenResult struct {
	Success bool `json:"success"`
	Enabled bool `json:"enabled"`
}

func BrowserFullscreen(mgr *browser.Manager) func(tool.Context, BrowserFullscreenArgs) (BrowserFullscreenResult, error) {
	return func(_ tool.Context, args BrowserFullscreenArgs) (BrowserFullscreenResult, error) {
		if err := mgr.SetFullscreen(args.Enabled); err != nil {
			return BrowserFullscreenResult{}, err
		}
		return BrowserFullscreenResult{Success: true, Enabled: args.Enabled}, nil
	}
}
