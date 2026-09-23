import { html, noChange, svg, type TemplateResult } from 'lit'

import { PositionedElement } from './base'

type GradientStop = { pos: number, color: string }
type Gradient = { kind: 'linear' | 'radial', angle: number, cx?: number, cy?: number, stops: GradientStop[] }

let gradientSeq = 0

/**
 * OOXML roundRect default adj is 16667 — one sixth of the shorter side.
 * Imported shapes override this via rect-radius (canvas px).
 */
export function roundRectRadius(w: number, h: number, authored?: number): number {
  const shorter = Math.min(w, h)
  if (shorter <= 0) return 0
  const raw = authored != null && Number.isFinite(authored) && authored > 0
    ? authored
    : shorter / 6
  return Math.min(raw, shorter / 2)
}

/** CSS equivalent of the SVG gradient, so roundRect can stay a CSS box. */
function cssGradient(g: Gradient): string {
  const stops = g.stops.map(s => `${s.color} ${s.pos}%`).join(', ')
  if (g.kind === 'radial') {
    const cx = g.cx && g.cx > 0 ? g.cx : 80
    const cy = g.cy && g.cy > 0 ? g.cy : 8
    return `radial-gradient(circle at ${cx}% ${cy}%, ${stops})`
  }
  return `linear-gradient(${g.angle}deg, ${stops})`
}

/** Geometry presets mapped to an SVG path `d` in the shape's own box. */
const GEOM_PATHS: Record<string, (w: number, h: number, radius?: number) => string> = {
  rect: (w, h) => `M0 0 H${w} V${h} H0 Z`,
  roundRect: (w, h, radius) => {
    const r = roundRectRadius(w, h, radius)
    return `M${r} 0 H${w - r} Q${w} 0 ${w} ${r} V${h - r} Q${w} ${h} ${w - r} ${h} H${r} Q0 ${h} 0 ${h - r} V${r} Q0 0 ${r} 0 Z`
  },
  ellipse: (w, h) => {
    const rx = w / 2, ry = h / 2
    return `M0 ${ry} A${rx} ${ry} 0 1 0 ${w} ${ry} A${rx} ${ry} 0 1 0 0 ${ry} Z`
  },
  triangle: (w, h) => `M${w / 2} 0 L${w} ${h} L0 ${h} Z`,
  diamond: (w, h) => `M${w / 2} 0 L${w} ${h / 2} L${w / 2} ${h} L0 ${h / 2} Z`,
  line: (w, h) => `M0 0 L${w} ${h}`,
}

export class AstShape extends PositionedElement {
  static override properties = {
    ...PositionedElement.properties,
    kind: { reflect: true }, fillToken: { attribute: 'fill-token', reflect: true },
    lineToken: { attribute: 'line-token', reflect: true }, lineWidth: { type: Number, attribute: 'line-width', reflect: true },
    // v2 fidelity attributes:
    fill: { reflect: true }, line: { reflect: true }, lineDash: { attribute: 'line-dash', reflect: true },
    headEnd: { attribute: 'head-end', reflect: true }, tailEnd: { attribute: 'tail-end', reflect: true },
    geom: { reflect: true }, path: { reflect: true }, opacity: { type: Number, reflect: true },
    gradient: { reflect: true },
    rectRadius: { type: Number, attribute: 'rect-radius', reflect: true },
  }
  kind = 'rect'
  fillToken = 'transparent'
  lineToken = 'transparent'
  lineWidth = 0
  fill = ''
  line = ''
  lineDash: 'solid' | 'dash' | 'dot' | '' = ''
  headEnd: 'none' | 'arrow' | 'triangle' | '' = ''
  tailEnd: 'none' | 'arrow' | 'triangle' | '' = ''
  geom = ''
  path = ''
  opacity = NaN
  gradient = ''
  rectRadius = 0

  private readonly gradId = `ast-grad-${gradientSeq++}`

  private paintServerId(): string {
    const slide = this.closest?.('ast-slide')
    const slideId = slide?.id ? String(slide.id).replace(/[^A-Za-z0-9_-]/g, '') : ''
    return slideId ? `${this.gradId}-${slideId}` : this.gradId
  }
  private cachedGradient: Gradient | null = null
  private gradientCached = false

  /** Whether the shape needs an inline SVG rather than the CSS box fallback. */
  private usesVector(): boolean {
    const geom = this.geom || this.kind
    // roundRect is painted as a CSS border-radius so the corner stays circular.
    // An SVG path (or <rect rx>) under preserveAspectRatio="none" is stretched
    // to the element box and turns wide cards into pills.
    if (geom === 'roundRect' && !this.path) return false
    return Boolean(this.geom || this.path || this.parseGradient() ||
      this.isRawColor(this.fill) || this.isRawColor(this.line) ||
      this.headEnd === 'arrow' || this.headEnd === 'triangle' ||
      this.tailEnd === 'arrow' || this.tailEnd === 'triangle')
  }

  private isRawColor(value: string): boolean {
    return value !== '' && value !== 'transparent'
  }

  /**
   * Reads a gradient definition from the `gradient` attr JSON or a nested
   * `<script type="application/json">` child. The result is cached because Lit
   * renders into the light DOM and clears children (including the script) after
   * the first render.
   */
  private parseGradient(): Gradient | null {
    if (this.gradientCached) return this.cachedGradient
    this.gradientCached = true
    let raw = this.gradient
    if (!raw) {
      const script = this.querySelector('script[type="application/json"]')
      if (script?.textContent) raw = script.textContent
    }
    if (!raw) { this.cachedGradient = null; return null }
    try {
      const data = JSON.parse(raw) as Partial<Gradient>
      if (!data || !Array.isArray(data.stops)) { this.cachedGradient = null; return null }
      this.cachedGradient = {
        kind: data.kind === 'radial' ? 'radial' : 'linear',
        angle: typeof data.angle === 'number' ? data.angle : 0,
        cx: typeof data.cx === 'number' ? data.cx : 0,
        cy: typeof data.cy === 'number' ? data.cy : 0,
        stops: data.stops.map(s => ({ pos: Number(s.pos) || 0, color: String(s.color ?? '') })),
      }
      return this.cachedGradient
    } catch {
      this.cachedGradient = null
      return null
    }
  }

  /** Resolves a paint value: gradient url, raw color, or token var fallback. */
  private paint(raw: string, token: string, hasGradient: boolean): string {
    if (hasGradient) return `url(#${this.paintServerId()})`
    if (this.isRawColor(raw)) return raw
    return `var(--ast-${token}, transparent)`
  }

  private dashArray(): string {
    if (this.lineDash === 'dash') return '12 8'
    if (this.lineDash === 'dot') return '2 6'
    return ''
  }

  private renderGradientDef(g: Gradient): TemplateResult {
    const stops = g.stops.map(s => svg`<stop offset="${s.pos}%" stop-color="${s.color}"></stop>`)
    if (g.kind === 'radial') {
      const cx = g.cx && g.cx > 0 ? g.cx : 80
      const cy = g.cy && g.cy > 0 ? g.cy : 8
      return svg`<radialGradient id="${this.paintServerId()}" cx="${cx}%" cy="${cy}%" r="72%">${stops}</radialGradient>`
    }
    const rad = (g.angle * Math.PI) / 180
    const x2 = (Math.cos(rad) * 0.5 + 0.5).toFixed(4)
    const y2 = (Math.sin(rad) * 0.5 + 0.5).toFixed(4)
    const x1 = (1 - Number(x2)).toFixed(4)
    const y1 = (1 - Number(y2)).toFixed(4)
    return svg`<linearGradient id="${this.paintServerId()}" x1="${x1}" y1="${y1}" x2="${x2}" y2="${y2}">${stops}</linearGradient>`
  }

  private markerId(suffix: string): string { return `${this.gradId}-${suffix}` }

  private renderMarker(id: string, shape: 'arrow' | 'triangle', color: string): TemplateResult {
    const path = shape === 'triangle' ? svg`<path d="M0 0 L10 5 L0 10 Z" fill="${color}"></path>`
      : svg`<path d="M0 0 L10 5 L0 10" fill="none" stroke="${color}" stroke-width="2"></path>`
    return svg`<marker id="${id}" markerWidth="10" markerHeight="10" refX="5" refY="5" orient="auto" markerUnits="userSpaceOnUse">${path}</marker>`
  }

  protected override render() {
    if (!this.usesVector()) return noChange
    const w = this.w || 100
    const h = this.h || 100
    const gradient = this.parseGradient()
    const fill = this.paint(this.fill, this.fillToken, Boolean(gradient))
    const stroke = this.paint(this.line, this.lineToken, false)
    const strokeWidth = this.lineWidth || (this.isRawColor(this.line) ? 1 : 0)
    const dash = this.dashArray()
    const preset = GEOM_PATHS[this.geom || this.kind] ?? GEOM_PATHS.rect
    const d = this.path || preset(w, h, this.rectRadius)
    const isOpenPath = (this.geom || this.kind) === 'line'
    const useHead = this.headEnd === 'arrow' || this.headEnd === 'triangle'
    const useTail = this.tailEnd === 'arrow' || this.tailEnd === 'triangle'
    const markerColor = this.isRawColor(this.line) ? this.line : `var(--ast-${this.lineToken}, #000)`

    const defs: TemplateResult[] = []
    if (gradient) defs.push(this.renderGradientDef(gradient))
    if (useHead) defs.push(this.renderMarker(this.markerId('head'), this.headEnd as 'arrow' | 'triangle', markerColor))
    if (useTail) defs.push(this.renderMarker(this.markerId('tail'), this.tailEnd as 'arrow' | 'triangle', markerColor))

    return html`<svg
      viewBox="0 0 ${w} ${h}"
      preserveAspectRatio="none"
      style="width:100%;height:100%;display:block;overflow:visible"
    >
      ${defs.length ? svg`<defs>${defs}</defs>` : ''}
      <path
        d="${d}"
        fill="${isOpenPath ? 'none' : fill}"
        stroke="${stroke}"
        stroke-width="${strokeWidth}"
        stroke-dasharray="${dash}"
        marker-start="${useTail ? `url(#${this.markerId('tail')})` : ''}"
        marker-end="${useHead ? `url(#${this.markerId('head')})` : ''}"
      ></path>
    </svg>`
  }

  protected override updated(): void {
    super.updated()
    this.dataset.kind = this.kind
    if (!Number.isNaN(this.opacity)) this.style.opacity = String(this.opacity)
    else this.style.opacity = ''
    if (this.usesVector()) {
      // The inline SVG carries all paint; clear the CSS box fallback.
      this.style.background = ''
      this.style.border = ''
      this.style.borderRadius = ''
      return
    }
    const exportedBorderColor = this.exportedBorder.replace(/^\d+(?:\.\d+)?px\s+\w+\s+/, '')
    const exportedBorderWidth = Number.parseFloat(this.exportedBorder) || 0
    const gradient = this.parseGradient()
    if (gradient) {
      this.style.background = cssGradient(gradient)
    } else if (this.isRawColor(this.fill)) {
      this.style.background = this.fill
    } else if (this.fillToken && this.fillToken !== 'transparent') {
      this.style.background = `var(--ast-${this.fillToken}, transparent)`
    } else if (this.exportedBackground) {
      this.style.background = this.exportedBackground
    } else {
      this.style.background = `var(--ast-${this.fillToken}, transparent)`
    }
    const stroke = this.isRawColor(this.line)
      ? this.line
      : (this.lineToken && this.lineToken !== 'transparent'
        ? `var(--ast-${this.lineToken}, transparent)`
        : (exportedBorderColor !== this.exportedBorder ? exportedBorderColor : ''))
    const width = this.lineWidth || (this.isRawColor(this.line) ? 1 : exportedBorderWidth)
    this.style.border = width && stroke ? `${width}px solid ${stroke}` : ''
    if (this.lineDash === 'dash') this.style.borderStyle = 'dashed'
    else if (this.lineDash === 'dot') this.style.borderStyle = 'dotted'
    else if (width && stroke) this.style.borderStyle = 'solid'
    const rounded = (this.geom || this.kind) === 'roundRect' || this.exportedRadius > 0
    const radius = rounded ? roundRectRadius(this.w, this.h, this.rectRadius || this.exportedRadius) : 0
    this.style.borderRadius = radius ? `${radius}px` : ''
  }

  private exportedBackground = ''
  private exportedBorder = ''
  private exportedRadius = 0

  override connectedCallback(): void {
    this.captureExportedPaint()
    super.connectedCallback()
  }

  /** Present HTML paints the card on style= and omits fill=. Capture it before Lit clears style. */
  private captureExportedPaint(): void {
    const raw = this.getAttribute('style') || ''
    const pick = (name: string) => {
      const m = raw.match(new RegExp(`(?:^|;)\\s*${name}\\s*:\\s*([^;]+)`, 'i'))
      return m ? m[1].trim() : ''
    }
    this.exportedBackground = pick('background') || pick('background-color')
    this.exportedBorder = pick('border')
    this.exportedRadius = Number.parseFloat(pick('border-radius')) || 0
  }
}
