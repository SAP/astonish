package pptxworker

import "encoding/json"

const ProtocolVersion = 1

type Request struct {
	ProtocolVersion int             `json:"protocolVersion"`
	Scene           json.RawMessage `json:"scene"`
	StrictNative    bool            `json:"strictNative"`
}

type Response struct {
	ProtocolVersion int      `json:"protocolVersion"`
	PPTXBase64      string   `json:"pptxBase64,omitempty"`
	Native          int      `json:"native"`
	Vector          int      `json:"vector"`
	Raster          int      `json:"raster"`
	Unsupported     int      `json:"unsupported"`
	Warnings        []string `json:"warnings,omitempty"`
	Error           string   `json:"error,omitempty"`
}
