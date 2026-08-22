import { PositionedElement } from './base'

export class AstShape extends PositionedElement {
  static override properties = {
    ...PositionedElement.properties,
    kind: { reflect: true }, fillToken: { attribute: 'fill-token', reflect: true },
    lineToken: { attribute: 'line-token', reflect: true }, lineWidth: { type: Number, attribute: 'line-width', reflect: true },
  }
  kind = 'rect'
  fillToken = 'transparent'
  lineToken = 'transparent'
  lineWidth = 0

  protected override updated(): void {
    super.updated()
    this.dataset.kind = this.kind
    this.style.background = `var(--ast-${this.fillToken}, transparent)`
    this.style.border = `${this.lineWidth}px solid var(--ast-${this.lineToken}, transparent)`
    this.style.borderRadius = this.kind === 'roundRect' ? '24px' : ''
  }
}
