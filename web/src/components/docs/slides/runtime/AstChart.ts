import { html } from 'lit'

import { PositionedElement } from './base'

export class AstChart extends PositionedElement {
  protected override render() { return html`<slot></slot>` }
}
