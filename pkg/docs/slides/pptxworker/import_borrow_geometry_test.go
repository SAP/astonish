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

// This test proves the "Mode A" geometry-faithful, flipped, swappable hero-image
// borrow. It hand-builds a minimal .pptx (pptxgenjs cannot emit an empty pic
// placeholder plus a sample <p:pic>) where:
//
//   - the LAYOUT ("Blue cover with anvil") ships an EMPTY <p:ph type="pic"> slot
//     (no blip) at one box, plus a decorative dark-green rect chrome shape, and a
//     title placeholder;
//   - the SAMPLE slide (slide1) that uses that layout carries a free-floating
//     <p:pic> raster blip at a DISTINCT full-bleed box with flipH="1".
//
// After import in template mode the title archetype must render the borrowed
// image at the SAMPLE picture's converted box (NOT the placeholder's box),
// mirrored (flip-h="true"), advertised as a fill slot, with the decorative green
// shape painted BEHIND it, and with no leftover neutral solid-fill panel.

// EMU -> canvas conversion (matches toCanvasBox in import_worker.mjs):
//   px  = EMU / 9525
//   scale = min(1920 / (slideCX/9525), 1080 / (slideCY/9525))
// With slide size 12192000 x 6858000 EMU the scale is exactly 1.5, so:
//   canvasUnits = round(EMU / 9525 * 1.5) = round(EMU / 6350)
//
// Sample hero <p:pic> box  off(1905000,190500) ext(8382000,4762500)
//   -> x=300 y=30 w=1320 h=750   (the BORROWED box we assert on)
// Layout pic placeholder box off(6069496,2029240) ext(5711687,2828844)
//   -> x=956 y=320 w=899 h=445   (must NOT be used)
const (
	wantBorrowX = 300
	wantBorrowY = 30
	wantBorrowW = 1320
	wantBorrowH = 750
)

// buildModeAFlipPPTX assembles a minimal .pptx zip encoding the Mode-A borrow
// scenario with a flipped, distinct-box sample hero photo and a decorative green
// chrome rect in the layout. Returns base64.
func buildModeAFlipPPTX(t *testing.T) string {
	t.Helper()
	png := tinyPNG(t)

	files := map[string]string{
		"[Content_Types].xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
<Default Extension="xml" ContentType="application/xml"/>
<Default Extension="png" ContentType="image/png"/>
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

		"ppt/theme/theme1.xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<a:theme xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" name="Office Theme">
<a:themeElements>
<a:clrScheme name="Office">
<a:dk1><a:sysClr val="windowText" lastClr="000000"/></a:dk1>
<a:lt1><a:sysClr val="window" lastClr="FFFFFF"/></a:lt1>
<a:dk2><a:srgbClr val="172033"/></a:dk2>
<a:lt2><a:srgbClr val="FFFFFF"/></a:lt2>
<a:accent1><a:srgbClr val="1D4ED8"/></a:accent1>
<a:accent2><a:srgbClr val="89D1FF"/></a:accent2>
<a:accent3><a:srgbClr val="2563EB"/></a:accent3>
<a:accent4><a:srgbClr val="FF8800"/></a:accent4>
<a:accent5><a:srgbClr val="00A0B0"/></a:accent5>
<a:accent6><a:srgbClr val="7C5CFF"/></a:accent6>
<a:hlink><a:srgbClr val="0000FF"/></a:hlink>
<a:folHlink><a:srgbClr val="800080"/></a:folHlink>
</a:clrScheme>
<a:fontScheme name="Office">
<a:majorFont><a:latin typeface="Arial"/><a:ea typeface=""/><a:cs typeface=""/></a:majorFont>
<a:minorFont><a:latin typeface="Arial"/><a:ea typeface=""/><a:cs typeface=""/></a:minorFont>
</a:fontScheme>
<a:fmtScheme name="Office">
<a:fillStyleLst><a:solidFill><a:schemeClr val="phClr"/></a:solidFill><a:solidFill><a:schemeClr val="phClr"/></a:solidFill><a:solidFill><a:schemeClr val="phClr"/></a:solidFill></a:fillStyleLst>
<a:lnStyleLst><a:ln><a:solidFill><a:schemeClr val="phClr"/></a:solidFill></a:ln><a:ln><a:solidFill><a:schemeClr val="phClr"/></a:solidFill></a:ln><a:ln><a:solidFill><a:schemeClr val="phClr"/></a:solidFill></a:ln></a:lnStyleLst>
<a:effectStyleLst><a:effectStyle><a:effectLst/></a:effectStyle><a:effectStyle><a:effectLst/></a:effectStyle><a:effectStyle><a:effectLst/></a:effectStyle></a:effectStyleLst>
<a:bgFillStyleLst><a:solidFill><a:schemeClr val="phClr"/></a:solidFill><a:solidFill><a:schemeClr val="phClr"/></a:solidFill><a:solidFill><a:schemeClr val="phClr"/></a:solidFill></a:bgFillStyleLst>
</a:fmtScheme>
</a:themeElements>
</a:theme>`,

		"ppt/slideMasters/slideMaster1.xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:sldMaster xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main">
<p:cSld><p:bg><p:bgPr><a:solidFill><a:srgbClr val="0B1220"/></a:solidFill><a:effectLst/></p:bgPr></p:bg>
<p:spTree>
<p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr>
<p:grpSpPr/>
</p:spTree></p:cSld>
<p:clrMap bg1="lt1" tx1="dk1" bg2="lt2" tx2="dk2" accent1="accent1" accent2="accent2" accent3="accent3" accent4="accent4" accent5="accent5" accent6="accent6" hlink="hlink" folHlink="folHlink"/>
<p:sldLayoutIdLst><p:sldLayoutId id="2147483649" r:id="rId1"/></p:sldLayoutIdLst>
</p:sldMaster>`,

		"ppt/slideMasters/_rels/slideMaster1.xml.rels": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideLayout" Target="../slideLayouts/slideLayout1.xml"/>
<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/theme" Target="../theme/theme1.xml"/>
</Relationships>`,

		// LAYOUT: a title cover whose picture region is an EMPTY <p:ph type="pic"
		// idx="10"> (no blip) at a SMALL box, plus a decorative dark-green rect
		// (painted before/behind the borrowed hero) and a title placeholder.
		"ppt/slideLayouts/slideLayout1.xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:sldLayout xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" type="title" preserve="1">
<p:cSld name="Blue cover with anvil and image">
<p:spTree>
<p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr>
<p:grpSpPr/>
<p:sp>
<p:nvSpPr><p:cNvPr id="9" name="Green Anvil"/><p:cNvSpPr/><p:nvPr/></p:nvSpPr>
<p:spPr><a:xfrm><a:off x="288000" y="600000"/><a:ext cx="2000000" cy="900000"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom><a:solidFill><a:srgbClr val="0B6E2E"/></a:solidFill></p:spPr>
</p:sp>
<p:sp>
<p:nvSpPr><p:cNvPr id="2" name="Title 1"/><p:cNvSpPr><a:spLocks noGrp="1"/></p:cNvSpPr><p:nvPr><p:ph type="title"/></p:nvPr></p:nvSpPr>
<p:spPr><a:xfrm><a:off x="288000" y="2700000"/><a:ext cx="5500000" cy="1000000"/></a:xfrm></p:spPr>
<p:txBody><a:bodyPr/><a:p><a:r><a:rPr lang="en-US"/><a:t>Click to edit title</a:t></a:r></a:p></p:txBody>
</p:sp>
<p:sp>
<p:nvSpPr><p:cNvPr id="3" name="Picture Placeholder 5"/><p:cNvSpPr><a:spLocks noGrp="1"/></p:cNvSpPr><p:nvPr><p:ph type="pic" sz="quarter" idx="10"/></p:nvPr></p:nvSpPr>
<p:spPr><a:xfrm><a:off x="6069496" y="2029240"/><a:ext cx="5711687" cy="2828844"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom></p:spPr>
</p:sp>
</p:spTree></p:cSld>
</p:sldLayout>`,

		"ppt/slideLayouts/_rels/slideLayout1.xml.rels": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideMaster" Target="../slideMasters/slideMaster1.xml"/>
</Relationships>`,

		// SAMPLE SLIDE: references slideLayout1 and drops the real hero photo as a
		// free-floating <p:pic> at a DISTINCT full-bleed-ish box with flipH="1".
		"ppt/slides/slide1.xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:sld xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main">
<p:cSld>
<p:spTree>
<p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr>
<p:grpSpPr/>
<p:pic>
<p:nvPicPr><p:cNvPr id="10" name="Hero Photo"/><p:cNvPicPr><a:picLocks noChangeAspect="1"/></p:cNvPicPr><p:nvPr/></p:nvPicPr>
<p:blipFill><a:blip r:embed="rId2"/><a:stretch><a:fillRect/></a:stretch></p:blipFill>
<p:spPr><a:xfrm flipH="1"><a:off x="1905000" y="190500"/><a:ext cx="8382000" cy="4762500"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom></p:spPr>
</p:pic>
</p:spTree></p:cSld>
</p:sld>`,

		"ppt/slides/_rels/slide1.xml.rels": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideLayout" Target="../slideLayouts/slideLayout1.xml"/>
<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="../media/image1.png"/>
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
	w, err := zw.Create("ppt/media/image1.png")
	if err != nil {
		t.Fatalf("zip create media: %v", err)
	}
	if _, err := w.Write(png); err != nil {
		t.Fatalf("zip write media: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

var (
	// phPicImageTagRe captures the whole <ast-image id="ph-pic-N" …> tag.
	phPicImageTagRe = regexp.MustCompile(`<ast-image id="ph-pic-\d+"[^>]*>`)
	// phPicShapePanelRe matches the leftover neutral solid-fill panel form.
	phPicShapePanelRe = regexp.MustCompile(`<ast-shape id="ph-pic-\d+"`)
	// attrIntRe extracts an integer geometry attribute value.
	greenShapeRe = regexp.MustCompile(`<ast-shape[^>]*fill="#0B6E2E"`)
)

func attrInt(t *testing.T, tag, attr string) int {
	t.Helper()
	re := regexp.MustCompile(attr + `="(-?\d+)"`)
	m := re.FindStringSubmatch(tag)
	if m == nil {
		t.Fatalf("attribute %q not found in tag:\n%s", attr, tag)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("bad %q value %q: %v", attr, m[1], err)
	}
	return n
}

// TestImportBorrowsSamplePhotoGeometryAndFlip proves the geometry-faithful,
// flipped, swappable hero-image borrow (Mode A): the borrowed image renders at
// the SAMPLE picture's box (not the placeholder's), mirrored, as a fill slot,
// behind-order-correct relative to the decorative green shape, and with no
// leftover neutral panel.
func TestImportBorrowsSamplePhotoGeometryAndFlip(t *testing.T) {
	workingDir, importScript, _ := requireNodeEnv(t)
	b64 := buildModeAFlipPPTX(t)

	resp, err := (ImportRunner{WorkingDir: workingDir, ScriptPath: importScript, Timeout: 30 * time.Second}).
		Run(context.Background(), ImportRequest{PPTXBase64: b64, Mode: "template"})
	if err != nil {
		t.Fatalf("import worker failed: %v", err)
	}
	var tmpl struct {
		Archetypes []struct {
			Kind      string   `json:"kind"`
			Title     string   `json:"title"`
			Markup    string   `json:"markup"`
			FillSlots []string `json:"fillSlots"`
		} `json:"archetypes"`
	}
	if err := json.Unmarshal(resp.SceneOrTemplate, &tmpl); err != nil {
		t.Fatalf("bad template: %v\n%s", err, string(resp.SceneOrTemplate))
	}

	// Find the archetype whose markup carries the borrowed picture placeholder.
	var arch *struct {
		Kind      string   `json:"kind"`
		Title     string   `json:"title"`
		Markup    string   `json:"markup"`
		FillSlots []string `json:"fillSlots"`
	}
	for i := range tmpl.Archetypes {
		if strings.Contains(tmpl.Archetypes[i].Markup, "ph-pic-") {
			arch = &tmpl.Archetypes[i]
			break
		}
	}
	if arch == nil {
		t.Fatalf("no archetype with a ph-pic- placeholder; got %+v", tmpl.Archetypes)
	}

	// (e) The borrow must have REPLACED the neutral panel: no ph-pic ast-shape.
	if phPicShapePanelRe.MatchString(arch.Markup) {
		t.Fatalf("picture placeholder still renders a solid-fill panel; markup:\n%s", arch.Markup)
	}

	// (a) The ph-pic element is an <ast-image> and it is flipped horizontally.
	tag := phPicImageTagRe.FindString(arch.Markup)
	if tag == "" {
		t.Fatalf("expected an <ast-image id=\"ph-pic-…\"> tag, got:\n%s", arch.Markup)
	}
	if !strings.Contains(tag, `flip-h="true"`) {
		t.Fatalf("expected flip-h=\"true\" on the borrowed hero image, got:\n%s", tag)
	}
	if !strings.Contains(tag, "asset-ref=") {
		t.Fatalf("expected asset-ref= on the borrowed hero image, got:\n%s", tag)
	}

	// (b) Its x/y/w/h equal the SAMPLE picture's converted box, NOT the
	// placeholder's box.
	gotX := attrInt(t, tag, "x")
	gotY := attrInt(t, tag, "y")
	gotW := attrInt(t, tag, "w")
	gotH := attrInt(t, tag, "h")
	if gotX != wantBorrowX || gotY != wantBorrowY || gotW != wantBorrowW || gotH != wantBorrowH {
		t.Fatalf("borrowed image geometry = (%d,%d,%d,%d); want the SAMPLE box (%d,%d,%d,%d) not the placeholder box (956,320,899,445)\ntag: %s",
			gotX, gotY, gotW, gotH, wantBorrowX, wantBorrowY, wantBorrowW, wantBorrowH, tag)
	}

	// (c) The ph-pic id is advertised as a fill slot.
	picIDRe := regexp.MustCompile(`id="(ph-pic-\d+)"`)
	idm := picIDRe.FindStringSubmatch(tag)
	if idm == nil {
		t.Fatalf("could not extract ph-pic id from tag:\n%s", tag)
	}
	picID := idm[1]
	found := false
	for _, s := range arch.FillSlots {
		if s == picID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("fillSlots %v does not include the swappable hero id %q", arch.FillSlots, picID)
	}

	// (d) The decorative green shape is painted BEFORE (behind) the borrowed
	// image: its substring index precedes the ast-image tag's index.
	greenLoc := greenShapeRe.FindStringIndex(arch.Markup)
	if greenLoc == nil {
		t.Fatalf("expected the decorative green (#0B6E2E) ast-shape in markup:\n%s", arch.Markup)
	}
	imgIdx := strings.Index(arch.Markup, tag)
	if imgIdx < 0 {
		t.Fatalf("could not locate the ph-pic ast-image tag in markup")
	}
	if greenLoc[0] >= imgIdx {
		t.Fatalf("decorative green shape (idx %d) must be painted BEFORE the borrowed image (idx %d)", greenLoc[0], imgIdx)
	}
}
