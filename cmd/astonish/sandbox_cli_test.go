package astonish

import (
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/sandbox"
)

func TestParseSandboxCpSource(t *testing.T) {
	id, path, err := parseSandboxCpSource("ff5c1146:/tmp/video.mp4")
	if err != nil {
		t.Fatalf("parseSandboxCpSource: %v", err)
	}
	if id != "ff5c1146" || path != "/tmp/video.mp4" {
		t.Fatalf("got %q %q", id, path)
	}
	if _, _, err := parseSandboxCpSource("nocolon"); err == nil {
		t.Fatal("expected error for missing colon")
	}
	if _, _, err := parseSandboxCpSource("id:"); err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestOverlayLayerChain(t *testing.T) {
	tests := []struct {
		name    string
		basedOn map[string]string
		want    string
	}{
		{name: "", want: sandbox.BaseTemplateID},
		{name: "base", want: sandbox.BaseTemplateID},
		{name: sandbox.BaseTemplateID, want: sandbox.BaseTemplateID},
		{name: "web", basedOn: nil, want: sandbox.BaseTemplateID + ",web"},
		{name: "child", basedOn: map[string]string{"child": "web", "web": sandbox.BaseTemplateID}, want: sandbox.BaseTemplateID + ",web,child"},
		{name: "loop", basedOn: map[string]string{"loop": "loop"}, want: sandbox.BaseTemplateID + ",loop"},
	}
	for _, tt := range tests {
		got := overlayLayerChain(tt.name, tt.basedOn)
		if strings.Join(got, ",") != tt.want {
			t.Errorf("overlayLayerChain(%q) = %v, want %s", tt.name, got, tt.want)
		}
	}
}

func TestIsBaseTemplateName(t *testing.T) {
	if !isBaseTemplateName("base") || !isBaseTemplateName("@base") || !isBaseTemplateName("") {
		t.Fatal("expected base names to match")
	}
	if isBaseTemplateName("web") {
		t.Fatal("web is not a base name")
	}
}

func TestTemplateSessionID(t *testing.T) {
	if got := templateSessionID("web"); got != "template-web" {
		t.Errorf("templateSessionID(web) = %q", got)
	}
	if got := templateSessionID("@base"); got != "template-base" {
		t.Errorf("templateSessionID(@base) = %q", got)
	}
}
