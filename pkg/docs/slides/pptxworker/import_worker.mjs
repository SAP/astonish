import { createRequire } from 'node:module'
import { createHash } from 'node:crypto'

const require = createRequire(`${process.cwd()}/package.json`)
const JSZip = require('jszip')
const { XMLParser } = require('fast-xml-parser')

// ---------------------------------------------------------------------------
// Constants / limits
// ---------------------------------------------------------------------------
const EMU_PER_PX = 9525 // 1px @96dpi = 9525 EMU
const CANVAS_W = 1920
const CANVAS_H = 1080
const MAX_TOTAL_UNCOMPRESSED = 200 * 1024 * 1024 // 200MB zip-bomb guard
const MAX_ENTRIES = 5000

const warnings = []
const warn = (m) => { warnings.push(String(m)) }

// ---------------------------------------------------------------------------
// preserveOrder XML helpers. Each node is an object like { 'p:sp': [...], ':@': {attrs} }.
// ---------------------------------------------------------------------------
const parser = new XMLParser({
  ignoreAttributes: false,
  preserveOrder: true,
  attributeNamePrefix: '@_',
  parseAttributeValue: false,
  trimValues: true,
})

// Return the tag name of a preserveOrder node (the single non-":@" key).
const tagOf = (node) => {
  if (!node || typeof node !== 'object') return null
  for (const k of Object.keys(node)) if (k !== ':@') return k
  return null
}
const attrsOf = (node) => (node && node[':@']) || {}
const childrenOf = (node) => {
  const t = tagOf(node)
  if (!t) return []
  const v = node[t]
  return Array.isArray(v) ? v : []
}
// Direct children matching a tag.
const findAll = (node, tag) => childrenOf(node).filter((c) => tagOf(c) === tag)
const findChild = (node, tag) => childrenOf(node).find((c) => tagOf(c) === tag) || null
// Deep first match (DFS).
const findDeep = (node, tag) => {
  for (const c of childrenOf(node)) {
    if (tagOf(c) === tag) return c
    const inner = findDeep(c, tag)
    if (inner) return inner
  }
  return null
}
// Text content of a preserveOrder node (concatenate #text children).
const textOf = (node) => {
  const t = tagOf(node)
  if (!t) return ''
  const v = node[t]
  if (!Array.isArray(v)) return ''
  let out = ''
  for (const c of v) if (c && typeof c === 'object' && '#text' in c) out += String(c['#text'])
  return out
}

const parseXml = (str) => {
  const arr = parser.parse(str)
  return Array.isArray(arr) ? arr : []
}
// Find a top-level element by tag from a parsed document array.
const rootEl = (docArr, tag) => docArr.find((c) => tagOf(c) === tag) || null

// ---------------------------------------------------------------------------
// Color handling
// ---------------------------------------------------------------------------
const clamp255 = (n) => Math.max(0, Math.min(255, Math.round(n)))
const hexToRgb = (hex) => {
  const h = String(hex || '').replace(/^#/, '').padStart(6, '0').slice(0, 6)
  return { r: parseInt(h.slice(0, 2), 16), g: parseInt(h.slice(2, 4), 16), b: parseInt(h.slice(4, 6), 16) }
}
const rgbToHex = ({ r, g, b }) => [r, g, b].map((v) => clamp255(v).toString(16).padStart(2, '0')).join('').toUpperCase()

// Apply an OOXML color transform in document order. mods is an array of
// { op, val } where val is 0..1 (parsed from the @_val "per-mille"/percent thousandths).
const applyMods = (hex, mods) => {
  let { r, g, b } = hexToRgb(hex)
  for (const { op, val } of mods) {
    switch (op) {
      case 'lumMod':
        r *= val; g *= val; b *= val
        break
      case 'lumOff':
        r += val * 255; g += val * 255; b += val * 255
        break
      case 'tint': // toward white
        r = r * val + 255 * (1 - val); g = g * val + 255 * (1 - val); b = b * val + 255 * (1 - val)
        break
      case 'shade': // toward black
        r = r * val; g = g * val; b = b * val
        break
      default:
        break
    }
  }
  return rgbToHex({ r, g, b })
}

// A percentage-ish attribute value in OOXML is stored as thousandths of a percent
// (e.g. 60000 => 60%). Return 0..1.
const pct = (v) => {
  const n = Number(v)
  if (!Number.isFinite(n)) return 1
  return n / 100000
}

// Build the theme color map from a:clrScheme.
const buildThemeColors = (themeDoc) => {
  const map = {}
  const theme = rootEl(themeDoc, 'a:theme')
  if (!theme) return map
  const elements = findChild(theme, 'a:themeElements')
  if (!elements) return map
  const scheme = findChild(elements, 'a:clrScheme')
  if (!scheme) return map
  for (const entry of childrenOf(scheme)) {
    const name = (tagOf(entry) || '').replace(/^a:/, '')
    const srgb = findChild(entry, 'a:srgbClr')
    const sys = findChild(entry, 'a:sysClr')
    if (srgb) map[name] = String(attrsOf(srgb)['@_val'] || '000000').toUpperCase()
    else if (sys) {
      const last = attrsOf(sys)['@_lastClr']
      const v = attrsOf(sys)['@_val']
      if (last) map[name] = String(last).toUpperCase()
      else map[name] = v === 'window' ? 'FFFFFF' : '000000'
    }
  }
  return map
}

// Resolve a color container element (solidFill child like a:srgbClr / a:schemeClr /
// a:sysClr / a:prstClr) into { hex, alpha }.
const SCHEME_ALIAS = { bg1: 'lt1', tx1: 'dk1', bg2: 'lt2', tx2: 'dk2', phClr: 'accent1' }
const resolveColorEl = (el, themeColors) => {
  if (!el) return null
  const t = tagOf(el)
  const attrs = attrsOf(el)
  let base = null
  if (t === 'a:srgbClr') base = String(attrs['@_val'] || '000000').toUpperCase()
  else if (t === 'a:sysClr') {
    const last = attrs['@_lastClr']
    base = last ? String(last).toUpperCase() : (attrs['@_val'] === 'window' ? 'FFFFFF' : '000000')
  } else if (t === 'a:schemeClr') {
    let name = String(attrs['@_val'] || '')
    if (SCHEME_ALIAS[name]) name = SCHEME_ALIAS[name]
    base = themeColors[name] || themeColors[SCHEME_ALIAS[name]] || '000000'
  } else if (t === 'a:prstClr') {
    base = '808080' // preset colors not resolved; grey fallback
  } else return null

  const mods = []
  let alpha = 1
  for (const c of childrenOf(el)) {
    const ct = (tagOf(c) || '').replace(/^a:/, '')
    const val = attrsOf(c)['@_val']
    if (ct === 'lumMod' || ct === 'lumOff' || ct === 'tint' || ct === 'shade') mods.push({ op: ct, val: pct(val) })
    else if (ct === 'alpha') alpha = pct(val)
  }
  return { hex: '#' + applyMods(base, mods), alpha }
}

// Find the first color inside a fill container (a:solidFill etc.).
const colorFromFill = (fillEl, themeColors) => {
  if (!fillEl) return null
  for (const c of childrenOf(fillEl)) {
    const r = resolveColorEl(c, themeColors)
    if (r) return r
  }
  return null
}

// ---------------------------------------------------------------------------
// Geometry
// ---------------------------------------------------------------------------
const int = (n) => Math.round(Number(n) || 0)

// ---------------------------------------------------------------------------
// Shape preset mapping (OOXML prst -> our geom set)
// ---------------------------------------------------------------------------
const GEOM_MAP = {
  rect: 'rect', roundRect: 'roundRect', ellipse: 'ellipse', oval: 'ellipse',
  triangle: 'triangle', diamond: 'diamond', line: 'line', straightConnector1: 'line',
  rightArrow: 'rightArrow', leftArrow: 'leftArrow', chevron: 'chevron',
  homePlate: 'chevron', pentagon: 'pentagon', hexagon: 'hexagon', star5: 'star',
  parallelogram: 'rect', trapezoid: 'rect', flowChartProcess: 'rect',
}
const mapGeom = (prst, id) => {
  if (!prst) return 'rect'
  const m = GEOM_MAP[prst]
  if (m) return m
  warn(`Unknown shape preset ${prst} approximated as rect (${id || 'unknown'})`)
  return 'rect'
}

const DASH_MAP = { solid: 'solid', dash: 'dash', dashDot: 'dashDot', lgDash: 'lgDash', dot: 'dot', sysDash: 'dash', sysDot: 'dot' }

// ---------------------------------------------------------------------------
// Rich text extraction: a:txBody -> runs[]
// ---------------------------------------------------------------------------
const extractRuns = (txBody, themeColors) => {
  const runs = []
  let text = ''
  if (!txBody) return { runs, text }
  for (const p of findAll(txBody, 'a:p')) {
    for (const r of findAll(p, 'a:r')) {
      const rPr = findChild(r, 'a:rPr')
      const t = findChild(r, 'a:t')
      const str = t ? textOf(t) : ''
      if (!str) continue
      const attrs = attrsOf(rPr)
      const run = { text: str }
      if (attrs['@_b'] === '1' || attrs['@_b'] === 'true') run.bold = true
      if (attrs['@_i'] === '1' || attrs['@_i'] === 'true') run.italic = true
      if (attrs['@_u'] && attrs['@_u'] !== 'none') run.underline = true
      if (attrs['@_sz']) run.size = int(Number(attrs['@_sz']) / 100)
      const latin = rPr ? findChild(rPr, 'a:latin') : null
      if (latin && attrsOf(latin)['@_typeface']) run.font = String(attrsOf(latin)['@_typeface'])
      const fill = rPr ? findChild(rPr, 'a:solidFill') : null
      const col = colorFromFill(fill, themeColors)
      if (col) run.color = col.hex
      runs.push(run)
      text += str
    }
    text += '\n'
  }
  return { runs, text: text.trim() }
}

// ---------------------------------------------------------------------------
// xfrm -> geometry in EMU (before scale/offset)
// ---------------------------------------------------------------------------
const readXfrm = (spPr) => {
  if (!spPr) return null
  const xfrm = findChild(spPr, 'a:xfrm')
  if (!xfrm) return null
  const off = findChild(xfrm, 'a:off')
  const ext = findChild(xfrm, 'a:ext')
  if (!off || !ext) return null
  const oa = attrsOf(off)
  const ea = attrsOf(ext)
  const rotAttr = attrsOf(xfrm)['@_rot']
  return {
    x: Number(oa['@_x'] || 0),
    y: Number(oa['@_y'] || 0),
    cx: Number(ea['@_cx'] || 0),
    cy: Number(ea['@_cy'] || 0),
    rot: rotAttr ? Math.round(Number(rotAttr) / 60000) : 0,
  }
}

// ---------------------------------------------------------------------------
// Fill / line / gradient
// ---------------------------------------------------------------------------
const extractFill = (spPr, themeColors, out) => {
  if (!spPr) return
  const solid = findChild(spPr, 'a:solidFill')
  if (solid) {
    const c = colorFromFill(solid, themeColors)
    if (c) {
      out.fill = c.hex
      if (c.alpha < 1) out.opacity = Number(c.alpha.toFixed(3))
    }
    return
  }
  const grad = findChild(spPr, 'a:gradFill')
  if (grad) {
    const gsLst = findChild(grad, 'a:gsLst')
    const stops = []
    if (gsLst) {
      for (const gs of findAll(gsLst, 'a:gs')) {
        const pos = int(pct(attrsOf(gs)['@_pos']) * 100)
        const c = colorFromFill(gs, themeColors)
        stops.push({ pos, color: c ? c.hex : '#FFFFFF' })
      }
    }
    let angle = 0
    const lin = findChild(grad, 'a:lin')
    if (lin) angle = Math.round(Number(attrsOf(lin)['@_ang'] || 0) / 60000)
    out.gradient = { kind: findChild(grad, 'a:path') ? 'radial' : 'linear', angle, stops }
    return
  }
  const noFill = findChild(spPr, 'a:noFill')
  if (noFill) out.opacity = 0
}

const extractLine = (spPr, themeColors, out) => {
  if (!spPr) return
  const ln = findChild(spPr, 'a:ln')
  if (!ln) return
  const c = colorFromFill(findChild(ln, 'a:solidFill'), themeColors)
  if (c) out.line = c.hex
  const dash = findChild(ln, 'a:prstDash')
  if (dash) out.dash = DASH_MAP[attrsOf(dash)['@_val']] || 'solid'
  const w = attrsOf(ln)['@_w']
  const head = findChild(ln, 'a:headEnd')
  const tail = findChild(ln, 'a:tailEnd')
  if (w || head || tail) {
    out.props = out.props || {}
    if (w) out.props.lineWidth = Math.max(1, Math.round(Number(w) / EMU_PER_PX))
    if (head && attrsOf(head)['@_type'] && attrsOf(head)['@_type'] !== 'none') out.props.headEnd = String(attrsOf(head)['@_type'])
    if (tail && attrsOf(tail)['@_type'] && attrsOf(tail)['@_type'] !== 'none') out.props.tailEnd = String(attrsOf(tail)['@_type'])
  }
}

// ---------------------------------------------------------------------------
// Relationship parsing
// ---------------------------------------------------------------------------
const parseRels = (xml) => {
  const rels = {}
  if (!xml) return rels
  const doc = parseXml(xml)
  const relsEl = rootEl(doc, 'Relationships')
  if (!relsEl) return rels
  for (const r of findAll(relsEl, 'Relationship')) {
    const a = attrsOf(r)
    rels[a['@_Id']] = { target: a['@_Target'], type: a['@_Type'], mode: a['@_TargetMode'] }
  }
  return rels
}

const MIME = { png: 'image/png', jpg: 'image/jpeg', jpeg: 'image/jpeg', gif: 'image/gif', bmp: 'image/bmp', svg: 'image/svg+xml', webp: 'image/webp', emf: 'image/emf', wmf: 'image/wmf', tiff: 'image/tiff' }

// Normalize a relative rels target against the part dir (e.g. ppt/slides).
const resolveTarget = (baseDir, target) => {
  if (!target) return null
  let t = target.replace(/\\/g, '/')
  const parts = (baseDir + '/' + t).split('/')
  const stack = []
  for (const p of parts) {
    if (p === '..') stack.pop()
    else if (p !== '.' && p !== '') stack.push(p)
  }
  return stack.join('/')
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------
let input = ''
for await (const chunk of process.stdin) input += chunk

const ok = (doc) => {
  process.stdout.write(JSON.stringify({
    protocolVersion: 1,
    sceneOrTemplate: doc,
    warnings,
  }))
}
const fail = (msg) => {
  process.stdout.write(JSON.stringify({
    protocolVersion: 1,
    sceneOrTemplate: null,
    error: msg instanceof Error ? msg.message : String(msg),
    warnings,
  }))
}

try {
  const request = JSON.parse(input)
  if (request.protocolVersion !== 1) throw new Error(`unsupported protocol ${request.protocolVersion}`)
  const mode = request.mode === 'template' ? 'template' : 'deck'
  if (!request.pptxBase64) throw new Error('empty pptxBase64')

  const buf = Buffer.from(request.pptxBase64, 'base64')
  let zip
  try {
    zip = await JSZip.loadAsync(buf)
  } catch (e) {
    throw new Error(`not a valid pptx (zip) archive: ${e instanceof Error ? e.message : e}`)
  }

  // Zip-bomb / garbage guard.
  const files = Object.values(zip.files).filter((f) => !f.dir)
  if (files.length > MAX_ENTRIES) throw new Error(`pptx has too many entries (${files.length})`)
  let totalUncompressed = 0
  for (const f of files) {
    const meta = f._data && typeof f._data.uncompressedSize === 'number' ? f._data.uncompressedSize : 0
    totalUncompressed += meta
  }
  if (totalUncompressed > MAX_TOTAL_UNCOMPRESSED) throw new Error(`pptx uncompressed size exceeds limit (${totalUncompressed} bytes)`)

  const readText = async (path) => {
    const f = zip.file(path)
    return f ? f.async('string') : null
  }

  const presXml = await readText('ppt/presentation.xml')
  if (!presXml) throw new Error('missing ppt/presentation.xml (not a PowerPoint file)')
  const presDoc = parseXml(presXml)
  const presentation = rootEl(presDoc, 'p:presentation')
  if (!presentation) throw new Error('malformed presentation.xml')

  // Slide size.
  let pxW = CANVAS_W
  let pxH = CANVAS_H
  const sldSz = findChild(presentation, 'p:sldSz')
  if (sldSz) {
    pxW = Number(attrsOf(sldSz)['@_cx'] || 0) / EMU_PER_PX
    pxH = Number(attrsOf(sldSz)['@_cy'] || 0) / EMU_PER_PX
  }
  if (!pxW || !pxH) { pxW = CANVAS_W; pxH = CANVAS_H }
  const scale = Math.min(CANVAS_W / pxW, CANVAS_H / pxH)

  // Theme: first theme part.
  let themeColors = {}
  let themeTokens = {}
  const themeXml = await readText('ppt/theme/theme1.xml')
  if (themeXml) {
    const themeDoc = parseXml(themeXml)
    themeColors = buildThemeColors(themeDoc)
    const theme = rootEl(themeDoc, 'a:theme')
    const elements = theme ? findChild(theme, 'a:themeElements') : null
    const fontScheme = elements ? findChild(elements, 'a:fontScheme') : null
    let major = 'Aptos Display'
    let minor = 'Aptos'
    if (fontScheme) {
      const majorFont = findChild(fontScheme, 'a:majorFont')
      const minorFont = findChild(fontScheme, 'a:minorFont')
      const latinOf = (fEl) => {
        const l = fEl ? findChild(fEl, 'a:latin') : null
        return l ? attrsOf(l)['@_typeface'] : null
      }
      major = latinOf(majorFont) || major
      minor = latinOf(minorFont) || minor
    }
    const hex = (name, fallback) => (themeColors[name] ? '#' + themeColors[name] : fallback)
    themeTokens = {
      surface: hex('lt1', '#FFFFFF'),
      ink: hex('dk1', '#172033'),
      accent: hex('accent1', '#2563EB'),
      accent2: hex('accent2', '#7C3AED'),
      muted: hex('lt2', '#F1F5F9'),
      displayFont: major,
      bodyFont: minor,
    }
  } else {
    themeTokens = { surface: '#FFFFFF', ink: '#172033', accent: '#2563EB', displayFont: 'Aptos Display', bodyFont: 'Aptos' }
  }

  // Assets map (content-addressed).
  const assets = {}
  const addAsset = async (path) => {
    const f = zip.file(path)
    if (!f) return null
    const data = await f.async('nodebuffer')
    const hash = createHash('sha256').update(data).digest('hex')
    const ref = `sha256-${hash}`
    if (!assets[ref]) {
      const ext = (path.split('.').pop() || '').toLowerCase()
      const mime = MIME[ext] || 'application/octet-stream'
      assets[ref] = `data:${mime};base64,${data.toString('base64')}`
    }
    return ref
  }

  // Slide order from sldIdLst + presentation.xml.rels.
  const presRels = parseRels(await readText('ppt/_rels/presentation.xml.rels'))
  const sldIdLst = findChild(presentation, 'p:sldIdLst')
  const slidePaths = []
  if (sldIdLst) {
    for (const sldId of findAll(sldIdLst, 'p:sldId')) {
      const rid = attrsOf(sldId)['@_r:id']
      const rel = presRels[rid]
      if (rel && rel.target) {
        const p = resolveTarget('ppt', rel.target)
        if (p) slidePaths.push(p)
      }
    }
  }
  if (slidePaths.length === 0) {
    // Fallback: enumerate ppt/slides/slideN.xml.
    for (const name of Object.keys(zip.files)) {
      if (/^ppt\/slides\/slide\d+\.xml$/.test(name)) slidePaths.push(name)
    }
    slidePaths.sort((a, b) => {
      const na = Number((a.match(/slide(\d+)\.xml/) || [])[1] || 0)
      const nb = Number((b.match(/slide(\d+)\.xml/) || [])[1] || 0)
      return na - nb
    })
  }

  // ---- Shape processing -------------------------------------------------
  let nodeCounter = 0
  const nextId = (prefix) => `${prefix}-${++nodeCounter}`

  // Convert EMU box -> logical px box with scale + group affine already applied
  // by the caller (which passes absolute EMU). Clamp to canvas.
  const toCanvasBox = (emu) => {
    const x = int(emu.x / EMU_PER_PX * scale)
    const y = int(emu.y / EMU_PER_PX * scale)
    const w = int(emu.cx / EMU_PER_PX * scale)
    const h = int(emu.cy / EMU_PER_PX * scale)
    const cx = Math.max(0, Math.min(CANVAS_W, x))
    const cy = Math.max(0, Math.min(CANVAS_H, y))
    return {
      x: cx,
      y: cy,
      w: Math.max(0, Math.min(CANVAS_W - cx, w)),
      h: Math.max(0, Math.min(CANVAS_H - cy, h)),
    }
  }

  // Apply a group affine transform to a child's absolute EMU box.
  const applyAffine = (emu, affine) => {
    if (!affine) return emu
    return {
      x: affine.xg + (emu.x - affine.chOffX) * affine.sx,
      y: affine.yg + (emu.y - affine.chOffY) * affine.sy,
      cx: emu.cx * affine.sx,
      cy: emu.cy * affine.sy,
      rot: emu.rot,
    }
  }

  const processSp = (sp, affine) => {
    const spPr = findChild(sp, 'p:spPr')
    let box = readXfrm(spPr)
    if (!box) return null
    box = applyAffine(box, affine)
    const node = { id: nextId('sp'), type: 'shape', geometry: toCanvasBox(box) }
    if (box.rot) node.rot = box.rot
    const prstGeom = spPr ? findChild(spPr, 'a:prstGeom') : null
    const custGeom = spPr ? findChild(spPr, 'a:custGeom') : null
    if (custGeom) {
      warn(`Custom geometry approximated as rect (${node.id})`)
      node.geom = 'rect'
    } else if (prstGeom) {
      node.geom = mapGeom(attrsOf(prstGeom)['@_prst'], node.id)
    } else {
      node.geom = 'rect'
    }
    extractFill(spPr, themeColors, node)
    extractLine(spPr, themeColors, node)
    const txBody = findChild(sp, 'p:txBody')
    const { runs, text } = extractRuns(txBody, themeColors)
    if (runs.length) {
      node.runs = runs
      node.text = text
    }
    return node
  }

  const processPic = async (pic, affine, slideRels, slideDir) => {
    const spPr = findChild(pic, 'p:spPr')
    let box = readXfrm(spPr)
    if (!box) return null
    box = applyAffine(box, affine)
    const node = { id: nextId('img'), type: 'image', geometry: toCanvasBox(box) }
    if (box.rot) node.rot = box.rot
    const blipFill = findChild(pic, 'p:blipFill')
    const blip = blipFill ? findChild(blipFill, 'a:blip') : null
    const embed = blip ? attrsOf(blip)['@_r:embed'] : null
    if (embed && slideRels[embed]) {
      const target = resolveTarget(slideDir, slideRels[embed].target)
      const ref = target ? await addAsset(target) : null
      if (ref) {
        node.props = { assetRef: ref }
      } else {
        warn(`Image relationship ${embed} could not be resolved`)
      }
    } else {
      warn(`Image without embedded blip skipped (${node.id})`)
    }
    return node
  }

  const processTable = (gf, affine) => {
    const xfrm = findDeep(gf, 'a:xfrm')
    let box = xfrm ? {
      x: Number(attrsOf(findChild(xfrm, 'a:off'))['@_x'] || 0),
      y: Number(attrsOf(findChild(xfrm, 'a:off'))['@_y'] || 0),
      cx: Number(attrsOf(findChild(xfrm, 'a:ext'))['@_cx'] || 0),
      cy: Number(attrsOf(findChild(xfrm, 'a:ext'))['@_cy'] || 0),
      rot: 0,
    } : null
    const tbl = findDeep(gf, 'a:tbl')
    if (!tbl || !box) {
      warn('graphicFrame without table geometry skipped')
      return null
    }
    box = applyAffine(box, affine)
    const rows = []
    for (const tr of findAll(tbl, 'a:tr')) {
      const cells = []
      for (const tc of findAll(tr, 'a:tc')) {
        const txBody = findChild(tc, 'a:txBody')
        const { text } = extractRuns(txBody, themeColors)
        cells.push(text)
      }
      rows.push(cells)
    }
    return { id: nextId('tbl'), type: 'table', geometry: toCanvasBox(box), table: { rows } }
  }

  // Recursively process a spTree/grpSp child list, flattening groups to
  // absolute canvas geometry.
  const processTree = async (container, affine, slideRels, slideDir, out) => {
    for (const child of childrenOf(container)) {
      const t = tagOf(child)
      switch (t) {
        case 'p:sp': {
          const n = processSp(child, affine)
          if (n) out.push(n)
          break
        }
        case 'p:cxnSp': {
          const n = processSp(child, affine)
          if (n) { n.geom = 'line'; out.push(n) }
          break
        }
        case 'p:pic': {
          const n = await processPic(child, affine, slideRels, slideDir)
          if (n) out.push(n)
          break
        }
        case 'p:graphicFrame': {
          const isChart = findDeep(child, 'c:chart') || findDeep(child, 'a:graphicData')
          const tbl = findDeep(child, 'a:tbl')
          if (tbl) {
            const n = processTable(child, affine)
            if (n) out.push(n)
          } else {
            warn('Chart/diagram graphicFrame is unsupported and was skipped')
          }
          break
        }
        case 'p:grpSp': {
          // Compute the group affine and recurse.
          const grpSpPr = findChild(child, 'p:grpSpPr')
          const xfrm = grpSpPr ? findChild(grpSpPr, 'a:xfrm') : null
          if (!xfrm) { await processTree(child, affine, slideRels, slideDir, out); break }
          const off = findChild(xfrm, 'a:off')
          const ext = findChild(xfrm, 'a:ext')
          const chOff = findChild(xfrm, 'a:chOff')
          const chExt = findChild(xfrm, 'a:chExt')
          const offX = Number(attrsOf(off)['@_x'] || 0)
          const offY = Number(attrsOf(off)['@_y'] || 0)
          const extCx = Number(attrsOf(ext)['@_cx'] || 1)
          const extCy = Number(attrsOf(ext)['@_cy'] || 1)
          const chOffX = Number(attrsOf(chOff)['@_x'] || 0)
          const chOffY = Number(attrsOf(chOff)['@_y'] || 0)
          const chExtCx = Number(attrsOf(chExt)['@_cx'] || extCx) || 1
          const chExtCy = Number(attrsOf(chExt)['@_cy'] || extCy) || 1
          // First map group's own box through the parent affine to absolute EMU.
          const parentBox = applyAffine({ x: offX, y: offY, cx: extCx, cy: extCy, rot: 0 }, affine)
          const childAffine = {
            sx: (parentBox.cx / chExtCx) || 0,
            sy: (parentBox.cy / chExtCy) || 0,
            chOffX,
            chOffY,
            xg: parentBox.x,
            yg: parentBox.y,
          }
          await processTree(child, childAffine, slideRels, slideDir, out)
          break
        }
        default:
          break
      }
    }
  }

  const buildSlide = async (slidePath) => {
    const xml = await readText(slidePath)
    if (!xml) return null
    const doc = parseXml(xml)
    const sld = rootEl(doc, 'p:sld')
    if (!sld) return null
    const slideDir = slidePath.split('/').slice(0, -1).join('/')
    const relsPath = `${slideDir}/_rels/${slidePath.split('/').pop()}.rels`
    const slideRels = parseRels(await readText(relsPath))
    const cSld = findChild(sld, 'p:cSld')
    const spTree = cSld ? findChild(cSld, 'p:spTree') : null
    const nodes = []
    if (spTree) await processTree(spTree, null, slideRels, slideDir, nodes)
    // Slide title = first shape with a title placeholder text, else first text.
    let title = ''
    for (const n of nodes) {
      if (n.type === 'shape' && n.text) { title = n.text.split('\n')[0]; break }
    }
    return { id: slidePath.replace(/[^a-zA-Z0-9]/g, '-'), title, nodes }
  }

  const slides = []
  for (const p of slidePaths) {
    const s = await buildSlide(p)
    if (s) slides.push(s)
  }
  if (slides.length === 0) warn('No slides were extracted from the presentation')

  if (mode === 'deck') {
    const scene = {
      schemaVersion: 2,
      title: themeTokens.title || 'Imported deck',
      theme: themeTokens,
      slides,
      assets,
    }
    ok(scene)
  } else {
    // template mode: synthesize archetypes from slide layouts' placeholders.
    const archetypes = []
    const layoutNames = Object.keys(zip.files).filter((n) => /^ppt\/slideLayouts\/slideLayout\d+\.xml$/.test(n)).sort()
    const seen = new Set()
    for (const ln of layoutNames) {
      const xml = await readText(ln)
      if (!xml) continue
      const doc = parseXml(xml)
      const layout = rootEl(doc, 'p:sldLayout')
      if (!layout) continue
      const phType = (() => {
        const ph = findDeep(layout, 'p:ph')
        return ph ? (attrsOf(ph)['@_type'] || 'body') : 'body'
      })()
      let kind = 'content'
      if (phType === 'title' || phType === 'ctrTitle') kind = 'title'
      else if (phType === 'secHead') kind = 'section'
      if (seen.has(kind)) continue
      seen.add(kind)
      archetypes.push(kind)
    }
    // Ensure the canonical three archetypes exist.
    for (const k of ['title', 'section', 'content']) if (!archetypes.includes(k)) archetypes.push(k)

    const surface = themeTokens.surface || '#FFFFFF'
    const ink = themeTokens.ink || '#172033'
    const accent = themeTokens.accent || '#2563EB'
    const markupFor = (kind) => {
      if (kind === 'title') {
        return `ast-slide bg="${surface}"
  ast-text x=160 y=420 w=1600 h=200 size=72 color="${ink}" bold
    ast-run {{TITLE}}
  ast-text x=160 y=640 w=1600 h=100 size=32 color="${accent}"
    ast-run {{BODY}}`
      }
      if (kind === 'section') {
        return `ast-slide bg="${accent}"
  ast-shape x=0 y=480 w=1920 h=120 geom=rect fill="${ink}"
  ast-text x=160 y=500 w=1600 h=80 size=54 color="${surface}" bold
    ast-run {{TITLE}}`
      }
      return `ast-slide bg="${surface}"
  ast-text x=160 y=120 w=1600 h=120 size=48 color="${ink}" bold
    ast-run {{TITLE}}
  ast-text x=160 y=280 w=1600 h=680 size=28 color="${ink}"
    ast-run {{BODY}}`
    }
    const template = {
      schema: 2,
      name: 'imported-template',
      label: 'Imported Template',
      tokens: themeTokens,
      assets,
      archetypes: archetypes.map((kind) => ({
        kind,
        title: kind.charAt(0).toUpperCase() + kind.slice(1),
        markup: markupFor(kind),
      })),
    }
    ok(template)
  }
} catch (error) {
  fail(error)
}
