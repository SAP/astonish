package slides

import (
	"fmt"
	"time"

	"github.com/SAP/astonish/pkg/pdfgen"
)

const slidesReadinessExpression = `document.documentElement.dataset.astRenderComplete === 'true'`

type HTMLPDFRenderer func(string, pdfgen.BrowserProvider, pdfgen.HTMLPrintOptions) ([]byte, error)

// PDFExporter prints the same self-contained deck document used by HTML export.
type PDFExporter struct {
	Browser   pdfgen.BrowserProvider
	RuntimeJS []byte
	Timeout   time.Duration
	Render    HTMLPDFRenderer
}

func (e PDFExporter) Export(scene SceneGraph) (ExportResult, error) {
	htmlResult, err := (HTMLExporter{RuntimeJS: e.RuntimeJS, Print: true}).Export(scene)
	if err != nil {
		return htmlResult, err
	}
	render := e.Render
	if render == nil {
		render = pdfgen.RenderHTMLToPDFChrome
	}
	pdf, err := render(string(htmlResult.Bytes), e.Browser, pdfgen.HTMLPrintOptions{
		Landscape:           true,
		PaperWidth:          12,
		PaperHeight:         6.75,
		PrintBackground:     true,
		ReadinessExpression: slidesReadinessExpression,
		Timeout:             e.Timeout,
	})
	if err != nil {
		return ExportResult{Diagnostics: htmlResult.Diagnostics}, fmt.Errorf("render slides PDF: %w", err)
	}
	htmlResult.Bytes = pdf
	return htmlResult, nil
}
