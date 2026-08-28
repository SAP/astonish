import { html, noChange, type TemplateResult } from 'lit'

import { PositionedElement } from './base'

// Maps the ASD `align` attribute — authored as `l | ctr | r` (and tolerating
// the CSS full words) — to a valid CSS `text-align` value. Without this a raw
// `ctr` reaches `text-align:ctr`, which is invalid CSS and silently falls back
// to left, so titles authored with align="ctr" appeared left-aligned. The PPTX
// exporter performs the equivalent mapping in worker.mjs (alignMap).
export function alignToCSS(align: string): string {
  switch (align) {
    case 'ctr':
    case 'c':
    case 'center':
    case 'centre':
      return 'center'
    case 'r':
    case 'right':
      return 'right'
    case 'justify':
    case 'justified':
      return 'justify'
    case 'l':
    case 'left':
    default:
      return 'left'
  }
}

// Normalizes a CSS `font-family` value so brand families are actually applied
// instead of being silently dropped. Per the CSS grammar an UNQUOTED family is
// a sequence of identifiers, and a CSS identifier may NOT start with a digit —
// so an imported brand family like `72 Brand` (as in `72 Brand, Aptos, ...`)
// is INVALID unquoted and the whole declaration is discarded, falling back to
// the default serif (Times). We quote each comma-separated family that needs it
// (starts with a digit, or contains a character outside [A-Za-z0-9 _-]) and
// leave generic keywords and already-quoted names untouched. Mirrors the PPTX
// importer's cssFontFamilyName (import_worker.mjs) so live and exported render
// identically.
const CSS_GENERIC = /^(sans-serif|serif|monospace|cursive|fantasy|system-ui|ui-serif|ui-sans-serif|ui-monospace|ui-rounded|inherit|initial|unset|revert)$/i
export function cssFontFamily(value: string): string {
  if (!value) return value
  return value
    .split(',')
    .map(part => {
      const p = part.trim()
      if (!p) return ''
      if (/^["']/.test(p)) return p // already quoted
      if (CSS_GENERIC.test(p)) return p
      const needsQuote = /(^|\s)\d/.test(p) || /[^A-Za-z0-9 _-]/.test(p)
      if (!needsQuote) return p
      if (p.includes('"')) return p // defensive: leave malformed names alone
      return `"${p}"`
    })
    .filter(Boolean)
    .join(', ')
}

type Run = {
  text: string
  bold: boolean
  italic: boolean
  underline: boolean
  color: string
  font: string
  size: number
  weight: string
}

export class AstText extends PositionedElement {
  static override properties = {
    ...PositionedElement.properties,
    fontToken: { attribute: 'font-token', reflect: true },
    size: { type: Number, reflect: true }, weight: { reflect: true }, align: { reflect: true },
    inset: { type: Number, reflect: true }, colorToken: { attribute: 'color-token', reflect: true },
    // v2 fidelity attributes:
    color: { reflect: true }, font: { reflect: true }, anchor: { reflect: true },
  }
  fontToken = 'body-font'
  size = 32
  weight = '400'
  align = 'left'
  inset = 0
  colorToken = 'ink'
  color = ''
  font = ''
  anchor = ''

  private runs: Run[] | null = null
  private runsRead = false

  /**
   * Snapshots any `<ast-run>` light-DOM children into styled runs. Cached
   * because Lit renders into the light DOM and clears children after the first
   * render.
   */
  private readRuns(): Run[] {
    if (this.runsRead) return this.runs ?? []
    this.runsRead = true
    const els = Array.from(this.querySelectorAll(':scope > ast-run'))
    if (els.length === 0) { this.runs = null; return [] }
    this.runs = els.map(el => {
      const sizeAttr = el.getAttribute('size')
      return {
        text: el.textContent ?? '',
        bold: el.hasAttribute('b'),
        italic: el.hasAttribute('i'),
        underline: el.hasAttribute('u'),
        color: el.getAttribute('color') ?? '',
        font: el.getAttribute('font') ?? '',
        size: sizeAttr ? Number(sizeAttr) : NaN,
        weight: el.getAttribute('weight') ?? '',
      }
    })
    return this.runs
  }

  private runStyle(run: Run): string {
    const parts: string[] = []
    if (run.bold) parts.push('font-weight:700')
    else if (run.weight) parts.push(`font-weight:${run.weight}`)
    if (run.italic) parts.push('font-style:italic')
    if (run.underline) parts.push('text-decoration:underline')
    if (run.color) parts.push(`color:${run.color}`)
    if (run.font) parts.push(`font-family:${cssFontFamily(run.font)}`)
    if (!Number.isNaN(run.size)) parts.push(`font-size:${run.size}px`)
    return parts.join(';')
  }

  protected override render(): unknown {
    const runs = this.readRuns()
    if (runs.length === 0) return noChange
    const spans: TemplateResult[] = runs.map(run => html`<span style="${this.runStyle(run)}">${run.text}</span>`)
    return html`${spans}`
  }

  protected override updated(): void {
    super.updated()
    this.style.fontFamily = this.font ? cssFontFamily(this.font) : `var(--ast-${this.fontToken}, Aptos, Arial, sans-serif)`
    this.style.fontSize = `${this.size}px`
    this.style.fontWeight = this.weight
    this.style.textAlign = alignToCSS(this.align)
    this.style.padding = `${this.inset}px`
    this.style.color = this.color ? this.color : `var(--ast-${this.colorToken}, currentColor)`
    // Preserve authored newlines between/inside ast-run spans (e.g. blank-line
    // separators between bullet items) and wrap long words. Mirrors the
    // ast-text{white-space:pre-wrap} rule in the HTML export.
    this.style.whiteSpace = 'pre-wrap'
    this.style.overflowWrap = 'break-word'
    this.style.fontVariantLigatures = 'none'
    // Clip horizontally (long tokens) but never shave glyph descenders.
    this.style.overflowX = 'clip'
    this.style.overflowY = 'visible'
    if (this.anchor === 'ctr') {
      this.style.display = 'flex'
      this.style.flexDirection = 'column'
      this.style.justifyContent = 'center'
    } else if (this.anchor === 'b') {
      this.style.display = 'flex'
      this.style.flexDirection = 'column'
      this.style.justifyContent = 'flex-end'
    } else {
      this.style.display = ''
      this.style.flexDirection = ''
      this.style.justifyContent = ''
    }
  }
}
