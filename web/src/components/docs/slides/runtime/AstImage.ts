import { html } from 'lit'

import { PositionedElement } from './base'

export class AstImage extends PositionedElement {
  static override properties = {
    ...PositionedElement.properties,
    src: { reflect: true }, alt: { reflect: true }, fit: { reflect: true },
  }
  src = ''
  alt = ''
  fit: 'contain' | 'cover' | 'fill' | 'none' | 'scale-down' = 'contain'

  protected override render() {
    return html`<img src=${this.src} alt=${this.alt} style=${`width:100%;height:100%;object-fit:${this.fit}`} />`
  }
}
