// PPTX → PNG thumbnail worker for slides-diag.
//
// Renders the original .pptx with pptx-glimpse (in-process SVG/PNG, no LibreOffice),
// not the import IR. One JSON request on stdin, one JSON response on stdout.
// Spawn with cwd = web/ so node_modules resolve.
//
// Request:  { "protocolVersion": 1, "pptxBase64": "...", "width": 480 }
// Response: { "protocolVersion": 1, "slides": [{ "slideNumber": 1, "pngBase64": "...", "width": 480, "height": 270, "warnings": [] }], "warnings": [], "error": "" }

import { createRequire } from 'node:module'
import { pathToFileURL } from 'node:url'

const PROTOCOL = 1

const readStdin = async () => {
  const chunks = []
  for await (const chunk of process.stdin) chunks.push(chunk)
  return Buffer.concat(chunks).toString('utf8')
}

const fail = (message) => {
  process.stdout.write(JSON.stringify({ protocolVersion: PROTOCOL, error: String(message) }))
  process.exit(0)
}

const warnText = (d) => {
  if (!d) return ''
  if (typeof d === 'string') return d
  const code = d.code || d.kind || d.severity || ''
  const msg = d.message || d.text || JSON.stringify(d)
  return code ? `[${code}] ${msg}` : String(msg)
}

const main = async () => {
  let req
  try {
    req = JSON.parse(await readStdin() || '{}')
  } catch (err) {
    fail(`invalid JSON request: ${err.message}`)
    return
  }
  if (req.protocolVersion && req.protocolVersion !== PROTOCOL) {
    fail(`unsupported protocol ${req.protocolVersion}`)
    return
  }
  if (!req.pptxBase64) {
    fail('pptxBase64 is required')
    return
  }
  let bytes
  try {
    bytes = Buffer.from(req.pptxBase64, 'base64')
  } catch (err) {
    fail(`pptxBase64 decode: ${err.message}`)
    return
  }
  const width = Number(req.width) > 0 ? Number(req.width) : 480

  let convertPptxToPng
  let result
  try {
    ;({ convertPptxToPng } = await import(pathToFileURL(createRequire(process.cwd() + '/package.json').resolve('pptx-glimpse')).href))
  } catch (err) {
    fail(`pptx-glimpse is not installed in the worker working directory (web/node_modules): ${err.message}`)
    return
  }
  try {
    result = await convertPptxToPng(bytes, { width })
  } catch (err) {
    try {
      result = await convertPptxToPng(bytes)
    } catch (err2) {
      fail(`render: ${err2.message || err.message}`)
      return
    }
  }

  const rendered = result?.slides || result || []
  const warnings = (result?.diagnostics || []).map(warnText).filter(Boolean)
  const slides = []
  for (let i = 0; i < rendered.length; i++) {
    const slide = rendered[i]
    const png = slide?.png || slide?.image || slide?.data
    if (!png) {
      warnings.push(`slide ${i + 1}: empty png`)
      slides.push({ slideNumber: i + 1, pngBase64: '', width: 0, height: 0, warnings: ['empty png'] })
      continue
    }
    const buf = Buffer.isBuffer(png) ? png : Buffer.from(png)
    slides.push({
      slideNumber: slide.slideNumber || slide.index + 1 || i + 1,
      pngBase64: buf.toString('base64'),
      width: slide.width || width,
      height: slide.height || 0,
      warnings: (slide.diagnostics || slide.warnings || []).map(warnText).filter(Boolean),
    })
  }

  process.stdout.write(JSON.stringify({ protocolVersion: PROTOCOL, slides, warnings }))
}

main().catch((err) => fail(err?.stack || err?.message || String(err)))
