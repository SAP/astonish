package pptxworker

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/docs/slides/themes"
)

// buildPatternPPTX is a Title-Only layout plus a sample slide that draws three
// filled roundRect cards (the GCO-class "designed extras live on the sample,
// not the layout" case). Returns base64.
func buildPatternPPTX(t *testing.T) string {
	t.Helper()

	theme := classificationTheme
	master := classificationMaster
	masterRels := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideLayout" Target="../slideLayouts/slideLayout1.xml"/>
<Relationship Id="rId6" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/theme" Target="../theme/theme1.xml"/>
</Relationships>`

	// Title Only: a single title placeholder, no body, inherits master chrome.
	titleOnly := layoutXML("Title Only", "",
		phSp(2, "Title 1", "title", "", 288000, 400000, 11000000, 900000, "", "Click to add title"))

	// Three roundRect cards with text, sitting on the sample (not the layout).
	card := func(id int, name, fill, text string, x, y, cx, cy int) string {
		return `<p:sp>
<p:nvSpPr><p:cNvPr id="` + itoa(id) + `" name="` + name + `"/><p:cNvSpPr/><p:nvPr/></p:nvSpPr>
<p:spPr><a:xfrm><a:off x="` + itoa(x) + `" y="` + itoa(y) + `"/><a:ext cx="` + itoa(cx) + `" cy="` + itoa(cy) + `"/></a:xfrm>
<a:prstGeom prst="roundRect"><a:avLst/></a:prstGeom>
<a:solidFill><a:srgbClr val="` + fill + `"/></a:solidFill></p:spPr>
<p:txBody><a:bodyPr/><a:p><a:r><a:rPr lang="en-US" sz="2800" b="1"/><a:t>` + text + `</a:t></a:r></a:p></p:txBody>
</p:sp>`
	}
	draft := `<p:sp>
<p:nvSpPr><p:cNvPr id="6" name="DRAFT"/><p:cNvSpPr/><p:nvPr/></p:nvSpPr>
<p:spPr><a:xfrm><a:off x="2000000" y="2500000"/><a:ext cx="8000000" cy="1800000"/></a:xfrm>
<a:prstGeom prst="rect"><a:avLst/></a:prstGeom></p:spPr>
<p:txBody><a:bodyPr/><a:p><a:r><a:rPr lang="en-US" sz="9600" b="1"/><a:t>DRAFT</a:t></a:r></a:p></p:txBody>
</p:sp>`
	dummy := `<p:sp>
<p:nvSpPr><p:cNvPr id="10" name="Dummy"/><p:cNvSpPr/><p:nvPr/></p:nvSpPr>
<p:spPr><a:xfrm><a:off x="9000000" y="800000"/><a:ext cx="1900000" cy="260000"/></a:xfrm>
<a:prstGeom prst="rect"><a:avLst/></a:prstGeom></p:spPr>
<p:txBody><a:bodyPr/><a:p><a:r><a:rPr lang="en-US" sz="1200"/><a:t>Dummy data</a:t></a:r></a:p></p:txBody>
</p:sp>`
	dateTok := `<p:sp>
<p:nvSpPr><p:cNvPr id="7" name="Date"/><p:cNvSpPr/><p:nvPr/></p:nvSpPr>
<p:spPr><a:xfrm><a:off x="8000000" y="200000"/><a:ext cx="3500000" cy="400000"/></a:xfrm>
<a:prstGeom prst="rect"><a:avLst/></a:prstGeom></p:spPr>
<p:txBody><a:bodyPr/><a:p><a:r><a:rPr lang="en-US" sz="1200"/><a:t>&lt;initials&gt; &lt;date&gt;yyyy-MM-dd&lt;/date&gt;:</a:t></a:r></a:p></p:txBody>
</p:sp>`
	pie := func(id, x, y string) string {
		return `<p:sp>
<p:nvSpPr><p:cNvPr id="` + id + `" name="Pie"/><p:cNvSpPr/><p:nvPr/></p:nvSpPr>
<p:spPr><a:xfrm><a:off x="` + x + `" y="` + y + `"/><a:ext cx="1800000" cy="1800000"/></a:xfrm>
<a:prstGeom prst="ellipse"><a:avLst/></a:prstGeom>
<a:solidFill><a:srgbClr val="E76500"/></a:solidFill></p:spPr></p:sp>`
	}
	sample := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:sld xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main">
<p:cSld><p:spTree>
<p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr>
<p:grpSpPr/>
` + phSp(2, "Title 1", "title", "", 288000, 400000, 11000000, 900000, "", "Our three pillars") + `
` + card(3, "Card 1", "DBEAFE", "Card one", 500000, 2000000, 3300000, 2500000) + `
` + card(4, "Card 2", "FEF3C7", "Card two", 4300000, 2000000, 3300000, 2500000) + `
` + card(5, "Card 3", "DCFCE7", "Card three", 8100000, 2000000, 3300000, 2500000) + `
` + draft + dateTok + dummy + pie("8", "2000000", "4500000") + pie("9", "4500000", "4500000") + `
</p:spTree></p:cSld>
</p:sld>`

	files := map[string]string{
		"[Content_Types].xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
<Default Extension="xml" ContentType="application/xml"/>
<Override PartName="/ppt/presentation.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.presentation.main+xml"/>
<Override PartName="/ppt/slideMasters/slideMaster1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slideMaster+xml"/>
<Override PartName="/ppt/slideLayouts/slideLayout1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slideLayout+xml"/>
<Override PartName="/ppt/slides/slide1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slide+xml"/>
<Override PartName="/ppt/theme/theme1.xml" ContentType="application/vnd.openxmlformats-officedocument.theme+xml"/>
</Types>`,
		"_rels/.rels": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="ppt/presentation.xml"/>
</Relationships>`,
		"ppt/presentation.xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:presentation xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main">
<p:sldMasterIdLst><p:sldMasterId id="2147483648" r:id="rId1"/></p:sldMasterIdLst>
<p:sldIdLst><p:sldId id="256" r:id="rId2"/></p:sldIdLst>
<p:sldSz cx="12192000" cy="6858000"/>
</p:presentation>`,
		"ppt/_rels/presentation.xml.rels": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideMaster" Target="slideMasters/slideMaster1.xml"/>
<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide1.xml"/>
<Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/theme" Target="theme/theme1.xml"/>
</Relationships>`,
		"ppt/theme/theme1.xml":                         theme,
		"ppt/slideMasters/slideMaster1.xml":            master,
		"ppt/slideMasters/_rels/slideMaster1.xml.rels": masterRels,
		"ppt/slideLayouts/slideLayout1.xml":            titleOnly,
		"ppt/slideLayouts/_rels/slideLayout1.xml.rels": layoutRelsToMaster,
		"ppt/slides/slide1.xml":                        sample,
		"ppt/slides/_rels/slide1.xml.rels": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideLayout" Target="../slideLayouts/slideLayout1.xml"/>
</Relationships>`,
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func importPatternFixture(t *testing.T) themes.Template {
	t.Helper()
	workingDir, importScript, _ := requireNodeEnv(t)
	resp, err := (ImportRunner{WorkingDir: workingDir, ScriptPath: importScript, Timeout: 30 * time.Second}).
		Run(context.Background(), ImportRequest{PPTXBase64: buildPatternPPTX(t), Mode: "template"})
	if err != nil {
		t.Fatalf("import worker failed: %v", err)
	}
	var tmpl themes.Template
	if err := json.Unmarshal(resp.SceneOrTemplate, &tmpl); err != nil {
		t.Fatalf("bad template: %v\n%s", err, string(resp.SceneOrTemplate))
	}
	return tmpl
}

func findArchByKindPrefix(tmpl themes.Template, prefix string) *themes.Archetype {
	for i := range tmpl.Archetypes {
		if strings.HasPrefix(tmpl.Archetypes[i].Kind, prefix) {
			return &tmpl.Archetypes[i]
		}
	}
	return nil
}

func findArchByTitle(tmpl themes.Template, sub string) *themes.Archetype {
	for i := range tmpl.Archetypes {
		if strings.Contains(tmpl.Archetypes[i].Title, sub) {
			return &tmpl.Archetypes[i]
		}
	}
	return nil
}

// TestImportPromotesSampleCardsToPattern asserts that a Title Only layout
// plus a sample slide with 3 roundRect cards becomes a fillable pattern
// archetype that keeps the cards and does not inject a generic body box.
func TestImportPromotesSampleCardsToPattern(t *testing.T) {
	tmpl := importPatternFixture(t)

	pattern := findArchByKindPrefix(tmpl, "pattern")
	if pattern == nil {
		kinds := make([]string, 0, len(tmpl.Archetypes))
		for _, a := range tmpl.Archetypes {
			kinds = append(kinds, a.Kind+"/"+a.Title)
		}
		t.Fatalf("expected a pattern-* archetype from the sample cards; got %v", kinds)
	}
	if pattern.Tier != "flexible" {
		t.Fatalf("pattern tier=%q, want flexible", pattern.Tier)
	}
	rr := strings.Count(pattern.Markup, `geom="roundRect"`)
	if rr < 3 {
		t.Fatalf("pattern should keep 3 roundRect cards, found %d:\n%s", rr, pattern.Markup)
	}
	if strings.Contains(pattern.Markup, `id="ph-body"`) {
		t.Fatalf("pattern must not inject a generic ph-body; markup:\n%s", pattern.Markup)
	}
	// Title placeholder + 3 card texts.
	if len(pattern.FillSlots) < 4 {
		t.Fatalf("pattern fillSlots want >=4 (title+3 cards), got %v", pattern.FillSlots)
	}
	for _, id := range pattern.FillSlots {
		if !strings.Contains(pattern.Markup, `id="`+id+`"`) {
			t.Fatalf("fillSlot %q missing from pattern markup", id)
		}
	}
	if len(pattern.SlotHints) != len(pattern.FillSlots) {
		t.Fatalf("slotHints (%d) must match fillSlots (%d)", len(pattern.SlotHints), len(pattern.FillSlots))
	}
	// Master accent bar (#E76500) must survive onto the pattern.
	if !strings.Contains(pattern.Markup, "E76500") && !strings.Contains(pattern.Markup, "e76500") {
		t.Fatalf("pattern lost master chrome accent; markup:\n%s", pattern.Markup)
	}
	// Sample copy must have been replaced with a slot prompt, not left as "Card one".
	if strings.Contains(pattern.Markup, "Card one") {
		t.Fatalf("pattern still contains sample copy %q; should be a fill slot", "Card one")
	}
	for _, junk := range []string{"DRAFT", "yyyy-MM-dd", "initials", `geom="ellipse"`, "Dummy data"} {
		if strings.Contains(pattern.Markup, junk) {
			t.Fatalf("pattern kept sample junk %q:\n%s", junk, pattern.Markup)
		}
	}

	titleOnly := findArchByTitle(tmpl, "Title Only")
	if titleOnly == nil {
		t.Fatal("expected the Title Only layout archetype to still exist")
	}
	if strings.Contains(titleOnly.Markup, `id="ph-body"`) {
		t.Fatalf("Title Only layout must not inject a generic ph-body:\n%s", titleOnly.Markup)
	}

	// Chrome roles remain guaranteed; none of them is a leftover example-* kind.
	base := map[string]bool{}
	for _, a := range tmpl.Archetypes {
		if strings.HasPrefix(a.Kind, "example") {
			t.Fatalf("unexpected example-* archetype %q", a.Kind)
		}
		base[stripVariantSuffix(a.Kind)] = true
	}
	for _, want := range []string{"title", "section", "agenda", "closing", "pattern"} {
		if !base[want] {
			t.Fatalf("missing guaranteed/pattern role %q; bases=%v", want, base)
		}
	}
}

// TestImportGCOHasDesignedPatterns is a skip-if-missing smoke against the local
// GCO corporate template: at least one pattern carries a roundRect (the sample
// cards), and example-* kinds stay gone.
func TestImportGCOHasDesignedPatterns(t *testing.T) {
	workingDir, importScript, _ := requireNodeEnv(t)
	ref := "/Users/I851355/Downloads/2026 GCO IPED PPT TEMPLATE.pptx"
	raw, err := os.ReadFile(ref)
	if err != nil {
		t.Skipf("reference template not present (%v)", err)
	}
	resp, err := (ImportRunner{WorkingDir: workingDir, ScriptPath: importScript, Timeout: 90 * time.Second}).
		Run(context.Background(), ImportRequest{PPTXBase64: base64.StdEncoding.EncodeToString(raw), Mode: "template"})
	if err != nil {
		t.Fatalf("import worker failed: %v", err)
	}
	var tmpl themes.Template
	if err := json.Unmarshal(resp.SceneOrTemplate, &tmpl); err != nil {
		t.Fatalf("bad template: %v", err)
	}
	patterns := 0
	withCards := 0
	for _, a := range tmpl.Archetypes {
		if strings.HasPrefix(a.Kind, "example") {
			t.Fatalf("unexpected example-* archetype %q", a.Kind)
		}
		if strings.HasPrefix(stripVariantSuffix(a.Kind), "pattern") {
			patterns++
			if strings.Contains(a.Markup, `geom="roundRect"`) {
				withCards++
			}
		}
	}
	if patterns == 0 {
		t.Fatal("GCO import produced no pattern-* archetypes from sample slides")
	}
	if withCards == 0 {
		t.Fatalf("GCO produced %d patterns but none contain roundRect cards", patterns)
	}
	for _, a := range tmpl.Archetypes {
		for _, junk := range []string{"DRAFT", "Dummy data", "For discussion", "yyyy-MM-dd", "Harvey"} {
			if strings.Contains(a.Markup, junk) {
				t.Errorf("archetype %s/%s kept master junk %q", a.Kind, a.Title, junk)
			}
		}
		if n := strings.Count(a.Markup, "<ast-shape"); n > 80 {
			t.Errorf("archetype %s/%s has %d shapes — hidden Harvey palette probably leaked", a.Kind, a.Title, n)
		}
		if strings.HasPrefix(stripVariantSuffix(a.Kind), "pattern") && len(a.FillSlots) > 16 {
			t.Errorf("archetype %s/%s has %d fill slots — widget sheet, not a body pattern", a.Kind, a.Title, len(a.FillSlots))
		}
	}
	var title, section *themes.Archetype
	for i := range tmpl.Archetypes {
		switch tmpl.Archetypes[i].Kind {
		case "title":
			title = &tmpl.Archetypes[i]
		case "section":
			section = &tmpl.Archetypes[i]
		}
	}
	if title == nil {
		t.Fatal("expected unsuffixed title archetype")
	}
	if !strings.Contains(title.Markup, `asset-ref=`) {
		if strings.Contains(strings.ToUpper(title.Markup), "89D1FF") {
			t.Errorf("unsuffixed title %q has a muted empty-pic slab:\n%s", title.Title, title.Markup)
		}
	}
	if section != nil {
		richerHasPic := false
		for _, a := range tmpl.Archetypes {
			if stripVariantSuffix(a.Kind) == "section" && strings.Contains(a.Markup, "<ast-image") {
				richerHasPic = true
			}
		}
		if richerHasPic && !strings.Contains(section.Markup, "<ast-image") {
			t.Errorf("unsuffixed section %q has no image but another section variant does", section.Title)
		}
	}
	for _, a := range tmpl.Archetypes {
		if stripVariantSuffix(a.Kind) == "closing" && strings.Contains(a.Markup, "Contact information:") {
			t.Errorf("closing %s/%s still has frozen Contact information chrome:\n%s", a.Kind, a.Title, a.Markup)
		}
	}
}

func zipPPTX(t *testing.T, files map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func minimalTemplateFiles(slideXML, layoutXMLBody, master string) map[string]string {
	return map[string]string{
		"[Content_Types].xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
<Default Extension="xml" ContentType="application/xml"/>
<Override PartName="/ppt/presentation.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.presentation.main+xml"/>
<Override PartName="/ppt/slideMasters/slideMaster1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slideMaster+xml"/>
<Override PartName="/ppt/slideLayouts/slideLayout1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slideLayout+xml"/>
<Override PartName="/ppt/slides/slide1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slide+xml"/>
<Override PartName="/ppt/theme/theme1.xml" ContentType="application/vnd.openxmlformats-officedocument.theme+xml"/>
</Types>`,
		"_rels/.rels": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="ppt/presentation.xml"/>
</Relationships>`,
		"ppt/presentation.xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:presentation xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main">
<p:sldMasterIdLst><p:sldMasterId id="2147483648" r:id="rId1"/></p:sldMasterIdLst>
<p:sldIdLst><p:sldId id="256" r:id="rId2"/></p:sldIdLst>
<p:sldSz cx="12192000" cy="6858000"/>
</p:presentation>`,
		"ppt/_rels/presentation.xml.rels": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideMaster" Target="slideMasters/slideMaster1.xml"/>
<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide1.xml"/>
<Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/theme" Target="theme/theme1.xml"/>
</Relationships>`,
		"ppt/theme/theme1.xml":              classificationTheme,
		"ppt/slideMasters/slideMaster1.xml": master,
		"ppt/slideMasters/_rels/slideMaster1.xml.rels": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideLayout" Target="../slideLayouts/slideLayout1.xml"/>
<Relationship Id="rId6" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/theme" Target="../theme/theme1.xml"/>
</Relationships>`,
		"ppt/slideLayouts/slideLayout1.xml":            layoutXMLBody,
		"ppt/slideLayouts/_rels/slideLayout1.xml.rels": layoutRelsToMaster,
		"ppt/slides/slide1.xml":                        slideXML,
		"ppt/slides/_rels/slide1.xml.rels": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideLayout" Target="../slideLayouts/slideLayout1.xml"/>
</Relationships>`,
	}
}

func TestImportCardGridReadingOrder(t *testing.T) {
	workingDir, importScript, _ := requireNodeEnv(t)
	titleOnly := layoutXML("Title Only", "",
		phSp(2, "Title 1", "title", "", 288000, 400000, 11000000, 900000, "", "Click to add title"))
	card := func(id int, name, fill, text string, x, y int) string {
		return `<p:sp>
<p:nvSpPr><p:cNvPr id="` + itoa(id) + `" name="` + name + `"/><p:cNvSpPr/><p:nvPr/></p:nvSpPr>
<p:spPr><a:xfrm><a:off x="` + itoa(x) + `" y="` + itoa(y) + `"/><a:ext cx="3300000" cy="1800000"/></a:xfrm>
<a:prstGeom prst="roundRect"><a:avLst/></a:prstGeom>
<a:solidFill><a:srgbClr val="` + fill + `"/></a:solidFill></p:spPr>
<p:txBody><a:bodyPr/><a:p><a:r><a:rPr lang="en-US" sz="2000" b="1"/><a:t>` + text + `</a:t></a:r></a:p></p:txBody>
</p:sp>`
	}
	kicker := `<p:sp>
<p:nvSpPr><p:cNvPr id="20" name="Kicker"/><p:cNvSpPr/><p:nvPr/></p:nvSpPr>
<p:spPr><a:xfrm><a:off x="500000" y="1400000"/><a:ext cx="11000000" cy="400000"/></a:xfrm>
<a:prstGeom prst="rect"><a:avLst/></a:prstGeom></p:spPr>
<p:txBody><a:bodyPr/><a:p><a:r><a:rPr lang="en-US" sz="1600"/><a:t>One-line kicker above the cards</a:t></a:r></a:p></p:txBody>
</p:sp>`
	sample := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:sld xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main">
<p:cSld><p:spTree>
<p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr>
<p:grpSpPr/>
` + phSp(2, "Title 1", "title", "", 288000, 400000, 11000000, 900000, "", "Products that changed everything") + `
` + card(10, "C1", "003FC9", "Card A", 500000, 2000000) + `
` + card(11, "C2", "003FC9", "Card B", 4300000, 2000000) + `
` + card(12, "C3", "003FC9", "Card C", 8100000, 2000000) + `
` + card(13, "C4", "1B90FF", "Card D", 500000, 4000000) + `
` + card(14, "C5", "1B90FF", "Card E", 4300000, 4000000) + `
` + card(15, "C6", "1B90FF", "Card F", 8100000, 4000000) + `
` + kicker + `
</p:spTree></p:cSld>
</p:sld>`
	resp, err := (ImportRunner{WorkingDir: workingDir, ScriptPath: importScript, Timeout: 30 * time.Second}).
		Run(context.Background(), ImportRequest{PPTXBase64: zipPPTX(t, minimalTemplateFiles(sample, titleOnly, classificationMaster)), Mode: "template"})
	if err != nil {
		t.Fatalf("import worker failed: %v", err)
	}
	var tmpl themes.Template
	if err := json.Unmarshal(resp.SceneOrTemplate, &tmpl); err != nil {
		t.Fatalf("bad template: %v", err)
	}
	pattern := findArchByKindPrefix(tmpl, "pattern")
	if pattern == nil {
		t.Fatal("expected a pattern-* archetype")
	}
	if len(pattern.SlotHints) == 0 {
		t.Fatal("pattern has no slotHints")
	}
	if pattern.SlotHints[0].Role != "title" && !strings.Contains(pattern.SlotHints[0].Hint, "Slide title") {
		t.Errorf("ph-1 should be the title in reading order, got %+v", pattern.SlotHints[0])
	}
	cardHints := 0
	kickerHints := 0
	for _, h := range pattern.SlotHints {
		if strings.Contains(h.Hint, "card ") && strings.Contains(h.Hint, "of 6") {
			cardHints++
		}
		if strings.Contains(h.Hint, "subtitle") || h.Role == "kicker" {
			kickerHints++
		}
	}
	if cardHints < 6 {
		t.Errorf("want 6 card-N-of-6 hints, got %d in %+v", cardHints, pattern.SlotHints)
	}
	if kickerHints < 1 {
		t.Errorf("want a kicker/subtitle hint, got %+v", pattern.SlotHints)
	}
	for _, junk := range []string{"Card A", "Card B", "One-line kicker"} {
		if strings.Contains(pattern.Markup, junk) {
			t.Errorf("pattern still contains sample copy %q", junk)
		}
	}
}

func TestImportPullsSideLabelsIntoCards(t *testing.T) {
	workingDir, importScript, _ := requireNodeEnv(t)
	titleOnly := layoutXML("Title Only", "",
		phSp(2, "Title 1", "title", "", 288000, 400000, 11000000, 900000, "", "Click to add title"))
	bar := func(id int, fill string, x, y int) string {
		return `<p:sp>
<p:nvSpPr><p:cNvPr id="` + itoa(id) + `" name="Bar"/><p:cNvSpPr/><p:nvPr/></p:nvSpPr>
<p:spPr><a:xfrm><a:off x="` + itoa(x) + `" y="` + itoa(y) + `"/><a:ext cx="6000000" cy="1200000"/></a:xfrm>
<a:prstGeom prst="roundRect"><a:avLst/></a:prstGeom>
<a:solidFill><a:srgbClr val="` + fill + `"/></a:solidFill></p:spPr></p:sp>`
	}
	label := func(id int, text string, x, y int) string {
		return `<p:sp>
<p:nvSpPr><p:cNvPr id="` + itoa(id) + `" name="Label"/><p:cNvSpPr/><p:nvPr/></p:nvSpPr>
<p:spPr><a:xfrm><a:off x="` + itoa(x) + `" y="` + itoa(y) + `"/><a:ext cx="4000000" cy="800000"/></a:xfrm>
<a:prstGeom prst="rect"><a:avLst/></a:prstGeom></p:spPr>
<p:txBody><a:bodyPr/><a:p><a:r><a:rPr lang="en-US" sz="1800"/><a:t>` + text + `</a:t></a:r></a:p></p:txBody>
</p:sp>`
	}
	sample := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:sld xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main">
<p:cSld><p:spTree>
<p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr>
<p:grpSpPr/>
` + phSp(2, "Title 1", "title", "", 288000, 400000, 11000000, 900000, "", "Milestones") + `
` + bar(10, "D1EFFF", 400000, 1800000) + label(11, "Label A", 6700000, 1850000) + `
` + bar(12, "D1EFFF", 400000, 3200000) + label(13, "Label B", 6700000, 3250000) + `
` + bar(14, "A6E0FF", 400000, 4600000) + label(15, "Label C", 6700000, 4650000) + `
</p:spTree></p:cSld>
</p:sld>`
	resp, err := (ImportRunner{WorkingDir: workingDir, ScriptPath: importScript, Timeout: 30 * time.Second}).
		Run(context.Background(), ImportRequest{PPTXBase64: zipPPTX(t, minimalTemplateFiles(sample, titleOnly, classificationMaster)), Mode: "template"})
	if err != nil {
		t.Fatalf("import worker failed: %v", err)
	}
	var tmpl themes.Template
	if err := json.Unmarshal(resp.SceneOrTemplate, &tmpl); err != nil {
		t.Fatalf("bad template: %v", err)
	}
	pattern := findArchByKindPrefix(tmpl, "pattern")
	if pattern == nil {
		t.Fatal("expected a pattern")
	}
	// Labels must be slotted inside the bars (left half), not left sitting to
	// the right of empty rounded rects.
	for _, id := range []string{"ph-2", "ph-3", "ph-4"} {
		re := regexp.MustCompile(`<ast-text id="` + id + `" x="(\d+)"`)
		m := re.FindStringSubmatch(pattern.Markup)
		if m == nil {
			// title is ph-1; cards may start at ph-2. Try any body slot.
			continue
		}
		x, _ := strconv.Atoi(m[1])
		if x > 900 {
			t.Errorf("slot %s x=%d is still to the right of the cards:\n%s", id, x, pattern.Markup)
		}
	}
	bodyLeft := 0
	re := regexp.MustCompile(`<ast-text id="ph-\d+" x="(\d+)"`)
	for _, m := range re.FindAllStringSubmatch(pattern.Markup, -1) {
		x, _ := strconv.Atoi(m[1])
		if x < 900 && x > 0 {
			bodyLeft++
		}
	}
	if bodyLeft < 3 {
		t.Fatalf("expected >=3 text slots inside the left-hand bars, markup:\n%s", pattern.Markup)
	}
	if !strings.Contains(pattern.Markup, `align="ctr"`) || !strings.Contains(pattern.Markup, `anchor="ctr"`) {
		t.Errorf("card slots should be center-aligned, markup:\n%s", pattern.Markup)
	}
}

func TestImportPatternInheritsLayoutTitle(t *testing.T) {
	workingDir, importScript, _ := requireNodeEnv(t)
	titleOnly := layoutXML("Title Only", "",
		phSp(2, "Title 1", "title", "", 288000, 400000, 11000000, 900000, "", "Click to add title"))
	card := func(id int, fill, text string, x, y int) string {
		return `<p:sp>
<p:nvSpPr><p:cNvPr id="` + itoa(id) + `" name="Card"/><p:cNvSpPr/><p:nvPr/></p:nvSpPr>
<p:spPr><a:xfrm><a:off x="` + itoa(x) + `" y="` + itoa(y) + `"/><a:ext cx="3300000" cy="1800000"/></a:xfrm>
<a:prstGeom prst="roundRect"><a:avLst/></a:prstGeom>
<a:solidFill><a:srgbClr val="` + fill + `"/></a:solidFill></p:spPr>
<p:txBody><a:bodyPr/><a:p><a:r><a:rPr lang="en-US" sz="2000"/><a:t>` + text + `</a:t></a:r></a:p></p:txBody>
</p:sp>`
	}
	// Sample has cards only — no title shape. The Title Only layout still has a
	// title placeholder that must become a fill slot so body slides aren't untitled.
	sample := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:sld xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main">
<p:cSld><p:spTree>
<p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr>
<p:grpSpPr/>
` + card(3, "DBEAFE", "One", 500000, 2000000) + `
` + card(4, "FEF3C7", "Two", 4300000, 2000000) + `
` + card(5, "DCFCE7", "Three", 8100000, 2000000) + `
</p:spTree></p:cSld>
</p:sld>`
	resp, err := (ImportRunner{WorkingDir: workingDir, ScriptPath: importScript, Timeout: 30 * time.Second}).
		Run(context.Background(), ImportRequest{PPTXBase64: zipPPTX(t, minimalTemplateFiles(sample, titleOnly, classificationMaster)), Mode: "template"})
	if err != nil {
		t.Fatalf("import worker failed: %v", err)
	}
	var tmpl themes.Template
	if err := json.Unmarshal(resp.SceneOrTemplate, &tmpl); err != nil {
		t.Fatalf("bad template: %v", err)
	}
	pattern := findArchByKindPrefix(tmpl, "pattern")
	if pattern == nil {
		t.Fatal("expected a pattern")
	}
	hasTitle := false
	for _, h := range pattern.SlotHints {
		if h.Role == "title" || strings.Contains(h.Hint, "Slide title") {
			hasTitle = true
		}
	}
	if !hasTitle {
		t.Fatalf("pattern must inherit the layout title placeholder, hints=%+v\n%s", pattern.SlotHints, pattern.Markup)
	}
}

func TestImportEmptyCardsGetFillSlots(t *testing.T) {
	workingDir, importScript, _ := requireNodeEnv(t)
	titleOnly := layoutXML("Title Only", "",
		phSp(2, "Title 1", "title", "", 288000, 400000, 11000000, 900000, "", "Click to add title"))
	emptyCard := func(id int, fill string, x, y int) string {
		return `<p:sp>
<p:nvSpPr><p:cNvPr id="` + itoa(id) + `" name="Card"/><p:cNvSpPr/><p:nvPr/></p:nvSpPr>
<p:spPr><a:xfrm><a:off x="` + itoa(x) + `" y="` + itoa(y) + `"/><a:ext cx="4000000" cy="2200000"/></a:xfrm>
<a:prstGeom prst="roundRect"><a:avLst/></a:prstGeom>
<a:solidFill><a:srgbClr val="` + fill + `"/></a:solidFill></p:spPr>
<p:txBody><a:bodyPr/><a:p/></p:txBody>
</p:sp>`
	}
	filledCard := func(id int, fill, text string, x, y int) string {
		return `<p:sp>
<p:nvSpPr><p:cNvPr id="` + itoa(id) + `" name="Card"/><p:cNvSpPr/><p:nvPr/></p:nvSpPr>
<p:spPr><a:xfrm><a:off x="` + itoa(x) + `" y="` + itoa(y) + `"/><a:ext cx="4000000" cy="2200000"/></a:xfrm>
<a:prstGeom prst="roundRect"><a:avLst/></a:prstGeom>
<a:solidFill><a:srgbClr val="` + fill + `"/></a:solidFill></p:spPr>
<p:txBody><a:bodyPr/><a:p><a:r><a:rPr lang="en-US" sz="2000"/><a:t>` + text + `</a:t></a:r></a:p></p:txBody>
</p:sp>`
	}
	sample := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:sld xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main">
<p:cSld><p:spTree>
<p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr>
<p:grpSpPr/>
` + filledCard(10, "DBEAFE", "Only this card has copy", 400000, 1800000) + `
` + emptyCard(11, "D1EFFF", 4800000, 1800000) + `
` + emptyCard(12, "A6E0FF", 400000, 4300000) + `
` + emptyCard(13, "89D1FF", 4800000, 4300000) + `
</p:spTree></p:cSld>
</p:sld>`
	resp, err := (ImportRunner{WorkingDir: workingDir, ScriptPath: importScript, Timeout: 30 * time.Second}).
		Run(context.Background(), ImportRequest{PPTXBase64: zipPPTX(t, minimalTemplateFiles(sample, titleOnly, classificationMaster)), Mode: "template"})
	if err != nil {
		t.Fatalf("import worker failed: %v", err)
	}
	var tmpl themes.Template
	if err := json.Unmarshal(resp.SceneOrTemplate, &tmpl); err != nil {
		t.Fatalf("bad template: %v", err)
	}
	pattern := findArchByKindPrefix(tmpl, "pattern")
	if pattern == nil {
		t.Fatal("expected a pattern")
	}
	textSlots := 0
	for _, id := range pattern.FillSlots {
		if strings.HasPrefix(id, "ph-") && !strings.HasPrefix(id, "ph-pic-") {
			textSlots++
		}
	}
	if textSlots < 5 {
		t.Fatalf("want title + 4 card slots, got %d fillSlots=%v\n%s", textSlots, pattern.FillSlots, pattern.Markup)
	}
	if strings.Count(pattern.Markup, `geom="roundRect"`) < 4 {
		t.Fatalf("empty cards must stay as chrome:\n%s", pattern.Markup)
	}
}

func TestImportIconRowCaptionsSitBelowMarkers(t *testing.T) {
	workingDir, importScript, _ := requireNodeEnv(t)
	titleOnly := layoutXML("Title Only", "",
		phSp(2, "Title 1", "title", "", 288000, 400000, 11000000, 900000, "", "Click to add title"))
	ellipse := func(id, x, y int) string {
		return `<p:sp>
<p:nvSpPr><p:cNvPr id="` + itoa(id) + `" name="Icon"/><p:cNvSpPr/><p:nvPr/></p:nvSpPr>
<p:spPr><a:xfrm><a:off x="` + itoa(x) + `" y="` + itoa(y) + `"/><a:ext cx="800000" cy="800000"/></a:xfrm>
<a:prstGeom prst="ellipse"><a:avLst/></a:prstGeom>
<a:solidFill><a:srgbClr val="0070F2"/></a:solidFill></p:spPr></p:sp>`
	}
	caption := func(id int, text string, x, y int) string {
		return `<p:sp>
<p:nvSpPr><p:cNvPr id="` + itoa(id) + `" name="Cap"/><p:cNvSpPr/><p:nvPr/></p:nvSpPr>
<p:spPr><a:xfrm><a:off x="` + itoa(x) + `" y="` + itoa(y) + `"/><a:ext cx="1800000" cy="900000"/></a:xfrm>
<a:prstGeom prst="rect"><a:avLst/></a:prstGeom></p:spPr>
<p:txBody><a:bodyPr/><a:p><a:r><a:rPr lang="en-US" sz="1400"/><a:t>` + text + `</a:t></a:r></a:p></p:txBody>
</p:sp>`
	}
	dot := func(id, x, y int) string {
		return `<p:sp>
<p:nvSpPr><p:cNvPr id="` + itoa(id) + `" name="Dot"/><p:cNvSpPr/><p:nvPr/></p:nvSpPr>
<p:spPr><a:xfrm><a:off x="` + itoa(x) + `" y="` + itoa(y) + `"/><a:ext cx="180000" cy="180000"/></a:xfrm>
<a:prstGeom prst="ellipse"><a:avLst/></a:prstGeom>
<a:solidFill><a:srgbClr val="0070F2"/></a:solidFill></p:spPr></p:sp>`
	}
	hairline := `<p:sp>
<p:nvSpPr><p:cNvPr id="30" name="Rule"/><p:cNvSpPr/><p:nvPr/></p:nvSpPr>
<p:spPr><a:xfrm><a:off x="400000" y="3100000"/><a:ext cx="11000000" cy="30000"/></a:xfrm>
<a:prstGeom prst="rect"><a:avLst/></a:prstGeom>
<a:solidFill><a:srgbClr val="0070F2"/></a:solidFill></p:spPr></p:sp>`
	// Five icon markers + timeline dots/rule. Dots must not become extra captions.
	sample := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:sld xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main">
<p:cSld><p:spTree>
<p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr>
<p:grpSpPr/>
` + ellipse(10, 400000, 2200000) + ellipse(11, 2800000, 2200000) + ellipse(12, 5200000, 2200000) + ellipse(13, 7600000, 2200000) + ellipse(14, 10000000, 2200000) + `
` + hairline + dot(31, 700000, 3020000) + dot(32, 3100000, 3020000) + dot(33, 5500000, 3020000) + `
` + caption(20, "One", 200000, 3300000) + caption(21, "Two", 2600000, 3300000) + caption(22, "Three", 5000000, 3300000) + caption(23, "Four", 7400000, 2400000) + `
</p:spTree></p:cSld>
</p:sld>`
	resp, err := (ImportRunner{WorkingDir: workingDir, ScriptPath: importScript, Timeout: 30 * time.Second}).
		Run(context.Background(), ImportRequest{PPTXBase64: zipPPTX(t, minimalTemplateFiles(sample, titleOnly, classificationMaster)), Mode: "template"})
	if err != nil {
		t.Fatalf("import worker failed: %v", err)
	}
	var tmpl themes.Template
	if err := json.Unmarshal(resp.SceneOrTemplate, &tmpl); err != nil {
		t.Fatalf("bad template: %v", err)
	}
	pattern := findArchByKindPrefix(tmpl, "pattern")
	if pattern == nil {
		t.Fatal("expected a pattern")
	}
	// Captions must sit below the markers (y=2200000 EMU ≈ 346px, h≈126 → bottom ~472)
	// AND below the timeline rule (~488px). Connector dots must not add extra slots.
	re := regexp.MustCompile(`<ast-text id="ph-\d+" x="(\d+)" y="(\d+)" w="(\d+)" h="(\d+)"[^>]*>`)
	markerBottom := 346 + 126
	ruleY := 3100000 / 6350 // ≈488
	captions := 0
	for _, m := range re.FindAllStringSubmatch(pattern.Markup, -1) {
		y, _ := strconv.Atoi(m[2])
		h, _ := strconv.Atoi(m[4])
		open := m[0]
		if y < 200 {
			continue // title
		}
		captions++
		if y < markerBottom {
			t.Errorf("caption y=%d overlaps markers (bottom ~%d):\n%s", y, markerBottom, pattern.Markup)
		}
		if y < ruleY+8 {
			t.Errorf("caption y=%d overlaps timeline rule (~%d):\n%s", y, ruleY, pattern.Markup)
		}
		if strings.Contains(open, `anchor="ctr"`) {
			t.Errorf("icon caption must top-align, not vertical-center:\n%s", open)
		}
		if h > 160 {
			t.Errorf("icon caption h=%d is too tall and will collide:\n%s", h, open)
		}
	}
	if captions > 5 {
		t.Fatalf("timeline dots must not become extra captions, got %d body slots\n%s", captions, pattern.Markup)
	}
}

// hiddenWidgetMaster is a GCO-class slide master: visible brand chrome plus the
// authoring palettes PowerPoint hides (Harvey-ball group, DRAFT/Dummy stickers,
// a unique lime bar used only to detect a hidden-flag miss).
const hiddenWidgetMaster = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:sldMaster xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main">
<p:cSld><p:bg><p:bgPr><a:solidFill><a:srgbClr val="FFFFFF"/></a:solidFill><a:effectLst/></p:bgPr></p:bg>
<p:spTree>
<p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr>
<p:grpSpPr/>
<p:sp><p:nvSpPr><p:cNvPr id="2" name="Accent Bar"/><p:cNvSpPr/><p:nvPr/></p:nvSpPr>
<p:spPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="200000" cy="6858000"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom><a:solidFill><a:srgbClr val="E76500"/></a:solidFill></p:spPr></p:sp>
<p:sp><p:nvSpPr><p:cNvPr id="3" name="Copyright Placeholder"/><p:cNvSpPr/><p:nvPr/></p:nvSpPr>
<p:spPr><a:xfrm><a:off x="504000" y="6536751"/><a:ext cx="10155600" cy="138498"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom></p:spPr>
<p:txBody><a:bodyPr/><a:p><a:r><a:rPr lang="en-US" sz="1000"/><a:t>INTERNAL – SAP and Partners Only</a:t></a:r></a:p></p:txBody></p:sp>
<p:sp><p:nvSpPr><p:cNvPr id="4" name="Draft [Sticker]" hidden="1"/><p:cNvSpPr/><p:nvPr/></p:nvSpPr>
<p:spPr><a:xfrm><a:off x="2000000" y="2500000"/><a:ext cx="8000000" cy="1800000"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom></p:spPr>
<p:txBody><a:bodyPr/><a:p><a:r><a:rPr lang="en-US" sz="9600" b="1"/><a:t>DRAFT</a:t></a:r></a:p></p:txBody></p:sp>
<p:sp><p:nvSpPr><p:cNvPr id="5" name="Dummy [Sticker]" hidden="1"/><p:cNvSpPr/><p:nvPr/></p:nvSpPr>
<p:spPr><a:xfrm><a:off x="9000000" y="800000"/><a:ext cx="1900000" cy="260000"/></a:xfrm>
<a:prstGeom prst="rect"><a:avLst/></a:prstGeom>
<a:solidFill><a:srgbClr val="7C5CFF"/></a:solidFill></p:spPr>
<p:txBody><a:bodyPr/><a:p><a:r><a:rPr lang="en-US" sz="1200"/><a:t>Dummy data</a:t></a:r></a:p></p:txBody></p:sp>
<p:sp><p:nvSpPr><p:cNvPr id="6" name="Hidden Lime Bar" hidden="1"/><p:cNvSpPr/><p:nvPr/></p:nvSpPr>
<p:spPr><a:xfrm><a:off x="0" y="6000000"/><a:ext cx="12192000" cy="400000"/></a:xfrm>
<a:prstGeom prst="rect"><a:avLst/></a:prstGeom>
<a:solidFill><a:srgbClr val="00FF00"/></a:solidFill></p:spPr></p:sp>
<p:grpSp>
<p:nvGrpSpPr><p:cNvPr id="20" name="Harvey 100" hidden="1"/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr>
<p:grpSpPr><a:xfrm>
<a:off x="8000000" y="3000000"/><a:ext cx="1000000" cy="1000000"/>
<a:chOff x="0" y="0"/><a:chExt cx="1000000" cy="1000000"/>
</a:xfrm></p:grpSpPr>
<p:sp><p:nvSpPr><p:cNvPr id="21" name="Harvey 0/5 [0]"/><p:cNvSpPr/><p:nvPr/></p:nvSpPr>
<p:spPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="1000000" cy="1000000"/></a:xfrm>
<a:prstGeom prst="rect"><a:avLst/></a:prstGeom>
<a:solidFill><a:schemeClr val="tx1"/></a:solidFill></p:spPr></p:sp>
<p:sp><p:nvSpPr><p:cNvPr id="22" name="Harvey 1/5 [1]"/><p:cNvSpPr/><p:nvPr/></p:nvSpPr>
<p:spPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="1000000" cy="1000000"/></a:xfrm>
<a:prstGeom prst="rect"><a:avLst/></a:prstGeom>
<a:solidFill><a:schemeClr val="tx1"/></a:solidFill></p:spPr></p:sp>
<p:sp><p:nvSpPr><p:cNvPr id="23" name="Harvey 2/5 [2]"/><p:cNvSpPr/><p:nvPr/></p:nvSpPr>
<p:spPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="1000000" cy="1000000"/></a:xfrm>
<a:prstGeom prst="rect"><a:avLst/></a:prstGeom>
<a:solidFill><a:schemeClr val="tx1"/></a:solidFill></p:spPr></p:sp>
<p:sp><p:nvSpPr><p:cNvPr id="24" name="Harvey 3/5 [3]"/><p:cNvSpPr/><p:nvPr/></p:nvSpPr>
<p:spPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="1000000" cy="1000000"/></a:xfrm>
<a:prstGeom prst="rect"><a:avLst/></a:prstGeom>
<a:solidFill><a:schemeClr val="tx1"/></a:solidFill></p:spPr></p:sp>
<p:sp><p:nvSpPr><p:cNvPr id="25" name="Harvey 4/5 [4]"/><p:cNvSpPr/><p:nvPr/></p:nvSpPr>
<p:spPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="1000000" cy="1000000"/></a:xfrm>
<a:prstGeom prst="rect"><a:avLst/></a:prstGeom>
<a:solidFill><a:schemeClr val="tx1"/></a:solidFill></p:spPr></p:sp>
</p:grpSp>
</p:spTree></p:cSld>
<p:clrMap bg1="lt1" tx1="dk1" bg2="lt2" tx2="dk2" accent1="accent1" accent2="accent2" accent3="accent3" accent4="accent4" accent5="accent5" accent6="accent6" hlink="hlink" folHlink="folHlink"/>
<p:sldLayoutIdLst>
<p:sldLayoutId id="2147483649" r:id="rId1"/>
</p:sldLayoutIdLst>
</p:sldMaster>`

func buildHiddenWidgetPPTX(t *testing.T) string {
	t.Helper()
	titleOnly := layoutXML("Title Only", "",
		phSp(2, "Title 1", "title", "", 288000, 400000, 11000000, 900000, "", "Click to add title"))
	card := func(id int, name, fill, text string, x, y, cx, cy int) string {
		return `<p:sp>
<p:nvSpPr><p:cNvPr id="` + itoa(id) + `" name="` + name + `"/><p:cNvSpPr/><p:nvPr/></p:nvSpPr>
<p:spPr><a:xfrm><a:off x="` + itoa(x) + `" y="` + itoa(y) + `"/><a:ext cx="` + itoa(cx) + `" cy="` + itoa(cy) + `"/></a:xfrm>
<a:prstGeom prst="roundRect"><a:avLst/></a:prstGeom>
<a:solidFill><a:srgbClr val="` + fill + `"/></a:solidFill></p:spPr>
<p:txBody><a:bodyPr/><a:p><a:r><a:rPr lang="en-US" sz="2800" b="1"/><a:t>` + text + `</a:t></a:r></a:p></p:txBody>
</p:sp>`
	}
	sample := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:sld xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main">
<p:cSld><p:spTree>
<p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr>
<p:grpSpPr/>
` + phSp(2, "Title 1", "title", "", 288000, 400000, 11000000, 900000, "", "Our three pillars") + `
` + card(3, "Card 1", "DBEAFE", "Card one", 500000, 2000000, 3300000, 2500000) + `
` + card(4, "Card 2", "FEF3C7", "Card two", 4300000, 2000000, 3300000, 2500000) + `
` + card(5, "Card 3", "DCFCE7", "Card three", 8100000, 2000000, 3300000, 2500000) + `
</p:spTree></p:cSld>
</p:sld>`

	files := map[string]string{
		"[Content_Types].xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
<Default Extension="xml" ContentType="application/xml"/>
<Override PartName="/ppt/presentation.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.presentation.main+xml"/>
<Override PartName="/ppt/slideMasters/slideMaster1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slideMaster+xml"/>
<Override PartName="/ppt/slideLayouts/slideLayout1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slideLayout+xml"/>
<Override PartName="/ppt/slides/slide1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slide+xml"/>
<Override PartName="/ppt/theme/theme1.xml" ContentType="application/vnd.openxmlformats-officedocument.theme+xml"/>
</Types>`,
		"_rels/.rels": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="ppt/presentation.xml"/>
</Relationships>`,
		"ppt/presentation.xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:presentation xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main">
<p:sldMasterIdLst><p:sldMasterId id="2147483648" r:id="rId1"/></p:sldMasterIdLst>
<p:sldIdLst><p:sldId id="256" r:id="rId2"/></p:sldIdLst>
<p:sldSz cx="12192000" cy="6858000"/>
</p:presentation>`,
		"ppt/_rels/presentation.xml.rels": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideMaster" Target="slideMasters/slideMaster1.xml"/>
<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide1.xml"/>
<Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/theme" Target="theme/theme1.xml"/>
</Relationships>`,
		"ppt/theme/theme1.xml":              classificationTheme,
		"ppt/slideMasters/slideMaster1.xml": hiddenWidgetMaster,
		"ppt/slideMasters/_rels/slideMaster1.xml.rels": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideLayout" Target="../slideLayouts/slideLayout1.xml"/>
<Relationship Id="rId6" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/theme" Target="../theme/theme1.xml"/>
</Relationships>`,
		"ppt/slideLayouts/slideLayout1.xml":            titleOnly,
		"ppt/slideLayouts/_rels/slideLayout1.xml.rels": layoutRelsToMaster,
		"ppt/slides/slide1.xml":                        sample,
		"ppt/slides/_rels/slide1.xml.rels": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideLayout" Target="../slideLayouts/slideLayout1.xml"/>
</Relationships>`,
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// TestImportDropsHiddenMasterWidgets asserts that OOXML hidden="1" master
// groups (Harvey balls) and stickers (DRAFT, Dummy data, a unique lime bar)
// do not leak into layout or pattern markup, while visible brand chrome does.
func TestImportDropsHiddenMasterWidgets(t *testing.T) {
	workingDir, importScript, _ := requireNodeEnv(t)
	resp, err := (ImportRunner{WorkingDir: workingDir, ScriptPath: importScript, Timeout: 30 * time.Second}).
		Run(context.Background(), ImportRequest{PPTXBase64: buildHiddenWidgetPPTX(t), Mode: "template"})
	if err != nil {
		t.Fatalf("import worker failed: %v", err)
	}
	var tmpl themes.Template
	if err := json.Unmarshal(resp.SceneOrTemplate, &tmpl); err != nil {
		t.Fatalf("bad template: %v\n%s", err, string(resp.SceneOrTemplate))
	}
	if len(tmpl.Archetypes) == 0 {
		t.Fatal("no archetypes")
	}
	sawInternal := false
	sawAccent := false
	for _, a := range tmpl.Archetypes {
		if strings.Contains(a.Markup, "00FF00") || strings.Contains(a.Markup, "00ff00") {
			t.Errorf("archetype %s/%s kept the hidden lime bar:\n%s", a.Kind, a.Title, a.Markup)
		}
		for _, junk := range []string{"DRAFT", "Dummy data", "Harvey"} {
			if strings.Contains(a.Markup, junk) {
				t.Errorf("archetype %s/%s kept hidden junk %q", a.Kind, a.Title, junk)
			}
		}
		if n := strings.Count(a.Markup, "<ast-shape"); n > 20 {
			t.Errorf("archetype %s/%s has %d shapes; hidden Harvey group probably leaked", a.Kind, a.Title, n)
		}
		if strings.Contains(a.Markup, "INTERNAL") {
			sawInternal = true
		}
		if strings.Contains(a.Markup, "E76500") || strings.Contains(a.Markup, "e76500") {
			sawAccent = true
		}
	}
	if !sawInternal {
		t.Fatal("visible INTERNAL footer was dropped along with hidden widgets")
	}
	if !sawAccent {
		t.Fatal("visible accent bar was dropped along with hidden widgets")
	}
}
