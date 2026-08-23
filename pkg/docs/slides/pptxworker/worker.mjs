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
  pptx.layout = 'LAYOUT_WIDE'
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
      case 'text':
        slide.addText(node.runs?.length ? node.runs.map(run => ({
          text: run.text,
          options: {
            bold: run.bold, italic: run.italic, underline: run.underline ? { style: 'sng' } : undefined,
            color: color(run.color), fontFace: run.font || undefined, fontSize: run.size ? Number(run.size) : undefined,
          },
        })) : node.text || '', {
          ...box, fontFace: node.props?.fontFamily || 'Aptos', fontSize: Number(node.props?.fontSize || 24),
          bold: Boolean(node.props?.bold), color: color(node.props?.color), margin: Number(node.props?.margin || 0),
          breakLine: false, fit: 'shrink', valign: node.props?.valign || 'mid',
          ...(node.rot ? { rotate: Number(node.rot) } : {}),
        })
        counts.native++
        break
      case 'shape': {
        const shapeOpts = {
          ...box,
          line: {
            color: color(node.line || node.props?.line || '172033'),
            width: Number(node.props?.lineWidth || 1),
            dashType: dashType(node.dash || node.props?.dash),
          },
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
        const headArrow = arrowType(node.props?.headEnd || node.props?.beginArrow)
        const tailArrow = arrowType(node.props?.tailEnd || node.props?.endArrow)
        if (headArrow) shapeOpts.line.beginArrowType = headArrow
        if (tailArrow) shapeOpts.line.endArrowType = tailArrow
        slide.addShape(shapeTypeFor(node), shapeOpts)
        counts.native++
        break
      }
      case 'image':
        if (!node.props?.data) throw new Error(`image ${node.id} has no validated data`)
        slide.addImage({ ...box, data: node.props.data })
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
