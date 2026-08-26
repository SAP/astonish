import { LitElement, html } from 'lit'

export class AstNotes extends LitElement {
  protected override createRenderRoot(): HTMLElement | DocumentFragment { return this }
  protected override render() { return html`<slot></slot>` }
}
