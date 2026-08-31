package slides

import (
	"fmt"
	"time"

	"github.com/SAP/astonish/pkg/pdfgen"
)

// SlidesReadinessExpression is the JS predicate the slides runtime sets true once
// the ast-deck has finished rendering; PDF and PNG exporters wait on it before
// capturing so the output is never a blank/partial frame.
const SlidesReadinessExpression = `document.documentElement.dataset.astRenderComplete === 'true'`

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
		// PaperWidth and PaperHeight already describe a landscape sheet. Setting
		// Landscape would make Chrome swap these custom dimensions and collapse
		// the 20in-wide print layout instead of paginating it correctly.
		Landscape: false,
		// Match the 1920x1080 canvas exactly: 1920/96dpi = 20in, 1080/96dpi = 11.25in.
		// This keeps the @page box (declared in the print CSS) 1:1 with the sheet so
		// each slide fills the page with no scaling or cropping.
		PaperWidth:          20,
		PaperHeight:         11.25,
		PrintBackground:     true,
		ReadinessExpression: SlidesReadinessExpression,
		Timeout:             e.Timeout,
	})
	if err != nil {
		return ExportResult{Diagnostics: htmlResult.Diagnostics}, fmt.Errorf("render slides PDF: %w", err)
	}
	htmlResult.Bytes = pdf
	return htmlResult, nil
}
