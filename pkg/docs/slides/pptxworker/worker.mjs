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

  const render = (slide, node, ox = 0, oy = 0) => {
    const box = options(node)
    box.x += ox
    box.y += oy
    switch (node.type) {
      case 'text':
        slide.addText(node.runs?.length ? node.runs.map(run => ({ text: run.text, options: { bold: run.bold, italic: run.italic, color: color(run.color) } })) : node.text || '', {
          ...box, fontFace: node.props?.fontFamily || 'Aptos', fontSize: Number(node.props?.fontSize || 24),
          bold: Boolean(node.props?.bold), color: color(node.props?.color), margin: Number(node.props?.margin || 0),
          breakLine: false, fit: 'shrink', valign: node.props?.valign || 'mid',
        })
        counts.native++
        break
      case 'shape':
        slide.addShape(pptx.ShapeType[node.props?.kind] || pptx.ShapeType.rect, {
          ...box, fill: { color: color(node.props?.fill || 'FFFFFF') },
          line: { color: color(node.props?.line || '172033'), width: Number(node.props?.lineWidth || 1) },
        })
        counts.native++
        break
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
