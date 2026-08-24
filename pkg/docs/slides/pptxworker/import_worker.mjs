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
// Custom geometry (a:custGeom) -> SVG path segments, parsed in DOCUMENT ORDER.
// Ported from the pptx-html pilot (src/parse/path.ts): moveTo/lnTo/arcTo/
// cubicBezTo/quadBezTo/close are emitted in the order they appear so mixed
// command sequences are preserved. Coordinates are expressed in the shape's own
// path-space (a:pathLst path @w/@h), which the renderer maps into the object box
// via a 0..w/0..h viewBox, so we keep raw path units and report w/h per subpath.
// ---------------------------------------------------------------------------
const ptOf = (el) => {
  const a = attrsOf(el)
  return { x: Number(a['@_x'] || 0), y: Number(a['@_y'] || 0) }
}
// Convert an OOXML arc (a:arcTo swAng/stAng in 60000ths of a degree, wR/hR radii)
// starting from the current point into an SVG 'A' command. OOXML arcs are given
// as start angle + sweep angle on an ellipse; we compute the end point and emit a
// single elliptical arc. This is an approximation good enough for brand curves.
const arcToSvg = (cur, arc) => {
  const a = attrsOf(arc)
  const wR = Number(a['@_wR'] || 0)
  const hR = Number(a['@_hR'] || 0)
  const stAng = (Number(a['@_stAng'] || 0) / 60000) * (Math.PI / 180)
  const swAng = (Number(a['@_swAng'] || 0) / 60000) * (Math.PI / 180)
  // Ellipse center from the current point at the start angle.
  const cx = cur.x - wR * Math.cos(stAng)
  const cy = cur.y - hR * Math.sin(stAng)
  const endAng = stAng + swAng
  const ex = cx + wR * Math.cos(endAng)
  const ey = cy + hR * Math.sin(endAng)
  const largeArc = Math.abs(swAng) > Math.PI ? 1 : 0
  const sweep = swAng > 0 ? 1 : 0
  cur.x = ex
  cur.y = ey
  return `A${int(wR)} ${int(hR)} 0 ${largeArc} ${sweep} ${int(ex)} ${int(ey)}`
}
// Parse an a:custGeom element into an array of { d, w, h, fillNone } subpaths.
const custGeomToPaths = (custGeom) => {
  const pathLst = findChild(custGeom, 'a:pathLst')
  if (!pathLst) return []
  const out = []
  for (const p of findAll(pathLst, 'a:path')) {
    const pa = attrsOf(p)
    const w = int(Number(pa['@_w'] || 0))
    const h = int(Number(pa['@_h'] || 0))
    const fillNone = pa['@_fill'] === 'none'
    const cur = { x: 0, y: 0 }
    let d = ''
    for (const cmd of childrenOf(p)) {
      const t = tagOf(cmd)
      switch (t) {
        case 'a:moveTo': {
          const pt = ptOf(findChild(cmd, 'a:pt'))
          cur.x = pt.x; cur.y = pt.y
          d += `M${int(pt.x)} ${int(pt.y)} `
          break
        }
        case 'a:lnTo': {
          const pt = ptOf(findChild(cmd, 'a:pt'))
          cur.x = pt.x; cur.y = pt.y
          d += `L${int(pt.x)} ${int(pt.y)} `
          break
        }
        case 'a:cubicBezTo': {
          const pts = findAll(cmd, 'a:pt').map(ptOf)
          if (pts.length === 3) {
            d += `C${int(pts[0].x)} ${int(pts[0].y)} ${int(pts[1].x)} ${int(pts[1].y)} ${int(pts[2].x)} ${int(pts[2].y)} `
            cur.x = pts[2].x; cur.y = pts[2].y
          }
          break
        }
        case 'a:quadBezTo': {
          const pts = findAll(cmd, 'a:pt').map(ptOf)
          if (pts.length === 2) {
            d += `Q${int(pts[0].x)} ${int(pts[0].y)} ${int(pts[1].x)} ${int(pts[1].y)} `
            cur.x = pts[1].x; cur.y = pts[1].y
          }
          break
        }
        case 'a:arcTo': {
          d += arcToSvg(cur, cmd) + ' '
          break
        }
        case 'a:close': {
          d += 'Z '
          break
        }
        default:
          break
      }
    }
    d = d.trim()
    if (d) out.push({ d, w: w || CANVAS_W, h: h || CANVAS_H, fillNone })
  }
  return out
}

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
  const xa = attrsOf(xfrm)
  return {
    x: Number(oa['@_x'] || 0),
    y: Number(oa['@_y'] || 0),
    cx: Number(ea['@_cx'] || 0),
    cy: Number(ea['@_cy'] || 0),
    rot: rotAttr ? Math.round(Number(rotAttr) / 60000) : 0,
    flipH: xa['@_flipH'] === '1' || xa['@_flipH'] === 'true',
    flipV: xa['@_flipV'] === '1' || xa['@_flipV'] === 'true',
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
      flipH: emu.flipH,
      flipV: emu.flipV,
    }
  }

  const processSp = (sp, affine) => {
    const spPr = findChild(sp, 'p:spPr')
    let box = readXfrm(spPr)
    if (!box) return null
    box = applyAffine(box, affine)
    const node = { id: nextId('sp'), type: 'shape', geometry: toCanvasBox(box) }
    if (box.rot) node.rot = box.rot
    if (box.flipH) node.flipH = true
    if (box.flipV) node.flipV = true
    // Record a placeholder marker (nvSpPr//p:ph) so template mode can classify
    // this shape as a fillable placeholder vs decorative chrome. Deck mode
    // ignores node.ph (it reads only type/geometry/geom/fill/line/runs), so
    // attaching it is inert there.
    const ph = findDeep(sp, 'p:ph')
    if (ph) {
      const pa = attrsOf(ph)
      node.ph = { type: pa['@_type'] || 'body', idx: pa['@_idx'] != null ? String(pa['@_idx']) : '' }
    }
    const prstGeom = spPr ? findChild(spPr, 'a:prstGeom') : null
    const custGeom = spPr ? findChild(spPr, 'a:custGeom') : null
    if (custGeom) {
      // Extract real SVG path segments (in document order) so the renderer can
      // draw the exact brand geometry rather than approximating as a rect.
      const paths = custGeomToPaths(custGeom)
      if (paths.length) {
        node.geom = 'path'
        node.paths = paths
      } else {
        warn(`Custom geometry produced no path segments; approximated as rect (${node.id})`)
        node.geom = 'rect'
      }
    } else if (prstGeom) {
      const prst = attrsOf(prstGeom)['@_prst']
      node.geom = mapGeom(prst, node.id)
      // Preserve the rounded-rect corner radius for the lossless IR (a:avLst
      // adj is a fraction of the shorter side * 100000). ASD only supports a
      // fixed roundRect radius today, but the IR keeps the true value.
      if (prst === 'roundRect') {
        const gd = findDeep(prstGeom, 'a:gd')
        const fmla = gd ? attrsOf(gd)['@_fmla'] : ''
        const m = fmla ? String(fmla).match(/val\s+(\d+)/) : null
        const adj = m ? Number(m[1]) : 0
        const shorter = Math.min(node.geometry.w, node.geometry.h)
        node.rectRadius = int((adj / 100000) * shorter) || int(0.12 * shorter)
      }
    } else {
      node.geom = 'rect'
    }
    extractFill(spPr, themeColors, node)
    extractLine(spPr, themeColors, node)
    // SAP "anvil" chrome shapes often carry no local fill; they inherit from the
    // theme via p:style/a:fillRef (idx into the theme fill style list, plus a
    // color). Resolve that so accent bars/logos are not rendered transparent.
    if (node.fill == null && node.gradient == null && node.opacity == null) {
      const style = findChild(sp, 'p:style')
      const fillRef = style ? findChild(style, 'a:fillRef') : null
      if (fillRef) {
        const c = colorFromFill(fillRef, themeColors)
        if (c) node.fill = c.hex
      }
    }
    if (node.line == null) {
      const style = findChild(sp, 'p:style')
      const lnRef = style ? findChild(style, 'a:lnRef') : null
      if (lnRef) {
        const c = colorFromFill(lnRef, themeColors)
        if (c) node.line = c.hex
      }
    }
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
    // Placeholder marker (pic placeholders are fillable image slots in layouts).
    const phEl = findDeep(pic, 'p:ph')
    if (phEl) {
      const pa = attrsOf(phEl)
      node.ph = { type: pa['@_type'] || 'pic', idx: pa['@_idx'] != null ? String(pa['@_idx']) : '' }
    }
    const blipFill = findChild(pic, 'p:blipFill')
    const blip = blipFill ? findChild(blipFill, 'a:blip') : null
    // Prefer a vector SVG alternative (asvg:svgBlip in extLst) when present, as
    // it scales without raster loss; fall back to the raster r:embed otherwise.
    let embed = null
    if (blip) {
      const extLst = findChild(blip, 'a:extLst')
      if (extLst) {
        const svgBlip = findDeep(extLst, 'asvg:svgBlip') || findDeep(extLst, 'svgBlip')
        if (svgBlip) {
          const se = attrsOf(svgBlip)['@_r:embed']
          if (se && slideRels[se]) embed = se
        }
      }
      if (!embed) embed = attrsOf(blip)['@_r:embed'] || null
    }
    if (embed && slideRels[embed]) {
      const target = resolveTarget(slideDir, slideRels[embed].target)
      const ext = target ? (target.split('.').pop() || '').toLowerCase() : ''
      if (ext === 'emf' || ext === 'wmf') {
        // EMF/WMF are vector metafiles the browser cannot render. Full replay is
        // out of scope for v1: use a raster sibling if one exists, else warn+skip.
        const raster = target.replace(/\.(emf|wmf)$/i, '.png')
        const ref = zip.file(raster) ? await addAsset(raster) : null
        if (ref) {
          node.props = { assetRef: ref }
        } else {
          warn(`EMF/WMF vector image not rendered (v1 limitation): ${target}`)
          return null
        }
      } else {
        const ref = target ? await addAsset(target) : null
        if (ref) {
          node.props = { assetRef: ref }
        } else {
          warn(`Image relationship ${embed} could not be resolved`)
        }
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
    // -----------------------------------------------------------------------
    // Template mode (Option A): ONE Astonish template per .pptx that exposes the
    // deck's LAYOUTS as classified, human-labeled, selectable variants. A single
    // slide master + single theme = one design system, so we do not split into
    // multiple templates. Each of the N slide layouts becomes an archetype whose
    // KIND is its role (title/section/content/agenda/closing/blank) and whose
    // LABEL is the real PowerPoint layout name (e.g. "Blue cover, anvil and
    // image"), so the user/AI can choose among same-role variants. Each layout's
    // color/chrome is captured via master→layout background+chrome inheritance
    // (the colorful covers/dividers carry their pictures + accent shapes in the
    // LAYOUT; plain layouts inherit the neutral background from the master).
    // A lossless per-layout / per-sample-slide IR (TemplateModel, schema 3) is
    // also persisted for the future in-browser editor. Everything renders through
    // the existing IR→ASD serializer + ASD runtime (no second renderer).
    // -----------------------------------------------------------------------

    // Map an OOXML placeholder type to our coarse placeholder role.
    const phRole = (t) => {
      switch (t) {
        case 'ctrTitle':
        case 'title':
          return 'title'
        case 'subTitle':
        case 'body':
        case 'obj':
          return 'body'
        case 'pic':
          return 'image'
        case 'tbl':
          return 'table'
        case 'chart':
          return 'chart'
        default:
          return 'body'
      }
    }

    // Extract the background of a cSld: solid color or full-bleed image.
    const bgOf = async (cSld, rels, dir) => {
      const bg = cSld ? findChild(cSld, 'p:bg') : null
      if (!bg) return { kind: 'solid', color: themeTokens.surface || '#FFFFFF' }
      const bgPr = findChild(bg, 'p:bgPr')
      const bgRef = findChild(bg, 'p:bgRef')
      if (bgPr) {
        const blipFill = findChild(bgPr, 'a:blipFill')
        const blip = blipFill ? findChild(blipFill, 'a:blip') : null
        const embed = blip ? attrsOf(blip)['@_r:embed'] : null
        if (embed && rels[embed]) {
          const target = resolveTarget(dir, rels[embed].target)
          const ref = target ? await addAsset(target) : null
          if (ref) return { kind: 'image', mediaKey: ref }
        }
        const c = colorFromFill(findChild(bgPr, 'a:solidFill'), themeColors)
        if (c) return { kind: 'solid', color: c.hex }
      }
      if (bgRef) {
        const c = colorFromFill(bgRef, themeColors)
        if (c) return { kind: 'solid', color: c.hex }
      }
      return { kind: 'solid', color: themeTokens.surface || '#FFFFFF' }
    }

    // isFallbackBg reports whether a background is the neutral surface fallback
    // bgOf returns when a cSld carries no explicit <p:bg> (so we know to look up
    // the inheritance chain instead of rendering white).
    const isFallbackBg = (bg) =>
      !bg || (bg.kind === 'solid' && (bg.color === (themeTokens.surface || '#FFFFFF')))

    // resolveBackground implements PowerPoint's master→layout background
    // inheritance: prefer the layout's own <p:bg>; if the layout has none
    // (bgOf returned the surface fallback), inherit the master's background
    // instead of rendering white. masterCtx may be null (deck mode / no master).
    const resolveBackground = async (ownCSld, ownRels, ownDir, masterCtx) => {
      const own = await bgOf(ownCSld, ownRels, ownDir)
      if (!isFallbackBg(own)) return own
      if (masterCtx && masterCtx.bg && !isFallbackBg(masterCtx.bg)) return masterCtx.bg
      return own
    }

    // Convert one processed node into either an IRChrome or an IRPlaceholder.
    const styleOf = (node) => {
      const st = {}
      const r0 = node.runs && node.runs[0]
      if (r0) {
        if (r0.size) st.fontSize = r0.size
        if (r0.color) st.color = r0.color
        if (r0.bold) st.bold = true
        if (r0.italic) st.italic = true
        if (r0.font) st.fontFace = r0.font
      }
      return st
    }

    let phCounter = 0
    const classify = (node, layout) => {
      const g = node.geometry
      if (node.ph) {
        const type = phRole(node.ph.type)
        phCounter += 1
        const name = `${type}-${phCounter}`
        layout.placeholders.push({
          name,
          type,
          x: g.x, y: g.y, w: g.w, h: g.h,
          style: styleOf(node),
          prompt: type === 'title' ? '{{TITLE}}' : '{{BODY}}',
          ooxmlType: node.ph.type,
          idx: node.ph.idx ? Number(node.ph.idx) : 0,
        })
        return
      }
      // Chrome object (decorative).
      const chrome = {
        kind: node.type === 'image' ? 'image'
          : node.type === 'table' ? 'text'
            : node.geom === 'path' ? 'path'
              : node.geom === 'line' ? 'line'
                : node.geom === 'ellipse' ? 'ellipse'
                  : node.text ? 'text' : 'rect',
        x: g.x, y: g.y, w: g.w, h: g.h,
      }
      if (node.rot) chrome.rot = node.rot
      if (node.flipH) chrome.flipH = true
      if (node.flipV) chrome.flipV = true
      if (node.fill) chrome.fill = { kind: 'solid', color: node.fill }
      else if (node.gradient) chrome.fill = { kind: 'gradient', gradient: node.gradient }
      if (node.line) chrome.line = { color: node.line, width: (node.props && node.props.lineWidth) || 1, dash: node.dash || 'solid' }
      if (node.rectRadius) chrome.rectRadius = node.rectRadius
      if (node.paths) chrome.paths = node.paths
      if (node.text) { chrome.text = node.text; chrome.style = styleOf(node) }
      if (node.type === 'image' && node.props && node.props.assetRef) chrome.mediaKey = node.props.assetRef
      chrome.geom = node.geom
      layout.objects.push(chrome)
    }

    // Build an IRLayout from a spTree root (layout or sample slide). When
    // masterCtx is supplied (template mode), the background is resolved through
    // the master→layout inheritance chain and the master's decorative chrome is
    // prepended (behind the layout's own chrome) unless the layout suppresses
    // master shapes (showMasterSp="0").
    const buildIRLayout = async (spTree, cSld, rels, dir, id, name, masterCtx = null, showMasterSp = true) => {
      const background = masterCtx
        ? await resolveBackground(cSld, rels, dir, masterCtx)
        : await bgOf(cSld, rels, dir)
      const layout = { id, name, background, objects: [], placeholders: [] }
      // Inherited master chrome first (behind), decorative objects only.
      if (masterCtx && showMasterSp && masterCtx.chromeObjects && masterCtx.chromeObjects.length) {
        for (const o of masterCtx.chromeObjects) layout.objects.push(o)
      }
      const nodes = []
      if (spTree) await processTree(spTree, null, rels, dir, nodes)
      for (const n of nodes) classify(n, layout)
      return layout
    }

    // ---- Determine archetype KIND from a layout's NAME, then placeholders ---
    // The .pptx layouts carry no <p:sldLayout type> attribute (confirmed empty
    // on all 39 in the reference corporate deck), so the human layout NAME is
    // the primary signal. Order matters: check the most specific roles first.
    const kindOf = (layout, layoutType) => {
      const name = (layout.name || '').toLowerCase()
      const hasTitle = layout.placeholders.some((p) => p.type === 'title')
      const bodyCount = layout.placeholders.filter((p) => p.type === 'body').length
      // Closing family (thank you / contact / copyright / Q&A / closing).
      if (/thank|closing|contact|copyright|q\s*&\s*a|q&a|farewell/.test(name)) return 'closing'
      // Agenda / table-of-contents family.
      if (/agenda|toc|overview|contents|table of contents/.test(name)) return 'agenda'
      // Section / divider / separator family.
      if (layoutType === 'secHead' || /divider|separator|section/.test(name)) return 'section'
      // Cover / title family.
      if (layoutType === 'title' || layoutType === 'titleOnly' || /cover|title slide/.test(name)) return 'title'
      // Blank.
      if (/blank/.test(name)) return 'blank'
      // Fall back to the placeholder signature when the name is uninformative.
      if (hasTitle && bodyCount === 0) return 'title'
      if (bodyCount > 0) return 'content'
      return 'content'
    }

    // uniqueKind() suffixes duplicates so variant multiplicity is preserved:
    // title, title-2, title-3, ...
    const kindCounts = {}
    const uniqueKind = (base) => {
      kindCounts[base] = (kindCounts[base] || 0) + 1
      return kindCounts[base] === 1 ? base : `${base}-${kindCounts[base]}`
    }

    // ---- IR -> ASD serialization ------------------------------------------
    // XML attribute-value escaping (ParseSlide consumes XML, not the indented
    // DSL). We also strip characters the validators reject up front.
    const esc = (s) => String(s == null ? '' : s)
      .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;')
    // Escape element text content.
    const escText = (s) => String(s == null ? '' : s)
      .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
    // Clamp geometry into the canvas so ValidateSlide's invalid_geometry check
    // always passes (extraction already clamps, but chrome may exceed after
    // synthesis).
    const clampGeo = (o) => {
      const x = Math.max(0, Math.min(CANVAS_W, o.x || 0))
      const y = Math.max(0, Math.min(CANVAS_H, o.y || 0))
      const w = Math.max(1, Math.min(CANVAS_W - x, o.w || 1))
      const h = Math.max(1, Math.min(CANVAS_H - y, o.h || 1))
      return `x="${x}" y="${y}" w="${w}" h="${h}"`
    }
    // Only #RRGGBB(AA) / rgb()/rgba() colors pass safeColor; drop anything else.
    const safeCol = (c) => (c && /^#[0-9a-fA-F]{6}([0-9a-fA-F]{2})?$/.test(c)) ? c : null
    // OOXML custom-path unit -> node-box px. The renderer's SVG viewBox is
    // "0 0 W H" (node box), so path coordinates must be in that space.
    const scalePath = (d, pathW, pathH, boxW, boxH) => {
      const sx = pathW ? boxW / pathW : 1
      const sy = pathH ? boxH / pathH : 1
      // Scale every run of digits that represents a coordinate. Commands are
      // letters; numbers between them are coordinate pairs. We scale alternating
      // x/y by walking numeric tokens per command.
      return d.replace(/([MLCQAHVZmlcqahvz])([^MLCQAHVZmlcqahvz]*)/g, (_, cmd, args) => {
        const nums = (args.match(/-?\d*\.?\d+/g) || []).map(Number)
        let out = cmd
        const up = cmd.toUpperCase()
        if (up === 'Z') return 'Z'
        if (up === 'H') return cmd + nums.map((n) => Math.round(n * sx)).join(' ')
        if (up === 'V') return cmd + nums.map((n) => Math.round(n * sy)).join(' ')
        if (up === 'A') {
          // rx ry rot largeArc sweep x y (7 params); scale rx,ry,x,y only.
          const parts = []
          for (let i = 0; i < nums.length; i += 7) {
            const g = nums.slice(i, i + 7)
            if (g.length < 7) break
            parts.push(`${Math.round(g[0] * sx)} ${Math.round(g[1] * sy)} ${g[2]} ${g[3]} ${g[4]} ${Math.round(g[5] * sx)} ${Math.round(g[6] * sy)}`)
          }
          return cmd + parts.join(' ')
        }
        for (let i = 0; i < nums.length; i += 2) {
          out += `${Math.round((nums[i] || 0) * sx)} ${Math.round((nums[i + 1] || 0) * sy)} `
        }
        return out.trim() + ' '
      }).trim()
    }

    // Serialize a single IRChrome to an XML ast-* element. Returns '' when ASD
    // cannot express it (caller records a warning).
    const chromeToAsd = (o, idc) => {
      const id = `c-${idc}`
      const geo = clampGeo(o)
      const rot = o.rot ? ` rot="${Math.max(-360, Math.min(360, o.rot))}"` : ''
      if (o.kind === 'image' && o.mediaKey) {
        return `<ast-image id="${id}" ${geo}${rot} asset-ref="${esc(o.mediaKey)}" fit="cover" decorative="true"></ast-image>`
      }
      if (o.kind === 'text' || o.text) {
        const st = o.style || {}
        const attrs = [`size="${st.fontSize || 24}"`]
        const col = safeCol(st.color) || (themeTokens.ink && safeCol(themeTokens.ink)) || '#172033'
        attrs.push(`color="${col}"`)
        if (st.bold) attrs.push('weight="bold"')
        if (st.fontFace && !/[;<>]/.test(st.fontFace)) attrs.push(`font="${esc(st.fontFace)}"`)
        // italic is a run-level attribute (i) — ast-text has no italic attr.
        const runAttr = st.italic ? ' i="true"' : ''
        const runs = String(o.text).split('\n').filter((l) => l !== '')
          .map((l) => `<ast-run${runAttr}>${escText(l)}</ast-run>`).join('')
        return `<ast-text id="${id}" ${geo}${rot} ${attrs.join(' ')}>${runs || '<ast-run></ast-run>'}</ast-text>`
      }
      // Shape family (rect/ellipse/line/path). ast-shape requires kind.
      let fillAttr = ''
      if (o.fill && o.fill.kind === 'solid' && safeCol(o.fill.color)) fillAttr = ` fill="${o.fill.color}"`
      else if (o.fill && o.fill.kind === 'gradient' && o.fill.gradient && o.fill.gradient.stops && o.fill.gradient.stops[0] && safeCol(o.fill.gradient.stops[0].color)) {
        // ASD gradients need a JSON <script> child; for the archetype we
        // approximate with the first stop color and keep the true gradient in IR.
        fillAttr = ` fill="${o.fill.gradient.stops[0].color}"`
        warn(`Gradient approximated as solid in archetype (${id})`)
      }
      const lineAttr = o.line && safeCol(o.line.color) ? ` line="${o.line.color}"` : ''
      if (o.kind === 'path' && o.paths && o.paths.length) {
        const p = o.paths[0]
        const d = scalePath(p.d, p.w, p.h, o.w, o.h)
        if (d && /^[MmLlCcQqZzHhVvAa0-9\s,.+-]*$/.test(d)) {
          return `<ast-shape id="${id}" kind="rect" ${geo}${rot} path="${esc(d)}"${fillAttr}${lineAttr}></ast-shape>`
        }
        // Unsafe/empty path -> approximate as rect.
        return `<ast-shape id="${id}" kind="rect" ${geo}${rot} geom="rect"${fillAttr}${lineAttr}></ast-shape>`
      }
      const geom = o.kind === 'line' ? 'line'
        : o.kind === 'ellipse' ? 'ellipse'
          : (o.geom === 'roundRect' || o.rectRadius) ? 'roundRect'
            : (o.geom && o.geom !== 'path' && ALLOWED_GEOM.has(o.geom)) ? o.geom : 'rect'
      const kind = geom === 'line' ? 'line' : geom === 'ellipse' ? 'ellipse' : 'rect'
      return `<ast-shape id="${id}" kind="${kind}" ${geo}${rot} geom="${geom}"${fillAttr}${lineAttr}></ast-shape>`
    }

    // Geometry presets ASD's validator allows (mirror of allowedGeomPresets).
    const ALLOWED_GEOM = new Set(['rect', 'roundRect', 'ellipse', 'triangle', 'rtTriangle', 'diamond', 'parallelogram', 'trapezoid', 'hexagon', 'octagon', 'star5', 'rightArrow', 'leftArrow', 'chevron', 'cloud', 'can', 'cube', 'line', 'bracketPair'])

    // Serialize a placeholder to a fillable ast-text (XML).
    const placeholderToAsd = (p, idc) => {
      const st = p.style || {}
      const col = safeCol(st.color) || (themeTokens.ink && safeCol(themeTokens.ink)) || '#172033'
      const attrs = [`size="${st.fontSize || (p.type === 'title' ? 54 : 28)}"`, `color="${col}"`]
      if (st.bold || p.type === 'title') attrs.push('weight="bold"')
      if (st.fontFace && !/[;<>]/.test(st.fontFace)) attrs.push(`font="${esc(st.fontFace)}"`)
      const runAttr = st.italic ? ' i="true"' : ''
      const geo = clampGeo(p)
      return `<ast-text id="ph-${idc}" ${geo} ${attrs.join(' ')}><ast-run${runAttr}>${escText(p.prompt || '{{BODY}}')}</ast-run></ast-text>`
    }

    // Serialize an entire IRLayout to a single-root <ast-slide> XML fragment.
    const layoutToAsd = (layout) => {
      const bg = layout.background || {}
      const parts = []
      // Solid background renders as a full-canvas decorative rect (matches the
      // built-in template convention); image background renders as a full-canvas
      // ast-image FIRST (behind chrome).
      if (bg.kind === 'image' && bg.mediaKey) {
        parts.push(`<ast-image id="bg" x="0" y="0" w="${CANVAS_W}" h="${CANVAS_H}" asset-ref="${esc(bg.mediaKey)}" fit="cover" decorative="true"></ast-image>`)
      } else {
        const col = safeCol(bg.color) || safeCol(themeTokens.surface) || '#FFFFFF'
        parts.push(`<ast-shape id="bg" kind="rect" x="0" y="0" w="${CANVAS_W}" h="${CANVAS_H}" geom="rect" fill="${col}" decorative="true"></ast-shape>`)
      }
      let idc = 0
      for (const o of layout.objects) {
        idc += 1
        const m = chromeToAsd(o, idc)
        if (m) parts.push(m)
        else warn(`Chrome object not representable in ASD and dropped (${layout.id} #${idc})`)
      }
      let pc = 0
      for (const p of layout.placeholders) {
        if (p.type === 'title' || p.type === 'body') {
          pc += 1
          parts.push(placeholderToAsd(p, pc))
        }
      }
      // Guarantee a title + body slot so create_deck can fill it.
      if (!layout.placeholders.some((p) => p.type === 'title')) {
        parts.push(`<ast-text id="ph-title" x="160" y="120" w="1600" h="160" size="54" color="${safeCol(themeTokens.ink) || '#172033'}" weight="bold"><ast-run>{{TITLE}}</ast-run></ast-text>`)
      }
      if (!layout.placeholders.some((p) => p.type === 'body')) {
        parts.push(`<ast-text id="ph-body" x="160" y="320" w="1600" h="600" size="28" color="${safeCol(themeTokens.ink) || '#172033'}"><ast-run>{{BODY}}</ast-run></ast-text>`)
      }
      const slideId = layout.id.replace(/[^a-zA-Z0-9-]/g, '-') || 'layout'
      return `<ast-slide id="${slideId}">${parts.join('')}</ast-slide>`
    }

    // ---- Extract every layout, then a few sample slides -------------------
    const layoutNames = Object.keys(zip.files)
      .filter((n) => /^ppt\/slideLayouts\/slideLayout\d+\.xml$/.test(n))
      .sort((a, b) => {
        const na = Number((a.match(/slideLayout(\d+)\.xml/) || [])[1] || 0)
        const nb = Number((b.match(/slideLayout(\d+)\.xml/) || [])[1] || 0)
        return na - nb
      })

    const irLayouts = []
    const archetypes = []
    const usedNames = new Set()
    const uniqueLayoutId = (name) => {
      let base = String(name || 'layout').toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '') || 'layout'
      let id = base
      let n = 2
      while (usedNames.has(id)) { id = `${base}-${n}`; n += 1 }
      usedNames.add(id)
      return id
    }

    // ---- Slide-master context (master→layout inheritance) -----------------
    // PowerPoint slides/layouts inherit their neutral background and decorative
    // chrome from the slide master. bgOf() alone only reads a part's OWN <p:bg>,
    // which is why plain content layouts (no own bg) rendered white. We load
    // each master once, capture its background and its DECORATIVE chrome objects
    // (placeholders and the slide-number are excluded — they are per-slide holes,
    // not brand chrome), and merge them behind each layout that references it.
    const masterCtxByPath = {}
    const loadMasterCtx = async (masterPath) => {
      if (!masterPath) return null
      if (masterCtxByPath[masterPath] !== undefined) return masterCtxByPath[masterPath]
      const xml = await readText(masterPath)
      if (!xml) { masterCtxByPath[masterPath] = null; return null }
      const doc = parseXml(xml)
      const masterEl = rootEl(doc, 'p:sldMaster')
      if (!masterEl) { masterCtxByPath[masterPath] = null; return null }
      const dir = masterPath.split('/').slice(0, -1).join('/')
      const relsPath = `${dir}/_rels/${masterPath.split('/').pop()}.rels`
      const rels = parseRels(await readText(relsPath))
      const cSld = findChild(masterEl, 'p:cSld')
      const spTree = cSld ? findChild(cSld, 'p:spTree') : null
      const bg = await bgOf(cSld, rels, dir)
      // Process the master spTree to IR, then keep only decorative objects.
      phCounter = 0
      const tmp = { id: 'master', name: 'master', background: bg, objects: [], placeholders: [] }
      const nodes = []
      if (spTree) await processTree(spTree, null, rels, dir, nodes)
      for (const n of nodes) classify(n, tmp)
      const ctx = { bg, chromeObjects: tmp.objects }
      masterCtxByPath[masterPath] = ctx
      return ctx
    }
    // Resolve a layout's master path from its .rels (slideMaster relationship).
    const masterPathOfLayout = (layoutRels, layoutDir) => {
      for (const id of Object.keys(layoutRels)) {
        const r = layoutRels[id]
        if (r && r.type && /slideMaster$/.test(r.type)) {
          return resolveTarget(layoutDir, r.target)
        }
      }
      return null
    }

    for (const ln of layoutNames) {
      const xml = await readText(ln)
      if (!xml) continue
      const doc = parseXml(xml)
      const layoutEl = rootEl(doc, 'p:sldLayout')
      if (!layoutEl) continue
      const dir = ln.split('/').slice(0, -1).join('/')
      const relsPath = `${dir}/_rels/${ln.split('/').pop()}.rels`
      const rels = parseRels(await readText(relsPath))
      const cSld = findChild(layoutEl, 'p:cSld')
      const spTree = cSld ? findChild(cSld, 'p:spTree') : null
      const layoutType = attrsOf(layoutEl)['@_type'] || ''
      const rawName = (cSld && attrsOf(cSld)['@_name']) || `Layout ${irLayouts.length + 1}`
      const id = uniqueLayoutId(rawName)
      // Master inheritance: resolve this layout's master and whether it
      // suppresses master shapes (showMasterSp="0").
      const masterCtx = await loadMasterCtx(masterPathOfLayout(rels, dir))
      const showMasterSp = attrsOf(layoutEl)['@_showMasterSp'] !== '0'
      if (masterCtx && !showMasterSp) {
        warn(`Layout "${rawName}" sets showMasterSp=0; master chrome intentionally suppressed`)
      }
      phCounter = 0
      const ir = await buildIRLayout(spTree, cSld, rels, dir, id, rawName, masterCtx, showMasterSp)
      irLayouts.push(ir)
      const kind = uniqueKind(kindOf(ir, layoutType))
      archetypes.push({ kind, title: rawName, markup: layoutToAsd(ir), _layout: ir })
    }

    // Capture a few sample slides into the IR (templateModel.slides) so a future
    // in-browser editor has real filled examples to work from. These are NOT
    // turned into archetypes: authored slides in a corporate template are thin
    // (a photo dropped into the layout's picture placeholder, no own background
    // or accent chrome — the color lives in the LAYOUTS), so slide-derived
    // archetypes rendered white. The colorful, inheritance-corrected LAYOUT
    // archetypes above are the on-brand starting points instead. Cap at 6.
    const irSlides = []
    for (const p of slidePaths.slice(0, 6)) {
      const xml = await readText(p)
      if (!xml) continue
      const doc = parseXml(xml)
      const sld = rootEl(doc, 'p:sld')
      if (!sld) continue
      const dir = p.split('/').slice(0, -1).join('/')
      const relsPath = `${dir}/_rels/${p.split('/').pop()}.rels`
      const rels = parseRels(await readText(relsPath))
      const cSld = findChild(sld, 'p:cSld')
      const spTree = cSld ? findChild(cSld, 'p:spTree') : null
      phCounter = 0
      const ir = await buildIRLayout(spTree, cSld, rels, dir, uniqueLayoutId(`slide-${irSlides.length + 1}`), `Slide ${irSlides.length + 1}`)
      irSlides.push(ir)
    }

    // Guarantee the canonical three archetype kinds exist (tests + baseline
    // authoring flow). Synthesize minimal fallbacks from theme tokens only when
    // a kind was not produced by any real layout.
    const surface = safeCol(themeTokens.surface) || '#FFFFFF'
    const ink = safeCol(themeTokens.ink) || '#172033'
    const accent = safeCol(themeTokens.accent) || '#2563EB'
    const fallbackMarkup = (kind) => {
      if (kind === 'title') {
        return `<ast-slide id="title"><ast-shape id="bg" kind="rect" x="0" y="0" w="1920" h="1080" geom="rect" fill="${surface}" decorative="true"></ast-shape><ast-text id="ph-title" x="160" y="420" w="1600" h="200" size="72" color="${ink}" weight="bold"><ast-run>{{TITLE}}</ast-run></ast-text><ast-text id="ph-body" x="160" y="640" w="1600" h="100" size="32" color="${accent}"><ast-run>{{BODY}}</ast-run></ast-text></ast-slide>`
      }
      if (kind === 'section') {
        return `<ast-slide id="section"><ast-shape id="bg" kind="rect" x="0" y="0" w="1920" h="1080" geom="rect" fill="${accent}" decorative="true"></ast-shape><ast-shape id="c-1" kind="rect" x="0" y="480" w="1920" h="120" geom="rect" fill="${ink}"></ast-shape><ast-text id="ph-title" x="160" y="500" w="1600" h="80" size="54" color="${surface}" weight="bold"><ast-run>{{TITLE}}</ast-run></ast-text><ast-text id="ph-body" x="160" y="620" w="1600" h="80" size="28" color="${surface}"><ast-run>{{BODY}}</ast-run></ast-text></ast-slide>`
      }
      return `<ast-slide id="content"><ast-shape id="bg" kind="rect" x="0" y="0" w="1920" h="1080" geom="rect" fill="${surface}" decorative="true"></ast-shape><ast-text id="ph-title" x="160" y="120" w="1600" h="120" size="48" color="${ink}" weight="bold"><ast-run>{{TITLE}}</ast-run></ast-text><ast-text id="ph-body" x="160" y="280" w="1600" h="680" size="28" color="${ink}"><ast-run>{{BODY}}</ast-run></ast-text></ast-slide>`
    }
    const presentKinds = new Set(archetypes.map((a) => a.kind.replace(/-\d+$/, '')))
    for (const want of ['title', 'section', 'content']) {
      if (!presentKinds.has(want)) {
        archetypes.push({ kind: uniqueKind(want), title: want.charAt(0).toUpperCase() + want.slice(1), markup: fallbackMarkup(want) })
      }
    }

    const templateModel = {
      schema: 3,
      size: { w: CANVAS_W, h: CANVAS_H },
      theme: themeTokens,
      layouts: irLayouts,
      slides: irSlides,
      // The IR warnings are structured objects ({code,message}) to match the Go
      // themes.IRWarning shape. The top-level ImportResponse.warnings stays a
      // flat string list (protocol contract), so we project the strings here.
      warnings: warnings.map((m) => ({ code: 'import', message: String(m) })),
    }

    const template = {
      schema: 2,
      name: 'imported-template',
      label: 'Imported Template',
      tokens: themeTokens,
      assets,
      archetypes: archetypes.map((a) => ({ kind: a.kind, title: a.title, markup: a.markup })),
      templateModel,
    }
    ok(template)
  }
} catch (error) {
  fail(error)
}
