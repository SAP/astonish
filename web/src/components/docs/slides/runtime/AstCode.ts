import { html } from 'lit'

import { PositionedElement } from './base'

export class AstCode extends PositionedElement {
  protected override render() { return html`<pre><code><slot></slot></code></pre>` }
}
