package slides

// Logical slide geometry is fixed so web and PowerPoint renderers share one
// deterministic coordinate system.
const (
	CanvasWidth  = 1920
	CanvasHeight = 1080
	UnitsPerInch = 160
	SchemaV1     = 1
	SchemaV2     = 2
	// SchemaV3 marks a template deck backed by a lossless imported TemplateModel
	// IR (persisted in Deck.template_model). Regular decks and plain templates
	// stay on SchemaV1/SchemaV2; only imported high-fidelity templates use V3.
	SchemaV3 = 3
)

type Geometry struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

type TextRun struct {
	Text      string `json:"text"`
	Bold      bool   `json:"bold,omitempty"`
	Italic    bool   `json:"italic,omitempty"`
	Underline bool   `json:"underline,omitempty"`
	Color     string `json:"color,omitempty"`
	Font      string `json:"font,omitempty"`
	Size      int    `json:"size,omitempty"`
	Weight    string `json:"weight,omitempty"`
}

// GradientStop is a single color stop in a gradient fill. Pos is 0..100.
type GradientStop struct {
	Pos   int    `json:"pos"`
	Color string `json:"color"`
}

// Gradient describes a linear or radial gradient fill for an ast-shape.
// Cx/Cy are radial origin percents (0–100). Zero means the exporter default
// (top-right 80/8). The product closer uses ~18/88 (bottom-left).
type Gradient struct {
	Kind  string         `json:"kind"` // linear | radial
	Angle int            `json:"angle,omitempty"`
	Cx    int            `json:"cx,omitempty"`
	Cy    int            `json:"cy,omitempty"`
	Stops []GradientStop `json:"stops"`
}

type TableData struct {
	Rows [][]string `json:"rows"`
}

type ChartSeries struct {
	Name   string    `json:"name"`
	Labels []string  `json:"labels"`
	Values []float64 `json:"values"`
}

type Node struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Geometry Geometry       `json:"geometry"`
	Text     string         `json:"text,omitempty"`
	Runs     []TextRun      `json:"runs,omitempty"`
	Props    map[string]any `json:"props,omitempty"`
	Table    *TableData     `json:"table,omitempty"`
	Series   []ChartSeries  `json:"series,omitempty"`
	Children []Node         `json:"children,omitempty"`

	// v2 fidelity fields.
	Rot      int       `json:"rot,omitempty"`
	Fill     string    `json:"fill,omitempty"`
	Line     string    `json:"line,omitempty"`
	Dash     string    `json:"dash,omitempty"`
	Opacity  float64   `json:"opacity,omitempty"`
	Geom     string    `json:"geom,omitempty"`
	Path     string    `json:"path,omitempty"`
	Gradient *Gradient `json:"gradient,omitempty"`
	FlipH    bool      `json:"flipH,omitempty"`
	FlipV    bool      `json:"flipV,omitempty"`
}

type Slide struct {
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
	Nodes []Node `json:"nodes"`
	Notes string `json:"notes,omitempty"`
}

type SceneGraph struct {
	SchemaVersion int               `json:"schemaVersion"`
	Title         string            `json:"title"`
	Subject       string            `json:"subject,omitempty"`
	Author        string            `json:"author,omitempty"`
	Theme         map[string]string `json:"theme,omitempty"`
	// Assets maps a content-addressed asset ref (e.g. "sha256-...") to a
	// self-contained data: URL. Imported templates carry their logos/media
	// here; the HTML exporter resolves each ast-image's asset-ref against this
	// map into a concrete img src. Empty for decks that embed no assets.
	Assets map[string]string `json:"assets,omitempty"`
	Slides []Slide           `json:"slides"`
}

type CapabilityCounts struct {
	Native      int `json:"native"`
	Vector      int `json:"vector"`
	Raster      int `json:"raster"`
	Unsupported int `json:"unsupported"`
}

type Diagnostic struct {
	Severity  string `json:"severity"`
	Code      string `json:"code"`
	Message   string `json:"message"`
	SlideID   string `json:"slideId,omitempty"`
	ElementID string `json:"elementId,omitempty"`
}

type ExportResult struct {
	Bytes        []byte           `json:"-"`
	Capabilities CapabilityCounts `json:"capabilities"`
	Diagnostics  []Diagnostic     `json:"diagnostics,omitempty"`
}
