package slides

// Logical slide geometry is fixed so web and PowerPoint renderers share one
// deterministic coordinate system.
const (
	CanvasWidth  = 1920
	CanvasHeight = 1080
	UnitsPerInch = 160
	SchemaV1     = 1
	SchemaV2     = 2
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
type Gradient struct {
	Kind  string         `json:"kind"` // linear | radial
	Angle int            `json:"angle,omitempty"`
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
	Slides        []Slide           `json:"slides"`
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
