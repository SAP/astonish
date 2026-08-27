package slides

import "fmt"

// Layout geometry is 1920×1080. Cards hug their copy (padding + 3–6 lines);
// they do not stretch to the footer. Footer lives in the bottom 48px.

func (b *recipeBuilder) layoutCover() {
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

func (b *recipeBuilder) layoutSplitNarrative() {
	y := b.header(true)
	p := b.pal
	leftW := 820
	rightX := p.mx + leftW + 48
	rightW := p.contentW - leftW - 48
	body1H := 280
	if !b.want("body_2") {
		body1H = 500
	}
	b.slot("body_1", p.mx, y, leftW, body1H, p.bodySize, p.ink, "", p.body, "")
	if b.want("body_2") {
		b.slot("body_2", p.mx, y+300, leftW, 240, p.bodySize, p.ink, "", p.body, "")
	}
	itemH := 170
	for i := 0; i < 3; i++ {
		iy := y + i*itemH
		if i > 0 {
			b.shape(fmt.Sprintf("item-rule-%d", i), "rect", rightX, iy-16, rightW, 1, p.card, true)
		}
		b.slot(fmt.Sprintf("item_%d_title", i+1), rightX, iy, rightW, 44, p.h3, p.ink, "700", p.display, "")
		b.slot(fmt.Sprintf("item_%d_body", i+1), rightX, iy+48, rightW, 96, p.bodySize-2, p.secondary, "", p.body, "")
	}
	b.close()
}

func (b *recipeBuilder) layoutQuoteSplit() {
	y := b.header(true)
	p := b.pal
	leftW := 860
	rightX := p.mx + leftW + 40
	rightW := p.contentW - leftW - 40
	body1H := 280
	if !b.want("body_2") {
		body1H = 420
	}
	b.slot("body_1", p.mx, y, leftW, body1H, p.bodySize, p.ink, "", p.body, "")
	if b.want("body_2") {
		b.slot("body_2", p.mx, y+300, leftW, 240, p.bodySize, p.ink, "", p.body, "")
	}
	quoteH := 168
	attrH := 48
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
	bodyH := 176
	colH := 24 + 28 + 8 + 56 + 12 + bodyH + 28
	for i := 0; i < 2; i++ {
		x := p.mx + i*(colW+gap)
		b.shape(fmt.Sprintf("col-card-%d", i+1), "roundRect", x, y, colW, colH, p.card, false)
		b.slot(fmt.Sprintf("col_%d_kicker", i+1), x+28, y+24, colW-56, 28, p.labelSize, p.accent, "600", p.body, "")
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
	bodyH := 148
	colH := 20 + 28 + 8 + 48 + 12 + bodyH + 24
	for i := 0; i < 3; i++ {
		x := p.mx + i*(colW+gap)
		b.shape(fmt.Sprintf("card-%d", i+1), "roundRect", x, y, colW, colH, p.card, false)
		b.shape(fmt.Sprintf("card-bar-%d", i+1), "rect", x, y, 6, colH, p.accent, true)
		b.slot(fmt.Sprintf("item_%d_kicker", i+1), x+24, y+20, colW-40, 28, p.labelSize, p.accent, "600", p.body, "")
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
	captionH := 96
	colH := 20 + 24 + 8 + 88 + 12 + captionH + 24
	for i := 0; i < n; i++ {
		x := p.mx + i*(colW+gap)
		b.shape(fmt.Sprintf("stat-card-%d", i+1), "roundRect", x, y, colW, colH, p.card, false)
		b.shape(fmt.Sprintf("stat-bar-%d", i+1), "rect", x, y, 5, colH, p.accent, true)
		b.slot(fmt.Sprintf("stat_%d_label", i+1), x+20, y+20, colW-36, 24, p.labelSize, p.accent, "600", p.body, "")
		b.slot(fmt.Sprintf("stat_%d_number", i+1), x+20, y+52, colW-36, 88, 64, p.ink, "700", p.display, "")
		b.slot(fmt.Sprintf("stat_%d_caption", i+1), x+20, y+148, colW-36, captionH, p.bodySize-2, p.secondary, "", p.body, "")
	}
	b.close()
}

func (b *recipeBuilder) layoutNumberedGrid() {
	y := b.header(true)
	p := b.pal
	cols := 3
	gapX, gapY := 28, 28
	cellW := (p.contentW - (cols-1)*gapX) / cols
	bodyH := 108
	cellH := 16 + 28 + 8 + 40 + 8 + bodyH + 20
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
	body1H := 280
	if !b.want("body_2") {
		body1H = 420
	}
	b.slot("body_1", p.mx, y, leftW, body1H, p.bodySize, p.ink, "", p.body, "")
	if b.want("body_2") {
		b.slot("body_2", p.mx, y+300, leftW, 240, p.bodySize, p.ink, "", p.body, "")
	}
	calloutBody := 140
	cardH := 24 + 28 + 8 + 56 + 12 + calloutBody + 24
	b.shape("callout-card", "roundRect", rightX, y, rightW, cardH, mixHex(p.surface, p.accent, 0.12), false)
	b.shape("callout-bar", "rect", rightX, y, 6, cardH, p.accent, true)
	b.slot("callout_kicker", rightX+24, y+24, rightW-48, 28, p.labelSize, p.accent, "600", p.body, "")
	b.slot("callout_title", rightX+24, y+56, rightW-48, 56, p.h3, p.ink, "700", p.display, "")
	b.slot("callout_body", rightX+24, y+124, rightW-48, calloutBody, p.bodySize, p.ink, "", p.body, "")
	b.close()
}

func (b *recipeBuilder) layoutYearHero() {
	y := b.header(true)
	p := b.pal
	yearW := 420
	b.slot("year", p.mx, y+20, yearW, 160, 120, p.accent, "700", p.display, "")
	rightX := p.mx + yearW + 48
	rightW := p.contentW - yearW - 48
	itemH := 170
	for i := 0; i < 3; i++ {
		iy := y + i*itemH
		if i > 0 {
			b.shape(fmt.Sprintf("year-rule-%d", i), "rect", rightX, iy-16, rightW, 1, p.card, true)
		}
		b.slot(fmt.Sprintf("item_%d_title", i+1), rightX, iy, rightW, 44, p.h3, p.ink, "700", p.display, "")
		b.slot(fmt.Sprintf("item_%d_body", i+1), rightX, iy+48, rightW, 100, p.bodySize, p.secondary, "", p.body, "")
	}
	b.close()
}

func (b *recipeBuilder) layoutCloser() {
	y := b.header(true)
	p := b.pal
	thesisH := 160
	b.slot("thesis", p.mx, y, p.contentW, thesisH, p.bodySize+2, p.ink, "", p.body, "")
	itemY := y + thesisH + 28
	gap := 24
	colW := (p.contentW - 2*gap) / 3
	bodyH := 120
	colH := 16 + 24 + 8 + 48 + 12 + bodyH + 24
	for i := 0; i < 3; i++ {
		x := p.mx + i*(colW+gap)
		b.shape(fmt.Sprintf("close-card-%d", i+1), "roundRect", x, itemY, colW, colH, p.card, false)
		b.staticText(fmt.Sprintf("close-idx-%d", i+1), x+20, itemY+16, 64, 24, p.labelSize, p.accent, "700", p.body, "", fmt.Sprintf("%02d", i+1))
		b.slot(fmt.Sprintf("item_%d_title", i+1), x+20, itemY+44, colW-40, 48, p.h3-2, p.ink, "700", p.display, "")
		b.slot(fmt.Sprintf("item_%d_body", i+1), x+20, itemY+96, colW-40, bodyH, p.bodySize-2, p.secondary, "", p.body, "")
	}
	b.close()
}
