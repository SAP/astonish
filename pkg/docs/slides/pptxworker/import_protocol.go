package pptxworker

import "encoding/json"

// ImportProtocolVersion pins the request/response contract shared with the
// Node PPTX->ASD import worker (import_worker.mjs).
const ImportProtocolVersion = 1

// ImportRequest is the single JSON message written to the import worker stdin.
// Mode selects the output shape: "deck" emits a full ASD v2 SceneGraph,
// "template" emits an ASD v2 Template document.
type ImportRequest struct {
	ProtocolVersion int    `json:"protocolVersion"`
	PPTXBase64      string `json:"pptxBase64"`
	Mode            string `json:"mode"`
}

// ImportResponse is the single JSON message the import worker writes to stdout.
// SceneOrTemplate is a raw JSON document whose shape depends on the request
// Mode (SceneGraph for deck, Template for template).
type ImportResponse struct {
	ProtocolVersion int             `json:"protocolVersion"`
	SceneOrTemplate json.RawMessage `json:"sceneOrTemplate"`
	Warnings        []string        `json:"warnings,omitempty"`
	Error           string          `json:"error,omitempty"`
}
