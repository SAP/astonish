import { createRequire } from 'node:module'
import { createHash } from 'node:crypto'

const require = createRequire(`${process.cwd()}/package.json`)
const JSZip = require('jszip')
const { XMLParser } = require('fast-xml-parser')
// mtx-decompressor recovers a plain TrueType font from a PowerPoint embedded-font
// part (ppt/fonts/fontN.fntdata): those are EOT containers whose payload is
// MicroType Express (MTX) compressed (PowerPoint compresses by default), so a
// naive EOT-header strip is not enough. eotToTtf parses the EOT header, applies
// the container's compression/encryption flags, and returns a standard .ttf.
const mtxDecompressor = require('mtx-decompressor')

// ---------------------------------------------------------------------------
// Constants / limits
// ---------------------------------------------------------------------------
const EMU_PER_PX = 9525 // 1px @96dpi = 9525 EMU
const CANVAS_W = 1920
const CANVAS_H = 1080
const MAX_TOTAL_UNCOMPRESSED = 200 * 1024 * 1024 // 200MB zip-bomb guard
const MAX_ENTRIES = 5000
// Per-face cap for embedded fonts recovered from ppt/fonts/*.fntdata. Brand
// display/body faces are small; multi-MB faces are full CJK fallbacks that would
// bloat every stored deck and the exported HTML with no visual benefit.
const MAX_EMBEDDED_FONT_BYTES = 4 * 1024 * 1024 // 4MB (recovered TTF/OTF)
// Cap on the COMPRESSED .fntdata part checked BEFORE decompression. MTX
// decompression of a large face is very CPU-heavy (a ~12MB CJK fallback takes
// ~17s), which alone blows the import worker timeout. Brand faces are tens of KB
// compressed, so gating on the raw part size skips the expensive decode entirely.
const MAX_EMBEDDED_FONT_COMPRESSED_BYTES = 2 * 1024 * 1024 // 2MB (raw .fntdata)

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
// styleDefaults (optional) supplies an inherited <size,font> resolved from the
// master/layout <p:txStyles> for this shape's placeholder role, used ONLY when a
// run carries no explicit @_sz / a:latin@typeface. This mirrors PowerPoint text
// inheritance (run rPr -> shape lstStyle -> placeholder lvl defaults -> master
// txStyles) so a footer/body run without an explicit run-level size/font is not
// dropped to the hard-coded chromeToAsd/placeholderToAsd default (24pt / serif).
// It is fully defensive: when styleDefaults is absent, behavior is unchanged.
const extractRuns = (txBody, themeColors, styleDefaults = null) => {
  const runs = []
  let text = ''
  if (!txBody) return { runs, text }
  // The shape's OWN <a:lstStyle><a:lvl1pPr><a:defRPr> is the most-local default
  // (tighter than the master/layout txStyles). Prefer it over styleDefaults.
  let localDefaults = null
  const lstStyle = findChild(txBody, 'a:lstStyle')
  if (lstStyle) {
    const lvl1 = findChild(lstStyle, 'a:lvl1pPr')
    const defRPr = lvl1 ? findChild(lvl1, 'a:defRPr') : null
    if (defRPr) {
      const da = attrsOf(defRPr)
      const dLatin = findChild(defRPr, 'a:latin')
      const dFill = colorFromFill(findChild(defRPr, 'a:solidFill'), themeColors)
      localDefaults = {
        size: da['@_sz'] ? int(Number(da['@_sz']) / 100) : null,
        font: dLatin && attrsOf(dLatin)['@_typeface'] ? String(attrsOf(dLatin)['@_typeface']) : null,
        color: dFill ? dFill.hex : null,
      }
    }
  }
  const inherit = (key) => {
    if (localDefaults && localDefaults[key] != null) return localDefaults[key]
    if (styleDefaults && styleDefaults[key] != null) return styleDefaults[key]
    return null
  }
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
      // Size: explicit run-level @_sz wins; else inherit from lstStyle/txStyles.
      if (attrs['@_sz']) run.size = int(Number(attrs['@_sz']) / 100)
      else { const s = inherit('size'); if (s != null) run.size = s }
      const latin = rPr ? findChild(rPr, 'a:latin') : null
      // Font: explicit run-level a:latin wins; else inherit.
      if (latin && attrsOf(latin)['@_typeface']) run.font = String(attrsOf(latin)['@_typeface'])
      else { const f = inherit('font'); if (f != null) run.font = f }
      const fill = rPr ? findChild(rPr, 'a:solidFill') : null
      const col = colorFromFill(fill, themeColors)
      if (col) run.color = col.hex
      else { const c = inherit('color'); if (c != null) run.color = c }
      runs.push(run)
      text += str
    }
    text += '\n'
  }
  return { runs, text: text.trim(), defaults: localDefaults }
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

// sniffMime inspects the leading bytes of a media file to recover its true image
// type when the file extension is missing, wrong, or generic (e.g. corporate
// templates that ship a PNG as "image6.bin"). Without this the extension-only
// MIME lookup falls back to application/octet-stream, and the render/export
// layer (resolveImageSrc requires a data:image/ prefix) silently drops the
// image — which is how real logos/chrome vanished. Mirrors the pptx-html pilot's
// sniffMime; generic across templates, keyed off bytes not names.
const sniffMime = (buf) => {
  if (!buf || buf.length < 3) return null
  const b = buf
  if (b.length >= 8 && b[0] === 0x89 && b[1] === 0x50 && b[2] === 0x4e && b[3] === 0x47) return 'image/png'
  if (b[0] === 0xff && b[1] === 0xd8 && b[2] === 0xff) return 'image/jpeg'
  if (b[0] === 0x47 && b[1] === 0x49 && b[2] === 0x46) return 'image/gif'
  if (b.length >= 12 && b[0] === 0x52 && b[1] === 0x49 && b[2] === 0x46 && b[3] === 0x46 && b[8] === 0x57 && b[9] === 0x45 && b[10] === 0x42 && b[11] === 0x50) return 'image/webp'
  if (b.length >= 2 && b[0] === 0x42 && b[1] === 0x4d) return 'image/bmp'
  // SVG / XML-wrapped SVG (text).
  const head = b.slice(0, 160).toString('utf-8').trimStart()
  if (head.startsWith('<svg') || (head.startsWith('<?xml') && head.includes('<svg'))) return 'image/svg+xml'
  return null
}


// eotFntdataToTtf recovers a plain TrueType font (Uint8Array) from a PowerPoint
// embedded-font part (ppt/fonts/fontN.fntdata). Those parts are EOT containers
// wrapping an MTX-compressed sfnt (NOT the Word .odttf GUID-XOR obfuscation).
// Returns { bytes, mime, family } or null on any failure/unsupported input.
// Never throws — a bad face is skipped so import still succeeds.
const eotFntdataToTtf = (buf) => {
  try {
    if (!buf || buf.length < 16) return null
    const u8 = buf instanceof Uint8Array ? buf : new Uint8Array(buf)
    let family = ''
    try {
      const meta = mtxDecompressor.parseEotMetadata(u8)
      // EOT name strings are NUL-terminated UTF-16; strip trailing NULs.
      family = String(meta.familyName || '').replace(/\0+$/g, '').trim()
    } catch { /* metadata is best-effort; family comes from presentation.xml anyway */ }
    const ttf = mtxDecompressor.eotToTtf(u8)
    if (!ttf || ttf.length < 4) return null
    // All recovered faces are sfnt TrueType; eotToTtf reconstructs a .ttf.
    return { bytes: ttf, mime: 'font/ttf', family }
  } catch {
    return null
  }
}

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

  // Resolve an OOXML theme font-reference token to a concrete family. Runs that
  // use the theme reference +mj-lt/+mn-lt (major/minor Latin) — or the -ea/-cs
  // variants — carry the token literally in a:latin/@typeface; without this it
  // would leak into markup as font="+mn-lt". themeTokens.displayFont/bodyFont
  // already hold the resolved major/minor families. Concrete names (e.g.
  // "72 Brand") are returned unchanged.
  const resolveFontToken = (tf) => {
    if (!tf) return tf
    const s = String(tf)
    if (/^\+mj-/.test(s)) return themeTokens.displayFont
    if (/^\+mn-/.test(s)) return themeTokens.bodyFont
    return s
  }

  // Corporate templates name concrete brand families (e.g. "72 Brand",
  // "72 Brand Medium") that are NOT installed in a browser. ast-text applies the
  // `font` attribute directly as `font-family`, so an uninstalled single family
  // falls back to the browser default — which is a SERIF (Times), not the deck's
  // sans-serif. Appending a web-safe generic fallback chain makes an uninstalled
  // brand font degrade to Aptos/Arial/sans-serif instead of serif, while still
  // preferring the real brand font when it IS available (embedded/installed).
  // Generic and defensive: if the family already ends in a CSS generic, or is a
  // theme token, it is returned unchanged.
  const FONT_FALLBACK = 'Aptos, Arial, sans-serif'
  // Quote a single font-family name for a CSS font-family value when it is not a
  // valid sequence of CSS identifiers. Per the CSS grammar an unquoted family is
  // a list of identifiers, and a CSS identifier may NOT start with a digit — so a
  // brand family like "72 Brand" is INVALID unquoted and the whole declaration is
  // dropped by the browser, silently falling back to the default serif (Times).
  // We quote when any space-separated word starts with a digit or the name
  // contains a character outside [A-Za-z0-9 _-]. Names with no embedded double
  // quote are wrapped in double quotes; the (rare) name containing a double quote
  // is left unquoted (defensive — it will be sanitized/dropped downstream).
  const cssFontFamilyName = (name) => {
    const n = String(name).trim()
    if (!n) return n
    const needsQuote = /(^|\s)\d/.test(n) || /[^A-Za-z0-9 _-]/.test(n)
    if (!needsQuote) return n
    if (n.includes('"')) return n
    return `"${n}"`
  }
  const withFontFallback = (family) => {
    if (!family) return family
    const s = String(family).trim()
    if (!s) return s
    // Already a comma list: normalize every family in it (quoting brand names
    // like "72 Brand" that are invalid unquoted CSS identifiers) and leave any
    // generic keyword / already-quoted family untouched. This is why we must
    // still process lists — the leading brand family may have been appended a
    // fallback chain upstream and would otherwise ship unquoted (→ serif/Times).
    if (s.includes(',')) {
      return s.split(',').map((part) => {
        const p = part.trim()
        if (!p) return p
        if (/^["']/.test(p)) return p // already quoted
        if (/\b(sans-serif|serif|monospace|cursive|fantasy|system-ui)$/i.test(p)) return p
        return cssFontFamilyName(p)
      }).filter(Boolean).join(', ')
    }
    if (/\b(sans-serif|serif|monospace|cursive|fantasy|system-ui)$/i.test(s)) return cssFontFamilyName(s)
    // Quote the brand family if it is not a valid unquoted CSS identifier list
    // (e.g. starts with a digit like "72 Brand"), then append the web-safe chain.
    return `${cssFontFamilyName(s)}, ${FONT_FALLBACK}`
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
      let mime = MIME[ext]
      // If the extension gives no MIME, or a non-image one (e.g. ".bin"), sniff
      // the bytes so a mislabeled image still resolves at render time.
      if (!mime || !mime.startsWith('image/')) {
        mime = sniffMime(data) || mime || 'application/octet-stream'
      }
      assets[ref] = `data:${mime};base64,${data.toString('base64')}`
    }
    return ref
  }

  // Slide order from sldIdLst + presentation.xml.rels.
  const presRels = parseRels(await readText('ppt/_rels/presentation.xml.rels'))

  // Extract fonts embedded in the .pptx (only present when the source enabled
  // "Embed fonts in the file"). They live under ppt/fonts/*.fntdata as EOT-wrapped
  // TTF/OTF and are mapped by family + variant via <p:embeddedFontLst> in
  // presentation.xml. Corporate templates name concrete brand families (e.g.
  // "72 Brand") that are NOT installed in a browser; without loading the real
  // font file, a bare family name falls back to the browser default serif (Times).
  // We recover each face (strip the EOT header via eotToSfnt), store it as a
  // font-namespaced data: asset (key `font:<family>:<variant>`, distinct from the
  // sha256- image keys), and record a compact manifest under
  // themeTokens.embeddedFonts so the HTML exporter can emit matching @font-face
  // rules. Generic across templates; a .pptx with no embedded fonts is a no-op.
  const collectEmbeddedFonts = async () => {
    try {
      const fontLst = findChild(presentation, 'p:embeddedFontLst')
      if (!fontLst) return
      const variantTags = { 'p:regular': 'regular', 'p:bold': 'bold', 'p:italic': 'italic', 'p:boldItalic': 'boldItalic' }
      const manifest = []
      for (const ef of findAll(fontLst, 'p:embeddedFont')) {
        const fontEl = findChild(ef, 'p:font')
        const family = fontEl ? String(attrsOf(fontEl)['@_typeface'] || '').trim() : ''
        if (!family) continue
        for (const [tag, variant] of Object.entries(variantTags)) {
          const vEl = findChild(ef, tag)
          if (!vEl) continue
          const rid = attrsOf(vEl)['@_r:id']
          if (!rid) continue
          const rel = presRels[rid]
          if (!rel || !rel.target) continue
          const path = resolveTarget('ppt', rel.target)
          if (!path) continue
          const f = zip.file(path)
          if (!f) { warn(`Embedded font ${family} ${variant}: part ${path} missing`); continue }
          let data
          try { data = await f.async('nodebuffer') } catch { warn(`Embedded font ${family} ${variant}: unreadable`); continue }
          // Gate on the COMPRESSED part size BEFORE the (expensive) MTX decode.
          // A large face is a full CJK fallback whose decompression alone can
          // exceed the import timeout; brand faces are tens of KB compressed.
          if (data.length > MAX_EMBEDDED_FONT_COMPRESSED_BYTES) {
            warn(`Embedded font ${family} ${variant}: ${Math.round(data.length / 1024)}KB compressed exceeds ${Math.round(MAX_EMBEDDED_FONT_COMPRESSED_BYTES / 1024)}KB cap; not embedded`)
            continue
          }
          const sfnt = eotFntdataToTtf(data)
          if (!sfnt) { warn(`Embedded font ${family} ${variant}: unsupported EOT/MTX payload; not embedded`); continue }
          // Cap per-face size: brand display/body fonts are small (tens–hundreds of
          // KB). A multi-MB face is almost always a full CJK fallback (e.g. Arial
          // Unicode MS) that would bloat every stored deck + exported HTML for no
          // visual benefit — the web-safe fallback chain covers those glyphs.
          if (sfnt.bytes.length > MAX_EMBEDDED_FONT_BYTES) {
            warn(`Embedded font ${family} ${variant}: ${Math.round(sfnt.bytes.length / 1024)}KB exceeds ${Math.round(MAX_EMBEDDED_FONT_BYTES / 1024)}KB cap; not embedded`)
            continue
          }
          const key = `font:${family}:${variant}`
          if (!assets[key]) {
            assets[key] = `data:${sfnt.mime};base64,${Buffer.from(sfnt.bytes).toString('base64')}`
          }
          manifest.push({ family, variant, assetKey: key })
        }
      }
      // Encode the embedded-font manifest as a single JSON STRING under a theme
      // key. themeTokens flows to the Go side as the deck theme (map[string]string),
      // so a nested array cannot pass through — a string can. The HTML exporter
      // parses this key (and does NOT emit it as a --ast-* CSS variable).
      if (manifest.length) themeTokens['embedded-fonts'] = JSON.stringify(manifest)
    } catch (e) {
      warn(`Embedded font extraction failed: ${e && e.message ? e.message : e}`)
    }
  }
  await collectEmbeddedFonts()

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

  // Inherited text-style defaults for the spTree currently being processed.
  // Set from the master/layout <p:txStyles> before processTree runs so a shape
  // whose runs carry no explicit @_sz / a:latin can inherit the real size/font
  // for its placeholder role instead of the hard-coded chromeToAsd default.
  // {title:{size,font}, body:{size,font}, other:{size,font}} (any may be null).
  let currentTxStyles = null
  // Map an OOXML placeholder role to the txStyles style bucket. Titles use
  // titleStyle; everything decorative/footer/other uses otherStyle; body/content
  // uses bodyStyle. When no placeholder is present (free text box), OTHER is the
  // closest match (footers are userDrawn text boxes with no p:ph).
  const txStyleDefaultFor = (phType) => {
    if (!currentTxStyles) return null
    const t = String(phType || '')
    if (t === 'title' || t === 'ctrTitle') return currentTxStyles.title
    if (t === 'body' || t === 'subTitle') return currentTxStyles.body
    return currentTxStyles.other
  }
  // Parse a master/layout <p:txStyles> into resolved {title,body,other} defaults.
  // Each style's lvl1 <a:defRPr> supplies the inherited @sz (points) and a:latin
  // typeface. Returns null when the element is absent (defensive: no change).
  const parseTxStyles = (txStyles) => {
    if (!txStyles) return null
    const pick = (styleTag) => {
      const st = findChild(txStyles, styleTag)
      if (!st) return null
      const lvl1 = findChild(st, 'a:lvl1pPr')
      const defRPr = lvl1 ? findChild(lvl1, 'a:defRPr') : null
      if (!defRPr) return null
      const da = attrsOf(defRPr)
      const latin = findChild(defRPr, 'a:latin')
      const font = latin && attrsOf(latin)['@_typeface'] ? String(attrsOf(latin)['@_typeface']) : null
      const size = da['@_sz'] ? int(Number(da['@_sz']) / 100) : null
      // Capture the inherited text color from the style's defRPr solidFill.
      // PowerPoint layouts commonly define the title/body color ONLY here (e.g.
      // white title on a dark cover), leaving individual runs uncolored. Without
      // this the placeholder falls back to the renderer's default (black).
      const fillCol = colorFromFill(findChild(defRPr, 'a:solidFill'), themeColors)
      const color = fillCol ? fillCol.hex : null
      if (size == null && font == null && color == null) return null
      return { size, font, color }
    }
    const title = pick('p:titleStyle')
    const body = pick('p:bodyStyle')
    const other = pick('p:otherStyle')
    if (!title && !body && !other) return null
    return { title, body, other }
  }

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
    const { runs, text, defaults } = extractRuns(txBody, themeColors, txStyleDefaultFor(node.ph && node.ph.type))
    if (runs.length) {
      node.runs = runs
      node.text = text
    }
    // Empty placeholders (no runs) still carry an authored color via the shape's
    // own <a:lstStyle> defRPr solidFill (a layout-level override). Preserve it so
    // classify() can backfill the placeholder color when styleOf finds no run.
    if (defaults) node.textDefaults = defaults
    return node
  }

  const processPic = async (pic, affine, slideRels, slideDir) => {
    const spPr = findChild(pic, 'p:spPr')
    let box = readXfrm(spPr)
    if (!box) return null
    box = applyAffine(box, affine)
    const node = { id: nextId('img'), type: 'image', geometry: toCanvasBox(box) }
    if (box.rot) node.rot = box.rot
    if (box.flipH) node.flipH = true
    if (box.flipV) node.flipV = true
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
  //
  // OOXML `hidden="1"` on p:cNvPr is how PowerPoint hides authoring palettes
  // (SAP "Harvey" status balls — 100 stacked squares in a hidden group) and
  // instruction stickers (DRAFT, Dummy data, For discussion). Flattening those
  // groups without honoring hidden paints ~100 black tiles onto every slide.
  const isOOXMLHidden = (el) => {
    const cNvPr = findDeep(el, 'p:cNvPr')
    if (!cNvPr) return false
    const h = attrsOf(cNvPr)['@_hidden']
    return h === '1' || h === 'true'
  }
  const processTree = async (container, affine, slideRels, slideDir, out) => {
    for (const child of childrenOf(container)) {
      const t = tagOf(child)
      switch (t) {
        case 'p:sp': {
          if (isOOXMLHidden(child)) break
          const n = processSp(child, affine)
          if (n) out.push(n)
          break
        }
        case 'p:cxnSp': {
          if (isOOXMLHidden(child)) break
          const n = processSp(child, affine)
          if (n) { n.geom = 'line'; out.push(n) }
          break
        }
        case 'p:pic': {
          if (isOOXMLHidden(child)) break
          const n = await processPic(child, affine, slideRels, slideDir)
          if (n) out.push(n)
          break
        }
        case 'p:graphicFrame': {
          if (isOOXMLHidden(child)) break
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
          if (isOOXMLHidden(child)) break
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
    // image"), so the user/AI can choose among same-role variants.
    //
    // TWO TIERS (see CHROME_KINDS / roleTier below):
    //   • FIXED brand chrome — the stable role set { title, section (divider),
    //     agenda, closing (thank-you/end) }. Reproduced VERBATIM; the AI edits
    //     only the text inside the archetype's recorded fillSlots. This set is
    //     GUARANTEED to exist: a missing role is filled by aliasing the closest
    //     branded layout, else by synthesizing one in the template's OWN style
    //     (master background + master chrome objects + theme tokens) — never a
    //     generic white slab — so the chrome family stays visually coherent.
    //   • FLEXIBLE content — content/blank/etc.; the AI copies the archetype
    //     markup VERBATIM and only edits content inside the recorded fillSlots.
    //
    // Each layout's color/chrome is captured via master→layout background+chrome
    // inheritance (the colorful covers/dividers carry their pictures + accent
    // shapes in the LAYOUT; plain layouts inherit the neutral background from the
    // master). A lossless per-layout / per-sample-slide IR (TemplateModel, schema
    // 3) is also persisted for the future in-browser editor. Everything renders
    // through the existing IR→ASD serializer + ASD runtime (no second renderer).
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
        // Scale the raw OOXML point size to the ASD canvas exactly once, in the
        // same closure where `scale` (line ~532) is defined. Geometry (x/y/w/h)
        // is already scaled by `scale`; a footer at sz="1000" (10pt) must render
        // at ~15 to match its 1.5x-enlarged box rather than a tiny size="10".
        // extractRuns stores the RAW pt value (unscaled); resolution happens here
        // mirroring the resolveFontToken pattern.
        if (r0.size) st.fontSize = Math.max(1, int(r0.size * scale))
        if (r0.color) st.color = r0.color
        if (r0.bold) st.bold = true
        if (r0.italic) st.italic = true
        if (r0.font) st.fontFace = resolveFontToken(r0.font)
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
        const ph = {
          name,
          type,
          x: g.x, y: g.y, w: g.w, h: g.h,
          style: styleOf(node),
          prompt: type === 'title' ? '{{TITLE}}' : '{{BODY}}',
          ooxmlType: node.ph.type,
          idx: node.ph.idx ? Number(node.ph.idx) : 0,
        }
        // A layout title/body placeholder is usually EMPTY (no a:r runs), so
        // styleOf yields no color/size/font. The real text color for that role
        // lives in the shape's own <a:lstStyle> defRPr (a layout-level override,
        // tightest) or the master <p:txStyles> (e.g. a white title on a dark
        // cover). Backfill any style not already resolved from runs so the
        // placeholder renders in its authored color instead of the default black.
        const txDef = txStyleDefaultFor(node.ph.type)
        const own = node.textDefaults || null
        const pickDef = (key) => {
          if (own && own[key] != null) return own[key]
          if (txDef && txDef[key] != null) return txDef[key]
          return null
        }
        const bColor = pickDef('color')
        const bSize = pickDef('size')
        const bFont = pickDef('font')
        if (ph.style.color == null && bColor != null) ph.style.color = bColor
        if (ph.style.fontSize == null && bSize != null) ph.style.fontSize = Math.max(1, int(bSize * scale))
        if (ph.style.fontFace == null && bFont != null) ph.style.fontFace = resolveFontToken(bFont)
        // A placeholder may ship its OWN default paint in the layout: a picture
        // placeholder can carry a default blip image, and any placeholder can
        // carry a default solid fill. Preserve those so the IR is lossless and
        // the ASD projection can render the real default rather than a synthetic
        // panel. (Generic: keyed off the extracted node, not any one template.)
        if (node.type === 'image' && node.props && node.props.assetRef) ph.mediaKey = node.props.assetRef
        if (node.fill) ph.fill = node.fill
        layout.placeholders.push(ph)
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
      // Inherit the master's text-style defaults so layout/sample shapes without
      // explicit run sizes resolve a real size/font (extractRuns reads this).
      currentTxStyles = masterCtx ? (masterCtx.txStyles || null) : null
      if (spTree) await processTree(spTree, null, rels, dir, nodes)
      currentTxStyles = null
      for (const n of nodes) classify(n, layout)
      return layout
    }

    // ---- Determine archetype KIND from a layout's NAME, then placeholders ---
    // The .pptx layouts carry no <p:sldLayout type> attribute (confirmed empty
    // on all 39 in the reference corporate deck), so the human layout NAME is
    // the primary signal. Order matters: check the most specific roles first.
    // The name is NORMALIZED first — non-alphanumerics collapse to single spaces
    // — so "TITLE_SLIDE"→"title slide" and "DividerPage"→"divider page" match.
    const kindOf = (layout, layoutType) => {
      const name = (layout.name || '').toLowerCase().replace(/[^a-z0-9]+/g, ' ').trim()
      const hasTitle = layout.placeholders.some((p) => p.type === 'title')
      const bodyCount = layout.placeholders.filter((p) => p.type === 'body').length
      const picCount = layout.placeholders.filter((p) => p.type === 'image').length
      // Signature-first: a layout carrying a body OR picture placeholder is
      // editable CONTENT even when its name contains "Title" (e.g. "Title and
      // Text", "Title and Text: 2 Columns", "Title and Content", "N Columns -
      // Text and Images", "Text and Screenshot", "Title and Text with Image").
      // Only PURE chrome names (cover/divider/agenda/thank-you) become fixed
      // brand chrome. So the name regexes below are gated so they never fire on a
      // compound content layout, and the loose `\btitle\b` cover match is dropped.
      //
      // Closing family (thank you / contact / copyright / farewell / goodbye) —
      // only when the layout has no body content of its own (Q&A here carries
      // title+body+pic, so it falls through to the content signature).
      if (bodyCount === 0 && picCount <= 1 && /thank|farewell|goodbye|copyright|contact/.test(name)) return 'closing'
      // Agenda / table-of-contents family (the Agenda layout is legitimately fixed).
      if (/agenda|toc|overview|contents|table of contents/.test(name)) return 'agenda'
      // Section / divider / separator family (dividers are fixed brand chrome).
      if (layoutType === 'secHead' || /divider|separator|section|chapter|transition/.test(name)) return 'section'
      // Cover / title family — STRICT: only genuine cover names, never bare
      // "title" (which would wrongly catch "Title and Text/Only/Content").
      if (layoutType === 'title' || /cover|title slide/.test(name) || /^title$/.test(name)) return 'title'
      // Blank.
      if (/blank/.test(name)) return 'blank'
      // Signature fallback: anything with a body or picture placeholder is
      // flexible content; a bare title-only page is an editable content layout,
      // not fixed brand chrome (the stable-chrome guarantee still aliases the
      // branded covers L1-L12 for the fixed 'title' role).
      if (bodyCount > 0 || picCount > 0) return 'content'
      return 'content'
    }

    // ---- Two-tier archetype model -----------------------------------------
    // CHROME_KINDS are the STABLE brand-chrome roles the deck must always be able
    // to offer: title (cover), section (divider), agenda, and closing (thank
    // you / end). Their archetypes are tier="fixed" — the layout is the brand,
    // so the AI reproduces the markup verbatim and edits ONLY the text inside the
    // recorded fillSlots. Every other role (content, blank, ...) is tier=
    // "flexible": the AI adapts the body region to the content type. When a chrome
    // role is missing from the imported layouts it is guaranteed downstream by
    // aliasing the closest branded layout, else by synthesizing one in the
    // template's OWN style (master chrome + theme tokens), never a white slab.
    const CHROME_KINDS = ['title', 'section', 'agenda', 'closing']
    const roleTier = (base) => (CHROME_KINDS.includes(base) ? 'fixed' : 'flexible')

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
        if (st.fontFace && !/[;<>]/.test(st.fontFace) && !/^\+/.test(st.fontFace)) attrs.push(`font="${esc(withFontFallback(st.fontFace))}"`)
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
      if (st.fontFace && !/[;<>]/.test(st.fontFace) && !/^\+/.test(st.fontFace)) attrs.push(`font="${esc(withFontFallback(st.fontFace))}"`)
      const runAttr = st.italic ? ' i="true"' : ''
      const geo = clampGeo(p)
      return `<ast-text id="ph-${idc}" ${geo} ${attrs.join(' ')}><ast-run${runAttr}>${escText(p.prompt || '{{BODY}}')}</ast-run></ast-text>`
    }

    // True when an IR chrome object still has a drawable shape (fill/geom/path)
    // even if classify() tagged it kind:'text' because it also carries a run.
    const objectHasShape = (o) => {
      if (!o) return false
      if (o.kind === 'image' || o.kind === 'ellipse' || o.kind === 'path' || o.kind === 'line') return true
      if (o.geom && o.geom !== 'rect') return true
      if (o.rectRadius) return true
      if (o.paths && o.paths.length) return true
      if (o.fill && (o.fill.color || o.fill.kind === 'gradient')) return true
      return false
    }
    const objectHasText = (o) => !!(o && o.text && String(o.text).trim())

    // Sample slides mix brand structure (colored bars/cards) with dummy authoring
    // artifacts (DRAFT watermarks, <date> tokens, leftover pies). Patterns must
    // keep the structure and drop the junk, or fill_slide produces a mess.
    const isWatermarkText = (o) => {
      const t = String((o && o.text) || '').trim()
      if (/^(draft|confidential|sample|copy|watermark)$/i.test(t)) return true
      const sz = (o && o.style && o.style.fontSize) || 0
      if (sz >= 64 && t.length < 24 && /draft|confidential|sample/i.test(t)) return true
      const area = ((o && o.w) || 0) * ((o && o.h) || 0)
      if (area > 500 * 200 && t.length < 24 && /draft|confidential|sample/i.test(t)) return true
      return false
    }
    const isDummyInstructionText = (text) => {
      const t = String(text || '').trim()
      if (!t) return false
      if (/^(for discussion|internal use only|final slide|backup|update data|dummy data|confidential)$/i.test(t)) return true
      if (/^insert page title/i.test(t)) return true
      if (/start typing to add text/i.test(t)) return true
      if (/^(second|third|fourth|fifth) level$/i.test(t)) return true
      return false
    }
    // Layout dummy the author should replace — fill slots, not frozen chrome.
    // Patterns are generic PowerPoint/template prompts (field captions, "goes here",
    // dummy dates), not a specific corporate deck's copy.
    const isFillablePromptText = (text) => {
      const t = String(text || '').trim()
      if (!t || t.length > 80) return false
      if (/goes here/i.test(t)) return true
      if (/\bhere\b.+\bhere\b/i.test(t)) return true
      if (/^[A-Za-z][A-Za-z0-9 /&-]{1,48}:$/.test(t)) return true // "Contact information:"
      if (/^(your |presenter |speaker |author )?name\b/i.test(t)) return true
      if (/^add .{0,40}(logo|photo|image|picture)\b/i.test(t)) return true
      if (/\bmonth\s*0+\b/i.test(t)) return true
      if (/^(jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\s*0+/i.test(t)) return true
      return false
    }
    const isPlaceholderTokenText = (text) => {
      const t = String(text || '').trim()
      if (!t) return true
      if (isDummyInstructionText(t)) return true
      if (isFillablePromptText(t)) return true
      if (/^[‹<]\s*#\s*[›>]$/.test(t)) return true // slide-number field ‹#›
      if (/<[a-zA-Z][\w-]*>/.test(t)) return true // <initials> <date>
      if (/&lt;[a-zA-Z][\w-]*&gt;/.test(t)) return true
      if (/yyyy[-_/]?MM/.test(t)) return true
      if (/click to (add|edit)/i.test(t)) return true
      if (/footnote/i.test(t) && t.length < 48) return true
      if (/^lorem\b/i.test(t)) return true
      if (/\bxxxx\b/i.test(t)) return true
      return false
    }
    const isWhiteOrEmptyFill = (o) => {
      if (o && o.opacity === 0) return true
      const c = o && o.fill && o.fill.color
      if (!c) return true
      const hex = String(c).replace('#', '').toUpperCase()
      return hex === 'FFF' || hex === 'FFFFFF' || hex === 'FFFFFFFF'
    }
    const isMasterClutter = (o) => {
      if (!o) return true
      if (o.kind === 'image' || o.mediaKey) return false
      if (isWatermarkText(o) || isDummyInstructionText(o.text) || (objectHasText(o) && isPlaceholderTokenText(o.text))) return true
      if (objectHasText(o) && String(o.text).trim()) return false
      if (o.kind === 'line' || (o.line && o.line.color)) return false
      if (o.fill && o.fill.kind === 'gradient') return false
      // Empty / noFill / white tiles from the slide master (SAP templates ship
      // ~100 of these). They paint as opaque white squares in PPTX.
      if (isWhiteOrEmptyFill(o)) return true
      // Authoring palettes (Harvey balls, stacked icon sheets) are small
      // squares. Real accent bars are long and thin — keep those.
      const w = o.w || 0
      const h = o.h || 0
      const maxSide = Math.max(w, h)
      const minSide = Math.min(w, h)
      if (maxSide > 0 && maxSide <= 220 && minSide >= maxSide * 0.55) return true
      return false
    }
    const isSampleJunkShape = (o) => {
      if (!o) return false
      if (objectHasText(o) && !isWatermarkText(o) && !isPlaceholderTokenText(o.text)) return false
      const maxSide = Math.max(o.w || 0, o.h || 0)
      if (o.kind === 'ellipse' || o.geom === 'ellipse') return maxSide >= 140
      return false
    }
    // Dummy/placeholder/watermark copy is always junk, even when it sits on a
    // filled sticker. Keeping those as chrome is how DRAFT / Dummy data leaked
    // onto generated decks. Fillable prompts (Contact information:) are slots.
    const isSampleJunk = (o) => {
      if (isWatermarkText(o) || isSampleJunkShape(o)) return true
      if (!objectHasText(o)) return false
      if (isFillablePromptText(o.text)) return false
      return isPlaceholderTokenText(o.text)
    }
    const insetBox = (o) => {
      const pad = Math.max(10, Math.round(Math.min(o.w || 0, o.h || 0) * 0.08))
      return {
        x: (o.x || 0) + pad,
        y: (o.y || 0) + pad,
        w: Math.max(1, (o.w || 0) - 2 * pad),
        h: Math.max(1, (o.h || 0) - 2 * pad),
        style: o.style,
      }
    }
    const regionHint = (o) => {
      const x = o.x || 0
      const y = o.y || 0
      const col = x < 500 ? 'left' : (x > 1100 ? 'right' : 'center')
      const row = y < 180 ? 'header' : (y > 880 ? 'footer' : 'body')
      return `${col} ${row}`
    }

    const hintRoleForPlaceholder = (p) => {
      if (p.type === 'title') return 'title'
      if (p.type === 'image') return 'image'
      return 'body'
    }
    const hintForPlaceholder = (p) => {
      if (p.type === 'title') return 'Slide title'
      if (p.type === 'image') return p.name || 'Image'
      return p.name || 'Body'
    }
    const hintRoleForText = (o) => {
      const sz = o && o.style && o.style.fontSize
      if (sz && sz >= 32) return 'heading'
      return 'body'
    }
    const hintForText = (o) => {
      const role = hintRoleForText(o) === 'heading' ? 'heading' : 'text'
      return `${regionHint(o)} ${role} — keep short enough to fit the box`
    }

    // Reading order: cluster into rows by similar y, then left-to-right.
    const readingOrder = (items) => {
      const sorted = items.slice().sort((a, b) => (a.y - b.y) || (a.x - b.x))
      const rows = []
      const rowTol = 80
      for (const it of sorted) {
        const row = rows[rows.length - 1]
        if (row && Math.abs(it.y - row[0].y) < rowTol) row.push(it)
        else rows.push([it])
      }
      for (const row of rows) row.sort((a, b) => a.x - b.x)
      return rows.flat()
    }
    const similarSize = (a, b) => {
      const dw = Math.abs((a.w || 0) - (b.w || 0))
      const dh = Math.abs((a.h || 0) - (b.h || 0))
      return dw <= 80 && dh <= 80
    }
    const detectCardGrid = (items) => {
      // Prefer split shape+text cards; also treat similar-sized text boxes as a
      // grid when the sample stored the copy next to the shape, not on it.
      const cands = items.filter((it) => (it.w || 0) >= 180 && (it.h || 0) >= 90 && (it.w || 0) < CANVAS_W * 0.72)
      if (cands.length < 2) return []
      let best = []
      for (const seed of cands) {
        const cluster = cands.filter((it) => similarSize(it, seed))
        if (cluster.length > best.length) best = cluster
      }
      if (best.length < 2) return []
      // A single row of similar text with no card shapes is an icon/timeline
      // row, not a card grid.
      const ys = best.map((it) => it.y)
      const oneRow = Math.max(...ys) - Math.min(...ys) < 80
      if (oneRow && best.length >= 3 && !best.some((it) => it.fromCard)) return []
      return best
    }
    const detectIconRow = (items, cards) => {
      const cardSet = new Set(cards)
      const rest = items.filter((it) => !cardSet.has(it) && (it.w || 0) < CANVAS_W * 0.45)
      if (rest.length < 3) return []
      const sorted = rest.slice().sort((a, b) => a.y - b.y)
      let best = []
      for (let i = 0; i < sorted.length; i += 1) {
        const row = sorted.filter((it) => Math.abs(it.y - sorted[i].y) < 70)
        const xs = row.slice().sort((a, b) => a.x - b.x)
        const uniqueX = []
        for (const it of xs) {
          if (!uniqueX.some((u) => Math.abs(u.x - it.x) < 40)) uniqueX.push(it)
        }
        if (uniqueX.length > best.length) best = uniqueX
      }
      return best.length >= 3 ? best : []
    }
    const gridPosLabel = (item, group) => {
      const snap = (v) => Math.round(v / 60) * 60
      const xs = [...new Set(group.map((g) => snap(g.x)))].sort((a, b) => a - b)
      const ys = [...new Set(group.map((g) => snap(g.y)))].sort((a, b) => a - b)
      const ci = xs.reduce((best, x, i) => (Math.abs(snap(item.x) - x) < Math.abs(snap(item.x) - xs[best]) ? i : best), 0)
      const ri = ys.reduce((best, y, i) => (Math.abs(snap(item.y) - y) < Math.abs(snap(item.y) - ys[best]) ? i : best), 0)
      const colName = xs.length === 1 ? '' : (xs.length === 2 ? ['left', 'right'][ci] : (xs.length === 3 ? ['left', 'middle', 'right'][ci] : `col ${ci + 1}`))
      const rowName = ys.length === 1 ? '' : (ys.length === 2 ? ['top', 'bottom'][ri] : (ys.length === 3 ? ['top', 'middle', 'bottom'][ri] : `row ${ri + 1}`))
      if (rowName && colName) return `${rowName}-${colName}`
      return colName || rowName || 'center'
    }
    const isKickerBox = (it, title, cards) => {
      if (it === title || (cards && cards.includes(it))) return false
      const w = it.w || 0
      const h = it.h || 0
      if (w < CANVAS_W * 0.45 || h > 160) return false
      if (title && it.y + 10 < title.y) return false
      return true
    }

    // Emit a fillable ast-text slot (XML) at an IR box.
    const textSlotToAsd = (box, style, id, prompt, extraAttrs = '') => {
      const st = style || {}
      const col = safeCol(st.color) || (themeTokens.ink && safeCol(themeTokens.ink)) || '#172033'
      const attrs = [`size="${st.fontSize || 28}"`, `color="${col}"`]
      if (st.bold) attrs.push('weight="bold"')
      if (st.fontFace && !/[;<>]/.test(st.fontFace) && !/^\+/.test(st.fontFace)) attrs.push(`font="${esc(withFontFallback(st.fontFace))}"`)
      const runAttr = st.italic ? ' i="true"' : ''
      const geo = clampGeo(box)
      return `<ast-text id="${id}" ${geo} ${attrs.join(' ')}${extraAttrs}><ast-run${runAttr}>${escText(prompt)}</ast-run></ast-text>`
    }
    const CENTER_ATTRS = ' align="ctr" anchor="ctr"'

    const mergePatternPlaceholders = (layoutPhs, samplePhs) => {
      const sample = (samplePhs || []).slice()
      if (sample.some((p) => p.type === 'title')) return sample
      const titles = (layoutPhs || []).filter((p) => p.type === 'title')
      return titles.concat(sample)
    }

    // Serialize an entire IRLayout to a single-root <ast-slide> XML fragment.
    // Returns { markup, fillSlots, slotHints }. fillSlots is the ordered list of
    // ast-text / image ids the author may edit. Chrome (backgrounds, accent
    // shapes, inherited master furniture) is NOT a fill slot.
    //
    // opts.extraTextAsSlots: sample-derived content PATTERNS — extra (non-
    // inherited) text objects become fill slots, and a shape+text card is split
    // into a chrome shape plus a text slot so the colored box survives.
    // opts.inheritedCount: objects[0..inheritedCount) are master/layout chrome
    // and are never turned into slots (footers, logos).
    const layoutToAsd = (layout, opts = {}) => {
      const bg = layout.background || {}
      const parts = []
      const fillSlots = []
      const slotHints = []
      const extraTextAsSlots = !!opts.extraTextAsSlots
      const inheritedCount = opts.inheritedCount == null ? 0 : opts.inheritedCount
      // omitEmptyFullBleedPic: fixed chrome covers should not paint a muted
      // full-bleed panel when the picture placeholder has no media.
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
      let pc = 0
      const pushSlot = (id, role, hint) => {
        fillSlots.push(id)
        slotHints.push({ id, role, hint })
      }
      const pendingText = []
      const pendingPics = []
      const cardChrome = []
      const noteCardChrome = (o) => {
        const w = o.w || 0
        const h = o.h || 0
        if (w < 200 || h < 80 || w >= CANVAS_W * 0.78 || h >= CANVAS_H * 0.65) return
        if (!(o.geom === 'roundRect' || o.rectRadius || (o.fill && o.fill.color))) return
        cardChrome.push({ x: o.x || 0, y: o.y || 0, w, h })
      }
      const queueTextSlot = (box, style, fromCard, placeholder) => {
        pendingText.push({
          x: box.x || 0, y: box.y || 0, w: box.w || 0, h: box.h || 0,
          style: style || {},
          fromCard: !!fromCard,
          placeholder: placeholder || null,
        })
      }
      for (let i = 0; i < (layout.objects || []).length; i += 1) {
        const o = layout.objects[i]
        idc += 1
        const inherited = i < inheritedCount
        if (inherited && isMasterClutter(o)) {
          continue
        }
        if (extraTextAsSlots && isSampleJunk(o)) {
          continue
        }
        if (extraTextAsSlots && inherited && objectHasText(o) && (isWatermarkText(o) || isPlaceholderTokenText(o.text))) {
          // Master date/initials/footnote prompts are dummy tokens, not brand
          // chrome. Drop the text; keep a structural shape if there is one.
          if (objectHasShape(o) && !isSampleJunkShape(o)) {
            const shapeOnly = { ...o, text: '', kind: o.kind === 'text' ? 'rect' : o.kind }
            const m = chromeToAsd(shapeOnly, idc)
            if (m) parts.push(m)
          }
          continue
        }
        const fillablePrompt = !inherited && objectHasText(o) && isFillablePromptText(o.text)
        const extraSlot = extraTextAsSlots && !inherited && objectHasText(o) && !isWatermarkText(o) && !isPlaceholderTokenText(o.text)
        if (fillablePrompt || extraSlot) {
          // Split a card (shape + text) so the colored box stays chrome and the
          // copy becomes a fill slot inset inside the box. Slot ids are assigned
          // later in reading order, not PowerPoint document order.
          if (objectHasShape(o)) {
            const shapeOnly = { ...o, text: '', kind: o.kind === 'text' ? (o.geom === 'ellipse' ? 'ellipse' : o.kind === 'line' ? 'line' : 'rect') : o.kind }
            const m = chromeToAsd(shapeOnly, idc)
            if (m) parts.push(m)
            else warn(`Chrome object not representable in ASD and dropped (${layout.id} #${idc})`)
            idc += 1
            if (!inherited) noteCardChrome(o)
          }
          const box = objectHasShape(o) ? insetBox(o) : o
          queueTextSlot(box, o.style, objectHasShape(o), null)
          continue
        }
        const m = chromeToAsd(o, idc)
        if (m) parts.push(m)
        else warn(`Chrome object not representable in ASD and dropped (${layout.id} #${idc})`)
        if (!inherited && objectHasShape(o)) noteCardChrome(o)
      }
      // Color bars with labels sitting to their right are one card: put the
      // fill slot inside the bar, not beside an empty shape.
      const usedChrome = new Set()
      for (const t of pendingText) {
        if (t.fromCard || t.placeholder) continue
        let best = null
        let bestDx = 1e9
        for (const c of cardChrome) {
          if (usedChrome.has(c)) continue
          const yOverlap = Math.min(t.y + t.h, c.y + c.h) - Math.max(t.y, c.y)
          if (yOverlap < Math.min(t.h || 1, c.h) * 0.35) continue
          if (t.x >= c.x && t.x < c.x + c.w) {
            t.fromCard = true
            usedChrome.add(c)
            best = null
            break
          }
          const dx = t.x - (c.x + c.w)
          if (dx >= -30 && dx <= 160 && dx < bestDx) {
            best = c
            bestDx = dx
          }
        }
        if (best) {
          const inset = insetBox(best)
          t.x = inset.x
          t.y = inset.y
          t.w = inset.w
          t.h = inset.h
          t.fromCard = true
          usedChrome.add(best)
        }
      }
      for (const p of layout.placeholders) {
        if (p.type === 'title' || p.type === 'body') {
          queueTextSlot(p, p.style, false, p)
        } else if (p.type === 'image') {
          pendingPics.push(p)
        }
      }
      // Icon/image rows: one centered slot under each marker. Missing labels
      // (5 icons / 4 captions) get an extra slot so copy stays aligned.
      const snapAndPadMarkerRow = (texts, objects) => {
        const markers = (objects || []).filter((o) => {
          const y = o.y || 0
          if (y < 140 || y > 780) return false
          const maxs = Math.max(o.w || 0, o.h || 0)
          if (maxs < 36 || maxs > 260) return false
          return o.kind === 'image' || o.kind === 'ellipse' || o.geom === 'ellipse'
        }).sort((a, b) => (a.x || 0) - (b.x || 0))
        if (markers.length < 3) return
        const ys = markers.map((m) => m.y || 0)
        if (Math.max(...ys) - Math.min(...ys) > 120) return
        const rowY = Math.max(...markers.map((m) => (m.y || 0) + (m.h || 0))) + 16
        const candidates = texts.filter((t) => !t.placeholder && !t.fromCard)
        const used = new Set()
        for (const m of markers) {
          const cx = (m.x || 0) + (m.w || 0) / 2
          let best = null
          let bestD = 1e9
          for (const t of candidates) {
            if (used.has(t)) continue
            const tcx = (t.x || 0) + (t.w || 0) / 2
            const d = Math.abs(tcx - cx)
            if (d < bestD) { best = t; bestD = d }
          }
          const w = Math.max(160, Math.min(340, Math.round((m.w || 80) * 2.4)))
          if (best && bestD < 300) {
            used.add(best)
            best.w = w
            best.x = Math.round(cx - w / 2)
            best.y = rowY
            best.h = Math.max(72, best.h || 0)
            best.extraAttrs = CENTER_ATTRS
          } else {
            texts.push({
              x: Math.round(cx - w / 2), y: rowY, w, h: 72,
              style: {}, fromCard: false, extraAttrs: CENTER_ATTRS,
            })
          }
        }
      }
      snapAndPadMarkerRow(pendingText, layout.objects)
      // Text slots in reading order (top-to-bottom, left-to-right) so ph-1 is
      // the first thing a reader sees — not PowerPoint's document order.
      const ordered = readingOrder(pendingText)
      const cards = detectCardGrid(ordered)
      const iconRow = detectIconRow(ordered, cards)
      let titleItem = ordered.find((it) => it.placeholder && it.placeholder.type === 'title') || null
      if (!titleItem) {
        const rest = ordered.filter((it) => !cards.includes(it))
        rest.sort((a, b) => {
          const sa = (a.style && a.style.fontSize) || 0
          const sb = (b.style && b.style.fontSize) || 0
          if (sb !== sa) return sb - sa
          return a.y - b.y
        })
        const cand = rest[0]
        if (cand && ((cand.style && cand.style.fontSize) || 0) >= 28) titleItem = cand
        else if (cand && (cand.h || 0) >= 70 && (cand.w || 0) > 600 && cand.y < 280) titleItem = cand
      }
      const emitTextItem = (item, role, hint, prompt, extraAttrs = '') => {
        pc += 1
        const id = `ph-${pc}`
        if (item.placeholder) {
          parts.push(placeholderToAsd(item.placeholder, pc))
        } else {
          parts.push(textSlotToAsd(item, item.style, id, prompt || '{{BODY}}', extraAttrs || item.extraAttrs || ''))
        }
        pushSlot(id, role, hint)
      }
      for (const item of ordered) {
        if (item === titleItem) {
          emitTextItem(item, 'title', 'Slide title', '{{TITLE}}')
          continue
        }
        if (cards.includes(item)) {
          const n = cards.indexOf(item) + 1
          const pos = gridPosLabel(item, cards)
          emitTextItem(item, 'body', `card ${n} of ${cards.length} (${pos}) — one short headline, not a heading+body pair`, '{{BODY}}', CENTER_ATTRS)
          continue
        }
        if (iconRow.includes(item)) {
          const n = iconRow.indexOf(item) + 1
          const pos = gridPosLabel(item, iconRow)
          emitTextItem(item, 'body', `item ${n} of ${iconRow.length} (${pos}) — matches the marker above it; one phrase`, '{{BODY}}', item.extraAttrs || CENTER_ATTRS)
          continue
        }
        if (isKickerBox(item, titleItem, cards)) {
          emitTextItem(item, 'kicker', 'one-line subtitle above the cards — not a card')
          continue
        }
        if (item.placeholder) {
          emitTextItem(item, hintRoleForPlaceholder(item.placeholder), hintForPlaceholder(item.placeholder))
          continue
        }
        emitTextItem(item, hintRoleForText(item), hintForText(item))
      }
      // Picture placeholders after text so a hero sits behind titles that overlap it.
      const omitEmptyFullBleedPic = !!opts.omitEmptyFullBleedPic
      const hasLargeFrame = (layout.objects || []).some((o) => {
        const w = o.w || 0
        const h = o.h || 0
        if (w >= CANVAS_W * 0.9 && h >= CANVAS_H * 0.9) return false
        return w * h > CANVAS_W * CANVAS_H * 0.12
      })
      for (const p of pendingPics) {
        const hasBorrowGeo = p.borrowW != null && p.borrowH != null
        const box = hasBorrowGeo
          ? { x: p.borrowX, y: p.borrowY, w: p.borrowW, h: p.borrowH }
          : p
        const area = (box.w || 0) * (box.h || 0)
        const fullBleed = area > CANVAS_W * CANVAS_H * 0.35
        if (p.mediaKey) {
          pc += 1
          const g = clampGeo(box)
          const flipAttr = `${p.flipH ? ' flip-h="true"' : ''}${p.flipV ? ' flip-v="true"' : ''}`
          const picId = `ph-pic-${pc}`
          parts.push(`<ast-image id="${picId}" ${g} asset-ref="${esc(p.mediaKey)}" fit="cover"${flipAttr} decorative="true"></ast-image>`)
          pushSlot(picId, 'image', hintForPlaceholder(p))
          continue
        }
        // Empty picture placeholder. A full-bleed muted panel on a sparse cover
        // is a cyan slab — omit it. Do not omit when a large frame (anvil, split
        // panel) is meant to hold the photo; dropping it leaves an empty hole.
        if (omitEmptyFullBleedPic && fullBleed && !hasLargeFrame) continue
        pc += 1
        const picId = `ph-pic-${pc}`
        const g = clampGeo(box)
        if (fullBleed) {
          parts.push(`<ast-shape id="${picId}" kind="rect" ${g} geom="rect" alt="${esc(p.name || 'Image')}"></ast-shape>`)
        } else {
          const panel = (p.fill && safeCol(p.fill)) || safeCol(themeTokens.muted) || safeCol(themeTokens.accent2) || '#E2E8F0'
          parts.push(`<ast-shape id="${picId}" kind="rect" ${g} geom="rect" fill="${panel}" alt="${esc(p.name || 'Image')}"></ast-shape>`)
        }
        pushSlot(picId, 'image', hintForPlaceholder(p))
      }
      // Do NOT inject a generic title/body hole onto layouts that already have
      // fillable slots (Title Only, Full Bleed, Quote, Blank, sample patterns).
      // A fake ph-body at (160,320,1600,600) is what turned composition canvases
      // into bland bullet slides. Only synthesize slots when the layout is a
      // genuinely empty synth that the chrome-guarantee path created with none.
      if (fillSlots.length === 0 && opts.allowInject) {
        parts.push(`<ast-text id="ph-title" x="160" y="120" w="1600" h="160" size="54" color="${safeCol(themeTokens.ink) || '#172033'}" weight="bold"><ast-run>{{TITLE}}</ast-run></ast-text>`)
        pushSlot('ph-title', 'title', 'Slide title')
        parts.push(`<ast-text id="ph-body" x="160" y="320" w="1600" h="600" size="28" color="${safeCol(themeTokens.ink) || '#172033'}"><ast-run>{{BODY}}</ast-run></ast-text>`)
        pushSlot('ph-body', 'body', 'Body')
      }
      const slideId = layout.id.replace(/[^a-zA-Z0-9-]/g, '-') || 'layout'
      return { markup: `<ast-slide id="${slideId}">${parts.join('')}</ast-slide>`, fillSlots, slotHints }
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
    // The first non-null master context seen while walking layouts is the deck's
    // shared brand chrome (single master in a corporate template). synthChrome
    // reuses its background + decorative objects so a synthesized chrome slide
    // shares the deck's real background/logo rather than being a generic slab.
    let sharedMasterCtx = null
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
      // Capture the master's <p:txStyles> so shapes without explicit run sizes
      // inherit the real title/body/other size+font (used by extractRuns).
      const masterTxStyles = parseTxStyles(findChild(masterEl, 'p:txStyles'))
      // Process the master spTree to IR, then keep only decorative objects.
      phCounter = 0
      currentTxStyles = masterTxStyles
      const tmp = { id: 'master', name: 'master', background: bg, objects: [], placeholders: [] }
      const nodes = []
      if (spTree) await processTree(spTree, null, rels, dir, nodes)
      for (const n of nodes) classify(n, tmp)
      currentTxStyles = null
      const ctx = {
        bg,
        chromeObjects: tmp.objects.filter((o) => !isMasterClutter(o)),
        txStyles: masterTxStyles,
      }
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
    // Resolve a slide's layout path from its .rels (slideLayout relationship).
    const layoutPathOfSlide = (slideRels, slideDir) => {
      for (const id of Object.keys(slideRels)) {
        const r = slideRels[id]
        if (r && r.type && /slideLayout$/.test(r.type)) {
          return resolveTarget(slideDir, r.target)
        }
      }
      return null
    }

    // ---- Sample slides (extracted BEFORE layouts so a layout with an EMPTY
    // picture placeholder can borrow the matching authored photo) --------------
    // Corporate templates author the hero photo on the SAMPLE SLIDE as a
    // free-floating <p:pic> (often with NO <p:ph> binding), while the LAYOUT
    // ships only an empty <p:ph type="pic"> "insert picture here" slot. Without
    // the borrow the layout archetype has no image for that region and
    // layoutToAsd falls back to a neutral panel (the reported blue box). We keep
    // each sample's layout path so borrowSampleImages() can find the right photo.
    // After layouts are classified, samples whose layout is NOT brand chrome
    // (cover/divider/agenda/closing) are promoted to fillable content PATTERNS
    // so the designed cards/boxes survive into the authoring catalog.
    const irSlides = []
    const samplesByLayoutPath = {}
    for (const p of slidePaths) {
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
      const lp = layoutPathOfSlide(rels, dir)
      if (lp) (samplesByLayoutPath[lp] = samplesByLayoutPath[lp] || []).push(ir)
    }

    // Intersection-over-union of two [x,y,w,h] boxes; used to match an authored
    // sample photo to the layout's empty picture-placeholder region.
    const boxIoU = (a, b) => {
      const ix = Math.max(a.x, b.x), iy = Math.max(a.y, b.y)
      const ix2 = Math.min(a.x + a.w, b.x + b.w), iy2 = Math.min(a.y + a.h, b.y + b.h)
      const iw = Math.max(0, ix2 - ix), ih = Math.max(0, iy2 - iy)
      const inter = iw * ih
      const uni = (a.w * a.h) + (b.w * b.h) - inter
      return uni > 0 ? inter / uni : 0
    }

    // For a layout whose image placeholder(s) carry no default blip, borrow the
    // best-matching authored PHOTO from a sample slide that USES this layout.
    // Matching is generic (no per-template assumptions). Corporate covers place
    // the real photo full-bleed BEHIND a colored vector "anvil" overlay (a
    // single-path SVG shape). A naive highest-IoU pick would grab that small
    // vector overlay (it sits exactly over the placeholder box) instead of the
    // photo, reproducing the wrong asset (the flat green shape). So we RANK:
    //   1. raster photos (non-SVG) strongly preferred over vector/SVG shapes;
    //   2. then larger area (a real photo covers more than a decorative accent);
    //   3. then box overlap (IoU) with the placeholder as the tie-breaker.
    // Only the mediaKey (the picture FILL) is copied — geometry and everything
    // else stay the layout's own, so this is not "sample slide as archetype".
    const isRasterAsset = (mediaKey) => {
      const d = assets[mediaKey]
      // data:image/<type>;base64,… — treat everything that is not svg as raster.
      return typeof d === 'string' && /^data:image\//.test(d) && !/^data:image\/svg\+xml/.test(d)
    }
    const borrowSampleImages = (layoutIR, layoutPath) => {
      const samples = samplesByLayoutPath[layoutPath]
      if (!samples || !samples.length) return 0
      // Collect candidate images (mediaKey + box) across all samples for this layout.
      const candidates = []
      // Normalize each candidate's box to {x,y,w,h,flipH,flipV} in canvas units.
      // Sample OBJECTS carry geometry under o.geometry (+ node-level flipH/flipV
      // set by processPic); sample PLACEHOLDERS carry x/y/w/h directly. Capturing
      // a uniform box lets the borrow reproduce the sample picture's OWN geometry
      // and horizontal/vertical flip, not just its mediaKey.
      const boxOf = (o) => ({
        x: o.x != null ? o.x : (o.geometry ? o.geometry.x : 0),
        y: o.y != null ? o.y : (o.geometry ? o.geometry.y : 0),
        w: o.w != null ? o.w : (o.geometry ? o.geometry.w : 0),
        h: o.h != null ? o.h : (o.geometry ? o.geometry.h : 0),
        flipH: !!o.flipH,
        flipV: !!o.flipV,
      })
      for (const s of samples) {
        for (const o of (s.objects || [])) {
          if (o.kind === 'image' && o.mediaKey) candidates.push({ mediaKey: o.mediaKey, box: boxOf(o) })
        }
        for (const ph of (s.placeholders || [])) {
          if (ph.type === 'image' && ph.mediaKey) candidates.push({ mediaKey: ph.mediaKey, box: boxOf(ph) })
        }
      }
      if (!candidates.length) return 0
      // Composite score: raster beats vector by a wide margin, then area, then IoU.
      const scoreFor = (c, ph) => {
        const raster = isRasterAsset(c.mediaKey) ? 1000000 : 0
        const area = (c.box.w || 0) * (c.box.h || 0)
        // Normalize area into a modest band so it never outweighs the raster flag
        // but reliably separates a full-bleed photo from a small accent shape.
        const areaScore = Math.min(area / (CANVAS_W * CANVAS_H), 1) * 1000
        const iou = boxIoU(ph, c.box) * 100
        return raster + areaScore + iou
      }
      let enriched = 0
      const usedKeys = new Set()
      for (const ph of layoutIR.placeholders) {
        if (ph.type !== 'image' || ph.mediaKey) continue
        let best = null, bestScore = -1
        for (const c of candidates) {
          if (usedKeys.has(c.mediaKey)) continue
          const s = scoreFor(c, ph)
          if (s > bestScore) { bestScore = s; best = c }
        }
        // Fallback: first unused candidate (an on-brand photo beats a blank panel).
        if (!best) {
          best = candidates.find((c) => !usedKeys.has(c.mediaKey)) || null
        }
        if (best) {
          ph.mediaKey = best.mediaKey
          usedKeys.add(best.mediaKey)
          // Preserve the sample picture's OWN geometry + flip so the default hero
          // renders faithfully (correct size/position/mirroring) rather than being
          // squeezed into the placeholder's small declared hole. Stored in separate
          // borrow* fields so a genuinely-empty placeholder (no borrow) still uses
          // its own box. layoutToAsd prefers borrow* geometry when present.
          ph.borrowX = best.box.x
          ph.borrowY = best.box.y
          ph.borrowW = best.box.w
          ph.borrowH = best.box.h
          if (best.box.flipH) ph.flipH = true
          if (best.box.flipV) ph.flipV = true
          enriched += 1
        }
      }
      return enriched
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
      if (!sharedMasterCtx && masterCtx) sharedMasterCtx = masterCtx
      const showMasterSp = attrsOf(layoutEl)['@_showMasterSp'] !== '0'
      phCounter = 0
      const ir = await buildIRLayout(spTree, cSld, rels, dir, id, rawName, masterCtx, showMasterSp)
      // showMasterSp=0 suppresses the master's decorative chrome. That is only a
      // fidelity LOSS when the layout does not carry its own chrome instead — the
      // 12 branded covers/dividers set showMasterSp=0 precisely because they paint
      // their own full-page chrome (bg + anvil + logo + footer), so warning on all
      // of them is false-alarm noise. buildIRLayout prepends master chrome only
      // when showMasterSp is true, so with showMasterSp=0 ir.objects holds ONLY the
      // layout's own objects — warn only when that is genuinely empty.
      if (masterCtx && !showMasterSp && ir.objects.length === 0) {
        warn(`Layout "${rawName}" suppresses master chrome and has no own decorative chrome; slide may render sparse`)
      }
      // A layout may ship an EMPTY picture placeholder (a "insert picture here"
      // slot with no default blip). Borrow the matching authored photo from a
      // sample slide that uses this layout so the hero image renders instead of
      // a synthetic panel. Only the picture FILL (mediaKey) is copied.
      borrowSampleImages(ir, ln)
      ir._layoutPath = ln
      ir._showMasterSp = showMasterSp
      ir._layoutType = layoutType
      ir._masterCount = (showMasterSp && masterCtx && masterCtx.chromeObjects) ? masterCtx.chromeObjects.length : 0
      irLayouts.push(ir)
      const baseKind = kindOf(ir, layoutType)
      const kind = uniqueKind(baseKind)
      const tier = roleTier(baseKind)
      // Keep master-inherited chrome on flexible layouts too. Chrome is no longer
      // copied through the model (fill_slide substitutes slots server-side), so
      // stripping it only produced unbranded content slides. inheritedCount lets
      // layoutToAsd drop leftover master clutter (dummy stickers, empty tiles).
      const { markup, fillSlots, slotHints } = layoutToAsd(ir, {
        inheritedCount: ir._masterCount,
        omitEmptyFullBleedPic: tier === 'fixed',
      })
      archetypes.push({ kind, title: rawName, markup, tier, fillSlots, slotHints, _layout: ir })
    }

    // Promote the richest variant of each chrome role to the unsuffixed kind
    // (title, section, closing) so fill_slide("title") gets a photo cover, not
    // the first empty "White cover with blue pattern".
    const stripKindSuffix = (k) => String(k || '').replace(/-\d+$/, '')
    const chromeRichness = (a) => {
      let s = 0
      const re = /<ast-image\b([^>]*)>/g
      let m
      let hero = 0
      while ((m = re.exec(a.markup))) {
        const tag = m[1]
        if (!/asset-ref=/.test(tag)) continue
        const w = Number((tag.match(/\bw="(\d+)"/) || [])[1] || 0)
        const h = Number((tag.match(/\bh="(\d+)"/) || [])[1] || 0)
        const area = w * h
        // Logos (~100×60) are not a cover photo. Score a real hero by area.
        if (area >= 400 * 300) hero += 120
        else if (area >= 250 * 180) hero += 40
      }
      s += hero
      const shapes = (a.markup.match(/<ast-shape /g) || []).length
      s += Math.min(shapes, 15) * 4
      if ((a.fillSlots || []).some((id) => String(id).startsWith('ph-pic-')) && hero >= 120) s += 40
      const nm = (a.title || '').toLowerCase()
      if (/image|photo|picture|cover/.test(nm) && hero >= 120) s += 20
      if (/image/.test(nm) && hero < 120) s -= 25
      if (shapes <= 2 && (a.fillSlots || []).length <= 1 && hero < 120) s -= 40
      return s
    }
    for (const base of CHROME_KINDS) {
      const group = archetypes.filter((a) => stripKindSuffix(a.kind) === base)
      if (group.length < 2) continue
      let best = group[0]
      for (const a of group) {
        if (chromeRichness(a) > chromeRichness(best)) best = a
      }
      const unsuffixed = group.find((a) => a.kind === base)
      if (unsuffixed && best !== unsuffixed) {
        const tmp = unsuffixed.kind
        unsuffixed.kind = best.kind
        best.kind = tmp
      }
    }

    // Guarantee the STABLE brand-chrome set { title, section, agenda, closing }
    // plus a flexible content role. For each role missing from the imported
    // layouts we PREFER aliasing the closest branded layout (so the role is a
    // real on-brand slide), and only SYNTHESIZE one when no layout fits — and
    // even then in the template's OWN style (shared master background + master
    // chrome objects + theme tokens), never a generic white slab. This is what
    // makes the chrome family coherent and stops the AI from receiving a blank
    // white archetype it would otherwise hand-rebuild off-brand.
    const surface = safeCol(themeTokens.surface) || '#FFFFFF'
    const ink = safeCol(themeTokens.ink) || '#172033'
    const accent = safeCol(themeTokens.accent) || '#2563EB'

    // Score how well an existing archetype's source layout can stand in for a
    // wanted chrome/content role. Higher is better; 0 means unsuitable.
    const scoreLayoutForRole = (arch, want) => {
      const l = arch._layout
      if (!l) return 0
      const nm = (l.name || '').toLowerCase().replace(/[^a-z0-9]+/g, ' ').trim()
      const hasTitle = l.placeholders.some((p) => p.type === 'title')
      const bodyCount = l.placeholders.filter((p) => p.type === 'body').length
      const baseKind = arch.kind.replace(/-\d+$/, '')
      if (want === 'title') {
        if (/cover|title slide|\btitle\b/.test(nm)) return 5
        if (hasTitle && bodyCount === 0) return 3
        if (hasTitle) return 2
      } else if (want === 'section') {
        if (/divider|separator|section|chapter|transition/.test(nm)) return 5
        if (hasTitle && bodyCount <= 1) return 2
      } else if (want === 'agenda') {
        if (/agenda|toc|overview|contents/.test(nm)) return 5
        if (hasTitle && bodyCount >= 1) return 2
      } else if (want === 'closing') {
        if (/thank|closing|contact|farewell|goodbye|conclusion|end/.test(nm)) return 5
        if (baseKind === 'title' || (hasTitle && bodyCount === 0)) return 2
      } else if (want === 'content') {
        if (bodyCount > 0) return 4
        if (hasTitle) return 1
      }
      return 0
    }

    // Synthesize a chrome slide for `want` in the template's own style, reusing
    // the shared master background + master chrome objects (logo/accent bars) so
    // it belongs to the same visual family, and adding only role-appropriate
    // fillable text (+ a section accent rule). Returns an IRLayout-shaped object
    // fed through layoutToAsd, so the fill slots are recorded automatically.
    const synthChrome = (want, siblingBg) => {
      const mc = sharedMasterCtx
      // Background: master bg → closest branded layout bg → theme surface.
      let background = mc && mc.bg ? mc.bg : null
      if ((!background || (background.kind === 'solid' && background.color === surface)) && siblingBg) {
        background = siblingBg
      }
      if (!background) background = { kind: 'solid', color: surface }
      // Copy the shared master chrome objects (logo, accent bars, brand shapes)
      // so the synthesized slide carries the deck's real chrome behind the text.
      const objects = mc && Array.isArray(mc.chromeObjects) ? mc.chromeObjects.map((o) => ({ ...o })) : []
      const placeholders = []
      const bgIsDark = (() => {
        const c = (background.kind === 'solid' && safeCol(background.color)) || surface
        const n = parseInt(c.slice(1, 7), 16)
        const r = (n >> 16) & 255, g = (n >> 8) & 255, b = n & 255
        return (0.299 * r + 0.587 * g + 0.114 * b) < 140
      })()
      const onBg = bgIsDark ? surface : ink
      // Every synthesized chrome slide carries a brand accent bar so it reads as
      // part of the deck's chrome family (not a blank white slate) even when the
      // master supplies no decorative objects.
      if (want === 'title') {
        objects.push({ kind: 'rect', x: 160, y: 360, w: 220, h: 12, fill: { kind: 'solid', color: accent }, geom: 'rect' })
        placeholders.push({ name: 'title-1', type: 'title', x: 160, y: 420, w: 1600, h: 200, style: { fontSize: 72, color: onBg, bold: true, fontFace: themeTokens.displayFont }, prompt: '{{TITLE}}' })
        placeholders.push({ name: 'body-1', type: 'body', x: 160, y: 640, w: 1600, h: 120, style: { fontSize: 32, color: onBg, fontFace: themeTokens.bodyFont }, prompt: '{{BODY}}' })
      } else if (want === 'section') {
        objects.push({ kind: 'rect', x: 160, y: 560, w: 480, h: 12, fill: { kind: 'solid', color: accent }, geom: 'rect' })
        placeholders.push({ name: 'title-1', type: 'title', x: 160, y: 380, w: 1600, h: 180, style: { fontSize: 64, color: accent, bold: true, fontFace: themeTokens.displayFont }, prompt: '{{TITLE}}' })
        placeholders.push({ name: 'body-1', type: 'body', x: 160, y: 600, w: 1600, h: 160, style: { fontSize: 36, color: onBg, fontFace: themeTokens.bodyFont }, prompt: '{{BODY}}' })
      } else if (want === 'agenda') {
        objects.push({ kind: 'rect', x: 160, y: 240, w: 220, h: 12, fill: { kind: 'solid', color: accent }, geom: 'rect' })
        placeholders.push({ name: 'title-1', type: 'title', x: 160, y: 120, w: 1600, h: 140, style: { fontSize: 56, color: accent, bold: true, fontFace: themeTokens.displayFont }, prompt: '{{TITLE}}' })
        placeholders.push({ name: 'body-1', type: 'body', x: 160, y: 320, w: 1600, h: 640, style: { fontSize: 36, color: onBg, fontFace: themeTokens.bodyFont }, prompt: '{{BODY}}' })
      } else if (want === 'closing') {
        objects.push({ kind: 'rect', x: 160, y: 400, w: 220, h: 12, fill: { kind: 'solid', color: accent }, geom: 'rect' })
        placeholders.push({ name: 'title-1', type: 'title', x: 160, y: 440, w: 1600, h: 200, style: { fontSize: 80, color: accent, bold: true, fontFace: themeTokens.displayFont }, prompt: '{{TITLE}}' })
        placeholders.push({ name: 'body-1', type: 'body', x: 160, y: 680, w: 1600, h: 120, style: { fontSize: 30, color: onBg, fontFace: themeTokens.bodyFont }, prompt: '{{BODY}}' })
      } else {
        placeholders.push({ name: 'title-1', type: 'title', x: 160, y: 120, w: 1600, h: 120, style: { fontSize: 48, color: onBg, bold: true, fontFace: themeTokens.displayFont }, prompt: '{{TITLE}}' })
        placeholders.push({ name: 'body-1', type: 'body', x: 160, y: 280, w: 1600, h: 680, style: { fontSize: 28, color: onBg, fontFace: themeTokens.bodyFont }, prompt: '{{BODY}}' })
      }
      return { id: uniqueLayoutId(`synth-${want}`), name: `${want.charAt(0).toUpperCase()}${want.slice(1)}`, background, objects, placeholders }
    }

    // Roles to guarantee: the stable chrome set (fixed) + a flexible content role.
    const presentKinds = new Set(archetypes.map((a) => a.kind.replace(/-\d+$/, '')))
    for (const want of ['title', 'section', 'agenda', 'closing', 'content']) {
      if (presentKinds.has(want)) continue
      // (a) Prefer aliasing the best-scoring existing branded layout.
      let best = null
      let bestScore = 0
      for (const a of archetypes) {
        if (!a._layout) continue
        const s = scoreLayoutForRole(a, want)
        if (s > bestScore) { best = a; bestScore = s }
      }
      const tier = roleTier(want)
      if (best && bestScore >= 2) {
        phCounter = 0
        const { markup, fillSlots, slotHints } = layoutToAsd(best._layout, {
          inheritedCount: best._layout._masterCount || 0,
          omitEmptyFullBleedPic: true,
        })
        archetypes.push({ kind: uniqueKind(want), title: best.title, markup, tier, fillSlots, slotHints, _layout: best._layout })
        continue
      }
      // (b) Else synthesize in the template's own style (master chrome + tokens).
      const siblingBg = (archetypes.find((a) => a._layout && a._layout.background && a._layout.background.kind === 'image') || {})._layout
      phCounter = 0
      const synth = synthChrome(want, siblingBg ? siblingBg.background : null)
      const { markup, fillSlots, slotHints } = layoutToAsd(synth, { allowInject: true })
      if (!sharedMasterCtx || !sharedMasterCtx.chromeObjects || sharedMasterCtx.chromeObjects.length === 0) {
        warn(`Synthesized chrome role ${want} from theme tokens; no master chrome available`)
      }
      archetypes.push({ kind: uniqueKind(want), title: synth.name, markup, tier, fillSlots, slotHints, _layout: synth })
    }

    // ---- Content patterns from sample slides --------------------------------
    // Layouts for body roles are empty placeholder holes. The designed cards,
    // colored boxes, icon rows, etc. live as free shapes on the SAMPLE slides
    // (typically authored on Title Only). Promote those samples to fillable
    // pattern archetypes so the model can fill them instead of copying a
    // title+body hole. Covers/dividers/agenda/closing samples are skipped —
    // those roles already have branded layout archetypes.
    const isDesignedExtra = (o) => {
      if (!o) return false
      if (o.kind === 'image' || o.kind === 'ellipse' || o.kind === 'path' || o.kind === 'line') return true
      if (o.geom === 'roundRect' || o.rectRadius) return true
      if (o.paths && o.paths.length) return true
      if (objectHasText(o)) return true
      if (o.fill && o.fill.color) return true
      return false
    }
    const patternLabel = (extras, slotCount, hints) => {
      const cardHints = (hints || []).filter((h) => /card \d+ of \d+/i.test(h.hint || ''))
      if (cardHints.length >= 2) {
        const m = String(cardHints[0].hint).match(/of (\d+)/)
        const n = m ? m[1] : String(cardHints.length)
        return `${n} cards — one phrase each`
      }
      if ((hints || []).some((h) => /item \d+ of \d+/i.test(h.hint || ''))) {
        return 'Icon row — one phrase per marker'
      }
      const rr = extras.filter((o) => o.geom === 'roundRect' || o.rectRadius).length
      const ell = extras.filter((o) => o.kind === 'ellipse').length
      const img = extras.filter((o) => o.kind === 'image').length
      const cards = extras.filter((o) => objectHasShape(o) && (o.fill || o.geom === 'roundRect' || o.rectRadius)).length
      if (rr >= 2) return `${rr} rounded cards`
      if (ell >= 2 && img) return 'Icon row with images'
      if (ell >= 2) return `${ell} icon markers`
      if (img >= 2) return `${img} images with text`
      if (cards >= 2) return `${cards} content cards`
      if (slotCount > 0) return `Designed content (${slotCount} slots)`
      return 'Designed content'
    }
    const scorePattern = (p) => {
      const slots = (p.fillSlots || []).length
      let s = 0
      if (slots >= 3 && slots <= 8) s += 10
      else if (slots > 20) s -= 15
      if (/roundRect/.test(p.markup)) s += 8
      const shapes = (p.markup.match(/<ast-shape /g) || []).length
      if (shapes > 40) s -= 20
      else if (shapes >= 4 && shapes <= 25) s += 5
      return s
    }

    const layoutByPath = {}
    for (const ir of irLayouts) {
      if (ir._layoutPath) layoutByPath[ir._layoutPath] = ir
    }
    const patternCandidates = []
    for (const [lp, samples] of Object.entries(samplesByLayoutPath)) {
      const layoutIR = layoutByPath[lp]
      if (!layoutIR) continue
      const baseKind = kindOf(layoutIR, layoutIR._layoutType || '')
      if (CHROME_KINDS.includes(baseKind)) continue
      // layoutIR.objects already includes master chrome when showMasterSp is on.
      const inherited = layoutIR.objects || []
      for (const sample of samples) {
        const extras = (sample.objects || []).filter((o) => !isSampleJunk(o) && !isMasterClutter(o))
        if (!extras.some(isDesignedExtra)) continue
        const bg = (sample.background && sample.background.kind === 'image' && sample.background.mediaKey)
          ? sample.background
          : (layoutIR.background || sample.background)
        const merged = {
          id: sample.id,
          name: sample.name,
          background: bg,
          objects: inherited.concat(extras),
          placeholders: mergePatternPlaceholders(layoutIR.placeholders, sample.placeholders),
        }
        const { markup, fillSlots, slotHints } = layoutToAsd(merged, {
          extraTextAsSlots: true,
          inheritedCount: inherited.length,
        })
        if (!fillSlots.length) continue
        // Widget sheets (every Harvey ball as a "card") are not body layouts.
        if (fillSlots.length > 16) continue
        if ((markup.match(/<ast-shape /g) || []).length > 30) continue
        patternCandidates.push({
          title: patternLabel(extras, fillSlots.length, slotHints),
          markup,
          fillSlots,
          slotHints,
          _layout: merged,
        })
      }
    }
    patternCandidates.sort((a, b) => scorePattern(b) - scorePattern(a))
    // Dedup labels so several "3 rounded cards" variants stay distinguishable.
    const patternLabelCounts = {}
    for (const p of patternCandidates) {
      const base = p.title
      patternLabelCounts[base] = (patternLabelCounts[base] || 0) + 1
      const n = patternLabelCounts[base]
      const title = n === 1 ? base : `${base} (${n})`
      archetypes.push({
        kind: uniqueKind('pattern'),
        title,
        markup: p.markup,
        tier: 'flexible',
        fillSlots: p.fillSlots,
        slotHints: p.slotHints,
        _layout: p._layout,
      })
    }

    const templateModel = {
      schema: 3,
      size: { w: CANVAS_W, h: CANVAS_H },
      theme: themeTokens,
      layouts: irLayouts.map((l) => {
        const { _layoutPath, _showMasterSp, _layoutType, _masterCount, ...rest } = l
        return rest
      }),
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
      archetypes: archetypes.map((a) => ({
        kind: a.kind,
        title: a.title,
        markup: a.markup,
        tier: a.tier,
        fillSlots: a.fillSlots,
        slotHints: a.slotHints,
      })),
      templateModel,
    }
    ok(template)
  }
} catch (error) {
  fail(error)
}
