package pptxworker

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// This file covers the corporate-template fidelity fixes measured against the
// real 2026 GCO IPED template:
//   - classification is SIGNATURE-FIRST: a layout carrying a body or picture
//     placeholder is flexible content even when its name contains "Title"
//     (e.g. "Title and Text", "Title and Text: 2 Columns", "Title and
//     Content"). Only pure cover/divider/agenda/thank-you names are fixed chrome;
//   - an empty picture placeholder in a content layout is emitted as a FILLABLE
//     image drop-slot (id in fillSlots), not a decorative-only neutral panel, and
//     produces NO "rendered neutral panel" warning;
//   - theme font references (+mj-*/+mn-*) resolve to displayFont/bodyFont and
//     never leak into markup as font="+mn-lt"/font="+mj-lt";
//   - showMasterSp=0 warns ONLY when suppression loses chrome (the layout has no
//     own decorative chrome), not for covers/dividers that carry their own.
//
// The fixtures are hand-built minimal .pptx zips (no pptxgenjs) so they can
// encode the exact layout NAMES + placeholder types + theme font-scheme a real
// corporate deck uses — which the export-worker round-trip cannot express.

// classificationTheme is a theme part whose major/minor Latin fonts are distinct
// concrete families so a +mj-lt/+mn-lt reference resolves to a known value.
const classificationTheme = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<a:theme xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" name="Corp Theme">
<a:themeElements>
<a:clrScheme name="Corp">
<a:dk1><a:sysClr val="windowText" lastClr="000000"/></a:dk1>
<a:lt1><a:sysClr val="window" lastClr="FFFFFF"/></a:lt1>
<a:dk2><a:srgbClr val="172033"/></a:dk2>
<a:lt2><a:srgbClr val="F1F5F9"/></a:lt2>
<a:accent1><a:srgbClr val="E76500"/></a:accent1>
<a:accent2><a:srgbClr val="97DD40"/></a:accent2>
<a:accent3><a:srgbClr val="2563EB"/></a:accent3>
<a:accent4><a:srgbClr val="FF8800"/></a:accent4>
<a:accent5><a:srgbClr val="00A0B0"/></a:accent5>
<a:accent6><a:srgbClr val="7C5CFF"/></a:accent6>
<a:hlink><a:srgbClr val="0000FF"/></a:hlink>
<a:folHlink><a:srgbClr val="800080"/></a:folHlink>
</a:clrScheme>
<a:fontScheme name="Corp">
<a:majorFont><a:latin typeface="72 Brand Display"/><a:ea typeface=""/><a:cs typeface=""/></a:majorFont>
<a:minorFont><a:latin typeface="72 Brand"/><a:ea typeface=""/><a:cs typeface=""/></a:minorFont>
</a:fontScheme>
<a:fmtScheme name="Corp">
<a:fillStyleLst><a:solidFill><a:schemeClr val="phClr"/></a:solidFill><a:solidFill><a:schemeClr val="phClr"/></a:solidFill><a:solidFill><a:schemeClr val="phClr"/></a:solidFill></a:fillStyleLst>
<a:lnStyleLst><a:ln><a:solidFill><a:schemeClr val="phClr"/></a:solidFill></a:ln><a:ln><a:solidFill><a:schemeClr val="phClr"/></a:solidFill></a:ln><a:ln><a:solidFill><a:schemeClr val="phClr"/></a:solidFill></a:ln></a:lnStyleLst>
<a:effectStyleLst><a:effectStyle><a:effectLst/></a:effectStyle><a:effectStyle><a:effectLst/></a:effectStyle><a:effectStyle><a:effectLst/></a:effectStyle></a:effectStyleLst>
<a:bgFillStyleLst><a:solidFill><a:schemeClr val="phClr"/></a:solidFill><a:solidFill><a:schemeClr val="phClr"/></a:solidFill><a:solidFill><a:schemeClr val="phClr"/></a:solidFill></a:bgFillStyleLst>
</a:fmtScheme>
</a:themeElements>
</a:theme>`

// classificationMaster is a master with a neutral bg and one decorative accent
// rectangle (so layouts that inherit the master carry that chrome, and a layout
// that suppresses the master with showMasterSp=0 AND carries no own chrome is a
// genuinely-sparse case).
const classificationMaster = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:sldMaster xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main">
<p:cSld><p:bg><p:bgPr><a:solidFill><a:srgbClr val="FFFFFF"/></a:solidFill><a:effectLst/></p:bgPr></p:bg>
<p:spTree>
<p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr>
<p:grpSpPr/>
<p:sp><p:nvSpPr><p:cNvPr id="2" name="Accent Bar"/><p:cNvSpPr/><p:nvPr/></p:nvSpPr>
<p:spPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="200000" cy="6858000"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom><a:solidFill><a:srgbClr val="E76500"/></a:solidFill></p:spPr></p:sp>
</p:spTree></p:cSld>
<p:clrMap bg1="lt1" tx1="dk1" bg2="lt2" tx2="dk2" accent1="accent1" accent2="accent2" accent3="accent3" accent4="accent4" accent5="accent5" accent6="accent6" hlink="hlink" folHlink="folHlink"/>
<p:sldLayoutIdLst>
<p:sldLayoutId id="2147483649" r:id="rId1"/>
<p:sldLayoutId id="2147483650" r:id="rId2"/>
<p:sldLayoutId id="2147483651" r:id="rId3"/>
<p:sldLayoutId id="2147483652" r:id="rId4"/>
<p:sldLayoutId id="2147483653" r:id="rId5"/>
</p:sldLayoutIdLst>
</p:sldMaster>`

// classificationMasterRels wires the master to five layouts + theme.
const classificationMasterRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideLayout" Target="../slideLayouts/slideLayout1.xml"/>
<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideLayout" Target="../slideLayouts/slideLayout2.xml"/>
<Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideLayout" Target="../slideLayouts/slideLayout3.xml"/>
<Relationship Id="rId4" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideLayout" Target="../slideLayouts/slideLayout4.xml"/>
<Relationship Id="rId5" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideLayout" Target="../slideLayouts/slideLayout5.xml"/>
<Relationship Id="rId6" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/theme" Target="../theme/theme1.xml"/>
</Relationships>`

// layoutRelsToMaster is the shared .rels body for a layout that only references
// the single master.
const layoutRelsToMaster = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideMaster" Target="../slideMasters/slideMaster1.xml"/>
</Relationships>`

// layoutXML wraps a spTree body in a named sldLayout with the given
// showMasterSp attribute (pass showMasterSp="0" or "").
func layoutXML(name, showMasterSp, body string) string {
	attr := ""
	if showMasterSp != "" {
		attr = ` showMasterSp="` + showMasterSp + `"`
	}
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:sldLayout xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"` + attr + ` preserve="1">
<p:cSld name="` + name + `">
<p:spTree>
<p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr>
<p:grpSpPr/>
` + body + `
</p:spTree></p:cSld>
</p:sldLayout>`
}

// phSp emits a placeholder <p:sp> (title/body/pic) at a box, optionally with a
// run whose a:latin typeface is the given token (e.g. "+mn-lt").
func phSp(id int, name, phType, phIdx string, x, y, cx, cy int, fontToken, text string) string {
	ph := `<p:ph type="` + phType + `"`
	if phIdx != "" {
		ph += ` idx="` + phIdx + `"`
	}
	ph += `/>`
	tx := ""
	if text != "" {
		latin := ""
		if fontToken != "" {
			latin = `<a:latin typeface="` + fontToken + `"/>`
		}
		tx = `<p:txBody><a:bodyPr/><a:p><a:r><a:rPr lang="en-US">` + latin + `</a:rPr><a:t>` + text + `</a:t></a:r></a:p></p:txBody>`
	}
	return `<p:sp>
<p:nvSpPr><p:cNvPr id="` + itoa(id) + `" name="` + name + `"/><p:cNvSpPr><a:spLocks noGrp="1"/></p:cNvSpPr><p:nvPr>` + ph + `</p:nvPr></p:nvSpPr>
<p:spPr><a:xfrm><a:off x="` + itoa(x) + `" y="` + itoa(y) + `"/><a:ext cx="` + itoa(cx) + `" cy="` + itoa(cy) + `"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom></p:spPr>
` + tx + `
</p:sp>`
}

func itoa(n int) string {
	return strconv.Itoa(n)
}

// buildClassificationPPTX assembles a minimal .pptx with one master + theme and
// FIVE named layouts exercising the classification/font/warning fixes:
//
//	L1 "Blue cover with anvil"            title + own bg rect (showMasterSp=0, own chrome)  -> title/fixed
//	L2 "Divider Page"                     ctrTitle only, showMasterSp=0, NO own chrome      -> section/fixed + sparse warn
//	L3 "Title and Text: 2 Columns"        title + 2 body                                    -> content/flexible
//	L4 "Title and Content"                title + empty pic placeholder                     -> content/flexible + image slot
//	L5 "Thank You"                        title with a +mn-lt run, no body                  -> closing/fixed + font resolved
func buildClassificationPPTX(t *testing.T) string {
	t.Helper()

	// L1: a cover that suppresses the master AND carries its own bg + chrome.
	l1 := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:sldLayout xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" showMasterSp="0" preserve="1">
<p:cSld name="Blue cover with anvil"><p:bg><p:bgPr><a:solidFill><a:srgbClr val="2563EB"/></a:solidFill><a:effectLst/></p:bgPr></p:bg>
<p:spTree>
<p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr>
<p:grpSpPr/>
<p:sp><p:nvSpPr><p:cNvPr id="2" name="Anvil"/><p:cNvSpPr/><p:nvPr/></p:nvSpPr>
<p:spPr><a:xfrm><a:off x="1000000" y="1000000"/><a:ext cx="3000000" cy="2000000"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom><a:solidFill><a:srgbClr val="97DD40"/></a:solidFill></p:spPr></p:sp>
` + phSp(3, "Title 1", "title", "", 288000, 2700000, 5500000, 1000000, "", "Click to edit title") + `
</p:spTree></p:cSld>
</p:sldLayout>`

	// L2: a divider that suppresses the master and carries NO own chrome (only a
	// ctrTitle placeholder). This is the genuinely-sparse case that SHOULD warn.
	l2 := layoutXML("Divider Page", "0",
		phSp(2, "Title 1", "ctrTitle", "", 288000, 2700000, 5500000, 1000000, "", "Section"))

	// L3: a multi-column content layout named "Title and Text: 2 Columns".
	l3 := layoutXML("Title and Text: 2 Columns", "",
		phSp(2, "Title 1", "title", "", 288000, 400000, 11000000, 900000, "", "Title")+
			phSp(3, "Body 1", "body", "1", 288000, 1500000, 5000000, 4000000, "", "Left column")+
			phSp(4, "Body 2", "body", "2", 6000000, 1500000, 5000000, 4000000, "", "Right column"))

	// L4: "Title and Content" with an EMPTY picture placeholder (no blip, no
	// borrowable sample). Must become a fillable image drop-slot (ph-pic in
	// fillSlots), not a neutral panel, and produce NO neutral-panel warning.
	l4 := layoutXML("Title and Content", "",
		phSp(2, "Title 1", "title", "", 288000, 400000, 11000000, 900000, "", "Title")+
			phSp(3, "Picture Placeholder 3", "pic", "10", 1000000, 1500000, 5000000, 4000000, "", ""))

	// L5: "Thank You" closing with a run whose a:latin typeface is the theme
	// reference +mn-lt (body) — must resolve to the minor font, never leak.
	l5 := layoutXML("Thank You", "",
		phSp(2, "Title 1", "title", "", 288000, 2700000, 5500000, 1000000, "+mn-lt", "Thank you"))

	files := map[string]string{
		"[Content_Types].xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
<Default Extension="xml" ContentType="application/xml"/>
<Default Extension="png" ContentType="image/png"/>
<Override PartName="/ppt/presentation.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.presentation.main+xml"/>
<Override PartName="/ppt/slideMasters/slideMaster1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slideMaster+xml"/>
<Override PartName="/ppt/slideLayouts/slideLayout1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slideLayout+xml"/>
<Override PartName="/ppt/slideLayouts/slideLayout2.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slideLayout+xml"/>
<Override PartName="/ppt/slideLayouts/slideLayout3.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slideLayout+xml"/>
<Override PartName="/ppt/slideLayouts/slideLayout4.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slideLayout+xml"/>
<Override PartName="/ppt/slideLayouts/slideLayout5.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slideLayout+xml"/>
<Override PartName="/ppt/theme/theme1.xml" ContentType="application/vnd.openxmlformats-officedocument.theme+xml"/>
</Types>`,

		"_rels/.rels": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="ppt/presentation.xml"/>
</Relationships>`,

		"ppt/presentation.xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:presentation xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main">
<p:sldMasterIdLst><p:sldMasterId id="2147483648" r:id="rId1"/></p:sldMasterIdLst>
<p:sldSz cx="12192000" cy="6858000"/>
</p:presentation>`,

		"ppt/_rels/presentation.xml.rels": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideMaster" Target="slideMasters/slideMaster1.xml"/>
<Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/theme" Target="theme/theme1.xml"/>
</Relationships>`,

		"ppt/theme/theme1.xml":                          classificationTheme,
		"ppt/slideMasters/slideMaster1.xml":             classificationMaster,
		"ppt/slideMasters/_rels/slideMaster1.xml.rels":  classificationMasterRels,
		"ppt/slideLayouts/slideLayout1.xml":             l1,
		"ppt/slideLayouts/slideLayout2.xml":             l2,
		"ppt/slideLayouts/slideLayout3.xml":             l3,
		"ppt/slideLayouts/slideLayout4.xml":             l4,
		"ppt/slideLayouts/slideLayout5.xml":             l5,
		"ppt/slideLayouts/_rels/slideLayout1.xml.rels":  layoutRelsToMaster,
		"ppt/slideLayouts/_rels/slideLayout2.xml.rels":  layoutRelsToMaster,
		"ppt/slideLayouts/_rels/slideLayout3.xml.rels":  layoutRelsToMaster,
		"ppt/slideLayouts/_rels/slideLayout4.xml.rels":  layoutRelsToMaster,
		"ppt/slideLayouts/_rels/slideLayout5.xml.rels":  layoutRelsToMaster,
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

// classificationArchetypes is the decoded archetype shape shared by the tests.
type classificationArchetypes struct {
	Archetypes []struct {
		Kind      string   `json:"kind"`
		Title     string   `json:"title"`
		Markup    string   `json:"markup"`
		Tier      string   `json:"tier"`
		FillSlots []string `json:"fillSlots"`
	} `json:"archetypes"`
	TemplateModel struct {
		Warnings []struct {
			Message string `json:"message"`
		} `json:"warnings"`
	} `json:"templateModel"`
}

func importClassificationFixture(t *testing.T) classificationArchetypes {
	t.Helper()
	workingDir, importScript, _ := requireNodeEnv(t)
	b64 := buildClassificationPPTX(t)
	resp, err := (ImportRunner{WorkingDir: workingDir, ScriptPath: importScript, Timeout: 30 * time.Second}).
		Run(context.Background(), ImportRequest{PPTXBase64: b64, Mode: "template"})
	if err != nil {
		t.Fatalf("import worker failed: %v", err)
	}
	var out classificationArchetypes
	if err := json.Unmarshal(resp.SceneOrTemplate, &out); err != nil {
		t.Fatalf("bad template: %v\n%s", err, string(resp.SceneOrTemplate))
	}
	return out
}

// findArchetype returns the first archetype whose Title contains sub.
func findArchetype(as classificationArchetypes, sub string) *struct {
	Kind      string   `json:"kind"`
	Title     string   `json:"title"`
	Markup    string   `json:"markup"`
	Tier      string   `json:"tier"`
	FillSlots []string `json:"fillSlots"`
} {
	for i := range as.Archetypes {
		if strings.Contains(as.Archetypes[i].Title, sub) {
			return &as.Archetypes[i]
		}
	}
	return nil
}

// TestImportClassifiesContentLayoutsAsFlexible asserts that signature-first
// classification makes "Title and Text: 2 Columns" and "Title and Content"
// flexible content (kind content*), while genuine chrome (cover/divider/
// thank-you) stays fixed.
func TestImportClassifiesContentLayoutsAsFlexible(t *testing.T) {
	as := importClassificationFixture(t)

	cases := []struct {
		titleSub  string
		wantKind  string // base kind prefix
		wantTier  string
	}{
		{"Title and Text: 2 Columns", "content", "flexible"},
		{"Title and Content", "content", "flexible"},
		{"Blue cover with anvil", "title", "fixed"},
		{"Divider Page", "section", "fixed"},
		{"Thank You", "closing", "fixed"},
	}
	for _, c := range cases {
		a := findArchetype(as, c.titleSub)
		if a == nil {
			t.Fatalf("archetype %q not found; got %+v", c.titleSub, as.Archetypes)
		}
		if !strings.HasPrefix(stripVariantSuffix(a.Kind), c.wantKind) {
			t.Fatalf("archetype %q: want kind prefix %q, got kind %q", c.titleSub, c.wantKind, a.Kind)
		}
		if a.Tier != c.wantTier {
			t.Fatalf("archetype %q: want tier %q, got %q (kind %q)", c.titleSub, c.wantTier, a.Tier, a.Kind)
		}
	}
}

var astImageAnyRe = regexp.MustCompile(`<ast-(image|shape) id="ph-pic-\d+"`)

// TestImportEmptyPictureBecomesFillableImageSlot asserts that an empty picture
// placeholder in a content layout is advertised as a fillable ph-pic image slot
// (id in fillSlots AND present in markup), NOT dropped, and produces no
// "neutral panel" warning.
func TestImportEmptyPictureBecomesFillableImageSlot(t *testing.T) {
	as := importClassificationFixture(t)

	a := findArchetype(as, "Title and Content")
	if a == nil {
		t.Fatalf("'Title and Content' archetype not found; got %+v", as.Archetypes)
	}
	// A ph-pic-N id must be advertised in fillSlots.
	var picID string
	for _, id := range a.FillSlots {
		if strings.HasPrefix(id, "ph-pic-") {
			picID = id
			break
		}
	}
	if picID == "" {
		t.Fatalf("'Title and Content' has an empty picture placeholder but no ph-pic-* fillSlot; fillSlots=%v\n%s", a.FillSlots, a.Markup)
	}
	// The advertised id must actually appear in the markup as an ast-image or an
	// advertised fillable panel (ast-shape) — never absent.
	if !strings.Contains(a.Markup, `id="`+picID+`"`) {
		t.Fatalf("fillSlot %q not present in markup:\n%s", picID, a.Markup)
	}
	if !astImageAnyRe.MatchString(a.Markup) {
		t.Fatalf("expected an ast-image/ast-shape image drop-slot with a ph-pic-N id:\n%s", a.Markup)
	}
	// Every fillSlot id must be present in the markup (contiguous / consistent).
	for _, id := range a.FillSlots {
		if !strings.Contains(a.Markup, `id="`+id+`"`) {
			t.Fatalf("archetype %q declares fillSlot %q absent from markup:\n%s", a.Title, id, a.Markup)
		}
	}
	// No "rendered neutral panel" warning for this content-layout case.
	for _, w := range as.TemplateModel.Warnings {
		if strings.Contains(w.Message, "neutral panel") {
			t.Fatalf("unexpected neutral-panel warning: %q", w.Message)
		}
	}
}

var fontTokenLeakRe = regexp.MustCompile(`font="\+m[jn]-`)

// TestImportResolvesThemeFontTokens asserts that theme font references
// (+mj-*/+mn-*) resolve to the concrete display/body families and never leak
// into markup as font="+mn-lt"/font="+mj-lt".
func TestImportResolvesThemeFontTokens(t *testing.T) {
	as := importClassificationFixture(t)

	// No archetype markup may contain a raw +mj-*/+mn-* font token.
	for _, a := range as.Archetypes {
		if fontTokenLeakRe.MatchString(a.Markup) {
			t.Fatalf("archetype %q leaks a raw theme font token into markup:\n%s", a.Kind, a.Markup)
		}
	}
	// The "Thank You" closing carried a +mn-lt run; it must resolve to the
	// minor font family declared in the fixture theme ("72 Brand"), emitted with
	// the web-safe fallback chain appended.
	a := findArchetype(as, "Thank You")
	if a == nil {
		t.Fatalf("'Thank You' archetype not found; got %+v", as.Archetypes)
	}
	if !strings.Contains(a.Markup, `font="&quot;72 Brand&quot;`) {
		t.Fatalf("expected +mn-lt run to resolve to the quoted minor font \"72 Brand\":\n%s", a.Markup)
	}
	if !strings.Contains(a.Markup, "sans-serif") {
		t.Fatalf("resolved font missing web-safe sans-serif fallback:\n%s", a.Markup)
	}
}

// TestImportSuppressMasterWarningIsQuiet asserts showMasterSp=0 warns only on
// genuine chrome loss: a cover that suppresses the master but carries its own
// chrome must NOT warn; a divider that suppresses the master with no own chrome
// SHOULD warn.
func TestImportSuppressMasterWarningIsQuiet(t *testing.T) {
	as := importClassificationFixture(t)

	var coverWarned, dividerWarned bool
	for _, w := range as.TemplateModel.Warnings {
		if !strings.Contains(w.Message, "suppresses master chrome") {
			continue
		}
		if strings.Contains(w.Message, "Blue cover with anvil") {
			coverWarned = true
		}
		if strings.Contains(w.Message, "Divider Page") {
			dividerWarned = true
		}
	}
	if coverWarned {
		t.Fatalf("cover with own chrome must NOT produce a suppress-master warning; warnings=%+v", as.TemplateModel.Warnings)
	}
	if !dividerWarned {
		t.Fatalf("a divider that suppresses the master with no own chrome SHOULD warn; warnings=%+v", as.TemplateModel.Warnings)
	}
}
