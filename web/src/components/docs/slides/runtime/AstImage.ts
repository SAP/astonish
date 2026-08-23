import { html } from 'lit'

import { PositionedElement } from './base'

export class AstImage extends PositionedElement {
  static override properties = {
    ...PositionedElement.properties,
    src: { reflect: true }, alt: { reflect: true }, fit: { reflect: true },
    // v2 fidelity attributes:
    opacity: { type: Number, reflect: true },
  }
  src = ''
  alt = ''
  fit: 'contain' | 'cover' | 'fill' | 'none' | 'scale-down' = 'contain'
  opacity = NaN

  protected override updated(): void {
    super.updated()
    this.style.opacity = Number.isNaN(this.opacity) ? '' : String(this.opacity)
  }

  protected override render() {
    return html`<img src=${this.src} alt=${this.alt} style=${`width:100%;height:100%;object-fit:${this.fit}`} />`
  }
}
