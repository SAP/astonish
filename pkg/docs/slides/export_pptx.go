package slides

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/SAP/astonish/pkg/docs/slides/pptxworker"
)

type PPTXExporter struct {
	Runner pptxworker.Runner
}

func (e PPTXExporter) Export(ctx context.Context, scene SceneGraph, strictNative bool) (ExportResult, error) {
	if scene.SchemaVersion != SchemaV1 && scene.SchemaVersion != SchemaV2 {
		return ExportResult{}, fmt.Errorf("unsupported slides schema version %d", scene.SchemaVersion)
	}
	data, err := json.Marshal(scene)
	if err != nil {
		return ExportResult{}, fmt.Errorf("marshal slide scene: %w", err)
	}
	response, err := e.Runner.Run(ctx, pptxworker.Request{
		ProtocolVersion: pptxworker.ProtocolVersion,
		Scene:           data,
		StrictNative:    strictNative,
	})
	if err != nil {
		return ExportResult{}, err
	}
	pptx, err := base64.StdEncoding.DecodeString(response.PPTXBase64)
	if err != nil {
		return ExportResult{}, fmt.Errorf("decode pptx worker output: %w", err)
	}
	result := ExportResult{
		Bytes: pptx,
		Capabilities: CapabilityCounts{
			Native: response.Native, Vector: response.Vector,
			Raster: response.Raster, Unsupported: response.Unsupported,
		},
	}
	for _, warning := range response.Warnings {
		result.Diagnostics = append(result.Diagnostics, Diagnostic{Severity: "warning", Code: "pptx_fallback", Message: warning})
	}
	return result, nil
}
