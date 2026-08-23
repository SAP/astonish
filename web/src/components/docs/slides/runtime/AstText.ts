import { html, noChange, type TemplateResult } from 'lit'

import { PositionedElement } from './base'

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
    if (run.font) parts.push(`font-family:${run.font}`)
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
    this.style.fontFamily = this.font ? this.font : `var(--ast-${this.fontToken}, Aptos, Arial, sans-serif)`
    this.style.fontSize = `${this.size}px`
    this.style.fontWeight = this.weight
    this.style.textAlign = this.align
    this.style.padding = `${this.inset}px`
    this.style.color = this.color ? this.color : `var(--ast-${this.colorToken}, currentColor)`
    // Preserve authored newlines between/inside ast-run spans (e.g. blank-line
    // separators between bullet items) and wrap long words. Mirrors the
    // ast-text{white-space:pre-wrap} rule in the HTML export.
    this.style.whiteSpace = 'pre-wrap'
    this.style.overflowWrap = 'break-word'
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
