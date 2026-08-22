import { PositionedElement } from './base'

export class AstText extends PositionedElement {
  static override properties = {
    ...PositionedElement.properties,
    fontToken: { attribute: 'font-token', reflect: true },
    size: { type: Number, reflect: true }, weight: { reflect: true }, align: { reflect: true },
    inset: { type: Number, reflect: true }, colorToken: { attribute: 'color-token', reflect: true },
  }
  fontToken = 'body-font'
  size = 32
  weight = '400'
  align = 'left'
  inset = 0
  colorToken = 'ink'

  protected override updated(): void {
    super.updated()
    this.style.fontFamily = `var(--ast-${this.fontToken}, Aptos, Arial, sans-serif)`
    this.style.fontSize = `${this.size}px`
    this.style.fontWeight = this.weight
    this.style.textAlign = this.align
    this.style.padding = `${this.inset}px`
    this.style.color = `var(--ast-${this.colorToken}, currentColor)`
  }
}
