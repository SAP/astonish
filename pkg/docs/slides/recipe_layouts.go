package slides

import "fmt"

// Layout geometry is 1920×1080. Body cards fill the band from the header to
// the footer hairline so pages are not a thin strip of content on empty canvas.

func (b *recipeBuilder) layoutCover() {
	if b.isProduct() {
		b.layoutCoverProduct()
		return
	}
	b.bg()
	b.paintLogo()
	b.paintConfidential()
	p := b.pal
	ey := b.topY()
	if b.chrome.LogoRef == "" && ey < 160 {
		ey = 160
	}
	b.slot("eyebrow", p.mx, ey, p.contentW-40, 36, p.labelSize, p.accent, "600", p.body, "")
	b.shape("rule", "rect", p.mx, ey+44, 72, 4, p.accent, true)
	hy := ey + 64
	hh := p.h1 + 12
	if hh < 100 {
		hh = 100
	}
	b.slot("headline", p.mx, hy, p.contentW-80, hh, p.h1, p.ink, "700", p.display, "")
	dekY := hy + hh + 8
	if b.want("headline_2") {
		b.slot("headline_2", p.mx, hy+hh-8, p.contentW-80, hh, p.h1, p.ink, "700", p.display, "")
		dekY = hy + 2*hh + 8
	}
	dekH := 110
	b.slot("dek", p.mx, dekY, p.contentW-200, dekH, p.bodySize+4, p.secondary, "", p.body, "")
	metaY := dekY + dekH + 48
	if metaY+90 > 1010 {
		metaY = 1010 - 90
	}
	nMeta := 4
	if b.fills != nil {
		nMeta = 2
		if b.want("meta_3_value") {
			nMeta = 3
		}
		if b.want("meta_4_value") {
			nMeta = 4
		}
	}
	cellW := (p.contentW - 24*nMeta) / nMeta
	for i := 0; i < nMeta; i++ {
		x := p.mx + i*(cellW+24)
		if i > 0 {
			b.shape(fmt.Sprintf("meta-rule-%d", i), "rect", x-12, metaY, 1, 88, p.card, true)
		}
		b.slot(fmt.Sprintf("meta_%d_label", i+1), x, metaY, cellW, 24, p.labelSize, p.accent, "600", p.body, "")
		b.slot(fmt.Sprintf("meta_%d_value", i+1), x, metaY+28, cellW, 56, 22, p.ink, "600", p.display, "")
	}
	b.close()
}

func (b *recipeBuilder) layoutCoverProduct() {
	b.bg()
	p := b.pal
	b.paintProductRail()
	if b.want("prompt") {
		b.slot("prompt", p.mx, 300, 1400, 36, 20, p.accent, "", p.mono, "")
	}
	hy := 360
	if !b.want("prompt") {
		hy = 320
	}
	b.slot("headline", p.mx, hy, 1600, 140, p.h1, p.ink, "800", p.display, "")
	if b.want("headline_2") {
		b.slot("headline_2", p.mx, hy+120, 1600, 100, p.h1-24, p.ink, "800", p.display, "")
		hy += 100
	}
	b.slot("dek", p.mx, hy+160, 1400, 90, 32, p.ink, "500", p.display, "")
	nMeta := 2
	if b.want("meta_3_value") {
		nMeta = 3
	}
	gap := 48
	cellW := (p.contentW - gap*(nMeta-1)) / nMeta
	for i := 0; i < nMeta; i++ {
		x := p.mx + i*(cellW+gap)
		align := ""
		if i == nMeta-1 && nMeta > 1 {
			align = "right"
		} else if i > 0 {
			align = "center"
		}
		b.slot(fmt.Sprintf("meta_%d_label", i+1), x, 990, cellW, 22, p.chromeSize, p.inkDim, "", p.mono, align)
		b.slot(fmt.Sprintf("meta_%d_value", i+1), x, 1012, cellW, 24, p.chromeSize, p.secondary, "", p.mono, align)
	}
	b.shape("chrome-hairline", "rect", p.mx, 960, p.contentW, 1, p.line, true)
}

func (b *recipeBuilder) layoutSplitNarrative() {
	y := b.header(true)
	p := b.pal
	leftW := 820
	rightX := p.mx + leftW + 48
	rightW := p.contentW - leftW - 48
	avail := b.bodyH(y)
	body1H := avail
	if b.want("body_2") {
		body1H = avail * 55 / 100
		b.slot("body_2", p.mx, y+body1H+16, leftW, avail-body1H-16, p.bodySize, p.ink, "", p.body, "")
	}
	b.slot("body_1", p.mx, y, leftW, body1H, p.bodySize, p.ink, "", p.body, "")
	itemH := avail / 3
	for i := 0; i < 3; i++ {
		iy := y + i*itemH
		if i > 0 {
			b.shape(fmt.Sprintf("item-rule-%d", i), "rect", rightX, iy-16, rightW, 1, p.card, true)
		}
		b.slot(fmt.Sprintf("item_%d_title", i+1), rightX, iy, rightW, 44, p.h3, p.ink, "700", p.display, "")
		b.slot(fmt.Sprintf("item_%d_body", i+1), rightX, iy+48, rightW, itemH-56, p.bodySize-2, p.secondary, "", p.body, "")
	}
	b.close()
}

func (b *recipeBuilder) layoutQuoteSplit() {
	y := b.header(true)
	p := b.pal
	leftW := 860
	rightX := p.mx + leftW + 40
	rightW := p.contentW - leftW - 40
	avail := b.bodyH(y)
	body1H := avail
	if b.want("body_2") {
		body1H = avail * 55 / 100
		b.slot("body_2", p.mx, y+body1H+16, leftW, avail-body1H-16, p.bodySize, p.ink, "", p.body, "")
	}
	b.slot("body_1", p.mx, y, leftW, body1H, p.bodySize, p.ink, "", p.body, "")
	attrH := 48
	quoteH := 200
	cardH := 28 + quoteH + 16 + attrH + 28
	b.shape("quote-card", "roundRect", rightX, y, rightW, cardH, p.card, false)
	b.shape("quote-bar", "rect", rightX, y, 6, cardH, p.accent, true)
	b.slot("quote", rightX+28, y+24, rightW-48, quoteH, p.h3, p.ink, "600", p.display, "")
	b.slot("attribution", rightX+28, y+24+quoteH+12, rightW-48, attrH, p.labelSize+1, p.secondary, "", p.body, "")
	b.close()
}

func (b *recipeBuilder) layoutTwoUp() {
	y := b.header(true)
	p := b.pal
	gap := 36
	colW := (p.contentW - gap) / 2
	colH := b.cappedBodyH(y, 460)
	bodyH := colH - 24 - 28 - 8 - 56 - 12 - 28
	if bodyH < 80 {
		bodyH = 80
	}
	emp := b.emphasis(2)
	for i := 0; i < 2; i++ {
		x := p.mx + i*(colW+gap)
		b.panel(fmt.Sprintf("col-card-%d", i+1), x, y, colW, colH, emp == i+1)
		b.slot(fmt.Sprintf("col_%d_kicker", i+1), x+28, y+24, colW-56, 28, p.labelSize, p.accent, "600", b.chromeFont(), "")
		b.slot(fmt.Sprintf("col_%d_title", i+1), x+28, y+56, colW-56, 56, p.h3+4, p.ink, "700", p.display, "")
		b.slot(fmt.Sprintf("col_%d_body", i+1), x+28, y+124, colW-56, bodyH, p.bodySize, p.ink, "", p.body, "")
	}
	b.close()
}

func (b *recipeBuilder) layoutThreeUp() {
	y := b.header(true)
	p := b.pal
	gap := 24
	colW := (p.contentW - 2*gap) / 3
	colH := b.cappedBodyH(y, 440)
	bodyH := colH - 20 - 28 - 8 - 48 - 12 - 24
	if bodyH < 80 {
		bodyH = 80
	}
	emp := b.emphasis(0)
	for i := 0; i < 3; i++ {
		x := p.mx + i*(colW+gap)
		b.panel(fmt.Sprintf("card-%d", i+1), x, y, colW, colH, emp == i+1)
		if !b.isProduct() && emp != i+1 {
			b.shape(fmt.Sprintf("card-bar-%d", i+1), "rect", x, y, 6, colH, p.accent, true)
		}
		b.slot(fmt.Sprintf("item_%d_kicker", i+1), x+24, y+20, colW-40, 28, p.labelSize, p.accent, "600", b.chromeFont(), "")
		b.slot(fmt.Sprintf("item_%d_title", i+1), x+24, y+52, colW-40, 48, p.h3, p.ink, "700", p.display, "")
		b.slot(fmt.Sprintf("item_%d_body", i+1), x+24, y+108, colW-40, bodyH, p.bodySize, p.ink, "", p.body, "")
	}
	b.close()
}

func (b *recipeBuilder) layoutStatRow() {
	y := b.header(true)
	p := b.pal
	n := 4
	if b.fills != nil && !b.want("stat_4_number") {
		n = 3
	}
	gap := 24
	colW := (p.contentW - (n-1)*gap) / n
	avail := b.bodyH(y)
	hasDetails := b.want("detail_1_title") || b.want("detail_2_title")
	colH := avail
	if hasDetails {
		colH = avail * 62 / 100
	} else if colH > 400 {
		colH = 400
	}
	captionH := colH - 20 - 24 - 8 - 88 - 12 - 24
	if captionH < 48 {
		captionH = 48
	}
	for i := 0; i < n; i++ {
		x := p.mx + i*(colW+gap)
		b.panel(fmt.Sprintf("stat-card-%d", i+1), x, y, colW, colH, false)
		if !b.isProduct() {
			b.shape(fmt.Sprintf("stat-bar-%d", i+1), "rect", x, y, 5, colH, p.accent, true)
		}
		b.slot(fmt.Sprintf("stat_%d_label", i+1), x+20, y+20, colW-36, 24, p.labelSize, p.accent, "600", b.chromeFont(), "")
		b.slot(fmt.Sprintf("stat_%d_number", i+1), x+20, y+52, colW-36, 88, p.stat, p.accent, "700", p.display, "")
		b.slot(fmt.Sprintf("stat_%d_caption", i+1), x+20, y+148, colW-36, captionH, p.bodySize-2, p.secondary, "", p.body, "")
	}
	if hasDetails {
		dy := y + colH + 24
		dh := b.contentBottom() - dy
		if dh < 120 {
			dh = 120
		}
		dw := (p.contentW - 24) / 2
		if b.want("detail_1_title") {
			b.panel("detail-card-1", p.mx, dy, dw, dh, false)
			b.slot("detail_1_kicker", p.mx+24, dy+16, dw-48, 22, p.labelSize, p.accent, "600", b.chromeFont(), "")
			b.slot("detail_1_title", p.mx+24, dy+42, dw-48, 36, p.h3, p.ink, "700", p.display, "")
			b.slot("detail_1_body", p.mx+24, dy+84, dw-48, dh-100, p.bodySize, p.secondary, "", p.body, "")
		}
		if b.want("detail_2_title") {
			x2 := p.mx + dw + 24
			b.panel("detail-card-2", x2, dy, dw, dh, false)
			b.slot("detail_2_kicker", x2+24, dy+16, dw-48, 22, p.labelSize, p.accent, "600", b.chromeFont(), "")
			b.slot("detail_2_title", x2+24, dy+42, dw-48, 36, p.h3, p.ink, "700", p.display, "")
			b.slot("detail_2_body", x2+24, dy+84, dw-48, dh-100, p.bodySize, p.secondary, "", p.body, "")
		}
	}
	b.close()
}

func (b *recipeBuilder) layoutNumberedGrid() {
	y := b.header(true)
	p := b.pal
	cols := 3
	gapX, gapY := 28, 28
	cellW := (p.contentW - (cols-1)*gapX) / cols
	avail := b.bodyH(y)
	cellH := (avail - gapY) / 2
	bodyH := cellH - 16 - 28 - 8 - 40 - 8 - 20
	if bodyH < 48 {
		bodyH = 48
	}
	for i := 0; i < 6; i++ {
		col := i % cols
		row := i / cols
		x := p.mx + col*(cellW+gapX)
		iy := y + row*(cellH+gapY)
		b.shape(fmt.Sprintf("grid-card-%d", i+1), "roundRect", x, iy, cellW, cellH, p.card, false)
		b.staticText(fmt.Sprintf("grid-idx-%d", i+1), x+20, iy+16, 80, 28, p.labelSize, p.accent, "700", p.body, "", fmt.Sprintf("%02d", i+1))
		b.slot(fmt.Sprintf("item_%d_title", i+1), x+20, iy+44, cellW-40, 40, p.h3-2, p.ink, "700", p.display, "")
		b.slot(fmt.Sprintf("item_%d_body", i+1), x+20, iy+88, cellW-40, bodyH, p.bodySize-2, p.secondary, "", p.body, "")
	}
	b.close()
}

func (b *recipeBuilder) layoutCalloutRail() {
	y := b.header(true)
	p := b.pal
	leftW := 980
	rightX := p.mx + leftW + 36
	rightW := p.contentW - leftW - 36
	avail := b.bodyH(y)
	body1H := avail
	if b.want("body_2") {
		body1H = avail * 55 / 100
		b.slot("body_2", p.mx, y+body1H+16, leftW, avail-body1H-16, p.bodySize, p.ink, "", p.body, "")
	}
	b.slot("body_1", p.mx, y, leftW, body1H, p.bodySize, p.ink, "", p.body, "")
	cardH := avail
	if cardH > 400 {
		cardH = 400
	}
	calloutBody := cardH - 24 - 28 - 8 - 56 - 12 - 24
	if calloutBody < 80 {
		calloutBody = 80
	}
	b.panel("callout-card", rightX, y, rightW, cardH, true)
	if !b.isProduct() {
		b.shape("callout-bar", "rect", rightX, y, 6, cardH, p.accent, true)
	}
	b.slot("callout_kicker", rightX+24, y+24, rightW-48, 28, p.labelSize, p.accent, "600", p.body, "")
	b.slot("callout_title", rightX+24, y+56, rightW-48, 56, p.h3, p.ink, "700", p.display, "")
	b.slot("callout_body", rightX+24, y+124, rightW-48, calloutBody, p.bodySize, p.ink, "", p.body, "")
	b.close()
}

func (b *recipeBuilder) layoutYearHero() {
	y := b.header(true)
	p := b.pal
	yearW := 420
	avail := b.bodyH(y)
	b.slot("year", p.mx, y+20, yearW, 160, 120, p.accent, "700", p.display, "")
	rightX := p.mx + yearW + 48
	rightW := p.contentW - yearW - 48
	itemH := avail / 3
	for i := 0; i < 3; i++ {
		iy := y + i*itemH
		if i > 0 {
			b.shape(fmt.Sprintf("year-rule-%d", i), "rect", rightX, iy-16, rightW, 1, p.card, true)
		}
		b.slot(fmt.Sprintf("item_%d_title", i+1), rightX, iy, rightW, 44, p.h3, p.ink, "700", p.display, "")
		b.slot(fmt.Sprintf("item_%d_body", i+1), rightX, iy+48, rightW, itemH-60, p.bodySize, p.secondary, "", p.body, "")
	}
	b.close()
}

func (b *recipeBuilder) layoutCloser() {
	if b.isProduct() {
		b.layoutCloserProduct()
		return
	}
	y := b.header(true)
	p := b.pal
	thesisH := 160
	b.slot("thesis", p.mx, y, p.contentW, thesisH, p.bodySize+2, p.ink, "", p.body, "")
	itemY := y + thesisH + 28
	gap := 24
	colW := (p.contentW - 2*gap) / 3
	colH := b.contentBottom() - itemY
	if colH < 180 {
		colH = 180
	}
	bodyH := colH - 16 - 24 - 8 - 48 - 12 - 24
	if bodyH < 64 {
		bodyH = 64
	}
	for i := 0; i < 3; i++ {
		x := p.mx + i*(colW+gap)
		b.panel(fmt.Sprintf("close-card-%d", i+1), x, itemY, colW, colH, false)
		b.staticText(fmt.Sprintf("close-idx-%d", i+1), x+20, itemY+16, 64, 24, p.labelSize, p.accent, "700", b.chromeFont(), "", fmt.Sprintf("%02d", i+1))
		b.slot(fmt.Sprintf("item_%d_title", i+1), x+20, itemY+44, colW-40, 48, p.h3-2, p.ink, "700", p.display, "")
		b.slot(fmt.Sprintf("item_%d_body", i+1), x+20, itemY+96, colW-40, bodyH, p.bodySize-2, p.secondary, "", p.body, "")
	}
	b.close()
}

func (b *recipeBuilder) layoutCloserProduct() {
	b.bg()
	p := b.pal
	b.paintProductRail()
	b.slot("headline", p.mx, 220, 1700, 110, 72, p.ink, "800", p.display, "")
	hy := 340
	if b.want("headline_2") {
		b.slot("headline_2", p.mx, hy, 1700, 90, 64, p.ink, "800", p.display, "")
		hy += 100
	}
	b.slot("thesis", p.mx, hy+12, 1600, 80, 22, p.secondary, "", p.display, "")
	chipY := 720
	chipH := b.contentBottom() - chipY
	if chipH < 160 {
		chipH = 160
	}
	gap := 24
	colW := (p.contentW - 2*gap) / 3
	for i := 0; i < 3; i++ {
		x := p.mx + i*(colW+gap)
		b.panel(fmt.Sprintf("close-card-%d", i+1), x, chipY, colW, chipH, false)
		b.staticText(fmt.Sprintf("close-idx-%d", i+1), x+24, chipY+20, 64, 24, p.labelSize, p.accent, "700", p.mono, "", fmt.Sprintf("%02d", i+1))
		b.slot(fmt.Sprintf("item_%d_title", i+1), x+24, chipY+48, colW-48, 40, p.h3-2, p.ink, "700", p.display, "")
		b.slot(fmt.Sprintf("item_%d_body", i+1), x+24, chipY+96, colW-48, chipH-116, p.bodySize-2, p.secondary, "", p.body, "")
	}
	b.close()
}

func (b *recipeBuilder) layoutStatementEvidence() {
	y := b.header(true)
	p := b.pal
	leftW := 860
	avail := b.bodyH(y)
	b.slot("body_1", p.mx, y, leftW, avail, p.bodySize+2, p.ink, "", p.body, "")
	rightX := p.mx + leftW + 40
	rightW := p.contentW - leftW - 40
	b.panel("evidence-panel", rightX, y, rightW, avail, false)
	b.slot("evidence_kicker", rightX+28, y+24, rightW-56, 24, p.labelSize, p.accent, "600", b.chromeFont(), "")
	rowH := (avail - 60) / 3
	for i := 0; i < 3; i++ {
		iy := y + 60 + i*rowH
		if i > 0 {
			b.shape(fmt.Sprintf("ev-rule-%d", i), "rect", rightX+28, iy-12, rightW-56, 1, p.line, true)
		}
		b.slot(fmt.Sprintf("evidence_%d_title", i+1), rightX+28, iy, rightW-56, 36, p.h3-2, p.ink, "700", p.display, "")
		b.slot(fmt.Sprintf("evidence_%d_body", i+1), rightX+28, iy+40, rightW-56, rowH-52, p.bodySize, p.secondary, "", p.body, "")
	}
	b.close()
}

func (b *recipeBuilder) layoutDataTable() {
	y := b.header(true)
	p := b.pal
	nCol := 4
	if b.fills != nil && !b.want("col_4") {
		nCol = 3
	}
	colW := p.contentW / nCol
	// Header row
	for i := 0; i < nCol; i++ {
		b.slot(fmt.Sprintf("col_%d", i+1), p.mx+i*colW, y, colW-16, 28, p.labelSize, p.secondary, "600", b.chromeFont(), "")
	}
	b.shape("table-head-rule", "rect", p.mx, y+36, p.contentW, 1, p.line, true)
	noteH := 0
	if b.want("table_note") {
		noteH = 48
	}
	rowH := (b.bodyH(y) - 52 - noteH) / 3
	if rowH < 72 {
		rowH = 72
	}
	for r := 0; r < 3; r++ {
		ry := y + 52 + r*rowH
		for c := 0; c < nCol; c++ {
			id := fmt.Sprintf("row_%d_col_%d", r+1, c+1)
			color := p.ink
			if c >= 2 {
				color = p.accent
			}
			b.slot(id, p.mx+c*colW, ry, colW-16, 72, p.bodySize+2, color, "600", p.display, "")
		}
		b.shape(fmt.Sprintf("table-rule-%d", r+1), "rect", p.mx, ry+rowH-16, p.contentW, 1, p.line, true)
	}
	if b.want("table_note") {
		b.slot("table_note", p.mx, y+52+3*rowH, p.contentW, 40, p.bodySize, p.secondary, "", p.body, "")
	}
	b.close()
}

func (b *recipeBuilder) layoutLayerStack() {
	y := b.header(true)
	p := b.pal
	if b.want("lede") {
		b.slot("lede", p.mx, y, p.contentW, 56, p.bodySize, p.secondary, "", p.body, "")
		y += 68
	}
	leftW := 840
	avail := b.bodyH(y)
	b.panel("stack-panel", p.mx, y, leftW, avail, false)
	b.slot("stack_label", p.mx+24, y+16, leftW-48, 24, p.labelSize, p.secondary, "600", b.chromeFont(), "")
	emp := b.emphasis(2)
	layers := []int{4, 3, 2, 1, 0}
	layerH := (avail - 64) / 5
	if layerH < 48 {
		layerH = 48
	}
	for i, layer := range layers {
		ly := y + 52 + i*layerH
		on := emp == layer
		b.panel(fmt.Sprintf("layer-%d", layer), p.mx+24, ly, leftW-48, layerH-12, on)
		b.staticText(fmt.Sprintf("layer-%d-idx", layer), p.mx+40, ly+18, 48, 24, 13, p.accent, "600", p.mono, "", fmt.Sprintf("L%d", layer))
		b.slot(fmt.Sprintf("layer_%d_name", layer), p.mx+92, ly+14, 400, 32, 18, p.ink, "600", p.body, "")
		b.slot(fmt.Sprintf("layer_%d_meta", layer), p.mx+500, ly+16, 300, 28, 14, p.secondary, "", b.chromeFont(), "right")
	}
	rightX := p.mx + leftW + 36
	rightW := p.contentW - leftW - 36
	cardGap := 16
	cardH := (avail - 2*cardGap) / 3
	for i := 0; i < 3; i++ {
		cy := y + i*(cardH+cardGap)
		b.panel(fmt.Sprintf("side-card-%d", i+1), rightX, cy, rightW, cardH, i == 2)
		b.slot(fmt.Sprintf("card_%d_kicker", i+1), rightX+24, cy+16, rightW-48, 22, p.labelSize, p.accent, "600", b.chromeFont(), "")
		b.slot(fmt.Sprintf("card_%d_title", i+1), rightX+24, cy+42, rightW-48, 32, 24, p.ink, "700", p.display, "")
		b.slot(fmt.Sprintf("card_%d_body", i+1), rightX+24, cy+78, rightW-48, cardH-90, 15, p.secondary, "", p.body, "")
	}
	b.close()
}

func (b *recipeBuilder) layoutProcessTerminal() {
	y := b.header(true)
	p := b.pal
	gap := 24
	colW := (p.contentW - 2*gap) / 3
	avail := b.bodyH(y)
	colH := avail * 42 / 100
	if colH < 180 {
		colH = 180
	}
	emp := b.emphasis(3)
	for i := 0; i < 3; i++ {
		x := p.mx + i*(colW+gap)
		b.panel(fmt.Sprintf("step-card-%d", i+1), x, y, colW, colH, emp == i+1)
		b.slot(fmt.Sprintf("step_%d_kicker", i+1), x+24, y+20, colW-48, 22, p.labelSize, p.accent, "600", b.chromeFont(), "")
		b.slot(fmt.Sprintf("step_%d_title", i+1), x+24, y+48, colW-48, 56, p.h3, p.ink, "700", p.display, "")
		b.slot(fmt.Sprintf("step_%d_body", i+1), x+24, y+112, colW-48, colH-128, p.bodySize, p.secondary, "", p.body, "")
	}
	ty := y + colH + 24
	th := b.contentBottom() - ty
	if th < 140 {
		th = 140
	}
	b.panel("terminal-panel", p.mx, ty, p.contentW, th, false)
	b.slot("terminal_kicker", p.mx+24, ty+16, p.contentW-48, 22, 13, p.inkDim, "", p.mono, "")
	b.slot("terminal_body", p.mx+24, ty+44, p.contentW-48, th-64, 17, p.ink, "", p.mono, "")
	b.close()
}
