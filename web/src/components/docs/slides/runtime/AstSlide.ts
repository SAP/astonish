import { LitElement, html } from 'lit'

export class AstSlide extends LitElement {
  static override properties = { active: { type: Boolean, reflect: true } }
  active = false

  protected override createRenderRoot(): HTMLElement | DocumentFragment { return this }
  protected override render() { return html`<slot></slot>` }
}
