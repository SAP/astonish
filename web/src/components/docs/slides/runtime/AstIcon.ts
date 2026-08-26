import { html } from 'lit'

import { PositionedElement } from './base'

export class AstIcon extends PositionedElement {
  protected override render() { return html`<span role="img" aria-label=${this.getAttribute('alt') || ''}><slot></slot></span>` }
}
