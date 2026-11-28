package main

import (
	"encoding/json"
	"fmt"
	"mime"
	"os"
	"path/filepath"

	"github.com/SAP/astonish/pkg/store"
	"github.com/go-rod/rod"
)

type employeePeerFormat string

const (
	employeePeerJSON employeePeerFormat = "application/json"
	employeePeerSSE  employeePeerFormat = "text/event-stream"
)

type employeePeerPayload struct {
	Source      string             `json:"source"`
	Path        string             `json:"path"`
	Status      int                `json:"status"`
	ContentType string             `json:"content_type"`
	Format      employeePeerFormat `json:"expected_format"`
	Body        string             `json:"-"`
}

// A read-only native browser GET, with explicit wire type. Keep raw SSE as SSE,
// JSON as JSON, and record the exact source even when a parser rejects it.
func employeeCapturePeer(page *rod.Page, root, label, path string, format employeePeerFormat) (employeePeerPayload, error) {
	p := employeePeerPayload{Path: path, Format: format}
	suffix := ".json"
	if format == employeePeerSSE {
		suffix = ".sse"
	} else if format != employeePeerJSON {
		return p, fmt.Errorf("unknown peer format %q", format)
	}
	p.Source = "employee-" + label + suffix
	result, err := page.Eval(`async path => { const r=await fetch(path,{credentials:'include',headers:{'X-Requested-With':'XMLHttpRequest'}}); return JSON.stringify({status:r.status,content_type:r.headers.get('Content-Type')||'',body:await r.text()}) }`, path)
	if err != nil {
		return p, fmt.Errorf("%s GET %s: %w", p.Source, path, err)
	}
	var wire struct {
		Status      int    `json:"status"`
		ContentType string `json:"content_type"`
		Body        string `json:"body"`
	}
	if err := json.Unmarshal([]byte(result.Value.Str()), &wire); err != nil {
		return p, fmt.Errorf("%s browser response: %w", p.Source, err)
	}
	p.Status, p.ContentType, p.Body = wire.Status, wire.ContentType, wire.Body
	if err := os.WriteFile(filepath.Join(root, p.Source), []byte(p.Body), 0600); err != nil {
		return p, err
	}
	metadata, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return p, err
	}
	if err := os.WriteFile(filepath.Join(root, "employee-"+label+"-source.json"), metadata, 0600); err != nil {
		return p, err
	}
	if err := p.validate(); err != nil {
		return p, err
	}
	return p, nil
}
func (p employeePeerPayload) validate() error {
	media, _, err := mime.ParseMediaType(p.ContentType)
	if p.Status != 200 || err != nil || media != string(p.Format) {
		return fmt.Errorf("%s GET %s HTTP %d: expected %s, received %q", p.Source, p.Path, p.Status, p.Format, p.ContentType)
	}
	if p.Format == employeePeerJSON && !json.Valid([]byte(p.Body)) {
		return fmt.Errorf("%s GET %s: malformed JSON document", p.Source, p.Path)
	}
	return nil
}
func (p employeePeerPayload) decode(dst any) error {
	if err := p.validate(); err != nil {
		return err
	}
	if p.Format != employeePeerJSON {
		return fmt.Errorf("%s: SSE is not a JSON document", p.Source)
	}
	if err := json.Unmarshal([]byte(p.Body), dst); err != nil {
		return fmt.Errorf("%s GET %s JSON: %w", p.Source, p.Path, err)
	}
	return nil
}
func (p employeePeerPayload) titleRun(peer store.Bot, runs []store.BotRun) (store.BotRun, error) {
	if err := p.validate(); err != nil {
		return store.BotRun{}, err
	}
	if p.Format != employeePeerSSE {
		return store.BotRun{}, fmt.Errorf("%s: JSON is not persisted SSE", p.Source)
	}
	run, err := employeePeerTitleRun(p.Body, peer, runs)
	if err != nil {
		return run, fmt.Errorf("%s GET %s persisted SSE: %w", p.Source, p.Path, err)
	}
	return run, nil
}

// The observer row is independent of API rendering. Include its artifact/field
// in failures instead of leaking an unlabeled json.Unmarshal error.
func employeeDiskHomeVolume(raw []byte, source, botID string) (string, error) {
	var disk employeeDisk
	if err := json.Unmarshal(raw, &disk); err != nil {
		return "", fmt.Errorf("%s disk JSON: %w", source, err)
	}
	if len(disk.Bots) != 1 || disk.Bots[0].ID != botID {
		return "", fmt.Errorf("%s: physical Home row identity mismatch", source)
	}
	volume, err := employeeHomeVolume(disk.Bots[0].HomeBinding, botID)
	if err != nil {
		return "", fmt.Errorf("%s bots[0].home_binding: %w", source, err)
	}
	return volume, nil
}
