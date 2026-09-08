package tools

import (
	"fmt"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
	"github.com/SAP/astonish/pkg/browser"
	"google.golang.org/adk/tool"
)

// elementInteractionTimeout bounds all implicit rod Wait calls (WaitStableRAF,
// WaitInteractable, WaitEnabled, WaitWritable) that run inside Click, Input,
// Hover, Select, etc. Without this, those waits poll indefinitely on dynamic
// SPAs like Reddit where the DOM never fully stabilizes.
const elementInteractionTimeout = 30 * time.Second

// --- browser_click ---

type BrowserClickArgs struct {
	Ref           string `json:"ref" jsonschema:"Element ref from a snapshot (e.g. ref5)"`
	Button        string `json:"button,omitempty" jsonschema:"Mouse button: left (default), right, or middle"`
	DoubleClick   bool   `json:"doubleClick,omitempty" jsonschema:"Double-click instead of single click"`
	AnimateCursor bool   `json:"animate_cursor,omitempty" jsonschema:"Move the visible demo cursor to the element before clicking (tutorial recordings)"`
}

type BrowserClickResult struct {
	Success     bool   `json:"success"`
	Description string `json:"description,omitempty"`
}

func BrowserClick(mgr *browser.Manager, refs *browser.RefMap) func(tool.Context, BrowserClickArgs) (BrowserClickResult, error) {
	return func(_ tool.Context, args BrowserClickArgs) (BrowserClickResult, error) {
		if args.Ref == "" {
			return BrowserClickResult{}, fmt.Errorf("ref is required")
		}

		pg, err := mgr.CurrentPage()
		if err != nil {
			return BrowserClickResult{}, err
		}

		el, err := refs.ResolveElement(pg, args.Ref)
		if err != nil {
			return BrowserClickResult{}, err
		}

		// Apply timeout so rod's internal WaitInteractable/WaitEnabled don't block
		// indefinitely on dynamic SPAs (e.g. Reddit overlays, spinners).
		el = el.Timeout(elementInteractionTimeout)

		button := proto.InputMouseButtonLeft
		switch strings.ToLower(args.Button) {
		case "right":
			button = proto.InputMouseButtonRight
		case "middle":
			button = proto.InputMouseButtonMiddle
		}

		clickCount := 1
		if args.DoubleClick {
			clickCount = 2
		}

		// Subtle random delay before clicking to break robotic timing patterns.
		browser.HumanDelay(50, 200)

		if args.AnimateCursor {
			center, err := el.Interactable()
			if err != nil {
				return BrowserClickResult{}, fmt.Errorf("element not interactable: %w", err)
			}
			if err := mgr.MoveMouseAnimated(pg, *center, 12, 0); err != nil {
				return BrowserClickResult{}, fmt.Errorf("animate cursor: %w", err)
			}
			browser.HumanDelay(80, 220)
			if err := pg.Mouse.Click(button, clickCount); err != nil {
				return BrowserClickResult{}, fmt.Errorf("click failed: %w", err)
			}
		} else if err := el.Click(button, clickCount); err != nil {
			return BrowserClickResult{}, fmt.Errorf("click failed: %w", err)
		}

		entry, _ := refs.Get(args.Ref)
		desc := fmt.Sprintf("Clicked %s", args.Ref)
		if entry.Name != "" {
			desc = fmt.Sprintf("Clicked %s %q", entry.Role, entry.Name)
		}

		return BrowserClickResult{Success: true, Description: desc}, nil
	}
}

// --- browser_type ---

type BrowserTypeArgs struct {
	Ref    string `json:"ref" jsonschema:"Element ref for the input field"`
	Text   string `json:"text" jsonschema:"Text to type into the element"`
	Submit bool   `json:"submit,omitempty" jsonschema:"Press Enter after typing (submit form)"`
	Slowly bool   `json:"slowly,omitempty" jsonschema:"Type character by character (for sites that need keystroke events)"`
}

type BrowserTypeResult struct {
	Success bool `json:"success"`
}

func BrowserType(mgr *browser.Manager, refs *browser.RefMap) func(tool.Context, BrowserTypeArgs) (BrowserTypeResult, error) {
	return func(_ tool.Context, args BrowserTypeArgs) (BrowserTypeResult, error) {
		if args.Ref == "" {
			return BrowserTypeResult{}, fmt.Errorf("ref is required")
		}
		if args.Text == "" {
			return BrowserTypeResult{}, fmt.Errorf("text is required")
		}

		pg, err := mgr.CurrentPage()
		if err != nil {
			return BrowserTypeResult{}, err
		}

		el, err := refs.ResolveElement(pg, args.Ref)
		if err != nil {
			return BrowserTypeResult{}, err
		}

		// Apply timeout so rod's internal WaitStableRAF/WaitEnabled/WaitWritable
		// don't block indefinitely on SPAs with continuous DOM mutations.
		el = el.Timeout(elementInteractionTimeout)

		// Clear existing content first
		if err := el.SelectAllText(); err == nil {
			_ = el.Type(input.Backspace)
		}

		if args.Slowly {
			// Type character by character with human-like random jitter
			// (50-150ms between keystrokes) to avoid bot detection.
			if err := browser.TypeHuman(el, args.Text); err != nil {
				return BrowserTypeResult{}, fmt.Errorf("typing failed: %w", err)
			}
		} else {
			if err := el.Input(args.Text); err != nil {
				return BrowserTypeResult{}, fmt.Errorf("typing failed: %w", err)
			}
		}

		if args.Submit {
			if err := el.Type(input.Enter); err != nil {
				return BrowserTypeResult{}, fmt.Errorf("submit (Enter) failed: %w", err)
			}
		}

		return BrowserTypeResult{Success: true}, nil
	}
}

// --- browser_hover ---

type BrowserHoverArgs struct {
	Ref string `json:"ref" jsonschema:"Element ref to hover over"`
}

type BrowserHoverResult struct {
	Success bool `json:"success"`
}

func BrowserHover(mgr *browser.Manager, refs *browser.RefMap) func(tool.Context, BrowserHoverArgs) (BrowserHoverResult, error) {
	return func(_ tool.Context, args BrowserHoverArgs) (BrowserHoverResult, error) {
		if args.Ref == "" {
			return BrowserHoverResult{}, fmt.Errorf("ref is required")
		}

		pg, err := mgr.CurrentPage()
		if err != nil {
			return BrowserHoverResult{}, err
		}

		el, err := refs.ResolveElement(pg, args.Ref)
		if err != nil {
			return BrowserHoverResult{}, err
		}

		// Apply timeout so rod's internal WaitInteractable doesn't block indefinitely.
		el = el.Timeout(elementInteractionTimeout)

		// Subtle random delay before hovering to break robotic timing patterns.
		browser.HumanDelay(30, 150)

		if err := el.Hover(); err != nil {
			return BrowserHoverResult{}, fmt.Errorf("hover failed: %w", err)
		}

		return BrowserHoverResult{Success: true}, nil
	}
}

// --- browser_drag ---

type BrowserDragArgs struct {
	StartRef string `json:"startRef" jsonschema:"Element ref to drag from"`
	EndRef   string `json:"endRef" jsonschema:"Element ref to drop onto"`
}

type BrowserDragResult struct {
	Success bool `json:"success"`
}

func BrowserDrag(mgr *browser.Manager, refs *browser.RefMap) func(tool.Context, BrowserDragArgs) (BrowserDragResult, error) {
	return func(_ tool.Context, args BrowserDragArgs) (BrowserDragResult, error) {
		if args.StartRef == "" || args.EndRef == "" {
			return BrowserDragResult{}, fmt.Errorf("startRef and endRef are required")
		}

		pg, err := mgr.CurrentPage()
		if err != nil {
			return BrowserDragResult{}, err
		}

		startEl, err := refs.ResolveElement(pg, args.StartRef)
		if err != nil {
			return BrowserDragResult{}, fmt.Errorf("startRef: %w", err)
		}

		endEl, err := refs.ResolveElement(pg, args.EndRef)
		if err != nil {
			return BrowserDragResult{}, fmt.Errorf("endRef: %w", err)
		}

		// Apply timeout so rod's internal WaitStableRAF doesn't block indefinitely.
		startEl = startEl.Timeout(elementInteractionTimeout)
		endEl = endEl.Timeout(elementInteractionTimeout)

		// Get center points of both elements
		startPt, err := startEl.Interactable()
		if err != nil {
			return BrowserDragResult{}, fmt.Errorf("start element not interactable: %w", err)
		}
		endPt, err := endEl.Interactable()
		if err != nil {
			return BrowserDragResult{}, fmt.Errorf("end element not interactable: %w", err)
		}

		// Perform drag: move to start, mousedown, move to end, mouseup
		mouse := pg.Mouse
		if err := mouse.MoveTo(*startPt); err != nil {
			return BrowserDragResult{}, err
		}
		if err := mouse.Down(proto.InputMouseButtonLeft, 1); err != nil {
			return BrowserDragResult{}, err
		}
		if err := mouse.MoveLinear(*endPt, 10); err != nil {
			return BrowserDragResult{}, err
		}
		if err := mouse.Up(proto.InputMouseButtonLeft, 1); err != nil {
			return BrowserDragResult{}, err
		}

		return BrowserDragResult{Success: true}, nil
	}
}

// --- browser_press_key ---

type BrowserPressKeyArgs struct {
	Key string `json:"key" jsonschema:"Key to press (e.g. Enter, Tab, Escape, Backspace, ArrowDown, a, A)"`
}

type BrowserPressKeyResult struct {
	Success bool `json:"success"`
}

// keyMap maps common key names to rod input.Key constants.
var keyMap = map[string]input.Key{
	"enter":      input.Enter,
	"tab":        input.Tab,
	"escape":     input.Escape,
	"backspace":  input.Backspace,
	"delete":     input.Delete,
	"arrowup":    input.ArrowUp,
	"arrowdown":  input.ArrowDown,
	"arrowleft":  input.ArrowLeft,
	"arrowright": input.ArrowRight,
	"home":       input.Home,
	"end":        input.End,
	"pageup":     input.PageUp,
	"pagedown":   input.PageDown,
	"space":      input.Space,
	" ":          input.Space,
	"control":    input.ControlLeft,
	"shift":      input.ShiftLeft,
	"alt":        input.AltLeft,
	"meta":       input.MetaLeft,
	"f1":         input.F1,
	"f2":         input.F2,
	"f3":         input.F3,
	"f4":         input.F4,
	"f5":         input.F5,
	"f6":         input.F6,
	"f7":         input.F7,
	"f8":         input.F8,
	"f9":         input.F9,
	"f10":        input.F10,
	"f11":        input.F11,
	"f12":        input.F12,
}

func BrowserPressKey(mgr *browser.Manager) func(tool.Context, BrowserPressKeyArgs) (BrowserPressKeyResult, error) {
	return func(_ tool.Context, args BrowserPressKeyArgs) (BrowserPressKeyResult, error) {
		if args.Key == "" {
			return BrowserPressKeyResult{}, fmt.Errorf("key is required")
		}

		pg, err := mgr.CurrentPage()
		if err != nil {
			return BrowserPressKeyResult{}, err
		}

		keyLower := strings.ToLower(args.Key)
		if k, ok := keyMap[keyLower]; ok {
			if err := pg.Keyboard.Type(k); err != nil {
				return BrowserPressKeyResult{}, fmt.Errorf("key press failed: %w", err)
			}
		} else if len(args.Key) == 1 {
			// Single character — use InsertText
			if err := pg.InsertText(args.Key); err != nil {
				return BrowserPressKeyResult{}, fmt.Errorf("key press failed: %w", err)
			}
		} else {
			return BrowserPressKeyResult{}, fmt.Errorf("unknown key %q — use names like Enter, Tab, Escape, ArrowDown, or single characters", args.Key)
		}

		return BrowserPressKeyResult{Success: true}, nil
	}
}

// --- browser_select_option ---

type BrowserSelectOptionArgs struct {
	Ref    string   `json:"ref" jsonschema:"Element ref for the select dropdown"`
	Values []string `json:"values" jsonschema:"Option values to select"`
}

type BrowserSelectOptionResult struct {
	Success  bool     `json:"success"`
	Selected []string `json:"selected,omitempty"`
}

func BrowserSelectOption(mgr *browser.Manager, refs *browser.RefMap) func(tool.Context, BrowserSelectOptionArgs) (BrowserSelectOptionResult, error) {
	return func(_ tool.Context, args BrowserSelectOptionArgs) (BrowserSelectOptionResult, error) {
		if args.Ref == "" {
			return BrowserSelectOptionResult{}, fmt.Errorf("ref is required")
		}

		pg, err := mgr.CurrentPage()
		if err != nil {
			return BrowserSelectOptionResult{}, err
		}

		el, err := refs.ResolveElement(pg, args.Ref)
		if err != nil {
			return BrowserSelectOptionResult{}, err
		}

		// Apply timeout so rod's internal WaitStableRAF doesn't block indefinitely.
		el = el.Timeout(elementInteractionTimeout)

		// Use Select with SelectorTypeText to match by visible text
		if err := el.Select(args.Values, true, rod.SelectorTypeText); err != nil {
			return BrowserSelectOptionResult{}, fmt.Errorf("select failed: %w", err)
		}

		return BrowserSelectOptionResult{Success: true, Selected: args.Values}, nil
	}
}

// --- browser_fill_form ---

type FormField struct {
	Ref   string `json:"ref" jsonschema:"Element ref for the form field"`
	Value string `json:"value" jsonschema:"Value to fill in"`
}

type BrowserFillFormArgs struct {
	Fields []FormField `json:"fields" jsonschema:"Array of {ref, value} pairs to fill"`
}

type BrowserFillFormResult struct {
	Success     bool `json:"success"`
	FilledCount int  `json:"filled_count"`
}

func BrowserFillForm(mgr *browser.Manager, refs *browser.RefMap) func(tool.Context, BrowserFillFormArgs) (BrowserFillFormResult, error) {
	return func(_ tool.Context, args BrowserFillFormArgs) (BrowserFillFormResult, error) {
		if len(args.Fields) == 0 {
			return BrowserFillFormResult{}, fmt.Errorf("fields is required")
		}

		pg, err := mgr.CurrentPage()
		if err != nil {
			return BrowserFillFormResult{}, err
		}

		filled := 0
		for i, f := range args.Fields {
			// Random pause between fields mimics a real user tabbing through a form.
			if i > 0 {
				browser.HumanDelay(100, 300)
			}

			el, err := refs.ResolveElement(pg, f.Ref)
			if err != nil {
				return BrowserFillFormResult{Success: false, FilledCount: filled}, fmt.Errorf("field %s: %w", f.Ref, err)
			}

			// Apply timeout so rod's internal Wait calls don't block indefinitely.
			el = el.Timeout(elementInteractionTimeout)

			// Clear and fill
			if err := el.SelectAllText(); err == nil {
				_ = el.Type(input.Backspace)
			}
			if err := el.Input(f.Value); err != nil {
				return BrowserFillFormResult{Success: false, FilledCount: filled}, fmt.Errorf("field %s fill failed: %w", f.Ref, err)
			}
			filled++
		}

		return BrowserFillFormResult{Success: true, FilledCount: filled}, nil
	}
}
