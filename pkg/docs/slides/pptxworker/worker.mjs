import { createRequire } from 'node:module'

const require = createRequire(`${process.cwd()}/package.json`)
const pptxgen = require('pptxgenjs')

let input = ''
for await (const chunk of process.stdin) input += chunk

try {
  const request = JSON.parse(input)
  if (request.protocolVersion !== 1) throw new Error(`unsupported protocol ${request.protocolVersion}`)
  const scene = request.scene
  const pptx = new pptxgen()
  // The scene canvas is 1920x1080 logical units at 160 units/inch (UnitsPerInch
  // in types.go) == 12in x 6.75in. pptxgenjs LAYOUT_WIDE is 13.333in x 7.5in, so
  // using it would place the 12in-wide content on a wider slide, leaving all the
  // slack on the right/bottom and shifting every element up-and-left of true
  // center (a centered title landed ~5% left of the slide center). Define a
  // custom layout that matches the canvas 1:1 so element geometry — and thus
  // centering — is identical to the HTML/PDF exports.
  pptx.defineLayout({ name: 'ASTONISH_CANVAS', width: 1920 / 160, height: 1080 / 160 })
  pptx.layout = 'ASTONISH_CANVAS'
  pptx.author = scene.author || 'Astonish'
  pptx.subject = scene.subject || ''
  pptx.title = scene.title || ''
  pptx.company = 'Astonish'
  pptx.lang = 'en-US'
  pptx.theme = {
    headFontFace: scene.theme?.displayFont || 'Aptos Display',
    bodyFontFace: scene.theme?.bodyFont || 'Aptos',
    lang: 'en-US',
  }

  const counts = { native: 0, vector: 0, raster: 0, unsupported: 0 }
  const warnings = []
  const inch = value => Number(value || 0) / 160
  const options = node => ({
    x: inch(node.geometry?.x), y: inch(node.geometry?.y),
    w: inch(node.geometry?.w), h: inch(node.geometry?.h),
  })
  const color = value => String(value || '172033').replace(/^#/, '')

  // Theme-token resolution mirrors the HTML export (:root defaults in
  // export_html.go). Token strings match theme-map keys verbatim.
  const themeDefaults = { ink: '172033', surface: 'FFFFFF', 'ink-muted': '64748B', accent: '1E40AF', 'accent-soft': 'DBEAFE' }
  // resolveColorToken returns a 6-hex string, or null so pptxgenjs can inherit.
  const resolveColorToken = (raw, token) => {
    if (raw) return color(raw)
    if (token) {
      const v = scene.theme?.[token] || themeDefaults[token]
      if (v) return color(v)
    }
    return null
  }
  // resolveFont prefers an explicit font, then maps token 'display' to the
  // display font family and anything else to the body font family.
  const displayFont = () => scene.theme?.displayFont || scene.theme?.display || 'Aptos Display'
  const bodyFont = () => scene.theme?.bodyFont || scene.theme?.['body-font'] || 'Aptos'
  const resolveFont = (raw, token) => {
    if (raw) return raw
    if (token === 'display') return displayFont()
    if (token && token !== 'body-font') return scene.theme?.[token] || bodyFont()
    return bodyFont()
  }
  // Font size is authored in CSS px on the 1920x1080 logical canvas, where
  // 160 canvas units == 1 inch (UnitsPerInch). points = px * 72 / 160.
  const ptFromPx = px => Math.round((Number(px) || 0) * 72 / 160 * 100) / 100
  const alignMap = { l: 'left', left: 'left', ctr: 'center', center: 'center', centre: 'center', c: 'center', r: 'right', right: 'right', justify: 'justify', justified: 'justify' }
  const anchorMap = { ctr: 'middle', center: 'middle', middle: 'middle', b: 'bottom', bottom: 'bottom', t: 'top', top: 'top' }

  const dashMap = { solid: 'solid', dash: 'dash', dashed: 'dash', dot: 'dot', dotted: 'dot', lgDash: 'lgDash', dashDot: 'dashDot' }
  const dashType = value => dashMap[String(value || '')] || 'solid'

  const geomMap = {
    rect: 'rect', rectangle: 'rect', roundRect: 'roundRect', ellipse: 'ellipse',
    oval: 'ellipse', circle: 'ellipse', triangle: 'triangle', line: 'line',
    diamond: 'diamond', hexagon: 'hexagon', pentagon: 'pentagon', chevron: 'chevron',
    arrow: 'rightArrow', rightArrow: 'rightArrow', leftArrow: 'leftArrow',
    star: 'star5', cloud: 'cloud',
  }
  const shapeTypeFor = node => {
    // Custom path geometry is not expressible with pptxgenjs presets.
    if (node.path) {
      warnings.push(`Custom geometry approximated as rectangle (${node.id || 'unknown'})`)
      return pptx.ShapeType.rect
    }
    const preset = node.geom || node.props?.kind
    if (!preset) return pptx.ShapeType.rect
    const mapped = geomMap[preset]
    if (mapped && pptx.ShapeType[mapped]) return pptx.ShapeType[mapped]
    if (pptx.ShapeType[preset]) return pptx.ShapeType[preset]
    warnings.push(`Unknown shape preset ${preset} approximated as rectangle (${node.id || 'unknown'})`)
    return pptx.ShapeType.rect
  }

  const arrowMap = { arrow: 'triangle', triangle: 'triangle', stealth: 'stealth', diamond: 'diamond', oval: 'oval', open: 'arrow', none: 'none' }
  const arrowType = value => arrowMap[String(value || '')] || null

  const render = (slide, node, ox = 0, oy = 0) => {
    const box = options(node)
    box.x += ox
    box.y += oy
    switch (node.type) {
      case 'text': {
        const p = node.props || {}
        const sizePx = p.size != null ? Number(p.size) : 32
        const fontSize = ptFromPx(sizePx)
        const weightNum = Number(p.weight)
        const isBold = (!Number.isNaN(weightNum) && weightNum >= 600) || p.weight === 'bold'
        const boxColor = resolveColorToken(p.color, p['color-token'] || 'ink')
        const boxFont = resolveFont(p.font, p['font-token'] || 'body-font')
        const boxAlign = alignMap[String(p.align || 'left')] || 'left'
        const boxValign = anchorMap[String(p.anchor || 'top')] || 'top'
        slide.addText(node.runs?.length ? node.runs.map(run => ({
          text: run.text,
          options: {
            bold: run.bold, italic: run.italic, underline: run.underline ? { style: 'sng' } : undefined,
            color: run.color ? color(run.color) : undefined,
            fontFace: run.font || undefined,
            fontSize: run.size ? ptFromPx(run.size) : undefined,
          },
        })) : node.text || '', {
          ...box, fontFace: boxFont, fontSize, bold: isBold,
          ...(boxColor ? { color: boxColor } : {}),
          align: boxAlign, valign: boxValign, margin: Number(p.inset || 0),
          breakLine: false, fit: 'shrink', wrap: true,
          ...(node.rot ? { rotate: Number(node.rot) } : {}),
        })
        counts.native++
        break
      }
      case 'shape': {
        const shapeOpts = {
          ...box,
        }
        // Border: only draw an outline when the deck actually authored one.
        // The HTML export gives a shape no border unless a line is specified,
        // so PPTX must match — otherwise fill-only shapes (e.g. a full-slide
        // background/frame panel) gain a spurious 1pt rectangle. A line is
        // considered authored when a line color, an explicit width, a dash, or
        // an arrow end is present.
        const lineColor = node.line || node.props?.line
        const hasLineWidth = node.props?.lineWidth != null
        const hasDash = node.dash != null || node.props?.dash != null
        const headArrow = arrowType(node.props?.headEnd || node.props?.beginArrow)
        const tailArrow = arrowType(node.props?.tailEnd || node.props?.endArrow)
        if (lineColor || hasLineWidth || hasDash || headArrow || tailArrow) {
          shapeOpts.line = {
            color: color(lineColor || '172033'),
            width: Number(node.props?.lineWidth || 1),
            dashType: dashType(node.dash || node.props?.dash),
          }
          if (headArrow) shapeOpts.line.beginArrowType = headArrow
          if (tailArrow) shapeOpts.line.endArrowType = tailArrow
        } else {
          // No authored outline: declare an explicit transparent line so no
          // rectangle is inherited from the theme/master.
          shapeOpts.line = { color: 'FFFFFF', transparency: 100 }
        }
        // Fill: node-level fill / props.fill / gradient (approximated as solid first stop).
        if (node.gradient?.stops?.length) {
          shapeOpts.fill = { color: color(node.gradient.stops[0].color) }
          warnings.push(`Gradient approximated as solid fill (${node.id || 'unknown'})`)
        } else {
          shapeOpts.fill = { color: color(node.fill || node.props?.fill || 'FFFFFF') }
        }
        if (typeof node.opacity === 'number' && node.opacity > 0 && node.opacity < 1) {
          shapeOpts.fill.transparency = Math.round((1 - node.opacity) * 100)
        }
        if (node.rot) shapeOpts.rotate = Number(node.rot)
        if (node.flipH) shapeOpts.flipH = true
        if (node.flipV) shapeOpts.flipV = true
        slide.addShape(shapeTypeFor(node), shapeOpts)
        counts.native++
        break
      }
      case 'image':
        if (!node.props?.data) throw new Error(`image ${node.id} has no validated data`)
        slide.addImage({
          ...box, data: node.props.data,
          ...(node.flipH ? { flipH: true } : {}),
          ...(node.flipV ? { flipV: true } : {}),
          ...(node.rot ? { rotate: Number(node.rot) } : {}),
        })
        counts.native++
        break
      case 'table':
        slide.addTable(node.table?.rows || [], { ...box, border: { type: 'solid', color: 'CBD5E1', pt: 1 }, fontFace: 'Aptos', fontSize: 14 })
        counts.native++
        break
      case 'chart': {
        const chartType = pptx.ChartType[node.props?.kind || 'bar'] || pptx.ChartType.bar
        slide.addChart(chartType, node.series || [], { ...box, showLegend: true, showTitle: false })
        counts.native++
        break
      }
      case 'group':
        counts.native++
        for (const child of node.children || []) render(slide, child, box.x, box.y)
        break
      default:
        counts.unsupported++
        warnings.push(`Unsupported component ${node.type} (${node.id || 'unknown'})`)
    }
  }

  for (const sourceSlide of scene.slides || []) {
    const slide = pptx.addSlide()
    slide.background = { color: color(scene.theme?.surface || 'FFFFFF') }
    for (const node of sourceSlide.nodes || []) render(slide, node)
    if (sourceSlide.notes) slide.addNotes(sourceSlide.notes)
  }

  if (request.strictNative && (counts.vector || counts.raster || counts.unsupported)) {
    throw new Error('strict-native export contains non-native components')
  }
  const buffer = await pptx.write({ outputType: 'nodebuffer', compression: true })
  process.stdout.write(JSON.stringify({ protocolVersion: 1, pptxBase64: Buffer.from(buffer).toString('base64'), ...counts, warnings }))
} catch (error) {
  process.stdout.write(JSON.stringify({ protocolVersion: 1, error: error instanceof Error ? error.message : String(error), native: 0, vector: 0, raster: 0, unsupported: 0 }))
}
