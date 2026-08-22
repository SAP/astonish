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
	if gotOptions.PaperWidth != 12 || gotOptions.PaperHeight != 6.75 || !gotOptions.Landscape || !gotOptions.PrintBackground {
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
