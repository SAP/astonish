package slides

// Logical slide geometry is fixed so web and PowerPoint renderers share one
// deterministic coordinate system.
const (
	CanvasWidth  = 1920
	CanvasHeight = 1080
	UnitsPerInch = 160
	SchemaV1     = 1
)

type Geometry struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

type TextRun struct {
	Text   string `json:"text"`
	Bold   bool   `json:"bold,omitempty"`
	Italic bool   `json:"italic,omitempty"`
	Color  string `json:"color,omitempty"`
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
