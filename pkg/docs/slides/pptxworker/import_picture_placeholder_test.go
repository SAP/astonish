package pptxworker

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"regexp"
	"strings"
	"testing"
	"time"
)

// picPanelRe matches the synthetic solid-fill panel (the blank blue box) that a
// picture placeholder falls back to when no image is available; picImageRe
// matches the desired borrowed image element.
var (
	picPanelRe = regexp.MustCompile(`<ast-shape id="ph-pic-\d+"`)
	picImageRe = regexp.MustCompile(`<ast-image id="ph-pic-\d+"[^>]*asset-ref=`)
)

// This test reproduces the "Mode A" corporate-template picture-placeholder case
// that the export-worker round-trip fixtures cannot express: a slide LAYOUT ships
// an EMPTY picture placeholder (<p:ph type="pic"> with no blip — an "insert
// picture here" slot), while the authored SAMPLE SLIDE that uses that layout
// carries the real hero photo as a free-floating <p:pic>. Before the fix the
// layout archetype rendered a synthetic neutral panel (the reported blank blue
// box); after the fix the importer borrows the sample slide's overlapping photo
// into the placeholder so the archetype emits a real <ast-image asset-ref=…>.
//
// The fixture is a hand-built minimal .pptx zip (no pptxgenjs) so it can encode
// the exact layout/sample-slide shape a real SAP corporate deck uses.

// tinyPNG returns a 2x2 PNG as raw bytes (a valid raster the importer ingests).
func tinyPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 20, G: 160, B: 90, A: 255})
	img.Set(1, 1, color.RGBA{R: 20, G: 160, B: 90, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

// buildModeAPPTX assembles a minimal .pptx with one master, one layout carrying
// an empty picture placeholder (idx=10) plus a title placeholder, and one sample
// slide (referencing that layout) that drops a real photo over the placeholder
// region. Returns base64.
func buildModeAPPTX(t *testing.T) string {
	t.Helper()
	png := tinyPNG(t)

	files := map[string]string{
		"[Content_Types].xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
<Default Extension="xml" ContentType="application/xml"/>
<Default Extension="png" ContentType="image/png"/>
<Default Extension="svg" ContentType="image/svg+xml"/>
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
<a:accent1><a:srgbClr val="97DD40"/></a:accent1>
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
<p:cSld><p:bg><p:bgPr><a:solidFill><a:srgbClr val="97DD40"/></a:solidFill><a:effectLst/></p:bgPr></p:bg>
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

		// LAYOUT: a cover ("Green cover with image") whose picture region is an
		// EMPTY <p:ph type="pic" idx="10"> — no blip. This is the "insert picture
		// here" slot that, un-borrowed, renders the synthetic panel.
		"ppt/slideLayouts/slideLayout1.xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:sldLayout xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" type="title" preserve="1">
<p:cSld name="Green cover with image in anvil shape">
<p:spTree>
<p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr>
<p:grpSpPr/>
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
		// free-floating <p:pic> whose box overlaps the layout's pic placeholder.
		"ppt/slides/slide1.xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:sld xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main">
<p:cSld>
<p:spTree>
<p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr>
<p:grpSpPr/>
<p:pic>
<p:nvPicPr><p:cNvPr id="11" name="Green Anvil Overlay"/><p:cNvPicPr><a:picLocks noChangeAspect="1"/></p:cNvPicPr><p:nvPr/></p:nvPicPr>
<p:blipFill><a:blip r:embed="rId3"/><a:stretch><a:fillRect/></a:stretch></p:blipFill>
<p:spPr><a:xfrm><a:off x="6069496" y="2029240"/><a:ext cx="5711687" cy="2828844"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom></p:spPr>
</p:pic>
<p:pic>
<p:nvPicPr><p:cNvPr id="10" name="Hero Photo"/><p:cNvPicPr><a:picLocks noChangeAspect="1"/></p:cNvPicPr><p:nvPr/></p:nvPicPr>
<p:blipFill><a:blip r:embed="rId2"/><a:stretch><a:fillRect/></a:stretch></p:blipFill>
<p:spPr><a:xfrm><a:off x="2359304" y="0"/><a:ext cx="9832696" cy="6858000"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom></p:spPr>
</p:pic>
</p:spTree></p:cSld>
</p:sld>`,

		"ppt/slides/_rels/slide1.xml.rels": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideLayout" Target="../slideLayouts/slideLayout1.xml"/>
<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="../media/image1.png"/>
<Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="../media/anvil.svg"/>
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
	// Binary media part.
	w, err := zw.Create("ppt/media/image1.png")
	if err != nil {
		t.Fatalf("zip create media: %v", err)
	}
	if _, err := w.Write(png); err != nil {
		t.Fatalf("zip write media: %v", err)
	}
	// A decorative flat vector "anvil" overlay whose box exactly matches the
	// picture placeholder — this is the highest-IoU candidate and would be picked
	// by a naive geometry match, but it is a vector shape, not a photo. The borrow
	// ranker must prefer the raster PNG over it.
	sw, err := zw.Create("ppt/media/anvil.svg")
	if err != nil {
		t.Fatalf("zip create svg media: %v", err)
	}
	if _, err := sw.Write([]byte(`<svg width="605" height="297" viewBox="0 0 605 297" xmlns="http://www.w3.org/2000/svg"><path d="M0 0 0 297 307 297 605 0Z" fill="#36A41D"/></svg>`)); err != nil {
		t.Fatalf("zip write svg media: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// TestImportBorrowsSampleImageIntoEmptyPicturePlaceholder is the regression test
// for the blank blue box: a layout with an empty picture placeholder must borrow
// the authored sample slide's overlapping photo so the title archetype emits an
// <ast-image asset-ref=…> for the hero region rather than a solid-fill panel.
func TestImportBorrowsSampleImageIntoEmptyPicturePlaceholder(t *testing.T) {
	workingDir, importScript, _ := requireNodeEnv(t)
	b64 := buildModeAPPTX(t)

	resp, err := (ImportRunner{WorkingDir: workingDir, ScriptPath: importScript, Timeout: 30 * time.Second}).
		Run(context.Background(), ImportRequest{PPTXBase64: b64, Mode: "template"})
	if err != nil {
		t.Fatalf("import worker failed: %v", err)
	}
	var tmpl struct {
		Archetypes []struct {
			Kind   string `json:"kind"`
			Title  string `json:"title"`
			Markup string `json:"markup"`
		} `json:"archetypes"`
	}
	if err := json.Unmarshal(resp.SceneOrTemplate, &tmpl); err != nil {
		t.Fatalf("bad template: %v\n%s", err, string(resp.SceneOrTemplate))
	}

	// Find the archetype derived from our "Green cover" layout (the title role).
	var green *struct {
		Kind   string `json:"kind"`
		Title  string `json:"title"`
		Markup string `json:"markup"`
	}
	for i := range tmpl.Archetypes {
		if strings.Contains(tmpl.Archetypes[i].Title, "Green cover") {
			green = &tmpl.Archetypes[i]
			break
		}
	}
	if green == nil {
		t.Fatalf("did not find the 'Green cover' title archetype; got %+v", tmpl.Archetypes)
	}

	// The picture-placeholder region must now be a real image (borrowed from the
	// sample slide), NOT a synthetic solid-fill panel.
	if !strings.Contains(green.Markup, `id="ph-pic-`) || !strings.Contains(green.Markup, "asset-ref=") {
		t.Fatalf("expected borrowed <ast-image asset-ref> for the picture placeholder, got:\n%s", green.Markup)
	}
	// Assert the ph-pic element is an ast-image, not an ast-shape panel.
	if picPanelRe.MatchString(green.Markup) {
		t.Fatalf("picture placeholder still renders a solid-fill panel (blue box); markup:\n%s", green.Markup)
	}
	if !picImageRe.MatchString(green.Markup) {
		t.Fatalf("expected an <ast-image id=\"ph-pic-…\" asset-ref=…>, got:\n%s", green.Markup)
	}

	// The borrowed asset MUST be the raster PHOTO, not the higher-IoU flat vector
	// "anvil" overlay that sits exactly over the placeholder box. Compute the
	// PNG's content-addressed ref and require the ph-pic image to reference it.
	wantRef := "sha256-" + hex.EncodeToString(sha256sum(tinyPNG(t)))
	m := picImageRefRe.FindStringSubmatch(green.Markup)
	if m == nil {
		t.Fatalf("could not extract ph-pic asset-ref from markup:\n%s", green.Markup)
	}
	if m[1] != wantRef {
		t.Fatalf("picture placeholder borrowed the wrong asset (a vector overlay instead of the photo):\n got  %s\n want %s (the raster PNG)", m[1], wantRef)
	}
}

// sha256sum returns the raw SHA-256 digest of b (matches the worker's addAsset
// content-addressing: `sha256-<hex>`).
func sha256sum(b []byte) []byte {
	h := sha256.Sum256(b)
	return h[:]
}

var picImageRefRe = regexp.MustCompile(`<ast-image id="ph-pic-\d+"[^>]*asset-ref="([^"]+)"`)
