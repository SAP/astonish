import { LitElement, html } from 'lit'

export class AstFragment extends LitElement {
  static override properties = {
    order: { type: Number, reflect: true },
    revealed: { type: Boolean, reflect: true },
  }
  order = 0
  revealed = false

  protected override createRenderRoot(): HTMLElement | DocumentFragment { return this }
  protected override render() { return html`<slot></slot>` }
}
