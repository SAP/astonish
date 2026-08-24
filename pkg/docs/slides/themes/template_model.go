package themes

// This file defines the lossless template intermediate representation (IR) that
// a high-fidelity .pptx import produces. It is modeled on the pptx-html pilot's
// src/model/types.ts (TemplateModel / LayoutModel / ChromeObject / Placeholder /
// MediaAsset / Background / TextStyle) but expressed in Astonish's canvas units:
// logical pixels on the fixed 1920x1080 canvas and #RRGGBB colors, rather than
// the pilot's inches + bare-hex.
//
// The IR is persisted verbatim (as JSON) on the imported template deck so a
// future in-browser template editor and a high-fidelity re-export have complete,
// lossless input. Today the IR is ALSO serialized down to ASD archetypes for the
// existing renderer/exporters; the IR is the source of truth, the archetypes are
// the rendered projection. Anything the ASD projection cannot express is recorded
// as an IRWarning rather than dropped silently.
//
// SchemaModelV3 mirrors slides.SchemaV3 (kept as a local const to avoid importing
// the parent slides package into themes, which would create an import cycle).
const SchemaModelV3 = 3

// TemplateModel is the top-level lossless IR for one imported template.
type TemplateModel struct {
	Schema   int               `json:"schema"`
	Size     IRSize            `json:"size"`
	Theme    map[string]string `json:"theme,omitempty"`
	Layouts  []IRLayout        `json:"layouts,omitempty"`
	Slides   []IRLayout        `json:"slides,omitempty"`
	Warnings []IRWarning       `json:"warnings,omitempty"`
}

// IRSize is the canvas size in logical pixels (normally 1920x1080).
type IRSize struct {
	W int `json:"w"`
	H int `json:"h"`
}

// IRLayout is one flattened layout OR one flattened sample slide (same shape,
// per the pilot): a background, ordered chrome objects (paint order == z-order),
// and named fillable placeholders.
type IRLayout struct {
	ID           string          `json:"id"`
	Name         string          `json:"name,omitempty"`
	Background   IRBackground    `json:"background"`
	Objects      []IRChrome      `json:"objects,omitempty"`
	Placeholders []IRPlaceholder `json:"placeholders,omitempty"`
	SlideNumber  *IRSlideNumber  `json:"slideNumber,omitempty"`
}

// IRBackground is a solid color or a full-bleed image (referenced by MediaKey,
// resolved through the deck asset map).
type IRBackground struct {
	Kind     string `json:"kind"` // solid | image
	Color    string `json:"color,omitempty"`
	MediaKey string `json:"mediaKey,omitempty"`
}

// IRChrome is a single decorative object. Kind selects which of the optional
// fields are meaningful:
//   - rect/ellipse: Fill, Line, RectRadius (rect only)
//   - line:         Line, FlipH, FlipV
//   - text:         Text, Style
//   - image:        MediaKey
//   - path:         Paths, Fill, Line
type IRChrome struct {
	Kind       string       `json:"kind"`
	X          int          `json:"x"`
	Y          int          `json:"y"`
	W          int          `json:"w"`
	H          int          `json:"h"`
	Rot        int          `json:"rot,omitempty"`
	FlipH      bool         `json:"flipH,omitempty"`
	FlipV      bool         `json:"flipV,omitempty"`
	Fill       *IRFill      `json:"fill,omitempty"`
	Line       *IRLine      `json:"line,omitempty"`
	RectRadius int          `json:"rectRadius,omitempty"`
	Paths      []IRPathSeg  `json:"paths,omitempty"`
	Text       string       `json:"text,omitempty"`
	Style      *IRTextStyle `json:"style,omitempty"`
	MediaKey   string       `json:"mediaKey,omitempty"`
	Name       string       `json:"name,omitempty"`
}

// IRPathSeg is one SVG-path subpath in the object's own W x H unit box. FillNone
// marks a stroke-only subpath. D uses the SVG-subset command set; note that the
// IR retains arc (A) commands that the ASD projection may or may not preserve.
type IRPathSeg struct {
	D        string `json:"d"`
	FillNone bool   `json:"fillNone,omitempty"`
	W        int    `json:"w"`
	H        int    `json:"h"`
}

// IRFill is a shape fill: either a solid color (Kind="solid") or a gradient
// (Kind="gradient"). Transparency is 0..100 percent for solids. The worker emits
// {kind, color?, gradient?}; both variants are retained so the persisted IR is
// lossless even though the ASD projection may approximate a gradient as a solid.
type IRFill struct {
	Kind         string      `json:"kind,omitempty"` // solid | gradient
	Color        string      `json:"color,omitempty"`
	Transparency int         `json:"transparency,omitempty"`
	Gradient     *IRGradient `json:"gradient,omitempty"`
}

// IRGradient is a linear/radial gradient with ordered color stops.
type IRGradient struct {
	Kind  string           `json:"kind,omitempty"` // linear | radial
	Angle int              `json:"angle,omitempty"`
	Stops []IRGradientStop `json:"stops,omitempty"`
}

// IRGradientStop is a single gradient stop: position 0..100 and a #RRGGBB color.
type IRGradientStop struct {
	Pos   int    `json:"pos"`
	Color string `json:"color,omitempty"`
}

// IRLine is a stroke: color, width (px), and dash style.
type IRLine struct {
	Color string `json:"color,omitempty"`
	Width int    `json:"width,omitempty"`
	Dash  string `json:"dash,omitempty"` // solid | dash | dot
}

// IRTextStyle is the resolved text style (after master txStyles inheritance).
type IRTextStyle struct {
	FontFace     string `json:"fontFace,omitempty"`
	FontSize     int    `json:"fontSize,omitempty"`
	Color        string `json:"color,omitempty"`
	Bold         bool   `json:"bold,omitempty"`
	Italic       bool   `json:"italic,omitempty"`
	Underline    bool   `json:"underline,omitempty"`
	Align        string `json:"align,omitempty"`  // left | center | right
	Valign       string `json:"valign,omitempty"` // top | middle | bottom
	Transparency int    `json:"transparency,omitempty"`
}

// IRPlaceholder is a fillable hole. Type is the normalized family
// (title|body|image|chart|table|media); OOXMLType/Idx preserve the source key
// so a future editor can round-trip exactly.
type IRPlaceholder struct {
	Name      string      `json:"name"`
	Type      string      `json:"type"`
	X         int         `json:"x"`
	Y         int         `json:"y"`
	W         int         `json:"w"`
	H         int         `json:"h"`
	Style     IRTextStyle `json:"style"`
	Prompt    string      `json:"prompt,omitempty"`
	OOXMLType string      `json:"ooxmlType,omitempty"`
	Idx       int         `json:"idx,omitempty"`
}

// IRSlideNumber is the slide-number placeholder position/style, kept separate
// because PowerPoint fills it at runtime.
type IRSlideNumber struct {
	X     int         `json:"x"`
	Y     int         `json:"y"`
	W     int         `json:"w,omitempty"`
	H     int         `json:"h,omitempty"`
	Style IRTextStyle `json:"style"`
}

// IRWarning records a construct the import approximated or could not represent.
// Layout is the layout id/name it occurred on (when applicable).
type IRWarning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Layout  string `json:"layout,omitempty"`
}
