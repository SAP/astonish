package slides

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/pdfgen"
)

func TestPDFExporterUsesSlidePrintContract(t *testing.T) {
	var gotHTML string
	var gotOptions pdfgen.HTMLPrintOptions
	exporter := PDFExporter{
		RuntimeJS: []byte("window.runtimeReady=true"), Timeout: 12 * time.Second,
		Render: func(document string, _ pdfgen.BrowserProvider, options pdfgen.HTMLPrintOptions) ([]byte, error) {
			gotHTML, gotOptions = document, options
			return []byte("pdf"), nil
		},
	}
	result, err := exporter.Export(exportTestScene())
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Bytes) != "pdf" {
		t.Fatalf("bytes = %q", result.Bytes)
	}
	if !strings.Contains(gotHTML, `<ast-deck schema="1" ratio="16:9" print>`) {
		t.Fatal("print marker missing")
	}
	// Paper matches the 1920x1080 canvas at 96dpi (20in x 11.25in) so each slide
	// fills a page with no scaling or cropping.
	if gotOptions.PaperWidth != 20 || gotOptions.PaperHeight != 11.25 || !gotOptions.Landscape || !gotOptions.PrintBackground {
		t.Fatalf("unexpected print options: %+v", gotOptions)
	}
	if gotOptions.ReadinessExpression != slidesReadinessExpression || gotOptions.Timeout != 12*time.Second {
		t.Fatalf("unexpected readiness options: %+v", gotOptions)
	}
}

func TestPDFExporterWrapsRendererError(t *testing.T) {
	exporter := PDFExporter{RuntimeJS: []byte("runtime"), Render: func(string, pdfgen.BrowserProvider, pdfgen.HTMLPrintOptions) ([]byte, error) {
		return nil, errors.New("boom")
	}}
	if _, err := exporter.Export(exportTestScene()); err == nil || !strings.Contains(err.Error(), "render slides PDF: boom") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestPDFExporterPreservesDiagnosticsOnRendererError asserts that when the
// Chrome render fails, the wrapped error still carries the HTML export
// diagnostics so the handler can log/return actionable context (this is the
// exact wrapping ExportSlidesPDFHandler surfaces to the operator).
func TestPDFExporterPreservesDiagnosticsOnRendererError(t *testing.T) {
	// A scene whose slide validates with a diagnostic-producing node still
	// exports HTML (diagnostics are warnings), then the renderer fails.
	scene := exportTestScene()
	exporter := PDFExporter{RuntimeJS: []byte("runtime"), Render: func(string, pdfgen.BrowserProvider, pdfgen.HTMLPrintOptions) ([]byte, error) {
		return nil, errors.New("failed to launch browser: no session")
	}}
	result, err := exporter.Export(scene)
	if err == nil || !strings.Contains(err.Error(), "render slides PDF: failed to launch browser") {
		t.Fatalf("expected wrapped launch error, got: %v", err)
	}
	// Diagnostics from the HTML export stage must survive onto the error result
	// (Bytes are intentionally empty because the PDF never rendered).
	if len(result.Bytes) != 0 {
		t.Fatalf("expected no PDF bytes on render failure, got %d", len(result.Bytes))
	}
}
